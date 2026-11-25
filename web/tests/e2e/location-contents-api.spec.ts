import { cpSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { DatabaseSync } from 'node:sqlite'
import { fileURLToPath } from 'node:url'
import { build } from 'vite'
import { test as applicationTest, expect } from './auth-fixture.ts'
import { provision, writeContext } from './domain-helpers.ts'

const test = applicationTest.extend<{}, { applicationWebDirectory: string }>({
  applicationWebDirectory: [async ({ browserName }, provideFixture) => {
    const directory = mkdtempSync(join(tmpdir(), `homebox-location-contents-sdk-${browserName}-`))
    const web = join(directory, 'web'), sdk = join(directory, 'sdk'), entry = join(directory, 'entry.ts')
    try {
      cpSync(fileURLToPath(new URL('../../dist', import.meta.url)), web, { recursive: true })
      // This SDK entry exists only in the disposable browser fixture.
      writeFileSync(entry, `export * from ${JSON.stringify(fileURLToPath(new URL('../../src/lib/api/locations.ts', import.meta.url)))};\n`)
      await build({ configFile: false, logLevel: 'silent', build: { outDir: sdk, minify: false, lib: { entry, formats: ['es'], fileName: 'location-contents-sdk' } } })
      cpSync(join(sdk, 'location-contents-sdk.js'), join(web, 'assets', 'location-contents-sdk.js'))
      await provideFixture(web)
    } finally { rmSync(directory, { recursive: true, force: true }) }
  }, { scope: 'worker' }],
})

test.use({ trace: 'off', screenshot: 'off', video: 'off' })

test('reads real scoped pages, rejects changed revisions and never replays while offline', async ({ page, context, application }) => {
  await provision(page, application)
  async function post(path: string, data: object, revision?: number) {
    const response = await page.request.post(path, { headers: await writeContext(page, application.url, revision), data })
    expect(response.status(), path).toBe(200)
    return response.json() as Promise<{ id: string; revision: number }>
  }
  const root = await post('/api/v1/locations', { name: 'Contents root', code: 'CONTENTS-ROOT' })
  const child = await post('/api/v1/locations', { name: 'Contents child', code: 'CONTENTS-CHILD', parent_id: root.id }, root.revision)
  const individual = await post('/api/v1/items', { name: 'Individual contents' })
  const unit = await post(`/api/v1/items/${individual.id}/units`, { location_id: root.id }, individual.revision)
  const quantity = await post('/api/v1/items', { name: 'Quantity contents', tracking_mode: 'quantity' })
  await post('/api/v1/stock/receive', { item_id: quantity.id, target_location_id: child.id, quantity_base: 5 }, quantity.revision)
  const methods: string[] = []
  page.on('request', (request) => { if (new URL(request.url()).pathname === `/api/v1/locations/${root.id}/contents`) methods.push(request.method()) })
  await page.goto('/')
  const read = (query: object) => page.evaluate(async ({ id, values }) => {
    const modulePath = '/assets/location-contents-sdk.js', api = await import(modulePath)
    try { return await api.fetchLocationContents(id, values, new AbortController().signal) }
    catch (error) { return { code: error instanceof Error && 'code' in error ? error.code : 'unexpected' } }
  }, { id: root.id, values: query })
  const first = await read({ limit: 1 })
  expect(first.value).toMatchObject({ location_id: root.id, scope: 'subtree', units: [{ id: unit.id }], stock: [], sub_locations: [] })
  expect(first.value.next_cursor).toBeTruthy()
  const second = await read({ limit: 1, cursor: first.value.next_cursor })
  expect(second.revision).toBe(first.revision)
  expect(second.value.stock).toMatchObject([{ item_id: quantity.id, location_id: child.id, quantity_base: 5 }])
  const third = await read({ limit: 1, cursor: second.value.next_cursor })
  expect(third.value).toMatchObject({ units: [], stock: [], sub_locations: [{ id: child.id }], next_cursor: null })
  expect(await read({ scope: 'direct', limit: 1, cursor: first.value.next_cursor })).toEqual({ code: 'INVALID_CURSOR' })
  expect(await read({ limit: 2, cursor: first.value.next_cursor })).toEqual({ code: 'INVALID_CURSOR' })
  const direct = await read({ scope: 'direct' })
  expect(direct.value).toMatchObject({ scope: 'direct', units: [{ id: unit.id }], stock: [], sub_locations: [{ id: child.id }], next_cursor: null })

  const old = await read({ limit: 1 })
  const current = await page.request.get(`/api/v1/locations/${root.id}`)
  expect(current.status()).toBe(200)
  await post('/api/v1/locations', { name: 'Later child', code: 'CONTENTS-LATER', parent_id: root.id }, (await current.json()).revision)
  expect(await read({ limit: 1, cursor: old.value.next_cursor })).toEqual({ code: 'INVALID_CURSOR' })
  const currentPage = await read({ limit: 1 })
  expect(currentPage.revision).toBeGreaterThan(old.revision)
  const endpoint = (url: URL) => url.pathname === `/api/v1/locations/${root.id}/contents`
  for (const value of [{ ...currentPage.value, location_id: 'other' }, { ...currentPage.value, next_cursor: '' },
    JSON.parse(JSON.stringify(currentPage.value).replace(/}$/, ',"__proto__":{"private":true}}'))]) {
    await page.route(endpoint, (route) => route.fulfill({ status: 200, json: value, headers: { ETag: `"${currentPage.revision}"` } }))
    expect(await read({ limit: 1 })).toEqual({ code: 'INVALID_RESPONSE' })
    await page.unroute(endpoint)
  }
  const before = methods.length
  await context.setOffline(true)
  expect(await read({ limit: 1 })).toEqual({ code: 'NETWORK_ERROR' })
  await context.setOffline(false)
  await page.evaluate(() => window.dispatchEvent(new Event('online')))
  expect(methods).toHaveLength(before + 1)
  expect(methods.every((method) => method === 'GET')).toBe(true)
})

const fixtureRoot = 'contents-fixture-root', legacyUnit = 'legacy/unit\0😀', legacyLocation = '\0legacy/location😀'
function prepareLargeContents(database: string) {
  // Offline fixture setup creates only retained source/counters/current payloads.
  const db = new DatabaseSync(database), now = '2026-11-06T00:00:00Z'
  try {
    db.exec('PRAGMA foreign_keys=ON; BEGIN IMMEDIATE')
    const counter = db.prepare('INSERT INTO projection_revisions(entity_type,entity_id,revision) VALUES(?,?,1)')
    const current = db.prepare('INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at) VALUES(?,?,1,?,?)')
    const baseline = (kind: string, id: string, payload: object) => { counter.run(kind, id); current.run(kind, id, JSON.stringify(payload), now) }
    const addLocation = db.prepare('INSERT INTO locations(id,parent_id,code,name,kind,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)')
    const addItem = db.prepare('INSERT INTO items(id,name,tracking_mode,created_at,updated_at) VALUES(?,?,?,?,?)')
    const addUnit = db.prepare('INSERT INTO item_units(id,item_id,location_id,asset_code,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?)')
    const addStock = db.prepare('INSERT INTO stock_balances(item_id,location_id,quantity_base,updated_at) VALUES(?,?,?,?)')
    const location = (id: string, parent: string | null, code: string) => {
      addLocation.run(id, parent, code, code, 'box', 'active', now, now)
      baseline('location', id, { id, parent_id: parent, code, name: code, kind: 'box', status: 'active', sort_order: 0, description: '' })
    }
    location(fixtureRoot, null, 'CONTENTS-LARGE')
    for (const [id, mode] of [['contents-individual', 'individual'], ['contents-quantity', 'quantity']] as const) {
      addItem.run(id, id, mode, now, now)
      baseline('item', id, { id, name: id, brand: null, model: null, category_id: null, description: '', notes: '', tracking_mode: mode, unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1, status: 'active' })
      counter.run('search_doc', JSON.stringify(['item', id]))
    }
    const locations = [legacyLocation, ...Array.from({ length: 205 }, (_, index) => `contents-child-${String(index).padStart(3, '0')}`)]
    for (const [index, id] of locations.entries()) {
      const code = `CONTENTS-${String(index).padStart(3, '0')}`, unitID = index === 0 ? legacyUnit : `unit-${String(index).padStart(3, '0')}`
      location(id, fixtureRoot, code)
      addUnit.run(unitID, 'contents-individual', id, `ASSET-${index}`, 'disposed', now, now)
      baseline('unit', unitID, { id: unitID, item_id: 'contents-individual', location_id: id, asset_code: `ASSET-${index}`, serial_number: null, notes: '', status: 'disposed' })
      addStock.run('contents-quantity', id, 0, now)
      baseline('stock', JSON.stringify(['contents-quantity', id]), { item_id: 'contents-quantity', location_id: id, quantity_base: 0, unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1 })
    }
    db.exec('COMMIT')
    expect(db.prepare('PRAGMA foreign_key_check').all()).toEqual([])
  } finally { db.close() }
}

test.describe('retained large location contents', () => {
  test.use({ applicationDatabaseFixture: { prepare: prepareLargeContents } })
  test('reads all actual mixed pages with exact legacy IDs and fresh-session continuation', async ({ page, application }) => {
    const password = await provision(page, application)
    await page.goto('/')
    const read = (cursor?: string) => page.evaluate(async ({ id, cursor }) => {
      const modulePath = '/assets/location-contents-sdk.js', api = await import(modulePath)
      return api.fetchLocationContents(id, { limit: 100, ...(cursor ? { cursor } : {}) }, new AbortController().signal)
    }, { id: fixtureRoot, cursor })
    const pages = [await read()]
    expect(pages[0].value.next_cursor).toBeTruthy()
    expect((await page.request.post('/api/v1/auth/logout', { headers: await writeContext(page, application.url) })).status()).toBe(200)
    expect((await page.request.post('/api/v1/auth/login', { headers: { Origin: application.url }, data: { username: 'owner', password } })).status()).toBe(200)
    while (pages.at(-1).value.next_cursor) {
      expect(pages.length).toBeLessThan(8)
      pages.push(await read(pages.at(-1).value.next_cursor))
    }
    expect(pages).toHaveLength(7)
    expect(pages.every((page) => page.revision === 1 && page.value.scope === 'subtree' &&
      page.value.units.length + page.value.stock.length + page.value.sub_locations.length <= 100)).toBe(true)
    const units = pages.flatMap((page) => page.value.units), stock = pages.flatMap((page) => page.value.stock), locations = pages.flatMap((page) => page.value.sub_locations)
    expect(units).toHaveLength(206); expect(stock).toHaveLength(206); expect(locations).toHaveLength(206)
    expect(new Set(units.map((row: { id: string }) => row.id)).size).toBe(206)
    expect(new Set(stock.map((row: { item_id: string; location_id: string }) => JSON.stringify([row.item_id, row.location_id]))).size).toBe(206)
    expect(new Set(locations.map((row: { id: string }) => row.id)).size).toBe(206)
    expect(units).toContainEqual(expect.objectContaining({ id: legacyUnit, location_id: legacyLocation, status: 'disposed' }))
    expect(stock).toContainEqual(expect.objectContaining({ location_id: legacyLocation, quantity_base: 0 }))
    expect(locations).toContainEqual(expect.objectContaining({ id: legacyLocation, parent_id: fixtureRoot }))
  })
})
