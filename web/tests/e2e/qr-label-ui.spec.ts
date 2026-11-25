import AxeBuilder from '@axe-core/playwright'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import type { Page } from '@playwright/test'
import { build } from 'vite'
import { messages } from '../../src/lib/messages.ts'
import { test as applicationTest, expect } from './auth-fixture.ts'
import { leaveDocument, permitBFCache, returnFromBFCache } from './bfcache-helpers.ts'
import { noOverflow, provision, watchDomainWrites, writeContext } from './domain-helpers.ts'

type QRProbe = { started: number; terminated: number; hold: boolean; held: boolean; hidden: boolean; signals: number; deliver?: () => void; redacted?: boolean }
declare global { interface Window { __qrUI: QRProbe } }
const text = messages.qr
const test = applicationTest.extend<{}, { decoder: string }>({
  decoder: [async ({ browserName }, provideFixture) => {
    const directory = mkdtempSync(join(tmpdir(), `homebox-qr-ui-${browserName}-`))
    try {
      await build({ configFile: false, logLevel: 'silent', resolve: { alias: { '@': fileURLToPath(new URL('../../src', import.meta.url)) } },
        build: { outDir: directory, rollupOptions: { preserveEntrySignatures: 'strict', input: fileURLToPath(new URL('../../src/lib/scan-zxing-core.ts', import.meta.url)), output: { entryFileNames: 'assets/qr-decode-private.js' } } } })
      await provideFixture(join(directory, 'assets', 'qr-decode-private.js'))
    } finally { rmSync(directory, { recursive: true, force: true }) }
  }, { scope: 'worker' }],
})
test.use({ launchOptions: { channel: 'chromium', ignoreDefaultArgs: ['--disable-back-forward-cache'] }, trace: 'off', screenshot: 'off', video: 'off' })

async function post(page: Page, origin: string, path: string, data: object, revision?: number) {
  const response = await page.request.post(path, { headers: await writeContext(page, origin, revision), data })
  expect(response.status(), path).toBe(200)
  return response.json() as Promise<{ id: string; revision: number }>
}
async function location(page: Page, origin: string) { return post(page, origin, '/api/v1/locations', { name: 'QR 測試房間', code: 'QR-ROOM' }) }
async function unit(page: Page, origin: string) {
  const item = await post(page, origin, '/api/v1/items', { name: 'QR 測試物品' })
  return post(page, origin, `/api/v1/items/${item.id}/units`, { location_id: null }, item.revision)
}
const panel = (page: Page) => page.getByRole('region', { name: text.title, exact: true })

async function install(page: Page, transport?: 'broadcast' | 'storage') {
  await page.addInitScript((selected) => {
    const probe: QRProbe = { started: 0, terminated: 0, hold: false, held: false, hidden: false, signals: 0 }
    window.__qrUI = probe
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
    window.Worker = new Proxy(window.Worker, { construct(target, args, newTarget) {
      const worker = Reflect.construct(target, args, newTarget) as Worker
      if (!/\/qr-label-worker-[^/]+\.js$/.test(new URL(String(args[0]), window.location.href).pathname)) return worker
      probe.started++
      worker.addEventListener('message', (event) => {
        if (!probe.hold) return
        event.stopImmediatePropagation(); probe.held = true; probe.deliver = () => worker.onmessage?.call(worker, event)
      }, true)
      const terminate = worker.terminate.bind(worker)
      worker.terminate = () => { probe.terminated++; terminate() }
      return worker
    } })
  }, transport)
}

async function decodeVisible(page: Page, decoder: string, imageName: string) {
  await page.route('**/assets/qr-decode-private.js', (route) => route.fulfill({ path: decoder, contentType: 'text/javascript', headers: { 'Cache-Control': 'no-store' } }))
  return page.evaluate(async (name) => {
    const svg = document.querySelector<SVGSVGElement>(`svg[aria-label="${name}"]`)
    if (!svg) throw new Error('Missing QR SVG')
    const copy = svg.cloneNode(true) as SVGSVGElement, size = svg.viewBox.baseVal.width
    copy.setAttribute('width', String(size * 4)); copy.setAttribute('height', String(size * 4))
    const url = URL.createObjectURL(new Blob([new XMLSerializer().serializeToString(copy)], { type: 'image/svg+xml' }))
    try {
      const image = new Image(); image.src = url; await image.decode()
      const canvas = document.createElement('canvas'); canvas.width = size * 4; canvas.height = size * 4
      const context = canvas.getContext('2d')
      if (!context) throw new Error('Canvas unavailable')
      context.imageSmoothingEnabled = false; context.drawImage(image, 0, 0)
      const rgba = context.getImageData(0, 0, canvas.width, canvas.height).data
      const pixels = new Uint8ClampedArray(canvas.width * canvas.height)
      for (let index = 0; index < pixels.length; index++) pixels[index] = rgba[index * 4]!
      const modulePath = '/assets/qr-decode-private.js', decoderModule = await import(modulePath)
      return decoderModule.decodeZXing(pixels, canvas.width, canvas.height, ['QR_CODE'])
    } finally { URL.revokeObjectURL(url) }
  }, imageName)
}

// Real production UI/worker and disposable Go sources; pixel decoding uses the
// same ZXing package as the writer, not an independent scanner or printer.
for (const kind of ['location', 'unit'] as const) test(`renders an exact ${kind} QR from current protected source without writes`, async ({ page, application, decoder }) => {
  await install(page); await provision(page, application)
  const source = kind === 'location' ? await location(page, application.url) : await unit(page, application.url)
  const writes = watchDomainWrites(page, ['/api/v1/locations', '/api/v1/items', '/api/v1/units', '/api/v1/stock'])
  const reads: string[] = []
  page.on('request', (request) => { const path = new URL(request.url()).pathname; if (request.method() === 'GET' && (path === '/api/v1/auth/me' || path === `/api/v1/${kind === 'location' ? 'locations' : 'units'}/${source.id}`)) reads.push(path) })
  await page.goto(`/${kind === 'location' ? 'locations' : 'units'}/${source.id}`)
  const region = panel(page), button = region.getByRole('button', { name: text.create })
  await expect(button).toHaveAttribute('aria-disabled', 'false')
  const initial = reads.length
  expect(await page.evaluate(() => window.__qrUI.started)).toBe(0)
  await button.click()
  const imageName = kind === 'location' ? text.locationImage : text.unitImage
  await expect(region.getByRole('img', { name: imageName })).toBeVisible()
  expect(reads.slice(initial).filter((path) => path === '/api/v1/auth/me')).toHaveLength(2)
  expect(reads.slice(initial).filter((path) => path.endsWith(`/${source.id}`))).toHaveLength(2)
  const url = `${application.url}/${kind === 'location' ? 'l' : 'u'}/${source.id}`
  await expect(region.getByText(url, { exact: true })).toBeVisible()
  expect(await decodeVisible(page, decoder, imageName)).toEqual([{ format: 'QR_CODE', rawValue: url }])
  expect(await page.evaluate(() => ({ started: window.__qrUI.started, terminated: window.__qrUI.terminated }))).toEqual({ started: 1, terminated: 1 })
  expect(writes).toEqual([])
  await page.goto(url)
  await expect(page).toHaveURL(new RegExp(`/${kind === 'location' ? 'locations' : 'units'}/${source.id}$`))
  await expect(page.getByRole('heading', { level: 1, name: kind === 'location' ? 'QR 測試房間' : messages.holdings.unitDetail })).toBeVisible()
})

test('rejects a real source change while the generated matrix is delayed', async ({ page, application }) => {
  await install(page); await provision(page, application)
  const source = await location(page, application.url)
  await page.goto(`/locations/${source.id}`)
  const region = panel(page), button = region.getByRole('button', { name: text.create })
  await expect(button).toHaveAttribute('aria-disabled', 'false')
  await page.evaluate(() => { window.__qrUI.hold = true })
  await button.click(); await expect.poll(() => page.evaluate(() => window.__qrUI.held)).toBe(true)
  const changed = await page.request.patch(`/api/v1/locations/${source.id}`, { headers: await writeContext(page, application.url, source.revision), data: { name: '已變更 QR 位置' } })
  expect(changed.status()).toBe(200)
  await page.evaluate(() => { window.__qrUI.deliver?.() })
  await expect(region.getByRole('alert')).toHaveText(text.changed)
  await expect(region.getByRole('img')).toHaveCount(0)
  expect(await page.evaluate(() => window.__qrUI.terminated)).toBe(1)
})

for (const transport of ['broadcast', 'storage'] as const) test(`removes a delayed QR after real logout using ${transport} only`, async ({ page, context, application }) => {
  await install(page, transport); await provision(page, application)
  const source = await location(page, application.url), writes = watchDomainWrites(page, ['/api/v1/locations', '/api/v1/items', '/api/v1/units'])
  await page.goto(`/locations/${source.id}`)
  const region = panel(page), button = region.getByRole('button', { name: text.create })
  await expect(button).toHaveAttribute('aria-disabled', 'false')
  await page.evaluate(() => { window.__qrUI.hold = true })
  await button.click(); await expect.poll(() => page.evaluate(() => window.__qrUI.held)).toBe(true)
  const other = await context.newPage()
  try {
    await other.goto('/settings/account')
    await other.getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await other.getByRole('alertdialog').getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await expect.poll(() => page.evaluate(() => window.__qrUI.signals)).toBeGreaterThan(0)
    await expect(region.getByText(text.cleared)).toBeVisible()
    expect(await page.evaluate(() => window.__qrUI.hidden)).toBe(false)
    expect(await page.evaluate(() => window.__qrUI.terminated)).toBe(1)
    await page.evaluate(() => { window.__qrUI.deliver?.() })
    await expect(region.getByRole('img')).toHaveCount(0); expect(writes).toEqual([])
  } finally { await other.close() }
})

test('clears a QR while offline and requires a new explicit generation on reconnect', async ({ page, context, application }) => {
  await install(page); await provision(page, application)
  const source = await location(page, application.url)
  await page.goto(`/locations/${source.id}`)
  const region = panel(page), button = region.getByRole('button', { name: text.create })
  await expect(button).toHaveAttribute('aria-disabled', 'false'); await button.click()
  await expect(region.getByRole('img', { name: text.locationImage })).toBeVisible()
  await context.setOffline(true)
  await expect(region.getByRole('img')).toHaveCount(0); await expect(region.getByText(text.offline)).toBeVisible()
  await context.setOffline(false)
  await expect(region.getByRole('button', { name: text.create })).toHaveAttribute('aria-disabled', 'false')
  expect(await page.evaluate(() => window.__qrUI.started)).toBe(1)
  await region.getByRole('button', { name: text.create }).click()
  await expect(region.getByRole('img', { name: text.locationImage })).toBeVisible()
  expect(await page.evaluate(() => window.__qrUI.started)).toBe(2)
})

test('redacts a rendered QR before actual BFCache and requires explicit revalidation', async ({ page, application }) => {
  await install(page); await provision(page, application)
  const source = await location(page, application.url), diagnostics = await permitBFCache(page, application.url)
  await page.goto(`/locations/${source.id}`)
  const region = panel(page), button = region.getByRole('button', { name: text.create })
  await expect(button).toHaveAttribute('aria-disabled', 'false'); await button.click()
  await expect(region.getByRole('img', { name: text.locationImage })).toBeVisible()
  await page.waitForLoadState('networkidle')
  await page.evaluate((imageName) => { window.addEventListener('pagehide', () => { window.__qrUI.redacted = !document.querySelector(`svg[aria-label="${imageName}"]`) }) }, text.locationImage)
  await leaveDocument(page, false); await returnFromBFCache(page, diagnostics)
  expect(await page.evaluate(() => window.__qrUI.redacted)).toBe(true)
  await expect(region.getByText(text.restore)).toBeVisible(); await expect(region.getByRole('img')).toHaveCount(0)
  expect(await page.evaluate(() => window.__qrUI.started)).toBe(1)
  const resume = region.getByRole('button', { name: text.continue })
  await expect(resume).toHaveAttribute('aria-disabled', 'false')
  let fail = true
  await page.route((url) => url.pathname === '/api/v1/auth/me', (route) => {
    if (!fail) return route.continue()
    fail = false
    return route.fulfill({ status: 503, contentType: 'application/json', body: '{}' })
  })
  await resume.click()
  await expect(region.getByRole('alert')).toHaveText(text.error)
  await expect(region.getByRole('img')).toHaveCount(0)
  expect(await page.evaluate(() => window.__qrUI.started)).toBe(1)
  await resume.click()
  await expect(region.getByRole('img', { name: text.locationImage })).toBeVisible()
  expect(await page.evaluate(() => window.__qrUI.started)).toBe(2)
})

test('keeps QR controls and exact identity accessible at 320px and 200% text', async ({ page, application }, info) => {
  await install(page); await provision(page, application)
  const source = await location(page, application.url)
  await page.setViewportSize({ width: 320, height: 700 }); await page.goto(`/locations/${source.id}`)
  await page.evaluate(() => { document.documentElement.style.fontSize = '200%' })
  const region = panel(page), button = region.getByRole('button', { name: text.create })
  await expect(button).toHaveAttribute('aria-disabled', 'false'); await button.focus(); await page.keyboard.press('Enter')
  await expect(region.getByRole('img', { name: text.locationImage })).toBeVisible()
  const again = region.getByRole('button', { name: text.again })
  await expect(again).toBeFocused()
  const lines = await region.evaluate((element) => {
    const heading = element.querySelector('h2'), button = element.querySelector('button')
    if (!heading || !button) throw new Error('Missing label controls')
    const h = getComputedStyle(heading), b = getComputedStyle(button)
    return { heading: Math.round(heading.clientHeight / parseFloat(h.lineHeight)),
      button: Math.round((button.clientHeight - parseFloat(b.paddingTop) - parseFloat(b.paddingBottom)) / parseFloat(b.lineHeight)),
      buttonFits: button.scrollWidth <= button.clientWidth }
  })
  expect(lines.heading).toBe(1); expect(lines.button).toBeLessThanOrEqual(2); expect(lines.buttonFits).toBe(true)
  for (const colorScheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme }); await expect(page.locator('html')).toHaveAttribute('data-theme', colorScheme)
    await noOverflow(page)
    const sizes = await page.evaluate(() => ({ width: document.documentElement.scrollWidth, viewport: window.visualViewport?.width ?? 0 }))
    expect(sizes.width).toBeLessThanOrEqual(Math.ceil(sizes.viewport))
    expect(await region.getByRole('img').evaluate((element) => element.getBoundingClientRect().width)).toBeGreaterThan(0)
    expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze()).violations).toEqual([])
    await region.screenshot({ path: info.outputPath(`qr-label-${colorScheme}.png`) })
  }
})
