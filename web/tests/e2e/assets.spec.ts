import AxeBuilder from '@axe-core/playwright'
import { DatabaseSync } from 'node:sqlite'
import type { Page } from '@playwright/test'
import type { components } from '../../src/lib/api/schema.d.ts'
import { messages } from '../../src/lib/messages.ts'
import { test, expect } from './auth-fixture.ts'
import { noOverflow, provision, watchDomainWrites, writeContext } from './domain-helpers.ts'

const text = messages.assets
const input = (page: Page, label: string) => page.getByLabel(new RegExp(`^${label}`))
const select = (page: Page, label: string) => page.getByRole('combobox', { name: new RegExp(`^${label}`) })
type Item = components['schemas']['Item']
type Unit = components['schemas']['Unit']
type Asset = components['schemas']['Asset']
type Usage = components['schemas']['Usage']
type Cost = components['schemas']['Cost']
const instant = '2026-11-07T10:00:00.123456789+08:00'
test.use({ trace: 'off', screenshot: 'off', video: 'off' })
async function currentItem(page: Page, id: string): Promise<Item> { const response = await page.request.get(`/api/v1/items/${id}`); expect(response.status()).toBe(200); return response.json() }
async function currentAsset(page: Page, id: string): Promise<Asset> { const response = await page.request.get(`/api/v1/units/${id}/asset`); expect(response.status()).toBe(200); return response.json() }
async function seed(page: Page, origin: string, withProfile = true, name = '實際單件資產', placement?: { category_id: string; location_id: string }) {
  const created = await page.request.post('/api/v1/items', { headers: await writeContext(page, origin), data: { name, ...(placement ? { category_id: placement.category_id } : {}) } }); expect(created.status()).toBe(200); const item: Item = await created.json()
  const physical = await page.request.post(`/api/v1/items/${item.id}/units`, { headers: await writeContext(page, origin, item.revision), data: { location_id: placement?.location_id ?? null, serial_number: '', notes: 'Retained physical source' } }); expect(physical.status()).toBe(200); const unit: Unit = await physical.json()
  if (withProfile) {
    const owner = await currentItem(page, item.id)
    const profile = await page.request.put(`/api/v1/units/${unit.id}/asset`, { headers: await writeContext(page, origin, unit.revision), data: { item_revision: owner.revision, asset_revision: null, cost_basis: 'manual', currency: 'JPY', manual_acquisition_minor: 100, in_service_at: null, retired_at: null, estimated_residual_minor: null, usage_unit: 'times' } })
    expect(profile.status()).toBe(200)
  }
  return { item, unit }
}
async function accessible(page: Page) { expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze()).violations).toEqual([]); await noOverflow(page) }
async function confirm(page: Page, label: string, destination: string, heading: string) {
  const dialog = page.getByRole('alertdialog'), title = dialog.getByRole('heading', { name: text.reviewTitle, exact: true })
  await expect(title).toBeFocused(); await expect(title).toBeInViewport(); await dialog.getByRole('button', { name: label, exact: true }).click()
  await expect(page).toHaveURL(new RegExp(`${destination}$`)); await expect(page.getByRole('heading', { level: 1, name: heading, exact: true })).toBeVisible(); await expect(page.getByText(text.done, { exact: true })).toBeVisible(); await expect(dialog).toHaveCount(0)
}

test('opts in from an actual individual Unit with explicit nullable decisions and zero, without physical mutation', async ({ page, application }) => {
  await provision(page, application); const { item, unit } = await seed(page, application.url, false), owner = await currentItem(page, item.id), writes = watchDomainWrites(page, ['/api/v1/units'])
  await page.goto(`/units/${unit.id}`); await page.getByRole('link', { name: text.title, exact: true }).click(); await expect(page.getByRole('heading', { name: text.noProfile, exact: true })).toBeVisible(); await page.getByRole('link', { name: text.optIn, exact: true }).click()
  await expect(select(page, text.fields.cost_basis)).toHaveValue(''); await expect(input(page, text.fields.currency)).toHaveValue(''); await page.getByRole('button', { name: text.review, exact: true }).click(); await expect(select(page, text.fields.cost_basis)).toBeFocused(); expect(writes).toHaveLength(0)
  await select(page, text.fields.cost_basis).selectOption('manual'); await input(page, text.fields.currency).fill('JPY'); await input(page, text.fields.manual_acquisition_minor).fill('0'); await select(page, text.fields.usage_unit).selectOption('times')
  for (const field of ['service_decision', 'retirement_decision', 'residual_decision'] as const) await select(page, text.fields[field]).selectOption('unknown')
  await page.getByRole('button', { name: text.review, exact: true }).click(); await accessible(page); await confirm(page, text.confirms['put-profile'], `/assets/${unit.id}`, text.detail)
  expect(await currentAsset(page, unit.id)).toMatchObject({ revision: 1, manual_acquisition_minor: 0, in_service_at: null, retired_at: null, estimated_residual_minor: null })
  expect(await (await page.request.get(`/api/v1/units/${unit.id}`)).json()).toEqual(unit)
  expect(writes).toHaveLength(1); expect(writes[0]?.proof).toBe(`"${unit.revision}"`); expect(JSON.parse(writes[0]?.body ?? '{}')).toMatchObject({ item_revision: owner.revision, asset_revision: null, manual_acquisition_minor: 0 })
})

test('edits complete exact profile intent and retains all nanoseconds, explicit residual zero and independent Unit proof', async ({ page, application }) => {
  await provision(page, application); const { unit } = await seed(page, application.url), writes = watchDomainWrites(page, ['/api/v1/units'])
  await page.goto(`/assets/${unit.id}/edit`); await expect(input(page, text.fields.manual_acquisition_minor)).toHaveValue('100'); await input(page, text.fields.manual_acquisition_minor).fill('123'); await select(page, text.fields.service_decision).selectOption('value'); await input(page, text.fields.in_service_at).fill(instant); await select(page, text.fields.residual_decision).selectOption('value'); await input(page, text.fields.estimated_residual_minor).fill('0')
  await page.getByRole('button', { name: text.review, exact: true }).click(); await confirm(page, text.confirms['put-profile'], `/assets/${unit.id}`, text.detail)
  expect(await currentAsset(page, unit.id)).toMatchObject({ revision: 2, manual_acquisition_minor: 123, in_service_at: '2026-11-07T02:00:00.123456789Z', estimated_residual_minor: 0, retired_at: null })
  expect(writes[0]?.proof).toBe('"1"'); expect(JSON.parse(writes[0]?.body ?? '{}')).toMatchObject({ asset_revision: 1, in_service_at: instant })
})

test('records a user-triggered +1 only after choosing its explicit instant and confirming once', async ({ page, application }) => {
  await provision(page, application); const { unit } = await seed(page, application.url), writes = watchDomainWrites(page, ['/api/v1/units'])
  await page.goto(`/assets/${unit.id}/usage/new`); await expect(input(page, text.fields.delta)).toHaveValue(''); await page.getByRole('button', { name: text.quickUse, exact: true }).click(); await expect(input(page, text.fields.delta)).toHaveValue('1'); await expect(input(page, text.fields.occurred_at)).toHaveValue(''); expect(writes).toHaveLength(0)
  await input(page, text.fields.occurred_at).fill(instant); await input(page, text.fields.note).fill('  一次使用 é\n  '); await page.getByRole('button', { name: text.review, exact: true }).click(); await confirm(page, text.confirms['record-usage'], `/assets/${unit.id}/usage`, text.usage)
  const actual: components['schemas']['UsageList'] = await (await page.request.get(`/api/v1/units/${unit.id}/usage`)).json(); expect(actual.data).toHaveLength(1); expect(actual.data[0]).toMatchObject({ delta: 1, event_type: 'use', occurred_at: '2026-11-07T02:00:00.123456789Z', note: '  一次使用 é\n  ', correction_of: null })
  expect(writes).toHaveLength(1); expect(writes[0]?.proof).toBe('"1"'); expect(JSON.parse(writes[0]?.body ?? '{}')).not.toHaveProperty('unit_revision'); expect(await (await page.request.get(`/api/v1/units/${unit.id}`)).json()).toEqual(unit)
})

for (const kind of ['usage', 'costs'] as const) {
  test(`appends a real selected ${kind} correction without overwriting original immutable history`, async ({ page, application }) => {
    await provision(page, application); const { unit } = await seed(page, application.url)
    const initial = await page.request.post(`/api/v1/units/${unit.id}/${kind}`, { headers: await writeContext(page, application.url, 1), data: kind === 'usage' ? { delta: 2, usage_unit: 'times', event_type: 'use', occurred_at: instant } : { amount_minor: 5, currency: 'JPY', kind: 'repair', direction: 'expense', occurred_at: instant } }); expect(initial.status()).toBe(200); const original: Usage | Cost = await initial.json(), writes = watchDomainWrites(page, ['/api/v1/units'])
    await page.goto(`/assets/${unit.id}/${kind}/new`)
    if (kind === 'usage') { await select(page, text.fields.event_type).selectOption('correction'); await input(page, text.fields.delta).fill('-1') }
    else { await select(page, text.fields.kind).selectOption('adjustment'); await select(page, text.fields.direction).selectOption('credit'); await input(page, text.fields.amount_minor).fill('2') }
    await select(page, text.fields.correction_of).selectOption(original.id); await input(page, text.fields.occurred_at).fill(instant); await input(page, text.fields.note).fill('  明確參照更正 é\n  '); await page.getByRole('button', { name: text.review, exact: true }).click(); await expect(page.getByRole('alertdialog')).toContainText(original.id)
    await confirm(page, text.confirms[kind === 'usage' ? 'record-usage' : 'record-cost'], `/assets/${unit.id}/${kind}`, text[kind]); const actual = await (await page.request.get(`/api/v1/units/${unit.id}/${kind}`)).json() as { data: (Usage | Cost)[] }
    expect(actual.data).toHaveLength(2); expect(actual.data.find((row) => row.id === original.id)).toEqual(original); expect(actual.data.find((row) => row.id !== original.id)).toMatchObject({ revision: 1, correction_of: original.id, ...(kind === 'usage' ? { delta: -1 } : { amount_minor: 2, direction: 'credit' }) })
    expect(writes).toHaveLength(1); expect(writes[0]?.proof).toBe('"2"'); expect(JSON.parse(writes[0]?.body ?? '{}')).not.toHaveProperty('unit_revision'); await accessible(page)
  })
}

for (const action of ['retire', 'dispose'] as const) {
  test(`records genuine ${action} with separate original physical and financial scopes and no inferred financial date`, async ({ page, application }) => {
    await provision(page, application); const { unit } = await seed(page, application.url), writes = watchDomainWrites(page, ['/api/v1/units'])
    await page.goto(`/assets/${unit.id}/${action}`)
    if (action === 'retire') { await expect(input(page, text.fields.retired_at)).toHaveValue(''); await input(page, text.fields.retired_at).fill(instant) }
    else { await select(page, text.dispose).selectOption('other'); await expect(input(page, text.fields.realized_proceeds_minor)).toHaveValue(''); await input(page, text.fields.realized_proceeds_minor).fill('0'); await input(page, text.fields.disposed_at).fill('1999-01-01T00:00:00Z') }
    await input(page, text.fields.note).fill('  實際生命週期 é\n  '); await page.getByRole('button', { name: text.review, exact: true }).click(); await confirm(page, text.confirms[action === 'retire' ? 'retire-asset' : 'dispose-asset'], `/assets/${unit.id}`, text.detail)
    const physical: Unit = await (await page.request.get(`/api/v1/units/${unit.id}`)).json(); expect(physical).toEqual({ ...unit, revision: 2, status: action === 'retire' ? 'retired' : 'disposed' })
    const asset = await currentAsset(page, unit.id); expect(asset.retired_at).toBe(action === 'retire' ? '2026-11-07T02:00:00.123456789Z' : null)
    if (action === 'dispose') expect(await (await page.request.get(`/api/v1/units/${unit.id}/disposal`)).json()).toMatchObject({ kind: 'other', realized_proceeds_minor: 0, disposed_at: '1999-01-01T00:00:00.000000000Z' })
    expect(writes).toHaveLength(1); expect(writes[0]?.proof).toBe('"1"'); expect(JSON.parse(writes[0]?.body ?? '{}')).toMatchObject({ unit_revision: 1 })
  })
}

test('renders actual null denominators and unknown residual without fabricating zero projected cost', async ({ page, application }) => {
  await provision(page, application); const { unit } = await seed(page, application.url)
  await page.goto(`/assets/${unit.id}/summary?as_of=2026-11-07T10%3A00%3A00.123456789%2B08%3A00`)
  const table = page.getByRole('table'); await expect(table).toBeVisible(); await expect(table.getByRole('row', { name: new RegExp(text.summaryFields.per_day_minor) })).toContainText(text.nullRatio); await expect(table.getByRole('row', { name: new RegExp(text.summaryFields.projected_net_cost_minor) })).toContainText(text.unknownValue)
  await expect(page.getByText('2026-11-07T02:00:00.123456789Z', { exact: true })).toBeVisible(); await expect(page.getByText(text.sourceRevision, { exact: true })).toBeVisible(); await accessible(page)
})

test('renders the actual negative realized net separately from residual projection and exact home-civil context', async ({ page, application }) => {
  await provision(page, application); const { item, unit } = await seed(page, application.url), owner = await currentItem(page, item.id)
  const profile = await page.request.put(`/api/v1/units/${unit.id}/asset`, { headers: await writeContext(page, application.url, unit.revision), data: { item_revision: owner.revision, asset_revision: 1, cost_basis: 'manual', currency: 'JPY', manual_acquisition_minor: 100, in_service_at: '2024-11-07T00:00:00Z', retired_at: null, estimated_residual_minor: 30, usage_unit: 'times' } }); expect(profile.status()).toBe(200)
  for (const [tail, data] of [['usage', { delta: 2, usage_unit: 'times', event_type: 'use', occurred_at: instant }], ['costs', { amount_minor: 5, currency: 'JPY', kind: 'repair', direction: 'expense', occurred_at: instant }], ['disposal', { unit_revision: unit.revision, kind: 'sold', disposed_at: instant, currency: 'JPY', realized_proceeds_minor: 200 }]] as const) {
    const asset = await currentAsset(page, unit.id); expect((await page.request.post(`/api/v1/units/${unit.id}/${tail}`, { headers: await writeContext(page, application.url, asset.revision), data })).status()).toBe(200)
  }
  await page.goto(`/assets/${unit.id}/summary?as_of=2026-11-07T10%3A00%3A00.123456789%2B08%3A00`)
  const table = page.getByRole('table'); await expect(table.getByRole('row', { name: new RegExp(text.summaryFields.net_cost_minor) }).getByRole('cell')).toContainText('-95'); await expect(table.getByRole('row', { name: new RegExp(text.summaryFields.projected_net_cost_minor) }).getByRole('cell')).toContainText('75')
  await expect(table.getByRole('row', { name: new RegExp(text.summaryFields.realized_proceeds_minor) }).getByRole('cell')).toContainText('200'); await expect(table.getByRole('row', { name: new RegExp(text.summaryFields.estimated_residual_minor) }).getByRole('cell')).toContainText('30'); await expect(table.getByRole('row', { name: new RegExp(text.summaryFields.elapsed_days) }).getByRole('cell')).toContainText('731'); await accessible(page)
})

test('rejects a genuine stale physical profile proof and adopts original context only after explicit current-data review', async ({ page, application }) => {
  await provision(page, application); const { unit } = await seed(page, application.url), writes = watchDomainWrites(page, ['/api/v1/units'])
  await page.goto(`/assets/${unit.id}/edit`); await input(page, text.fields.manual_acquisition_minor).fill('123'); await page.getByRole('button', { name: text.review, exact: true }).click()
  expect((await page.request.patch(`/api/v1/units/${unit.id}`, { headers: await writeContext(page, application.url, unit.revision), data: { notes: 'Concurrent actual physical metadata' } })).status()).toBe(200)
  await page.getByRole('alertdialog').getByRole('button', { name: text.confirms['put-profile'], exact: true }).click(); await expect(page.getByRole('alertdialog')).toContainText(messages.locations.conflict); expect(writes[0]?.proof).toBe('"1"')
  await page.getByRole('alertdialog').getByRole('button', { name: text.cancel, exact: true }).click(); await expect(page.getByRole('alertdialog')).toHaveCount(0); await expect(page.getByText(text.changed, { exact: true })).toBeVisible(); await expect(input(page, text.fields.manual_acquisition_minor)).toHaveValue('123'); await page.getByRole('button', { name: text.keepEdits, exact: true }).click()
  await page.getByRole('button', { name: text.review, exact: true }).click(); await confirm(page, text.confirms['put-profile'], `/assets/${unit.id}`, text.detail)
  expect(writes).toHaveLength(2); expect(writes[1]?.proof).toBe('"2"'); expect(writes[1]?.key).not.toBe(writes[0]?.key); expect(await currentAsset(page, unit.id)).toMatchObject({ revision: 2, manual_acquisition_minor: 123 }); expect(await (await page.request.get(`/api/v1/units/${unit.id}`)).json()).toMatchObject({ revision: 2, notes: 'Concurrent actual physical metadata' })
})

test('requires explicit whole financial adoption while preserving invalid raw cost money without currency conversion', async ({ page, application }) => {
  await provision(page, application); const { item, unit } = await seed(page, application.url), writes = watchDomainWrites(page, ['/api/v1/units'])
  await page.goto(`/assets/${unit.id}/costs/new`); await select(page, text.fields.kind).selectOption('repair'); await select(page, text.fields.direction).selectOption('expense'); await input(page, text.fields.amount_minor).fill('1.5'); await input(page, text.fields.occurred_at).fill(instant)
  const owner = await currentItem(page, item.id); expect((await page.request.put(`/api/v1/units/${unit.id}/asset`, { headers: await writeContext(page, application.url, unit.revision), data: { item_revision: owner.revision, asset_revision: 1, cost_basis: 'manual', currency: 'USD', manual_acquisition_minor: 100, in_service_at: null, retired_at: null, estimated_residual_minor: null, usage_unit: 'times' } })).status()).toBe(200)
  await page.getByRole('button', { name: text.refresh, exact: true }).click(); await expect(page.getByText(text.interpretationChanged, { exact: true })).toBeVisible(); await expect(page.getByRole('button', { name: text.keepEdits, exact: true })).toBeDisabled()
  // Use the native pointer action immediately; do not force/delay past blur geometry.
  await page.getByRole('checkbox', { name: text.acknowledge, exact: true }).check(); await page.getByRole('button', { name: text.keepEdits, exact: true }).click(); await expect(input(page, text.fields.currency)).toHaveValue('USD'); await expect(input(page, text.fields.amount_minor)).toHaveValue('1.5')
  await page.getByRole('button', { name: text.review, exact: true }).click(); await expect(input(page, text.fields.amount_minor)).toBeFocused(); expect(writes).toHaveLength(0); await input(page, text.fields.amount_minor).fill('2'); await page.getByRole('button', { name: text.review, exact: true }).click(); await confirm(page, text.confirms['record-cost'], `/assets/${unit.id}/costs`, text.costs)
  expect(writes).toHaveLength(1); expect(writes[0]?.proof).toBe('"2"'); expect(JSON.parse(writes[0]?.body ?? '{}')).toMatchObject({ currency: 'USD', amount_minor: 2 }); await accessible(page)
})

test('reconciles a genuinely committed malformed +1 reply only by explicit original key after later authoritative cost changes', async ({ page, application }) => {
  await provision(page, application); const { unit } = await seed(page, application.url), writes = watchDomainWrites(page, ['/api/v1/units']); let attempts = 0
  await page.route((url) => url.pathname === `/api/v1/units/${unit.id}/usage`, async (route) => { if (route.request().method() !== 'POST') { await route.continue(); return }; const response = await route.fetch(); expect(response.status()).toBe(200); if (++attempts === 1) await route.fulfill({ response, body: JSON.stringify({ ...await response.json(), delta: 999 }) }); else await route.fulfill({ response }) })
  await page.goto(`/assets/${unit.id}/usage/new`); await page.getByRole('button', { name: text.quickUse, exact: true }).click(); await input(page, text.fields.occurred_at).fill(instant); await page.getByRole('button', { name: text.review, exact: true }).click(); await page.getByRole('alertdialog').getByRole('button', { name: text.confirms['record-usage'], exact: true }).click(); await expect(page.getByRole('alertdialog').getByRole('heading', { name: text.uncertain, exact: true })).toBeVisible(); await page.getByRole('alertdialog').getByRole('button', { name: text.cancel, exact: true }).click()
  const asset = await currentAsset(page, unit.id); expect((await page.request.post(`/api/v1/units/${unit.id}/costs`, { headers: await writeContext(page, application.url, asset.revision), data: { amount_minor: 1, currency: 'JPY', kind: 'fee', direction: 'expense', occurred_at: instant } })).status()).toBe(200)
  await page.getByRole('button', { name: text.readCurrent, exact: true }).click(); await page.evaluate(() => window.dispatchEvent(new Event('online'))); expect(attempts).toBe(1); await expect(input(page, text.fields.delta)).toBeDisabled(); await expect(input(page, text.fields.delta)).toHaveValue('1'); await page.getByRole('button', { name: text.openOriginal, exact: true }).click(); await page.getByRole('alertdialog').getByRole('button', { name: text.retryOriginal, exact: true }).click()
  await expect(page).toHaveURL(`${application.url}/assets/${unit.id}/usage`); await expect(page.getByRole('heading', { name: text.usage, exact: true })).toBeVisible(); await expect(page.getByText(text.done, { exact: true })).toBeVisible(); expect(writes).toHaveLength(2); expect(writes[1]).toEqual(writes[0]); expect(attempts).toBe(2)
  const actual: components['schemas']['UsageList'] = await (await page.request.get(`/api/v1/units/${unit.id}/usage`)).json(); expect(actual.data).toHaveLength(1); expect(actual.data[0]?.delta).toBe(1); expect((await currentAsset(page, unit.id)).revision).toBe(3)
})

test('preserves a volatile offline draft but returns only safe Asset/catalog read scope through genuine login', async ({ page, application }) => {
  const password = await provision(page, application); const { unit } = await seed(page, application.url), writes = watchDomainWrites(page, ['/api/v1/units'])
  await page.goto('/assets?currency=JPY&limit=1'); await page.getByRole('link', { name: `${text.identifier}：${unit.id}`, exact: true }).click(); await page.getByRole('link', { name: text.costs, exact: true }).click(); await page.getByRole('link', { name: text.newCost, exact: true }).click(); await input(page, text.fields.amount_minor).fill('123')
  await page.context().setOffline(true); await expect(page.getByText(text.offline, { exact: true })).toBeVisible(); await input(page, text.fields.amount_minor).fill('0'); await expect(page.getByRole('button', { name: text.review, exact: true })).toBeDisabled(); await page.context().setOffline(false); await expect(page.getByRole('button', { name: text.review, exact: true })).toBeEnabled(); expect(writes).toHaveLength(0)
  expect((await page.request.post('/api/v1/auth/logout', { headers: await writeContext(page, application.url) })).status()).toBe(200); await page.getByRole('button', { name: text.refresh, exact: true }).click(); await expect(page.getByRole('heading', { name: messages.auth.loginTitle, exact: true })).toBeVisible()
  await input(page, messages.auth.username).fill('owner'); await input(page, messages.auth.password).fill(password); await page.getByRole('button', { name: messages.auth.login, exact: true }).click(); await expect(page).toHaveURL(`${application.url}/assets/${unit.id}/costs/new`); await expect(input(page, text.fields.amount_minor)).toHaveValue('')
  const state = await page.evaluate(() => window.history.state.usr); expect(state).toMatchObject({ assetList: '/assets?currency=JPY&limit=1' }); for (const key of ['amount_minor', 'key', 'command', 'csrfToken']) expect(state).not.toHaveProperty(key); expect(writes).toHaveLength(0)
})

test('keeps long Asset reviews reachable and unclipped at 200 percent text in paired light and dark themes', async ({ page, application }, info) => {
  await provision(page, application); const { unit } = await seed(page, application.url, true, '單件資產長名稱'.repeat(25)), writes = watchDomainWrites(page, ['/api/v1/units'])
  await page.goto(`/assets/${unit.id}/costs/new`); await select(page, text.fields.kind).selectOption('repair'); await select(page, text.fields.direction).selectOption('expense'); await input(page, text.fields.amount_minor).fill('5'); await input(page, text.fields.occurred_at).fill(instant); await input(page, text.fields.note).fill('長事件備註 é 😀 '.repeat(180)); await page.locator('html').evaluate((element) => { element.style.fontSize = '200%' })
  for (const theme of ['light', 'dark'] as const) {
    await page.getByRole('button', { name: messages.theme.label, exact: true }).click(); await page.getByRole('menuitemradio', { name: messages.theme[theme], exact: true }).click(); await expect(page.getByRole('menu')).toHaveCount(0); await page.getByRole('button', { name: text.review, exact: true }).click()
    const dialog = page.getByRole('alertdialog'), title = dialog.getByRole('heading', { name: text.reviewTitle, exact: true }), confirm = dialog.getByRole('button', { name: text.confirms['record-cost'], exact: true })
    await expect(title).toBeFocused(); await expect(title).toBeInViewport(); await accessible(page); await page.screenshot({ path: info.outputPath(`asset-review-${theme}.png`) })
    for (let count = 0; count < 12 && !await confirm.evaluate((element) => element === document.activeElement); count++) await page.keyboard.press('Tab')
    await expect(confirm).toBeFocused(); await expect(confirm).toBeInViewport(); expect(await confirm.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true); await page.screenshot({ path: info.outputPath(`asset-confirm-${theme}.png`) }); await page.keyboard.press('Escape'); await expect(dialog).toHaveCount(0)
  }
  expect(writes).toHaveLength(0)
})

test('filters real exact-Unit purchase evidence with signed pages and clears old cursor trails on explicit scope changes', async ({ page, application }) => {
  await provision(page, application)
  const categoryReply = await page.request.post('/api/v1/categories', { headers: await writeContext(page, application.url), data: { name: '資產分類' } }); expect(categoryReply.status()).toBe(200); const category: components['schemas']['Category'] = await categoryReply.json()
  const locationReply = await page.request.post('/api/v1/locations', { headers: await writeContext(page, application.url), data: { name: '資產位置', code: 'ASSET-FILTER' } }); expect(locationReply.status()).toBe(200); const location: components['schemas']['Location'] = await locationReply.json()
  const units: Unit[] = [], merchant = '實際資產商家\n完整名稱'
  for (let index = 0; index < 2; index++) {
    const { item, unit } = await seed(page, application.url, false, `資產篩選 ${index}`, { category_id: category.id, location_id: location.id }); units.push(unit)
    const purchaseReply = await page.request.post('/api/v1/purchases', { headers: await writeContext(page, application.url), data: { merchant_name: merchant, purchased_at: instant, currency: 'JPY' } }); expect(purchaseReply.status()).toBe(200); const purchase: components['schemas']['Purchase'] = await purchaseReply.json()
    const beforeLine = await currentItem(page, item.id); expect((await page.request.post(`/api/v1/purchases/${purchase.id}/lines`, { headers: await writeContext(page, application.url, purchase.revision), data: { item_id: item.id, item_revision: beforeLine.revision, item_unit_id: unit.id, quantity_base: 1, paid_minor: 0 } })).status()).toBe(200)
    const owner = await currentItem(page, item.id); expect((await page.request.put(`/api/v1/units/${unit.id}/asset`, { headers: await writeContext(page, application.url, unit.revision), data: { item_revision: owner.revision, asset_revision: null, cost_basis: 'purchase', currency: 'JPY', manual_acquisition_minor: null, in_service_at: null, retired_at: null, estimated_residual_minor: null, usage_unit: 'times' } })).status()).toBe(200)
  }
  await page.goto('/assets'); await page.getByText(messages.catalog.advanced, { exact: true }).click()
  const cat = messages.catalog
  await input(page, cat.from).fill('2026-11-07'); await input(page, cat.to).fill('2026-11-07'); await input(page, cat.currency).fill('JPY'); await input(page, cat.merchant).fill(merchant); await input(page, cat.pageSize).fill('1'); await page.getByRole('combobox', { name: cat.fields.category_id, exact: true }).selectOption(category.id); await select(page, cat.location).selectOption(location.id); await select(page, cat.scope).selectOption('direct'); await select(page, cat.fields.status).selectOption('active'); await page.getByRole('button', { name: cat.apply, exact: true }).click()
  expect(Object.fromEntries(new URL(page.url()).searchParams)).toMatchObject({ from: '2026-11-07', to: '2026-11-07', currency: 'JPY', merchant, category_id: category.id, location_id: location.id, scope: 'direct', status: 'active', limit: '1' })
  const row = page.getByRole('main').getByRole('link', { name: new RegExp(`^${text.identifier}：`) }); await expect(row).toHaveCount(1); const first = await row.getAttribute('href'); await page.getByRole('button', { name: cat.nextPage, exact: true }).click(); await expect(row).not.toHaveAttribute('href', first!); await expect(page.getByRole('button', { name: cat.nextPage, exact: true })).toBeDisabled()
  await page.getByText(cat.advanced, { exact: true }).click(); await input(page, cat.merchant).fill('明確變更後沒有符合的商家'); await page.getByRole('button', { name: cat.apply, exact: true }).click(); await expect(page.getByRole('heading', { name: text.empty, exact: true })).toBeVisible(); await expect(page.getByRole('button', { name: cat.previousPage, exact: true })).toBeDisabled(); expect(new URL(page.url()).searchParams.has('cursor')).toBe(false)
  expect(units).toHaveLength(2); await accessible(page)
})

test('retains the safe Asset list return through physical Unit navigation without reinterpreting its source', async ({ page, application }) => {
  await provision(page, application); const { unit } = await seed(page, application.url)
  await page.goto('/assets?currency=JPY&limit=1'); await page.getByRole('link', { name: `${text.identifier}：${unit.id}`, exact: true }).click(); await page.getByRole('link', { name: messages.holdings.unitDetail, exact: true }).click(); await page.getByRole('link', { name: text.title, exact: true }).click(); await page.getByRole('link', { name: text.back, exact: true }).click(); await expect(page).toHaveURL(`${application.url}/assets?currency=JPY&limit=1`)
})

test('adopts financial disposal of an already disposed Unit without another physical removal or new Unit revision', async ({ page, application }) => {
  await provision(page, application); const { item, unit } = await seed(page, application.url, false)
  const disposedReply = await page.request.post(`/api/v1/units/${unit.id}/status`, { headers: await writeContext(page, application.url, unit.revision), data: { status: 'disposed', disposal_kind: 'donated' } }); expect(disposedReply.status()).toBe(200); const disposed: Unit = await disposedReply.json(), owner = await currentItem(page, item.id)
  expect((await page.request.put(`/api/v1/units/${unit.id}/asset`, { headers: await writeContext(page, application.url, disposed.revision), data: { item_revision: owner.revision, asset_revision: null, cost_basis: 'manual', currency: 'JPY', manual_acquisition_minor: 100, in_service_at: null, retired_at: null, estimated_residual_minor: null, usage_unit: 'times' } })).status()).toBe(200)
  const writes = watchDomainWrites(page, ['/api/v1/units']); await page.goto(`/assets/${unit.id}/dispose`); await expect(page.getByText(text.noDisposal, { exact: true })).toBeVisible(); await select(page, text.dispose).selectOption('donated'); await input(page, text.fields.realized_proceeds_minor).fill('0'); await input(page, text.fields.disposed_at).fill(instant); await page.getByRole('button', { name: text.review, exact: true }).click(); await confirm(page, text.confirms['dispose-asset'], `/assets/${unit.id}`, text.detail)
  expect(await (await page.request.get(`/api/v1/units/${unit.id}`)).json()).toEqual(disposed); expect(writes[0]?.proof).toBe('"1"'); expect(JSON.parse(writes[0]?.body ?? '{}')).toMatchObject({ unit_revision: 2, realized_proceeds_minor: 0 }); expect(await (await page.request.get(`/api/v1/units/${unit.id}/disposal`)).json()).toMatchObject({ revision: 1, kind: 'donated', realized_proceeds_minor: 0 })
})

test('does not mistake financially voided disposal for absent history or recreate its physical decision', async ({ page, application }) => {
  await provision(page, application); const { unit } = await seed(page, application.url), path = `/api/v1/units/${unit.id}`
  const originalReply = await page.request.post(`${path}/disposal`, { headers: await writeContext(page, application.url, (await currentAsset(page, unit.id)).revision), data: { unit_revision: unit.revision, kind: 'sold', disposed_at: instant, currency: 'JPY', realized_proceeds_minor: 200 } }); expect(originalReply.status()).toBe(200)
  const original: components['schemas']['Disposal'] = await originalReply.json(), beforeReply = await page.request.get(`${path}/disposal-record`); expect(beforeReply.status()).toBe(200)
  const before: components['schemas']['DisposalRecord'] = await beforeReply.json(); expect(beforeReply.headers().etag).toBe(`"${before.revision}"`); expect(before.current).toMatchObject({ realized_proceeds_minor: 200 })
  const physical: Unit = await (await page.request.get(path)).json(), asset = await currentAsset(page, unit.id); expect(physical).toMatchObject({ status: 'disposed', revision: 2 })
  const voidReply = await page.request.post(`${path}/disposal-corrections`, { headers: await writeContext(page, application.url, before.revision), data: { asset_revision: asset.revision, action: 'void', replacement: null, reason: 'Financial decision voided for review' } }); expect(voidReply.status()).toBe(200)
  const decision: components['schemas']['DisposalCorrection'] = await voidReply.json(); expect(decision.before).toEqual(before); expect(decision.after.current).toBeNull(); expect(decision.after.revision).toBe(before.revision + 1)
  expect((await page.request.get(`${path}/disposal`)).status()).toBe(404)
  const retainedReply = await page.request.get(`${path}/disposal-record`); expect(retainedReply.status()).toBe(200); expect(retainedReply.headers().etag).toBe(`"${decision.after.revision}"`)
  const retained: components['schemas']['DisposalRecord'] = await retainedReply.json(); expect(retained).toEqual(decision.after); expect(retained.original).toMatchObject({ item_unit_id: unit.id, kind: original.kind, realized_proceeds_minor: 200 }); expect(retained.current).toBeNull()
  const historyReply = await page.request.get(`${path}/disposal-corrections`); expect(historyReply.status()).toBe(200)
  const history: components['schemas']['DisposalCorrectionList'] = await historyReply.json(); expect(history.data).toEqual([decision]); expect(history.next_cursor).toBeNull()
  const current = await currentAsset(page, unit.id), writes = watchDomainWrites(page, ['/api/v1/units'])
  await page.goto(`/assets/${unit.id}/dispose`); await expect(page.getByText('目前沒有生效的處置財務決定；這不代表沒有歷史紀錄。', { exact: true })).toBeVisible(); await expect(page.getByText(text.disposalHelp, { exact: true }).first()).toBeVisible()
  await select(page, text.dispose).selectOption('sold'); await input(page, text.fields.realized_proceeds_minor).fill('100'); await input(page, text.fields.disposed_at).fill(instant); await page.getByRole('button', { name: text.review, exact: true }).click()
  const dialog = page.getByRole('alertdialog'); await expect(dialog.getByText(text.noDisposal, { exact: true })).toBeVisible(); await accessible(page)
  const rejection = page.waitForResponse((response) => new URL(response.url()).pathname === `${path}/disposal` && response.request().method() === 'POST')
  await dialog.getByRole('button', { name: text.confirms['dispose-asset'], exact: true }).click(); const response = await rejection
  expect(response.status()).toBe(409); expect((await response.json()).error.code).toBe('ASSET_HISTORY_LOCKED'); await expect(dialog.getByText(text.historyLocked, { exact: true })).toBeVisible()
  expect(writes).toHaveLength(1); expect(writes[0]?.proof).toBe(`"${current.revision}"`); expect(JSON.parse(writes[0]?.body ?? '{}')).toMatchObject({ unit_revision: physical.revision })
  expect(await (await page.request.get(path)).json()).toEqual(physical); expect(await currentAsset(page, unit.id)).toEqual(current); expect(await (await page.request.get(`${path}/disposal-record`)).json()).toEqual(retained); expect(await (await page.request.get(`${path}/disposal-corrections`)).json()).toEqual(history)
})

test('keeps exact maximum-safe financial table values readable at 200 percent text in both themes', async ({ page, application }, info) => {
  await provision(page, application); const { item, unit } = await seed(page, application.url), owner = await currentItem(page, item.id)
  expect((await page.request.put(`/api/v1/units/${unit.id}/asset`, { headers: await writeContext(page, application.url, unit.revision), data: { item_revision: owner.revision, asset_revision: 1, cost_basis: 'manual', currency: 'JPY', manual_acquisition_minor: Number.MAX_SAFE_INTEGER, in_service_at: null, retired_at: null, estimated_residual_minor: 0, usage_unit: 'times' } })).status()).toBe(200)
  await page.goto(`/assets/${unit.id}/summary`); const table = page.getByRole('table'); await expect(table).toBeVisible(); await page.locator('html').evaluate((element) => { element.style.fontSize = '200%' })
  for (const theme of ['light', 'dark'] as const) {
    await page.getByRole('button', { name: messages.theme.label, exact: true }).click(); await page.getByRole('menuitemradio', { name: messages.theme[theme], exact: true }).click(); await expect(page.getByRole('menu')).toHaveCount(0)
    const row = table.getByRole('row', { name: new RegExp(text.summaryFields.net_cost_minor) }), net = row.getByRole('cell')
    await expect(table.getByRole('columnheader')).toHaveCount(2); await expect(table.getByRole('rowheader')).toHaveCount(12)
    await expect(net).toContainText(String(Number.MAX_SAFE_INTEGER)); expect(await net.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)
    const header = (await row.getByRole('rowheader').boundingBox())!, value = (await net.boundingBox())!, bounds = (await table.boundingBox())!
    if (page.viewportSize()!.width < 640) {
      expect(value.y).toBeGreaterThanOrEqual(header.y + header.height - 1); expect(value.width).toBeGreaterThan(bounds.width * 0.9)
      expect(await net.evaluate((element) => { const digits = document.createRange(); digits.selectNodeContents(element.firstChild!); return digits.getClientRects().length })).toBeLessThanOrEqual(3)
    } else expect(value.x).toBeGreaterThanOrEqual(header.x + header.width - 1)
    await accessible(page); await table.screenshot({ path: info.outputPath(`asset-calculation-${theme}.png`) })
  }
})

test.describe('complete retained Asset event review', () => {
  test.use({ applicationDatabaseFixture: { prepare(database) {
    // Prepare actual legacy source offline, then close it before the real Go CLI
    // validates/bootstrap/serves. Never invent receipts or bypass HTTP admission.
    const db = new DatabaseSync(database), now = '2026-11-07T00:00:00Z'
    try {
      db.exec('PRAGMA foreign_keys=ON; BEGIN IMMEDIATE;')
      const counter = db.prepare('INSERT INTO projection_revisions(entity_type,entity_id,revision) VALUES(?,?,1)'), current = db.prepare('INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at) VALUES(?,?,1,?,?)')
      const baseline = (kind: string, id: string, payload: object) => { counter.run(kind, id); current.run(kind, id, JSON.stringify(payload), now) }
      const item = { id: 'legacy-asset-item', name: '實際既有資產物品', brand: null, model: null, category_id: null, description: '', notes: '', tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1, status: 'active' }
      db.prepare('INSERT INTO items(id,name,tracking_mode,created_at,updated_at) VALUES(?,?,?,?,?)').run(item.id, item.name, item.tracking_mode, now, now); baseline('item', item.id, item); counter.run('search_doc', JSON.stringify(['item', item.id]))
      const unit = { id: 'new', item_id: item.id, asset_code: 'AST-legacy-new', location_id: null, serial_number: null, notes: '', status: 'stored' }
      db.prepare('INSERT INTO item_units(id,item_id,asset_code,created_at,updated_at) VALUES(?,?,?,?,?)').run(unit.id, unit.item_id, unit.asset_code, now, now); baseline('unit', unit.id, unit); counter.run('search_doc', JSON.stringify(['unit', unit.id]))
      const asset = { item_unit_id: unit.id, cost_basis: 'manual', manual_acquisition_minor: 100, currency: 'JPY', in_service_at: null, retired_at: null, estimated_residual_minor: null, usage_unit: 'times' }
      db.prepare('INSERT INTO asset_profiles(item_unit_id,cost_basis,manual_acquisition_minor,currency,created_at,updated_at) VALUES(?,?,?,?,?,?)').run(unit.id, asset.cost_basis, asset.manual_acquisition_minor, asset.currency, now, now); baseline('asset', unit.id, asset)
      const append = db.prepare('INSERT INTO asset_usage_events(id,item_unit_id,occurred_at,delta,usage_unit,event_type,note,created_at) VALUES(?,?,?,?,?,?,?,?)')
      for (let index = 0; index < 205; index++) {
        const event = { id: `legacy-${String(index).padStart(3, '0')}`, item_unit_id: unit.id, occurred_at: now, delta: 1, usage_unit: 'times', event_type: 'use', note: 'Retained actual source', correction_of: null }
        append.run(event.id, event.item_unit_id, now, event.delta, event.usage_unit, event.event_type, event.note, now); baseline('usage', event.id, event)
      }
      db.exec('COMMIT'); expect(db.prepare('PRAGMA foreign_key_check').all()).toEqual([])
    } finally { db.close() }
  } } })
  test('completes 205 real events for an imported new Unit and corrects an actual target beyond the first page', async ({ page, application }) => {
    await provision(page, application); const reads: URL[] = [], writes = watchDomainWrites(page, ['/api/v1/units'])
    page.on('request', (request) => { const url = new URL(request.url()); if (request.method() === 'GET' && url.pathname === '/api/v1/units/new/usage') reads.push(url) })
    await page.goto('/assets/new/usage'); await expect(page.getByText(`${text.eventCount}205`, { exact: true })).toBeVisible(); expect(reads.filter((url) => url.searchParams.has('cursor'))).toHaveLength(2)
    await input(page, text.eventSearch).fill('legacy-204'); await expect(page.getByText(new RegExp(`${text.eventID}：legacy-204`))).toBeVisible(); await page.getByRole('link', { name: text.newUsage, exact: true }).click(); await select(page, text.fields.event_type).selectOption('correction'); await select(page, text.fields.correction_of).selectOption('legacy-204'); await input(page, text.fields.delta).fill('-1'); await input(page, text.fields.occurred_at).fill(instant); await page.getByRole('button', { name: text.review, exact: true }).click(); await expect(page.getByRole('alertdialog')).toContainText('legacy-204'); await confirm(page, text.confirms['record-usage'], '/assets/new/usage', text.usage)
    await expect(page.getByText(`${text.eventCount}206`, { exact: true })).toBeVisible(); expect(writes).toHaveLength(1); expect(JSON.parse(writes[0]?.body ?? '{}')).toMatchObject({ correction_of: 'legacy-204', delta: -1 }); await accessible(page)
  })
})
