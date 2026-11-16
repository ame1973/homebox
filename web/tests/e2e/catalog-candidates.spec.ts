import AxeBuilder from '@axe-core/playwright'
import type { Page } from '@playwright/test'
import type { components } from '../../src/lib/api/schema.d.ts'
import { messages } from '../../src/lib/messages.ts'
import { test, expect } from './auth-fixture.ts'
import { noOverflow, provision, watchDomainWrites, writeContext } from './domain-helpers.ts'

test.use({ trace: 'off', screenshot: 'off', video: 'off' })
const text = messages.catalog
const input = (page: Page) => page.getByRole('textbox', { name: new RegExp(`^${text.fields.name}`) })
type Item = components['schemas']['Item']

async function seed(page: Page, origin: string, name: string, brand?: string): Promise<Item> {
  const response = await page.request.post('/api/v1/items', { headers: await writeContext(page, origin), data: { name, ...(brand === undefined ? {} : { brand }) } })
  expect(response.status()).toBe(200)
  return response.json()
}

async function openReview(page: Page, name: string) {
  await page.goto('/items/new')
  await input(page).fill(name)
  await page.getByRole('button', { name: text.review, exact: true }).click()
  const dialog = page.getByRole('alertdialog')
  return { dialog, related: dialog.getByRole('region', { name: text.relatedTitle }) }
}

test('shows actual approved-text candidates in explicitly requested pages before Item creation', async ({ page, application }) => {
  await provision(page, application)
  const records: Item[] = []
  for (let index = 0; index < 6; index++) records.push(await seed(page, application.url, `珍珠茶 ${index}`))
  records.push(await seed(page, application.url, 'Different title', '珍珠茶 brand'))
  const archived = await page.request.post(`/api/v1/items/${records[0]!.id}/archive`, { headers: await writeContext(page, application.url, records[0]!.revision), data: {} })
  expect(archived.status()).toBe(200)
  const writes = watchDomainWrites(page, ['/api/v1/items']), queries: URLSearchParams[] = []
  page.on('request', (request) => { const url = new URL(request.url()); if (url.pathname === '/api/v1/items' && request.method() === 'GET') queries.push(url.searchParams) })
  const exact = '  珍珠茶  ', { dialog, related } = await openReview(page, exact)
  await expect(related.getByRole('listitem')).toHaveCount(5)
  expect(queries).toHaveLength(1)
  expect(Object.fromEntries(queries[0]!)).toEqual({ q: exact, limit: '5' })
  await expect(related.getByText(text.relatedHelp)).toBeVisible()
  const first = await related.getByRole('listitem').allTextContents()
  await related.getByRole('button', { name: text.nextPage, exact: true }).click()
  await expect(related.getByRole('listitem')).toHaveCount(2)
  const second = await related.getByRole('listitem').allTextContents()
  const contents = [...first, ...second].join('\n')
  for (const record of records) expect(contents).toContain(record.name)
  expect(contents).toContain(text.archived)
  expect(queries).toHaveLength(2)
  expect(queries[1]?.get('cursor')).toBeTruthy()
  await related.getByRole('button', { name: text.previousPage, exact: true }).click()
  await expect(related.getByRole('listitem')).toHaveCount(5)
  expect(queries).toHaveLength(3)
  const candidate = related.getByRole('link', { name: new RegExp(`^${text.relatedOpen}`) }).first()
  await candidate.click()
  const leave = page.getByRole('alertdialog')
  await expect(leave.getByRole('heading', { name: text.leaveTitle })).toBeVisible()
  await leave.getByRole('button', { name: text.stay }).click()
  await expect(dialog.getByRole('region', { name: text.relatedTitle }).getByRole('listitem')).toHaveCount(5)
  expect(queries).toHaveLength(3)
  expect(writes).toEqual([])
})

test('requires explicit acknowledgement for a whitespace-only name without an unfiltered read', async ({ page, application }) => {
  await provision(page, application)
  await seed(page, application.url, 'Unrelated Item')
  const writes = watchDomainWrites(page, ['/api/v1/items']), queries: URL[] = []
  page.on('request', (request) => { const url = new URL(request.url()); if (url.pathname === '/api/v1/items' && request.method() === 'GET') queries.push(url) })
  const exact = '\u0085 ', { dialog, related } = await openReview(page, exact)
  await expect(related.getByText(text.relatedWhitespace)).toBeVisible()
  const confirm = dialog.getByRole('button', { name: text.confirmCreate })
  await expect(confirm).toBeDisabled()
  expect(queries).toHaveLength(0)
  expect(writes).toEqual([])
  await related.getByRole('button', { name: text.relatedSkip }).click()
  await expect(related.getByText(text.relatedSkipped)).toBeVisible()
  await expect(confirm).toBeEnabled()
  await confirm.click()
  await expect(page.getByText(text.done, { exact: true })).toBeVisible()
  expect(writes).toHaveLength(1)
  expect(JSON.parse(writes[0]!.body!).name).toBe(exact)
})

test('withholds an inconsistent related Item page and requires an explicit fresh check', async ({ page, application }) => {
  await provision(page, application)
  for (let index = 0; index < 7; index++) await seed(page, application.url, `Matching ${index}`)
  let firstID = ''
  await page.route((url) => url.pathname === '/api/v1/items' && url.searchParams.get('q') === 'Matching', async (route) => {
    const response = await route.fetch(), body = await response.json()
    if (!new URL(route.request().url()).searchParams.has('cursor')) firstID = body.data[0].id
    else body.data[0].id = firstID
    await route.fulfill({ response, json: body })
  })
  const writes = watchDomainWrites(page, ['/api/v1/items'])
  const { dialog, related } = await openReview(page, 'Matching')
  await expect(related.getByRole('listitem')).toHaveCount(5)
  await related.getByRole('button', { name: text.nextPage }).click()
  await expect(related.getByText(text.invalidRead)).toBeVisible()
  await expect(related.getByRole('listitem')).toHaveCount(0)
  await expect(dialog.getByRole('button', { name: text.confirmCreate })).toBeDisabled()
  await related.getByRole('button', { name: text.relatedRetry }).click()
  await expect(related.getByRole('listitem')).toHaveCount(5)
  await expect(dialog.getByRole('button', { name: text.confirmCreate })).toBeEnabled()
  expect(writes).toEqual([])
})

test('does not describe an empty live continuation as an empty name search after real deletions', async ({ page, application }) => {
  await provision(page, application)
  const records: Item[] = []
  for (let index = 0; index < 7; index++) records.push(await seed(page, application.url, `Vanishing ${index}`))
  const writes = watchDomainWrites(page, ['/api/v1/items'])
  const { dialog, related } = await openReview(page, 'Vanishing')
  await expect(related.getByRole('listitem')).toHaveCount(5)
  const first = await page.request.get('/api/v1/items?q=Vanishing&limit=5')
  expect(first.status()).toBe(200)
  const firstPage = await first.json(), retained = new Set<string>(firstPage.data.map((item: Item) => item.id))
  expect(firstPage.next_cursor).toBeTruthy()
  const remaining = records.filter((item) => !retained.has(item.id))
  expect(remaining).toHaveLength(2)
  for (const item of remaining) {
    const response = await page.request.delete(`/api/v1/items/${item.id}`, { headers: await writeContext(page, application.url, item.revision) })
    expect(response.status()).toBe(200)
  }
  await related.getByRole('button', { name: text.nextPage }).click()
  await expect(related.getByText(text.relatedEmptyPage)).toBeVisible()
  await expect(related.getByText(text.relatedEmpty, { exact: true })).toHaveCount(0)
  await expect(dialog.getByRole('button', { name: text.confirmCreate })).toBeEnabled()
  expect(writes).toEqual([])
})

test('requires an explicit bypass when the real name-candidate request fails', async ({ page, application }) => {
  await provision(page, application)
  let searches = 0
  await page.route((url) => url.pathname === '/api/v1/items' && url.searchParams.get('q') === 'Uncertain lookup', async (route) => {
    searches++
    await route.fulfill({ status: 503, contentType: 'application/json', body: '{"error":{"code":"SERVICE_UNAVAILABLE","request_id":"related-read-failure"}}' })
  })
  const writes = watchDomainWrites(page, ['/api/v1/items'])
  const { dialog, related } = await openReview(page, 'Uncertain lookup')
  const confirm = dialog.getByRole('button', { name: text.confirmCreate })
  await expect(related.getByRole('button', { name: text.relatedSkip })).toBeVisible()
  await expect(related.getByText(text.relatedEmpty)).toHaveCount(0)
  await expect(confirm).toBeDisabled()
  expect(writes).toEqual([])
  await related.getByRole('button', { name: text.relatedSkip }).click()
  await expect(related.getByText(text.relatedSkipped)).toBeVisible()
  await confirm.click()
  await expect(page.getByText(text.done, { exact: true })).toBeVisible()
  expect(searches).toBe(1)
  expect(writes).toHaveLength(1)
})

test('reconciles a committed Item with its original key after the related check later fails', async ({ page, application }) => {
  await provision(page, application)
  const name = 'Item with uncertain name check'
  let searches = 0, posts = 0
  await page.route((url) => url.pathname === '/api/v1/items', async (route) => {
    const request = route.request(), url = new URL(request.url())
    if (request.method() === 'GET' && url.searchParams.get('q') === name) {
      searches++
      if (searches === 2) { await route.fulfill({ status: 503, contentType: 'application/json', body: '{"error":{"code":"SERVICE_UNAVAILABLE","request_id":"related-retry-failure"}}' }); return }
    } else if (request.method() === 'POST') {
      posts++
      if (posts === 1) {
        const committed = await route.fetch()
        expect(committed.status()).toBe(200)
        await route.fulfill({ status: 503, contentType: 'application/json', body: '{"error":{"code":"SERVICE_UNAVAILABLE","request_id":"lost-item-response"}}' })
        return
      }
    }
    await route.continue()
  })
  const writes = watchDomainWrites(page, ['/api/v1/items'])
  const { dialog, related } = await openReview(page, name)
  await expect(related.getByText(text.relatedEmpty)).toBeVisible()
  await dialog.getByRole('button', { name: text.confirmCreate }).click()
  await expect(dialog.getByRole('heading', { name: text.uncertain })).toBeVisible()
  expect(searches).toBe(1)
  await related.getByRole('button', { name: text.relatedRetry }).click()
  await expect(related.getByRole('button', { name: text.relatedSkip })).toBeVisible()
  const retry = dialog.getByRole('button', { name: text.retryOriginal })
  await expect(retry).toBeEnabled()
  await retry.click()
  await expect(page.getByText(text.done, { exact: true })).toBeVisible()
  expect(writes).toHaveLength(2)
  expect(writes[0]?.key).toBeTruthy()
  expect(writes[1]?.key).toBe(writes[0]?.key)
  expect(writes[1]?.body).toBe(writes[0]?.body)
  const actual = await page.request.get(`/api/v1/items?q=${encodeURIComponent(name)}`)
  expect(actual.status()).toBe(200)
  expect((await actual.json()).data).toEqual([expect.objectContaining({ name })])
  expect(searches).toBe(2)
})

for (const transport of ['storage', 'broadcast'] as const) test(`purges ordinary Item review on cross-tab logout using ${transport} only`, async ({ page, context, application }) => {
  await page.addInitScript((selected) => {
    if (selected === 'storage') Object.defineProperty(window, 'BroadcastChannel', { configurable: true, value: undefined })
    else window.addEventListener = new Proxy(window.addEventListener, { apply(target, receiver, args: unknown[]) {
      return args[0] === 'storage' ? undefined : Reflect.apply(target, receiver, args)
    } })
  }, transport)
  await provision(page, application)
  await seed(page, application.url, 'Private existing Item')
  const writes = watchDomainWrites(page, ['/api/v1/items'])
  const { dialog, related } = await openReview(page, 'Private')
  await expect(related.getByRole('listitem')).toContainText(['Private existing Item'])
  const other = await context.newPage()
  try {
    await other.goto('/settings/account')
    await other.getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await other.getByRole('alertdialog').getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await expect(page.getByText(messages.owned.sessionClearedHelp)).toBeVisible()
    await expect(page.getByText(messages.owned.sessionClearedUncertain)).toBeVisible()
    await expect(dialog).toHaveCount(0)
    await expect(page.locator('body')).not.toContainText('Private existing Item')
    await expect(page.locator('body')).not.toContainText('Private')
    await page.getByRole('link', { name: messages.scan.signIn }).click()
    await expect(page).toHaveURL(/\/login$/)
    expect(JSON.stringify(await page.evaluate(() => history.state))).not.toContain('Private')
    expect(writes).toEqual([])
  } finally { await other.close() }
})

test('discards a late actual name-candidate response after cross-tab logout', async ({ page, context, application }) => {
  await provision(page, application)
  await seed(page, application.url, 'Private delayed Item')
  let release!: () => void, received!: () => void, delivered!: () => void
  const held = new Promise<void>((resolve) => { release = resolve })
  const seen = new Promise<void>((resolve) => { received = resolve })
  const finished = new Promise<void>((resolve) => { delivered = resolve })
  await page.route((url) => url.pathname === '/api/v1/items' && url.searchParams.get('q') === 'Private', async (route) => {
    const response = await route.fetch()
    expect(response.status()).toBe(200)
    received()
    await held
    try { await route.fulfill({ response }) } catch { /* Logout can abort the pending browser read. */ }
    finally { delivered() }
  })
  const writes = watchDomainWrites(page, ['/api/v1/items'])
  const { dialog } = await openReview(page, 'Private')
  await seen
  const other = await context.newPage()
  try {
    await other.goto('/settings/account')
    await other.getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await other.getByRole('alertdialog').getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await expect(page.getByText(messages.owned.sessionClearedHelp)).toBeVisible()
    await expect(dialog).toHaveCount(0)
    release()
    await finished
    await expect(page.locator('body')).not.toContainText('Private delayed Item')
    await expect(page.locator('body')).not.toContainText('Private')
    expect(writes).toEqual([])
  } finally { release(); await other.close() }
})

test('clears a committed ordinary Item with a held response on cross-tab logout and requires manual reconciliation', async ({ page, context, application }) => {
  const password = await provision(page, application), name = 'Private committed ordinary Item'
  const writes = watchDomainWrites(page, ['/api/v1/items'])
  let release!: () => void, committed!: () => void, delivered!: () => void
  const held = new Promise<void>((resolve) => { release = resolve })
  const received = new Promise<void>((resolve) => { committed = resolve })
  const finished = new Promise<void>((resolve) => { delivered = resolve })
  await page.route((url) => url.pathname === '/api/v1/items', async (route) => {
    if (route.request().method() !== 'POST') { await route.continue(); return }
    const response = await route.fetch()
    expect(response.status()).toBe(200)
    committed()
    await held
    try { await route.fulfill({ response }) } catch { /* Logout can abort the pending browser write. */ }
    finally { delivered() }
  })
  const { dialog, related } = await openReview(page, name)
  const other = await context.newPage()
  try {
    await expect(related.getByText(text.relatedEmpty)).toBeVisible()
    await dialog.getByRole('button', { name: text.confirmCreate }).click()
    await received
    await other.goto('/settings/account')
    await other.getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await other.getByRole('alertdialog').getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await expect(page.getByText(messages.owned.sessionClearedUncertain)).toBeVisible()
    await expect(dialog).toHaveCount(0)
    await expect(page.locator('body')).not.toContainText(name)
    release()
    await finished
    await expect(page).toHaveURL(/\/items\/new$/)
    expect(writes).toHaveLength(1)
    expect((await page.request.post('/api/v1/auth/login', { headers: { Origin: application.url }, data: { username: 'owner', password } })).status()).toBe(200)
    const actual = await page.request.get(`/api/v1/items?q=${encodeURIComponent(name)}`)
    expect(actual.status()).toBe(200)
    expect((await actual.json()).data).toEqual([expect.objectContaining({ name })])
    expect(writes).toHaveLength(1)
  } finally { release(); await other.close() }
})

test('requires a fresh name check after offline and never retries or writes in the background', async ({ page, context, application }) => {
  await provision(page, application)
  const writes = watchDomainWrites(page, ['/api/v1/items']), queries: URL[] = []
  page.on('request', (request) => { const url = new URL(request.url()); if (url.pathname === '/api/v1/items' && request.method() === 'GET' && url.searchParams.has('q')) queries.push(url) })
  const { dialog, related } = await openReview(page, 'Offline name review')
  await expect(related.getByText(text.relatedEmpty)).toBeVisible()
  const confirm = dialog.getByRole('button', { name: text.confirmCreate })
  await expect(confirm).toBeEnabled()
  expect(queries).toHaveLength(1)
  await context.setOffline(true)
  try { await expect(confirm).toBeDisabled() } finally { await context.setOffline(false) }
  await expect(related.getByText(text.relatedStale)).toBeVisible()
  await expect(confirm).toBeDisabled()
  expect(queries).toHaveLength(1)
  expect(writes).toEqual([])
  await related.getByRole('button', { name: text.relatedRetry }).click()
  await expect(related.getByText(text.relatedEmpty)).toBeVisible()
  await expect(confirm).toBeEnabled()
  expect(queries).toHaveLength(2)
  expect(writes).toEqual([])
})

test('keeps related Item review accessible at 320px and 200% text in both themes', async ({ page, application }) => {
  await provision(page, application)
  for (let index = 0; index < 6; index++) await seed(page, application.url, `候選${index} ${'長名稱'.repeat(80)}`)
  await page.setViewportSize({ width: 320, height: 568 })
  await page.goto('/items/new')
  await page.locator('html').evaluate((element) => { element.style.fontSize = '200%' })
  await input(page).fill('長名稱')
  await page.getByRole('button', { name: text.review }).click()
  const dialog = page.getByRole('alertdialog'), related = dialog.getByRole('region', { name: text.relatedTitle })
  await expect(related.getByRole('listitem')).toHaveCount(5)
  await expect(dialog.getByRole('heading', { name: text.reviewTitle })).toBeFocused()
  for (const colorScheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme }); await expect(page.locator('html')).toHaveAttribute('data-theme', colorScheme)
    expect(await page.evaluate(() => innerWidth)).toBe(320)
    await noOverflow(page)
    expect(await dialog.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)
    expect(await dialog.getByRole('button', { name: text.confirmCreate }).evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)
    expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze()).violations).toEqual([])
  }
  await related.getByRole('button', { name: text.nextPage }).click()
  await expect(related.getByRole('listitem')).toHaveCount(1)
  await noOverflow(page)
  expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze()).violations).toEqual([])
})
