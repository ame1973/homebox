import { cpSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { build } from 'vite'
import { test as applicationTest, expect } from './auth-fixture.ts'
import { provision, writeContext } from './domain-helpers.ts'

const test = applicationTest.extend<{}, { applicationWebDirectory: string }>({
  applicationWebDirectory: [async ({ browserName }, provideFixture) => {
    const directory = mkdtempSync(join(tmpdir(), `homebox-system-version-sdk-${browserName}-`))
    const web = join(directory, 'web'), sdk = join(directory, 'sdk'), entry = join(directory, 'entry.ts')
    try {
      cpSync(fileURLToPath(new URL('../../dist', import.meta.url)), web, { recursive: true })
      // This SDK bundle belongs only to the disposable browser fixture.
      writeFileSync(entry, `export * from ${JSON.stringify(fileURLToPath(new URL('../../src/lib/api/system-version.ts', import.meta.url)))};\n`)
      await build({ configFile: false, logLevel: 'silent', build: { outDir: sdk, minify: false, lib: { entry, formats: ['es'], fileName: 'system-version-sdk' } } })
      cpSync(join(sdk, 'system-version-sdk.js'), join(web, 'assets', 'system-version-sdk.js'))
      await provideFixture(web)
    } finally { rmSync(directory, { recursive: true, force: true }) }
  }, { scope: 'worker' }],
})

test.use({ trace: 'off', screenshot: 'off', video: 'off' })

test('reads actual version sources without guessing model inventory or replaying requests', async ({ page, context, application }) => {
  const requests: string[] = []
  page.on('request', (request) => {
    if (new URL(request.url()).pathname === '/api/v1/system/version') requests.push(request.method())
  })
  await page.goto('/')
  const read = () => page.evaluate(async () => {
    const modulePath = '/assets/system-version-sdk.js', api = await import(modulePath)
    try { return await api.fetchSystemVersion(new AbortController().signal) }
    catch (error) { return { code: error instanceof Error && 'code' in error ? error.code : 'unexpected' } }
  })
  expect(await read()).toEqual({ code: 'AUTHENTICATION_REQUIRED' })
  await provision(page, application)
  const observed = await read()
  const packageVersion = (JSON.parse(readFileSync(fileURLToPath(new URL('../../package.json', import.meta.url)), 'utf8')) as { version: string }).version
  const worker = await page.evaluate(() => document.querySelector<HTMLMetaElement>('meta[name="homebox-shell-worker"]')?.content)
  expect(worker).toMatch(/^\/pwa-worker-[a-f0-9]{24}\.js$/)
  expect(observed).toMatchObject({ api_version: '1', projection_version: 1, offline_projection_version: 1,
    web_build_id: worker!.slice('/pwa-worker-'.length, -'.js'.length), web_version: packageVersion,
    min_supported_client: null, min_web_build_id: null, model_index_versions: null,
    supported_write_client_versions: ['0.1.0', '0.1.0-dev'] })
  expect(observed.server_instance_id).toMatch(/^[a-f0-9]{32}$/)
  expect(observed.dataset_epoch).toBeTruthy()
  expect(observed.schema_version).toBeGreaterThan(0)
  const sync = await page.request.get('/api/v1/sync/status', { headers: { Origin: application.url } })
  expect(sync.status()).toBe(200)
  expect(await sync.json()).toMatchObject({ dataset_epoch: observed.dataset_epoch, server_instance_id: observed.server_instance_id, projection_version: observed.projection_version, bootstrap_ready: false })
  const build = await page.request.get('/web-build.json')
  expect(build.status()).toBe(404)
  expect(build.headers()['cache-control']).toBe('no-store')

  const headers = await writeContext(page, application.url)
  const accepted = await page.request.post('/api/v1/system/version', { headers })
  expect(accepted.status()).toBe(405)
  const rejected = await page.request.post('/api/v1/system/version', { headers: { ...headers, 'X-HomeBox-Client-Version': '0.2.0' } })
  expect(rejected.status()).toBe(426)
  expect((await rejected.json()).error.code).toBe('CLIENT_UPGRADE_REQUIRED')

  const versionRoute = (url: URL) => url.pathname === '/api/v1/system/version'
  const poisoned = JSON.parse(JSON.stringify(observed))
  Object.defineProperty(poisoned, '__proto__', { value: 'private', enumerable: true })
  for (const [body, responseHeaders] of [
    [poisoned, { 'Cache-Control': 'no-store' }],
    [observed, { 'Cache-Control': 'no-store', ETag: '"1"' }],
    [observed, { 'Cache-Control': 'public, max-age=60' }],
  ] as const) {
    await page.route(versionRoute, (route) => route.fulfill({ status: 200, json: body, headers: responseHeaders }))
    expect(await read()).toEqual({ code: 'INVALID_RESPONSE' })
    await page.unroute(versionRoute)
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
