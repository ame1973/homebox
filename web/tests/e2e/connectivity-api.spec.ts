import { cpSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { build } from 'vite'
import { messages } from '../../src/lib/messages.ts'
import { test as applicationTest, expect } from './auth-fixture.ts'
import { leaveDocument, permitBFCache, returnFromBFCache } from './bfcache-helpers.ts'
import { provision, writeContext } from './domain-helpers.ts'

const test = applicationTest.extend<{}, { applicationWebDirectory: string }>({
  applicationWebDirectory: [async ({ browserName }, provideFixture) => {
    const directory = mkdtempSync(join(tmpdir(), `homebox-connectivity-sdk-${browserName}-`))
    const web = join(directory, 'web'), sdk = join(directory, 'sdk'), entry = join(directory, 'entry.ts')
    try {
      cpSync(fileURLToPath(new URL('../../dist', import.meta.url)), web, { recursive: true })
      // The SDK bundle is private to this test fixture, not a production entrypoint.
      const adapter = fileURLToPath(new URL('../../src/lib/api/connectivity.ts', import.meta.url))
      const monitor = fileURLToPath(new URL('../../src/lib/foreground-connectivity.ts', import.meta.url))
      const source = fileURLToPath(new URL('../../src', import.meta.url))
      writeFileSync(entry, `export * from ${JSON.stringify(adapter)}; export * from ${JSON.stringify(monitor)};\n`)
      await build({ configFile: false, logLevel: 'silent', resolve: { alias: { '@': source } }, build: { outDir: sdk, minify: false, lib: { entry, formats: ['es'], fileName: 'connectivity-sdk' } } })
      cpSync(join(sdk, 'connectivity-sdk.js'), join(web, 'assets', 'connectivity-sdk.js'))
      await provideFixture(web)
    } finally { rmSync(directory, { recursive: true, force: true }) }
  }, { scope: 'worker' }],
})

test.use({ launchOptions: { channel: 'chromium', ignoreDefaultArgs: ['--disable-back-forward-cache'] }, trace: 'off', screenshot: 'off', video: 'off' })

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

type MonitorHarness = { states: string[]; check: () => boolean; stop: () => void }

for (const transport of ['storage', 'broadcast'] as const) test(`monitors a verified session and stops on logout using ${transport} only`, async ({ page, context, application }) => {
  await page.addInitScript((selected) => {
    if (selected === 'storage') Object.defineProperty(window, 'BroadcastChannel', { configurable: true, value: undefined })
    else window.addEventListener = new Proxy(window.addEventListener, { apply(target, receiver, args: unknown[]) {
      return args[0] === 'storage' ? undefined : Reflect.apply(target, receiver, args)
    } })
  }, transport)
  await provision(page, application)
  await page.goto('/')
  const methods: string[] = []
  page.on('request', (request) => { if (new URL(request.url()).pathname === '/api/v1/system/connectivity') methods.push(request.method()) })
  await page.evaluate(async () => {
    Object.defineProperty(navigator, 'onLine', { configurable: true, value: false })
    const modulePath = '/assets/connectivity-sdk.js', api = await import(modulePath)
    const states: string[] = []
    const monitor = api.monitorForegroundConnectivity((value: { phase: string }) => { states.push(value.phase) })
    ;(window as unknown as { __connectivity: MonitorHarness }).__connectivity = { states, ...monitor }
  })
  const observation = () => page.evaluate(() => (window as unknown as { __connectivity: MonitorHarness }).__connectivity.states.at(-1))
  const check = () => page.evaluate(() => (window as unknown as { __connectivity: MonitorHarness }).__connectivity.check())
  await expect.poll(observation).toBe('reachable')
  await context.setOffline(true)
  await expect.poll(check).toBe(true)
  await expect.poll(observation).toBe('unavailable')
  await context.setOffline(false)
  await expect.poll(check).toBe(true)
  await expect.poll(observation).toBe('reachable')
  const other = await context.newPage()
  try {
    await other.goto('/settings/account')
    await other.getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await other.getByRole('alertdialog').getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await expect.poll(observation).toBe('stopped')
    const before = methods.length
    expect(await check()).toBe(false)
    await page.evaluate(() => window.dispatchEvent(new Event('online')))
    expect(methods).toHaveLength(before)
    expect(methods.every((method) => method === 'GET')).toBe(true)
  } finally { await other.close() }
})

test.describe('back-forward cache', () => {
  test('stops before suspension and never restarts after an actual BFCache restoration', async ({ page, application }) => {
    await provision(page, application)
    const diagnostics = await permitBFCache(page, application.url)
    let reads = 0
    page.on('request', (request) => { if (new URL(request.url()).pathname === '/api/v1/system/connectivity') reads++ })
    await page.goto('/')
    await page.evaluate(async () => {
      const modulePath = '/assets/connectivity-sdk.js', api = await import(modulePath)
      const states: string[] = []
      const monitor = api.monitorForegroundConnectivity((value: { phase: string }) => { states.push(value.phase) })
      ;(window as unknown as { __connectivity: MonitorHarness }).__connectivity = { states, ...monitor }
    })
    const observation = () => page.evaluate(() => (window as unknown as { __connectivity: MonitorHarness }).__connectivity.states.at(-1))
    await expect.poll(observation).toBe('reachable')
    const initialReads = reads
    await leaveDocument(page, false)
    await returnFromBFCache(page, diagnostics)
    await expect.poll(observation).toBe('stopped')
    expect(await page.evaluate(() => (window as unknown as { __connectivity: MonitorHarness }).__connectivity.check())).toBe(false)
    expect(reads).toBe(initialReads)
  })
})
