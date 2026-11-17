import { cpSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { build } from 'vite'
import { test as applicationTest, expect } from './auth-fixture.ts'
import { provision, writeContext } from './domain-helpers.ts'

const test = applicationTest.extend<{}, { applicationWebDirectory: string }>({
  applicationWebDirectory: [async ({ browserName }, provideFixture) => {
    const directory = mkdtempSync(join(tmpdir(), `homebox-sync-status-sdk-${browserName}-`))
    const web = join(directory, 'web'), sdk = join(directory, 'sdk'), entry = join(directory, 'entry.ts')
    try {
      cpSync(fileURLToPath(new URL('../../dist', import.meta.url)), web, { recursive: true })
      // This SDK bundle is private to the browser fixture, not a production entrypoint.
      writeFileSync(entry, `export * from ${JSON.stringify(fileURLToPath(new URL('../../src/lib/api/sync-status.ts', import.meta.url)))};\n`)
      await build({ configFile: false, logLevel: 'silent', build: { outDir: sdk, minify: false, lib: { entry, formats: ['es'], fileName: 'sync-status-sdk' } } })
      cpSync(join(sdk, 'sync-status-sdk.js'), join(web, 'assets', 'sync-status-sdk.js'))
      await provideFixture(web)
    } finally { rmSync(directory, { recursive: true, force: true }) }
  }, { scope: 'worker' }],
})

test.use({ trace: 'off', screenshot: 'off', video: 'off' })

test('reads actual materialized status without enabling sync or replaying reads', async ({ page, context, application }) => {
  const requests: string[] = [], replies: { status: number; cache: string | undefined; etag: string | undefined }[] = []
  page.on('request', (request) => {
    if (new URL(request.url()).pathname === '/api/v1/sync/status') requests.push(request.method())
  })
  page.on('response', (response) => {
    if (new URL(response.url()).pathname === '/api/v1/sync/status') {
      const headers = response.headers()
      replies.push({ status: response.status(), cache: headers['cache-control'], etag: headers.etag })
    }
  })
  await page.goto('/')
  const read = () => page.evaluate(async () => {
    const modulePath = '/assets/sync-status-sdk.js', api = await import(modulePath)
    try { return await api.fetchSyncStatus(new AbortController().signal) }
    catch (error) { return { code: error instanceof Error && 'code' in error ? error.code : 'unexpected' } }
  })
  expect(await read()).toEqual({ code: 'AUTHENTICATION_REQUIRED' })
  await provision(page, application)
  const initial = await read()
  expect(initial).toMatchObject({ projection_version: 1, min_retained_seq: 0, high_watermark: 0, counts: {},
    counts_scope: 'materialized_projections', last_change_at: null, retention_days: 30, bootstrap_ready: false, retention_enabled: false })
  expect(initial.server_instance_id).toMatch(/^[0-9a-f]{32}$/)
  const created = await page.request.post('/api/v1/locations', { headers: await writeContext(page, application.url), data: { name: 'Status fixture', code: 'SYNC-STATUS' } })
  expect(created.status()).toBe(200)
  const updated = await read()
  expect(updated).toMatchObject({ dataset_epoch: initial.dataset_epoch, server_instance_id: initial.server_instance_id,
    high_watermark: 1, min_retained_seq: 0, counts: { location: 1 }, bootstrap_ready: false, retention_enabled: false })
  expect(updated.last_change_at).toMatch(/^\d{4}-\d\d-\d\dT/)
  const poisonedTop = JSON.parse(JSON.stringify(updated))
  Object.defineProperty(poisonedTop, '__proto__', { value: 'private', enumerable: true })
  const poisonedCounts = { ...updated, counts: JSON.parse('{"__proto__":1}') }
  const statusRoute = (url: URL) => url.pathname === '/api/v1/sync/status'
  for (const poisoned of [poisonedTop, poisonedCounts]) {
    await page.route(statusRoute, async (route) => {
      await route.fulfill({ status: 200, body: JSON.stringify(poisoned), headers: { 'Content-Type': 'application/json', 'Cache-Control': 'no-store' } })
    })
    expect(await read()).toEqual({ code: 'INVALID_RESPONSE' })
    await page.unroute(statusRoute)
  }
  for (const path of ['/api/v1/sync/bootstrap', '/api/v1/sync/changes?after=opaque']) {
    expect((await page.request.get(path, { headers: { Origin: application.url } })).status()).toBe(404)
  }
  const reads = requests.length
  await context.setOffline(true)
  expect(await read()).toEqual({ code: 'NETWORK_ERROR' })
  await context.setOffline(false)
  await page.evaluate(() => window.dispatchEvent(new Event('online')))
  expect(requests).toHaveLength(reads + 1)
  const headers = await writeContext(page, application.url)
  expect((await page.request.post('/api/v1/auth/logout', { headers })).status()).toBe(200)
  expect(await read()).toEqual({ code: 'AUTHENTICATION_REQUIRED' })
  expect(requests).toHaveLength(reads + 2)
  expect(requests.every((method) => method === 'GET')).toBe(true)
  expect(replies[0]).toEqual({ status: 401, cache: 'no-store', etag: undefined })
  expect(replies.slice(1, -1).every((reply) => reply.status === 200 && reply.cache === 'no-store' && reply.etag === undefined)).toBe(true)
  expect(replies.at(-1)).toEqual({ status: 401, cache: 'no-store', etag: undefined })
})
