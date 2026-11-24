import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { build } from 'vite'
import { test as applicationTest, expect } from './auth-fixture.ts'

const test = applicationTest.extend<{}, { imagePreflightBundle: string }>({
  imagePreflightBundle: [async ({ browserName }, provideFixture) => {
    const directory = mkdtempSync(join(tmpdir(), `homebox-image-preflight-${browserName}-`))
    try {
      await build({ configFile: false, logLevel: 'silent',
        resolve: { alias: { '@': fileURLToPath(new URL('../../src', import.meta.url)) } },
        build: { outDir: directory, minify: false, lib: { entry: fileURLToPath(new URL('../../src/features/capture/image-container.ts', import.meta.url)), formats: ['es'], fileName: 'image-preflight' } } })
      await provideFixture(join(directory, 'image-preflight.js'))
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
