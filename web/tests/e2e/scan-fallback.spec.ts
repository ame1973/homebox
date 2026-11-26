import AxeBuilder from '@axe-core/playwright'
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import type { Page } from '@playwright/test'
import { messages } from '../../src/lib/messages.ts'
import { test as applicationTest, expect } from './auth-fixture.ts'
import { leaveDocument, permitBFCache, returnFromBFCache } from './bfcache-helpers.ts'
import { noOverflow, provision, watchDomainWrites } from './domain-helpers.ts'

// These GS1 bit patterns are independent of ZXing's reader. Chromium receives
// one uncompressed synthetic frame through its real fake-camera device.
function barcodeVideo(): Buffer {
  const digits = '0042100005264'
  const left = ['0001101', '0011001', '0010011', '0111101', '0100011', '0110001', '0101111', '0111011', '0110111', '0001011']
  const invert = (value: string) => [...value].map((bit) => bit === '0' ? '1' : '0').join('')
  const bits = '101' + [...digits.slice(1, 7)].map((value) => left[Number(value)]!).join('') + '01010' +
    [...digits.slice(7)].map((value) => invert(left[Number(value)]!)).join('') + '101'
  const width = 256, height = 128, pixels = Buffer.alloc(width * height, 235), leftEdge = Math.floor((width - bits.length * 2) / 2)
  for (let y = 24; y < 104; y++) for (let x = 0; x < bits.length * 2; x++) {
    if (bits[Math.floor(x / 2)] === '1') pixels[y * width + leftEdge + x] = 16
  }
  return Buffer.concat([Buffer.from(`YUV4MPEG2 W${width} H${height} F30:1 Ip A1:1 C420jpeg\nFRAME\n`), pixels, Buffer.alloc(width * height / 2, 128)])
}

type FallbackProbe = { workers: string[]; terminated: number; tracks: MediaStreamTrack[]; hold: boolean; deliver?: () => void }
declare global { interface Window { __fallbackProbe: FallbackProbe } }

const test = applicationTest.extend<{}, { barcodeVideoFile: string }>({
  barcodeVideoFile: [async ({ browserName }, provideFixture) => {
    const directory = mkdtempSync(join(tmpdir(), `homebox-barcode-video-${browserName}-`))
    const file = join(directory, 'ean.y4m')
    try {
      writeFileSync(file, barcodeVideo(), { mode: 0o600, flag: 'wx' })
      await provideFixture(file)
    } finally { rmSync(directory, { recursive: true, force: true }) }
  }, { scope: 'worker' }],
  browser: [async ({ playwright, launchOptions, barcodeVideoFile }, provideFixture) => {
    const browser = await playwright.chromium.launch({ ...launchOptions, args: [...(launchOptions.args ?? []), '--use-fake-device-for-media-stream', '--use-fake-ui-for-media-stream', `--use-file-for-fake-video-capture=${barcodeVideoFile}`] })
    try { await provideFixture(browser) } finally { await browser.close() }
  }, { scope: 'worker' }],
})
test.use({
  permissions: ['camera'], trace: 'off', screenshot: 'off', video: 'off',
  launchOptions: { channel: 'chromium', ignoreDefaultArgs: ['--disable-back-forward-cache'] },
})
const text = messages.scan

async function installProbe(page: Page, mode: 'none' | 'partial' | 'all') {
  await page.addInitScript((coverage) => {
    const probe: FallbackProbe = { workers: [], terminated: 0, tracks: [], hold: false }
    window.__fallbackProbe = probe
    const OriginalWorker = window.Worker
    window.Worker = new Proxy(OriginalWorker, { construct(target, args, newTarget) {
      const worker = Reflect.construct(target, args, newTarget) as Worker
      probe.workers.push(String(args[0]))
      worker.addEventListener('message', (event) => {
        if (probe.hold && event.data?.type === 'result') {
          probe.hold = false; event.stopImmediatePropagation()
          probe.deliver = () => worker.onmessage?.call(worker, event)
        }
      }, true)
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
    if (coverage === 'none') Object.defineProperty(window, 'BarcodeDetector', { configurable: true, value: undefined })
    else {
      class NativeDetector {
        static async getSupportedFormats() { return coverage === 'all' ? ['qr_code', 'ean_13', 'ean_8', 'upc_a', 'upc_e', 'code_128'] : ['ean_13'] }
        async detect() { return [{ format: 'ean_13', rawValue: '0042100005264' }] }
      }
      Object.defineProperty(window, 'BarcodeDetector', { configurable: true, value: NativeDetector })
    }
  }, mode)
}

for (const mode of ['none', 'partial'] as const) test(`uses real worker pixels for ${mode} native coverage without an automatic lookup`, async ({ page, application }) => {
  await installProbe(page, mode)
  await provision(page, application)
  const writes = watchDomainWrites(page, ['/api/v1/items', '/api/v1/barcodes', '/api/v1/inventory']), lookups: URL[] = []
  page.on('request', (request) => { const url = new URL(request.url()); if (url.pathname === '/api/v1/search/barcodes') lookups.push(url) })
  await page.goto('/')
  await page.waitForLoadState('networkidle')
  expect(await page.evaluate(() => window.__fallbackProbe.workers)).toEqual([])
  await page.goto('/scan')
  await expect(page.getByText(text.fallbackFormats, { exact: true })).toBeVisible()
  if (mode === 'partial') await expect(page.getByText(text.nativeFormats, { exact: true })).toBeVisible()
  await expect(page.getByText(text.nativeMissing, { exact: true })).toHaveCount(0)
  expect(await page.evaluate(() => window.__fallbackProbe.tracks.length)).toBe(0)
  await page.getByRole('button', { name: text.cameraStart }).click()
  const results = page.getByRole('list', { name: text.nativeResults })
  await expect(results).toContainText('"0042100005264"', { timeout: 15_000 })
  await expect(results).toContainText('"042100005264"')
  await expect(page.getByText(text.nativeAmbiguous)).toBeVisible()
  await expect.poll(() => page.evaluate(() => window.__fallbackProbe.tracks[0]?.readyState)).toBe('ended')
  await expect.poll(() => page.evaluate(() => window.__fallbackProbe.terminated)).toBeGreaterThan(0)
  const workerURLs = await page.evaluate(() => window.__fallbackProbe.workers)
  expect(workerURLs.length).toBeGreaterThan(0)
  expect(workerURLs.every((value) => new URL(value, application.url).origin === application.url)).toBe(true)
  expect(lookups).toEqual([]); expect(writes).toEqual([])
  await results.getByRole('listitem').filter({ hasText: '"0042100005264"' }).getByRole('button', { name: text.nativeUse }).click()
  await expect(page.getByRole('textbox', { name: text.input })).toHaveValue('"0042100005264"')
  await expect(page.getByRole('combobox', { name: text.symbology })).toHaveValue('EAN_13')
  expect(lookups).toEqual([])
  await page.getByRole('button', { name: text.search }).click()
  await expect(page.getByRole('heading', { name: text.empty })).toBeVisible()
  expect(lookups).toHaveLength(1)
  expect(lookups[0]?.searchParams.get('value')).toBe('0042100005264')
  expect(lookups[0]?.searchParams.get('symbology')).toBe('EAN_13')
  expect(writes).toEqual([])
})

test('terminates a held real worker frame when video playback pauses', async ({ page, application }) => {
  await installProbe(page, 'none'); await provision(page, application)
  await page.goto('/scan')
  await expect(page.getByText(text.fallbackFormats, { exact: true })).toBeVisible()
  const writes = watchDomainWrites(page, ['/api/v1/items', '/api/v1/barcodes'])
  await page.evaluate(() => { window.__fallbackProbe.hold = true })
  await page.getByRole('button', { name: text.cameraStart }).click()
  const video = page.getByLabel(text.cameraVideo)
  await video.scrollIntoViewIfNeeded()
  await expect.poll(() => page.evaluate(() => !!window.__fallbackProbe.deliver)).toBe(true)
  await video.evaluate(async (element: HTMLVideoElement) => {
    const paused = new Promise<void>((resolve) => element.addEventListener('pause', () => resolve(), { once: true }))
    element.pause(); await paused
  })
  await expect.poll(() => page.evaluate(() => window.__fallbackProbe.terminated)).toBeGreaterThan(0)
  expect(await page.evaluate(() => window.__fallbackProbe.tracks[0]?.readyState)).toBe('live')
  await page.evaluate(() => { window.__fallbackProbe.deliver?.() })
  await expect(page.getByRole('list', { name: text.nativeResults })).toHaveCount(0)
  await video.evaluate(async (element: HTMLVideoElement) => { await element.play() })
  await expect(page.getByRole('list', { name: text.nativeResults })).toContainText('"0042100005264"')
  await expect.poll(() => page.evaluate(() => window.__fallbackProbe.tracks[0]?.readyState)).toBe('ended')
  expect(writes).toEqual([])
})

test('terminates a real worker and ignores its delayed result when the page becomes hidden', async ({ page, application }) => {
  await installProbe(page, 'none'); await provision(page, application)
  await page.goto('/scan')
  await expect(page.getByText(text.fallbackFormats, { exact: true })).toBeVisible()
  await page.evaluate(() => { window.__fallbackProbe.hold = true })
  await page.getByRole('button', { name: text.cameraStart }).click()
  await expect.poll(() => page.evaluate(() => !!window.__fallbackProbe.deliver)).toBe(true)
  await page.evaluate(() => { Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'hidden' }); document.dispatchEvent(new Event('visibilitychange')) })
  await expect.poll(() => page.evaluate(() => window.__fallbackProbe.tracks[0]?.readyState)).toBe('ended')
  expect(await page.evaluate(() => window.__fallbackProbe.terminated)).toBeGreaterThan(0)
  await page.evaluate(() => { window.__fallbackProbe.deliver?.(); Reflect.deleteProperty(document, 'visibilityState'); document.dispatchEvent(new Event('visibilitychange')) })
  await expect(page.getByRole('list', { name: text.nativeResults })).toHaveCount(0)
  expect(await page.evaluate(() => window.__fallbackProbe.tracks.length)).toBe(1)
  await page.getByRole('button', { name: text.cameraStart }).click()
  await expect(page.getByRole('list', { name: text.nativeResults })).toContainText('"0042100005264"')
  expect(await page.evaluate(() => window.__fallbackProbe.tracks.length)).toBe(2)
})

for (const transport of ['storage', 'broadcast'] as const) test(`terminates a real worker after cross-tab logout using ${transport} only`, async ({ page, context, application }) => {
  await page.addInitScript((selected) => {
    if (selected === 'storage') Object.defineProperty(window, 'BroadcastChannel', { configurable: true, value: undefined })
    else window.addEventListener = new Proxy(window.addEventListener, { apply(target, receiver, args: unknown[]) {
      return args[0] === 'storage' ? undefined : Reflect.apply(target, receiver, args)
    } })
  }, transport)
  await installProbe(page, 'none'); await provision(page, application)
  await page.goto('/scan')
  await expect(page.getByText(text.fallbackFormats, { exact: true })).toBeVisible()
  await page.evaluate(() => { Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' }); window.__fallbackProbe.hold = true })
  await page.getByRole('textbox', { name: text.input }).fill('private scan draft')
  await page.getByRole('button', { name: text.cameraStart }).click()
  await expect.poll(() => page.evaluate(() => !!window.__fallbackProbe.deliver)).toBe(true)
  const other = await context.newPage()
  try {
    await other.goto('/settings/account')
    await other.getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await other.getByRole('alertdialog').getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await expect(page.getByText(text.sessionClearedHelp)).toBeVisible()
    expect(await page.evaluate(() => window.__fallbackProbe.tracks[0]?.readyState)).toBe('ended')
    expect(await page.evaluate(() => window.__fallbackProbe.terminated)).toBeGreaterThan(0)
    await page.evaluate(() => { window.__fallbackProbe.deliver?.() })
    await expect(page.getByRole('list', { name: text.nativeResults })).toHaveCount(0)
    await expect(page.locator('body')).not.toContainText('0042100005264')
    await expect(page.locator('body')).not.toContainText('private scan draft')
  } finally { await other.close() }
})

test('discards a real worker result before restoring the private scan page', async ({ page, application }) => {
  await installProbe(page, 'none'); await provision(page, application)
  const diagnostics = await permitBFCache(page, application.url)
  await page.goto('/scan')
  await expect(page.getByText(text.fallbackFormats, { exact: true })).toBeVisible()
  await page.getByRole('button', { name: text.cameraStart }).click()
  await expect(page.getByRole('list', { name: text.nativeResults })).toContainText('"0042100005264"')
  expect(await page.evaluate(() => window.__fallbackProbe.terminated)).toBeGreaterThan(0)
  await leaveDocument(page, false)
  await returnFromBFCache(page, diagnostics)
  await expect(page.getByText(text.restoreReady)).toBeVisible()
  await expect(page.locator('body')).not.toContainText('0042100005264')
  await page.getByRole('button', { name: text.restoreContinue }).click()
  await expect(page.getByRole('textbox', { name: text.input })).toHaveValue('')
  await expect(page.getByRole('list', { name: text.nativeResults })).toHaveCount(0)
  expect(await page.evaluate(() => window.__fallbackProbe.tracks.length)).toBe(1)
})

test('keeps real worker results accessible and restores focus at 320px and 200% text', async ({ page, application }) => {
  await installProbe(page, 'none'); await provision(page, application)
  await page.setViewportSize({ width: 320, height: 568 }); await page.goto('/scan')
  await page.locator('html').evaluate((element) => { element.style.fontSize = '200%' })
  await expect(page.getByText(text.fallbackFormats, { exact: true })).toBeVisible()
  await page.evaluate(() => { window.__fallbackProbe.hold = true })
  await page.getByRole('button', { name: text.cameraStart }).click()
  await expect.poll(() => page.evaluate(() => !!window.__fallbackProbe.deliver)).toBe(true)
  await page.getByRole('button', { name: text.nativePause }).focus()
  await page.evaluate(() => { window.__fallbackProbe.deliver?.() })
  await expect(page.getByRole('heading', { name: text.nativeResults })).toBeFocused()
  await expect(page.getByText(text.nativeAmbiguous)).toBeVisible()
  for (const colorScheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme }); await expect(page.locator('html')).toHaveAttribute('data-theme', colorScheme)
    expect(await page.evaluate(() => innerWidth)).toBe(320)
    await noOverflow(page)
    const camera = page.getByRole('region', { name: text.cameraTitle })
    for (const button of await camera.getByRole('button').all()) expect(await button.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)
    expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze()).violations).toEqual([])
  }
  await page.getByRole('button', { name: text.nativeClear }).click()
  await expect(page.getByRole('button', { name: text.cameraStart })).toBeFocused()
})

test('does not create the ZXing worker when native support covers every format', async ({ page, application }) => {
  await installProbe(page, 'all'); await provision(page, application)
  await page.goto('/scan')
  await expect(page.getByText(text.nativeFormats, { exact: true })).toBeVisible()
  await expect(page.getByText(text.fallbackFormats, { exact: true })).toHaveCount(0)
  expect(await page.evaluate(() => window.__fallbackProbe.workers)).toEqual([])
  await page.getByRole('button', { name: text.cameraStart }).click()
  await expect(page.getByRole('list', { name: text.nativeResults })).toContainText('"0042100005264"')
  expect(await page.evaluate(() => window.__fallbackProbe.workers)).toEqual([])
})
