import AxeBuilder from '@axe-core/playwright'
import { messages } from '../../src/lib/messages.ts'
import { test, expect } from './auth-fixture.ts'
import { noOverflow, provision, watchDomainWrites } from './domain-helpers.ts'

const text = messages.serverStorage

test.use({ trace: 'off', screenshot: 'off', video: 'off' })

test('reads server storage separately from browser estimates and hides stale observations', async ({ page, context, application }) => {
  await provision(page, application)
  const methods: string[] = []
  page.on('request', (request) => {
    if (new URL(request.url()).pathname === '/api/v1/system/storage') methods.push(request.method())
  })
  const writes = watchDomainWrites(page, ['/api/v1/'])
  await page.goto('/settings')
  await page.getByRole('list').getByRole('link', { name: new RegExp(messages.settings.storage) }).click()
  await expect(page.getByText(text.filesystemHelp)).toBeVisible()
  await expect(page.getByText(text.unknown, { exact: true })).toHaveCount(2)
  await expect(page.getByText(text.readOnlyHelp)).toBeVisible()
  expect(new URL(page.url()).pathname).toBe('/settings/storage')
  const actual = await page.request.get('/api/v1/system/storage', { headers: { Origin: application.url } })
  expect(actual.status()).toBe(200)
  expect(actual.headers()['cache-control']).toBe('no-store')
  expect(actual.headers().etag).toBeUndefined()
  const storage = await actual.json()
  const path = (url: URL) => url.pathname === '/api/v1/system/storage'
  const refresh = page.getByRole('button', { name: text.refresh })
  let release!: () => void
  const held = new Promise<void>((resolve) => { release = resolve })
  await page.route(path, async (route) => { await held; await route.continue() })
  try {
    await refresh.click()
    await expect(refresh).toHaveAttribute('aria-disabled', 'true')
    await expect(refresh).toBeFocused()
    await expect(page.getByText(text.filesystemHelp)).toHaveCount(0)
  } finally { release() }
  await expect(page.getByText(text.filesystemHelp)).toBeVisible()
  await expect(refresh).toHaveAttribute('aria-disabled', 'false')
  await expect(refresh).toBeFocused()
  await page.unroute(path)

  await page.route(path, (route) => route.fulfill({ status: 200, headers: { 'Cache-Control': 'no-store' }, json: { ...storage, media_bytes: 0, backup_bytes: 3072 } }))
  await refresh.click()
  await expect(page.getByText(text.media).locator('..')).toContainText('0 B')
  await expect(page.getByText(text.backup).locator('..')).toContainText('3 KiB')
  await expect(page.getByText(text.unknown, { exact: true })).toHaveCount(0)
  await page.unroute(path)
  await page.route(path, (route) => route.fulfill({ status: 200, headers: { 'Cache-Control': 'no-store' }, json: { private: 'should not display' } }))
  await refresh.click()
  await expect(page.getByRole('alert')).toContainText(text.readError)
  await expect(page.getByText(text.filesystemHelp)).toHaveCount(0)
  await expect(page.locator('body')).not.toContainText('should not display')
  await page.unroute(path)
  await refresh.click()
  await expect(page.getByText(text.filesystemHelp)).toBeVisible()
  await context.setOffline(true)
  await expect(page.getByText(text.offlineHelp)).toBeVisible()
  await expect(page.getByText(text.filesystemHelp)).toHaveCount(0)
  await expect(refresh).toHaveAttribute('aria-disabled', 'true')
  await context.setOffline(false)
  await expect(page.getByText(text.filesystemHelp)).toBeVisible()
  expect(methods.length).toBeGreaterThanOrEqual(5)
  expect(methods.every((method) => method === 'GET')).toBe(true)
  expect(writes).toEqual([])
})

for (const transport of ['storage', 'broadcast'] as const) test(`clears server storage after logout using ${transport} only`, async ({ page, context, application }) => {
  await page.addInitScript((selected) => {
    if (selected === 'storage') Object.defineProperty(window, 'BroadcastChannel', { configurable: true, value: undefined })
    else window.addEventListener = new Proxy(window.addEventListener, { apply(target, receiver, args: unknown[]) {
      return args[0] === 'storage' ? undefined : Reflect.apply(target, receiver, args)
    } })
  }, transport)
  await provision(page, application)
  await page.goto('/settings/storage')
  await expect(page.getByText(text.filesystemHelp)).toBeVisible()
  const other = await context.newPage()
  try {
    await other.goto('/settings/account')
    await other.getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await other.getByRole('alertdialog').getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await expect(page.getByText(text.sessionCleared)).toBeVisible()
    await expect(page.getByText(text.filesystemHelp)).toHaveCount(0)
  } finally { await other.close() }
})

test('keeps exact counts and actions accessible at 320px and 200% text', async ({ page, application }, testInfo) => {
  await provision(page, application)
  const actual = await page.request.get('/api/v1/system/storage', { headers: { Origin: application.url } })
  expect(actual.status()).toBe(200)
  const storage = await actual.json()
  await page.route((url) => url.pathname === '/api/v1/system/storage', (route) => route.fulfill({ status: 200, headers: { 'Cache-Control': 'no-store' }, json: {
    ...storage, database_bytes: Number.MAX_SAFE_INTEGER, free_bytes: Number.MAX_SAFE_INTEGER,
    database_filesystem_total_bytes: Number.MAX_SAFE_INTEGER, media_bytes: null, backup_bytes: 0, measured_at: '2026-11-27T12:00:00.123456789+08:00',
  } }))
  await page.setViewportSize({ width: 320, height: 568 })
  await page.goto('/settings/storage')
  await expect(page.getByText(text.filesystemHelp)).toBeVisible()
  await page.locator('html').evaluate((element) => { element.style.fontSize = '200%' })
  const heading = page.getByRole('heading', { name: text.title })
  const lines = await heading.evaluate((element) => Math.round(element.getBoundingClientRect().height / Number.parseFloat(getComputedStyle(element).lineHeight)))
  expect(lines).toBeLessThanOrEqual(2)
  for (const name of [text.filesystem, text.otherScopes]) {
    const scopeHeading = page.getByRole('heading', { name })
    const scopeLines = await scopeHeading.evaluate((element) => Math.round(element.getBoundingClientRect().height / Number.parseFloat(getComputedStyle(element).lineHeight)))
    expect(scopeLines).toBeLessThanOrEqual(2)
  }
  await expect(page.getByText(text.database).locator('..')).toContainText('9,007,199,254,740,991')
  const refresh = page.getByRole('button', { name: text.refresh })
  await refresh.focus()
  await expect(refresh).toBeFocused()
  for (const colorScheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme }); await expect(page.locator('html')).toHaveAttribute('data-theme', colorScheme)
    expect(await page.evaluate(() => innerWidth)).toBe(320)
    await noOverflow(page)
    for (const element of await page.getByRole('main').locator('button, a').all()) {
      expect(await element.evaluate((value) => value.scrollWidth <= value.clientWidth)).toBe(true)
      expect((await element.boundingBox())?.height).toBeGreaterThanOrEqual(44)
    }
    expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze()).violations).toEqual([])
  }
  await page.getByRole('main').screenshot({ path: testInfo.outputPath('server-storage-320-dark.png') })
})
