import { expect, test } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { messages } from '../../src/lib/messages.ts'

const text = messages.connection

for (const colorScheme of ['light', 'dark'] as const) {
  test(`real server, responsive layout and accessibility in ${colorScheme}`, async ({ page }, info) => {
    const errors: string[] = []
    const origins = new Set<string>()
    page.on('pageerror', (error) => errors.push(error.message))
    page.on('request', (request) => origins.add(new URL(request.url()).origin))
    await page.emulateMedia({ colorScheme })
    const responsePromise = page.waitForResponse('**/api/v1/health/live')
    await page.goto('/')
    const response = await responsePromise
    expect(response.status()).toBe(200)
    expect(response.headers()['cache-control']).toBe('no-store')
    expect(await response.json()).toEqual({ status: 'ok' })
    await expect(page.getByText(text.ok, { exact: true })).toBeVisible()
    await expect(page.locator('html')).toHaveAttribute('lang', 'zh-Hant')
    await expect(page.locator('html')).toHaveAttribute('data-theme', colorScheme)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
    for (const button of await page.getByRole('button').all()) {
      const box = await button.boundingBox()
      expect(box?.width).toBeGreaterThanOrEqual(44)
      expect(box?.height).toBeGreaterThanOrEqual(44)
    }
    const accessibility = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze()
    expect(accessibility.violations).toEqual([])
    expect(errors).toEqual([])
    expect([...origins]).toEqual([new URL(page.url()).origin])
    await page.screenshot({ path: info.outputPath(`connection-${colorScheme}.png`), fullPage: true })
  })
}

test('keyboard navigation, menu touch targets and persisted theme', async ({ page }) => {
  await page.goto('/')
  await expect(page.getByText(text.ok, { exact: true })).toBeVisible()
  await page.keyboard.press('Tab')
  await expect(page.getByRole('link', { name: messages.app.skip })).toBeFocused()
  await page.keyboard.press('Enter')
  await expect(page.locator('#main')).toBeFocused()
  const trigger = page.getByRole('button', { name: messages.theme.label })
  await trigger.focus()
  await page.keyboard.press('Enter')
  const system = page.getByRole('menuitemradio', { name: messages.theme.system })
  await expect(system).toBeFocused()
  for (const item of await page.getByRole('menuitemradio').all()) {
    expect((await item.boundingBox())?.height).toBeGreaterThanOrEqual(44)
  }
  expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa']).analyze()).violations).toEqual([])
  await page.keyboard.press('ArrowDown')
  await page.keyboard.press('ArrowDown')
  await page.keyboard.press('Enter')
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
  await expect(trigger).toBeFocused()
  await page.reload()
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
  await trigger.click()
  await expect(page.getByRole('menuitemradio', { name: messages.theme.dark })).toHaveAttribute('aria-checked', 'true')
  await page.keyboard.press('Escape')
  await expect(trigger).toBeFocused()
})

test('follows system appearance until a manual preference is selected', async ({ page }) => {
  await page.goto('/')
  await page.emulateMedia({ colorScheme: 'dark' })
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
  await page.emulateMedia({ colorScheme: 'light' })
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light')
  await page.getByRole('button', { name: messages.theme.label }).click()
  await page.getByRole('menuitemradio', { name: messages.theme.dark }).click()
  await page.emulateMedia({ colorScheme: 'light' })
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
})

test('offline recheck fails visibly and reconnect only refreshes the read', async ({ page, context }) => {
  const methods: string[] = []
  page.on('request', (request) => { if (request.url().includes('/api/')) methods.push(request.method()) })
  await page.goto('/')
  await expect(page.getByText(text.ok, { exact: true })).toBeVisible()
  await context.setOffline(true)
  await page.getByRole('button', { name: text.retry }).click()
  await expect(page.getByText(text.error, { exact: true })).toBeVisible()
  await expect(page.getByText(text.ok, { exact: true })).toHaveCount(0)
  await context.setOffline(false)
  await expect(page.getByText(text.ok, { exact: true })).toBeVisible()
  expect(methods.length).toBeGreaterThanOrEqual(3)
  expect(methods.every((method) => method === 'GET')).toBe(true)
})

test('slow response reserves space and respects reduced motion', async ({ page }) => {
  let release: () => void = () => {}
  const gate = new Promise<void>((resolve) => { release = resolve })
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await page.route('**/api/v1/health/live', async (route) => { await gate; await route.continue() })
  try {
    await page.goto('/')
    await expect(page.getByText(text.pending, { exact: true })).toBeVisible()
    await expect(page.getByRole('button', { name: text.checking })).toBeDisabled()
    expect(await page.getByRole('status').locator('svg').evaluate((element) => getComputedStyle(element).animationName)).toBe('none')
  } finally { release() }
  await expect(page.getByText(text.ok, { exact: true })).toBeVisible()
})

test('malformed and degraded responses remain recoverable', async ({ page }) => {
  await page.route('**/api/v1/health/live', (route) => route.fulfill({ json: { status: 'unsupported' } }))
  await page.goto('/')
  await expect(page.getByText(text.error, { exact: true })).toBeVisible()
  await page.unroute('**/api/v1/health/live')
  await page.route('**/api/v1/health/live', (route) => route.fulfill({ json: { status: 'degraded' } }))
  await page.getByRole('button', { name: text.retry }).click()
  await expect(page.getByText(text.degraded, { exact: true })).toBeVisible()
  await page.unroute('**/api/v1/health/live')
  await page.getByRole('button', { name: text.retry }).click()
  await expect(page.getByText(text.ok, { exact: true })).toBeVisible()
})

test('deep-link fallback provides a real recovery link', async ({ page }) => {
  await page.goto('/missing/page')
  await expect(page.getByRole('heading', { name: messages.app.notFound })).toBeVisible()
  await page.getByRole('link', { name: messages.app.returnHome }).click()
  await expect(page.getByText(text.ok, { exact: true })).toBeVisible()
  await expect(page).toHaveURL(/\/$/)
})
