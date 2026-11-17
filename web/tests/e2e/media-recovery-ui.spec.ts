import AxeBuilder from '@axe-core/playwright'
import { messages } from '../../src/lib/messages.ts'
import { test, expect } from './auth-fixture.ts'
import { noOverflow, provision, watchDomainWrites } from './domain-helpers.ts'

const text = messages.mediaRecovery
const idle = { phase: 'idle', completed: 0, saturated: false, failure: '' } as const

test.use({ trace: 'off', screenshot: 'off', video: 'off' })

test('shows read-only recovery status, withholds invalid responses and never sends a recovery write', async ({ page, context, application }) => {
  await provision(page, application)
  let value: object = idle
  await page.route((url) => url.pathname === '/api/v1/system/media-recovery', async (route) => {
    if (route.request().method() === 'GET') await route.fulfill({ status: 200, headers: { 'Cache-Control': 'no-store' }, json: value })
    else await route.continue()
  })
  const writes = watchDomainWrites(page, ['/api/v1/system/media-recovery'])
  await page.goto('/settings')
  await page.getByRole('list').getByRole('link', { name: new RegExp(messages.settings.recovery) }).click()
  await expect(page.getByRole('heading', { name: text.phases.idle })).toBeVisible()
  await expect(page.getByText(text.readOnlyHelp)).toBeVisible()
  value = { phase: 'blocked', completed: 3, saturated: false, failure: 'staging-unproven' }
  await page.getByRole('button', { name: text.refresh }).click()
  await expect(page.getByRole('heading', { name: text.phases.blocked })).toBeVisible()
  await expect(page.getByText(text.failures['staging-unproven'])).toBeVisible()
  await expect(page.getByText(text.blockedHelp)).toBeVisible()
  await expect(page.getByRole('button', { name: /刪除|重試清理/ })).toHaveCount(0)
  value = { phase: 'blocked', completed: 3, saturated: false, failure: 'private/unknown-file' }
  await page.getByRole('button', { name: text.refresh }).click()
  await expect(page.getByRole('alert')).toContainText(text.readError)
  await expect(page.locator('body')).not.toContainText('private/unknown-file')
  await expect(page.getByRole('heading', { name: text.phases.blocked })).toHaveCount(0)
  await context.setOffline(true)
  await expect(page.getByText(text.offlineHelp)).toBeVisible()
  await expect(page.getByRole('button', { name: text.refresh })).toBeDisabled()
  value = idle
  await context.setOffline(false)
  await expect(page.getByRole('heading', { name: text.phases.idle })).toBeVisible()
  expect(writes).toEqual([])
})

for (const transport of ['storage', 'broadcast'] as const) test(`clears a status snapshot after cross-tab logout with ${transport} only`, async ({ page, context, application }) => {
  await page.addInitScript((selected) => {
    if (selected === 'storage') Object.defineProperty(window, 'BroadcastChannel', { configurable: true, value: undefined })
    else window.addEventListener = new Proxy(window.addEventListener, { apply(target, receiver, args: unknown[]) {
      return args[0] === 'storage' ? undefined : Reflect.apply(target, receiver, args)
    } })
  }, transport)
  await provision(page, application)
  await page.route((url) => url.pathname === '/api/v1/system/media-recovery', async (route) => {
    if (route.request().method() === 'GET') await route.fulfill({ status: 200, json: idle })
    else await route.continue()
  })
  await page.goto('/settings/media-recovery')
  await expect(page.getByRole('heading', { name: text.phases.idle })).toBeVisible()
  const other = await context.newPage()
  try {
    await other.goto('/settings/account')
    await other.getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await other.getByRole('alertdialog').getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await expect(page.getByText(text.sessionCleared)).toBeVisible()
    await expect(page.getByRole('heading', { name: text.phases.idle })).toHaveCount(0)
  } finally { await other.close() }
})

test('keeps the status and refresh controls accessible at 320px and 200% text in both themes', async ({ page, application }) => {
  await provision(page, application)
  await page.route((url) => url.pathname === '/api/v1/system/media-recovery', async (route) => {
    await route.fulfill({ status: 200, json: { phase: 'idle', completed: Number.MAX_SAFE_INTEGER, saturated: true, failure: '' } })
  })
  await page.setViewportSize({ width: 320, height: 568 })
  await page.goto('/settings/media-recovery')
  await expect(page.getByRole('heading', { name: text.phases.idle })).toBeVisible()
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
