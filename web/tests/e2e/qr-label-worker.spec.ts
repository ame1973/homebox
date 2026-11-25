import { mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { gzipSync } from 'node:zlib'
import { build } from 'vite'
import type { Page } from '@playwright/test'
import { messages } from '../../src/lib/messages.ts'
import { test as applicationTest, expect } from './auth-fixture.ts'
import { leaveDocument, permitBFCache, returnFromBFCache } from './bfcache-helpers.ts'
import { provision, watchDomainWrites } from './domain-helpers.ts'

type Bundle = { entry: string; worker: string; workerName: string; workerGzipBytes: number }
type QRProbe = {
  urls: string[]; requests: unknown[]; responses: number; terminated: number
  hold: boolean; held: boolean; kind?: string; signalCount: number; hidden: boolean
  controller?: AbortController; deliver?: () => void; inject?: (data: unknown) => void
}
declare global { interface Window { __qrProbe: QRProbe } }

const test = applicationTest.extend<{}, { qrBundle: Bundle }>({
  qrBundle: [async ({ browserName }, provideFixture) => {
    const directory = mkdtempSync(join(tmpdir(), `homebox-qr-worker-${browserName}-`))
    try {
      const entry = join(directory, 'entry.ts'), output = join(directory, 'out')
      const client = fileURLToPath(new URL('../../src/lib/qr-label-client.ts', import.meta.url))
      const decoder = fileURLToPath(new URL('../../src/lib/scan-zxing-core.ts', import.meta.url))
      writeFileSync(entry, `export * from ${JSON.stringify(client)}; export * from ${JSON.stringify(decoder)};\n`)
      await build({ configFile: false, logLevel: 'silent', define: { 'process.env.NODE_ENV': JSON.stringify('production') },
        resolve: { alias: { '@': fileURLToPath(new URL('../../src', import.meta.url)) } },
        build: { outDir: output, rollupOptions: { preserveEntrySignatures: 'strict', input: entry, output: { entryFileNames: 'assets/qr-private.js' } } } })
      const assets = join(output, 'assets'), names = readdirSync(assets), workers = names.filter((name) => /^qr-label-worker-.*\.js$/.test(name))
      if (workers.length !== 1) throw new Error(`Expected one private QR worker, got ${JSON.stringify(names)}`)
      const worker = join(assets, workers[0]!), workerGzipBytes = gzipSync(readFileSync(worker)).length
      if (workerGzipBytes > 150 * 1024) throw new Error(`QR worker exceeds the 150 KiB gzip budget: ${workerGzipBytes}`)
      await provideFixture({ entry: join(assets, 'qr-private.js'), worker, workerName: workers[0]!, workerGzipBytes })
    } finally { rmSync(directory, { recursive: true, force: true }) }
  }, { scope: 'worker' }],
})

test.use({ launchOptions: { channel: 'chromium', ignoreDefaultArgs: ['--disable-back-forward-cache'] }, trace: 'off', screenshot: 'off', video: 'off' })

async function install(page: Page, bundle: Bundle, transport?: 'broadcast' | 'storage') {
  await page.addInitScript(({ workerName, selected }) => {
    const probe: QRProbe = { urls: [], requests: [], responses: 0, terminated: 0, hold: false, held: false, signalCount: 0, hidden: false }
    window.__qrProbe = probe
    document.addEventListener('visibilitychange', () => { if (document.visibilityState !== 'visible') probe.hidden = true })
    if (selected === 'storage') {
      Object.defineProperty(window, 'BroadcastChannel', { configurable: true, value: undefined })
      window.addEventListener('storage', (event) => { if (event.key === 'homebox:session-cleared' && event.newValue === 'cleared') probe.signalCount++ })
    } else if (selected === 'broadcast') {
      window.addEventListener = new Proxy(window.addEventListener, { apply(target, receiver, args: unknown[]) {
        return args[0] === 'storage' ? undefined : Reflect.apply(target, receiver, args)
      } })
      window.BroadcastChannel = new Proxy(window.BroadcastChannel, { construct(target, args, newTarget) {
        const channel = Reflect.construct(target, args, newTarget) as BroadcastChannel
        channel.addEventListener('message', (event) => { if (event.data === 'cleared') probe.signalCount++ })
        return channel
      } })
    }
    window.Worker = new Proxy(window.Worker, { construct(target, args, newTarget) {
      const worker = Reflect.construct(target, args, newTarget) as Worker
      if (new URL(String(args[0]), window.location.href).pathname !== `/assets/${workerName}`) return worker
      probe.urls.push(String(args[0]))
      worker.postMessage = new Proxy(worker.postMessage, { apply(method, receiver, values: unknown[]) {
        probe.requests.push(values[0]); return Reflect.apply(method, receiver, values)
      } })
      worker.addEventListener('message', (event) => {
        probe.responses++
        if (!probe.hold) return
        event.stopImmediatePropagation(); probe.held = true
        probe.deliver = () => worker.onmessage?.call(worker, event)
        probe.inject = (data) => worker.onmessage?.call(worker, new MessageEvent('message', { data }))
      }, true)
      const terminate = worker.terminate.bind(worker)
      worker.terminate = () => { probe.terminated++; terminate() }
      return worker
    } })
  }, { workerName: bundle.workerName, selected: transport })
  await page.route('**/assets/qr-private.js', (route) => route.fulfill({ path: bundle.entry, contentType: 'text/javascript', headers: { 'Cache-Control': 'no-store' } }))
  await page.route((url) => url.pathname === `/assets/${bundle.workerName}`, (route) => route.fulfill({ path: bundle.worker, contentType: 'text/javascript', headers: { 'Cache-Control': 'no-store' } }))
}

async function startHeld(page: Page) {
  await page.evaluate(async () => {
    const modulePath = '/assets/qr-private.js', api = await import(modulePath), probe = window.__qrProbe
    probe.hold = true; probe.held = false; probe.controller = new AbortController()
    void api.prepareQRLabel('location', 'room-1', location.origin, probe.controller.signal).then((value: { kind: string }) => { probe.kind = value.kind })
  })
  await expect.poll(() => page.evaluate(() => window.__qrProbe.held)).toBe(true)
}

// This private bundle exercises the real same-origin worker with synthetic IDs.
// The same ZXing library writes and reads the pixels; this is not optical or printer acceptance.
test('encodes actual worker matrices for exact local targets without domain writes', async ({ page, application, qrBundle }) => {
  await provision(page, application); await install(page, qrBundle)
  const writes = watchDomainWrites(page, ['/api/v1/locations', '/api/v1/units', '/api/v1/items'])
  await page.goto('/scan')
  await expect(page.getByRole('textbox', { name: messages.scan.input })).toBeVisible()
  const values = await page.evaluate(async () => {
    const modulePath = '/assets/qr-private.js', api = await import(modulePath), results = []
    for (const [kind, id] of [['location', 'new'], ['unit', 'unit-1']] as const) {
      const label = await api.prepareQRLabel(kind, id, location.origin, new AbortController().signal)
      if (label.kind !== 'ready') throw new Error(`Worker preparation failed: ${label.kind}`)
      const width = label.size * 4, pixels = new Uint8ClampedArray(width * width)
      for (let y = 0; y < width; y++) for (let x = 0; x < width; x++) pixels[y * width + x] = label.modules[Math.floor(y / 4) * label.size + Math.floor(x / 4)] ? 0 : 255
      results.push({ kind, id, url: label.link.url, path: label.link.path, size: label.size, decoded: api.decodeZXing(pixels, width, width, ['QR_CODE']) })
    }
    return results
  })
  expect(values).toEqual([
    expect.objectContaining({ kind: 'location', id: 'new', url: `${application.url}/l/new`, path: '/locations/new/view', decoded: [{ format: 'QR_CODE', rawValue: `${application.url}/l/new` }] }),
    expect.objectContaining({ kind: 'unit', id: 'unit-1', url: `${application.url}/u/unit-1`, path: '/units/unit-1', decoded: [{ format: 'QR_CODE', rawValue: `${application.url}/u/unit-1` }] }),
  ])
  const probe = await page.evaluate(() => window.__qrProbe)
  expect(probe.urls).toHaveLength(2); expect(probe.urls.every((url) => new URL(url, application.url).origin === application.url)).toBe(true)
  expect(probe.urls.every((url) => new URL(url, application.url).pathname === `/assets/${qrBundle.workerName}`)).toBe(true)
  expect(probe.requests).toEqual([{ type: 'encode', kind: 'location', id: 'new', origin: application.url }, { type: 'encode', kind: 'unit', id: 'unit-1', origin: application.url }])
  expect(probe.responses).toBe(2); expect(probe.terminated).toBe(2); expect(qrBundle.workerGzipBytes).toBeLessThanOrEqual(150 * 1024)
  expect(writes).toEqual([])
})

test('holds one slot, cancels explicitly, ignores a late result and permits a fresh worker', async ({ page, application, qrBundle }) => {
  await provision(page, application); await install(page, qrBundle); await page.goto('/scan')
  await expect(page.getByRole('textbox', { name: messages.scan.input })).toBeVisible()
  await startHeld(page)
  const busy = await page.evaluate(async () => {
    const modulePath = '/assets/qr-private.js', api = await import(modulePath)
    return api.prepareQRLabel('unit', 'other', location.origin, new AbortController().signal)
  })
  expect(busy).toEqual({ kind: 'busy' })
  await page.evaluate(() => { window.__qrProbe.controller?.abort() })
  await expect.poll(() => page.evaluate(() => window.__qrProbe.kind)).toBe('cancelled')
  await page.evaluate(() => { window.__qrProbe.deliver?.(); window.__qrProbe.hold = false })
  expect(await page.evaluate(() => window.__qrProbe.kind)).toBe('cancelled')
  const fresh = await page.evaluate(async () => {
    const modulePath = '/assets/qr-private.js', api = await import(modulePath)
    return (await api.prepareQRLabel('unit', 'other', location.origin, new AbortController().signal)).kind
  })
  expect(fresh).toBe('ready'); expect(await page.evaluate(() => window.__qrProbe.terminated)).toBe(2)
})

for (const mode of ['hidden', 'pagehide', 'malformed', 'timeout'] as const) test(`discards a held worker result on ${mode}`, async ({ page, application, qrBundle }) => {
  await provision(page, application); await install(page, qrBundle); await page.goto('/scan')
  await expect(page.getByRole('textbox', { name: messages.scan.input })).toBeVisible()
  if (mode === 'timeout') await page.clock.install()
  await startHeld(page)
  if (mode === 'timeout') await page.clock.fastForward(8_000)
  else await page.evaluate((selected) => {
    const probe = window.__qrProbe
    if (selected === 'hidden') { Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'hidden' }); document.dispatchEvent(new Event('visibilitychange')) }
    if (selected === 'pagehide') window.dispatchEvent(new Event('pagehide'))
    if (selected === 'malformed') probe.inject?.({ type: 'result', size: 29, modules: new ArrayBuffer(29 * 29), privateDetail: 'must not escape' })
  }, mode)
  await expect.poll(() => page.evaluate(() => window.__qrProbe.kind)).toBe(mode === 'timeout' ? 'timeout' : mode === 'malformed' ? 'failed' : 'cancelled')
  expect(await page.evaluate(() => window.__qrProbe.terminated)).toBe(1)
  await page.evaluate(() => { window.__qrProbe.deliver?.() })
  expect(await page.evaluate(() => window.__qrProbe.kind)).toBe(mode === 'timeout' ? 'timeout' : mode === 'malformed' ? 'failed' : 'cancelled')
})

for (const transport of ['broadcast', 'storage'] as const) test(`cancels a held worker after real logout using ${transport} only`, async ({ page, context, application, qrBundle }) => {
  await install(page, qrBundle, transport); await provision(page, application)
  const writes = watchDomainWrites(page, ['/api/v1/locations', '/api/v1/units', '/api/v1/items'])
  await page.goto('/scan'); await expect(page.getByRole('textbox', { name: messages.scan.input })).toBeVisible()
  await startHeld(page)
  const other = await context.newPage()
  try {
    await other.goto('/settings/account')
    await other.getByRole('button', { name: messages.auth.logout, exact: true }).click()
    expect(await page.evaluate(() => window.__qrProbe.kind)).toBeUndefined()
    await other.getByRole('alertdialog').getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await expect.poll(() => page.evaluate(() => window.__qrProbe.signalCount)).toBeGreaterThan(0)
    await expect.poll(() => page.evaluate(() => window.__qrProbe.kind)).toBe('cancelled')
    expect(await page.evaluate(() => window.__qrProbe.hidden)).toBe(false)
    expect(await page.evaluate(() => window.__qrProbe.terminated)).toBe(1)
    await page.evaluate(() => { window.__qrProbe.deliver?.() })
    expect(await page.evaluate(() => window.__qrProbe.kind)).toBe('cancelled'); expect(writes).toEqual([])
  } finally { await other.close() }
})

test('terminates a held worker before an actual BFCache suspension', async ({ page, application, qrBundle }) => {
  await provision(page, application)
  const diagnostics = await permitBFCache(page, application.url)
  await install(page, qrBundle)
  const writes = watchDomainWrites(page, ['/api/v1/locations', '/api/v1/units', '/api/v1/items'])
  await page.goto('/scan'); await expect(page.getByRole('textbox', { name: messages.scan.input })).toBeVisible()
  await page.waitForLoadState('networkidle'); await startHeld(page)
  await leaveDocument(page, false); await returnFromBFCache(page, diagnostics)
  await expect(page.getByText(messages.scan.restoreReady)).toBeVisible()
  await expect.poll(() => page.evaluate(() => window.__qrProbe.kind)).toBe('cancelled')
  expect(await page.evaluate(() => window.__qrProbe.terminated)).toBe(1)
  await page.evaluate(() => { window.__qrProbe.deliver?.() })
  expect(await page.evaluate(() => window.__qrProbe.kind)).toBe('cancelled'); expect(writes).toEqual([])
})
