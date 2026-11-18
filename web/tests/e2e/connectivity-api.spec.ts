import { cpSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { build } from 'vite'
import { test as applicationTest, expect } from './auth-fixture.ts'
import { provision, writeContext } from './domain-helpers.ts'

const test = applicationTest.extend<{}, { applicationWebDirectory: string }>({
  applicationWebDirectory: [async ({ browserName }, provideFixture) => {
    const directory = mkdtempSync(join(tmpdir(), `homebox-connectivity-sdk-${browserName}-`))
    const web = join(directory, 'web'), sdk = join(directory, 'sdk'), entry = join(directory, 'entry.ts')
    try {
      cpSync(fileURLToPath(new URL('../../dist', import.meta.url)), web, { recursive: true })
      // The SDK bundle is private to this test fixture, not a production entrypoint.
      writeFileSync(entry, `export * from ${JSON.stringify(fileURLToPath(new URL('../../src/lib/api/connectivity.ts', import.meta.url)))};\n`)
      await build({ configFile: false, logLevel: 'silent', build: { outDir: sdk, minify: false, lib: { entry, formats: ['es'], fileName: 'connectivity-sdk' } } })
      cpSync(join(sdk, 'connectivity-sdk.js'), join(web, 'assets', 'connectivity-sdk.js'))
      await provideFixture(web)
    } finally { rmSync(directory, { recursive: true, force: true }) }
  }, { scope: 'worker' }],
})

test.use({ trace: 'off', screenshot: 'off', video: 'off' })

test('probes a real completed session without writes or replay, and rejects altered replies', async ({ page, context, application }) => {
  const requests: string[] = [], replies: { status: number; cache: string | undefined; etag: string | undefined }[] = []
  page.on('request', (request) => {
    if (new URL(request.url()).pathname === '/api/v1/system/connectivity') requests.push(request.method())
  })
  page.on('response', (response) => {
    if (new URL(response.url()).pathname === '/api/v1/system/connectivity') {
      const headers = response.headers()
      replies.push({ status: response.status(), cache: headers['cache-control'], etag: headers.etag })
    }
  })
  await page.goto('/')
  const read = () => page.evaluate(async () => {
    const modulePath = '/assets/connectivity-sdk.js', api = await import(modulePath)
    try { return await api.probeConnectivity(new AbortController().signal) }
    catch (error) { return { code: error instanceof Error && 'code' in error ? error.code : 'unexpected' } }
  })
  expect(await read()).toEqual({ code: 'AUTHENTICATION_REQUIRED' })
  await provision(page, application)
  expect(await read()).toEqual({ status: 'ok' })
  const statusRoute = (url: URL) => url.pathname === '/api/v1/system/connectivity'
  const poisoned = JSON.parse('{"status":"ok","__proto__":"private"}')
  await page.route(statusRoute, async (route) => {
    await route.fulfill({ status: 200, body: JSON.stringify(poisoned), headers: { 'Content-Type': 'application/json', 'Cache-Control': 'no-store' } })
  })
  expect(await read()).toEqual({ code: 'INVALID_RESPONSE' })
  await page.unroute(statusRoute)
  await page.route(statusRoute, async (route) => {
    await route.fulfill({ status: 200, json: { status: 'ok' }, headers: { ETag: '"1"' } })
  })
  expect(await read()).toEqual({ code: 'INVALID_RESPONSE' })
  await page.unroute(statusRoute)
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
  expect(replies[1]).toEqual({ status: 200, cache: 'no-store', etag: undefined })
  expect(replies.at(-1)).toEqual({ status: 401, cache: 'no-store', etag: undefined })
})
