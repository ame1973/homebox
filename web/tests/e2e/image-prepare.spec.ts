import { mkdtempSync, readdirSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { build } from 'vite'
import type { Page } from '@playwright/test'
import { messages } from '../../src/lib/messages.ts'
import { test as applicationTest, expect } from './auth-fixture.ts'
import { leaveDocument, permitBFCache, returnFromBFCache } from './bfcache-helpers.ts'
import { provision, watchDomainWrites } from './domain-helpers.ts'

type Bundle = { entry: string; worker: string; workerName: string }
type PrepareProbe = { urls: string[]; terminated: number; inputs: { named: boolean; type: string; size: number }[];
  hold: boolean; held: boolean; deliver?: () => void; kind?: string }
declare global { interface Window { __prepareProbe: PrepareProbe; __prepareSignals: number; __prepareHidden: boolean } }

const test = applicationTest.extend<{}, { prepareBundle: Bundle }>({
  prepareBundle: [async ({ browserName }, provideFixture) => {
    const directory = mkdtempSync(join(tmpdir(), `homebox-image-worker-${browserName}-`))
    try {
      const entry = join(directory, 'entry.ts'), output = join(directory, 'out')
      const client = fileURLToPath(new URL('../../src/features/capture/image-prepare-client.ts', import.meta.url))
      writeFileSync(entry, `export * from ${JSON.stringify(client)};\n`)
      await build({ configFile: false, logLevel: 'silent', resolve: { alias: { '@': fileURLToPath(new URL('../../src', import.meta.url)) } },
        build: { outDir: output, minify: false, rollupOptions: { preserveEntrySignatures: 'strict', input: entry, output: { entryFileNames: 'assets/image-prepare-private.js' } } } })
      const assets = join(output, 'assets'), names = readdirSync(assets), workers = names.filter((name) => /^image-prepare-worker-.*\.js$/.test(name))
      if (workers.length !== 1) throw new Error(`Expected one private worker asset, got ${JSON.stringify(names)}`)
      await provideFixture({ entry: join(assets, 'image-prepare-private.js'), worker: join(assets, workers[0]!), workerName: workers[0]! })
    } finally { rmSync(directory, { recursive: true, force: true }) }
  }, { scope: 'worker' }],
})

test.use({ launchOptions: { channel: 'chromium', ignoreDefaultArgs: ['--disable-back-forward-cache'] }, trace: 'off', screenshot: 'off', video: 'off' })

async function install(page: Page, bundle: Bundle) {
  await page.addInitScript((workerName) => {
    const probe: PrepareProbe = { urls: [], terminated: 0, inputs: [], hold: false, held: false }
    window.__prepareProbe = probe
    window.Worker = new Proxy(window.Worker, { construct(target, args, newTarget) {
      const worker = Reflect.construct(target, args, newTarget) as Worker
      if (new URL(String(args[0]), window.location.href).pathname !== `/assets/${workerName}`) return worker
      probe.urls.push(String(args[0]))
      worker.postMessage = new Proxy(worker.postMessage, { apply(method, receiver, values: unknown[]) {
        const value = values[0] as { blob?: unknown } | null
        if (value?.blob instanceof Blob) probe.inputs.push({ named: 'name' in value.blob, type: value.blob.type, size: value.blob.size })
        return Reflect.apply(method, receiver, values)
      } })
      worker.addEventListener('message', (event) => {
        if (probe.hold && event.data?.type === 'result') {
          probe.held = true; event.stopImmediatePropagation()
          probe.deliver = () => worker.onmessage?.call(worker, event)
        }
      }, true)
      const terminate = worker.terminate.bind(worker)
      worker.terminate = () => { probe.terminated++; terminate() }
      return worker
    } })
  }, bundle.workerName)
  await page.route('**/assets/image-prepare-private.js', (route) => route.fulfill({ path: bundle.entry, contentType: 'text/javascript', headers: { 'Cache-Control': 'no-store' } }))
  await page.route((url) => url.pathname === `/assets/${bundle.workerName}`, (route) => route.fulfill({ path: bundle.worker, contentType: 'text/javascript', headers: { 'Cache-Control': 'no-store' } }))
}

// The bundle and input images exist only inside a disposable fixture. It is
// not a production file picker, upload, physical device or OS-prompt test.
test('prepares real browser file bytes in a same-origin worker without filename or writes', async ({ page, application, prepareBundle }) => {
  await install(page, prepareBundle)
  const writes = watchDomainWrites(page, ['/api/v1/media', '/api/v1/items', '/api/v1/inventory'])
  await page.goto('/login')
  const result = await page.evaluate(async () => {
    const modulePath = '/assets/image-prepare-private.js', api = await import(modulePath)
    const bytes = Uint8Array.from(atob('iVBORw0KGgoAAAANSUhEUgAAAAIAAAADCAYAAAC56t6BAAAAHElEQVR4nAXBAQEAMAjAIHwyo9l8B5EcqnhhtB+Pawm7X97uPgAAAABJRU5ErkJggg=='), (c) => c.charCodeAt(0))
    const marker = new TextEncoder().encode('PRIVATE_GPS'), tiff = new Uint8Array(26 + marker.length), view = new DataView(tiff.buffer)
    tiff.set([73, 73]); view.setUint16(2, 42, true); view.setUint32(4, 8, true); view.setUint16(8, 1, true)
    view.setUint16(10, 0x112, true); view.setUint16(12, 3, true); view.setUint32(14, 1, true); view.setUint16(18, 6, true); tiff.set(marker, 26)
    const exif = new Uint8Array(12 + tiff.length), chunk = new DataView(exif.buffer)
    chunk.setUint32(0, tiff.length); exif.set([101, 88, 73, 102], 4); exif.set(tiff, 8)
    let crc = 0xffffffff
    for (const byte of exif.subarray(4, exif.length - 4)) { crc ^= byte; for (let bit = 0; bit < 8; bit++) crc = crc >>> 1 ^ (crc & 1 ? 0xedb88320 : 0) }
    chunk.setUint32(exif.length - 4, (crc ^ 0xffffffff) >>> 0)
    const raw = new Uint8Array(bytes.length + exif.length)
    raw.set(bytes.subarray(0, 33)); raw.set(exif, 33); raw.set(bytes.subarray(33), 33 + exif.length)
    const input = new File([raw], 'PRIVATE-LOCATION-name.png', { type: 'image/gif', lastModified: 123 })
    const prepared = await api.prepareSelectedImage(input, new AbortController().signal)
    if (prepared.kind !== 'prepared') return { kind: prepared.kind }
    const bitmap = await createImageBitmap(prepared.blob)
    const canvas = document.createElement('canvas'); canvas.width = bitmap.width; canvas.height = bitmap.height
    const context = canvas.getContext('2d')
    if (!context) throw new Error('Canvas unavailable')
    context.drawImage(bitmap, 0, 0); bitmap.close()
    const alpha = Array.from({ length: 6 }, (_, index) => context.getImageData(index % 2, Math.floor(index / 2), 1, 1).data[3])
    canvas.width = 0; canvas.height = 0
    const privateMetadata = new TextDecoder().decode(await prepared.blob.arrayBuffer()).includes('PRIVATE_GPS')
    return { kind: prepared.kind, mime: prepared.mime, width: prepared.width, height: prepared.height, orientation: prepared.orientation,
      blobType: prepared.blob.type, isFile: prepared.blob instanceof File, privateMetadata, alpha }
  })
  expect(result).toEqual({ kind: 'prepared', mime: 'image/png', width: 2, height: 3, orientation: 6, blobType: 'image/png', isFile: false, privateMetadata: false, alpha: [255, 128, 255, 0, 255, 64] })
  const probe = await page.evaluate(() => ({ urls: window.__prepareProbe.urls, terminated: window.__prepareProbe.terminated, inputs: window.__prepareProbe.inputs }))
  expect(probe.urls).toHaveLength(1)
  expect(new URL(probe.urls[0]!, application.url).origin).toBe(application.url)
  expect(new URL(probe.urls[0]!, application.url).pathname).toBe(`/assets/${prepareBundle.workerName}`)
  expect(probe.inputs).toEqual([{ named: false, type: '', size: 134 }])
  expect(probe.terminated).toBe(1); expect(writes).toEqual([])
})

for (const transport of ['broadcast', 'storage'] as const) test(`cancels a held preparation after real cross-window logout using ${transport} only`, async ({ page, context, application, prepareBundle }) => {
  await page.addInitScript((selected) => {
    window.__prepareSignals = 0; window.__prepareHidden = false
    document.addEventListener('visibilitychange', () => { if (document.visibilityState !== 'visible') window.__prepareHidden = true })
    if (selected === 'storage') {
      Object.defineProperty(window, 'BroadcastChannel', { configurable: true, value: undefined })
      window.addEventListener('storage', (event) => { if (event.key === 'homebox:session-cleared' && event.newValue === 'cleared') window.__prepareSignals++ })
    } else {
      window.addEventListener = new Proxy(window.addEventListener, { apply(target, receiver, args: unknown[]) {
        return args[0] === 'storage' ? undefined : Reflect.apply(target, receiver, args)
      } })
      window.BroadcastChannel = new Proxy(window.BroadcastChannel, { construct(target, args, newTarget) {
        const channel = Reflect.construct(target, args, newTarget) as BroadcastChannel
        channel.addEventListener('message', (event) => { if (event.data === 'cleared') window.__prepareSignals++ })
        return channel
      } })
    }
  }, transport)
  await provision(page, application)
  await install(page, prepareBundle)
  const writes = watchDomainWrites(page, ['/api/v1/media', '/api/v1/items', '/api/v1/inventory'])
  await page.goto('/scan')
  await expect(page.getByRole('textbox', { name: messages.scan.input })).toBeVisible()
  await page.evaluate(async () => {
    const modulePath = '/assets/image-prepare-private.js', api = await import(modulePath), probe = window.__prepareProbe
    const bytes = Uint8Array.from(atob('iVBORw0KGgoAAAANSUhEUgAAAAIAAAADCAYAAAC56t6BAAAAHElEQVR4nAXBAQEAMAjAIHwyo9l8B5EcqnhhtB+Pawm7X97uPgAAAABJRU5ErkJggg=='), (c) => c.charCodeAt(0))
    probe.hold = true
    void api.prepareSelectedImage(new File([bytes], 'private-name.png'), new AbortController().signal).then((value: { kind: string }) => { probe.kind = value.kind })
  })
  await expect.poll(() => page.evaluate(() => window.__prepareProbe.held)).toBe(true)
  const other = await context.newPage()
  try {
    await other.goto('/settings/account')
    expect(await page.evaluate(() => window.__prepareProbe.kind)).toBeUndefined()
    await other.getByRole('button', { name: messages.auth.logout, exact: true }).click()
    expect(await page.evaluate(() => window.__prepareProbe.kind)).toBeUndefined()
    await other.getByRole('alertdialog').getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await expect.poll(() => page.evaluate(() => window.__prepareSignals)).toBeGreaterThan(0)
    await expect.poll(() => page.evaluate(() => window.__prepareProbe.kind)).toBe('cancelled')
    expect(await page.evaluate(() => window.__prepareHidden)).toBe(false)
    expect(await page.evaluate(() => window.__prepareProbe.terminated)).toBe(1)
    await page.evaluate(() => { window.__prepareProbe.deliver?.(); window.__prepareProbe.deliver = undefined })
    expect(await page.evaluate(() => window.__prepareProbe.kind)).toBe('cancelled')
    expect(writes).toEqual([])
  } finally { await other.close() }
})

test('terminates a held real worker before BFCache suspension and rejects its late result', async ({ page, application, prepareBundle }) => {
  await provision(page, application)
  const diagnostics = await permitBFCache(page, application.url)
  await install(page, prepareBundle)
  const writes = watchDomainWrites(page, ['/api/v1/media', '/api/v1/items', '/api/v1/inventory'])
  await page.goto('/scan')
  await expect(page.getByRole('textbox', { name: messages.scan.input })).toBeVisible()
  await page.waitForLoadState('networkidle')
  await page.evaluate(async () => {
    const modulePath = '/assets/image-prepare-private.js', api = await import(modulePath), probe = window.__prepareProbe
    const bytes = Uint8Array.from(atob('iVBORw0KGgoAAAANSUhEUgAAAAIAAAADCAYAAAC56t6BAAAAHElEQVR4nAXBAQEAMAjAIHwyo9l8B5EcqnhhtB+Pawm7X97uPgAAAABJRU5ErkJggg=='), (c) => c.charCodeAt(0))
    probe.hold = true
    void api.prepareSelectedImage(new File([bytes], 'private-name.png'), new AbortController().signal).then((value: { kind: string }) => { probe.kind = value.kind })
  })
  await expect.poll(() => page.evaluate(() => window.__prepareProbe.held)).toBe(true)
  await leaveDocument(page, false)
  await returnFromBFCache(page, diagnostics)
  await expect(page.getByText(messages.scan.restoreReady)).toBeVisible()
  await expect.poll(() => page.evaluate(() => window.__prepareProbe.kind)).toBe('cancelled')
  expect(await page.evaluate(() => window.__prepareProbe.terminated)).toBe(1)
  await page.evaluate(() => { window.__prepareProbe.deliver?.(); window.__prepareProbe.deliver = undefined })
  expect(await page.evaluate(() => window.__prepareProbe.kind)).toBe('cancelled')
  expect(writes).toEqual([])
})
