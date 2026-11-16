import AxeBuilder from '@axe-core/playwright'
import type { Page } from '@playwright/test'
import { messages } from '../../src/lib/messages.ts'
import { quoteExactText } from '../../src/lib/text-literal.ts'
import { test, expect } from './auth-fixture.ts'
import { noOverflow, provision, watchDomainWrites, writeContext } from './domain-helpers.ts'
import { installNativeProbe } from './native-scan-probe.ts'

// The detector is a test stub and the video is Chromium's fake device. These
// checks are not native pixel decoding or physical-device acceptance.
test.use({ launchOptions: { args: ['--use-fake-device-for-media-stream', '--use-fake-ui-for-media-stream'] }, permissions: ['camera'], trace: 'off', screenshot: 'off', video: 'off' })
const text = messages.scan

async function openScan(page: Page) {
  await page.goto('/scan')
  await expect(page.getByRole('button', { name: text.cameraStart })).toBeEnabled()
  await expect(page.getByText(text.nativeFormats, { exact: true })).toBeVisible()
}
async function trackState(page: Page, index: number) {
  return await page.evaluate((position) => window.__nativeScanProbe.tracks[position]?.readyState, index)
}

test('displays multiple exact detections and performs only the selected explicit Go lookup', async ({ page, application }) => {
  await installNativeProbe(page)
  await provision(page, application)
  const writes = watchDomainWrites(page, ['/api/v1/items', '/api/v1/barcodes', '/api/v1/inventory']), queries: URL[] = []
  page.on('request', (request) => { const url = new URL(request.url()); if (url.pathname === '/api/v1/search/barcodes') queries.push(url) })
  await openScan(page)
  await expect(page.getByText(text.nativeMissing, { exact: true })).toBeVisible()
  const raw = '\0legacy é\r\n000123', outside = 'https://outside.example/u/private'
  await page.evaluate(({ rawValue, external, origin }) => {
    window.__nativeScanProbe.queue.push([
      { format: 'upc_a', rawValue }, { format: 'ean_13', rawValue: '0001234567890' },
      { format: 'qr_code', rawValue: `${origin}/l/room` }, { format: 'qr_code', rawValue: `${origin}/u/device%2B1` },
      { format: 'qr_code', rawValue: `${origin}/u/%252F` }, { format: 'qr_code', rawValue: external },
      { format: 'qr_code', rawValue: 'x'.repeat(513) },
    ])
  }, { rawValue: raw, external: outside, origin: application.url })
  expect(await page.evaluate(() => window.__nativeScanProbe.calls)).toBe(0)
  await page.getByRole('textbox', { name: text.input }).fill('previous draft')
  await page.getByRole('button', { name: text.cameraStart }).click()
  const results = page.getByRole('list', { name: text.nativeResults })
  await expect(results.getByRole('listitem')).toHaveCount(7)
  expect(await trackState(page, 0)).toBe('ended')
  const detector = await page.evaluate(() => ({ requested: window.__nativeScanProbe.requested, frames: window.__nativeScanProbe.frames }))
  expect(detector.requested).toEqual(['qr_code', 'ean_13', 'upc_a'])
  expect(detector.frames).toEqual([{ width: expect.any(Number), currentTrack: true }])
  expect(detector.frames[0]!.width).toBeGreaterThan(0)
  await expect(results).toContainText(quoteExactText(raw))
  await expect(results.getByRole('link', { name: text.nativeOpenLocation })).toHaveAttribute('href', '/locations/room')
  await expect(results.getByRole('link', { name: text.nativeOpenUnit })).toHaveAttribute('href', '/units/device%2B1')
  await expect(results.getByRole('link')).toHaveCount(2)
  await expect(results.getByText(text.nativeUntrustedQR)).toHaveCount(3)
  await expect(results.getByText(text.nativeTooLong)).toBeVisible()
  expect(queries).toHaveLength(0)
  await results.getByRole('listitem').first().getByRole('button', { name: text.nativeUse }).click()
  const input = page.getByRole('textbox', { name: text.input })
  await expect(input).toHaveValue(quoteExactText(raw))
  await expect(input).toBeFocused()
  await expect(page.getByRole('combobox', { name: text.inputFormat })).toHaveValue('json')
  await expect(page.getByRole('combobox', { name: text.symbology })).toHaveValue('UPC_A')
  await expect(results).toHaveCount(0)
  expect(queries).toHaveLength(0)
  expect(page.url()).not.toContain('legacy')
  expect(await page.evaluate(() => JSON.stringify(history.state))).not.toContain('legacy')
  await page.getByRole('button', { name: text.search }).click()
  await expect(page.getByRole('heading', { name: text.empty })).toBeVisible()
  expect(queries).toHaveLength(1)
  expect(queries[0]?.searchParams.get('value')).toBe(raw)
  expect(queries[0]?.searchParams.get('symbology')).toBe('UPC_A')
  expect(writes).toEqual([])
})

test('opens an actual Location only after selecting a validated same-origin QR link', async ({ page, application }) => {
  await installNativeProbe(page); await provision(page, application)
  const created = await page.request.post('/api/v1/locations', { headers: await writeContext(page, application.url), data: { name: 'Scanned room', code: 'QR-ROOM', kind: 'room', parent_id: null } })
  expect(created.status()).toBe(200)
  const location = await created.json() as { id: string }
  const writes = watchDomainWrites(page, ['/api/v1/items', '/api/v1/barcodes', '/api/v1/locations'])
  await openScan(page)
  await page.evaluate(({ origin, id }) => {
    window.__nativeScanProbe.queue.push([{ format: 'qr_code', rawValue: `${origin}/l/${encodeURIComponent(id)}` }, { format: 'qr_code', rawValue: 'https://outside.example/l/other' }])
  }, { origin: application.url, id: location.id })
  await page.getByRole('button', { name: text.cameraStart }).click()
  const results = page.getByRole('list', { name: text.nativeResults })
  await expect(results.getByRole('listitem')).toHaveCount(2)
  await expect(page).toHaveURL(`${application.url}/scan`)
  await expect(results.getByRole('link')).toHaveCount(1)
  await results.getByRole('link', { name: text.nativeOpenLocation }).click()
  await expect(page).toHaveURL(`${application.url}/locations/${encodeURIComponent(location.id)}`)
  await expect(page.getByRole('heading', { level: 1, name: 'Scanned room' })).toBeVisible()
  expect(await trackState(page, 0)).toBe('ended')
  expect(writes).toEqual([])
})

test('pauses a pending detection without stopping the camera and resumes only on request', async ({ page, application }) => {
  await installNativeProbe(page); await provision(page, application); await openScan(page)
  const writes = watchDomainWrites(page, ['/api/v1/items', '/api/v1/barcodes'])
  await page.evaluate(() => { window.__nativeScanProbe.hold = true })
  await page.getByRole('button', { name: text.cameraStart }).click()
  await expect.poll(() => page.evaluate(() => !!window.__nativeScanProbe.finish)).toBe(true)
  await page.getByRole('button', { name: text.nativePause }).click()
  await expect(page.getByText(text.nativePaused)).toBeVisible()
  expect(await trackState(page, 0)).toBe('live')
  await page.evaluate(() => {
    window.__nativeScanProbe.queue.push([{ format: 'upc_a', rawValue: '000123' }])
    window.__nativeScanProbe.finish?.([{ format: 'qr_code', rawValue: 'stale private detection' }])
  })
  await expect(page.getByRole('list', { name: text.nativeResults })).toHaveCount(0)
  await expect.poll(() => page.evaluate(() => window.__nativeScanProbe.calls)).toBe(1)
  await page.getByRole('button', { name: text.nativeResume }).click()
  const results = page.getByRole('list', { name: text.nativeResults })
  await expect(results).toContainText('"000123"')
  await expect(results).not.toContainText('stale private detection')
  await expect(page.getByRole('heading', { name: text.nativeResults })).toBeFocused()
  expect(await trackState(page, 0)).toBe('ended')
  expect(await page.evaluate(() => window.__nativeScanProbe.calls)).toBe(2)
  await page.getByRole('button', { name: text.nativeClear }).click()
  await expect(page.getByRole('button', { name: text.cameraStart })).toBeFocused()
  expect(writes).toEqual([])
})

test('discards a late old-camera result and serializes detection while switching cameras', async ({ page, application }) => {
  await installNativeProbe(page); await provision(page, application); await openScan(page)
  await page.evaluate(() => { window.__nativeScanProbe.hold = true })
  await page.getByRole('button', { name: text.cameraStart }).click()
  await expect.poll(() => page.evaluate(() => !!window.__nativeScanProbe.finish)).toBe(true)
  await page.getByRole('button', { name: text.cameraSwitch }).click()
  await expect.poll(() => trackState(page, 1)).toBe('live')
  expect(await trackState(page, 0)).toBe('ended')
  expect(await page.evaluate(() => window.__nativeScanProbe.calls)).toBe(1)
  await page.evaluate(() => {
    window.__nativeScanProbe.queue.push([{ format: 'ean_13', rawValue: '0001234567890' }])
    window.__nativeScanProbe.finish?.([{ format: 'upc_a', rawValue: 'old camera' }])
  })
  const results = page.getByRole('list', { name: text.nativeResults })
  await expect(results).toContainText('"0001234567890"')
  await expect(results).not.toContainText('old camera')
  expect(await trackState(page, 1)).toBe('ended')
  expect(await page.evaluate(() => window.__nativeScanProbe.frames.map((frame) => frame.currentTrack))).toEqual([true, true])
})

test('retains a selected offline result without lookup or replay until explicit submission', async ({ page, context, application }) => {
  await installNativeProbe(page); await provision(page, application); await openScan(page)
  const writes = watchDomainWrites(page, ['/api/v1/items', '/api/v1/barcodes']), queries: URL[] = []
  page.on('request', (request) => { const url = new URL(request.url()); if (url.pathname === '/api/v1/search/barcodes') queries.push(url) })
  await context.setOffline(true)
  try {
    await expect(page.getByText(text.offline, { exact: true })).toBeVisible()
    await page.evaluate(() => { window.__nativeScanProbe.queue.push([{ format: 'upc_a', rawValue: '000123' }]) })
    await page.getByRole('button', { name: text.cameraStart }).click()
    await page.getByRole('list', { name: text.nativeResults }).getByRole('button', { name: text.nativeUse }).click()
    await expect(page.getByRole('textbox', { name: text.input })).toHaveValue('"000123"')
    await expect(page.getByRole('button', { name: text.search })).toBeDisabled()
    expect(queries).toHaveLength(0)
  } finally { await context.setOffline(false) }
  await expect(page.getByRole('button', { name: text.search })).toBeEnabled()
  expect(queries).toHaveLength(0)
  await page.getByRole('button', { name: text.search }).click()
  await expect(page.getByRole('heading', { name: text.empty })).toBeVisible()
  expect(queries).toHaveLength(1)
  expect(queries[0]?.searchParams.get('value')).toBe('000123')
  expect(writes).toEqual([])
})

test('keeps a hung detector single-flight across SPA navigation until a real reload', async ({ page, application }) => {
  await installNativeProbe(page); await provision(page, application); await openScan(page)
  await page.evaluate(() => { window.__nativeScanProbe.hold = true })
  await page.getByRole('button', { name: text.cameraStart }).click()
  await expect.poll(() => page.evaluate(() => !!window.__nativeScanProbe.finish)).toBe(true)
  await page.getByRole('link', { name: messages.catalog.nav, exact: true }).click()
  await expect(page).toHaveURL(/\/items$/)
  await expect.poll(() => trackState(page, 0)).toBe('ended')
  await page.getByRole('link', { name: text.nav, exact: true }).click()
  await expect(page.getByRole('button', { name: text.cameraStart })).toBeEnabled()
  await page.getByRole('button', { name: text.cameraStart }).click()
  await expect.poll(() => trackState(page, 1)).toBe('live')
  expect(await page.evaluate(() => window.__nativeScanProbe.calls)).toBe(1)
  await expect(page.getByText(text.nativeTimeout)).toBeVisible({ timeout: 12_000 })
  expect(await trackState(page, 1)).toBe('ended')
  await page.evaluate(() => { window.__nativeScanProbe.finish?.([{ format: 'upc_a', rawValue: 'stale hung result' }]) })
  await page.getByRole('link', { name: messages.catalog.nav, exact: true }).click()
  await page.getByRole('link', { name: text.nav, exact: true }).click()
  await expect(page.getByText(text.nativeTimeout)).toBeVisible()
  await expect(page.locator('body')).not.toContainText('stale hung result')
  await page.reload()
  await expect(page.getByRole('button', { name: text.cameraStart })).toBeEnabled()
  await expect(page.getByText(text.nativeTimeout)).toHaveCount(0)
  await page.evaluate(() => { window.__nativeScanProbe.queue.push([{ format: 'upc_a', rawValue: '001' }]) })
  await page.getByRole('button', { name: text.cameraStart }).click()
  await expect(page.getByRole('list', { name: text.nativeResults })).toContainText('"001"')
})

test('stops on a detector error and requires explicit camera reopening', async ({ page, application }) => {
  await installNativeProbe(page); await provision(page, application); await openScan(page)
  await page.evaluate(() => { window.__nativeScanProbe.fail = true })
  await page.getByRole('button', { name: text.cameraStart }).click()
  await expect(page.getByText(text.nativeError)).toBeVisible()
  expect(await trackState(page, 0)).toBe('ended')
  await expect(page.locator('body')).not.toContainText('private native detector diagnostics')
  await page.evaluate(() => { window.__nativeScanProbe.queue.push([{ format: 'upc_a', rawValue: '001' }]) })
  await page.getByRole('button', { name: text.cameraStart }).click()
  await expect(page.getByRole('list', { name: text.nativeResults })).toContainText('"001"')
  expect(await trackState(page, 1)).toBe('ended')
})

for (const transport of ['storage', 'broadcast'] as const) test(`discards a late detector result after cross-tab logout using ${transport} only`, async ({ page, context, application }) => {
  await page.addInitScript((selected) => {
    if (selected === 'storage') Object.defineProperty(window, 'BroadcastChannel', { configurable: true, value: undefined })
    else window.addEventListener = new Proxy(window.addEventListener, { apply(target, receiver, args: unknown[]) {
      return args[0] === 'storage' ? undefined : Reflect.apply(target, receiver, args)
    } })
  }, transport)
  await installNativeProbe(page); await provision(page, application); await openScan(page)
  await page.evaluate(() => { Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' }); window.__nativeScanProbe.hold = true })
  await page.getByRole('textbox', { name: text.input }).fill('private before logout')
  await page.getByRole('button', { name: text.cameraStart }).click()
  await expect.poll(() => page.evaluate(() => !!window.__nativeScanProbe.finish)).toBe(true)
  const other = await context.newPage()
  try {
    await other.goto('/settings/account')
    await other.getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await other.getByRole('alertdialog').getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await expect(page.getByText(text.sessionClearedHelp)).toBeVisible()
    expect(await trackState(page, 0)).toBe('ended')
    await page.evaluate(() => { window.__nativeScanProbe.finish?.([{ format: 'qr_code', rawValue: 'late private detection' }]) })
    await expect(page.getByRole('list', { name: text.nativeResults })).toHaveCount(0)
    await expect(page.locator('body')).not.toContainText('private before logout')
    await expect(page.locator('body')).not.toContainText('late private detection')
  } finally { await other.close() }
})

test('keeps results and live controls accessible at 320px and 200% text', async ({ page, application }) => {
  await installNativeProbe(page); await provision(page, application)
  await page.setViewportSize({ width: 320, height: 568 }); await openScan(page)
  await page.locator('html').evaluate((element) => { element.style.fontSize = '200%' })
  await page.evaluate(() => { window.__nativeScanProbe.hold = true })
  await page.getByRole('button', { name: text.cameraStart }).click()
  await expect.poll(() => page.evaluate(() => !!window.__nativeScanProbe.finish)).toBe(true)
  for (const colorScheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme }); await expect(page.locator('html')).toHaveAttribute('data-theme', colorScheme)
    expect(await page.evaluate(() => innerWidth)).toBe(320)
    await noOverflow(page)
    expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze()).violations).toEqual([])
  }
  await page.evaluate(() => { window.__nativeScanProbe.finish?.([{ format: 'qr_code', rawValue: 'x'.repeat(513) }, { format: 'upc_a', rawValue: '0'.repeat(200) }]) })
  await expect(page.getByRole('list', { name: text.nativeResults }).getByRole('listitem')).toHaveCount(2)
  for (const colorScheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme }); await expect(page.locator('html')).toHaveAttribute('data-theme', colorScheme)
    expect(await page.evaluate(() => innerWidth)).toBe(320)
    await noOverflow(page)
    const camera = page.getByRole('region', { name: text.cameraTitle })
    for (const button of await camera.getByRole('button').all()) expect(await button.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)
    expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze()).violations).toEqual([])
  }
})
