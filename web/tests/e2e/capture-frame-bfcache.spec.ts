import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import react from '@vitejs/plugin-react'
import { build } from 'vite'
import { messages } from '../../src/lib/messages.ts'
import { test as applicationTest, expect } from './auth-fixture.ts'
import { leaveDocument, permitBFCache, returnFromBFCache } from './bfcache-helpers.ts'
import { provision, watchDomainWrites } from './domain-helpers.ts'

type CaptureBFProbe = { tracks: MediaStreamTrack[]; held: boolean; kind?: string; release?: () => void; renderCanvas?: HTMLCanvasElement }
declare global { interface Window { __captureBF: CaptureBFProbe; __captureBFUnmount: () => void } }

const test = applicationTest.extend<{}, { cameraBundle: string }>({
  cameraBundle: [async ({ browserName }, provideFixture) => {
    const directory = mkdtempSync(join(tmpdir(), `homebox-frame-bfcache-${browserName}-`))
    try {
      await build({ configFile: false, logLevel: 'silent', plugins: [react()], define: { 'process.env.NODE_ENV': JSON.stringify('production') },
        resolve: { alias: { '@': fileURLToPath(new URL('../../src', import.meta.url)) } },
        build: { outDir: directory, minify: false, lib: { entry: fileURLToPath(new URL('./camera-harness-entry.tsx', import.meta.url)), formats: ['es'], fileName: 'camera-harness' } } })
      await provideFixture(join(directory, 'camera-harness.js'))
    } finally { rmSync(directory, { recursive: true, force: true }) }
  }, { scope: 'worker' }],
})

// The video is Chromium's fake device and the helper exists only in a private
// test bundle. BFCache is real; no physical camera or capture screen is tested.
test.use({ launchOptions: { channel: 'chromium', ignoreDefaultArgs: ['--disable-back-forward-cache'], args: ['--use-fake-device-for-media-stream', '--use-fake-ui-for-media-stream'] }, permissions: ['camera'], trace: 'off', screenshot: 'off', video: 'off' })

test('discards a pending encoded frame across actual BFCache restoration', async ({ page, cameraBundle, application }) => {
  await page.addInitScript(() => {
    const devices = navigator.mediaDevices
    if (!devices?.getUserMedia) return
    const acquire = devices.getUserMedia.bind(devices), probe: CaptureBFProbe = { tracks: [], held: false }
    window.__captureBF = probe
    devices.getUserMedia = async (constraints) => {
      const stream = await acquire(constraints)
      probe.tracks.push(...stream.getTracks())
      return stream
    }
  })
  await provision(page, application)
  const diagnostics = await permitBFCache(page, application.url), writes = watchDomainWrites(page, ['/api/v1/media', '/api/v1/items'])
  await page.route('**/assets/camera-harness.js', (route) => route.fulfill({ path: cameraBundle, contentType: 'text/javascript', headers: { 'Cache-Control': 'no-store' } }))
  await page.goto('/scan')
  await page.evaluate(async () => {
    const modulePath = '/assets/camera-harness.js', harness = await import(modulePath)
    window.__captureBFUnmount = harness.mountCameraHarness()
  })
  await page.getByRole('button', { name: 'Start camera' }).click()
  await expect(page.locator('[aria-label="Camera state"]')).toHaveText('active')
  await expect.poll(() => page.locator('video[aria-label="Live camera"]').evaluate((video: HTMLVideoElement) => video.videoWidth)).toBeGreaterThan(0)
  await page.evaluate(async () => {
    const modulePath = '/assets/camera-harness.js', harness = await import(modulePath)
    const video = document.querySelector<HTMLVideoElement>('video[aria-label="Live camera"]'), probe = window.__captureBF
    if (!video || !(video.srcObject instanceof MediaStream)) throw new Error('Missing live video')
    const native = HTMLCanvasElement.prototype.toBlob
    HTMLCanvasElement.prototype.toBlob = function (callback, type, quality) {
      native.call(this, (blob) => { probe.held = true; probe.release = () => callback(blob) }, type, quality)
    }
    void harness.captureVideoFrame(video, video.srcObject, new AbortController().signal).then((result: { kind: string }) => { probe.kind = result.kind })
  })
  await expect.poll(() => page.evaluate(() => window.__captureBF.held)).toBe(true)
  await leaveDocument(page, false)
  await returnFromBFCache(page, diagnostics)
  await expect(page.getByText(messages.scan.restoreReady)).toBeVisible()
  expect(await page.evaluate(() => window.__captureBF.tracks.map((track) => track.readyState))).toEqual(['ended'])
  await expect.poll(() => page.evaluate(() => window.__captureBF.kind)).toBe('cancelled')
  await page.evaluate(() => { window.__captureBF.release?.() })
  expect(await page.evaluate(() => window.__captureBF.kind)).toBe('cancelled')
  await page.getByRole('button', { name: messages.scan.restoreContinue }).click()
  await expect(page.getByRole('button', { name: messages.scan.cameraStart })).toBeEnabled()
  await page.evaluate(() => { window.__captureBFUnmount() })
  expect(writes).toEqual([])
})

test('discards a pending rendered photo across actual BFCache restoration', async ({ page, cameraBundle, application }) => {
  await page.addInitScript(() => {
    const devices = navigator.mediaDevices
    if (!devices?.getUserMedia) return
    const acquire = devices.getUserMedia.bind(devices), probe: CaptureBFProbe = { tracks: [], held: false }
    window.__captureBF = probe
    devices.getUserMedia = async (constraints) => {
      const stream = await acquire(constraints)
      probe.tracks.push(...stream.getTracks())
      return stream
    }
  })
  await provision(page, application)
  const diagnostics = await permitBFCache(page, application.url), writes = watchDomainWrites(page, ['/api/v1/media', '/api/v1/items'])
  await page.route('**/assets/camera-harness.js', (route) => route.fulfill({ path: cameraBundle, contentType: 'text/javascript', headers: { 'Cache-Control': 'no-store' } }))
  await page.goto('/scan')
  await page.evaluate(async () => {
    const modulePath = '/assets/camera-harness.js', harness = await import(modulePath)
    window.__captureBFUnmount = harness.mountCameraHarness()
  })
  await page.getByRole('button', { name: 'Start camera' }).click()
  await expect(page.locator('[aria-label="Camera state"]')).toHaveText('active')
  await expect.poll(() => page.locator('video[aria-label="Live camera"]').evaluate((video: HTMLVideoElement) => video.videoWidth)).toBeGreaterThan(0)
  await page.evaluate(async () => {
    const modulePath = '/assets/camera-harness.js', harness = await import(modulePath)
    const video = document.querySelector<HTMLVideoElement>('video[aria-label="Live camera"]'), probe = window.__captureBF
    if (!video || !(video.srcObject instanceof MediaStream)) throw new Error('Missing live video')
    const captured = await harness.captureVideoFrame(video, video.srcObject, new AbortController().signal)
    if (captured.kind !== 'captured') throw new Error(`Capture failed: ${captured.kind}`)
    const geometry = harness.newPhotoGeometry(captured.width, captured.height)
    if (!geometry) throw new Error('Missing capture geometry')
    const native = HTMLCanvasElement.prototype.toBlob
    HTMLCanvasElement.prototype.toBlob = function (callback, type, quality) {
      probe.renderCanvas = this
      native.call(this, (blob) => { probe.held = true; probe.release = () => callback(blob) }, type, quality)
    }
    void harness.renderCapturedPhoto(captured, geometry, new AbortController().signal).then((result: { kind: string }) => { probe.kind = result.kind })
  })
  await expect.poll(() => page.evaluate(() => window.__captureBF.held)).toBe(true)
  await leaveDocument(page, false)
  await returnFromBFCache(page, diagnostics)
  await expect(page.getByText(messages.scan.restoreReady)).toBeVisible()
  expect(await page.evaluate(() => window.__captureBF.tracks.map((track) => track.readyState))).toEqual(['ended'])
  await expect.poll(() => page.evaluate(() => window.__captureBF.kind)).toBe('cancelled')
  expect(await page.evaluate(() => [window.__captureBF.renderCanvas?.width, window.__captureBF.renderCanvas?.height])).toEqual([0, 0])
  await page.evaluate(() => { window.__captureBF.release?.() })
  expect(await page.evaluate(() => window.__captureBF.kind)).toBe('cancelled')
  await page.getByRole('button', { name: messages.scan.restoreContinue }).click()
  await expect(page.getByRole('button', { name: messages.scan.cameraStart })).toBeEnabled()
  await page.evaluate(() => { window.__captureBFUnmount() })
  expect(writes).toEqual([])
})
