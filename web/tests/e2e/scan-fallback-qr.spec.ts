import { BarcodeFormat, EncodeHintType, QRCodeWriter } from '@zxing/library'
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { messages } from '../../src/lib/messages.ts'
import { test as applicationTest, expect } from './auth-fixture.ts'
import { provision, watchDomainWrites } from './domain-helpers.ts'

const rawQR = 'http://127.0.0.1:35917/u/62aca93f-e021-426f-86d9-000000000013'

// The video and reader use the same ZXing package; this is not independent
// optical or physical-device evidence.
function qrVideo(): Buffer {
  const hints = new Map([[EncodeHintType.CHARACTER_SET, 'UTF-8'], [EncodeHintType.ERROR_CORRECTION, 'M']])
  const matrix = new QRCodeWriter().encode(rawQR, BarcodeFormat.QR_CODE, 0, 0, hints)
  const width = matrix.getWidth() * 4, height = matrix.getHeight() * 4
  const pixels = Buffer.alloc(width * height, 235)
  for (let y = 0; y < height; y++) for (let x = 0; x < width; x++) {
    if (matrix.get(Math.floor(x / 4), Math.floor(y / 4))) pixels[y * width + x] = 16
  }
  return Buffer.concat([Buffer.from(`YUV4MPEG2 W${width} H${height} F30:1 Ip A1:1 C420jpeg\nFRAME\n`), pixels, Buffer.alloc(width * height / 2, 128)])
}

type QRScanProbe = { workers: string[]; terminated: number; tracks: MediaStreamTrack[] }
declare global { interface Window { __qrScanProbe: QRScanProbe } }
const test = applicationTest.extend<{}, { qrVideoFile: string }>({
  qrVideoFile: [async ({ browserName }, provideFixture) => {
    const directory = mkdtempSync(join(tmpdir(), `homebox-qr-video-${browserName}-`)), file = join(directory, 'qr.y4m')
    try {
      writeFileSync(file, qrVideo(), { mode: 0o600, flag: 'wx' })
      await provideFixture(file)
    } finally { rmSync(directory, { recursive: true, force: true }) }
  }, { scope: 'worker' }],
  browser: [async ({ playwright, launchOptions, qrVideoFile }, provideFixture) => {
    const browser = await playwright.chromium.launch({ ...launchOptions, args: [...(launchOptions.args ?? []), '--use-fake-device-for-media-stream', '--use-fake-ui-for-media-stream', `--use-file-for-fake-video-capture=${qrVideoFile}`] })
    try { await provideFixture(browser) } finally { await browser.close() }
  }, { scope: 'worker' }],
})
test.use({ permissions: ['camera'], trace: 'off', screenshot: 'off', video: 'off' })

test('decodes a generated QR through the actual camera worker without opening an external link', async ({ page, application }) => {
  await page.addInitScript(() => {
    const probe: QRScanProbe = { workers: [], terminated: 0, tracks: [] }
    window.__qrScanProbe = probe
    Object.defineProperty(window, 'BarcodeDetector', { configurable: true, value: undefined })
    const OriginalWorker = window.Worker
    window.Worker = new Proxy(OriginalWorker, { construct(target, args, newTarget) {
      const worker = Reflect.construct(target, args, newTarget) as Worker
      probe.workers.push(String(args[0]))
      const terminate = worker.terminate.bind(worker)
      worker.terminate = () => { probe.terminated++; terminate() }
      return worker
    } })
    const devices = navigator.mediaDevices, get = devices.getUserMedia.bind(devices)
    devices.getUserMedia = async (constraints) => {
      const stream = await get(constraints)
      probe.tracks.push(...stream.getVideoTracks())
      return stream
    }
  })
  await provision(page, application)
  const writes = watchDomainWrites(page, ['/api/v1/items', '/api/v1/barcodes', '/api/v1/inventory']), lookups: string[] = []
  page.on('request', (request) => { if (new URL(request.url()).pathname === '/api/v1/search/barcodes') lookups.push(request.url()) })
  await page.goto('/scan')
  const text = messages.scan
  await expect(page.getByText(text.fallbackFormats, { exact: true })).toBeVisible()
  expect(await page.evaluate(() => window.__qrScanProbe.tracks.length)).toBe(0)
  await page.getByRole('button', { name: text.cameraStart }).click()
  const results = page.getByRole('list', { name: text.nativeResults })
  await expect(results).toContainText(JSON.stringify(rawQR), { timeout: 15_000 })
  if (new URL(rawQR).origin === application.url) {
    await expect(results.getByRole('link', { name: text.nativeOpenUnit })).toHaveAttribute('href', '/units/62aca93f-e021-426f-86d9-000000000013')
  } else {
    await expect(results.getByText(text.nativeUntrustedQR)).toBeVisible()
    await expect(results.getByRole('link')).toHaveCount(0)
  }
  await expect(page).toHaveURL(`${application.url}/scan`)
  await expect.poll(() => page.evaluate(() => window.__qrScanProbe.tracks[0]?.readyState)).toBe('ended')
  expect(await page.evaluate(() => window.__qrScanProbe.terminated)).toBeGreaterThan(0)
  const workers = await page.evaluate(() => window.__qrScanProbe.workers)
  expect(workers.length).toBeGreaterThan(0)
  expect(workers.every((url) => new URL(url, application.url).origin === application.url)).toBe(true)
  expect(lookups).toEqual([]); expect(writes).toEqual([])
})
