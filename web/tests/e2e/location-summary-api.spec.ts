import { cpSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { build } from 'vite'
import { test as applicationTest, expect } from './auth-fixture.ts'
import { provision, writeContext } from './domain-helpers.ts'

const test = applicationTest.extend<{}, { applicationWebDirectory: string }>({
  applicationWebDirectory: [async ({ browserName }, provideFixture) => {
    const directory = mkdtempSync(join(tmpdir(), `homebox-location-summary-sdk-${browserName}-`))
    const web = join(directory, 'web'), sdk = join(directory, 'sdk'), entry = join(directory, 'entry.ts')
    try {
      cpSync(fileURLToPath(new URL('../../dist', import.meta.url)), web, { recursive: true })
      // The standalone SDK bundle exists only in this disposable browser fixture.
      writeFileSync(entry, `export * from ${JSON.stringify(fileURLToPath(new URL('../../src/lib/api/locations.ts', import.meta.url)))};\n`)
      await build({ configFile: false, logLevel: 'silent', build: { outDir: sdk, minify: false, lib: { entry, formats: ['es'], fileName: 'location-summary-sdk' } } })
      cpSync(join(sdk, 'location-summary-sdk.js'), join(web, 'assets', 'location-summary-sdk.js'))
      await provideFixture(web)
    } finally { rmSync(directory, { recursive: true, force: true }) }
  }, { scope: 'worker' }],
})

test.use({ trace: 'off', screenshot: 'off', video: 'off' })

test('reads actual direct/subtree holdings, rejects altered replies, and never replays offline', async ({ page, context, application }) => {
  const requests: string[] = []
  page.on('request', (request) => { if (/^\/api\/v1\/locations\/[^/]+\/summary$/.test(new URL(request.url()).pathname)) requests.push(request.method()) })
  await page.goto('/')
  const read = (id: string) => page.evaluate(async (locationID) => {
    const modulePath = '/assets/location-summary-sdk.js', api = await import(modulePath)
    try { return await api.fetchLocationSummary(locationID, new AbortController().signal) }
    catch (error) { return { code: error instanceof Error && 'code' in error ? error.code : 'unexpected' } }
  }, id)
  expect(await read('missing')).toEqual({ code: 'AUTHENTICATION_REQUIRED' })
  await provision(page, application)
  async function post(path: string, data: object, revision?: number) {
    const response = await page.request.post(path, { headers: await writeContext(page, application.url, revision), data })
    expect(response.status(), path).toBe(200)
    return response.json() as Promise<{ id: string; revision: number }>
  }
  const root = await post('/api/v1/locations', { name: 'Root', code: 'SUMMARY-ROOT' })
  const child = await post('/api/v1/locations', { name: 'Child', code: 'SUMMARY-CHILD', parent_id: root.id }, root.revision)
  const mass = await post('/api/v1/items', { name: 'Mass', tracking_mode: 'quantity', unit_code: 'kg', base_unit: 'g', scale_factor: 1000 })
  await post('/api/v1/stock/receive', { item_id: mass.id, target_location_id: root.id, quantity_base: 400 }, mass.revision)
  await post('/api/v1/stock/receive', { item_id: mass.id, target_location_id: child.id, quantity_base: 1100 }, mass.revision + 1)
  const liquid = await post('/api/v1/items', { name: 'Liquid', tracking_mode: 'quantity', unit_code: 'l', base_unit: 'ml', scale_factor: 1000 })
  await post('/api/v1/stock/receive', { item_id: liquid.id, target_location_id: child.id, quantity_base: 1200 }, liquid.revision)
  const owner = await post('/api/v1/items', { name: 'Individual' })
  await post(`/api/v1/items/${owner.id}/units`, { location_id: root.id }, owner.revision)
  const disposed = await post(`/api/v1/items/${owner.id}/units`, { location_id: child.id }, owner.revision + 1)
  await post(`/api/v1/units/${disposed.id}/status`, { status: 'disposed', disposal_kind: 'recycled' }, disposed.revision)
  const before = await read(root.id)
  expect(before).toMatchObject({ value: { location_id: root.id, direct_units: 1, subtree_units: 1, distinct_item_count: 3,
    direct_quantities: [{ base_unit: 'g', quantity_base: 400 }],
    subtree_quantities: [{ base_unit: 'g', quantity_base: 1500 }, { base_unit: 'ml', quantity_base: 1200 }] } })
  const location = await page.request.get(`/api/v1/locations/${root.id}`)
  expect(location.status()).toBe(200)
  expect(before.revision).toBe((await location.json()).revision)
  const childCurrent = await page.request.get(`/api/v1/locations/${child.id}`)
  const childRevision = (await childCurrent.json()).revision as number
  await post(`/api/v1/locations/${child.id}/move`, { target_parent_id: null }, childRevision)
  const after = await read(root.id)
  expect(after.revision).toBeGreaterThan(before.revision)
  expect(after.value).toEqual({ location_id: root.id, direct_units: 1, subtree_units: 1, distinct_item_count: 2,
    direct_quantities: [{ base_unit: 'g', quantity_base: 400 }], subtree_quantities: [{ base_unit: 'g', quantity_base: 400 }] })
  expect(await read('missing')).toEqual({ code: 'NOT_FOUND' })
  const exactPath = (url: URL) => url.pathname === `/api/v1/locations/${root.id}/summary`
  for (const body of [{ ...after.value, location_id: 'other' }, { ...after.value, direct_units: 2 },
    JSON.parse(JSON.stringify(after.value).replace(/}$/, ',"__proto__":{"private":true}}'))]) {
    await page.route(exactPath, (route) => route.fulfill({ status: 200, body: JSON.stringify(body), headers: { 'Content-Type': 'application/json', 'Cache-Control': 'no-store', ETag: `"${after.revision}"` } }))
    expect(await read(root.id)).toEqual({ code: 'INVALID_RESPONSE' })
    await page.unroute(exactPath)
  }
  await page.route(exactPath, (route) => route.fulfill({ status: 200, body: JSON.stringify(after.value), headers: { 'Content-Type': 'application/json', 'Cache-Control': 'no-store', ETag: 'W/"1"' } }))
  expect(await read(root.id)).toEqual({ code: 'INVALID_RESPONSE' })
  await page.unroute(exactPath)
  const count = requests.length
  await context.setOffline(true)
  expect(await read(root.id)).toEqual({ code: 'NETWORK_ERROR' })
  await context.setOffline(false)
  await page.evaluate(() => window.dispatchEvent(new Event('online')))
  expect(requests).toHaveLength(count + 1)
  expect((await page.request.post('/api/v1/auth/logout', { headers: await writeContext(page, application.url) })).status()).toBe(200)
  expect(await read(root.id)).toEqual({ code: 'AUTHENTICATION_REQUIRED' })
  expect(requests).toHaveLength(count + 2)
  expect(requests.every((method) => method === 'GET')).toBe(true)
})
