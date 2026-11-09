import AxeBuilder from '@axe-core/playwright'
import { DatabaseSync } from 'node:sqlite'
import type { Page } from '@playwright/test'
import type { components } from '../../src/lib/api/schema.d.ts'
import { messages } from '../../src/lib/messages.ts'
import { test, expect } from './auth-fixture.ts'
import { noOverflow, provision, watchDomainWrites, writeContext } from './domain-helpers.ts'

const text = messages.purchaseCorrections, purchaseText = messages.purchases, history = messages.purchaseCorrectionHistory, catalog = messages.catalog
const input = (page: Page, label: string) => page.getByLabel(new RegExp(`^${label}`))
const select = (page: Page, label: string) => page.getByRole('combobox', { name: new RegExp(`^${label}`) })
type Item = components['schemas']['Item']
type Purchase = components['schemas']['Purchase']
type Line = components['schemas']['PurchaseLine']
type Unit = components['schemas']['Unit']
type Correction = components['schemas']['PurchaseLineCorrection']
test.use({ trace: 'off', screenshot: 'off', video: 'off' })

async function item(page: Page, origin: string, name: string, quantity = true): Promise<Item> {
  const response = await page.request.post('/api/v1/items', { headers: await writeContext(page, origin), data: { name, ...(quantity ? { tracking_mode: 'quantity', unit_code: 'kg', base_unit: 'g', scale_factor: 1000 } : {}) } }); expect(response.status()).toBe(200); return response.json()
}
async function seed(page: Page, origin: string, merchant = '實際財務更正商家') {
  const original = await item(page, origin, '原始數量物品')
  const response = await page.request.post('/api/v1/purchases', { headers: await writeContext(page, origin), data: { merchant_name: merchant, purchased_at: '2026-11-07T10:00:00.123456789+08:00', currency: 'JPY', receipt_note: '原始採購' } }); expect(response.status()).toBe(200); const purchase: Purchase = await response.json()
  const created = await page.request.post(`/api/v1/purchases/${purchase.id}/lines`, { headers: await writeContext(page, origin, purchase.revision), data: { item_id: original.id, item_revision: original.revision, item_unit_id: null, quantity_base: 1500, paid_minor: 0, note: '原始零元分攤' } }); expect(created.status()).toBe(200); const line: Line = await created.json()
  return { original, purchase, line }
}
async function current(page: Page, purchaseID: string, itemID: string) {
  const purchase = await page.request.get(`/api/v1/purchases/${purchaseID}`), item = await page.request.get(`/api/v1/items/${itemID}`); expect(purchase.status()).toBe(200); expect(item.status()).toBe(200)
  return { purchase: await purchase.json() as Purchase, item: await item.json() as Item }
}
async function openEditor(page: Page, purchaseID: string, lineID: string) {
  await page.goto(`/purchases/${purchaseID}/line-corrections`); await page.getByRole('link', { name: `${history.start}：${JSON.stringify(lineID)}`, exact: true }).click()
  await expect(page.getByRole('heading', { name: text.title, exact: true })).toBeVisible(); await expect(page.getByRole('button', { name: text.review, exact: true })).toBeEnabled()
}
async function chooseTarget(page: Page, id: string) {
  await page.getByText(text.picker, { exact: true }).click(); await page.getByRole('button', { name: `${text.chooseItem}：${JSON.stringify(id)}`, exact: true }).click()
  await expect(page.getByText(text.interpretationChanged, { exact: true })).toBeVisible(); await expect(page.getByRole('button', { name: text.keepEdits, exact: true })).toBeDisabled()
  await page.getByRole('checkbox', { name: text.acknowledge, exact: true }).check(); await page.getByRole('button', { name: text.keepEdits, exact: true }).click(); await expect(page.getByRole('button', { name: text.review, exact: true })).toBeEnabled()
}
async function confirm(page: Page, label: string, purchaseID: string) {
  const dialog = page.getByRole('alertdialog'), title = dialog.getByRole('heading', { name: text.reviewTitle, exact: true }); await expect(title).toBeFocused(); await expect(title).toBeInViewport()
  await dialog.getByRole('button', { name: label, exact: true }).click(); await expect(dialog).toHaveCount(0); await expect(page).toHaveURL(new RegExp(`/purchases/${purchaseID}/line-corrections$`))
  await expect(page.getByRole('heading', { name: history.title, exact: true })).toBeVisible(); await expect(page.getByText(purchaseText.done, { exact: true })).toBeVisible()
}
async function accessible(page: Page) { expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze()).violations).toEqual([]); await noOverflow(page) }

test('explicitly voids a real zero-paid allocation without receiving stock', async ({ page, application }) => {
  await provision(page, application); const { purchase, original, line } = await seed(page, application.url), source = await current(page, purchase.id, original.id), writes = watchDomainWrites(page, ['/api/v1/purchases', '/api/v1/stock', '/api/v1/units'])
  await openEditor(page, purchase.id, line.id); await expect(select(page, text.action)).toHaveValue(''); await page.getByRole('button', { name: text.review, exact: true }).click(); await expect(select(page, text.action)).toBeFocused()
  await select(page, text.action).selectOption('void'); await input(page, text.reason).fill('  明確作廢零元分攤 é\n'); await page.getByRole('button', { name: text.review, exact: true }).click()
  await expect(page.getByRole('alertdialog')).toContainText(text.voidHelp); expect(writes).toHaveLength(0); await accessible(page); await confirm(page, text.confirmVoid, purchase.id)
  expect(writes).toHaveLength(1); expect(writes[0]?.proof).toBe(`"${source.purchase.revision}"`); expect(JSON.parse(writes[0]?.body ?? '{}')).toEqual({ line_id: line.id, line_revision: line.revision, item_revision: source.item.revision, action: 'void', replacement: null, reason: '  明確作廢零元分攤 é\n' })
  const currentLines = await (await page.request.get(`/api/v1/purchases/${purchase.id}/lines`)).json(); expect(currentLines.data).toEqual([]); const entries = await (await page.request.get(`/api/v1/purchases/${purchase.id}/line-corrections`)).json(); expect(entries.data).toHaveLength(1); expect(entries.data[0]).toMatchObject({ action: 'void', before: line, after: null })
  expect(await (await page.request.get(`/api/v1/inventory?item_id=${original.id}`)).json()).toEqual({ data: [], next_cursor: null })
})

test('uses real paged Item discovery and explicitly replaces the whole quantity allocation', async ({ page, application }) => {
  await provision(page, application); const { purchase, original, line } = await seed(page, application.url), replacement = await item(page, application.url, '替代數量物品'), extra = await item(page, application.url, '其他數量物品'), source = await current(page, purchase.id, original.id)
  const writes = watchDomainWrites(page, ['/api/v1/purchases', '/api/v1/stock', '/api/v1/units']); await page.goto('/purchases?currency=JPY&limit=1'); await page.getByRole('link', { name: /實際財務更正商家/ }).click(); await page.getByRole('link', { name: history.title, exact: true }).click(); await page.getByRole('link', { name: `${history.start}：${JSON.stringify(line.id)}`, exact: true }).click(); await expect(page.getByRole('button', { name: text.review, exact: true })).toBeEnabled()
  await select(page, text.action).selectOption('replace'); await input(page, text.reason).fill('更正整筆分攤'); await page.getByText(text.picker, { exact: true }).click()
  await page.getByText(catalog.advanced, { exact: true }).click(); await input(page, catalog.pageSize).fill('1'); await page.getByRole('button', { name: catalog.apply, exact: true }).click(); await page.getByRole('button', { name: catalog.nextPage, exact: true }).click(); await expect(page.getByRole('button', { name: catalog.previousPage, exact: true })).toBeEnabled()
  await input(page, catalog.search).fill('替代數量物品'); await page.getByRole('button', { name: catalog.apply, exact: true }).click(); await expect(page.getByRole('button', { name: catalog.previousPage, exact: true })).toBeDisabled(); expect(await page.evaluate(() => window.history.state.usr.purchaseList)).toBe('/purchases?currency=JPY&limit=1'); await page.getByRole('button', { name: `${text.chooseItem}：${JSON.stringify(replacement.id)}`, exact: true }).click()
  await expect(page.getByText(text.interpretationChanged, { exact: true })).toBeVisible(); await expect(input(page, text.reason)).toHaveValue('更正整筆分攤'); await page.getByRole('checkbox', { name: text.acknowledge, exact: true }).check(); await page.getByRole('button', { name: text.keepEdits, exact: true }).click()
  await expect(input(page, text.targetID)).toHaveValue(replacement.id); await expect(input(page, purchaseText.fields.quantity_base)).toHaveValue(''); await expect(input(page, purchaseText.fields.paid_minor)).toHaveValue('')
  await select(page, purchaseText.fields.allocation).selectOption('none'); await input(page, purchaseText.fields.quantity_base).fill('2500'); await input(page, purchaseText.fields.paid_minor).fill('0'); await input(page, purchaseText.fields.note).fill('替代分攤\n')
  await page.getByRole('button', { name: text.review, exact: true }).click(); await confirm(page, text.confirmReplace, purchase.id)
  expect(writes).toHaveLength(1); expect(writes[0]?.proof).toBe(`"${source.purchase.revision}"`); expect(JSON.parse(writes[0]?.body ?? '{}')).toMatchObject({ line_id: line.id, line_revision: line.revision, item_revision: source.item.revision, action: 'replace', replacement: { item_id: replacement.id, item_revision: replacement.revision, item_unit_id: null, quantity_base: 2500, paid_minor: 0, note: '替代分攤\n' } })
  const lines = await (await page.request.get(`/api/v1/purchases/${purchase.id}/lines`)).json(); expect(lines.data).toHaveLength(1); expect(lines.data[0]).toMatchObject({ item_id: replacement.id, paid_minor: 0, quantity_base: 2500 }); expect(lines.data[0].id).not.toBe(line.id)
  for (const id of [original.id, replacement.id, extra.id]) expect((await (await page.request.get(`/api/v1/inventory?item_id=${id}`)).json()).data).toEqual([])
})

test('rejects a real stale replacement proof and explicitly adopts it without changing the physical Unit', async ({ page, application }) => {
  await provision(page, application); const { purchase, original, line } = await seed(page, application.url), target = await item(page, application.url, '替代個體物品', false)
  const created = await page.request.post(`/api/v1/items/${target.id}/units`, { headers: await writeContext(page, application.url, target.revision), data: { location_id: null, notes: 'Physical target remains unchanged' } }); expect(created.status()).toBe(200); const unit: Unit = await created.json(), targetBefore = (await current(page, purchase.id, target.id)).item
  const writes = watchDomainWrites(page, ['/api/v1/purchases', '/api/v1/stock', '/api/v1/units']); await openEditor(page, purchase.id, line.id); await select(page, text.action).selectOption('replace'); await input(page, text.reason).fill('指定實際個體'); await chooseTarget(page, target.id)
  await select(page, purchaseText.fields.allocation).selectOption('unit'); await page.getByRole('button', { name: text.review, exact: true }).click(); await expect(select(page, purchaseText.fields.item_unit_id)).toBeFocused()
  await select(page, purchaseText.fields.item_unit_id).selectOption(unit.id); await input(page, purchaseText.fields.quantity_base).fill('2'); await input(page, purchaseText.fields.paid_minor).fill('50'); await page.getByRole('button', { name: text.review, exact: true }).click(); await expect(input(page, purchaseText.fields.quantity_base)).toBeFocused()
  await input(page, purchaseText.fields.quantity_base).fill('1'); await page.getByRole('button', { name: text.review, exact: true }).click()
  const changed = await page.request.patch(`/api/v1/items/${target.id}`, { headers: await writeContext(page, application.url, targetBefore.revision), data: { notes: 'Concurrent target metadata' } }); expect(changed.status()).toBe(200)
  await page.getByRole('alertdialog').getByRole('button', { name: text.confirmReplace, exact: true }).click(); await expect(page.getByRole('alertdialog')).toContainText(messages.locations.conflict)
  await page.getByRole('alertdialog').getByRole('button', { name: text.cancel, exact: true }).click(); await expect(page.getByText(text.changed, { exact: true })).toBeVisible(); await input(page, purchaseText.fields.paid_minor).fill('1.5'); await page.getByRole('button', { name: text.keepEdits, exact: true }).click(); await expect(input(page, purchaseText.fields.paid_minor)).toHaveValue('1.5')
  await page.getByRole('button', { name: text.review, exact: true }).click(); await expect(input(page, purchaseText.fields.paid_minor)).toBeFocused(); await input(page, purchaseText.fields.paid_minor).fill('0'); await page.getByRole('button', { name: text.review, exact: true }).click(); await confirm(page, text.confirmReplace, purchase.id)
  expect(writes).toHaveLength(2); expect(writes[1]?.key).not.toBe(writes[0]?.key); expect(JSON.parse(writes[0]?.body ?? '{}').replacement.item_revision).toBe(targetBefore.revision); expect(JSON.parse(writes[1]?.body ?? '{}').replacement).toMatchObject({ item_revision: targetBefore.revision + 1, item_unit_id: unit.id, quantity_base: 1, paid_minor: 0 })
  expect(await (await page.request.get(`/api/v1/units/${unit.id}`)).json()).toEqual(unit); expect((await (await page.request.get(`/api/v1/inventory?item_id=${original.id}`)).json()).data).toEqual([])
})

test('rejects an independently stale original Item proof before an explicit void review', async ({ page, application }) => {
  await provision(page, application); const { purchase, original, line } = await seed(page, application.url), source = await current(page, purchase.id, original.id), writes = watchDomainWrites(page, ['/api/v1/purchases'])
  await openEditor(page, purchase.id, line.id); await select(page, text.action).selectOption('void'); await input(page, text.reason).fill('核對原始物品版本'); await page.getByRole('button', { name: text.review, exact: true }).click()
  expect((await page.request.patch(`/api/v1/items/${original.id}`, { headers: await writeContext(page, application.url, source.item.revision), data: { notes: 'Concurrent original Item metadata' } })).status()).toBe(200)
  await page.getByRole('alertdialog').getByRole('button', { name: text.confirmVoid, exact: true }).click(); await expect(page.getByRole('alertdialog')).toContainText(messages.locations.conflict); await page.getByRole('alertdialog').getByRole('button', { name: text.cancel, exact: true }).click()
  await expect(page.getByText(text.changed, { exact: true })).toBeVisible(); await expect(input(page, text.reason)).toHaveValue('核對原始物品版本'); await page.getByRole('button', { name: text.keepEdits, exact: true }).click(); await page.getByRole('button', { name: text.review, exact: true }).click(); await confirm(page, text.confirmVoid, purchase.id)
  expect(writes).toHaveLength(2); expect(writes[1]?.key).not.toBe(writes[0]?.key); expect(writes[0]?.proof).toBe(`"${source.purchase.revision}"`); expect(writes[1]?.proof).toBe(writes[0]?.proof); expect(JSON.parse(writes[0]?.body ?? '{}').item_revision).toBe(source.item.revision); expect(JSON.parse(writes[1]?.body ?? '{}').item_revision).toBe(source.item.revision + 1)
})

test('preserves raw replacement fields after the selected Item is deleted until it is explicitly cleared', async ({ page, application }) => {
  await provision(page, application); const { purchase, line } = await seed(page, application.url), target = await item(page, application.url, '即將移除的替代物品'), writes = watchDomainWrites(page, ['/api/v1/purchases'])
  await openEditor(page, purchase.id, line.id); await select(page, text.action).selectOption('replace'); await input(page, text.reason).fill('保留刪除目標前的草稿'); await chooseTarget(page, target.id)
  await select(page, purchaseText.fields.allocation).selectOption('none'); await input(page, purchaseText.fields.quantity_base).fill('1.5'); await input(page, purchaseText.fields.paid_minor).fill('9007199254740992')
  expect((await page.request.delete(`/api/v1/items/${target.id}`, { headers: await writeContext(page, application.url, target.revision) })).status()).toBe(200); await page.getByRole('button', { name: purchaseText.refresh, exact: true }).click()
  await expect(page.getByText(text.missing, { exact: true })).toBeVisible(); await expect(input(page, text.targetID)).toHaveValue(target.id); await expect(input(page, purchaseText.fields.quantity_base)).toHaveValue('1.5'); await expect(input(page, purchaseText.fields.paid_minor)).toHaveValue('9007199254740992'); await expect(page.getByRole('button', { name: text.review, exact: true })).toBeDisabled(); expect(writes).toHaveLength(0)
  await page.getByText(text.picker, { exact: true }).click(); await page.getByRole('button', { name: text.clearItem, exact: true }).click(); await expect(page.getByText(text.interpretationChanged, { exact: true })).toBeVisible(); await page.getByRole('checkbox', { name: text.acknowledge, exact: true }).check(); await page.getByRole('button', { name: text.keepEdits, exact: true }).click()
  await expect(input(page, text.targetID)).toHaveValue(''); await expect(input(page, text.reason)).toHaveValue('保留刪除目標前的草稿'); await select(page, text.action).selectOption('void'); await page.getByRole('button', { name: text.review, exact: true }).click(); await confirm(page, text.confirmVoid, purchase.id)
  expect(writes).toHaveLength(1); expect(JSON.parse(writes[0]?.body ?? '{}')).toMatchObject({ line_id: line.id, action: 'void', replacement: null })
})

test('retains a deleted selected Unit until the user explicitly chooses no Unit', async ({ page, application }) => {
  await provision(page, application); const { purchase, line } = await seed(page, application.url), target = await item(page, application.url, '個體替代目標', false)
  const created = await page.request.post(`/api/v1/items/${target.id}/units`, { headers: await writeContext(page, application.url, target.revision), data: { location_id: null } }); expect(created.status()).toBe(200); const unit: Unit = await created.json(), writes = watchDomainWrites(page, ['/api/v1/purchases'])
  await openEditor(page, purchase.id, line.id); await select(page, text.action).selectOption('replace'); await input(page, text.reason).fill('保留個體選擇'); await chooseTarget(page, target.id); await select(page, purchaseText.fields.allocation).selectOption('unit'); await select(page, purchaseText.fields.item_unit_id).selectOption(unit.id); await input(page, purchaseText.fields.quantity_base).fill('1'); await input(page, purchaseText.fields.paid_minor).fill('0')
  expect((await page.request.delete(`/api/v1/units/${unit.id}`, { headers: await writeContext(page, application.url, unit.revision) })).status()).toBe(200); await page.getByRole('button', { name: purchaseText.refresh, exact: true }).click(); await expect(page.getByText(text.changed, { exact: true })).toBeVisible(); await page.getByRole('button', { name: text.keepEdits, exact: true }).click()
  const selectedUnit = select(page, purchaseText.fields.item_unit_id); await expect(selectedUnit).toBeVisible(); await expect(selectedUnit).toHaveValue(unit.id); expect(await selectedUnit.evaluate((element: HTMLSelectElement) => element.options[element.selectedIndex]?.textContent)).toContain(purchaseText.unavailableUnit); await page.getByRole('button', { name: text.review, exact: true }).click(); await expect(selectedUnit).toBeFocused(); expect(writes).toHaveLength(0)
  await select(page, purchaseText.fields.allocation).selectOption('none'); await page.getByRole('button', { name: text.review, exact: true }).click(); await confirm(page, text.confirmReplace, purchase.id); expect(writes).toHaveLength(1); expect(JSON.parse(writes[0]?.body ?? '{}').replacement).toMatchObject({ item_id: target.id, item_unit_id: null, quantity_base: 1, paid_minor: 0 }); expect((await page.request.get(`/api/v1/units/${unit.id}`)).status()).toBe(404)
})

test('reconciles a committed malformed void only with the original key after the Line disappears', async ({ page, application }) => {
  await provision(page, application); const { purchase, line } = await seed(page, application.url), writes = watchDomainWrites(page, ['/api/v1/purchases']); let attempts = 0
  await page.route((url) => url.pathname === `/api/v1/purchases/${purchase.id}/line-corrections`, async (route) => {
    if (route.request().method() !== 'POST') { await route.continue(); return }
    const response = await route.fetch(); expect(response.status()).toBe(200)
    if (++attempts === 1) { const body = await response.json(); body.reason = 'Altered response'; await route.fulfill({ response, body: JSON.stringify(body) }) } else await route.fulfill({ response })
  })
  await openEditor(page, purchase.id, line.id); await select(page, text.action).selectOption('void'); await input(page, text.reason).fill('核對原始請求'); await page.getByRole('button', { name: text.review, exact: true }).click(); await page.getByRole('alertdialog').getByRole('button', { name: text.confirmVoid, exact: true }).click()
  await expect(page.getByRole('alertdialog').getByRole('heading', { name: text.uncertain, exact: true })).toBeVisible(); await page.getByRole('alertdialog').getByRole('button', { name: text.cancel, exact: true }).click(); await expect(page.getByText(purchaseText.missingHelp, { exact: true })).toBeVisible()
  expect((await (await page.request.get(`/api/v1/purchases/${purchase.id}/lines`)).json()).data).toEqual([]); await expect(input(page, text.reason)).toHaveValue('核對原始請求'); await expect(page.getByRole('button', { name: text.review, exact: true })).toBeDisabled()
  await page.evaluate(() => window.dispatchEvent(new Event('online'))); expect(attempts).toBe(1); await page.getByRole('button', { name: text.openOriginal, exact: true }).click(); await page.getByRole('alertdialog').getByRole('button', { name: text.retryOriginal, exact: true }).click()
  await expect(page.getByRole('heading', { name: history.title, exact: true })).toBeVisible(); await expect(page.getByText(purchaseText.done, { exact: true })).toBeVisible(); expect(attempts).toBe(2); expect(writes).toHaveLength(2); expect(writes[1]).toEqual(writes[0])
  const entries = await (await page.request.get(`/api/v1/purchases/${purchase.id}/line-corrections`)).json(); expect(entries.data).toHaveLength(1); expect(entries.data[0]).toMatchObject({ action: 'void', before: line, after: null })
})

test('purges a dirty draft when real replacement discovery loses authorization', async ({ page, application }) => {
  const password = await provision(page, application); const { purchase, line } = await seed(page, application.url), writes = watchDomainWrites(page, ['/api/v1/purchases'])
  await page.goto('/purchases?currency=JPY&limit=1'); await page.getByRole('link', { name: /實際財務更正商家/ }).click(); await page.getByRole('link', { name: history.title, exact: true }).click(); await page.getByRole('link', { name: `${history.start}：${JSON.stringify(line.id)}`, exact: true }).click()
  await expect(page.getByRole('button', { name: text.review, exact: true })).toBeEnabled(); await select(page, text.action).selectOption('void'); await input(page, text.reason).fill('不應帶入登入狀態')
  await page.context().setOffline(true); await expect(page.getByText(text.offline, { exact: true })).toBeVisible(); await expect(page.getByRole('button', { name: text.review, exact: true })).toBeDisabled(); await input(page, text.reason).fill('離線保留但不自動重送'); await page.context().setOffline(false); await expect(page.getByRole('button', { name: text.review, exact: true })).toBeEnabled(); await expect(input(page, text.reason)).toHaveValue('離線保留但不自動重送'); expect(writes).toHaveLength(0)
  expect((await page.request.post('/api/v1/auth/logout', { headers: await writeContext(page, application.url) })).status()).toBe(200); await page.getByText(text.picker, { exact: true }).click(); await expect(page.getByRole('heading', { name: messages.auth.loginTitle, exact: true })).toBeVisible()
  await input(page, messages.auth.username).fill('owner'); await input(page, messages.auth.password).fill(password); await page.getByRole('button', { name: messages.auth.login, exact: true }).click()
  await expect(page).toHaveURL(new RegExp(`/purchases/${purchase.id}/line-corrections/edit\\?line=`)); await expect(input(page, text.reason)).toHaveValue('')
  const state = await page.evaluate(() => window.history.state.usr); expect(state).toMatchObject({ purchaseList: '/purchases?currency=JPY&limit=1' }); for (const key of ['reason', 'key', 'command', 'csrfToken']) expect(state).not.toHaveProperty(key); expect(writes).toHaveLength(0)
})

test('keeps long financial review reachable at 200 percent text in both themes', async ({ page, application }, info) => {
  await provision(page, application); const { purchase, line } = await seed(page, application.url, '長商家名稱'.repeat(50)), writes = watchDomainWrites(page, ['/api/v1/purchases']); await openEditor(page, purchase.id, line.id)
  await select(page, text.action).selectOption('void'); await input(page, text.reason).fill('長財務更正原因 é 😀 '.repeat(150)); await page.locator('html').evaluate((element) => { element.style.fontSize = '200%' })
  for (const theme of ['light', 'dark'] as const) {
    await page.getByRole('button', { name: messages.theme.label, exact: true }).click(); await page.getByRole('menuitemradio', { name: messages.theme[theme], exact: true }).click(); await expect(page.getByRole('menu')).toHaveCount(0)
    await page.evaluate(() => window.scrollTo(0, 0)); await noOverflow(page); const brand = page.getByRole('link', { name: messages.app.home, exact: true })
    expect(await brand.evaluate((element) => { const range = document.createRange(); range.selectNodeContents(element.querySelector('span')!); return element.scrollWidth <= element.clientWidth && element.getBoundingClientRect().right <= window.innerWidth && range.getClientRects().length === 1 })).toBe(true)
    const heading = page.getByRole('heading', { name: text.title, exact: true }); await heading.scrollIntoViewIfNeeded(); expect(await heading.evaluate((element) => { const range = document.createRange(); range.selectNodeContents(element); return range.getClientRects().length })).toBeLessThanOrEqual(2); await page.screenshot({ path: info.outputPath(`purchase-correction-page-${theme}.png`) }); await page.getByRole('button', { name: text.review, exact: true }).click()
    const dialog = page.getByRole('alertdialog'), title = dialog.getByRole('heading', { name: text.reviewTitle, exact: true }), confirmButton = dialog.getByRole('button', { name: text.confirmVoid, exact: true })
    await expect(title).toBeFocused(); await expect(title).toBeInViewport(); await accessible(page); await page.screenshot({ path: info.outputPath(`purchase-correction-review-${theme}.png`) })
    for (let count = 0; count < 12 && !await confirmButton.evaluate((element) => element === document.activeElement); count++) await page.keyboard.press('Tab')
    await expect(confirmButton).toBeFocused(); await expect(confirmButton).toBeInViewport(); expect(await confirmButton.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true); await page.screenshot({ path: info.outputPath(`purchase-correction-confirm-${theme}.png`) }); await page.keyboard.press('Escape'); await expect(dialog).toHaveCount(0)
  }
  await page.getByRole('link', { name: text.backHistory, exact: true }).click(); const leave = page.getByRole('alertdialog'); await expect(leave.getByText(text.leaveHelp, { exact: true })).toBeVisible(); await leave.getByRole('button', { name: text.stay, exact: true }).click(); expect(writes).toHaveLength(0)
})

test.describe('retained body-only Line identity', () => {
  test.use({ applicationDatabaseFixture: { prepare(database) {
    const db = new DatabaseSync(database), now = '2026-11-07T00:00:00Z'
    try {
      db.exec('PRAGMA foreign_keys=ON; BEGIN IMMEDIATE;')
      const counter = db.prepare('INSERT INTO projection_revisions(entity_type,entity_id,revision) VALUES(?,?,1)'), currentProjection = db.prepare('INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at) VALUES(?,?,1,?,?)')
      const baseline = (kind: string, id: string, payload: object) => { counter.run(kind, id); currentProjection.run(kind, id, JSON.stringify(payload), now) }
      const original = { id: 'legacy-item', name: '既有數量物品', brand: null, model: null, category_id: null, description: '', notes: '', tracking_mode: 'quantity', unit_code: 'kg', base_unit: 'g', scale_factor: 1000, status: 'active' }
      db.prepare('INSERT INTO items(id,name,tracking_mode,unit_code,base_unit,scale_factor,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)').run(original.id, original.name, original.tracking_mode, original.unit_code, original.base_unit, original.scale_factor, now, now); baseline('item', original.id, original); counter.run('search_doc', JSON.stringify(['item', original.id]))
      const purchase = { id: 'new', merchant_name: '既有採購', purchased_at: now, currency: 'JPY', receipt_note: '' }; db.prepare('INSERT INTO purchases(id,merchant_name,purchased_at,currency,receipt_note,created_at) VALUES(?,?,?,?,?,?)').run(purchase.id, purchase.merchant_name, now, purchase.currency, '', now); baseline('purchase', purchase.id, purchase)
      const line = { id: 'legacy/line?&', purchase_id: purchase.id, item_id: original.id, item_unit_id: null, quantity_base: 1500, paid_minor: 0, note: 'Retained legacy Line' }; db.prepare('INSERT INTO purchase_lines(id,purchase_id,item_id,item_unit_id,quantity_base,paid_minor,note) VALUES(?,?,?,?,?,?,?)').run(line.id, line.purchase_id, line.item_id, null, line.quantity_base, line.paid_minor, line.note); baseline('purchase_line', line.id, line)
      db.exec('COMMIT'); expect(db.prepare('PRAGMA foreign_key_check').all()).toEqual([])
    } finally { db.close() }
  } } })
  test('selects, logs in, and voids the exact imported Line without turning its ID into a path', async ({ page, application }) => {
    const password = await provision(page, application), lineID = 'legacy/line?&', writes = watchDomainWrites(page, ['/api/v1/purchases'])
    await page.goto('/purchases/new/line-corrections'); await page.getByRole('link', { name: `${history.start}：${JSON.stringify(lineID)}`, exact: true }).click()
    await expect(page).toHaveURL(`${application.url}/purchases/new/line-corrections/edit?line=legacy%2Fline%3F%26`); await expect(page.getByRole('button', { name: text.review, exact: true })).toBeEnabled()
    await page.context().clearCookies(); await page.getByRole('button', { name: purchaseText.refresh, exact: true }).click(); await expect(page.getByRole('heading', { name: messages.auth.loginTitle, exact: true })).toBeVisible()
    await input(page, messages.auth.username).fill('owner'); await input(page, messages.auth.password).fill(password); await page.getByRole('button', { name: messages.auth.login, exact: true }).click(); await expect(page).toHaveURL(`${application.url}/purchases/new/line-corrections/edit?line=legacy%2Fline%3F%26`)
    await select(page, text.action).selectOption('void'); await input(page, text.reason).fill('保留既有識別碼'); await page.getByRole('button', { name: text.review, exact: true }).click(); await confirm(page, text.confirmVoid, 'new')
    expect(writes).toHaveLength(1); expect(JSON.parse(writes[0]?.body ?? '{}')).toMatchObject({ line_id: lineID, action: 'void', replacement: null }); const entries = await (await page.request.get('/api/v1/purchases/new/line-corrections')).json(); expect(entries.data).toHaveLength(1); expect((entries.data[0] as Correction).before.id).toBe(lineID)
  })
})
