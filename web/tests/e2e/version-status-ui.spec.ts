import AxeBuilder from '@axe-core/playwright'
import { messages } from '../../src/lib/messages.ts'
import { test, expect } from './auth-fixture.ts'
import { noOverflow, provision, watchDomainWrites } from './domain-helpers.ts'

const text = messages.versionStatus

test.use({ trace: 'off', screenshot: 'off', video: 'off' })

test('reads version status without updating the page or replaying requests', async ({ page, context, application }) => {
  await provision(page, application)
  const methods: string[] = []
  page.on('request', (request) => {
    if (new URL(request.url()).pathname === '/api/v1/system/version') methods.push(request.method())
  })
  const writes = watchDomainWrites(page, ['/api/v1/'])
  await page.goto('/settings')
  await page.getByRole('list').getByRole('link', { name: new RegExp(messages.settings.version) }).click()
  await expect(page.getByText(text.writeAccepted)).toBeVisible()
  await expect(page.getByText(text.pageMatches)).toBeVisible()
  await expect(page.getByText(text.modelsUnknown)).toBeVisible()
  await expect(page.getByText(text.readOnlyHelp)).toBeVisible()
  const actual = await page.request.get('/api/v1/system/version', { headers: { Origin: application.url } })
  expect(actual.status()).toBe(200)
  const version = await actual.json()
  const path = (url: URL) => url.pathname === '/api/v1/system/version'
  await page.route(path, (route) => route.fulfill({ status: 200, headers: { 'Cache-Control': 'no-store' }, json: {
    ...version, web_build_id: 'f'.repeat(24), supported_write_client_versions: ['0.9.0'], model_index_versions: [],
  } }))
  await page.getByRole('button', { name: text.refresh }).click()
  await expect(page.getByText(text.writeRejected)).toBeVisible()
  await expect(page.getByText(text.writeRejectedHelp)).toBeVisible()
  await expect(page.getByText(text.pageDifferent)).toBeVisible()
  await expect(page.getByText(text.modelsEmpty)).toBeVisible()
  expect(new URL(page.url()).pathname).toBe('/settings/version')
  await page.unroute(path)
  await page.getByRole('button', { name: text.refresh }).click()
  await expect(page.getByText(text.writeAccepted)).toBeVisible()
  await page.route(path, (route) => route.fulfill({ status: 200, headers: { 'Cache-Control': 'no-store' }, json: { private: 'should not display' } }))
  await page.getByRole('button', { name: text.refresh }).click()
  await expect(page.getByRole('alert')).toContainText(text.readError)
  await expect(page.getByText(text.writeAccepted)).toHaveCount(0)
  await expect(page.locator('body')).not.toContainText('should not display')
  await page.unroute(path)
  await page.getByRole('button', { name: text.refresh }).click()
  await expect(page.getByText(text.writeAccepted)).toBeVisible()
  await context.setOffline(true)
  await expect(page.getByText(text.offlineHelp)).toBeVisible()
  await expect(page.getByText(text.writeAccepted)).toHaveCount(0)
  await expect(page.getByRole('button', { name: text.refresh })).toBeDisabled()
  await context.setOffline(false)
  await expect(page.getByText(text.writeAccepted)).toBeVisible()
  expect(methods.length).toBeGreaterThanOrEqual(5)
  expect(methods.every((method) => method === 'GET')).toBe(true)
  expect(writes).toEqual([])
})

for (const transport of ['storage', 'broadcast'] as const) test(`clears version information after logout using ${transport} only`, async ({ page, context, application }) => {
  await page.addInitScript((selected) => {
    if (selected === 'storage') Object.defineProperty(window, 'BroadcastChannel', { configurable: true, value: undefined })
    else window.addEventListener = new Proxy(window.addEventListener, { apply(target, receiver, args: unknown[]) {
      return args[0] === 'storage' ? undefined : Reflect.apply(target, receiver, args)
    } })
  }, transport)
  await provision(page, application)
  await page.goto('/settings/version')
  await expect(page.getByText(text.writeAccepted)).toBeVisible()
  const other = await context.newPage()
  try {
    await other.goto('/settings/account')
    await other.getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await other.getByRole('alertdialog').getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await expect(page.getByText(text.sessionCleared)).toBeVisible()
    await expect(page.getByText(text.writeAccepted)).toHaveCount(0)
  } finally { await other.close() }
})

test('keeps full version values and actions accessible at 320px and 200% text', async ({ page, application }) => {
  await provision(page, application)
  const actual = await page.request.get('/api/v1/system/version', { headers: { Origin: application.url } })
  expect(actual.status()).toBe(200)
  const version = await actual.json()
  await page.route((url) => url.pathname === '/api/v1/system/version', (route) => route.fulfill({ status: 200, headers: { 'Cache-Control': 'no-store' }, json: {
    ...version, dataset_epoch: '世'.repeat(128), web_version: 'v'.repeat(32), api_version: 'a'.repeat(32),
    min_supported_client: 'c'.repeat(32), min_web_build_id: 'b'.repeat(128), model_index_versions: ['模型'.repeat(128)],
    supported_write_client_versions: ['0.1.0-dev', ...Array.from({ length: 15 }, (_, index) => `version-${index}`)],
  } }))
  await page.setViewportSize({ width: 320, height: 568 })
  await page.goto('/settings/version')
  await expect(page.getByText(text.writeAccepted)).toBeVisible()
  await page.locator('html').evaluate((element) => { element.style.fontSize = '200%' })
  for (const colorScheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme }); await expect(page.locator('html')).toHaveAttribute('data-theme', colorScheme)
    expect(await page.evaluate(() => innerWidth)).toBe(320)
    await noOverflow(page)
    for (const element of await page.getByRole('main').getByRole('button').all()) {
      expect(await element.evaluate((value) => value.scrollWidth <= value.clientWidth)).toBe(true)
      expect((await element.boundingBox())?.height).toBeGreaterThanOrEqual(44)
    }
    expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze()).violations).toEqual([])
  }
})
