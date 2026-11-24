import AxeBuilder from '@axe-core/playwright'
import type { Page } from '@playwright/test'
import { messages } from '../../src/lib/messages.ts'
import { test, expect } from './auth-fixture.ts'
import { noOverflow, provision, watchDomainWrites, writeContext } from './domain-helpers.ts'

const text = messages.locations

test.use({ trace: 'off', screenshot: 'off', video: 'off' })

async function post(page: Page, origin: string, path: string, data: object, revision?: number) {
  const response = await page.request.post(path, { headers: await writeContext(page, origin, revision), data })
  expect(response.status(), path).toBe(200)
  return response.json() as Promise<{ id: string; revision: number }>
}

async function seed(page: Page, origin: string) {
  const root = await post(page, origin, '/api/v1/locations', { name: '統計房間', code: 'SUMMARY-ROOT' })
  const child = await post(page, origin, '/api/v1/locations', { name: '統計收納箱', code: 'SUMMARY-CHILD', parent_id: root.id }, root.revision)
  const mass = await post(page, origin, '/api/v1/items', { name: '重量物品', tracking_mode: 'quantity', unit_code: 'kg', base_unit: 'g', scale_factor: 1000 })
  await post(page, origin, '/api/v1/stock/receive', { item_id: mass.id, target_location_id: root.id, quantity_base: 400 }, mass.revision)
  await post(page, origin, '/api/v1/stock/receive', { item_id: mass.id, target_location_id: child.id, quantity_base: 1100 }, mass.revision + 1)
  const liquid = await post(page, origin, '/api/v1/items', { name: '液體物品', tracking_mode: 'quantity', unit_code: 'l', base_unit: 'ml', scale_factor: 1000 })
  await post(page, origin, '/api/v1/stock/receive', { item_id: liquid.id, target_location_id: child.id, quantity_base: 1200 }, liquid.revision)
  const owner = await post(page, origin, '/api/v1/items', { name: '個體物品' })
  await post(page, origin, `/api/v1/items/${owner.id}/units`, { location_id: root.id }, owner.revision)
  const disposed = await post(page, origin, `/api/v1/items/${owner.id}/units`, { location_id: child.id }, owner.revision + 1)
  await post(page, origin, `/api/v1/units/${disposed.id}/status`, { status: 'disposed', disposal_kind: 'recycled' }, disposed.revision)
  return { root, child, mass }
}

const panel = (page: Page) => page.getByRole('region', { name: text.summaryTitle, exact: true })
const amount = (page: Page, label: string) => panel(page).getByText(label, { exact: true }).locator('..').locator('dd')
const stock = (page: Page, label: string) => panel(page).getByRole('region', { name: label, exact: true })

test('shows actual recorded holdings and refreshes after stock and subtree changes', async ({ page, application }) => {
  await provision(page, application)
  const { root, child, mass } = await seed(page, application.url)
  const writes = watchDomainWrites(page, ['/api/v1/locations', '/api/v1/stock', '/api/v1/units'])
  let reads = 0
  page.on('request', (request) => { if (new URL(request.url()).pathname === `/api/v1/locations/${root.id}/summary`) reads++ })
  await page.goto(`/locations/${root.id}`)
  await expect(amount(page, text.summaryDirectUnits)).toHaveText('1')
  await expect(amount(page, text.summarySubtreeUnits)).toHaveText('1')
  await expect(amount(page, text.summaryDistinctItems)).toHaveText('3')
  await expect(stock(page, text.summaryDirectStock)).toContainText('g400')
  await expect(stock(page, text.summarySubtreeStock)).toContainText('g1500')
  await expect(stock(page, text.summarySubtreeStock)).toContainText('ml1200')
  await expect(panel(page)).toContainText('不代表實際所在位置')

  await post(page, application.url, '/api/v1/stock/transfer', { item_id: mass.id, source_location_id: child.id, target_location_id: root.id, quantity_base: 300 }, mass.revision + 2)
  expect(reads).toBe(1)
  await panel(page).getByRole('button', { name: text.summaryRefresh }).click()
  await expect(stock(page, text.summaryDirectStock)).toContainText('g700')
  await expect(stock(page, text.summarySubtreeStock)).toContainText('g1500')

  const current = await page.request.get(`/api/v1/locations/${child.id}`)
  expect(current.status()).toBe(200)
  await post(page, application.url, `/api/v1/locations/${child.id}/move`, { target_parent_id: null }, (await current.json()).revision)
  await panel(page).getByRole('button', { name: text.summaryRefresh }).click()
  await expect(amount(page, text.summaryDistinctItems)).toHaveText('2')
  await expect(stock(page, text.summarySubtreeStock)).toContainText('g700')
  await expect(stock(page, text.summarySubtreeStock)).not.toContainText('ml')
  expect(writes).toEqual([])
})

test('withholds a summary read across a real concurrent change until explicit revalidation', async ({ page, application }) => {
  await provision(page, application)
  const root = await post(page, application.url, '/api/v1/locations', { name: '變動中的位置', code: 'SUMMARY-RACE' })
  const item = await post(page, application.url, '/api/v1/items', { name: '新增的庫存', tracking_mode: 'quantity' })
  let started!: () => void, release!: () => void
  const requested = new Promise<void>((resolve) => { started = resolve })
  const gate = new Promise<void>((resolve) => { release = resolve })
  let first = true
  await page.route((url) => url.pathname === `/api/v1/locations/${root.id}/summary`, async (route) => {
    if (first) { first = false; started(); await gate }
    await route.continue()
  })
  try {
    await page.goto(`/locations/${root.id}`)
    await requested
    await expect(page.getByRole('heading', { level: 1, name: '變動中的位置' })).toBeVisible()
    await post(page, application.url, '/api/v1/stock/receive', { item_id: item.id, target_location_id: root.id, quantity_base: 17 }, item.revision)
    release()
    await expect(panel(page).getByText(text.summaryChanged)).toBeVisible()
    await expect(panel(page).getByText(text.summaryDirectUnits, { exact: true })).toHaveCount(0)
    await panel(page).getByRole('button', { name: text.summaryRefresh }).click()
    await expect(stock(page, text.summaryDirectStock)).toContainText('pcs17')
    await expect(amount(page, text.summaryDistinctItems)).toHaveText('1')
  } finally { release() }
})

for (const transport of ['storage', 'broadcast'] as const) test(`discards a late summary after real cross-tab logout with ${transport} only`, async ({ page, context, application }) => {
  await page.addInitScript((selected) => {
    if (selected === 'storage') Object.defineProperty(window, 'BroadcastChannel', { configurable: true, value: undefined })
    else window.addEventListener = new Proxy(window.addEventListener, { apply(target, receiver, args: unknown[]) {
      return args[0] === 'storage' ? undefined : Reflect.apply(target, receiver, args)
    } })
  }, transport)
  await provision(page, application)
  const root = await post(page, application.url, '/api/v1/locations', { name: '安全位置', code: 'SUMMARY-LOGOUT' })
  await page.goto(`/locations/${root.id}`)
  await expect(amount(page, text.summaryDirectUnits)).toHaveText('0')
  let started!: () => void, release!: () => void
  const requested = new Promise<void>((resolve) => { started = resolve })
  const gate = new Promise<void>((resolve) => { release = resolve })
  await page.route((url) => url.pathname === `/api/v1/locations/${root.id}/summary`, async (route) => {
    const response = await route.fetch()
    started(); await gate
    await route.fulfill({ response }).catch(() => {})
  })
  const other = await context.newPage()
  try {
    await panel(page).getByRole('button', { name: text.summaryRefresh }).click()
    await requested
    await other.goto('/settings/account')
    await other.getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await other.getByRole('alertdialog').getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await expect(panel(page).getByText(text.summaryCleared)).toBeVisible()
    release()
    await expect(panel(page).getByText(text.summaryDirectUnits, { exact: true })).toHaveCount(0)
    await expect(panel(page).getByText(text.summaryCleared)).toBeVisible()
  } finally { release(); await other.close() }
})

test('hides failed or offline numbers and requires an explicit retry after a failed read', async ({ page, context, application }) => {
  await provision(page, application)
  const root = await post(page, application.url, '/api/v1/locations', { name: '離線位置', code: 'SUMMARY-OFFLINE' })
  const writes = watchDomainWrites(page, ['/api/v1/locations'])
  await page.goto(`/locations/${root.id}`)
  await expect(amount(page, text.summaryDirectUnits)).toHaveText('0')
  let reads = 0
  const summaryPath = (url: URL) => url.pathname === `/api/v1/locations/${root.id}/summary`
  await page.route(summaryPath, async (route) => {
    reads++
    await route.fulfill({ status: 503, json: { code: 'unavailable' } })
  })
  await panel(page).getByRole('button', { name: text.summaryRefresh }).click()
  await expect(panel(page).getByText(text.summaryError)).toBeVisible()
  await expect(panel(page).getByText(text.summaryDirectUnits, { exact: true })).toHaveCount(0)
  await page.getByRole('button', { name: text.refresh, exact: true }).click()
  await expect(page.getByRole('button', { name: text.refresh, exact: true })).toBeEnabled()
  expect(reads).toBe(1)
  await context.setOffline(true)
  await expect(panel(page).getByText(text.summaryOffline)).toBeVisible()
  await expect(panel(page).getByText(text.summaryDirectUnits, { exact: true })).toHaveCount(0)
  await page.unroute(summaryPath)
  await context.setOffline(false)
  await expect(panel(page).getByRole('button', { name: text.summaryRefresh })).toHaveAttribute('aria-disabled', 'false')
  await panel(page).getByRole('button', { name: text.summaryRefresh }).click()
  await expect(amount(page, text.summaryDirectUnits)).toHaveText('0')
  expect(writes).toEqual([])
})

test('recovers explicitly when the Location read fails during summary refresh', async ({ page, application }) => {
  await provision(page, application)
  const root = await post(page, application.url, '/api/v1/locations', { name: '暫時無法讀取的位置', code: 'SUMMARY-RETRY' })
  await page.goto(`/locations/${root.id}`)
  await expect(amount(page, text.summaryDirectUnits)).toHaveText('0')
  let fail = true
  await page.route((url) => url.pathname === `/api/v1/locations/${root.id}`, async (route) => {
    if (fail) { fail = false; await route.fulfill({ status: 503, json: { code: 'unavailable' } }) }
    else await route.continue()
  })
  const refresh = panel(page).getByRole('button', { name: text.summaryRefresh })
  await refresh.click()
  await expect(panel(page).getByText(text.summaryError)).toBeVisible()
  await expect(panel(page).getByText(text.summaryDirectUnits, { exact: true })).toHaveCount(0)
  await expect(refresh).toHaveAttribute('aria-disabled', 'false')
  await refresh.click()
  await expect(amount(page, text.summaryDirectUnits)).toHaveText('0')
})

test('keeps maximum safe quantities and controls readable at 320px and 200% text in both themes', async ({ page, application }) => {
  await provision(page, application)
  const root = await post(page, application.url, '/api/v1/locations', { name: '最大數量位置', code: 'SUMMARY-MAX' })
  const item = await post(page, application.url, '/api/v1/items', { name: '大量庫存', tracking_mode: 'quantity' })
  await post(page, application.url, '/api/v1/stock/receive', { item_id: item.id, target_location_id: root.id, quantity_base: Number.MAX_SAFE_INTEGER }, item.revision)
  await page.setViewportSize({ width: 320, height: 568 })
  await page.goto(`/locations/${root.id}`)
  await expect(stock(page, text.summaryDirectStock)).toContainText('9007199254740991')
  await page.locator('html').evaluate((element) => { element.style.fontSize = '200%' })
  for (const colorScheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme })
    await expect(page.locator('html')).toHaveAttribute('data-theme', colorScheme)
    const geometry = await page.evaluate(() => ({ width: innerWidth, overflowing: Array.from(document.querySelectorAll('body *')).map((element) => {
      const rect = element.getBoundingClientRect()
      return { tag: element.tagName, className: element.getAttribute('class'), left: rect.left, right: rect.right, scroll: element.scrollWidth, client: element.clientWidth }
    }).filter((element) => element.right > 320.5 || element.left < -0.5).sort((a, b) => b.right - a.right).slice(0, 20) }))
    expect(geometry.width, JSON.stringify(geometry.overflowing)).toBe(320)
    await noOverflow(page)
    for (const button of [panel(page).getByRole('button', { name: text.summaryRefresh }), page.getByRole('button', { name: text.refresh, exact: true })]) {
      expect(await button.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)
      expect((await button.boundingBox())?.height).toBeGreaterThanOrEqual(44)
    }
    expect(await stock(page, text.summaryDirectStock).evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)
    expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze()).violations).toEqual([])
  }
})
