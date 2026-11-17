import { messages } from '../../src/lib/messages.ts'
import { test, expect } from './auth-fixture.ts'
import { leaveDocument, permitBFCache, returnFromBFCache } from './bfcache-helpers.ts'
import { provision, watchDomainWrites } from './domain-helpers.ts'

type CaptureBFCacheProbe = { tracks: MediaStreamTrack[]; created: string[]; revoked: string[]; held: boolean; release?: () => void }
declare global { interface Window { __capturePageBF: CaptureBFCacheProbe } }

// Real Chromium BFCache and fake video pixels are exercised here; no physical
// camera, operating-system permission prompt or power-loss behavior is tested.
test.use({ launchOptions: { channel: 'chromium', ignoreDefaultArgs: ['--disable-back-forward-cache'], args: ['--use-fake-device-for-media-stream', '--use-fake-ui-for-media-stream'] }, permissions: ['camera'], trace: 'off', screenshot: 'off', video: 'off' })
const text = messages.capture, camera = messages.scan

test('redacts photos and a pending frame before BFCache and ignores its late encoder callback', async ({ page, application }) => {
  await page.addInitScript(() => {
    const devices = navigator.mediaDevices
    if (!devices?.getUserMedia) return
    const get = devices.getUserMedia.bind(devices), create = URL.createObjectURL.bind(URL), revoke = URL.revokeObjectURL.bind(URL)
    const probe: CaptureBFCacheProbe = { tracks: [], created: [], revoked: [], held: false }
    window.__capturePageBF = probe
    devices.getUserMedia = async (constraints) => {
      const stream = await get(constraints)
      probe.tracks.push(...stream.getTracks())
      return stream
    }
    URL.createObjectURL = (blob) => { const url = create(blob); probe.created.push(url); return url }
    URL.revokeObjectURL = (url) => { probe.revoked.push(url); revoke(url) }
  })
  await provision(page, application)
  const diagnostics = await permitBFCache(page, application.url), writes = watchDomainWrites(page, ['/api/v1/media', '/api/v1/items'])
  await page.goto('/photos/capture?owner_type=unit&owner_id=private-owner')
  await page.getByRole('button', { name: camera.cameraStart }).click()
  const video = page.getByLabel(camera.cameraVideo)
  await expect.poll(() => video.evaluate((element: HTMLVideoElement) => element.videoWidth)).toBeGreaterThan(0)
  await page.getByRole('button', { name: text.take }).click()
  await expect(page.getByRole('img', { name: text.preview })).toBeVisible()
  await page.getByRole('textbox', { name: text.description }).fill('private capture description')
  await page.getByRole('button', { name: camera.cameraStart }).click()
  await expect.poll(() => video.evaluate((element: HTMLVideoElement) => element.videoWidth)).toBeGreaterThan(0)
  await page.evaluate(() => {
    const native = HTMLCanvasElement.prototype.toBlob, probe = window.__capturePageBF
    HTMLCanvasElement.prototype.toBlob = function (callback, type, quality) {
      native.call(this, (blob) => { probe.held = true; probe.release = () => callback(blob) }, type, quality)
    }
  })
  await page.getByRole('button', { name: text.take }).click()
  await expect.poll(() => page.evaluate(() => window.__capturePageBF.held)).toBe(true)
  await leaveDocument(page, true)
  await returnFromBFCache(page, diagnostics)
  await expect(page.getByRole('heading', { name: text.restoreTitle })).toBeVisible()
  await expect(page.getByText(camera.restoreReady)).toBeVisible()
  const state = await page.evaluate(() => ({ tracks: window.__capturePageBF.tracks.map((track) => track.readyState), created: window.__capturePageBF.created, revoked: window.__capturePageBF.revoked }))
  expect(state.tracks).toEqual(['ended', 'ended'])
  expect(state.revoked).toContain(state.created[0])
  await expect(page.locator('body')).not.toContainText('private capture description')
  await page.evaluate(() => { window.__capturePageBF.release?.() })
  await page.getByRole('button', { name: camera.restoreContinue }).click()
  await expect(page.getByRole('button', { name: camera.cameraStart })).toBeEnabled()
  await expect(page.getByText(text.empty)).toBeVisible()
  await expect(page.getByRole('img', { name: text.preview })).toHaveCount(0)
  expect(await page.evaluate(() => window.__capturePageBF.created.length)).toBe(1)
  expect(writes).toEqual([])
})
