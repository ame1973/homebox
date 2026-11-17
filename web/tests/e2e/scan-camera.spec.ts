import AxeBuilder from '@axe-core/playwright'
import type { Page, Route } from '@playwright/test'
import { messages } from '../../src/lib/messages.ts'
import { test, expect } from './auth-fixture.ts'
import { noOverflow, provision, watchDomainWrites } from './domain-helpers.ts'

type CameraProbe = {
  calls: { constraints: MediaStreamConstraints | undefined; previous: string[] }[]
  streams: MediaStream[]
  failures: string[]
  hold: boolean
  resume?: () => void
}
declare global { interface Window { __scanCameraProbe: CameraProbe } }

// Chromium provides a fake video device. These checks do not grant or exercise
// a physical device or an operating-system permission prompt.
test.use({ launchOptions: { args: ['--use-fake-device-for-media-stream', '--use-fake-ui-for-media-stream'] }, permissions: ['camera'], trace: 'off', screenshot: 'off', video: 'off' })
const text = messages.scan

async function installProbe(page: Page) {
  await page.addInitScript(() => {
    // Camera-only regressions stay independent of decoder behavior.
    Object.defineProperty(window, 'Worker', { configurable: true, value: undefined })
    const devices = navigator.mediaDevices
    if (!devices?.getUserMedia) return
    const native = devices.getUserMedia.bind(devices)
    const probe: CameraProbe = { calls: [], streams: [], failures: [], hold: false }
    window.__scanCameraProbe = probe
    devices.getUserMedia = async (constraints) => {
      try {
        probe.calls.push({ constraints, previous: probe.streams.flatMap((stream) => stream.getTracks().map((track) => track.readyState)) })
        const stream = await native(constraints)
        probe.streams.push(stream)
        if (probe.hold) { probe.hold = false; await new Promise<void>((resolve) => { probe.resume = resolve }) }
        return stream
      } catch (cause) { probe.failures.push(String(cause)); throw cause }
    }
  })
}
async function openScan(page: Page) {
  await page.goto('/scan')
  await expect(page.getByRole('button', { name: text.cameraStart })).toBeEnabled()
}
async function state(page: Page, index: number) {
  return page.evaluate((position) => window.__scanCameraProbe.streams[position]?.getVideoTracks()[0]?.readyState, index)
}

test('uses the live fake video only after a gesture and stops before switching or closing', async ({ page, application }) => {
  await installProbe(page)
  await provision(page, application)
  const writes = watchDomainWrites(page, ['/api/v1/items', '/api/v1/barcodes', '/api/v1/inventory'])
  await openScan(page)
  await expect(page.getByText(text.cameraUnavailable)).toBeVisible()
  expect(await page.evaluate(() => window.__scanCameraProbe.calls.length)).toBe(0)
  await page.getByRole('textbox', { name: text.input }).fill('000123')
  await page.getByRole('button', { name: text.cameraStart }).click()
  const video = page.getByLabel(text.cameraVideo)
  await expect(video).toBeVisible()
  await expect.poll(() => video.evaluate((element: HTMLVideoElement) => element.videoWidth)).toBeGreaterThan(0)
  const first = await page.evaluate(() => {
    const probe = window.__scanCameraProbe, element = document.querySelector<HTMLVideoElement>('video')
    if (!element) throw new Error('Missing camera video')
    return { call: probe.calls[0], audio: probe.streams[0]?.getAudioTracks().length, muted: element.muted, inline: element.playsInline, actual: element.srcObject === probe.streams[0] }
  })
  expect(first).toEqual({ call: { constraints: { audio: false, video: { facingMode: { ideal: 'environment' } } }, previous: [] }, audio: 0, muted: true, inline: true, actual: true })
  await page.getByRole('button', { name: text.cameraSwitch }).click()
  await expect.poll(() => state(page, 1)).toBe('live')
  expect(await state(page, 0)).toBe('ended')
  expect(await page.evaluate(() => window.__scanCameraProbe.calls[1])).toEqual({ constraints: { audio: false, video: { facingMode: { ideal: 'user' } } }, previous: ['ended'] })
  await page.getByRole('button', { name: text.cameraStop }).click()
  expect(await state(page, 1)).toBe('ended')
  await expect(video).toHaveCount(0)
  await expect(page.getByRole('textbox', { name: text.input })).toHaveValue('000123')
  expect(writes).toEqual([])
})

test('allows a previously authenticated user to inspect a local camera offline without replay', async ({ page, context, application }) => {
  await installProbe(page)
  await provision(page, application)
  const writes = watchDomainWrites(page, ['/api/v1/items', '/api/v1/barcodes']), lookups: URL[] = []
  page.on('request', (request) => { const url = new URL(request.url()); if (url.pathname === '/api/v1/search/barcodes') lookups.push(url) })
  await openScan(page)
  await context.setOffline(true)
  try {
    await expect(page.getByText(text.offline, { exact: true })).toBeVisible()
    await expect(page.getByRole('button', { name: text.cameraStart })).toBeEnabled()
    await page.getByRole('button', { name: text.cameraStart }).click()
    await expect.poll(() => state(page, 0)).toBe('live')
    await page.getByRole('textbox', { name: text.input }).fill('Offline exact code')
    await expect(page.getByRole('button', { name: text.search })).toBeDisabled()
    await page.getByRole('button', { name: text.cameraStop }).click()
    expect(await state(page, 0)).toBe('ended')
  } finally { await context.setOffline(false) }
  await expect(page.getByRole('button', { name: text.search })).toBeEnabled()
  await expect(page.getByRole('textbox', { name: text.input })).toHaveValue('Offline exact code')
  expect(await page.evaluate(() => window.__scanCameraProbe.calls.length)).toBe(1)
  expect(lookups).toEqual([])
  expect(writes).toEqual([])
})

test('never starts a camera automatically and stops active tracks on hidden or route departure', async ({ page, application }) => {
  await installProbe(page)
  await provision(page, application)
  await openScan(page)
  await page.getByRole('button', { name: text.cameraStart }).click()
  await expect.poll(() => state(page, 0)).toBe('live')
  await page.evaluate(() => { Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'hidden' }); document.dispatchEvent(new Event('visibilitychange')) })
  await expect.poll(() => state(page, 0)).toBe('ended')
  await page.evaluate(() => { Reflect.deleteProperty(document, 'visibilityState'); document.dispatchEvent(new Event('visibilitychange')) })
  await expect(page.getByRole('button', { name: text.cameraStart })).toBeEnabled()
  expect(await page.evaluate(() => window.__scanCameraProbe.calls.length)).toBe(1)
  await page.getByRole('button', { name: text.cameraStart }).click()
  await expect.poll(() => state(page, 1)).toBe('live')
  await page.getByRole('link', { name: messages.catalog.nav, exact: true }).click()
  await expect(page).toHaveURL(/\/items$/)
  await expect.poll(() => state(page, 1)).toBe('ended')
  expect(await page.evaluate(() => window.__scanCameraProbe.calls.length)).toBe(2)
})

test('stops a fake-device stream returned after the user cancels a pending request', async ({ page, application }) => {
  await installProbe(page)
  await provision(page, application)
  await openScan(page)
  await page.evaluate(() => { window.__scanCameraProbe.hold = true })
  await page.getByRole('button', { name: text.cameraStart }).click()
  await expect.poll(() => page.evaluate(() => window.__scanCameraProbe.streams.length)).toBe(1)
  await expect(page.getByText(text.cameraWaiting)).toBeVisible()
  await page.getByRole('button', { name: text.cameraStop }).click()
  await page.evaluate(() => { window.__scanCameraProbe.resume?.() })
  await expect.poll(() => state(page, 0)).toBe('ended')
  await expect(page.getByLabel(text.cameraVideo)).toHaveCount(0)
  expect(await page.evaluate(() => window.__scanCameraProbe.calls.length)).toBe(1)
})

test('shows native permission-policy denial and permits only an explicit new attempt', async ({ page, application }) => {
  await installProbe(page)
  await provision(page, application)
  const deny = async (route: Route) => {
    const response = await route.fetch()
    await route.fulfill({ response, headers: { ...response.headers(), 'permissions-policy': 'camera=()' } })
  }
  const scanRoute = (url: URL) => url.pathname === '/scan'
  await page.route(scanRoute, deny)
  await openScan(page)
  await page.getByRole('button', { name: text.cameraStart }).click()
  await expect(page.getByText(text.cameraErrors['permission-denied'])).toBeVisible()
  expect(await page.evaluate(() => window.__scanCameraProbe.failures[0])).toMatch(/^NotAllowedError:/)
  expect(await page.evaluate(() => window.__scanCameraProbe.calls.length)).toBe(1)
  await page.unroute(scanRoute, deny)
  await page.reload()
  await expect(page.getByRole('button', { name: text.cameraStart })).toBeEnabled()
  expect(await page.evaluate(() => window.__scanCameraProbe.calls.length)).toBe(0)
  await page.getByRole('button', { name: text.cameraStart }).click()
  await expect.poll(() => state(page, 0)).toBe('live')
  await page.getByRole('button', { name: text.cameraStop }).click()
  expect(await state(page, 0)).toBe('ended')
})

for (const transport of ['storage', 'broadcast'] as const) test(`stops the live camera and purges scan data on cross-tab logout using ${transport} only`, async ({ page, context, application }) => {
  await page.addInitScript((selected) => {
    if (selected === 'storage') Object.defineProperty(window, 'BroadcastChannel', { configurable: true, value: undefined })
    else window.addEventListener = new Proxy(window.addEventListener, { apply(target, receiver, args: unknown[]) {
      return args[0] === 'storage' ? undefined : Reflect.apply(target, receiver, args)
    } })
  }, transport)
  await installProbe(page)
  await provision(page, application)
  const writes = watchDomainWrites(page, ['/api/v1/items', '/api/v1/barcodes'])
  await openScan(page)
  await page.evaluate(() => { Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' }) })
  await page.getByRole('textbox', { name: text.input }).fill('Private camera lookup')
  await page.getByRole('button', { name: text.search }).click()
  await expect(page.getByRole('heading', { name: text.empty })).toBeVisible()
  await page.getByRole('button', { name: text.cameraStart }).click()
  await expect.poll(() => state(page, 0)).toBe('live')
  const other = await context.newPage()
  try {
    await other.goto('/settings/account')
    await other.getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await other.getByRole('alertdialog').getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await expect(page.getByText(text.sessionClearedHelp)).toBeVisible()
    await expect.poll(() => state(page, 0)).toBe('ended')
    await expect(page.getByLabel(text.cameraVideo)).toHaveCount(0)
    await expect(page.locator('body')).not.toContainText('Private camera lookup')
    expect(writes).toEqual([])
  } finally { await other.close() }
})

test('stops a late fake-device response after real cross-tab logout', async ({ page, context, application }) => {
  await installProbe(page)
  await provision(page, application)
  await openScan(page)
  await page.evaluate(() => { Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' }); window.__scanCameraProbe.hold = true })
  await page.getByRole('button', { name: text.cameraStart }).click()
  await expect.poll(() => page.evaluate(() => window.__scanCameraProbe.streams.length)).toBe(1)
  const other = await context.newPage()
  try {
    await other.goto('/settings/account')
    await other.getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await other.getByRole('alertdialog').getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await expect(page.getByText(text.sessionClearedHelp)).toBeVisible()
    await page.evaluate(() => { window.__scanCameraProbe.resume?.() })
    await expect.poll(() => state(page, 0)).toBe('ended')
    await expect(page.getByLabel(text.cameraVideo)).toHaveCount(0)
    expect(await page.evaluate(() => window.__scanCameraProbe.calls.length)).toBe(1)
  } finally { await other.close() }
})

test('keeps the live camera controls accessible at 320px and 200% text', async ({ page, application }) => {
  await installProbe(page)
  await provision(page, application)
  await page.setViewportSize({ width: 320, height: 568 })
  await openScan(page)
  await page.locator('html').evaluate((element) => { element.style.fontSize = '200%' })
  await page.getByRole('button', { name: text.cameraStart }).click()
  await expect.poll(() => state(page, 0)).toBe('live')
  for (const colorScheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme }); await expect(page.locator('html')).toHaveAttribute('data-theme', colorScheme)
    expect(await page.evaluate(() => innerWidth)).toBe(320)
    await noOverflow(page)
    const camera = page.getByRole('region', { name: text.cameraTitle })
    for (const button of await camera.getByRole('button').all()) expect(await button.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)
    expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze()).violations).toEqual([])
  }
  await page.getByRole('button', { name: text.cameraStop }).click()
  expect(await state(page, 0)).toBe('ended')
})
