import AxeBuilder from '@axe-core/playwright'
import type { Page } from '@playwright/test'
import { messages } from '../../src/lib/messages.ts'
import { test, expect } from './auth-fixture.ts'
import { noOverflow, provision, watchDomainWrites, writeContext } from './domain-helpers.ts'

test.use({
  launchOptions: { channel: 'chromium', ignoreDefaultArgs: ['--disable-back-forward-cache'] },
  trace: 'off', screenshot: 'off', video: 'off',
})

const scan = messages.scan, catalog = messages.catalog, owned = messages.owned
const raw = '012345678905'

async function permitBFCache(page: Page, origin: string) {
  const original = await page.request.get('/scan')
  expect(original.headers()['cache-control']).toContain('no-store')
  let privateResponses = 0
  // Keep production headers unchanged. Override only this browser's responses
  // to exercise browsers that can restore private no-store pages from BFCache.
  await page.route((url) => url.origin === origin, async (route) => {
    if (!['document', 'fetch', 'xhr'].includes(route.request().resourceType())) { await route.continue(); return }
    const response = await route.fetch(), headers = { ...response.headers() }
    if (headers['cache-control']?.includes('no-store')) privateResponses++
    headers['cache-control'] = 'private, max-age=0'
    await route.fulfill({ response, headers })
  })
  const cdp = await page.context().newCDPSession(page)
  await cdp.send('Page.enable')
  const notUsed: unknown[] = []
  cdp.on('Page.backForwardCacheNotUsed', (event) => notUsed.push(event))
  return { privateResponses: () => privateResponses, notUsed }
}

async function openHandoff(page: Page) {
  await page.goto('/scan')
  await page.getByRole('textbox', { name: scan.input }).fill(raw)
  await page.getByRole('button', { name: scan.search }).click()
  await expect(page.getByRole('heading', { name: scan.empty })).toBeVisible()
  await page.getByRole('button', { name: scan.create }).click()
  await page.getByRole('textbox', { name: new RegExp(`^${catalog.fields.name}`) }).fill('Unfinished private Item')
  await expect(page.getByText(scan.handoffHelp).locator('..')).toContainText(raw)
}

async function leaveDocument(page: Page, dirty: boolean) {
  await page.evaluate(() => {
    const probe = window as Window & { __homeboxRestored?: boolean }
    probe.__homeboxRestored = false
    window.addEventListener('pageshow', (event) => { probe.__homeboxRestored = event.persisted })
  })
  let confirmedLeave = false
  if (dirty) page.once('dialog', async (dialog) => { confirmedLeave = dialog.type() === 'beforeunload'; await dialog.accept() })
  await page.goto('/api/v1/health/live')
  if (dirty) expect(confirmedLeave).toBe(true)
}

async function returnFromBFCache(page: Page, diagnostics: { privateResponses: () => number; notUsed: unknown[] }) {
  await page.goBack({ waitUntil: 'commit' })
  const probe = await page.evaluate(() => ({
    restored: (window as Window & { __homeboxRestored?: boolean }).__homeboxRestored === true,
    reasons: (performance.getEntriesByType('navigation')[0] as PerformanceNavigationTiming & { notRestoredReasons?: unknown })?.notRestoredReasons,
  }))
  expect(probe.restored, JSON.stringify({ reasons: probe.reasons, notUsed: diagnostics.notUsed })).toBe(true)
  expect(diagnostics.privateResponses()).toBeGreaterThan(0)
}

test('clears an unfinished Item handoff and rechecks the session after actual BFCache restoration', async ({ page, application }) => {
  await provision(page, application)
  const diagnostics = await permitBFCache(page, application.url), writes = watchDomainWrites(page, ['/api/v1/items', '/api/v1/barcodes'])
  const authReads: number[] = []
  page.on('response', (response) => { if (new URL(response.url()).pathname === '/api/v1/auth/me') authReads.push(response.status()) })
  await openHandoff(page)
  const before = authReads.length
  await leaveDocument(page, true)
  await returnFromBFCache(page, diagnostics)
  await expect(page.getByText(scan.restoreHelp)).toBeVisible()
  await expect(page.getByText(scan.restoreReady)).toBeVisible()
  expect(authReads.length).toBeGreaterThan(before)
  await expect(page.locator('body')).not.toContainText(raw)
  await expect(page.locator('body')).not.toContainText('Unfinished private Item')
  await page.getByRole('button', { name: scan.restoreContinue }).click()
  await expect(page.getByRole('main')).toBeFocused()
  await expect(page.getByText(scan.handoffLost)).toBeVisible()
  await expect(page.getByRole('textbox', { name: new RegExp(`^${catalog.fields.name}`) })).toHaveValue('')
  expect(writes).toEqual([])
})

test('keeps the reviewed draft when the user cancels leaving the document', async ({ page, application }) => {
  await provision(page, application)
  await permitBFCache(page, application.url)
  const writes = watchDomainWrites(page, ['/api/v1/items', '/api/v1/barcodes'])
  await openHandoff(page)
  await page.getByRole('button', { name: catalog.review }).click()
  const review = page.getByRole('alertdialog')
  await expect(review).toContainText(raw)
  let rejectedLeave = false
  page.once('dialog', async (dialog) => { rejectedLeave = dialog.type() === 'beforeunload'; await dialog.dismiss() })
  await expect(page.goto('/api/v1/health/live', { timeout: 5_000 })).rejects.toThrow()
  expect(rejectedLeave).toBe(true)
  await expect(page).toHaveURL(/\/items\/new$/)
  await expect(review).toContainText(raw)
  expect(writes).toEqual([])
})

test('does not reveal the old handoff when the real session expires while the page is cached', async ({ page, application }) => {
  await provision(page, application)
  const diagnostics = await permitBFCache(page, application.url), writes = watchDomainWrites(page, ['/api/v1/items', '/api/v1/barcodes'])
  await openHandoff(page)
  await leaveDocument(page, true)
  expect((await page.request.post('/api/v1/auth/logout', { headers: await writeContext(page, application.url) })).status()).toBe(200)
  await returnFromBFCache(page, diagnostics)
  await expect(page.getByText(messages.auth.expired)).toBeVisible()
  await expect(page.locator('body')).not.toContainText(raw)
  await expect(page.getByRole('button', { name: scan.restoreContinue })).toHaveCount(0)
  await page.getByRole('link', { name: scan.signIn }).click()
  await expect(page).toHaveURL(/\/login$/)
  await expect(page.getByText(messages.auth.scanHandoffLost)).toBeVisible()
  const state = JSON.stringify(await page.evaluate(() => history.state))
  expect(state).toContain('/items/new')
  expect(state).not.toContain(raw)
  expect(writes).toEqual([])
})

test('invalidates a held restoration-session reply after a real cross-tab logout', async ({ page, context, application }) => {
  await provision(page, application)
  const diagnostics = await permitBFCache(page, application.url), writes = watchDomainWrites(page, ['/api/v1/items', '/api/v1/barcodes'])
  await openHandoff(page)
  await leaveDocument(page, true)
  let release!: () => void, requested!: () => void, delivered!: () => void
  const held = new Promise<void>((resolve) => { release = resolve })
  const seen = new Promise<void>((resolve) => { requested = resolve })
  const finished = new Promise<void>((resolve) => { delivered = resolve })
  let holdFirst = true
  await page.route((url) => url.pathname === '/api/v1/auth/me', async (route) => {
    if (!holdFirst) { await route.fallback(); return }
    holdFirst = false
    const response = await route.fetch()
    expect(response.status()).toBe(200)
    requested()
    await held
    try { await route.fulfill({ response }) } catch { /* The superseded request can already be aborted. */ }
    delivered()
  })
  const other = await context.newPage()
  try {
    await returnFromBFCache(page, diagnostics)
    await seen
    await expect(page.getByText(messages.auth.checking)).toBeVisible()
    await expect(page.getByRole('button', { name: scan.restoreContinue })).toHaveCount(0)
    await other.goto('/settings/account')
    await other.getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await other.getByRole('alertdialog').getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await expect(page.getByText(messages.auth.expired)).toBeVisible()
    release()
    await finished
    await expect(page.getByText(messages.auth.expired)).toBeVisible()
    await expect(page.getByRole('button', { name: scan.restoreContinue })).toHaveCount(0)
    await expect(page.locator('body')).not.toContainText(raw)
    expect(writes).toEqual([])
  } finally { release(); await other.close() }
})

test('discards an open barcode review before actual BFCache restoration', async ({ page, application }) => {
  await provision(page, application)
  const diagnostics = await permitBFCache(page, application.url), writes = watchDomainWrites(page, ['/api/v1/items', '/api/v1/barcodes'])
  await openHandoff(page)
  await page.getByRole('button', { name: catalog.review }).click()
  await page.getByRole('alertdialog').getByRole('button', { name: catalog.confirmCreate }).click()
  await expect(page).toHaveURL(/\/items\/[^/]+\/barcodes\/new$/)
  await page.getByRole('combobox', { name: new RegExp(`^${owned.fields.symbology}`) }).selectOption('UPC_A')
  await page.getByRole('button', { name: owned.review }).click()
  await expect(page.getByRole('alertdialog')).toContainText(raw)
  await leaveDocument(page, true)
  await returnFromBFCache(page, diagnostics)
  await expect(page.getByText(scan.restoreHelp)).toBeVisible()
  await expect(page.getByRole('alertdialog')).toHaveCount(0)
  await expect(page.locator('body')).not.toContainText(raw)
  await expect(page.getByText(scan.restoreReady)).toBeVisible()
  await page.getByRole('button', { name: scan.restoreContinue }).click()
  await expect(page.getByText(scan.barcodeDraftLost)).toBeVisible()
  await expect(page.getByRole('textbox', { name: new RegExp(`^${owned.fields.raw_value}`) })).toHaveValue('')
  expect(writes.filter((request) => request.method === 'POST')).toHaveLength(1)
})

test('never replays a committed barcode after its reply is held across page suspension', async ({ page, application }) => {
  await provision(page, application)
  const diagnostics = await permitBFCache(page, application.url), writes = watchDomainWrites(page, ['/api/v1/items', '/api/v1/barcodes'])
  await openHandoff(page)
  await page.getByRole('button', { name: catalog.review }).click()
  await page.getByRole('alertdialog').getByRole('button', { name: catalog.confirmCreate }).click()
  await expect(page).toHaveURL(/\/items\/[^/]+\/barcodes\/new$/)
  const itemID = new URL(page.url()).pathname.split('/')[2]!
  await page.getByRole('combobox', { name: new RegExp(`^${owned.fields.symbology}`) }).selectOption('UPC_A')
  let release!: () => void, committed!: () => void
  const held = new Promise<void>((resolve) => { release = resolve })
  const received = new Promise<void>((resolve) => { committed = resolve })
  await page.route((url) => url.pathname === `/api/v1/items/${itemID}/barcodes`, async (route) => {
    if (route.request().method() !== 'POST') { await route.fallback(); return }
    const response = await route.fetch()
    expect(response.status()).toBe(200)
    committed()
    await held
    try { await route.fulfill({ response }) } catch { /* The suspended page can already have aborted the request. */ }
  })
  try {
    await page.getByRole('button', { name: owned.review }).click()
    await page.getByRole('alertdialog').getByRole('button', { name: owned.confirmCreate.barcodes }).click()
    await received
    await leaveDocument(page, true)
    await returnFromBFCache(page, diagnostics)
    await expect(page.getByText(scan.restoreHelp)).toBeVisible()
    await expect(page.getByRole('alertdialog')).toHaveCount(0)
    await expect(page.locator('body')).not.toContainText(raw)
    release()
    await expect(page.getByText(scan.restoreReady)).toBeVisible()
    expect(writes.filter((request) => request.method === 'POST')).toHaveLength(2)
    const current = await page.request.get(`/api/v1/items/${itemID}/barcodes`)
    expect(current.status()).toBe(200)
    expect((await current.json()).data).toEqual([expect.objectContaining({ raw_value: raw, symbology: 'UPC_A' })])
    await page.getByRole('button', { name: scan.restoreContinue }).click()
    await expect(page.getByText(scan.barcodeDraftLost)).toBeVisible()
    expect(writes.filter((request) => request.method === 'POST')).toHaveLength(2)
  } finally { release() }
})

test('keeps the restoration gate accessible at 320px and 200% text', async ({ page, application }) => {
  await provision(page, application)
  const diagnostics = await permitBFCache(page, application.url)
  await page.setViewportSize({ width: 320, height: 568 })
  await openHandoff(page)
  await page.locator('html').evaluate((element) => { element.style.fontSize = '200%' })
  await leaveDocument(page, true)
  await returnFromBFCache(page, diagnostics)
  await expect(page.getByText(scan.restoreReady)).toBeVisible()
  await expect(page.getByRole('heading', { name: scan.restoreTitle })).toBeFocused()
  for (const colorScheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme }); await expect(page.locator('html')).toHaveAttribute('data-theme', colorScheme)
    expect(await page.evaluate(() => innerWidth)).toBe(320)
    await noOverflow(page)
    expect(await page.getByRole('button', { name: scan.restoreContinue }).evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)
    expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze()).violations).toEqual([])
  }
})

test('clears a manual lookup input on actual BFCache restoration', async ({ page, application }) => {
  await provision(page, application)
  const diagnostics = await permitBFCache(page, application.url), writes = watchDomainWrites(page, ['/api/v1/items', '/api/v1/barcodes'])
  await page.goto('/scan')
  await page.getByRole('textbox', { name: scan.input }).fill(raw)
  await leaveDocument(page, false)
  await returnFromBFCache(page, diagnostics)
  await expect(page.getByText(scan.restoreReady)).toBeVisible()
  await page.getByRole('button', { name: scan.restoreContinue }).click()
  await expect(page.getByRole('textbox', { name: scan.input })).toHaveValue('')
  expect(writes).toEqual([])
})
