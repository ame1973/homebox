import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import react from '@vitejs/plugin-react'
import type { Page, Route } from '@playwright/test'
import { build } from 'vite'
import { messages } from '../../src/lib/messages.ts'
import { test as applicationTest, expect } from './auth-fixture.ts'
import { provision } from './domain-helpers.ts'

type CameraProbe = {
  calls: { constraints: MediaStreamConstraints | undefined; previous: string[] }[]
  streams: MediaStream[]
  failures: string[]
  hold: boolean
  resume?: () => void
}
type FrameProbe = { held: boolean; kind?: string; release?: () => void }
declare global {
  interface Window { __cameraProbe: CameraProbe; __unmountCamera: () => void; __frameCaptureProbe: FrameProbe }
}

const test = applicationTest.extend<{}, { cameraBundle: string }>({
  cameraBundle: [async ({ browserName }, provideFixture) => {
    const directory = mkdtempSync(join(tmpdir(), `homebox-camera-harness-${browserName}-`))
    try {
      await build({ configFile: false, logLevel: 'silent', plugins: [react()],
        define: { 'process.env.NODE_ENV': JSON.stringify('production') },
        resolve: { alias: { '@': fileURLToPath(new URL('../../src', import.meta.url)) } },
        build: { outDir: directory, minify: false, lib: { entry: fileURLToPath(new URL('./camera-harness-entry.tsx', import.meta.url)), formats: ['es'], fileName: 'camera-harness' } } })
      await provideFixture(join(directory, 'camera-harness.js'))
    } finally { rmSync(directory, { recursive: true, force: true }) }
  }, { scope: 'worker' }],
})

// The camera is Chromium's fake device; no physical camera, credentials or image
// content is recorded in traces or screenshots.
test.use({ launchOptions: { args: ['--use-fake-device-for-media-stream', '--use-fake-ui-for-media-stream'] }, permissions: ['camera'], trace: 'off', screenshot: 'off', video: 'off' })

async function prepareCamera(page: Page, bundle: string, path = '/') {
  await page.route('**/assets/camera-harness.js', (route) => route.fulfill({ path: bundle, contentType: 'text/javascript', headers: { 'Cache-Control': 'no-store' } }))
  await page.addInitScript(() => {
    const devices = navigator.mediaDevices
    if (!devices?.getUserMedia) return
    const native = devices.getUserMedia.bind(devices)
    const probe: CameraProbe = { calls: [], streams: [], failures: [], hold: false }
    ;window.__cameraProbe = probe
    devices.getUserMedia = async (constraints) => {
      try {
        probe.calls.push({ constraints, previous: probe.streams.flatMap((stream) => stream.getTracks().map((track) => track.readyState)) })
        const stream = await native(constraints)
        probe.streams.push(stream)
        if (probe.hold) {
          probe.hold = false
          await new Promise<void>((resolve) => { probe.resume = resolve })
        }
        return stream
      } catch (cause) {
        probe.failures.push(String(cause))
        throw cause
      }
    }
  })
  await page.goto(path)
  await mountCamera(page)
}

async function mountCamera(page: Page) {
  await page.evaluate(async () => {
    const modulePath = '/assets/camera-harness.js'
    const harness = await import(modulePath)
    ;window.__unmountCamera = harness.mountCameraHarness()
  })
  await expect(page.locator('[aria-label="Camera state"]')).toHaveText('idle')
}

async function streamState(page: Page, index: number) {
  return page.evaluate((position) => window.__cameraProbe.streams[position]?.getVideoTracks()[0]?.readyState, index)
}

async function holdFrameEncoding(page: Page) {
  await page.evaluate(async () => {
    const modulePath = '/assets/camera-harness.js', harness = await import(modulePath)
    const video = document.querySelector<HTMLVideoElement>('video[aria-label="Live camera"]')
    if (!video || !(video.srcObject instanceof MediaStream)) throw new Error('Missing live video stream')
    const native = HTMLCanvasElement.prototype.toBlob, probe: FrameProbe = { held: false }
    window.__frameCaptureProbe = probe
    HTMLCanvasElement.prototype.toBlob = function (callback, type, quality) {
      native.call(this, (blob) => { probe.held = true; probe.release = () => callback(blob) }, type, quality)
    }
    void harness.captureVideoFrame(video, video.srcObject, new AbortController().signal).then((result: { kind: string }) => { probe.kind = result.kind })
  })
  await expect.poll(() => page.evaluate(() => window.__frameCaptureProbe.held)).toBe(true)
}

async function hidePage(page: Page) {
  await page.evaluate(() => {
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'hidden' })
    document.dispatchEvent(new Event('visibilitychange'))
  })
}

async function showPage(page: Page) {
  await page.evaluate(() => {
    Reflect.deleteProperty(document, 'visibilityState')
    document.dispatchEvent(new Event('visibilitychange'))
  })
}

test('uses a live fake video, switches only after stopping, and releases it on hide, pagehide and unmount', async ({ page, cameraBundle }) => {
  await prepareCamera(page, cameraBundle)
  expect(await page.evaluate(() => window.__cameraProbe.calls.length)).toBe(0)
  await page.getByRole('button', { name: 'Start camera' }).click()
  await expect.poll(() => page.evaluate(() => ({ state: document.querySelector('[aria-label="Camera state"]')?.textContent, failures: window.__cameraProbe.failures }))).toEqual({ state: 'active', failures: [] })
  await expect(page.locator('[aria-label="Camera state"]')).toHaveText('active')
  await expect.poll(() => page.locator('video[aria-label="Live camera"]').evaluate((video: HTMLVideoElement) => video.videoWidth)).toBeGreaterThan(0)
  const first = await page.evaluate(() => {
    const probe = window.__cameraProbe
    const video = document.querySelector<HTMLVideoElement>('video[aria-label="Live camera"]')
    if (!video) throw new Error('Missing video')
    const canvas = document.createElement('canvas')
    canvas.width = video.videoWidth; canvas.height = video.videoHeight
    const context = canvas.getContext('2d')
    if (!context) throw new Error('Missing canvas context')
    context.drawImage(video, 0, 0)
    return { call: probe.calls[0], audio: probe.streams[0]?.getAudioTracks().length, muted: video.muted,
      inline: video.playsInline, actualStream: video.srcObject === probe.streams[0], frame: canvas.toDataURL('image/jpeg').startsWith('data:image/jpeg') }
  })
  expect(first).toEqual({ call: { constraints: { audio: false, video: { facingMode: { ideal: 'environment' } } }, previous: [] }, audio: 0, muted: true, inline: true, actualStream: true, frame: true })
  await page.getByRole('button', { name: 'Switch camera' }).click()
  await expect.poll(() => streamState(page, 1)).toBe('live')
  await expect(page.locator('[aria-label="Camera state"]')).toHaveText('active')
  expect(await streamState(page, 0)).toBe('ended')
  expect(await page.evaluate(() => window.__cameraProbe.calls[1])).toEqual({ constraints: { audio: false, video: { facingMode: { ideal: 'user' } } }, previous: ['ended'] })
  await hidePage(page)
  await expect(page.locator('[aria-label="Camera state"]')).toHaveText('idle')
  expect(await streamState(page, 1)).toBe('ended')
  await showPage(page)
  expect(await page.evaluate(() => window.__cameraProbe.calls.length)).toBe(2)
  await page.getByRole('button', { name: 'Start camera' }).click()
  await expect(page.locator('[aria-label="Camera state"]')).toHaveText('active')
  await page.evaluate(() => { window.dispatchEvent(new Event('pagehide')) })
  await expect(page.locator('[aria-label="Camera state"]')).toHaveText('idle')
  expect(await streamState(page, 2)).toBe('ended')
  await page.getByRole('button', { name: 'Start camera' }).click()
  await expect(page.locator('[aria-label="Camera state"]')).toHaveText('active')
  await page.evaluate(() => { window.__unmountCamera() })
  expect(await streamState(page, 3)).toBe('ended')
})

test('encodes actual fake-camera video pixels into a bounded JPEG in the private helper', async ({ page, cameraBundle }) => {
  await prepareCamera(page, cameraBundle)
  const uploads: string[] = []
  page.on('request', (request) => { if (request.method() === 'POST' && new URL(request.url()).pathname.startsWith('/api/v1/media')) uploads.push(request.url()) })
  await page.getByRole('button', { name: 'Start camera' }).click()
  await expect(page.locator('[aria-label="Camera state"]')).toHaveText('active')
  await expect.poll(() => page.locator('video[aria-label="Live camera"]').evaluate((video: HTMLVideoElement) => video.videoWidth)).toBeGreaterThan(0)
  await expect.poll(() => page.locator('video[aria-label="Live camera"]').evaluate((video: HTMLVideoElement) => {
    const canvas = document.createElement('canvas')
    canvas.width = video.videoWidth; canvas.height = video.videoHeight
    const context = canvas.getContext('2d')
    if (!context) return 0
    context.drawImage(video, 0, 0)
    const pixels = context.getImageData(0, 0, canvas.width, canvas.height).data, colors = new Set<string>()
    for (let index = 0; index < pixels.length; index += 4 * 29) colors.add(`${pixels[index]! >> 4},${pixels[index + 1]! >> 4},${pixels[index + 2]! >> 4}`)
    return colors.size
  })).toBeGreaterThan(1)
  const result = await page.evaluate(async () => {
    const modulePath = '/assets/camera-harness.js'
    const harness = await import(modulePath)
    const video = document.querySelector<HTMLVideoElement>('video[aria-label="Live camera"]')
    if (!video || !(video.srcObject instanceof MediaStream)) throw new Error('Missing live video stream')
    const captured = await harness.captureVideoFrame(video, video.srcObject, new AbortController().signal)
    if (captured.kind !== 'captured') return captured
    const bytes = new Uint8Array(await captured.blob.arrayBuffer())
    const bitmap = await createImageBitmap(captured.blob)
    try {
      const canvas = document.createElement('canvas')
      canvas.width = bitmap.width; canvas.height = bitmap.height
      const context = canvas.getContext('2d')
      if (!context) throw new Error('Missing image context')
      context.drawImage(bitmap, 0, 0)
      const pixels = context.getImageData(0, 0, bitmap.width, bitmap.height).data, colors = new Set<string>()
      for (let index = 0; index < pixels.length; index += 4 * 29) colors.add(`${pixels[index]! >> 4},${pixels[index + 1]! >> 4},${pixels[index + 2]! >> 4}`)
      return { kind: captured.kind, width: captured.width, height: captured.height, mime: captured.blob.type,
        size: captured.blob.size, first: [...bytes.slice(0, 3)], last: [...bytes.slice(-2)], decodedWidth: bitmap.width, decodedHeight: bitmap.height, colors: colors.size }
    } finally { bitmap.close() }
  })
  expect(result).toMatchObject({ kind: 'captured', mime: 'image/jpeg', first: [255, 216, 255], last: [255, 217] })
  if (result.kind !== 'captured') throw new Error('Camera frame was not captured')
  expect(result.size).toBeGreaterThan(0)
  expect(result.width).toBe(result.decodedWidth)
  expect(result.height).toBe(result.decodedHeight)
  expect(result.colors).toBeGreaterThan(1)
  expect(await streamState(page, 0)).toBe('live')
  await page.getByRole('button', { name: 'Stop camera' }).click()
  expect(await streamState(page, 0)).toBe('ended')
  expect(uploads).toEqual([])
})

for (const reason of ['pagehide', 'session'] as const) test(`discards a real encoded frame returned after ${reason}`, async ({ page, cameraBundle }) => {
  await prepareCamera(page, cameraBundle)
  await page.getByRole('button', { name: 'Start camera' }).click()
  await expect(page.locator('[aria-label="Camera state"]')).toHaveText('active')
  await expect.poll(() => page.locator('video[aria-label="Live camera"]').evaluate((video: HTMLVideoElement) => video.videoWidth)).toBeGreaterThan(0)
  await holdFrameEncoding(page)
  if (reason === 'pagehide') await page.evaluate(() => { window.dispatchEvent(new Event('pagehide')) })
  else await page.getByRole('button', { name: 'Purge session' }).click()
  await expect.poll(() => page.evaluate(() => window.__frameCaptureProbe.kind)).toBe('cancelled')
  await expect.poll(() => streamState(page, 0)).toBe('ended')
  const blocked = await page.evaluate(async () => {
    const modulePath = '/assets/camera-harness.js', harness = await import(modulePath)
    const video = document.querySelector<HTMLVideoElement>('video[aria-label="Live camera"]')
    if (!video) throw new Error('Missing camera video')
    return (await harness.captureVideoFrame(video, window.__cameraProbe.streams[0], new AbortController().signal)).kind
  })
  expect(blocked).toBe('busy')
  await page.evaluate(() => { window.__frameCaptureProbe.release?.() })
  expect(await page.evaluate(() => window.__frameCaptureProbe.kind)).toBe('cancelled')
})

for (const transport of ['broadcast', 'storage'] as const) test(`discards a pending encoded frame after real cross-window logout using ${transport}`, async ({ page, context, cameraBundle, application }) => {
  await provision(page, application)
  await context.addInitScript((selected: 'broadcast' | 'storage') => {
    if (selected === 'broadcast') Object.defineProperty(window, 'localStorage', { configurable: true, get() { throw new DOMException('disabled for test', 'SecurityError') } })
    else Object.defineProperty(window, 'BroadcastChannel', { configurable: true, value: undefined })
  }, transport)
  await prepareCamera(page, cameraBundle, '/settings/account')
  await page.evaluate(() => { Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' }) })
  await page.getByRole('button', { name: 'Start camera' }).click()
  await expect(page.locator('[aria-label="Camera state"]')).toHaveText('active')
  await expect.poll(() => page.locator('video[aria-label="Live camera"]').evaluate((video: HTMLVideoElement) => video.videoWidth)).toBeGreaterThan(0)
  await holdFrameEncoding(page)
  const other = await context.newPage()
  try {
    await other.goto('/settings/account')
    await other.getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await other.getByRole('alertdialog').getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await expect.poll(() => page.evaluate(() => window.__frameCaptureProbe.kind)).toBe('cancelled')
    await expect.poll(() => streamState(page, 0)).toBe('ended')
    await page.evaluate(() => { window.__frameCaptureProbe.release?.() })
    expect(await page.evaluate(() => window.__frameCaptureProbe.kind)).toBe('cancelled')
  } finally { await other.close() }
})

test('stops a real fake-device stream returned after the page was hidden', async ({ page, cameraBundle }) => {
  await prepareCamera(page, cameraBundle)
  await page.evaluate(() => { window.__cameraProbe.hold = true })
  await page.getByRole('button', { name: 'Start camera' }).click()
  await expect.poll(() => page.evaluate(() => window.__cameraProbe.streams.length)).toBe(1)
  await expect(page.locator('[aria-label="Camera state"]')).toHaveText('requesting')
  await hidePage(page)
  await expect(page.locator('[aria-label="Camera state"]')).toHaveText('idle')
  await page.evaluate(() => { window.__cameraProbe.resume?.() })
  await expect.poll(() => streamState(page, 0)).toBe('ended')
  await showPage(page)
  expect(await page.evaluate(() => window.__cameraProbe.calls.length)).toBe(1)
})

test('shows native policy denial and restarts only after a new gesture', async ({ page, cameraBundle }) => {
  // Chromium's fake UI auto-grants prompts; a document policy still denies the
  // native getUserMedia call without mocking its error.
  const denyCamera = async (route: Route) => {
    const response = await route.fetch()
    await route.fulfill({ response, headers: { ...response.headers(), 'permissions-policy': 'camera=()' } })
  }
  await page.route('**/', denyCamera)
  await prepareCamera(page, cameraBundle)
  await page.getByRole('button', { name: 'Start camera' }).click()
  await expect(page.locator('[aria-label="Camera state"]')).toHaveText('error:permission-denied')
  expect(await page.evaluate(() => window.__cameraProbe.failures[0])).toMatch(/^NotAllowedError:/)
  expect(await page.evaluate(() => window.__cameraProbe.calls.length)).toBe(1)
  await page.unroute('**/', denyCamera)
  await page.reload()
  await mountCamera(page)
  expect(await page.evaluate(() => window.__cameraProbe.calls.length)).toBe(0)
  await page.getByRole('button', { name: 'Start camera' }).click()
  await expect(page.locator('[aria-label="Camera state"]')).toHaveText('active')
  await page.getByRole('button', { name: 'Stop camera' }).click()
  expect(await streamState(page, 0)).toBe('ended')
})

for (const transport of ['broadcast', 'storage'] as const) {
  test(`logout in another window stops the camera using ${transport}`, async ({ page, context, cameraBundle, application }) => {
    await provision(page, application)
    await context.addInitScript((selected: 'broadcast' | 'storage') => {
      if (selected === 'broadcast') {
        Object.defineProperty(window, 'localStorage', { configurable: true, get() { throw new DOMException('disabled for test', 'SecurityError') } })
      } else {
        Object.defineProperty(window, 'BroadcastChannel', { configurable: true, value: undefined })
      }
    }, transport)
    await prepareCamera(page, cameraBundle, '/settings/account')
    // Two visible windows can coexist; prevent tab-switch emulation from being
    // the reason the camera stops before the other window actually logs out.
    await page.evaluate(() => { Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' }) })
    await page.getByRole('button', { name: 'Start camera' }).click()
    await expect(page.locator('[aria-label="Camera state"]')).toHaveText('active')
    const other = await context.newPage()
    await other.goto('/settings/account')
    await expect(other.getByRole('button', { name: messages.auth.logout, exact: true })).toBeVisible()
    expect(await streamState(page, 0)).toBe('live')
    await other.getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await other.getByRole('alertdialog').getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await expect(other).toHaveURL(/\/login$/)
    await expect(page.locator('[aria-label="Camera state"]')).toHaveText('idle')
    expect(await streamState(page, 0)).toBe('ended')
    await page.getByRole('button', { name: 'Start camera' }).click()
    expect(await page.evaluate(() => window.__cameraProbe.calls.length)).toBe(1)
  })
}

test('real account logout purges a still-mounted camera before it can restart', async ({ page, cameraBundle, application }) => {
  await provision(page, application)
  await prepareCamera(page, cameraBundle, '/settings/account')
  await page.getByRole('button', { name: 'Start camera' }).click()
  await expect(page.locator('[aria-label="Camera state"]')).toHaveText('active')
  await page.getByRole('button', { name: messages.auth.logout, exact: true }).click()
  await page.getByRole('alertdialog').getByRole('button', { name: messages.auth.logout, exact: true }).click()
  await expect(page).toHaveURL(/\/login$/)
  await expect(page.locator('[aria-label="Camera state"]')).toHaveText('idle')
  expect(await streamState(page, 0)).toBe('ended')
  await page.getByRole('button', { name: 'Start camera' }).click()
  expect(await page.evaluate(() => window.__cameraProbe.calls.length)).toBe(1)
})
