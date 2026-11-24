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
type NormalizeProbe = { mode: 'none' | 'decode' | 'encode'; held: boolean; closed: number; release?: () => void; kind?: string; canvas?: HTMLCanvasElement }
declare global { interface Window { __prepareProbe: PrepareProbe; __prepareSignals: number; __prepareHidden: boolean; __normalizeProbe: NormalizeProbe } }

const test = applicationTest.extend<{}, { prepareBundle: Bundle }>({
  prepareBundle: [async ({ browserName }, provideFixture) => {
    const directory = mkdtempSync(join(tmpdir(), `homebox-image-worker-${browserName}-`))
    try {
      const entry = join(directory, 'entry.ts'), output = join(directory, 'out')
      const client = fileURLToPath(new URL('../../src/features/capture/image-prepare-client.ts', import.meta.url))
      const normalize = fileURLToPath(new URL('../../src/features/capture/image-normalize.ts', import.meta.url))
      writeFileSync(entry, `export * from ${JSON.stringify(client)}; export * from ${JSON.stringify(normalize)};\n`)
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

async function holdNativePhotoCallbacks(page: Page) {
  await page.addInitScript(() => {
    const probe: NormalizeProbe = { mode: 'none', held: false, closed: 0 }
    window.__normalizeProbe = probe
    window.createImageBitmap = new Proxy(window.createImageBitmap, { apply(target, receiver, args: unknown[]) {
      const result = Reflect.apply(target, receiver, args) as Promise<ImageBitmap>
      if (probe.mode !== 'decode') return result
      return result.then((bitmap) => new Promise<ImageBitmap>((resolve) => {
        const close = bitmap.close.bind(bitmap)
        bitmap.close = () => { probe.closed++; close() }
        probe.held = true; probe.release = () => resolve(bitmap)
      }))
    } })
    const toBlob = HTMLCanvasElement.prototype.toBlob
    HTMLCanvasElement.prototype.toBlob = function (callback, type, quality) {
      if (probe.mode !== 'encode') { toBlob.call(this, callback, type, quality); return }
      toBlob.call(this, (blob) => { probe.held = true; probe.canvas = this; probe.release = () => callback(blob) }, type, quality)
    }
  })
}

async function startHeldNormalization(page: Page, mode: 'decode' | 'encode') {
  await page.evaluate(async (selected) => {
    const modulePath = '/assets/image-prepare-private.js', api = await import(modulePath), probe = window.__normalizeProbe
    const bytes = Uint8Array.from(atob('iVBORw0KGgoAAAANSUhEUgAAAAIAAAADCAYAAAC56t6BAAAAHElEQVR4nAXBAQEAMAjAIHwyo9l8B5EcqnhhtB+Pawm7X97uPgAAAABJRU5ErkJggg=='), (c) => c.charCodeAt(0))
    const source = await api.prepareSelectedImage(new File([bytes], 'private-name.png'), new AbortController().signal)
    if (source.kind !== 'prepared') throw new Error(`Preparation failed: ${source.kind}`)
    probe.mode = selected
    void api.normalizeSelectedImage(source, new AbortController().signal).then((value: { kind: string }) => { probe.kind = value.kind })
  }, mode)
  await expect.poll(() => page.evaluate(() => window.__normalizeProbe.held)).toBe(true)
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

test('normalizes real transparent pixels in all eight EXIF orientations without retaining private metadata', async ({ page, application, prepareBundle }) => {
  await install(page, prepareBundle)
  const writes = watchDomainWrites(page, ['/api/v1/media', '/api/v1/items', '/api/v1/inventory'])
  await page.goto('/login')
  const values = await page.evaluate(async () => {
    const modulePath = '/assets/image-prepare-private.js', api = await import(modulePath)
    const from64 = (value: string) => Uint8Array.from(atob(value), (character) => character.charCodeAt(0))
    const text = (value: string) => Uint8Array.from(value, (character) => character.charCodeAt(0))
    const concat = (...parts: Uint8Array[]) => {
      const out = new Uint8Array(parts.reduce((sum, part) => sum + part.length, 0))
      let offset = 0; for (const part of parts) { out.set(part, offset); offset += part.length }; return out
    }
    const tiff = (orientation: number) => {
      const raw = new Uint8Array(26), view = new DataView(raw.buffer)
      raw.set([73, 73]); view.setUint16(2, 42, true); view.setUint32(4, 8, true); view.setUint16(8, 1, true)
      view.setUint16(10, 0x112, true); view.setUint16(12, 3, true); view.setUint32(14, 1, true); view.setUint16(18, orientation, true)
      return concat(raw, text('PRIVATE_GPS'))
    }
    function pngChunk(name: string, data: Uint8Array) {
      const raw = new Uint8Array(data.length + 12), view = new DataView(raw.buffer)
      view.setUint32(0, data.length); raw.set(text(name), 4); raw.set(data, 8)
      let crc = 0xffffffff
      for (const byte of raw.subarray(4, raw.length - 4)) { crc ^= byte; for (let bit = 0; bit < 8; bit++) crc = crc >>> 1 ^ (crc & 1 ? 0xedb88320 : 0) }
      view.setUint32(raw.length - 4, (crc ^ 0xffffffff) >>> 0); return raw
    }
    function webpChunk(name: string, data: Uint8Array) {
      const raw = new Uint8Array(8 + data.length + (data.length & 1))
      raw.set(text(name)); new DataView(raw.buffer).setUint32(4, data.length, true); raw.set(data, 8); return raw
    }
    function webp(...chunks: Uint8Array[]) {
      const raw = concat(text('RIFF'), new Uint8Array(4), text('WEBP'), ...chunks)
      new DataView(raw.buffer).setUint32(4, raw.length - 8, true); return raw
    }
    const png = from64('iVBORw0KGgoAAAANSUhEUgAAAAIAAAADCAYAAAC56t6BAAAAHElEQVR4nAXBAQEAMAjAIHwyo9l8B5EcqnhhtB+Pawm7X97uPgAAAABJRU5ErkJggg==')
    const originalWebP = from64('UklGRjYAAABXRUJQVlA4TCkAAAAvAYAAEC8w/wKCIv9HExAU+T+aQIDgzP+U4A0i6igIBNKQUgorHNH/WAA=')
    const cases = [
      ...Array.from({ length: 8 }, (_, index) => ({ orientation: index + 1, mime: 'image/png', original: png,
        input: concat(png.subarray(0, 33), pngChunk('eXIf', tiff(index + 1)), png.subarray(33)) })),
      { orientation: 7, mime: 'image/webp', original: originalWebP,
        input: webp(webpChunk('VP8X', new Uint8Array([0x18, 0, 0, 0, 1, 0, 0, 2, 0, 0])), originalWebP.subarray(12), webpChunk('EXIF', tiff(7))) },
    ]
    const mappings = [[0, 1, 2, 3, 4, 5], [1, 0, 3, 2, 5, 4], [5, 4, 3, 2, 1, 0], [4, 5, 2, 3, 0, 1],
      [0, 2, 4, 1, 3, 5], [4, 2, 0, 5, 3, 1], [5, 3, 1, 4, 2, 0], [1, 3, 5, 0, 2, 4]]
    async function pixels(blob: Blob) {
      const bitmap = await createImageBitmap(blob), canvas = document.createElement('canvas')
      canvas.width = bitmap.width; canvas.height = bitmap.height
      const context = canvas.getContext('2d')
      if (!context) throw new Error('Canvas unavailable')
      context.drawImage(bitmap, 0, 0); bitmap.close()
      const result = { width: canvas.width, height: canvas.height, values: Array.from(context.getImageData(0, 0, canvas.width, canvas.height).data) }
      canvas.width = 0; canvas.height = 0; return result
    }
    const results = []
    for (const item of cases) {
      const source = await pixels(new Blob([item.original], { type: item.mime }))
      const prepared = await api.prepareSelectedImage(new File([item.input], 'PRIVATE-NAME.image'), new AbortController().signal)
      if (prepared.kind !== 'prepared') throw new Error(`Preparation failed: ${prepared.kind}`)
      const frame = await api.normalizeSelectedImage(prepared, new AbortController().signal)
      if (frame.kind !== 'imported') throw new Error(`Normalization failed: ${frame.kind}`)
      const actual = await pixels(frame.blob), mapping = mappings[item.orientation - 1]!
      const matches = mapping.every((sourceIndex, index) => [0, 1, 2, 3].every((channel) => {
        const expected = source.values[sourceIndex * 4 + channel]!, got = actual.values[index * 4 + channel]!
        return Math.abs(expected - got) <= (channel === 3 ? 0 : 2)
      }))
      const privateMetadata = new TextDecoder().decode(await frame.blob.arrayBuffer()).includes('PRIVATE_GPS')
      results.push({ orientation: item.orientation, kind: frame.kind, width: frame.width, height: frame.height, mime: frame.blob.type,
        decodedWidth: actual.width, decodedHeight: actual.height, matches, privateMetadata,
        branded: api.isImportedFrame(frame), clonedBrand: api.isImportedFrame({ ...frame }) })
    }
    return results
  })
  expect(values).toHaveLength(9)
  for (const result of values) {
    const size = result.orientation >= 5 ? [3, 2] : [2, 3]
    expect([result.width, result.height]).toEqual(size); expect([result.decodedWidth, result.decodedHeight]).toEqual(size)
    expect(result).toMatchObject({ kind: 'imported', mime: 'image/png', matches: true, privateMetadata: false, branded: true, clonedBrand: false })
  }
  const probe = await page.evaluate(() => ({ urls: window.__prepareProbe.urls, terminated: window.__prepareProbe.terminated }))
  expect(probe.urls).toHaveLength(9); expect(probe.terminated).toBe(9)
  expect(probe.urls.every((url) => new URL(url, application.url).origin === application.url)).toBe(true)
  expect(writes).toEqual([])
})

test('rotates a real JPEG once and re-encodes without its private EXIF data', async ({ page, prepareBundle }) => {
  await install(page, prepareBundle)
  const writes = watchDomainWrites(page, ['/api/v1/media', '/api/v1/items', '/api/v1/inventory'])
  await page.goto('/login')
  const result = await page.evaluate(async () => {
    const modulePath = '/assets/image-prepare-private.js', api = await import(modulePath)
    const canvas = document.createElement('canvas'); canvas.width = 48; canvas.height = 32
    const context = canvas.getContext('2d')
    if (!context) throw new Error('Canvas unavailable')
    for (const [x, y, color] of [[0, 0, '#ff0000'], [24, 0, '#00ff00'], [0, 16, '#0000ff'], [24, 16, '#ffff00']] as const) {
      context.fillStyle = color; context.fillRect(x, y, 24, 16)
    }
    const encoded = await new Promise<Blob | null>((resolve) => canvas.toBlob(resolve, 'image/jpeg', 0.95))
    if (!encoded) throw new Error('JPEG unavailable')
    const original = new Uint8Array(await encoded.arrayBuffer()), marker = new TextEncoder().encode('PRIVATE_GPS')
    const tiff = new Uint8Array(26 + marker.length), view = new DataView(tiff.buffer)
    tiff.set([73, 73]); view.setUint16(2, 42, true); view.setUint32(4, 8, true); view.setUint16(8, 1, true)
    view.setUint16(10, 0x112, true); view.setUint16(12, 3, true); view.setUint32(14, 1, true); view.setUint16(18, 6, true); tiff.set(marker, 26)
    const segment = new Uint8Array(10 + tiff.length); segment.set([255, 225, 0, segment.length - 2, 69, 120, 105, 102, 0, 0]); segment.set(tiff, 10)
    const raw = new Uint8Array(original.length + segment.length)
    raw.set(original.subarray(0, 2)); raw.set(segment, 2); raw.set(original.subarray(2), 2 + segment.length)
    const prepared = await api.prepareSelectedImage(new File([raw], 'private-photo.jpg'), new AbortController().signal)
    if (prepared.kind !== 'prepared') throw new Error(`Preparation failed: ${prepared.kind}`)
    const frame = await api.normalizeSelectedImage(prepared, new AbortController().signal)
    if (frame.kind !== 'imported') throw new Error(`Normalization failed: ${frame.kind}`)
    async function pixels(blob: Blob) {
      const image = await createImageBitmap(blob), output = document.createElement('canvas')
      output.width = image.width; output.height = image.height
      const target = output.getContext('2d')
      if (!target) throw new Error('Canvas unavailable')
      target.drawImage(image, 0, 0); image.close()
      const data = Array.from(target.getImageData(0, 0, output.width, output.height).data)
      const width = output.width, height = output.height
      output.width = 0; output.height = 0
      return { width, height, data }
    }
    const before = await pixels(encoded), after = await pixels(frame.blob)
    const samples = [[8, 12], [24, 12], [8, 36], [24, 36]].map(([x, y]) => {
      const source = (31 - x!) * before.width + y!, target = y! * after.width + x!
      return [0, 1, 2].every((channel) => Math.abs(before.data[source * 4 + channel]! - after.data[target * 4 + channel]!) <= 20)
    })
    const privateMetadata = new TextDecoder().decode(await frame.blob.arrayBuffer()).includes('PRIVATE_GPS')
    canvas.width = 0; canvas.height = 0
    return { kind: frame.kind, width: frame.width, height: frame.height, mime: frame.blob.type,
      decodedWidth: after.width, decodedHeight: after.height, samples, privateMetadata }
  })
  expect(result).toEqual({ kind: 'imported', width: 32, height: 48, mime: 'image/jpeg', decodedWidth: 32, decodedHeight: 48,
    samples: [true, true, true, true], privateMetadata: false })
  expect(writes).toEqual([])
})

async function isolateSessionTransport(page: Page, transport: 'broadcast' | 'storage') {
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
}

test('documents Chromium canvas precision for an independently encoded 16-bit PNG', async ({ page, prepareBundle }) => {
  await install(page, prepareBundle)
  await page.goto('/login')
  const result = await page.evaluate(async () => {
    const modulePath = '/assets/image-prepare-private.js', api = await import(modulePath)
    const raw = Uint8Array.from(atob('iVBORw0KGgoAAAANSUhEUgAAAAIAAAADEAAAAADMEV0eAAAAF0lEQVR4nGNgYGBgZGRk1Ldg2JT3/z8ACd0Digep9dgAAAAASUVORK5CYII='), (c) => c.charCodeAt(0))
    const prepared = await api.prepareSelectedImage(new File([raw], 'sixteen-bit.png'), new AbortController().signal)
    if (prepared.kind !== 'prepared') throw new Error(`Preparation failed: ${prepared.kind}`)
    const frame = await api.normalizeSelectedImage(prepared, new AbortController().signal)
    if (frame.kind !== 'imported') throw new Error(`Normalization failed: ${frame.kind}`)
    const output = new Uint8Array(await frame.blob.arrayBuffer())
    return { inputDepth: raw[24], outputDepth: output[24], mime: frame.blob.type, width: frame.width, height: frame.height }
  })
  expect(result).toEqual({ inputDepth: 16, outputDepth: 8, mime: 'image/png', width: 2, height: 3 })
})

for (const transport of ['broadcast', 'storage'] as const) test(`cancels a held preparation after real cross-window logout using ${transport} only`, async ({ page, context, application, prepareBundle }) => {
  await isolateSessionTransport(page, transport)
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

for (const transport of ['broadcast', 'storage'] as const) test(`discards a held native image decode after real logout using ${transport} only`, async ({ page, context, application, prepareBundle }) => {
  await isolateSessionTransport(page, transport)
  await holdNativePhotoCallbacks(page)
  await provision(page, application)
  await install(page, prepareBundle)
  const writes = watchDomainWrites(page, ['/api/v1/media', '/api/v1/items', '/api/v1/inventory'])
  await page.goto('/scan')
  await expect(page.getByRole('textbox', { name: messages.scan.input })).toBeVisible()
  await startHeldNormalization(page, 'decode')
  const other = await context.newPage()
  try {
    await other.goto('/settings/account')
    await other.getByRole('button', { name: messages.auth.logout, exact: true }).click()
    expect(await page.evaluate(() => window.__normalizeProbe.kind)).toBeUndefined()
    await other.getByRole('alertdialog').getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await expect.poll(() => page.evaluate(() => window.__prepareSignals)).toBeGreaterThan(0)
    await expect.poll(() => page.evaluate(() => window.__normalizeProbe.kind)).toBe('cancelled')
    expect(await page.evaluate(() => window.__prepareHidden)).toBe(false)
    expect(await page.evaluate(() => window.__normalizeProbe.closed)).toBe(0)
    await page.evaluate(() => { window.__normalizeProbe.release?.(); window.__normalizeProbe.release = undefined })
    await expect.poll(() => page.evaluate(() => window.__normalizeProbe.closed)).toBe(1)
    expect(await page.evaluate(() => window.__normalizeProbe.kind)).toBe('cancelled')
    expect(writes).toEqual([])
  } finally { await other.close() }
})

for (const mode of ['decode', 'encode'] as const) test(`discards held native ${mode} across an actual BFCache restoration`, async ({ page, application, prepareBundle }) => {
  await holdNativePhotoCallbacks(page)
  await provision(page, application)
  const diagnostics = await permitBFCache(page, application.url)
  await install(page, prepareBundle)
  const writes = watchDomainWrites(page, ['/api/v1/media', '/api/v1/items', '/api/v1/inventory'])
  await page.goto('/scan')
  await expect(page.getByRole('textbox', { name: messages.scan.input })).toBeVisible()
  await page.waitForLoadState('networkidle')
  await startHeldNormalization(page, mode)
  await leaveDocument(page, false)
  await returnFromBFCache(page, diagnostics)
  await expect(page.getByText(messages.scan.restoreReady)).toBeVisible()
  await expect.poll(() => page.evaluate(() => window.__normalizeProbe.kind)).toBe('cancelled')
  if (mode === 'encode') expect(await page.evaluate(() => [window.__normalizeProbe.canvas?.width, window.__normalizeProbe.canvas?.height])).toEqual([0, 0])
  await page.evaluate(() => { window.__normalizeProbe.release?.(); window.__normalizeProbe.release = undefined })
  if (mode === 'decode') await expect.poll(() => page.evaluate(() => window.__normalizeProbe.closed)).toBe(1)
  expect(await page.evaluate(() => window.__normalizeProbe.kind)).toBe('cancelled')
  expect(writes).toEqual([])
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
