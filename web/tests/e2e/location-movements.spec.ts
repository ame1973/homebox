import AxeBuilder from '@axe-core/playwright'
import type { Page } from '@playwright/test'
import { messages } from '../../src/lib/messages.ts'
import { quoteExactText } from '../../src/lib/text-literal.ts'
import { test, expect } from './auth-fixture.ts'
import { noOverflow, provision, watchDomainWrites, writeContext } from './domain-helpers.ts'
import { deletedMovementOwner, firstMovement, historicalParent, legacyOccurrence, maximumMovement, movementRoot, prepareMovementScreen } from './location-movements-fixture.ts'

const text = messages.locations
const panel = (page: Page) => page.getByRole('region', { name: text.movementsTitle, exact: true })
const next = (page: Page) => panel(page).getByRole('button', { name: text.movementsNext, exact: true })
const previous = (page: Page) => panel(page).getByRole('button', { name: text.movementsPrevious, exact: true })
const restart = (page: Page) => panel(page).getByRole('button', { name: text.movementsRestart, exact: true })
async function open(page: Page) {
  const button = panel(page).getByRole('button', { name: text.movementsOpen, exact: true })
  await expect(button).toHaveAttribute('aria-disabled', 'false'); await button.click()
  await expect(panel(page).getByText(text.movementsMore)).toBeVisible()
}

test.use({ trace: 'off', screenshot: 'off', video: 'off', applicationDatabaseFixture: { prepare: prepareMovementScreen } })

test('browses actual retained parent changes in explicit pages without inventing a timeline', async ({ page, application }) => {
  await provision(page, application)
  const writes = watchDomainWrites(page, ['/api/v1/locations', '/api/v1/location-movements'])
  let reads = 0
  page.on('request', (request) => { if (new URL(request.url()).pathname === `/api/v1/locations/${movementRoot}/movements`) reads++ })
  await page.goto(`/locations/${movementRoot}`)
  expect(reads).toBe(0)
  await open(page)
  await expect(panel(page).getByText(text.movementsHelp)).toContainText('不是完整稽核紀錄')
  await expect(panel(page).getByText(text.movementsHelp)).toContainText('不是時間先後順序')
  const first = panel(page).getByText(quoteExactText(firstMovement), { exact: true }).locator('xpath=ancestor::li[1]')
  await expect(first.getByText(quoteExactText(legacyOccurrence), { exact: true })).toBeVisible()
  await expect(first.getByText(quoteExactText(historicalParent), { exact: true })).toBeVisible()
  await expect(first.getByText(text.movementsRoot, { exact: true })).toBeVisible()
  await expect(restart(page)).toBeFocused()
  await next(page).click()
  await expect(panel(page).getByText(text.movementsEnd)).toBeVisible()
  await expect(panel(page).getByText('"movement-20"', { exact: true })).toBeVisible()
  const completedReads = reads
  await previous(page).click(); await expect(panel(page).getByText(quoteExactText(firstMovement), { exact: true })).toBeVisible()
  await next(page).click(); await expect(panel(page).getByText(text.movementsEnd)).toBeVisible()
  expect(reads).toBe(completedReads)
  expect(writes).toEqual([])
})

test('requires a first-page restart after an actual Location revision changes', async ({ page, application }) => {
  await provision(page, application)
  await page.goto(`/locations/${movementRoot}`); await open(page)
  const changed = await page.request.patch(`/api/v1/locations/${movementRoot}`, { headers: await writeContext(page, application.url, 1), data: { name: '已變更房間' } })
  expect(changed.status()).toBe(200)
  await next(page).click()
  await expect(panel(page).getByText(text.movementsChanged)).toBeVisible()
  await expect(panel(page).getByText(quoteExactText(firstMovement), { exact: true })).toHaveCount(0)
  await restart(page).click(); await expect(panel(page).getByText(quoteExactText(firstMovement), { exact: true })).toBeVisible()
})

test('withholds a repeated event on a later live page', async ({ page, application }) => {
  await provision(page, application)
  let sample: unknown, reads = 0
  const endpoint = (url: URL) => url.pathname === `/api/v1/locations/${movementRoot}/movements`
  await page.route(endpoint, async (route) => {
    const response = await route.fetch(), value = await response.json() as { data: unknown[]; next_cursor: string | null }
    reads++
    if (reads === 1) sample = value.data[0]
    else if (reads === 2) value.data[0] = sample
    await route.fulfill({ response, json: value })
  })
  await page.goto(`/locations/${movementRoot}`); await open(page); await next(page).click()
  await expect(panel(page).getByText(text.movementsError)).toBeVisible()
  await expect(panel(page).getByText(quoteExactText(firstMovement), { exact: true })).toHaveCount(0)
  await page.unroute(endpoint)
  await restart(page).click(); await expect(panel(page).getByText(text.movementsMore)).toBeVisible()
  expect(reads).toBe(2)
})

test('hides failed and offline pages without automatic reconnect or revalidation retry', async ({ page, context, application }) => {
  await provision(page, application)
  const endpoint = (url: URL) => url.pathname === `/api/v1/locations/${movementRoot}/movements`
  let reads = 0
  page.on('request', (request) => { if (endpoint(new URL(request.url()))) reads++ })
  await page.goto(`/locations/${movementRoot}`); await open(page)
  await page.route(endpoint, async (route) => { await route.fulfill({ status: 503, json: { error: { code: 'SERVICE_UNAVAILABLE' } } }) })
  await next(page).click(); await expect(panel(page).getByText(text.movementsError)).toBeVisible()
  const afterFailure = reads
  await page.getByRole('button', { name: text.refresh, exact: true }).click()
  await expect(page.getByRole('button', { name: text.refresh, exact: true })).toBeEnabled()
  expect(reads).toBe(afterFailure)
  await context.setOffline(true); await expect(panel(page).getByText(text.movementsOffline)).toBeVisible()
  await expect(panel(page).getByText(quoteExactText(firstMovement), { exact: true })).toHaveCount(0)
  await page.unroute(endpoint); await context.setOffline(false)
  await expect(restart(page)).toHaveAttribute('aria-disabled', 'false')
  expect(reads).toBe(afterFailure)
  await expect(panel(page).getByText(text.movementsOffline)).toBeVisible()
  await restart(page).click(); await expect(panel(page).getByText(text.movementsMore)).toBeVisible()
  expect(reads).toBe(afterFailure + 1)
})

for (const transport of ['storage', 'broadcast'] as const) test(`discards a late movement page on real cross-tab logout using ${transport} only`, async ({ page, context, application }) => {
  await page.addInitScript((selected) => {
    if (selected === 'storage') Object.defineProperty(window, 'BroadcastChannel', { configurable: true, value: undefined })
    else window.addEventListener = new Proxy(window.addEventListener, { apply(target, receiver, args: unknown[]) {
      return args[0] === 'storage' ? undefined : Reflect.apply(target, receiver, args)
    } })
  }, transport)
  await provision(page, application)
  await page.goto(`/locations/${movementRoot}`); await open(page)
  let started!: () => void, release!: () => void
  const requested = new Promise<void>((resolve) => { started = resolve }), gate = new Promise<void>((resolve) => { release = resolve })
  await page.route((url) => url.pathname === `/api/v1/locations/${movementRoot}/movements`, async (route) => {
    const response = await route.fetch(); started(); await gate
    await route.fulfill({ response }).catch(() => {})
  })
  const other = await context.newPage()
  try {
    await next(page).click(); await requested
    await other.goto('/settings/account')
    await other.getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await other.getByRole('alertdialog').getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await expect(panel(page).getByText(text.movementsCleared)).toBeVisible()
    release()
    await expect(panel(page).getByText(quoteExactText(firstMovement), { exact: true })).toHaveCount(0)
    await expect(panel(page).getByText(text.movementsCleared)).toBeVisible()
  } finally { release(); await other.close() }
})

test('keeps history controls and exact retained text accessible at 320px and 200%', async ({ page, application }) => {
  await provision(page, application)
  await page.setViewportSize({ width: 320, height: 568 })
  await page.goto(`/locations/${movementRoot}`); await open(page)
  await next(page).click(); await expect(panel(page).getByText(quoteExactText(maximumMovement), { exact: true })).toBeVisible()
  await page.locator('html').evaluate((element) => { element.style.fontSize = '200%' })
  for (const colorScheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme }); await expect(page.locator('html')).toHaveAttribute('data-theme', colorScheme)
    expect(await page.evaluate(() => innerWidth)).toBe(320)
    await noOverflow(page)
    expect(await panel(page).getByText(quoteExactText(maximumMovement), { exact: true }).evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)
    for (const button of [previous(page), next(page), restart(page)]) {
      expect(await button.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)
      expect((await button.boundingBox())?.height).toBeGreaterThanOrEqual(44)
    }
    await next(page).focus(); await expect(next(page)).toBeFocused()
    expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze()).violations).toEqual([])
  }
})

const lookup = (page: Page) => page.getByRole('region', { name: text.movementLookupTitle, exact: true })
async function selectDeletedOwner(page: Page) {
  await lookup(page).getByRole('radio', { name: text.movementLookupJSON, exact: true }).check()
  await lookup(page).getByRole('textbox', { name: text.movementLookupID }).fill(quoteExactText(deletedMovementOwner))
  const button = lookup(page).getByRole('button', { name: text.movementLookupChoose, exact: true })
  await expect(button).toHaveAttribute('aria-disabled', 'false'); await button.click()
}

test('reads an exact deleted owner by explicit selection without putting its ID in the route', async ({ page, application }) => {
  await provision(page, application)
  const writes = watchDomainWrites(page, ['/api/v1/locations', '/api/v1/location-movements'])
  const queries: string[] = []
  page.on('request', (request) => {
    const url = new URL(request.url())
    if (url.pathname === '/api/v1/location-movements') queries.push(url.searchParams.get('id') ?? '')
  })
  await page.goto('/locations')
  await lookup(page).getByRole('radio', { name: text.movementLookupJSON }).check()
  await lookup(page).getByRole('textbox', { name: text.movementLookupID }).fill('"unfinished')
  await lookup(page).getByRole('button', { name: text.movementLookupChoose }).click()
  await expect(lookup(page).getByText(text.movementLookupInvalid)).toBeVisible()
  expect(queries).toEqual([])
  await selectDeletedOwner(page)
  expect(queries).toEqual([])
  await expect(lookup(page).getByText(text.movementLookupSelected)).toBeVisible()
  await panel(page).getByRole('button', { name: text.movementsOpen, exact: true }).click()
  await expect(panel(page).getByText(text.movementsDeleted)).toBeVisible()
  await expect(panel(page).getByText('"deleted-event-1"', { exact: true })).toBeVisible()
  await expect(panel(page)).toContainText(quoteExactText(deletedMovementOwner))
  expect(queries).toEqual([deletedMovementOwner])
  expect(new URL(page.url()).pathname).toBe('/locations'); expect(new URL(page.url()).search).toBe('')
  expect(writes).toEqual([])
})

test('permits explicit session recovery after a failed account revalidation', async ({ page, application }) => {
  await provision(page, application); await page.goto('/locations')
  const refresh = page.getByRole('button', { name: text.refresh, exact: true })
  await expect(refresh).toBeEnabled()
  const account = (url: URL) => url.pathname === '/api/v1/auth/me'
  await page.route(account, async (route) => { await route.fulfill({ status: 503, json: { error: { code: 'SERVICE_UNAVAILABLE' } } }) })
  const failed = page.waitForResponse((response) => account(new URL(response.url())) && response.status() === 503)
  await refresh.click(); await failed; await expect(refresh).toBeEnabled()
  await selectDeletedOwner(page)
  await page.unroute(account)
  const openHistory = panel(page).getByRole('button', { name: text.movementsOpen })
  await expect(openHistory).toHaveAttribute('aria-disabled', 'false'); await openHistory.click()
  await expect(panel(page).getByText(text.movementsDeleted)).toBeVisible()
})

for (const transport of ['storage', 'broadcast'] as const) test(`clears a deleted-owner lookup on cross-tab logout using ${transport} only`, async ({ page, context, application }) => {
  await page.addInitScript((selected) => {
    if (selected === 'storage') Object.defineProperty(window, 'BroadcastChannel', { configurable: true, value: undefined })
    else window.addEventListener = new Proxy(window.addEventListener, { apply(target, receiver, args: unknown[]) {
      return args[0] === 'storage' ? undefined : Reflect.apply(target, receiver, args)
    } })
  }, transport)
  await provision(page, application); await page.goto('/locations'); await selectDeletedOwner(page)
  let started!: () => void, release!: () => void
  const requested = new Promise<void>((resolve) => { started = resolve }), gate = new Promise<void>((resolve) => { release = resolve })
  await page.route((url) => url.pathname === '/api/v1/location-movements', async (route) => {
    const response = await route.fetch(); started(); await gate
    await route.fulfill({ response }).catch(() => {})
  })
  const other = await context.newPage()
  try {
    await panel(page).getByRole('button', { name: text.movementsOpen }).click(); await requested
    await other.goto('/settings/account')
    await other.getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await other.getByRole('alertdialog').getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await expect(lookup(page).getByText(text.movementLookupCleared)).toBeVisible()
    await expect(lookup(page).getByRole('textbox', { name: text.movementLookupID })).toHaveCount(0)
    await expect(page.getByRole('region', { name: text.movementsTitle, exact: true })).toHaveCount(0)
    release()
    await expect(lookup(page).getByText(text.movementLookupCleared)).toBeVisible()
  } finally { release(); await other.close() }
})

test('keeps the exact deleted-owner selector accessible at 320px and 200%', async ({ page, application }) => {
  await provision(page, application); await page.setViewportSize({ width: 320, height: 568 }); await page.goto('/locations')
  await selectDeletedOwner(page); await panel(page).getByRole('button', { name: text.movementsOpen }).click()
  await expect(panel(page).getByText(text.movementsDeleted)).toBeVisible()
  await page.locator('html').evaluate((element) => { element.style.fontSize = '200%' })
  for (const colorScheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme }); await expect(page.locator('html')).toHaveAttribute('data-theme', colorScheme)
    expect(await page.evaluate(() => innerWidth)).toBe(320); await noOverflow(page)
    const choose = lookup(page).getByRole('button', { name: text.movementLookupChoose })
    expect(await choose.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)
    expect((await choose.boundingBox())?.height).toBeGreaterThanOrEqual(44)
    await lookup(page).getByRole('textbox', { name: text.movementLookupID }).focus()
    await expect(lookup(page).getByRole('textbox', { name: text.movementLookupID })).toBeFocused()
    expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze()).violations).toEqual([])
  }
})
