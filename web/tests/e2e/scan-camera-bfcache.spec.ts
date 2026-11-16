import { messages } from '../../src/lib/messages.ts'
import { test, expect } from './auth-fixture.ts'
import { leaveDocument, permitBFCache, returnFromBFCache } from './bfcache-helpers.ts'
import { provision, watchDomainWrites } from './domain-helpers.ts'

type BFCameraProbe = { calls: number; tracks: MediaStreamTrack[] }
declare global { interface Window { __bfScanCamera: BFCameraProbe } }

// Use full Chromium so BFCache is available, with a fake camera only; no real
// device or operating-system permission prompt is exercised by this test.
test.use({ launchOptions: { channel: 'chromium', ignoreDefaultArgs: ['--disable-back-forward-cache'], args: ['--use-fake-device-for-media-stream', '--use-fake-ui-for-media-stream'] }, permissions: ['camera'], trace: 'off', screenshot: 'off', video: 'off' })
const scan = messages.scan

test('stops the fake camera before actual BFCache restoration without reopening it', async ({ page, application }) => {
  await page.addInitScript(() => {
    const devices = navigator.mediaDevices
    if (!devices?.getUserMedia) return
    const native = devices.getUserMedia.bind(devices), probe: BFCameraProbe = { calls: 0, tracks: [] }
    window.__bfScanCamera = probe
    devices.getUserMedia = async (constraints) => {
      probe.calls++
      const stream = await native(constraints)
      probe.tracks.push(...stream.getTracks())
      return stream
    }
  })
  await provision(page, application)
  const diagnostics = await permitBFCache(page, application.url), writes = watchDomainWrites(page, ['/api/v1/items', '/api/v1/barcodes'])
  await page.goto('/scan')
  await page.getByRole('textbox', { name: scan.input }).fill('Private camera draft')
  await page.getByRole('button', { name: scan.cameraStart }).click()
  const video = page.getByLabel(scan.cameraVideo)
  await expect.poll(() => video.evaluate((element: HTMLVideoElement) => element.videoWidth)).toBeGreaterThan(0)
  expect(await page.evaluate(() => window.__bfScanCamera.calls)).toBe(1)
  await leaveDocument(page, false)
  await returnFromBFCache(page, diagnostics)
  await expect(page.getByText(scan.restoreReady)).toBeVisible()
  expect(await page.evaluate(() => window.__bfScanCamera.tracks.map((track) => track.readyState))).toEqual(['ended'])
  await expect(page.getByLabel(scan.cameraVideo)).toHaveCount(0)
  await expect(page.locator('body')).not.toContainText('Private camera draft')
  await page.getByRole('button', { name: scan.restoreContinue }).click()
  await expect(page.getByRole('button', { name: scan.cameraStart })).toBeEnabled()
  await expect(page.getByRole('textbox', { name: scan.input })).toHaveValue('')
  expect(await page.evaluate(() => window.__bfScanCamera.calls)).toBe(1)
  expect(writes).toEqual([])
})
