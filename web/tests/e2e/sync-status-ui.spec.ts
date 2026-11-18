import AxeBuilder from '@axe-core/playwright'
import { messages } from '../../src/lib/messages.ts'
import { test, expect } from './auth-fixture.ts'
import { noOverflow, provision, watchDomainWrites, writeContext } from './domain-helpers.ts'

const text = messages.syncStatus

test.use({ trace: 'off', screenshot: 'off', video: 'off' })

test('shows live server diagnostics, withholds invalid and offline replies, and never starts sync', async ({ page, context, application }) => {
  await provision(page, application)
  const methods: string[] = [], syncRequests: string[] = []
  page.on('request', (request) => {
    const path = new URL(request.url()).pathname
    if (path === '/api/v1/sync/status') methods.push(request.method())
    else if (path === '/api/v1/sync/bootstrap' || path === '/api/v1/sync/changes') syncRequests.push(path)
  })
  const writes = watchDomainWrites(page, ['/api/v1/sync'])
  await page.goto('/settings')
  await page.getByRole('list').getByRole('link', { name: new RegExp(messages.settings.sync) }).click()
  await expect(page.getByText(text.bootstrapUnavailable)).toBeVisible()
  await expect(page.getByText(text.retentionDisabled)).toBeVisible()
  await expect(page.getByText(text.noCounts)).toBeVisible()
  await expect(page.getByText(text.noLastChange)).toBeVisible()
  const created = await page.request.post('/api/v1/locations', { headers: await writeContext(page, application.url), data: { name: 'Status fixture', code: 'STATUS-UI' } })
  expect(created.status()).toBe(200)
  await page.getByRole('button', { name: text.refresh }).click()
  const counts = page.getByRole('region', { name: text.counts })
  await expect(counts.getByText(text.kinds.location, { exact: true })).toBeVisible()
  await expect(counts.getByText('1', { exact: true })).toBeVisible()
  await expect(page.getByText(text.countsHelp)).toBeVisible()
  await expect(page.getByText(text.bootstrapHelp)).toBeVisible()
  await expect(page.getByText(text.noLastChange)).toHaveCount(0)
  const statusRoute = (url: URL) => url.pathname === '/api/v1/sync/status'
  await page.route(statusRoute, async (route) => {
    await route.fulfill({ status: 200, headers: { 'Cache-Control': 'no-store' }, json: { private: 'should not display' } })
  })
  await page.getByRole('button', { name: text.refresh }).click()
  await expect(page.getByRole('alert')).toContainText(text.readError)
  await expect(page.getByRole('region', { name: text.counts })).toHaveCount(0)
  await expect(page.locator('body')).not.toContainText('should not display')
  await page.unroute(statusRoute)
  await page.getByRole('button', { name: text.refresh }).click()
  await expect(counts.getByText(text.kinds.location, { exact: true })).toBeVisible()
  await context.setOffline(true)
  await expect(page.getByText(text.offlineHelp)).toBeVisible()
  await expect(page.getByRole('region', { name: text.counts })).toHaveCount(0)
  await expect(page.getByRole('button', { name: text.refresh })).toBeDisabled()
  await context.setOffline(false)
  await expect(counts.getByText(text.kinds.location, { exact: true })).toBeVisible()
  expect(methods.length).toBeGreaterThanOrEqual(4)
  expect(methods.every((method) => method === 'GET')).toBe(true)
  expect(syncRequests).toEqual([])
  expect(writes).toEqual([])
})

for (const transport of ['storage', 'broadcast'] as const) test(`clears the diagnostic after cross-tab logout with ${transport} only`, async ({ page, context, application }) => {
  await page.addInitScript((selected) => {
    if (selected === 'storage') Object.defineProperty(window, 'BroadcastChannel', { configurable: true, value: undefined })
    else window.addEventListener = new Proxy(window.addEventListener, { apply(target, receiver, args: unknown[]) {
      return args[0] === 'storage' ? undefined : Reflect.apply(target, receiver, args)
    } })
  }, transport)
  await provision(page, application)
  await page.goto('/settings/sync-status')
  await expect(page.getByText(text.bootstrapUnavailable)).toBeVisible()
  const other = await context.newPage()
  try {
    await other.goto('/settings/account')
    await other.getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await other.getByRole('alertdialog').getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await expect(page.getByText(text.sessionCleared)).toBeVisible()
    await expect(page.getByText(text.bootstrapUnavailable)).toHaveCount(0)
  } finally { await other.close() }
})

test('keeps large diagnostic values and controls accessible at 320px and 200% text in both themes', async ({ page, application }) => {
  await provision(page, application)
  await page.route((url) => url.pathname === '/api/v1/sync/status', async (route) => {
    await route.fulfill({ status: 200, json: {
      dataset_epoch: '世'.repeat(128), server_instance_id: 'a'.repeat(32), projection_version: 1,
      min_retained_seq: Number.MAX_SAFE_INTEGER - 1, high_watermark: Number.MAX_SAFE_INTEGER,
      counts: { item: Number.MAX_SAFE_INTEGER, inventory_event: 1 }, counts_scope: 'materialized_projections',
      last_change_at: '2026-11-18T04:05:06.123456789+08:00', retention_days: Number.MAX_SAFE_INTEGER,
      bootstrap_ready: false, retention_enabled: false, models: ['model-'.repeat(42)],
    } })
  })
  await page.setViewportSize({ width: 320, height: 568 })
  await page.goto('/settings/sync-status')
  await expect(page.getByText(text.bootstrapUnavailable)).toBeVisible()
  await page.locator('html').evaluate((element) => { element.style.fontSize = '200%' })
  for (const colorScheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme }); await expect(page.locator('html')).toHaveAttribute('data-theme', colorScheme)
    expect(await page.evaluate(() => innerWidth)).toBe(320)
    await noOverflow(page)
    for (const button of await page.getByRole('main').getByRole('button').all()) {
      expect(await button.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)
      expect((await button.boundingBox())?.height).toBeGreaterThanOrEqual(44)
    }
    expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze()).violations).toEqual([])
  }
})
