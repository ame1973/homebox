import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { build } from 'vite'
import { test as applicationTest, expect } from './auth-fixture.ts'

const test = applicationTest.extend<{}, { imagePreflightBundle: string }>({
  imagePreflightBundle: [async ({ browserName }, provideFixture) => {
    const directory = mkdtempSync(join(tmpdir(), `homebox-image-preflight-${browserName}-`))
    try {
      const preflight = fileURLToPath(new URL('../../src/features/capture/image-container.ts', import.meta.url))
      const prepare = fileURLToPath(new URL('../../src/features/capture/image-decode-input.ts', import.meta.url))
      const entry = join(directory, 'entry.ts')
      writeFileSync(entry, `export * from ${JSON.stringify(preflight)}; export * from ${JSON.stringify(prepare)};\n`)
      await build({ configFile: false, logLevel: 'silent',
        resolve: { alias: { '@': fileURLToPath(new URL('../../src', import.meta.url)) } },
        build: { outDir: join(directory, 'out'), minify: false, lib: { entry, formats: ['es'], fileName: 'image-preflight' } } })
      await provideFixture(join(directory, 'out', 'image-preflight.js'))
    } finally { rmSync(directory, { recursive: true, force: true }) }
  }, { scope: 'worker' }],
})

test.use({ trace: 'off', screenshot: 'off', video: 'off' })

// The helper is loaded only from this private bundle. These browser-generated
// and independent synthetic images are not a file picker or device acceptance.
test('checks actual browser encodings, transparent WebP and all JPEG orientations without writing', async ({ page, imagePreflightBundle }) => {
  const writes: string[] = []
  page.on('request', (request) => {
    if (new URL(request.url()).pathname.startsWith('/api/') && request.method() !== 'GET') writes.push(request.method())
  })
  await page.route('**/assets/image-preflight.js', (route) => route.fulfill({ path: imagePreflightBundle, contentType: 'text/javascript', headers: { 'Cache-Control': 'no-store' } }))
  await page.goto('/login')
  const result = await page.evaluate(async () => {
    const modulePath = '/assets/image-preflight.js', { inspectImageContainer } = await import(modulePath)
    const canvas = document.createElement('canvas'); canvas.width = 4; canvas.height = 3
    const context = canvas.getContext('2d')
    if (!context) throw new Error('Canvas unavailable')
    context.fillStyle = '#ff0000'; context.fillRect(0, 0, 2, 3)
    context.fillStyle = 'rgba(0,255,0,0.5)'; context.fillRect(2, 0, 2, 3)
    const encoded: { mime: string; actual: string; info: unknown; decoded: number[] }[] = []
    let jpeg: Uint8Array | undefined
    for (const mime of ['image/jpeg', 'image/png', 'image/webp']) {
      const blob = await new Promise<Blob | null>((resolve) => canvas.toBlob(resolve, mime, 0.9))
      if (!blob) throw new Error('Encoding unavailable')
      const raw = new Uint8Array(await blob.arrayBuffer()), info = inspectImageContainer(raw), bitmap = await createImageBitmap(blob)
      encoded.push({ mime, actual: blob.type, info, decoded: [bitmap.width, bitmap.height] })
      bitmap.close()
      if (mime === 'image/jpeg') jpeg = raw
    }
    if (!jpeg) throw new Error('Missing JPEG')
    const orientationResults: { value: number; info: unknown; decoded: number[] }[] = []
    for (let value = 1; value <= 8; value++) {
      const tiff = new Uint8Array(26), view = new DataView(tiff.buffer)
      tiff.set([73, 73]); view.setUint16(2, 42, true); view.setUint32(4, 8, true); view.setUint16(8, 1, true)
      view.setUint16(10, 0x112, true); view.setUint16(12, 3, true); view.setUint32(14, 1, true); view.setUint16(18, value, true)
      const segment = new Uint8Array(4 + 6 + tiff.length)
      segment.set([255, 225, 0, segment.length - 2, 69, 120, 105, 102, 0, 0]); segment.set(tiff, 10)
      const raw = new Uint8Array(jpeg.length + segment.length)
      raw.set(jpeg.subarray(0, 2)); raw.set(segment, 2); raw.set(jpeg.subarray(2), 2 + segment.length)
      const info = inspectImageContainer(raw), bitmap = await createImageBitmap(new Blob([raw], { type: 'image/jpeg' }))
      orientationResults.push({ value, info, decoded: [bitmap.width, bitmap.height] }); bitmap.close()
    }
    const lossyAlpha = 'UklGRogAAABXRUJQVlA4WAoAAAAQAAAAAQAAAgAAQUxQSAcAAAAA/4D/AP9AAFZQOCBaAAAAsAMAnQEqAgADAADAEiWoAnS6AfgB+gEwA/gGEAev/dgAMDSy0AAA/QPp3b8Z/+/9lv/2xEaL/+l4PvTutm0CAx6d1s21V//ceX/4VWu0YoCd+0YpiN/zcAAA'
    const animated = 'UklGRqIAAABXRUJQVlA4WAoAAAASAAAAAQAAAgAAQU5JTQYAAAAAAAAAAABBTk1GRAAAAAAAAAAAAAEAAAIAAHgAAAJWUDhMKwAAAC8BgAAQLyAQSNofeo35FxAU/B+dQCAJbbPVg0xc7AAEApSxYoMHIvof7wAAQU5NRioAAAAAAAAAAAABAAACAAB4AAAAVlA4TBEAAAAvAYAAAAdQlFKUp/+BiOh/AAA='
    const bytes = (data: string) => Uint8Array.from(atob(data), (character) => character.charCodeAt(0))
    const alphaBytes = bytes(lossyAlpha), alphaInfo = inspectImageContainer(alphaBytes)
    const alphaBitmap = await createImageBitmap(new Blob([alphaBytes], { type: 'image/webp' }))
    canvas.width = 2; canvas.height = 3; context.drawImage(alphaBitmap, 0, 0); alphaBitmap.close()
    const alpha = Array.from({ length: 6 }, (_, index) => context.getImageData(index % 2, Math.floor(index / 2), 1, 1).data[3])
    const animation = inspectImageContainer(bytes(animated))
    canvas.width = 0; canvas.height = 0
    return { encoded, orientationResults, alphaInfo, alpha, animation }
  })
  for (const item of result.encoded) {
    expect(item.actual).toBe(item.mime)
    expect(item.info).toEqual({ kind: 'inspected', mime: item.mime, width: 4, height: 3, orientation: 1 })
    expect(item.decoded).toEqual([4, 3])
  }
  for (const item of result.orientationResults) {
    expect(item.info).toEqual({ kind: 'inspected', mime: 'image/jpeg', width: 4, height: 3, orientation: item.value })
    expect(item.decoded).toEqual(item.value >= 5 ? [3, 4] : [4, 3])
  }
  expect(result.alphaInfo).toEqual({ kind: 'inspected', mime: 'image/webp', width: 2, height: 3, orientation: 1 })
  expect(result.alpha).toEqual([255, 128, 255, 0, 255, 64])
  expect(result.animation).toEqual({ kind: 'invalid' })
  expect(writes).toEqual([])
})

test('decodes metadata-minimized intermediates without changing pixels or transparency', async ({ page, imagePreflightBundle }) => {
  await page.route('**/assets/image-preflight.js', (route) => route.fulfill({ path: imagePreflightBundle, contentType: 'text/javascript', headers: { 'Cache-Control': 'no-store' } }))
  await page.goto('/login')
  const results = await page.evaluate(async () => {
    const modulePath = '/assets/image-preflight.js', { prepareImageDecodeInput } = await import(modulePath)
    const text = (value: string) => Uint8Array.from(value, (c) => c.charCodeAt(0))
    const decoded = (value: string) => Uint8Array.from(atob(value), (c) => c.charCodeAt(0))
    const concat = (...parts: Uint8Array[]) => {
      const raw = new Uint8Array(parts.reduce((total, part) => total + part.length, 0))
      let pos = 0; for (const part of parts) { raw.set(part, pos); pos += part.length }; return raw
    }
    function tiff(orientation: number) {
      const raw = new Uint8Array(26), view = new DataView(raw.buffer)
      raw.set([73, 73]); view.setUint16(2, 42, true); view.setUint32(4, 8, true); view.setUint16(8, 1, true)
      view.setUint16(10, 0x112, true); view.setUint16(12, 3, true); view.setUint32(14, 1, true); view.setUint16(18, orientation, true)
      return concat(raw, text('PRIVATE_GPS'))
    }
    function jpegEXIF(raw: Uint8Array, orientation: number) {
      const data = concat(text('Exif\0\0'), tiff(orientation)), segment = new Uint8Array(data.length + 4)
      segment.set([255, 225]); new DataView(segment.buffer).setUint16(2, data.length + 2); segment.set(data, 4)
      return concat(raw.subarray(0, 2), segment, raw.subarray(2))
    }
    function crc32(raw: Uint8Array) {
      let value = 0xffffffff
      for (const byte of raw) { value ^= byte; for (let bit = 0; bit < 8; bit++) value = value >>> 1 ^ (value & 1 ? 0xedb88320 : 0) }
      return (value ^ 0xffffffff) >>> 0
    }
    function pngChunk(name: string, data: Uint8Array) {
      const raw = new Uint8Array(data.length + 12), view = new DataView(raw.buffer)
      view.setUint32(0, data.length); raw.set(text(name), 4); raw.set(data, 8); view.setUint32(data.length + 8, crc32(raw.subarray(4, data.length + 8)))
      return raw
    }
    function webpChunk(name: string, data: Uint8Array) {
      const raw = new Uint8Array(8 + data.length + (data.length & 1))
      raw.set(text(name)); new DataView(raw.buffer).setUint32(4, data.length, true); raw.set(data, 8); return raw
    }
    function webp(...chunks: Uint8Array[]) {
      const raw = concat(text('RIFF'), new Uint8Array(4), text('WEBP'), ...chunks)
      new DataView(raw.buffer).setUint32(4, raw.length - 8, true); return raw
    }
    const canvas = document.createElement('canvas'); canvas.width = 4; canvas.height = 3
    const context = canvas.getContext('2d')
    if (!context) throw new Error('Canvas unavailable')
    context.fillStyle = '#ee1020'; context.fillRect(0, 0, 2, 3)
    context.fillStyle = 'rgba(0,255,0,0.5)'; context.fillRect(2, 0, 2, 3)
    const blob = await new Promise<Blob | null>((resolve) => canvas.toBlob(resolve, 'image/jpeg', 0.9))
    if (!blob) throw new Error('JPEG unavailable')
    const jpeg = new Uint8Array(await blob.arrayBuffer())
    const png = decoded('iVBORw0KGgoAAAANSUhEUgAAAAIAAAADCAYAAAC56t6BAAAAHElEQVR4nAXBAQEAMAjAIHwyo9l8B5EcqnhhtB+Pawm7X97uPgAAAABJRU5ErkJggg==')
    const indexedPNG = decoded('iVBORw0KGgoAAAANSUhEUgAAAAIAAAADBAMAAABLxMOyAAAAMFBMVEX/AAAA/wAAAP///wD/AP8A//8AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAArHiQ1AAAABnRSTlP/gP8A/0AAeabcAAAADklEQVR42mNgZFBmcAUAALkAaq0usjMAAAAASUVORK5CYII=')
    const originalWebP = decoded('UklGRjYAAABXRUJQVlA4TCkAAAAvAYAAEC8w/wKCIv9HExAU+T+aQIDgzP+U4A0i6igIBNKQUgorHNH/WAA=')
    // Independently encoded CMYK JPEG with an Adobe color-transform header.
    const cmyk = decoded('/9j/7gAOQWRvYmUAZAAAAAAA/9sAQwADAgIDAgIDAwMDBAMDBAUIBQUEBAUKBwcGCAwKDAwLCgsLDQ4SEA0OEQ4LCxAWEBETFBUVFQwPFxgWFBgSFBUU/8AAFAgAAwACBEMRAE0RAFkRAEsRAP/EAB8AAAEFAQEBAQEBAAAAAAAAAAABAgMEBQYHCAkKC//EALUQAAIBAwMCBAMFBQQEAAABfQECAwAEEQUSITFBBhNRYQcicRQygZGhCCNCscEVUtHwJDNicoIJChYXGBkaJSYnKCkqNDU2Nzg5OkNERUZHSElKU1RVVldYWVpjZGVmZ2hpanN0dXZ3eHl6g4SFhoeIiYqSk5SVlpeYmZqio6Slpqeoqaqys7S1tre4ubrCw8TFxsfIycrS09TV1tfY2drh4uPk5ebn6Onq8fLz9PX29/j5+v/aAA4EQwBNAFkASwAAPwD4+/aF+OHiPwX8ffiX4e0/TfB9xYaT4m1Owt5tW8FaNqV48cV1IitPd3NpJPcSkKC00zvI7ZZ2ZiSfv79k7/k1j4N/9iZo3/pDDX1nq3/BMv8AZo1rVb3ULj4YW8dxdzPcSJaatf20KszFiEijuFSNcnhEUKowAAABX6p1/9k=')
    const cases = [
      ...Array.from({ length: 8 }, (_, index) => ({ mime: 'image/jpeg', original: jpeg, raw: jpegEXIF(jpeg, index + 1), orientation: index + 1 })),
      { mime: 'image/jpeg', original: cmyk, raw: jpegEXIF(cmyk, 3), orientation: 3 },
      { mime: 'image/png', original: png, raw: concat(png.subarray(0, 33), pngChunk('eXIf', tiff(6)), pngChunk('iCCP', text('PRIVATE_GPS')), png.subarray(33)), orientation: 6 },
      { mime: 'image/png', original: indexedPNG, raw: concat(indexedPNG.subarray(0, 33), pngChunk('eXIf', tiff(7)), indexedPNG.subarray(33)), orientation: 7 },
      { mime: 'image/webp', original: originalWebP, raw: webp(webpChunk('VP8X', new Uint8Array([0x3c, 0, 0, 0, 1, 0, 0, 2, 0, 0])),
        webpChunk('ICCP', text('PRIVATE_GPS')), originalWebP.subarray(12), webpChunk('EXIF', tiff(8)), webpChunk('XMP ', text('PRIVATE_GPS'))), orientation: 8 },
    ]
    async function pixels(raw: Uint8Array<ArrayBuffer>, mime: string) {
      const bitmap = await createImageBitmap(new Blob([raw], { type: mime }))
      canvas.width = bitmap.width; canvas.height = bitmap.height; context!.drawImage(bitmap, 0, 0); bitmap.close()
      return { width: canvas.width, height: canvas.height, data: Array.from(context!.getImageData(0, 0, canvas.width, canvas.height).data) }
    }
    const results = []
    for (const item of cases) {
      const prepared = prepareImageDecodeInput(item.raw)
      if (prepared.kind !== 'prepared') throw new Error(`Preparation failed: ${prepared.kind}`)
      const before = await pixels(item.original, item.mime), after = await pixels(prepared.bytes, item.mime)
      const privateBytes = new TextDecoder().decode(prepared.bytes).includes('PRIVATE_GPS')
      const trailing = new Uint8Array(prepared.bytes.buffer, prepared.bytes.byteOffset + prepared.bytes.byteLength)
      results.push({ mime: prepared.mime, orientation: prepared.orientation, expectedOrientation: item.orientation,
        width: before.width, height: before.height, pixelsEqual: JSON.stringify(before) === JSON.stringify(after), privateBytes,
        cleanTail: trailing.every((value) => value === 0), alpha: item.mime === 'image/jpeg' ? [] : after.data.filter((_, index) => index % 4 === 3) })
    }
    canvas.width = 0; canvas.height = 0
    return results
  })
  expect(results).toHaveLength(12)
  for (const result of results) {
    expect(result.orientation).toBe(result.expectedOrientation)
    expect(result.pixelsEqual).toBe(true)
    expect(result.privateBytes).toBe(false)
    expect(result.cleanTail).toBe(true)
    if (result.mime !== 'image/jpeg') expect(result.alpha).toEqual([255, 128, 255, 0, 255, 64])
  }
})
