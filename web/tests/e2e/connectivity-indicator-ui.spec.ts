import AxeBuilder from '@axe-core/playwright'
import { messages } from '../../src/lib/messages.ts'
import { test, expect } from './auth-fixture.ts'
import { leaveDocument, permitBFCache, returnFromBFCache } from './bfcache-helpers.ts'
import { noOverflow, provision, watchDomainWrites, writeContext } from './domain-helpers.ts'

const text = messages.connectivityIndicator
const probePath = '/api/v1/system/connectivity'

test.use({ launchOptions: { channel: 'chromium', ignoreDefaultArgs: ['--disable-back-forward-cache'] }, trace: 'off', screenshot: 'off', video: 'off' })

test('uses a fresh completed session and recovers from an unreachable probe without replaying writes', async ({ page, application }) => {
  const methods: string[] = [], successful: number[] = []
  page.on('request', (request) => { if (new URL(request.url()).pathname === probePath) methods.push(request.method()) })
  page.on('response', (response) => { if (new URL(response.url()).pathname === probePath && response.status() === 200) successful.push(response.status()) })
  await page.goto('/')
  await expect(page.getByLabel(text.label)).toHaveCount(0)
  expect(methods).toEqual([])
  await provision(page, application)
  await page.clock.install()
  const writes = watchDomainWrites(page, ['/api/v1/'])
  await page.goto('/settings')
  const indicator = page.getByLabel(text.label)
  await expect(indicator.getByText(text.phases.reachable)).toBeVisible()
  await expect(indicator.getByText(text.help)).toBeVisible()
  expect(successful).toHaveLength(1)
  const block = (url: URL) => url.pathname === probePath
  await page.route(block, async (route) => { await route.abort('internetdisconnected') })
  await page.clock.fastForward(60_000)
  await expect(indicator.getByText(text.phases.unavailable)).toBeVisible()
  expect(await page.evaluate(() => navigator.onLine)).toBe(true)
  await page.unroute(block)
  let retrying = false
  let release!: () => void
  const held = new Promise<void>((resolve) => { release = resolve })
  await page.route(block, async (route) => { retrying = true; await held; await route.fallback() })
  try {
    await page.clock.fastForward(15_000)
    await expect.poll(() => retrying).toBe(true)
    await expect(indicator.getByText(text.phases.unavailable)).toBeVisible()
    release()
    await expect(indicator.getByText(text.phases.reachable)).toBeVisible()
    expect(successful).toHaveLength(2)
    expect(methods).toEqual(['GET', 'GET', 'GET'])
    expect(writes).toEqual([])
  } finally { release(); await page.unroute(block) }
})

test('shows an expired session without automatically logging out or redirecting', async ({ page, application }) => {
  await provision(page, application)
  await page.clock.install()
  const writes = watchDomainWrites(page, ['/api/v1/'])
  await page.goto('/settings')
  const indicator = page.getByLabel(text.label)
  await expect(indicator.getByText(text.phases.reachable)).toBeVisible()
  const headers = await writeContext(page, application.url)
  expect((await page.request.post('/api/v1/auth/logout', { headers })).status()).toBe(200)
  await page.clock.fastForward(60_000)
  await expect(indicator.getByText(text.phases['session-required'])).toBeVisible()
  await expect(page).toHaveURL(/\/settings$/)
  expect(writes).toEqual([])
  await indicator.getByRole('link', { name: text.account }).click()
  await expect(page).toHaveURL(/\/login$/)
  await expect(page.getByRole('heading', { name: messages.auth.loginTitle })).toBeVisible()
})

for (const transport of ['storage', 'broadcast'] as const) test(`stops the indicator after cross-tab logout using ${transport} alone`, async ({ page, context, application }) => {
  await page.addInitScript((selected) => {
    if (selected === 'storage') Object.defineProperty(window, 'BroadcastChannel', { configurable: true, value: undefined })
    else window.addEventListener = new Proxy(window.addEventListener, { apply(target, receiver, args: unknown[]) {
      return args[0] === 'storage' ? undefined : Reflect.apply(target, receiver, args)
    } })
  }, transport)
  await provision(page, application)
  await page.clock.install()
  let reads = 0
  page.on('request', (request) => { if (new URL(request.url()).pathname === probePath) reads++ })
  await page.goto('/settings')
  await expect(page.getByLabel(text.label).getByText(text.phases.reachable)).toBeVisible()
  const other = await context.newPage()
  try {
    await other.goto('/settings/account')
    await other.getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await other.getByRole('alertdialog').getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await expect(page.getByLabel(text.label)).toHaveCount(0)
    const before = reads
    await page.evaluate(() => window.dispatchEvent(new Event('online')))
    await page.clock.fastForward(300_000)
    expect(reads).toBe(before)
  } finally { await other.close() }
})

test('requires a fresh session check after an actual BFCache restoration', async ({ page, application }) => {
  await provision(page, application)
  const diagnostics = await permitBFCache(page, application.url)
  let reads = 0
  const authReplies: number[] = []
  page.on('request', (request) => { if (new URL(request.url()).pathname === probePath) reads++ })
  page.on('response', (response) => { if (new URL(response.url()).pathname === '/api/v1/auth/me') authReplies.push(response.status()) })
  await page.goto('/settings')
  const indicator = page.getByLabel(text.label)
  await expect(indicator.getByText(text.phases.reachable)).toBeVisible()
  const before = reads
  await page.evaluate((label) => {
    window.addEventListener('pagehide', () => {
      ;(window as Window & { __hiddenIndicator?: string }).__hiddenIndicator = document.querySelector(`[aria-label="${label}"]`)?.textContent ?? ''
    })
  }, text.label)
  let waiting = false
  let release!: () => void
  const held = new Promise<void>((resolve) => { release = resolve })
  const auth = (url: URL) => url.pathname === '/api/v1/auth/me'
  await page.route(auth, async (route) => { waiting = true; await held; await route.fallback() })
  try {
    await leaveDocument(page, false)
    await returnFromBFCache(page, diagnostics)
    expect(await page.evaluate(() => (window as Window & { __hiddenIndicator?: string }).__hiddenIndicator)).toContain(text.phases.stopped)
    await expect.poll(() => waiting).toBe(true)
    await expect(indicator.getByText(text.phases.stopped)).toBeVisible()
    expect(reads).toBe(before)
    release()
    await expect(indicator.getByText(text.phases.reachable)).toBeVisible()
    expect(reads).toBeGreaterThan(before)
    expect(authReplies.filter((status) => status === 200).length).toBeGreaterThanOrEqual(2)
  } finally { release(); await page.unroute(auth) }
})

test('leaves a restored public page stopped until the account is checked again', async ({ page, application }) => {
  await provision(page, application)
  const diagnostics = await permitBFCache(page, application.url)
  let reads = 0
  page.on('request', (request) => { if (new URL(request.url()).pathname === probePath) reads++ })
  await page.goto('/settings')
  const indicator = page.getByLabel(text.label)
  await expect(indicator.getByText(text.phases.reachable)).toBeVisible()
  await page.getByRole('link', { name: messages.app.home }).click()
  await expect(page.getByRole('heading', { name: messages.connection.title })).toBeVisible()
  const before = reads
  await leaveDocument(page, false)
  await returnFromBFCache(page, diagnostics)
  await expect(indicator.getByText(text.phases.stopped)).toBeVisible()
  expect(reads).toBe(before)
  await indicator.getByRole('link', { name: text.account }).click()
  await expect(indicator.getByText(text.phases.reachable)).toBeVisible()
  expect(reads).toBeGreaterThan(before)
})

test('keeps the indicator accessible at 320px and 200% text in both themes', async ({ page, application }) => {
  await provision(page, application)
  await page.setViewportSize({ width: 320, height: 568 })
  await page.goto('/settings')
  const indicator = page.getByLabel(text.label)
  await expect(indicator.getByText(text.phases.reachable)).toBeVisible()
  await page.locator('html').evaluate((element) => { element.style.fontSize = '200%' })
  for (const colorScheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme })
    await expect(page.locator('html')).toHaveAttribute('data-theme', colorScheme)
    expect(await page.evaluate(() => innerWidth)).toBe(320)
    await noOverflow(page)
    expect(await indicator.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)
    expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze()).violations).toEqual([])
  }
  await page.evaluate(() => window.dispatchEvent(new Event('pagehide')))
  const account = indicator.getByRole('link', { name: text.account })
  await expect(account).toBeVisible()
  await account.focus()
  await expect(account).toBeFocused()
  for (const colorScheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme })
    await expect(page.locator('html')).toHaveAttribute('data-theme', colorScheme)
    expect(await page.evaluate(() => innerWidth)).toBe(320)
    await noOverflow(page)
    expect(await account.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)
    expect((await account.boundingBox())?.height).toBeGreaterThanOrEqual(44)
    expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze()).violations).toEqual([])
  }
})
