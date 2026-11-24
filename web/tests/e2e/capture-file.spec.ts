import AxeBuilder from '@axe-core/playwright'
import type { Page } from '@playwright/test'
import { messages } from '../../src/lib/messages.ts'
import { test, expect } from './auth-fixture.ts'
import { leaveDocument, permitBFCache, returnFromBFCache } from './bfcache-helpers.ts'
import { noOverflow, provision, watchDomainWrites } from './domain-helpers.ts'

type FileProbe = {
  cameraCalls: number; tracks: MediaStreamTrack[]; created: string[]; revoked: string[]; blobs: Blob[]
  workers: string[]; terminated: number; inputs: { named: boolean; type: string; size: number }[]
  holdWorker: boolean; workerHeld: boolean; deliverWorker?: () => void
  nativeMode: 'none' | 'decode' | 'encode'; nativeHeld: boolean; closed: number; releaseNative?: () => void
  signals: number; hidden: boolean
}
declare global { interface Window { __captureFileProbe: FileProbe } }

// These are synthetic inputs and fake camera pixels, not physical-device or
// operating-system picker/permission acceptance.
test.use({ launchOptions: { channel: 'chromium', ignoreDefaultArgs: ['--disable-back-forward-cache'], args: ['--use-fake-device-for-media-stream', '--use-fake-ui-for-media-stream'] }, permissions: ['camera'], trace: 'off', screenshot: 'off', video: 'off' })
const text = messages.capture, camera = messages.scan
const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAIAAAADCAYAAAC56t6BAAAAHElEQVR4nAXBAQEAMAjAIHwyo9l8B5EcqnhhtB+Pawm7X97uPgAAAABJRU5ErkJggg==', 'base64')
const webp = Buffer.from('UklGRjYAAABXRUJQVlA4TCkAAAAvAYAAEC8w/wKCIv9HExAU+T+aQIDgzP+U4A0i6igIBNKQUgorHNH/WAA=', 'base64')
const animatedWebP = Buffer.from('UklGRqIAAABXRUJQVlA4WAoAAAASAAAAAQAAAgAAQU5JTQYAAAAAAAAAAABBTk1GRAAAAAAAAAAAAAEAAAIAAHgAAAJWUDhMKwAAAC8BgAAQLyAQSNofeo35FxAU/B+dQCAJbbPVg0xc7AAEApSxYoMHIvof7wAAQU5NRioAAAAAAAAAAAABAAACAAB4AAAAVlA4TBEAAAAvAYAAAAdQlFKUp/+BiOh/AAA=', 'base64')
const file = (name: string, mimeType: string, buffer: Buffer) => ({ name, mimeType, buffer })

function orientedPNG() {
  const tiff = Buffer.alloc(37)
  tiff.write('II'); tiff.writeUInt16LE(42, 2); tiff.writeUInt32LE(8, 4); tiff.writeUInt16LE(1, 8)
  tiff.writeUInt16LE(0x112, 10); tiff.writeUInt16LE(3, 12); tiff.writeUInt32LE(1, 14); tiff.writeUInt16LE(6, 18)
  tiff.write('PRIVATE_GPS', 26)
  const chunk = Buffer.alloc(tiff.length + 12)
  chunk.writeUInt32BE(tiff.length, 0); chunk.write('eXIf', 4); tiff.copy(chunk, 8)
  let crc = 0xffffffff
  for (const byte of chunk.subarray(4, chunk.length - 4)) { crc ^= byte; for (let bit = 0; bit < 8; bit++) crc = crc >>> 1 ^ (crc & 1 ? 0xedb88320 : 0) }
  chunk.writeUInt32BE((crc ^ 0xffffffff) >>> 0, chunk.length - 4)
  return Buffer.concat([png.subarray(0, 33), chunk, png.subarray(33)])
}

async function jpeg(page: Page) {
  const bytes = await page.evaluate(async () => {
    const canvas = document.createElement('canvas'); canvas.width = 24; canvas.height = 16
    const context = canvas.getContext('2d')!
    context.fillStyle = '#e92431'; context.fillRect(0, 0, 12, 16)
    context.fillStyle = '#15ad4a'; context.fillRect(12, 0, 12, 16)
    const blob = await new Promise<Blob>((resolve, reject) => canvas.toBlob((value) => value ? resolve(value) : reject(new Error('JPEG unavailable')), 'image/jpeg', 0.95))
    canvas.width = 0; canvas.height = 0
    return Array.from(new Uint8Array(await blob.arrayBuffer()))
  })
  return Buffer.from(bytes)
}

async function installProbe(page: Page, transport?: 'broadcast' | 'storage') {
  await page.addInitScript((selected) => {
    const probe: FileProbe = { cameraCalls: 0, tracks: [], created: [], revoked: [], blobs: [], workers: [], terminated: 0, inputs: [],
      holdWorker: false, workerHeld: false, nativeMode: 'none', nativeHeld: false, closed: 0, signals: 0, hidden: false }
    window.__captureFileProbe = probe
    document.addEventListener('visibilitychange', () => { if (document.visibilityState !== 'visible') probe.hidden = true })
    if (selected === 'storage') {
      Object.defineProperty(window, 'BroadcastChannel', { configurable: true, value: undefined })
      window.addEventListener('storage', (event) => { if (event.key === 'homebox:session-cleared' && event.newValue === 'cleared') probe.signals++ })
    } else if (selected === 'broadcast') {
      window.addEventListener = new Proxy(window.addEventListener, { apply(target, receiver, args: unknown[]) {
        return args[0] === 'storage' ? undefined : Reflect.apply(target, receiver, args)
      } })
      window.BroadcastChannel = new Proxy(window.BroadcastChannel, { construct(target, args, newTarget) {
        const channel = Reflect.construct(target, args, newTarget) as BroadcastChannel
        channel.addEventListener('message', (event) => { if (event.data === 'cleared') probe.signals++ })
        return channel
      } })
    }
    const devices = navigator.mediaDevices, get = devices.getUserMedia.bind(devices)
    devices.getUserMedia = async (constraints) => { probe.cameraCalls++; const stream = await get(constraints); probe.tracks.push(...stream.getTracks()); return stream }
    const create = URL.createObjectURL.bind(URL), revoke = URL.revokeObjectURL.bind(URL)
    URL.createObjectURL = (blob) => { const url = create(blob); probe.created.push(url); if (blob instanceof Blob) probe.blobs.push(blob); return url }
    URL.revokeObjectURL = (url) => { probe.revoked.push(url); revoke(url) }
    window.Worker = new Proxy(window.Worker, { construct(target, args, newTarget) {
      const worker = Reflect.construct(target, args, newTarget) as Worker, url = String(args[0])
      if (!/\/assets\/image-prepare-worker-[^/]+\.js$/.test(new URL(url, location.href).pathname)) return worker
      probe.workers.push(url)
      worker.postMessage = new Proxy(worker.postMessage, { apply(method, receiver, values: unknown[]) {
        const value = values[0] as { blob?: unknown } | null
        if (value?.blob instanceof Blob) probe.inputs.push({ named: 'name' in value.blob, type: value.blob.type, size: value.blob.size })
        return Reflect.apply(method, receiver, values)
      } })
      worker.addEventListener('message', (event) => {
        if (probe.holdWorker && event.data?.type === 'result') {
          const handler = worker.onmessage
          probe.workerHeld = true; event.stopImmediatePropagation(); probe.deliverWorker = () => handler?.call(worker, event)
        }
      }, true)
      const terminate = worker.terminate.bind(worker)
      worker.terminate = () => { probe.terminated++; terminate() }
      return worker
    } })
    window.createImageBitmap = new Proxy(window.createImageBitmap, { apply(target, receiver, args: unknown[]) {
      const result = Reflect.apply(target, receiver, args) as Promise<ImageBitmap>
      if (probe.nativeMode !== 'decode') return result
      return result.then((bitmap) => new Promise<ImageBitmap>((resolve) => {
        const close = bitmap.close.bind(bitmap)
        bitmap.close = () => { probe.closed++; close() }
        probe.nativeHeld = true; probe.releaseNative = () => resolve(bitmap)
      }))
    } })
    const toBlob = HTMLCanvasElement.prototype.toBlob
    HTMLCanvasElement.prototype.toBlob = function (callback, type, quality) {
      if (probe.nativeMode !== 'encode') { toBlob.call(this, callback, type, quality); return }
      toBlob.call(this, (blob) => { probe.nativeHeld = true; probe.releaseNative = () => callback(blob) }, type, quality)
    }
  }, transport)
}

async function openCapture(page: Page, application: { url: string; bootstrapToken: string }) {
  await provision(page, application)
  await page.goto('/photos/capture?owner_type=item&owner_id=synthetic-owner')
  await expect(page.getByLabel(text.fileInput)).toBeEnabled()
}
async function selectPNG(page: Page) {
  await page.getByLabel(text.fileInput).setInputFiles(file('PRIVATE-name.png', 'image/png', png))
  await expect(page.getByRole('img', { name: text.preview })).toBeVisible()
}
async function holdNext(page: Page, mode: 'worker' | 'decode' | 'encode') {
  await page.evaluate((selected) => {
    const probe = window.__captureFileProbe
    probe.holdWorker = selected === 'worker'; probe.workerHeld = false
    probe.nativeMode = selected === 'worker' ? 'none' : selected; probe.nativeHeld = false
  }, mode)
  await page.getByLabel(text.fileInput).setInputFiles(file('PENDING-private.png', 'image/png', png))
  await expect.poll(() => page.evaluate((selected) => selected === 'worker' ? window.__captureFileProbe.workerHeld : window.__captureFileProbe.nativeHeld, mode)).toBe(true)
}
async function releaseHeld(page: Page, mode: 'worker' | 'decode' | 'encode') {
  await page.evaluate((selected) => {
    const probe = window.__captureFileProbe
    probe.holdWorker = false; probe.nativeMode = 'none'
    if (selected === 'worker') probe.deliverWorker?.(); else probe.releaseNative?.()
  }, mode)
  if (mode === 'decode') await expect.poll(() => page.evaluate(() => window.__captureFileProbe.closed)).toBe(1)
}

test('imports JPEG, oriented PNG and WebP through the real picker and edits a mixed camera album without writes', async ({ page, application }) => {
  await installProbe(page); await openCapture(page, application)
  const writes = watchDomainWrites(page, ['/api/v1/media', '/api/v1/items', '/api/v1/inventory'])
  await expect(page.getByText(text.fileQuality)).toBeVisible()
  expect(await page.evaluate(() => window.__captureFileProbe.cameraCalls)).toBe(0)
  const jpg = await jpeg(page), oriented = orientedPNG()
  await page.getByLabel(text.fileInput).setInputFiles([file('PRIVATE-jpeg.jpg', 'image/jpeg', jpg), file('PRIVATE-oriented.png', 'image/png', oriented), file('PRIVATE-alpha.webp', 'image/webp', webp)])
  const album = page.getByRole('list', { name: text.album })
  await expect(album.getByRole('button')).toHaveCount(3)
  await expect(page.getByText(`${text.filesAdded}3`)).toBeVisible()
  expect(await page.evaluate(() => window.__captureFileProbe.cameraCalls)).toBe(0)
  expect(await page.getByLabel(text.fileInput).inputValue()).toBe('')
  await expect(page.locator('body')).not.toContainText('PRIVATE-')
  const outputs = await page.evaluate(async () => {
    const probe = window.__captureFileProbe
    const details = await Promise.all(probe.blobs.map(async (blob) => ({ type: blob.type, privateMetadata: new TextDecoder().decode(await blob.arrayBuffer()).includes('PRIVATE_GPS') })))
    probe.blobs.length = 0
    return { details, workers: probe.workers, inputs: probe.inputs, terminated: probe.terminated }
  })
  expect(outputs.details).toEqual([{ type: 'image/jpeg', privateMetadata: false }, { type: 'image/png', privateMetadata: false }, { type: 'image/png', privateMetadata: false }])
  expect(outputs.workers).toHaveLength(3); expect(outputs.terminated).toBe(3)
  expect(outputs.workers.every((url) => new URL(url, application.url).origin === application.url)).toBe(true)
  expect(outputs.inputs).toEqual([jpg, oriented, webp].map((bytes) => ({ named: false, type: '', size: bytes.length })))
  await album.getByRole('button', { name: `${text.photo} 2`, exact: true }).click()
  const preview = page.getByRole('img', { name: text.preview })
  await expect(preview).toHaveAttribute('viewBox', '0 0 3 2')
  const pixels = await preview.evaluate(async (element: SVGElement) => {
    const image = new Image(); image.src = element.querySelector('image')!.getAttribute('href')!; await image.decode()
    const canvas = document.createElement('canvas'); canvas.width = image.naturalWidth; canvas.height = image.naturalHeight
    const context = canvas.getContext('2d')!; context.drawImage(image, 0, 0)
    const alpha = Array.from({ length: 6 }, (_, index) => context.getImageData(index % 3, Math.floor(index / 3), 1, 1).data[3])
    canvas.width = 0; canvas.height = 0; return alpha
  })
  expect(pixels).toEqual([255, 255, 255, 64, 0, 128])
  await page.getByRole('textbox', { name: text.description }).fill('synthetic description')
  await page.getByRole('button', { name: text.setCover }).click()
  await page.getByRole('button', { name: text.rotateRight }).click()
  await expect(preview).toHaveAttribute('viewBox', '0 0 2 3')
  for (const [label, value] of [[text.cropX, '0'], [text.cropY, '1'], [text.cropWidth, '2'], [text.cropHeight, '2']] as const) await page.getByRole('textbox', { name: label }).fill(value)
  await page.getByRole('button', { name: text.crop }).click()
  await expect(preview).toHaveAttribute('viewBox', '0 0 2 2')
  await page.getByRole('button', { name: text.later }).click()
  await page.getByRole('button', { name: text.retake }).click()
  await page.getByRole('alertdialog').getByRole('button', { name: text.confirmRetake }).click()
  const video = page.getByLabel(camera.cameraVideo)
  await expect.poll(() => video.evaluate((element: HTMLVideoElement) => element.videoWidth)).toBeGreaterThan(0)
  await page.getByRole('button', { name: text.take }).click()
  await expect.poll(() => page.evaluate(() => window.__captureFileProbe.created.length)).toBe(4)
  await expect.poll(() => page.evaluate(() => window.__captureFileProbe.tracks.map((track) => track.readyState))).toEqual(['ended'])
  await expect(page.getByRole('textbox', { name: text.description })).toHaveValue('synthetic description')
  await expect(page.getByRole('button', { name: text.clearCover })).toHaveAttribute('aria-pressed', 'true')
  const state = await page.evaluate(() => ({ calls: window.__captureFileProbe.cameraCalls, created: window.__captureFileProbe.created, revoked: window.__captureFileProbe.revoked }))
  expect(state.calls).toBe(1); expect(state.revoked).toContain(state.created[1])
  await page.getByRole('button', { name: text.remove }).click()
  await page.getByRole('alertdialog').getByRole('button', { name: text.confirmRemove }).click()
  await expect(album.getByRole('button')).toHaveCount(2)
  expect(writes).toEqual([])
})

test('rejects excessive batches and stops at the first invalid file without replaying the rest', async ({ page, application }) => {
  await installProbe(page); await openCapture(page, application)
  const input = page.getByLabel(text.fileInput), writes = watchDomainWrites(page, ['/api/v1/media', '/api/v1/items'])
  await input.setInputFiles(Array.from({ length: 11 }, (_, index) => file(`private-${index}.png`, 'image/png', png)))
  await expect(page.getByText(text.fileErrors['count-limit'])).toBeVisible()
  expect(await page.evaluate(() => window.__captureFileProbe.workers.length)).toBe(0)
  await input.setInputFiles([file('first.png', 'image/png', png), file('animated.webp', 'image/webp', animatedWebP), file('last.png', 'image/png', png)])
  await expect(page.getByText(text.fileErrors.invalid)).toBeVisible()
  await expect(page.getByText(text.fileRemaining)).toBeVisible()
  await expect(page.getByRole('list', { name: text.album }).getByRole('button')).toHaveCount(1)
  expect(await page.evaluate(() => window.__captureFileProbe.workers.length)).toBe(2)
  await input.setInputFiles(file('last.png', 'image/png', png))
  await expect(page.getByRole('list', { name: text.album }).getByRole('button')).toHaveCount(2)
  await input.setInputFiles(file('last.png', 'image/png', png))
  await expect(page.getByRole('list', { name: text.album }).getByRole('button')).toHaveCount(3)
  expect(await page.evaluate(() => window.__captureFileProbe.workers.length)).toBe(4)
  expect(await page.evaluate(() => window.__captureFileProbe.cameraCalls)).toBe(0)
  expect(writes).toEqual([])
})

test('rejects an actual over-limit File before creating a worker', async ({ page, application }) => {
  await installProbe(page); await openCapture(page, application)
  await page.getByLabel(text.fileInput).evaluate((element: HTMLInputElement) => {
    const selected = new DataTransfer()
    selected.items.add(new File([new Uint8Array(32 * 1024 * 1024 + 1)], 'PRIVATE-oversized.png', { type: 'image/png' }))
    element.files = selected.files; element.dispatchEvent(new Event('change', { bubbles: true }))
  })
  await expect(page.getByText(text.fileErrors['too-large'])).toBeVisible()
  expect(await page.evaluate(() => window.__captureFileProbe.workers.length)).toBe(0)
  await expect(page.locator('body')).not.toContainText('PRIVATE-oversized')
})

test('does not steal focus after the user clicks outside during processing', async ({ page, application }) => {
  await installProbe(page); await openCapture(page, application)
  await page.evaluate(() => { window.__captureFileProbe.holdWorker = true })
  const input = page.getByLabel(text.fileInput)
  await input.focus(); await expect(input).toBeFocused()
  const chooser = page.waitForEvent('filechooser')
  await page.keyboard.press('Enter')
  await (await chooser).setFiles(file('private.png', 'image/png', png))
  await expect.poll(() => page.evaluate(() => window.__captureFileProbe.workerHeld)).toBe(true)
  await page.getByRole('heading', { name: text.title }).click()
  await page.evaluate(() => { if (document.activeElement instanceof HTMLElement) document.activeElement.blur() })
  expect(await page.evaluate(() => document.activeElement === document.body)).toBe(true)
  await releaseHeld(page, 'worker')
  await expect(page.getByRole('img', { name: text.preview })).toBeVisible()
  expect(await page.evaluate(() => document.activeElement === document.body)).toBe(true)
})

test('cancels pending work on blocked navigation and keeps the existing album when staying', async ({ page, application }) => {
  await installProbe(page); await openCapture(page, application); await selectPNG(page)
  await page.getByRole('textbox', { name: text.description }).fill('kept description')
  await holdNext(page, 'worker')
  await page.getByRole('link', { name: text.back }).click()
  const dialog = page.getByRole('alertdialog')
  await expect(dialog).toContainText(text.leaveHelp)
  await dialog.getByRole('button', { name: text.stay }).click()
  await expect(page.getByRole('textbox', { name: text.description })).toHaveValue('kept description')
  await releaseHeld(page, 'worker')
  await expect(page.getByRole('list', { name: text.album }).getByRole('button')).toHaveCount(1)
  expect(await page.evaluate(() => window.__captureFileProbe.created.length)).toBe(1)
  let canceled = false
  page.once('dialog', async (dialogEvent) => { canceled = dialogEvent.type() === 'beforeunload'; await dialogEvent.dismiss() })
  await page.goto('/api/v1/health/live').catch(() => {})
  expect(canceled).toBe(true)
  await expect(page.getByRole('textbox', { name: text.description })).toHaveValue('kept description')
  expect(await page.evaluate(() => window.__captureFileProbe.revoked.length)).toBe(0)
})

for (const mode of ['worker', 'decode', 'encode'] as const) test(`cancels a pending ${mode} and never installs its late result`, async ({ page, application }) => {
  await installProbe(page); await openCapture(page, application); await selectPNG(page)
  const writes = watchDomainWrites(page, ['/api/v1/media', '/api/v1/items'])
  await holdNext(page, mode)
  await expect(page.getByRole('button', { name: camera.cameraStart })).toBeDisabled()
  await page.getByRole('button', { name: text.fileCancel }).click()
  await expect(page.getByText(text.fileCancelled)).toBeVisible()
  await expect(page.getByLabel(text.fileInput)).toBeFocused()
  await releaseHeld(page, mode)
  await expect(page.getByRole('list', { name: text.album }).getByRole('button')).toHaveCount(1)
  expect(await page.evaluate(() => window.__captureFileProbe.created.length)).toBe(1)
  expect(writes).toEqual([])
})

for (const transport of ['broadcast', 'storage'] as const) test(`clears imported photos and a pending worker on real logout using ${transport} only`, async ({ page, context, application }) => {
  await installProbe(page, transport); await openCapture(page, application); await selectPNG(page)
  const writes = watchDomainWrites(page, ['/api/v1/media', '/api/v1/items'])
  await page.getByRole('textbox', { name: text.description }).fill('private description')
  await holdNext(page, 'worker')
  const other = await context.newPage()
  try {
    await other.goto('/settings/account')
    expect(await page.evaluate(() => window.__captureFileProbe.workerHeld)).toBe(true)
    await other.getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await other.getByRole('alertdialog').getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await expect.poll(() => page.evaluate(() => window.__captureFileProbe.signals)).toBeGreaterThan(0)
    await expect(page.getByText(text.sessionCleared)).toBeVisible()
    const state = await page.evaluate(() => ({ hidden: window.__captureFileProbe.hidden, created: window.__captureFileProbe.created, revoked: window.__captureFileProbe.revoked, terminated: window.__captureFileProbe.terminated }))
    expect(state.hidden).toBe(false); expect(state.revoked).toContain(state.created[0]); expect(state.terminated).toBe(2)
    await releaseHeld(page, 'worker')
    await expect(page.locator('body')).not.toContainText('private description')
    await expect(page.getByRole('img', { name: text.preview })).toHaveCount(0)
    expect(await page.evaluate(() => window.__captureFileProbe.created.length)).toBe(1)
    expect(writes).toEqual([])
  } finally { await other.close() }
})

for (const mode of ['worker', 'decode', 'encode'] as const) test(`redacts an imported album and pending ${mode} before actual BFCache restoration`, async ({ page, application }) => {
  await installProbe(page); await provision(page, application)
  const diagnostics = await permitBFCache(page, application.url), writes = watchDomainWrites(page, ['/api/v1/media', '/api/v1/items'])
  await page.goto('/photos/capture?owner_type=item&owner_id=synthetic-owner')
  await expect(page.getByLabel(text.fileInput)).toBeEnabled(); await page.waitForLoadState('networkidle')
  await selectPNG(page); await page.getByRole('textbox', { name: text.description }).fill('private description')
  await holdNext(page, mode)
  await leaveDocument(page, true); await returnFromBFCache(page, diagnostics)
  await expect(page.getByRole('heading', { name: text.restoreTitle })).toBeVisible()
  await expect(page.getByText(camera.restoreReady)).toBeVisible()
  const state = await page.evaluate(() => ({ created: window.__captureFileProbe.created, revoked: window.__captureFileProbe.revoked }))
  expect(state.revoked).toContain(state.created[0])
  await releaseHeld(page, mode)
  await page.getByRole('button', { name: camera.restoreContinue }).click()
  await expect(page.getByText(text.empty)).toBeVisible()
  await expect(page.locator('body')).not.toContainText('private description')
  expect(await page.evaluate(() => window.__captureFileProbe.created.length)).toBe(1)
  expect(writes).toEqual([])
})

test('keeps the picker, errors and imported album accessible at 320px and 200% text in both themes', async ({ page, application }) => {
  await installProbe(page); await openCapture(page, application)
  await page.setViewportSize({ width: 320, height: 568 })
  await page.locator('html').evaluate((element) => { element.style.fontSize = '200%' })
  const input = page.getByLabel(text.fileInput)
  await input.setInputFiles(file('invalid.png', 'image/png', Buffer.from('invalid image')))
  await expect(input).toHaveAttribute('aria-invalid', 'true')
  for (const colorScheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme }); await expect(page.locator('html')).toHaveAttribute('data-theme', colorScheme)
    expect(await page.evaluate(() => innerWidth)).toBe(320)
    await noOverflow(page)
    const geometry = await input.evaluate((element) => ({ width: element.clientWidth, content: element.scrollWidth,
      buttonFont: Number.parseFloat(getComputedStyle(element, '::file-selector-button').fontSize) }))
    expect(geometry.content).toBeLessThanOrEqual(geometry.width)
    expect(geometry.buttonFont).toBeGreaterThanOrEqual(30)
    expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze()).violations).toEqual([])
  }
  await input.focus(); await expect(input).toBeFocused()
  const chooser = page.waitForEvent('filechooser')
  await page.keyboard.press('Enter')
  await (await chooser).setFiles(file('PRIVATE-name.png', 'image/png', png))
  await expect(page.getByRole('img', { name: text.preview })).toBeVisible()
  await expect(page.getByRole('heading', { name: `${text.selected}：${text.photo} 1` })).toBeFocused()
  for (const colorScheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme }); await expect(page.locator('html')).toHaveAttribute('data-theme', colorScheme)
    expect(await page.evaluate(() => innerWidth)).toBe(320)
    await noOverflow(page)
    expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze()).violations).toEqual([])
  }
})
