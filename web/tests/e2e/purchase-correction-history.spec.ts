import AxeBuilder from '@axe-core/playwright'
import type { Page } from '@playwright/test'
import type { components } from '../../src/lib/api/schema.d.ts'
import { messages } from '../../src/lib/messages.ts'
import { test, expect } from './auth-fixture.ts'
import { noOverflow, provision, watchDomainWrites, writeContext } from './domain-helpers.ts'

const text = messages.purchaseCorrectionHistory, purchaseText = messages.purchases
const input = (page: Page, label: string) => page.getByLabel(new RegExp(`^${label}`))
type Item = components['schemas']['Item']
type Purchase = components['schemas']['Purchase']
type Line = components['schemas']['PurchaseLine']
type Correction = components['schemas']['PurchaseLineCorrection']
test.use({ trace: 'off', screenshot: 'off', video: 'off' })

async function seed(page: Page, origin: string, name = '採購財務原物品') {
  const itemResponse = await page.request.post('/api/v1/items', { headers: await writeContext(page, origin), data: { name, tracking_mode: 'quantity', unit_code: 'kg', base_unit: 'g', scale_factor: 1000 } }); expect(itemResponse.status()).toBe(200); const item: Item = await itemResponse.json()
  const purchaseResponse = await page.request.post('/api/v1/purchases', { headers: await writeContext(page, origin), data: { merchant_name: '財務更正商家', purchased_at: '2026-11-07T10:00:00.123456789+08:00', currency: 'JPY', receipt_note: '原始採購' } }); expect(purchaseResponse.status()).toBe(200); const purchase: Purchase = await purchaseResponse.json()
  const lineResponse = await page.request.post(`/api/v1/purchases/${purchase.id}/lines`, { headers: await writeContext(page, origin, purchase.revision), data: { item_id: item.id, item_revision: item.revision, item_unit_id: null, quantity_base: 1500, paid_minor: 0, note: '原始零元分攤' } }); expect(lineResponse.status()).toBe(200); const line: Line = await lineResponse.json()
  return { item, purchase, line }
}
async function current(page: Page, purchaseID: string, itemID: string) {
  const purchaseResponse = await page.request.get(`/api/v1/purchases/${purchaseID}`), itemResponse = await page.request.get(`/api/v1/items/${itemID}`)
  expect(purchaseResponse.status()).toBe(200); expect(itemResponse.status()).toBe(200)
  return { purchase: await purchaseResponse.json() as Purchase, item: await itemResponse.json() as Item }
}
async function correct(page: Page, origin: string, purchaseID: string, line: Line, action: 'void' | 'replace', reason: string, paid = 250) {
  const source = await current(page, purchaseID, line.item_id)
  const response = await page.request.post(`/api/v1/purchases/${purchaseID}/line-corrections`, { headers: await writeContext(page, origin, source.purchase.revision), data: {
    line_id: line.id, line_revision: line.revision, item_revision: source.item.revision, action, reason,
    replacement: action === 'void' ? null : { item_id: source.item.id, item_revision: source.item.revision, item_unit_id: null, quantity_base: 2500, paid_minor: paid, note: '替代分攤' },
  } })
  expect(response.status()).toBe(200); return response.json() as Promise<Correction>
}
async function accessible(page: Page) { expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze()).violations).toEqual([]); await noOverflow(page) }

test('reads actual linked replacement and void history without claiming a zero or physical receipt', async ({ page, application }) => {
  await provision(page, application); const { item, purchase, line } = await seed(page, application.url)
  const replaced = await correct(page, application.url, purchase.id, line, 'replace', '原始分攤需更正'), voided = await correct(page, application.url, purchase.id, replaced.after!, 'void', '作廢替代分攤')
  const writes = watchDomainWrites(page, ['/api/v1/purchases', '/api/v1/inventory', '/api/v1/stock', '/api/v1/units'])
  await page.goto(`/purchases/${purchase.id}`); await page.getByRole('link', { name: text.title, exact: true }).click()
  await expect(page.getByRole('heading', { name: text.title, exact: true })).toBeVisible(); await expect(page.getByRole('heading', { name: purchaseText.emptyLines, exact: true })).toBeVisible()
  await expect(page.getByText(`${text.historyCount}2`, { exact: true })).toBeVisible(); await expect(page.getByText(text.noHistory, { exact: true })).toHaveCount(0)
  const replaceEntry = page.locator('details').filter({ has: page.locator('summary', { hasText: text.replace }) }), voidEntry = page.locator('details').filter({ has: page.locator('summary', { hasText: text.void }) })
  await replaceEntry.locator('summary').click(); await expect(replaceEntry.getByText('0 JPY', { exact: true })).toBeVisible(); await expect(replaceEntry.getByText('250 JPY', { exact: true })).toBeVisible()
  await expect(replaceEntry.getByText(JSON.stringify(replaced.before.id), { exact: true })).toBeVisible(); await expect(replaceEntry.getByText(JSON.stringify(replaced.after!.id), { exact: true })).toBeVisible()
  await voidEntry.locator('summary').click(); await expect(voidEntry.getByText(text.voided, { exact: true })).toBeVisible(); await expect(voidEntry.getByText(JSON.stringify(voided.before.id), { exact: true })).toBeVisible()
  await accessible(page); expect(writes).toHaveLength(0); expect(await (await page.request.get(`/api/v1/inventory?item_id=${item.id}`)).json()).toEqual({ data: [], next_cursor: null })
})

test('shows actual no-correction history and an active zero allocation separately', async ({ page, application }) => {
  await provision(page, application); const { purchase } = await seed(page, application.url), writes = watchDomainWrites(page, ['/api/v1/purchases'])
  await page.goto(`/purchases/${purchase.id}/line-corrections`); await expect(page.getByText(text.noHistory, { exact: true })).toBeVisible(); await expect(page.getByText(`${text.historyCount}0`, { exact: true })).toBeVisible()
  await expect(page.getByText(`${purchaseText.lineCount}1`, { exact: true })).toBeVisible(); await expect(page.getByText('0 JPY', { exact: true })).toBeVisible(); expect(writes).toHaveLength(0); await accessible(page)
})

test('retains the server currency lock when all current Lines were financially voided', async ({ page, application }) => {
  await provision(page, application); const { purchase, item, line } = await seed(page, application.url); await correct(page, application.url, purchase.id, line, 'void', 'Retain original financial history')
  const writes = watchDomainWrites(page, ['/api/v1/purchases']); await page.goto(`/purchases/${purchase.id}/edit`)
  await input(page, purchaseText.fields.currency).fill('USD'); await page.getByRole('button', { name: purchaseText.review, exact: true }).click()
  const dialog = page.getByRole('alertdialog'); await dialog.getByRole('button', { name: purchaseText.confirmSave, exact: true }).click(); await expect(dialog.getByText(purchaseText.currencyLocked, { exact: true })).toBeVisible()
  expect((await current(page, purchase.id, item.id)).purchase.currency).toBe('JPY'); expect(writes).toHaveLength(1)
})

test('withholds a mixed real history interval and recovers only on a fresh read', async ({ page, application }) => {
  await provision(page, application); const { purchase, item } = await seed(page, application.url), source = await current(page, purchase.id, item.id); let changed = false
  await page.route((url) => url.pathname === `/api/v1/purchases/${purchase.id}/line-corrections`, async (route) => {
    if (changed || route.request().method() !== 'GET') { await route.continue(); return }
    const response = await route.fetch(); changed = true
    const update = await page.request.patch(`/api/v1/purchases/${purchase.id}`, { headers: await writeContext(page, application.url, source.purchase.revision), data: { receipt_note: '並行變更後的備註' } }); expect(update.status()).toBe(200)
    await route.fulfill({ response })
  })
  const writes = watchDomainWrites(page, ['/api/v1/purchases'])
  await page.goto(`/purchases/${purchase.id}/line-corrections`); await expect(page.getByText(purchaseText.invalidRead, { exact: true })).toBeVisible()
  await expect(page.getByText(text.noHistory, { exact: true })).toHaveCount(0); await expect(page.getByText(`${text.historyCount}0`, { exact: true })).toHaveCount(0)
  await page.getByRole('button', { name: purchaseText.refresh, exact: true }).click(); await expect(page.getByText(text.noHistory, { exact: true })).toBeVisible(); await expect(page.getByText(JSON.stringify('並行變更後的備註'), { exact: true })).toBeVisible(); expect(writes).toHaveLength(0)
})

test('returns through genuine login with only validated purchase read scope', async ({ page, application }) => {
  const password = await provision(page, application); const { purchase } = await seed(page, application.url), writes = watchDomainWrites(page, ['/api/v1/purchases'])
  await page.goto('/purchases?currency=JPY&limit=1'); await page.getByRole('link', { name: /財務更正商家/ }).click(); await page.getByRole('link', { name: text.title, exact: true }).click(); await expect(page.getByText(text.noHistory, { exact: true })).toBeVisible()
  expect((await page.request.post('/api/v1/auth/logout', { headers: await writeContext(page, application.url) })).status()).toBe(200)
  await page.getByRole('button', { name: purchaseText.refresh, exact: true }).click(); await expect(page.getByRole('heading', { name: messages.auth.loginTitle, exact: true })).toBeVisible()
  await input(page, messages.auth.username).fill('owner'); await input(page, messages.auth.password).fill(password); await page.getByRole('button', { name: messages.auth.login, exact: true }).click()
  await expect(page).toHaveURL(`${application.url}/purchases/${purchase.id}/line-corrections`); await expect(page.getByText(text.noHistory, { exact: true })).toBeVisible()
  const state = await page.evaluate(() => window.history.state.usr); expect(state).toMatchObject({ purchaseList: '/purchases?currency=JPY&limit=1' }); for (const key of ['reason', 'key', 'command', 'csrfToken']) expect(state).not.toHaveProperty(key); expect(writes).toHaveLength(0)
})

test('changes offscreen contained action colors atomically in both directions', async ({ page, application }) => {
  await provision(page, application); const { purchase, line } = await seed(page, application.url); await correct(page, application.url, purchase.id, line, 'replace', '保留正確對比')
  await page.goto(`/purchases/${purchase.id}/line-corrections`); await page.locator('html').evaluate((element) => { element.style.fontSize = '200%' })
  const action = page.getByRole('link', { name: new RegExp(`^${text.start}`) })
  await expect(action).toBeVisible(); await expect(action.locator('..')).toHaveCSS('content-visibility', 'auto')
  // A long transition makes any delayed offscreen interpolation observable without waiting.
  await action.evaluate((element) => { element.style.transitionDuration = '10s'; return getComputedStyle(element).color })
  for (const theme of ['dark', 'light'] as const) {
    await page.getByRole('button', { name: messages.theme.label, exact: true }).click(); await page.getByRole('menuitemradio', { name: messages.theme[theme], exact: true }).click(); await expect(page.getByRole('menu')).toHaveCount(0)
    const state = await action.evaluate((element) => ({
      actual: getComputedStyle(element).color, expected: getComputedStyle(document.body).color,
      transitions: element.getAnimations().filter((animation): animation is CSSTransition => animation instanceof CSSTransition).map((animation) => animation.transitionProperty),
    }))
    expect(state.actual).toBe(state.expected); expect(state.transitions).not.toContain('color'); expect(state.transitions).not.toContain('background-color')
  }
})

test('keeps long exact financial history accessible at 200 percent text in both themes', async ({ page, application }, info) => {
  await provision(page, application); const { purchase, line } = await seed(page, application.url, '長財務物品名稱'.repeat(25)); await correct(page, application.url, purchase.id, line, 'replace', '長財務更正原因 é 😀 '.repeat(150), Number.MAX_SAFE_INTEGER)
  const writes = watchDomainWrites(page, ['/api/v1/purchases']); await page.goto(`/purchases/${purchase.id}/line-corrections`); await page.locator('html').evaluate((element) => { element.style.fontSize = '200%' })
  for (const theme of ['light', 'dark'] as const) {
    await page.getByRole('button', { name: messages.theme.label, exact: true }).click(); await page.getByRole('menuitemradio', { name: messages.theme[theme], exact: true }).click(); await expect(page.getByRole('menu')).toHaveCount(0)
    const heading = page.getByRole('heading', { name: text.title, exact: true }); await heading.scrollIntoViewIfNeeded()
    expect(await heading.evaluate((element) => { const range = document.createRange(); range.selectNodeContents(element); return range.getClientRects().length })).toBeLessThanOrEqual(2)
    await page.screenshot({ path: info.outputPath(`purchase-history-heading-${theme}.png`) }); const summary = page.locator('details summary'); await summary.focus(); await expect(summary).toBeFocused()
    if (await summary.locator('..').getAttribute('open') === null) await page.keyboard.press('Enter')
    await expect(page.getByRole('heading', { name: text.after, exact: true })).toBeVisible(); await accessible(page)
    const amount = page.locator('details').getByText(`${Number.MAX_SAFE_INTEGER} JPY`, { exact: true }); await amount.scrollIntoViewIfNeeded()
    expect(await amount.evaluate((element) => { const range = document.createRange(); range.setStart(element.firstChild!, 0); range.setEnd(element.firstChild!, String(Number.MAX_SAFE_INTEGER).length); return range.getClientRects().length })).toBeLessThanOrEqual(2)
    await page.screenshot({ path: info.outputPath(`purchase-history-detail-${theme}.png`) })
  }
  expect(writes).toHaveLength(0)
})
