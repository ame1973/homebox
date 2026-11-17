import AxeBuilder from '@axe-core/playwright'
import type { Page } from '@playwright/test'
import { messages } from '../../src/lib/messages.ts'
import { test, expect } from './auth-fixture.ts'
import { noOverflow, provision, watchDomainWrites, writeContext } from './domain-helpers.ts'

type CaptureProbe = { tracks: MediaStreamTrack[]; created: string[]; revoked: string[]; calls: number }
declare global { interface Window { __capturePageProbe: CaptureProbe } }

// Chromium supplies a fake camera; these checks are not physical-device or
// operating-system permission-prompt acceptance.
test.use({ launchOptions: { args: ['--use-fake-device-for-media-stream', '--use-fake-ui-for-media-stream'] }, permissions: ['camera'], trace: 'off', screenshot: 'off', video: 'off' })
const text = messages.capture, camera = messages.scan

async function installProbe(page: Page) {
  await page.addInitScript(() => {
    const devices = navigator.mediaDevices
    if (!devices?.getUserMedia) return
    const get = devices.getUserMedia.bind(devices), create = URL.createObjectURL.bind(URL), revoke = URL.revokeObjectURL.bind(URL)
    const probe: CaptureProbe = { tracks: [], created: [], revoked: [], calls: 0 }
    window.__capturePageProbe = probe
    devices.getUserMedia = async (constraints) => {
      probe.calls++
      const stream = await get(constraints)
      probe.tracks.push(...stream.getTracks())
      return stream
    }
    URL.createObjectURL = (blob) => { const url = create(blob); probe.created.push(url); return url }
    URL.revokeObjectURL = (url) => { probe.revoked.push(url); revoke(url) }
  })
}
async function takePhoto(page: Page) {
  await page.getByRole('button', { name: camera.cameraStart }).click()
  const video = page.getByLabel(camera.cameraVideo)
  await expect.poll(() => video.evaluate((element: HTMLVideoElement) => element.videoWidth)).toBeGreaterThan(0)
  await page.getByRole('button', { name: text.take }).click()
  await expect(page.getByRole('img', { name: text.preview })).toBeVisible()
}
async function actualItem(page: Page, origin: string) {
  const response = await page.request.post('/api/v1/items', { headers: await writeContext(page, origin), data: { name: '拍攝測試物品' } })
  expect(response.status()).toBe(200)
  return (await response.json() as { id: string }).id
}

test('captures multiple real fake-camera frames and edits their original-pixel preview without a write', async ({ page, application }) => {
  await installProbe(page)
  await provision(page, application)
  const id = await actualItem(page, application.url), writes = watchDomainWrites(page, ['/api/v1/media', '/api/v1/items', '/api/v1/inventory'])
  await page.goto(`/photos?${new URLSearchParams({ owner_type: 'item', owner_id: id })}`)
  await page.getByRole('link', { name: messages.media.capture }).click()
  await expect(page.getByText(text.unsavedHelp)).toBeVisible()
  expect(await page.evaluate(() => window.__capturePageProbe.calls)).toBe(0)
  await takePhoto(page)
  await expect.poll(() => page.evaluate(() => window.__capturePageProbe.tracks.map((track) => track.readyState))).toEqual(['ended'])
  await page.getByRole('textbox', { name: text.description }).fill('保留這張的說明')
  await page.getByRole('button', { name: text.setCover }).click()
  await page.getByRole('button', { name: text.rotateRight }).click()
  for (const [label, value] of [[text.cropX, '10'], [text.cropY, '20'], [text.cropWidth, '100'], [text.cropHeight, '200']] as const) {
    await page.getByRole('textbox', { name: label }).fill(value)
  }
  await page.getByRole('button', { name: text.crop }).click()
  const preview = page.getByRole('img', { name: text.preview })
  await expect(preview).toHaveAttribute('viewBox', '0 0 100 200')
  const expected = await preview.evaluate(async (element: SVGElement) => {
    const image = element.querySelector('image'), href = image?.getAttribute('href')
    if (!href) throw new Error('Missing original preview image')
    const source = new Image()
    source.src = href
    await source.decode()
    const canvas = document.createElement('canvas')
    canvas.width = source.naturalWidth; canvas.height = source.naturalHeight
    const context = canvas.getContext('2d')
    if (!context) throw new Error('Missing source canvas')
    context.drawImage(source, 0, 0)
    const rect = element.getBoundingClientRect(), scale = Math.min(rect.width / 100, rect.height / 200)
    const offsetX = (rect.width - 100 * scale) / 2, offsetY = (rect.height - 200 * scale) / 2
    const samples = []
    for (const u of [20, 35, 50, 65, 80]) for (const v of [25, 50, 75, 100, 125, 150, 175]) {
      samples.push({ x: offsetX + (u + 0.5) * scale, y: offsetY + (v + 0.5) * scale,
        pixel: Array.from(context.getImageData(20 + v, source.naturalHeight - 11 - u, 1, 1).data) })
    }
    canvas.width = 0
    return { width: rect.width, height: rect.height, samples }
  })
  const screenshot = await preview.screenshot({ scale: 'css', animations: 'disabled' })
  const actual = await page.evaluate(async ({ data, expected }) => {
    const image = new Image()
    image.src = `data:image/png;base64,${data}`
    await image.decode()
    const canvas = document.createElement('canvas')
    canvas.width = image.naturalWidth; canvas.height = image.naturalHeight
    const context = canvas.getContext('2d')
    if (!context) throw new Error('Missing screenshot canvas')
    context.drawImage(image, 0, 0)
    const samples = expected.samples.map(({ x, y }) => Array.from(context.getImageData(
      Math.min(image.naturalWidth - 1, Math.floor(x * image.naturalWidth / expected.width)),
      Math.min(image.naturalHeight - 1, Math.floor(y * image.naturalHeight / expected.height)), 1, 1).data))
    canvas.width = 0
    return samples
  }, { data: screenshot.toString('base64'), expected })
  const matching = expected.samples.filter(({ pixel }, index) => pixel.every((channel, component) => Math.abs(channel - actual[index]![component]!) <= 20)).length
  expect(matching, 'the displayed preview must show the cropped, rotated camera pixels').toBeGreaterThanOrEqual(28)
  await takePhoto(page)
  await page.getByRole('button', { name: `${text.photo} 1（${text.cover}）` }).click()
  await page.getByRole('button', { name: text.later }).click()
  await page.getByRole('button', { name: text.retake }).click()
  const dialog = page.getByRole('alertdialog')
  await expect(dialog).toContainText(text.retakeHelp)
  await dialog.getByRole('button', { name: text.confirmRetake }).click()
  const video = page.getByLabel(camera.cameraVideo)
  await expect.poll(() => video.evaluate((element: HTMLVideoElement) => element.videoWidth)).toBeGreaterThan(0)
  await page.getByRole('button', { name: text.take }).click()
  await expect.poll(() => page.evaluate(() => window.__capturePageProbe.created.length)).toBe(3)
  const latest = await page.evaluate(() => window.__capturePageProbe.created[2])
  await expect(preview.locator('image')).toHaveAttribute('href', latest!)
  await expect.poll(() => page.evaluate(() => window.__capturePageProbe.tracks.map((track) => track.readyState))).toEqual(['ended', 'ended', 'ended'])
  await expect(page.getByRole('textbox', { name: text.description })).toHaveValue('保留這張的說明')
  await expect(page.getByRole('button', { name: text.clearCover })).toHaveAttribute('aria-pressed', 'true')
  await expect(page.getByRole('heading', { name: `${text.selected}：${text.photo} 2` })).toBeVisible()
  const state = await page.evaluate(() => ({ tracks: window.__capturePageProbe.tracks.map((track) => track.readyState), created: window.__capturePageProbe.created, revoked: window.__capturePageProbe.revoked }))
  expect(state.tracks).toEqual(['ended', 'ended', 'ended'])
  expect(state.revoked).toContain(state.created[0])
  expect(writes).toEqual([])
})

for (const transport of ['storage', 'broadcast'] as const) test(`clears photos and stops a live camera on cross-tab logout using ${transport} only`, async ({ page, context, application }) => {
  await page.addInitScript((selected) => {
    if (selected === 'storage') Object.defineProperty(window, 'BroadcastChannel', { configurable: true, value: undefined })
    else window.addEventListener = new Proxy(window.addEventListener, { apply(target, receiver, args: unknown[]) {
      return args[0] === 'storage' ? undefined : Reflect.apply(target, receiver, args)
    } })
  }, transport)
  await installProbe(page)
  await provision(page, application)
  const writes = watchDomainWrites(page, ['/api/v1/media', '/api/v1/items'])
  await page.goto('/photos/capture?owner_type=item&owner_id=exact-owner')
  await expect(page.getByRole('button', { name: camera.cameraStart })).toBeEnabled()
  await page.evaluate(() => { Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' }) })
  await takePhoto(page)
  await page.getByRole('textbox', { name: text.description }).fill('private capture description')
  await page.getByRole('button', { name: camera.cameraStart }).click()
  await expect.poll(() => page.evaluate(() => window.__capturePageProbe.tracks[1]?.readyState)).toBe('live')
  const other = await context.newPage()
  try {
    await other.goto('/settings/account')
    await other.getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await other.getByRole('alertdialog').getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await expect(page.getByText(text.sessionCleared)).toBeVisible()
    await expect.poll(() => page.evaluate(() => window.__capturePageProbe.tracks.map((track) => track.readyState))).toEqual(['ended', 'ended'])
    await expect(page.getByRole('img', { name: text.preview })).toHaveCount(0)
    await expect(page.locator('body')).not.toContainText('private capture description')
    const state = await page.evaluate(() => ({ created: window.__capturePageProbe.created, revoked: window.__capturePageProbe.revoked }))
    expect(state.revoked).toContain(state.created[0])
    expect(writes).toEqual([])
  } finally { await other.close() }
})

test('keeps the camera and selected photo controls accessible at 320px and 200% text in both themes', async ({ page, application }) => {
  await installProbe(page)
  await provision(page, application)
  await page.setViewportSize({ width: 320, height: 568 })
  await page.goto('/photos/capture?owner_type=item&owner_id=owner')
  await expect(page.getByRole('button', { name: camera.cameraStart })).toBeEnabled()
  await page.locator('html').evaluate((element) => { element.style.fontSize = '200%' })
  await takePhoto(page)
  for (const colorScheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme }); await expect(page.locator('html')).toHaveAttribute('data-theme', colorScheme)
    expect(await page.evaluate(() => innerWidth)).toBe(320)
    await noOverflow(page)
    for (const button of await page.getByRole('region', { name: text.album }).getByRole('button').all()) {
      expect(await button.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)
    }
    expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze()).violations).toEqual([])
    await page.getByRole('button', { name: text.retake }).click()
    const dialog = page.getByRole('alertdialog')
    await expect(dialog.getByRole('heading', { name: text.retakeTitle })).toBeFocused()
    expect(await dialog.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)
    expect(await dialog.getByRole('button', { name: text.confirmRetake }).evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)
    expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze()).violations).toEqual([])
    await page.keyboard.press('Escape')
    await expect(dialog).toHaveCount(0)
    await expect(page.getByRole('button', { name: text.retake })).toBeFocused()
  }
})
