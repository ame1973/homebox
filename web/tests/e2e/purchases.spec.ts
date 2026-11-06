import AxeBuilder from '@axe-core/playwright'
import { DatabaseSync } from 'node:sqlite'
import type { Page } from '@playwright/test'
import type { components } from '../../src/lib/api/schema.d.ts'
import { messages } from '../../src/lib/messages.ts'
import { test, expect } from './auth-fixture.ts'
import { noOverflow, provision, watchDomainWrites, writeContext } from './domain-helpers.ts'

const text = messages.purchases, cat = messages.catalog
const input = (page: Page, label: string) => page.getByLabel(new RegExp(`^${label}`))
const select = (page: Page, label: string) => page.getByRole('combobox', { name: new RegExp(`^${label}`) })
const watch = (page: Page) => watchDomainWrites(page, ['/api/v1/purchases'])
type Item = components['schemas']['Item']
type Purchase = components['schemas']['Purchase']
type Unit = components['schemas']['Unit']
test.use({ trace: 'off', screenshot: 'off', video: 'off' })
async function seedItem(page: Page, origin: string, quantity = true, category_id?: string): Promise<Item> {
  const response = await page.request.post('/api/v1/items', { headers: await writeContext(page, origin), data: { name: quantity ? '採購數量物品' : '採購個體物品', ...(category_id ? { category_id } : {}), ...(quantity ? { tracking_mode: 'quantity', unit_code: 'kg', base_unit: 'g', scale_factor: 1000 } : {}) } })
  expect(response.status()).toBe(200); return response.json()
}
async function seedPurchase(page: Page, origin: string, merchant = '實際採購商家'): Promise<Purchase> {
  const response = await page.request.post('/api/v1/purchases', { headers: await writeContext(page, origin), data: { merchant_name: merchant, purchased_at: '2026-11-07T10:00:00.123456789+08:00', currency: 'JPY', receipt_note: '  原始採購備註 é  ' } })
  expect(response.status()).toBe(200); return response.json()
}
async function currentItem(page: Page, id: string): Promise<Item> { const response = await page.request.get(`/api/v1/items/${id}`); expect(response.status()).toBe(200); return response.json() }
async function currentPurchase(page: Page, id: string): Promise<Purchase> { const response = await page.request.get(`/api/v1/purchases/${id}`); expect(response.status()).toBe(200); return response.json() }
async function seedLine(page: Page, origin: string, purchase: Purchase, item: Item, paid_minor = 0) {
  const response = await page.request.post(`/api/v1/purchases/${purchase.id}/lines`, { headers: await writeContext(page, origin, purchase.revision), data: { item_id: item.id, item_revision: item.revision, item_unit_id: null, quantity_base: 1500, paid_minor } })
  expect(response.status()).toBe(200); return response.json() as Promise<components['schemas']['PurchaseLine']>
}
async function fillLine(page: Page, unitID?: string) {
  await select(page, text.fields.allocation).selectOption(unitID ? 'unit' : 'none')
  if (unitID) await select(page, text.fields.item_unit_id).selectOption(unitID)
  await input(page, text.fields.quantity_base).fill(unitID ? '1' : '1500'); await input(page, text.fields.paid_minor).fill('123'); await input(page, text.fields.note).fill('  整筆分攤 é\n保留空白  ')
}
async function confirm(page: Page, label: string) {
  const dialog = page.getByRole('alertdialog'), heading = dialog.getByRole('heading', { name: text.reviewTitle, exact: true })
  await expect(heading).toBeFocused(); await expect(heading).toBeInViewport(); await dialog.getByRole('button', { name: label, exact: true }).click()
  await expect(page.getByText(text.done, { exact: true })).toBeVisible(); await expect(dialog).toHaveCount(0)
}
async function accessible(page: Page) { expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze()).violations).toEqual([]); await noOverflow(page) }

test('creates and descriptively corrects a genuine header with exact instants and no invented totals', async ({ page, application }) => {
  await provision(page, application); const writes = watch(page)
  await page.goto('/purchases'); await page.getByRole('link', { name: text.create, exact: true }).click()
  await expect(input(page, text.fields.purchased_at)).toHaveValue(''); await expect(input(page, text.fields.currency)).toHaveValue('')
  await page.getByRole('button', { name: text.review, exact: true }).click(); await expect(input(page, text.fields.merchant_name)).toBeFocused()
  await input(page, text.fields.merchant_name).fill('  實際商家 é 😀\n第二行  '); await input(page, text.fields.purchased_at).fill('2026-11-07T10:00:00.123456789+08:00'); await input(page, text.fields.currency).fill('JPY'); await input(page, text.fields.receipt_note).fill('  Exact receipt\n  ')
  await expect(input(page, text.fields.merchant_name)).toHaveValue('  實際商家 é 😀\n第二行  ')
  await page.getByRole('button', { name: text.review, exact: true }).click(); expect(writes).toHaveLength(0); await accessible(page); await confirm(page, text.confirmCreate)
  const id = new URL(page.url()).pathname.split('/').at(-1)!
  expect(await currentPurchase(page, id)).toMatchObject({ revision: 1, merchant_name: '  實際商家 é 😀\n第二行  ', purchased_at: '2026-11-07T02:00:00.123456789Z', currency: 'JPY', receipt_note: '  Exact receipt\n  ' })
  await page.getByRole('link', { name: text.edit, exact: true }).click(); await expect(input(page, text.fields.merchant_name)).toHaveValue('  實際商家 é 😀\n第二行  '); await input(page, text.fields.receipt_note).fill(''); await page.getByRole('button', { name: text.review, exact: true }).click(); await confirm(page, text.confirmSave)
  expect(writes).toHaveLength(2); expect(writes[0]?.proof).toBeUndefined(); expect(writes[1]?.proof).toBe('"1"'); expect(JSON.parse(writes[1]?.body ?? '{}')).toEqual({ receipt_note: '' })
  expect((await currentPurchase(page, id)).receipt_note).toBe(''); await accessible(page)
})

test('selects a real paged catalog Item and records an explicit whole financial allocation without receiving Stock', async ({ page, application }) => {
  await provision(page, application); const item = await seedItem(page, application.url), purchase = await seedPurchase(page, application.url), writes = watch(page)
  await page.goto(`/purchases/${purchase.id}`); await page.getByRole('link', { name: text.allocate, exact: true }).click()
  await expect(page.getByRole('heading', { name: text.selectItem, exact: true })).toBeVisible(); await expect(input(page, cat.search)).toBeVisible(); await page.getByRole('link', { name: text.chooseItem, exact: true }).click()
  await expect(select(page, text.fields.allocation)).toHaveValue(''); await expect(input(page, text.fields.quantity_base)).toHaveValue(''); await expect(input(page, text.fields.paid_minor)).toHaveValue('')
  await fillLine(page); await page.getByRole('button', { name: text.review, exact: true }).click(); expect(writes).toHaveLength(0); await expect(page.getByRole('alertdialog')).toContainText('"g"'); await confirm(page, text.confirmLine)
  const actual: components['schemas']['PurchaseLineList'] = await (await page.request.get(`/api/v1/purchases/${purchase.id}/lines`)).json()
  expect(actual.data).toHaveLength(1); expect(actual.data[0]).toMatchObject({ revision: 1, item_id: item.id, item_unit_id: null, quantity_base: 1500, paid_minor: 123 })
  expect(writes[0]?.proof).toBe('"1"'); expect(JSON.parse(writes[0]?.body ?? '{}')).toMatchObject({ item_revision: 1, paid_minor: 123, quantity_base: 1500 })
  expect(await (await page.request.get(`/api/v1/inventory?item_id=${item.id}`)).json()).toEqual({ data: [], next_cursor: null })
  await page.goto(`/items/${item.id}`); await page.getByRole('link', { name: text.itemPurchases, exact: true }).click(); await expect(page.getByText(`${text.associatedCount}1`, { exact: true })).toBeVisible(); await accessible(page)
})

test('requires an actual named Unit and explicit quantity one without changing physical Unit state', async ({ page, application }) => {
  await provision(page, application); const item = await seedItem(page, application.url, false), purchase = await seedPurchase(page, application.url)
  const created = await page.request.post(`/api/v1/items/${item.id}/units`, { headers: await writeContext(page, application.url, item.revision), data: { location_id: null, serial_number: '', notes: 'Retained physical Unit' } }); expect(created.status()).toBe(200); const unit: Unit = await created.json(), owner = await currentItem(page, item.id), writes = watch(page)
  await page.goto(`/purchases/${purchase.id}/allocate/${item.id}`); await select(page, text.fields.allocation).selectOption('unit'); await page.getByRole('button', { name: text.review, exact: true }).click(); await expect(select(page, text.fields.item_unit_id)).toBeFocused()
  await fillLine(page, unit.id); await input(page, text.fields.quantity_base).fill('2'); await page.getByRole('button', { name: text.review, exact: true }).click(); await expect(input(page, text.fields.quantity_base)).toBeFocused(); expect(writes).toHaveLength(0)
  await input(page, text.fields.quantity_base).fill('1'); await page.getByRole('button', { name: text.review, exact: true }).click(); await expect(page.getByRole('alertdialog')).toContainText(unit.asset_code); await confirm(page, text.confirmLine)
  expect(JSON.parse(writes[0]?.body ?? '{}')).toMatchObject({ item_revision: owner.revision, item_unit_id: unit.id, quantity_base: 1, paid_minor: 123 }); expect(writes[0]?.proof).toBe('"1"')
  expect(await (await page.request.get(`/api/v1/units/${unit.id}`)).json()).toEqual(unit)
})

test('keeps zero-paid history and blocks currency changes locally while allowing real descriptive correction', async ({ page, application }) => {
  await provision(page, application); const item = await seedItem(page, application.url), purchase = await seedPurchase(page, application.url), line = await seedLine(page, application.url, purchase, item), writes = watch(page)
  await page.goto(`/purchases/${purchase.id}/edit`); await input(page, text.fields.currency).fill('USD'); await page.getByRole('button', { name: text.review, exact: true }).click(); await expect(input(page, text.fields.currency)).toBeFocused(); await expect(input(page, text.fields.currency)).toHaveAttribute('aria-invalid', 'true'); expect(writes).toHaveLength(0)
  await input(page, text.fields.currency).fill('JPY'); await input(page, text.fields.receipt_note).fill('Actual audited correction'); await page.getByRole('button', { name: text.review, exact: true }).click(); await confirm(page, text.confirmSave)
  expect(JSON.parse(writes[0]?.body ?? '{}')).toEqual({ receipt_note: 'Actual audited correction' }); expect(writes[0]?.proof).toBe('"2"')
  expect((await (await page.request.get(`/api/v1/purchases/${purchase.id}/lines`)).json()).data).toEqual([line]); expect((await currentPurchase(page, purchase.id)).currency).toBe('JPY')
})

test('rejects a genuine stale Item proof and only adopts the complete current interpretation explicitly', async ({ page, application }) => {
  await provision(page, application); const item = await seedItem(page, application.url), purchase = await seedPurchase(page, application.url)
  await page.goto(`/purchases/${purchase.id}/allocate/${item.id}`); await fillLine(page); await page.getByRole('button', { name: text.review, exact: true }).click()
  expect((await page.request.patch(`/api/v1/items/${item.id}`, { headers: await writeContext(page, application.url, item.revision), data: { unit_code: 'L', base_unit: 'ml', scale_factor: 100 } })).status()).toBe(200)
  const writes = watch(page); await page.getByRole('alertdialog').getByRole('button', { name: text.confirmLine, exact: true }).click(); await expect(page.getByRole('alertdialog')).toContainText(messages.locations.conflict); expect(JSON.parse(writes[0]?.body ?? '{}')).toMatchObject({ item_revision: 1, quantity_base: 1500, paid_minor: 123 })
  await page.getByRole('alertdialog').getByRole('button', { name: text.cancel, exact: true }).click(); await expect(page.getByText(text.changed, { exact: true })).toBeVisible(); await expect(page.getByRole('button', { name: text.keepEdits, exact: true })).toBeDisabled(); await expect(input(page, text.fields.quantity_base)).toHaveValue('1500')
  await page.getByRole('checkbox', { name: text.acknowledge, exact: true }).check(); await page.getByRole('button', { name: text.keepEdits, exact: true }).click(); await expect(input(page, text.fields.quantity_base)).toHaveAccessibleName(/ml/)
  await page.getByRole('button', { name: text.review, exact: true }).click(); await confirm(page, text.confirmLine); expect(writes).toHaveLength(2); expect(JSON.parse(writes[1]?.body ?? '{}')).toMatchObject({ item_revision: 2, quantity_base: 1500, paid_minor: 123 }); expect(writes[1]?.key).not.toBe(writes[0]?.key)
})

test('requires explicit currency and whole measurement adoption without converting raw money or count drafts', async ({ page, application }) => {
  await provision(page, application); const item = await seedItem(page, application.url), purchase = await seedPurchase(page, application.url)
  await page.goto(`/purchases/${purchase.id}/allocate/${item.id}`); await fillLine(page)
  expect((await page.request.patch(`/api/v1/items/${item.id}`, { headers: await writeContext(page, application.url, item.revision), data: { unit_code: 'L', base_unit: 'ml', scale_factor: 100 } })).status()).toBe(200)
  expect((await page.request.patch(`/api/v1/purchases/${purchase.id}`, { headers: await writeContext(page, application.url, purchase.revision), data: { currency: 'USD' } })).status()).toBe(200)
  const writes = watch(page); await page.getByRole('button', { name: text.refresh, exact: true }).click(); await expect(page.getByText(text.interpretationChanged, { exact: true })).toBeVisible(); await expect(page.getByRole('button', { name: text.keepEdits, exact: true })).toBeDisabled()
  await page.getByRole('checkbox', { name: text.acknowledge, exact: true }).check(); await page.getByRole('button', { name: text.keepEdits, exact: true }).click(); await expect(input(page, text.fields.quantity_base)).toHaveValue('1500'); await expect(input(page, text.fields.paid_minor)).toHaveValue('123'); await expect(input(page, text.fields.paid_minor)).toHaveAccessibleName(/USD/)
  await page.getByRole('button', { name: text.review, exact: true }).click(); await confirm(page, text.confirmLine); expect(writes).toHaveLength(1); expect(writes[0]?.proof).toBe('"2"'); expect(JSON.parse(writes[0]?.body ?? '{}')).toMatchObject({ item_revision: 2, quantity_base: 1500, paid_minor: 123 })
})

test('retains a committed malformed allocation reply and only explicitly reconciles the original financial request after later changes', async ({ page, application }) => {
  await provision(page, application); const item = await seedItem(page, application.url), purchase = await seedPurchase(page, application.url), writes = watch(page); let attempts = 0
  await page.route((url) => url.pathname === `/api/v1/purchases/${purchase.id}/lines`, async (route) => { if (route.request().method() !== 'POST') { await route.continue(); return }; const response = await route.fetch(); expect(response.status()).toBe(200); if (++attempts === 1) await route.fulfill({ response, body: JSON.stringify({ ...await response.json(), paid_minor: 999 }) }); else await route.fulfill({ response }) })
  await page.goto(`/purchases/${purchase.id}/allocate/${item.id}`); await fillLine(page); await page.getByRole('button', { name: text.review, exact: true }).click(); await page.getByRole('alertdialog').getByRole('button', { name: text.confirmLine, exact: true }).click(); await expect(page.getByRole('alertdialog').getByRole('heading', { name: text.uncertain, exact: true })).toBeVisible(); await page.getByRole('alertdialog').getByRole('button', { name: text.cancel, exact: true }).click()
  const current = await currentPurchase(page, purchase.id)
  expect((await page.request.patch(`/api/v1/purchases/${purchase.id}`, { headers: await writeContext(page, application.url, current.revision), data: { receipt_note: 'Later authoritative description' } })).status()).toBe(200)
  await page.getByRole('button', { name: text.readCurrent, exact: true }).click(); await page.evaluate(() => window.dispatchEvent(new Event('online'))); expect(attempts).toBe(1); await expect(input(page, text.fields.paid_minor)).toBeDisabled(); await expect(input(page, text.fields.paid_minor)).toHaveValue('123')
  await page.getByRole('button', { name: text.openOriginal, exact: true }).click(); await page.getByRole('alertdialog').getByRole('button', { name: text.retryOriginal, exact: true }).click(); await expect(page.getByText(text.done, { exact: true })).toBeVisible()
  expect(writes).toHaveLength(2); expect(writes[1]).toEqual(writes[0]); expect(attempts).toBe(2); const actual = await (await page.request.get(`/api/v1/purchases/${purchase.id}/lines`)).json(); expect(actual.data).toHaveLength(1); expect(actual.data[0]).toMatchObject({ paid_minor: 123, quantity_base: 1500 }); expect((await currentPurchase(page, purchase.id)).receipt_note).toBe('Later authoritative description')
})

test('keeps a removed selected Unit visible in the draft until explicit repair instead of silently selecting null', async ({ page, application }) => {
  await provision(page, application); const item = await seedItem(page, application.url, false), purchase = await seedPurchase(page, application.url)
  const response = await page.request.post(`/api/v1/items/${item.id}/units`, { headers: await writeContext(page, application.url, item.revision), data: { location_id: null } }); expect(response.status()).toBe(200); const unit: Unit = await response.json()
  await page.goto(`/purchases/${purchase.id}/allocate/${item.id}`); await fillLine(page, unit.id)
  expect((await page.request.delete(`/api/v1/units/${unit.id}`, { headers: await writeContext(page, application.url, unit.revision) })).status()).toBe(200)
  const writes = watch(page); await page.getByRole('button', { name: text.refresh, exact: true }).click(); await expect(page.getByText(text.changed, { exact: true })).toBeVisible(); await page.getByRole('button', { name: text.keepEdits, exact: true }).click(); await expect(select(page, text.fields.item_unit_id)).toHaveValue(unit.id)
  await page.getByRole('button', { name: text.review, exact: true }).click(); await expect(select(page, text.fields.item_unit_id)).toBeFocused(); expect(writes).toHaveLength(0)
  await select(page, text.fields.allocation).selectOption('none'); await page.getByRole('button', { name: text.review, exact: true }).click(); await confirm(page, text.confirmLine); expect(JSON.parse(writes[0]?.body ?? '{}')).toMatchObject({ item_unit_id: null, quantity_base: 1, paid_minor: 123 })
})

test('keeps dirty financial input and original review across Back cancellation and first Escape without sending', async ({ page, application }) => {
  await provision(page, application); const purchase = await seedPurchase(page, application.url), writes = watch(page)
  await page.goto(`/purchases/${purchase.id}`); await page.getByRole('link', { name: text.edit, exact: true }).click(); await input(page, text.fields.receipt_note).fill('Unsaved financial context'); await page.getByRole('button', { name: text.review, exact: true }).click(); await page.goBack()
  await expect(page.getByRole('alertdialog').getByRole('heading', { name: text.leaveTitle, exact: true })).toBeVisible(); await page.getByRole('alertdialog').getByRole('button', { name: text.stay, exact: true }).click(); await expect(page.getByRole('alertdialog').getByRole('heading', { name: text.reviewTitle, exact: true })).toBeVisible(); await page.keyboard.press('Escape'); await expect(page.getByRole('alertdialog')).toHaveCount(0); await expect(input(page, text.fields.receipt_note)).toHaveValue('Unsaved financial context'); expect(writes).toHaveLength(0)
  await page.goBack(); await page.getByRole('alertdialog').getByRole('button', { name: text.leave, exact: true }).click(); await expect(page.getByRole('heading', { name: text.detail, exact: true })).toBeVisible(); expect(writes).toHaveLength(0)
})

test('preserves only safe catalog and purchase return scopes through real session login, never replaying an offline draft', async ({ page, application }) => {
  const password = await provision(page, application); const item = await seedItem(page, application.url), purchase = await seedPurchase(page, application.url), writes = watch(page)
  await page.goto('/items?status=active&limit=1'); await page.getByRole('link', { name: new RegExp(item.name) }).click(); await page.getByRole('link', { name: text.itemPurchases, exact: true }).click(); await page.getByRole('link', { name: text.all, exact: true }).click(); await page.getByRole('link', { name: JSON.stringify(purchase.merchant_name), exact: true }).click(); await page.getByRole('link', { name: text.allocate, exact: true }).click(); await fillLine(page)
  await page.context().setOffline(true); await expect(page.getByText(text.offline, { exact: true })).toBeVisible(); await expect(page.getByRole('button', { name: text.review, exact: true })).toBeDisabled(); await input(page, text.fields.paid_minor).fill('0'); await page.context().setOffline(false); await expect(page.getByRole('button', { name: text.review, exact: true })).toBeEnabled(); expect(writes).toHaveLength(0)
  expect((await page.request.post('/api/v1/auth/logout', { headers: await writeContext(page, application.url) })).status()).toBe(200); await page.getByRole('button', { name: text.refresh, exact: true }).click(); await expect(page.getByRole('heading', { name: messages.auth.loginTitle, exact: true })).toBeVisible()
  await input(page, messages.auth.username).fill('owner'); await input(page, messages.auth.password).fill(password); await page.getByRole('button', { name: messages.auth.login, exact: true }).click(); await expect(page).toHaveURL(`${application.url}/purchases/${purchase.id}/allocate/${item.id}`); await expect(input(page, text.fields.quantity_base)).toHaveValue(''); await expect(input(page, text.fields.paid_minor)).toHaveValue('')
  const state = await page.evaluate(() => window.history.state.usr); expect(state).not.toHaveProperty('paid_minor'); expect(state).not.toHaveProperty('key'); expect(state).toMatchObject({ purchaseList: '/purchases', catalogList: '/items?status=active&limit=1' })
  await page.getByRole('link', { name: cat.back, exact: true }).click(); expect(new URL(page.url()).search).toBe('?status=active&limit=1'); expect(writes).toHaveLength(0)
})

test('forwards all real filters with explicit stable-ID pagination and retains the scoped list return', async ({ page, application }) => {
  await provision(page, application)
  const categoryReply = await page.request.post('/api/v1/categories', { headers: await writeContext(page, application.url), data: { name: '採購分類' } }); expect(categoryReply.status()).toBe(200); const category: components['schemas']['Category'] = await categoryReply.json()
  const locationReply = await page.request.post('/api/v1/locations', { headers: await writeContext(page, application.url), data: { name: '採購位置', code: 'PURCHASE-FILTER' } }); expect(locationReply.status()).toBe(200); const location: components['schemas']['Location'] = await locationReply.json()
  const item = await seedItem(page, application.url, true, category.id), first = await seedPurchase(page, application.url, '實際採購商家\n完整名稱'), second = await seedPurchase(page, application.url, first.merchant_name)
  expect((await page.request.post('/api/v1/stock/receive', { headers: await writeContext(page, application.url, item.revision), data: { item_id: item.id, target_location_id: location.id, quantity_base: 1 } })).status()).toBe(200)
  await seedLine(page, application.url, first, await currentItem(page, item.id)); await seedLine(page, application.url, second, await currentItem(page, item.id))
  await page.goto('/purchases'); await page.getByText(cat.advanced, { exact: true }).click(); await input(page, cat.from).fill('2026-11-07'); await input(page, cat.to).fill('2026-11-07'); await input(page, cat.currency).fill('JPY'); await input(page, cat.merchant).fill(first.merchant_name); await input(page, cat.pageSize).fill('1'); await page.getByRole('combobox', { name: cat.fields.category_id, exact: true }).selectOption(category.id); await select(page, cat.location).selectOption(location.id); await select(page, cat.scope).selectOption('direct'); await select(page, cat.fields.status).selectOption('active'); await page.getByRole('button', { name: cat.apply, exact: true }).click()
  const query = new URL(page.url()).searchParams
  expect(Object.fromEntries(query)).toMatchObject({ from: '2026-11-07', to: '2026-11-07', currency: 'JPY', merchant: first.merchant_name, category_id: category.id, location_id: location.id, scope: 'direct', status: 'active', limit: '1' })
  const row = page.getByRole('main').getByRole('link', { name: JSON.stringify(first.merchant_name), exact: true }); await expect(row).toHaveCount(1); const firstPath = await row.getAttribute('href'); await page.getByRole('button', { name: cat.nextPage, exact: true }).click(); await expect(row).not.toHaveAttribute('href', firstPath!); await expect(page.getByRole('button', { name: cat.nextPage, exact: true })).toBeDisabled(); await page.getByRole('button', { name: cat.previousPage, exact: true }).click(); await expect(row).toHaveAttribute('href', firstPath!)
  const listReturn = new URL(page.url()).pathname + new URL(page.url()).search; await row.click(); await page.getByRole('link', { name: text.back, exact: true }).click(); expect(new URL(page.url()).pathname + new URL(page.url()).search).toBe(listReturn); await accessible(page)
})

test('keeps long allocation reviews keyboard reachable at 200 percent text in paired light and dark themes', async ({ page, application }, info) => {
  await provision(page, application); const item = await seedItem(page, application.url), purchase = await seedPurchase(page, application.url, '長商家名稱'.repeat(45))
  await page.goto(`/purchases/${purchase.id}/allocate/${item.id}`); await fillLine(page); await input(page, text.fields.note).fill('長分攤備註 é 😀 '.repeat(180)); await page.locator('html').evaluate((element) => { element.style.fontSize = '200%' })
  for (const theme of ['light', 'dark'] as const) {
    await page.getByRole('button', { name: messages.theme.label, exact: true }).click(); await page.getByRole('menuitemradio', { name: messages.theme[theme], exact: true }).click(); await expect(page.getByRole('menu')).toHaveCount(0); await expect(page.locator('html')).toHaveAttribute('data-theme', theme); await page.getByRole('button', { name: text.review, exact: true }).click()
    const dialog = page.getByRole('alertdialog'), heading = dialog.getByRole('heading', { name: text.reviewTitle, exact: true }), confirm = dialog.getByRole('button', { name: text.confirmLine, exact: true })
    await expect(heading).toBeFocused(); await expect(heading).toBeInViewport(); await accessible(page); await page.screenshot({ path: info.outputPath(`purchase-review-${theme}.png`) })
    for (let count = 0; count < 12 && !await confirm.evaluate((element) => element === document.activeElement); count++) await page.keyboard.press('Tab')
    await expect(confirm).toBeFocused(); await expect(confirm).toBeInViewport(); expect(await confirm.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true); await page.screenshot({ path: info.outputPath(`purchase-confirm-${theme}.png`) }); await page.keyboard.press('Escape'); await expect(dialog).toHaveCount(0)
  }
})

test.describe('real retained purchase collections and financial guards', () => {
  test.use({ applicationDatabaseFixture: { prepare(database) {
    // Offline retained source only: close Node SQLite before the real Go CLI
    // validates/provisions/serves it. Do not invent HTTP receipts or audit events.
    const db = new DatabaseSync(database), now = '2026-11-07T00:00:00Z'
    try {
      db.exec('PRAGMA foreign_keys=ON; BEGIN IMMEDIATE;')
      const counter = db.prepare('INSERT INTO projection_revisions(entity_type,entity_id,revision) VALUES(?,?,1)'), current = db.prepare('INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at) VALUES(?,?,1,?,?)')
      const baseline = (kind: string, id: string, payload: object) => { counter.run(kind, id); current.run(kind, id, JSON.stringify(payload), now) }
      for (const id of ['new', 'asset-item']) {
        const item = { id, name: id === 'new' ? '實際既有採購物品' : '既有資產物品', brand: null, model: null, category_id: null, description: '', notes: '', tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1, status: 'active' }
        db.prepare('INSERT INTO items(id,name,tracking_mode,created_at,updated_at) VALUES(?,?,?,?,?)').run(id, item.name, item.tracking_mode, now, now); baseline('item', id, item); counter.run('search_doc', JSON.stringify(['item', id]))
      }
      const header = db.prepare('INSERT INTO purchases(id,merchant_name,purchased_at,currency,receipt_note,created_at) VALUES(?,?,?,?,?,?)'), allocation = db.prepare('INSERT INTO purchase_lines(id,purchase_id,item_id,item_unit_id,quantity_base,paid_minor,note) VALUES(?,?,?,?,?,?,?)')
      for (let index = 0; index < 205; index++) {
        const id = index === 0 ? 'new' : `purchase-${String(index).padStart(3, '0')}`, purchase = { id, merchant_name: `既有商家 ${index}`, purchased_at: now, currency: index === 0 ? 'ZZZ' : 'JPY', receipt_note: '' }
        header.run(id, purchase.merchant_name, now, purchase.currency, '', now); baseline('purchase', id, purchase)
        const line = { id: `associated-${index}`, purchase_id: id, item_id: 'new', item_unit_id: null, quantity_base: 1, paid_minor: 0, note: '' }; allocation.run(line.id, id, 'new', null, 1, 0, ''); baseline('purchase_line', line.id, line)
      }
      for (let index = 0; index < 204; index++) { const line = { id: `extra-${index}`, purchase_id: 'new', item_id: 'new', item_unit_id: null, quantity_base: index + 1, paid_minor: index, note: '' }; allocation.run(line.id, line.purchase_id, line.item_id, null, line.quantity_base, line.paid_minor, ''); baseline('purchase_line', line.id, line) }
      const purchase = { id: 'asset-purchase', merchant_name: '實際資產採購', purchased_at: now, currency: 'JPY', receipt_note: '' }; header.run(purchase.id, purchase.merchant_name, now, 'JPY', '', now); baseline('purchase', purchase.id, purchase)
      for (const [id, basis, currency] of [['manual', 'manual', 'JPY'], ['foreign', 'purchase', 'USD'], ['compatible', 'purchase', 'JPY']] as const) {
        const unit = { id, item_id: 'asset-item', location_id: null, asset_code: `AST-${id}`, serial_number: null, notes: '', status: 'stored' }
        db.prepare('INSERT INTO item_units(id,item_id,asset_code,created_at,updated_at) VALUES(?,?,?,?,?)').run(id, unit.item_id, unit.asset_code, now, now); baseline('unit', id, unit); counter.run('search_doc', JSON.stringify(['unit', id]))
        db.prepare('INSERT INTO asset_profiles(item_unit_id,cost_basis,manual_acquisition_minor,currency,created_at,updated_at) VALUES(?,?,?,?,?,?)').run(id, basis, basis === 'manual' ? 0 : null, currency, now, now)
      }
      db.exec('COMMIT'); expect(db.prepare('PRAGMA foreign_key_check').all()).toEqual([])
    } finally { db.close() }
  } } })
  test('completes all 205 associated headers and selected Lines while preserving imported new IDs and retained unknown currency', async ({ page, application }) => {
    await provision(page, application); const reads: URL[] = []; page.on('request', (request) => { const url = new URL(request.url()); if (request.method() === 'GET' && ['/api/v1/items/new/purchases', '/api/v1/purchases/new/lines'].includes(url.pathname)) reads.push(url) })
    await page.goto('/items/new/purchases'); await expect(page.getByText(`${text.associatedCount}205`, { exact: true })).toBeVisible(); expect(reads.filter((url) => url.pathname.endsWith('/purchases') && url.searchParams.has('cursor'))).toHaveLength(2)
    await page.getByRole('link', { name: '"既有商家 0"', exact: true }).click(); await expect(page).toHaveURL(`${application.url}/purchases/new/view`); await expect(page.getByText(`${text.lineCount}205`, { exact: true })).toBeVisible(); await expect(page.getByText('"ZZZ"', { exact: true })).toBeVisible(); expect(reads.filter((url) => url.pathname.endsWith('/lines') && url.searchParams.has('cursor'))).toHaveLength(2); await accessible(page)
  })
  test('displays all three genuine authoritative asset basis/currency/unfinished-producer guards without financial or physical changes', async ({ page, application }) => {
    await provision(page, application); const writes = watch(page); await page.goto('/purchases/asset-purchase/allocate/asset-item')
    for (const [id, guidance] of [['manual', text.basisConflict], ['foreign', text.currencyConflict], ['compatible', text.assetRequired]] as const) {
      await fillLine(page, id); await page.getByRole('button', { name: text.review, exact: true }).click(); await page.getByRole('alertdialog').getByRole('button', { name: text.confirmLine, exact: true }).click(); await expect(page.getByRole('alertdialog')).toContainText(guidance); await page.getByRole('alertdialog').getByRole('button', { name: text.cancel, exact: true }).click(); await expect(page.getByRole('button', { name: text.review, exact: true })).toBeEnabled()
    }
    expect(writes).toHaveLength(3); expect((await (await page.request.get('/api/v1/purchases/asset-purchase/lines')).json()).data).toEqual([]); expect((await currentPurchase(page, 'asset-purchase')).revision).toBe(1); expect((await currentItem(page, 'asset-item')).revision).toBe(1)
  })
})
