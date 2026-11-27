import { cpSync, lstatSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { build } from 'vite'
import { test as applicationTest, expect } from './auth-fixture.ts'
import { provision, writeContext } from './domain-helpers.ts'

const test = applicationTest.extend<{}, { applicationWebDirectory: string }>({
  applicationWebDirectory: [async ({ browserName }, provideFixture) => {
    const directory = mkdtempSync(join(tmpdir(), `homebox-system-storage-sdk-${browserName}-`))
    const web = join(directory, 'web'), sdk = join(directory, 'sdk'), entry = join(directory, 'entry.ts')
    try {
      cpSync(fileURLToPath(new URL('../../dist', import.meta.url)), web, { recursive: true })
      // This SDK bundle belongs only to the disposable browser fixture.
      writeFileSync(entry, `export * from ${JSON.stringify(fileURLToPath(new URL('../../src/lib/api/system-storage.ts', import.meta.url)))};\n`)
      await build({ configFile: false, logLevel: 'silent', build: { outDir: sdk, minify: false, lib: { entry, formats: ['es'], fileName: 'system-storage-sdk' } } })
      cpSync(join(sdk, 'system-storage-sdk.js'), join(web, 'assets', 'system-storage-sdk.js'))
      await provideFixture(web)
    } finally { rmSync(directory, { recursive: true, force: true }) }
  }, { scope: 'worker' }],
})

test.use({ trace: 'off', screenshot: 'off', video: 'off' })

test('reads the actual database filesystem without guessing unmeasured scopes or replaying', async ({ page, context, application }) => {
  const requests: string[] = []
  page.on('request', (request) => {
    if (new URL(request.url()).pathname === '/api/v1/system/storage') requests.push(request.method())
  })
  await page.goto('/')
  const read = () => page.evaluate(async () => {
    const modulePath = '/assets/system-storage-sdk.js', api = await import(modulePath)
    try { return await api.fetchSystemStorage(new AbortController().signal) }
    catch (error) { return { code: error instanceof Error && 'code' in error ? error.code : 'unexpected' } }
  })
  expect(await read()).toEqual({ code: 'AUTHENTICATION_REQUIRED' })
  await provision(page, application)
  const observed = await read()
  expect(observed).toMatchObject({ media_bytes: null, backup_bytes: null })
  expect(observed.database_bytes).toBeGreaterThanOrEqual(lstatSync(application.database).size)
  expect(Number.isSafeInteger(observed.free_bytes)).toBe(true)
  expect(observed.free_bytes).toBeGreaterThanOrEqual(0)
  expect(observed.database_filesystem_total_bytes).toBeGreaterThanOrEqual(observed.free_bytes)
  expect(observed.measured_at).toMatch(/^\d{4}-\d{2}-\d{2}T/)
  expect(JSON.stringify(observed)).not.toContain(application.database)
  expect(JSON.stringify(observed)).not.toContain(application.bootstrapToken)

  const path = (url: URL) => url.pathname === '/api/v1/system/storage'
  const poisoned = JSON.parse(JSON.stringify(observed))
  Object.defineProperty(poisoned, '__proto__', { value: 'private', enumerable: true })
  for (const [body, responseHeaders] of [
    [poisoned, { 'Cache-Control': 'no-store' }],
    [{ ...observed, free_bytes: observed.database_filesystem_total_bytes + 1 }, { 'Cache-Control': 'no-store' }],
    [observed, { 'Cache-Control': 'no-store', ETag: '"1"' }],
    [observed, { 'Cache-Control': 'public, max-age=60' }],
  ] as const) {
    await page.route(path, (route) => route.fulfill({ status: 200, json: body, headers: responseHeaders }))
    expect(await read()).toEqual({ code: 'INVALID_RESPONSE' })
    await page.unroute(path)
  }
  const reads = requests.length
  await context.setOffline(true)
  expect(await read()).toEqual({ code: 'NETWORK_ERROR' })
  await context.setOffline(false)
  await page.evaluate(() => window.dispatchEvent(new Event('online')))
  expect(requests).toHaveLength(reads + 1)
  expect((await page.request.post('/api/v1/auth/logout', { headers: await writeContext(page, application.url) })).status()).toBe(200)
  expect(await read()).toEqual({ code: 'AUTHENTICATION_REQUIRED' })
  expect(requests).toHaveLength(reads + 2)
  expect(requests.every((method) => method === 'GET')).toBe(true)
})
