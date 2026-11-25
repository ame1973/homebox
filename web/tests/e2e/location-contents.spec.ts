import AxeBuilder from '@axe-core/playwright'
import type { Page } from '@playwright/test'
import { messages } from '../../src/lib/messages.ts'
import { quoteExactText } from '../../src/lib/text-literal.ts'
import { test, expect } from './auth-fixture.ts'
import { noOverflow, provision, watchDomainWrites, writeContext } from './domain-helpers.ts'
import { contentsChild, contentsRoot, contentsUnsafeItem, contentsUnsafeLocation, contentsUnsafeUnit, prepareContentsScreen } from './location-contents-fixture.ts'

const text = messages.locations
const panel = (page: Page) => page.getByRole('region', { name: text.contentsTitle, exact: true })
const scope = (page: Page, label: string) => panel(page).getByRole('radio', { name: label, exact: true })
const next = (page: Page) => panel(page).getByRole('button', { name: text.contentsNext })
const previous = (page: Page) => panel(page).getByRole('button', { name: text.contentsPrevious })
const restart = (page: Page) => panel(page).getByRole('button', { name: text.contentsRestart })
async function open(page: Page) {
  const button = panel(page).getByRole('button', { name: text.contentsOpen })
  await expect(button).toHaveAttribute('aria-disabled', 'false')
  await button.click()
  await expect(panel(page).getByText(text.contentsMore)).toBeVisible()
}

test.use({ trace: 'off', screenshot: 'off', video: 'off', applicationDatabaseFixture: { prepare: prepareContentsScreen } })

test('browses real bounded subtree and direct records with exact legacy identities', async ({ page, application }) => {
  await provision(page, application)
  const writes = watchDomainWrites(page, ['/api/v1/locations', '/api/v1/stock', '/api/v1/units'])
  let reads = 0
  page.on('request', (request) => { if (new URL(request.url()).pathname === `/api/v1/locations/${contentsRoot}/contents`) reads++ })
  await page.goto(`/locations/${contentsRoot}`)
  expect(reads).toBe(0)
  await open(page)
  await expect(panel(page).getByText(quoteExactText(contentsUnsafeUnit), { exact: true })).toBeVisible()
  await expect(panel(page).getByText('"ASSET-\\u202eLEGACY"', { exact: true })).toBeVisible()
  await expect(panel(page).getByText(messages.holdings.statuses.disposed, { exact: true })).toBeVisible()
  const unsafeUnit = panel(page).getByText(quoteExactText(contentsUnsafeUnit), { exact: true }).locator('xpath=ancestor::li[1]')
  await expect(unsafeUnit.getByRole('link', { name: text.contentsUnitLink })).toHaveCount(0)
  await expect(unsafeUnit.getByRole('link', { name: `${text.contentsItemLink}："screen-individual"`, exact: true })).toHaveAttribute('href', '/items/screen-individual')
  await next(page).click()
  await expect(panel(page).getByText(text.contentsEnd)).toBeVisible()
  await expect(panel(page).getByText(text.contentsZero)).toBeVisible()
  await expect(panel(page).getByText(quoteExactText(contentsUnsafeItem), { exact: true })).toBeVisible()
  const unsafeStock = panel(page).getByText(quoteExactText(contentsUnsafeItem), { exact: true }).locator('xpath=ancestor::li[1]')
  await expect(unsafeStock.getByRole('link', { name: text.contentsItemLink })).toHaveCount(0)
  await expect(panel(page)).toContainText('9,007,199,254,740,991')
  await expect(panel(page).getByText(quoteExactText(contentsUnsafeLocation), { exact: true })).toBeVisible()
  const unsafeLocation = panel(page).getByText(quoteExactText(contentsUnsafeLocation), { exact: true }).locator('xpath=ancestor::li[1]')
  await expect(unsafeLocation.getByRole('link', { name: text.contentsLocationLink })).toHaveCount(0)
  const safeLocation = panel(page).getByText('"內容箱"', { exact: true }).locator('xpath=ancestor::li[1]')
  await expect(safeLocation.getByRole('link', { name: `${text.contentsLocationLink}：${quoteExactText(contentsChild)}`, exact: true })).toHaveAttribute('href', `/locations/${contentsChild}/view`)
  const completedReads = reads
  await previous(page).click(); await expect(panel(page).getByText(text.contentsMore)).toBeVisible()
  await next(page).click(); await expect(panel(page).getByText(text.contentsEnd)).toBeVisible()
  expect(reads).toBe(completedReads)
  await scope(page, text.contentsDirect).check()
  await expect(panel(page).getByText(text.contentsEnd)).toBeVisible()
  await expect(panel(page).getByText('"ASSET-01"', { exact: true })).toHaveCount(0)
  await expect(panel(page).getByText('"ASSET-02"', { exact: true })).toBeVisible()
  await expect(panel(page).getByText(text.contentsZero)).toBeVisible()
  expect(writes).toEqual([])
})

test('requires an explicit restart after an actual subtree change invalidates a cursor', async ({ page, application }) => {
  await provision(page, application)
  await page.goto(`/locations/${contentsRoot}`); await open(page)
  const created = await page.request.post('/api/v1/locations', { headers: await writeContext(page, application.url, 1), data: { name: '新下層', code: 'SCREEN-NEW', parent_id: contentsRoot } })
  expect(created.status()).toBe(200)
  await next(page).click()
  await expect(panel(page).getByText(text.contentsChanged)).toBeVisible()
  await expect(panel(page).getByText(quoteExactText(contentsUnsafeUnit), { exact: true })).toHaveCount(0)
  await restart(page).click()
  await expect(panel(page).getByText(quoteExactText(contentsUnsafeUnit), { exact: true })).toBeVisible()
  await next(page).click()
  await expect(panel(page).getByText('"新下層"', { exact: true })).toBeVisible()
})

test('withholds a duplicate cross-page identity and permits an explicit first-page retry', async ({ page, application }) => {
  await provision(page, application)
  let sample: unknown, reads = 0
  const endpoint = (url: URL) => url.pathname === `/api/v1/locations/${contentsRoot}/contents`
  await page.route(endpoint, async (route) => {
    const response = await route.fetch(), value = await response.json() as { units: unknown[]; stock: unknown[]; sub_locations: unknown[]; next_cursor: string | null }
    reads++
    if (reads === 1) sample = value.units[0]
    else if (reads === 2) { value.units = [sample]; value.stock = []; value.sub_locations = []; value.next_cursor = null }
    await route.fulfill({ response, json: value })
  })
  await page.goto(`/locations/${contentsRoot}`); await open(page); await next(page).click()
  await expect(panel(page).getByText(text.contentsError)).toBeVisible()
  await expect(panel(page).getByText(text.contentsUnits, { exact: true })).toHaveCount(0)
  await page.unroute(endpoint)
  await restart(page).click(); await expect(panel(page).getByText(text.contentsMore)).toBeVisible()
  expect(reads).toBe(2)
})

test('hides failed and offline pages and does not retry on revalidation or reconnect', async ({ page, context, application }) => {
  await provision(page, application)
  const endpoint = (url: URL) => url.pathname === `/api/v1/locations/${contentsRoot}/contents`
  let reads = 0
  page.on('request', (request) => { if (endpoint(new URL(request.url()))) reads++ })
  await page.goto(`/locations/${contentsRoot}`); await open(page)
  await page.route(endpoint, async (route) => { await route.fulfill({ status: 503, json: { error: { code: 'SERVICE_UNAVAILABLE' } } }) })
  await next(page).click(); await expect(panel(page).getByText(text.contentsError)).toBeVisible()
  const afterFailure = reads
  await page.getByRole('button', { name: text.refresh, exact: true }).click()
  await expect(page.getByRole('button', { name: text.refresh, exact: true })).toBeEnabled()
  expect(reads).toBe(afterFailure)
  await context.setOffline(true); await expect(panel(page).getByText(text.contentsOffline)).toBeVisible()
  await expect(panel(page).getByText(text.contentsUnits, { exact: true })).toHaveCount(0)
  await page.unroute(endpoint); await context.setOffline(false)
  await expect(restart(page)).toHaveAttribute('aria-disabled', 'false')
  expect(reads).toBe(afterFailure)
  await expect(panel(page).getByText(text.contentsOffline)).toBeVisible()
  await restart(page).click(); await expect(panel(page).getByText(text.contentsMore)).toBeVisible()
  expect(reads).toBe(afterFailure + 1)
})

for (const transport of ['storage', 'broadcast'] as const) test(`discards a late contents page on real cross-tab logout using ${transport} only`, async ({ page, context, application }) => {
  await page.addInitScript((selected) => {
    if (selected === 'storage') Object.defineProperty(window, 'BroadcastChannel', { configurable: true, value: undefined })
    else window.addEventListener = new Proxy(window.addEventListener, { apply(target, receiver, args: unknown[]) {
      return args[0] === 'storage' ? undefined : Reflect.apply(target, receiver, args)
    } })
  }, transport)
  await provision(page, application)
  await page.goto(`/locations/${contentsRoot}`); await open(page)
  let started!: () => void, release!: () => void
  const requested = new Promise<void>((resolve) => { started = resolve }), gate = new Promise<void>((resolve) => { release = resolve })
  await page.route((url) => url.pathname === `/api/v1/locations/${contentsRoot}/contents`, async (route) => {
    const response = await route.fetch(); started(); await gate
    await route.fulfill({ response }).catch(() => {})
  })
  const other = await context.newPage()
  try {
    await next(page).click(); await requested
    await other.goto('/settings/account')
    await other.getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await other.getByRole('alertdialog').getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await expect(panel(page).getByText(text.contentsCleared)).toBeVisible()
    release()
    await expect(panel(page).getByText(text.contentsUnits, { exact: true })).toHaveCount(0)
    await expect(panel(page).getByText(text.contentsCleared)).toBeVisible()
  } finally { release(); await other.close() }
})

test('keeps the contents controls and maximum quantity accessible at 320px and 200% text', async ({ page, application }) => {
  await provision(page, application)
  await page.setViewportSize({ width: 320, height: 568 })
  await page.goto(`/locations/${contentsRoot}`); await open(page)
  await next(page).click(); await expect(panel(page)).toContainText('9,007,199,254,740,991')
  await page.locator('html').evaluate((element) => { element.style.fontSize = '200%' })
  for (const colorScheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme }); await expect(page.locator('html')).toHaveAttribute('data-theme', colorScheme)
    expect(await page.evaluate(() => innerWidth)).toBe(320)
    await noOverflow(page)
    for (const button of [previous(page), next(page), restart(page)]) {
      expect(await button.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)
      expect((await button.boundingBox())?.height).toBeGreaterThanOrEqual(44)
    }
    await previous(page).focus(); await expect(previous(page)).toBeFocused()
    expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze()).violations).toEqual([])
  }
})
