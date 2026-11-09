import AxeBuilder from '@axe-core/playwright'
import type { Page } from '@playwright/test'
import type { components } from '../../src/lib/api/schema.d.ts'
import { messages } from '../../src/lib/messages.ts'
import { test, expect } from './auth-fixture.ts'
import { noOverflow, provision, watchDomainWrites, writeContext } from './domain-helpers.ts'

const text = messages.disposalCorrections, assetText = messages.assets
const instant = '2026-11-07T10:00:00.123456789+08:00'
const input = (page: Page, label: string) => page.getByLabel(new RegExp(`^${label}`))
const select = (page: Page, label: string) => page.getByRole('combobox', { name: new RegExp(`^${label}`) })
type Item = components['schemas']['Item']
type Unit = components['schemas']['Unit']
type Asset = components['schemas']['Asset']
type Record = components['schemas']['DisposalRecord']
type History = components['schemas']['DisposalCorrectionList']
test.use({ trace: 'off', screenshot: 'off', video: 'off' })

async function seed(page: Page, origin: string, name = '財務處置實際個體', note = '原始處置紀錄') {
  const created = await page.request.post('/api/v1/items', { headers: await writeContext(page, origin), data: { name } }); expect(created.status()).toBe(200); const item: Item = await created.json()
  const physical = await page.request.post(`/api/v1/items/${item.id}/units`, { headers: await writeContext(page, origin, item.revision), data: { location_id: null, notes: 'Physical source remains unchanged' } }); expect(physical.status()).toBe(200); const unit: Unit = await physical.json()
  const owner: Item = await (await page.request.get(`/api/v1/items/${item.id}`)).json()
  const profile = await page.request.put(`/api/v1/units/${unit.id}/asset`, { headers: await writeContext(page, origin, unit.revision), data: { item_revision: owner.revision, asset_revision: null, cost_basis: 'manual', currency: 'JPY', manual_acquisition_minor: 100, in_service_at: null, retired_at: null, estimated_residual_minor: 30, usage_unit: 'times' } }); expect(profile.status()).toBe(200)
  const disposal = await page.request.post(`/api/v1/units/${unit.id}/disposal`, { headers: await writeContext(page, origin, 1), data: { unit_revision: unit.revision, kind: 'sold', currency: 'JPY', realized_proceeds_minor: 200, disposed_at: instant, note } }); expect(disposal.status()).toBe(200)
  const retained: Record = await (await page.request.get(`/api/v1/units/${unit.id}/disposal-record`)).json()
  const currentUnit: Unit = await (await page.request.get(`/api/v1/units/${unit.id}`)).json()
  return { item, unit: currentUnit, retained }
}
async function record(page: Page, unit: string): Promise<Record> { const response = await page.request.get(`/api/v1/units/${unit}/disposal-record`); expect(response.status()).toBe(200); return response.json() }
async function currentAsset(page: Page, unit: string): Promise<Asset> { const response = await page.request.get(`/api/v1/units/${unit}/asset`); expect(response.status()).toBe(200); return response.json() }
async function history(page: Page, unit: string): Promise<History> { const response = await page.request.get(`/api/v1/units/${unit}/disposal-corrections`); expect(response.status()).toBe(200); return response.json() }
async function accessible(page: Page) { expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze()).violations).toEqual([]); await noOverflow(page) }
async function confirm(page: Page, label: string, unit: string) {
  const dialog = page.getByRole('alertdialog'), title = dialog.getByRole('heading', { name: text.reviewTitle, exact: true })
  await expect(title).toBeFocused(); await expect(title).toBeInViewport(); await dialog.getByRole('button', { name: label, exact: true }).click()
  await expect(dialog).toHaveCount(0); await expect(page).toHaveURL(new RegExp(`/assets/${unit}/disposal-corrections$`)); await expect(page.getByRole('heading', { name: text.title, exact: true })).toBeVisible()
  await expect(page.getByText(text.done, { exact: true })).toBeVisible(); await expect(page.getByRole('button', { name: text.review, exact: true })).toBeEnabled()
}

test('reviews real zero replacement and financial void without changing the physical Unit', async ({ page, application }) => {
  await provision(page, application); const { unit, retained } = await seed(page, application.url), writes = watchDomainWrites(page, ['/api/v1/units', '/api/v1/stock'])
  await page.goto(`/assets/${unit.id}`); await page.getByRole('link', { name: text.title, exact: true }).click(); await expect(page.getByRole('heading', { name: text.title, exact: true })).toBeVisible()
  await expect(page.getByText(text.noHistory, { exact: true })).toBeVisible(); await expect(select(page, text.action)).toHaveValue('')
  await select(page, text.action).selectOption('replace'); await input(page, text.reason).fill('更正為明確零元'); await select(page, text.kind).selectOption('donated')
  await expect(input(page, text.proceeds)).toHaveValue(''); await expect(input(page, text.disposedAt)).toHaveValue(''); await page.getByRole('button', { name: text.review, exact: true }).click(); await expect(input(page, text.proceeds)).toBeFocused(); expect(writes).toHaveLength(0)
  await input(page, text.proceeds).fill('0'); await input(page, text.disposedAt).fill(instant); await input(page, text.note).fill('  零元處置 é\n  '); await page.getByRole('button', { name: text.review, exact: true }).click(); await accessible(page); await confirm(page, text.confirmReplace, unit.id)
  const zero = await record(page, unit.id); expect(zero).toMatchObject({ revision: retained.revision + 1, original: retained.original, current: { kind: 'donated', realized_proceeds_minor: 0, disposed_at: retained.original.disposed_at } })
  await select(page, text.action).selectOption('void'); await input(page, text.reason).fill('明確作廢財務決定'); await page.getByRole('button', { name: text.review, exact: true }).click(); await confirm(page, text.confirmVoid, unit.id)
  const voided = await record(page, unit.id); expect(voided).toMatchObject({ revision: zero.revision + 1, original: retained.original, current: null }); expect((await page.request.get(`/api/v1/units/${unit.id}/disposal`)).status()).toBe(404)
  await expect(page.getByRole('heading', { name: text.current }).locator('..').getByText(text.voided, { exact: true })).toBeVisible(); await expect(select(page, text.action).locator('option[value="void"]')).toBeDisabled()
  const entries = await history(page, unit.id); expect(entries.data).toHaveLength(2); expect(await (await page.request.get(`/api/v1/units/${unit.id}`)).json()).toEqual(unit)
  expect(writes).toHaveLength(2); expect(writes.map((value) => value.proof)).toEqual([`"${retained.revision}"`, `"${zero.revision}"`])
  for (const request of writes) expect(JSON.parse(request.body ?? '{}')).not.toHaveProperty('unit_revision')
  await accessible(page)
})

test('explicitly reinstates a previously voided record with its real subject proof', async ({ page, application }) => {
  await provision(page, application); const { unit, retained } = await seed(page, application.url), profile = await currentAsset(page, unit.id)
  const earlier = await page.request.post(`/api/v1/units/${unit.id}/disposal-corrections`, { headers: await writeContext(page, application.url, retained.revision), data: { asset_revision: profile.revision, action: 'void', replacement: null, reason: 'Previously voided finance' } }); expect(earlier.status()).toBe(200)
  const voided = await record(page, unit.id), current = await currentAsset(page, unit.id), writes = watchDomainWrites(page, ['/api/v1/units', '/api/v1/stock'])
  expect(voided.current).toBeNull(); await page.goto(`/assets/${unit.id}/disposal-corrections`); await expect(page.getByRole('heading', { name: text.current }).locator('..').getByText(text.voided, { exact: true })).toBeVisible()
  await select(page, text.action).selectOption('replace'); await input(page, text.reason).fill('明確重新採認'); await select(page, text.kind).selectOption('sold'); await input(page, text.proceeds).fill('300'); await input(page, text.disposedAt).fill('2026-11-07T02:00:00.123456789Z')
  await page.getByRole('button', { name: text.review, exact: true }).click(); await confirm(page, text.confirmReinstate, unit.id)
  expect(await record(page, unit.id)).toMatchObject({ revision: voided.revision + 1, original: retained.original, current: { realized_proceeds_minor: 300, disposed_at: retained.original.disposed_at } })
  expect((await history(page, unit.id)).data).toHaveLength(2); expect(await (await page.request.get(`/api/v1/units/${unit.id}`)).json()).toEqual(unit)
  expect(writes).toHaveLength(1); expect(writes[0]?.proof).toBe(`"${voided.revision}"`); expect(JSON.parse(writes[0]?.body ?? '{}')).toMatchObject({ asset_revision: current.revision, action: 'replace' }); expect(JSON.parse(writes[0]?.body ?? '{}')).not.toHaveProperty('unit_revision')
})

test('retains a stale original proof until explicit adoption of a real later financial decision', async ({ page, application }) => {
  await provision(page, application); const { unit, retained } = await seed(page, application.url), writes = watchDomainWrites(page, ['/api/v1/units', '/api/v1/stock'])
  await page.goto(`/assets/${unit.id}/disposal-corrections`); await select(page, text.action).selectOption('replace'); await input(page, text.reason).fill('保留草稿原因'); await select(page, text.kind).selectOption('sold'); await input(page, text.proceeds).fill('50'); await input(page, text.disposedAt).fill(instant); await page.getByRole('button', { name: text.review, exact: true }).click()
  const before = await currentAsset(page, unit.id)
  const concurrent = await page.request.post(`/api/v1/units/${unit.id}/disposal-corrections`, { headers: await writeContext(page, application.url, retained.revision), data: { asset_revision: before.revision, action: 'replace', replacement: { kind: 'sold', currency: 'JPY', realized_proceeds_minor: 300, disposed_at: instant }, reason: 'Concurrent decision' } }); expect(concurrent.status()).toBe(200)
  await page.getByRole('alertdialog').getByRole('button', { name: text.confirmReplace, exact: true }).click(); await expect(page.getByRole('alertdialog')).toContainText(messages.locations.conflict)
  await page.getByRole('alertdialog').getByRole('button', { name: text.cancel, exact: true }).click(); await expect(page.getByRole('alertdialog')).toHaveCount(0); await expect(page.getByText(text.changed, { exact: true })).toBeVisible()
  await input(page, text.proceeds).fill('1.5'); await page.getByRole('button', { name: text.keepEdits, exact: true }).click(); await expect(input(page, text.proceeds)).toHaveValue('1.5'); await expect(input(page, text.reason)).toHaveValue('保留草稿原因')
  await page.getByRole('button', { name: text.review, exact: true }).click(); await expect(input(page, text.proceeds)).toBeFocused(); expect(writes).toHaveLength(1)
  const current = await record(page, unit.id), currentProfile = await currentAsset(page, unit.id)
  await input(page, text.proceeds).fill('0'); await page.getByRole('button', { name: text.review, exact: true }).click(); await confirm(page, text.confirmReplace, unit.id)
  expect(writes).toHaveLength(2); expect(writes[0]?.proof).toBe(`"${retained.revision}"`); expect(writes[1]?.proof).toBe(`"${current.revision}"`); expect(writes[1]?.key).not.toBe(writes[0]?.key)
  expect(JSON.parse(writes[1]?.body ?? '{}')).toMatchObject({ asset_revision: currentProfile.revision, replacement: { realized_proceeds_minor: 0 } }); expect(await (await page.request.get(`/api/v1/units/${unit.id}`)).json()).toEqual(unit)
})

test('keeps an uncertain committed original command available after current-source reads fail', async ({ page, application }) => {
  await provision(page, application); const { unit } = await seed(page, application.url), writes = watchDomainWrites(page, ['/api/v1/units', '/api/v1/stock']); let attempts = 0, blockRecord = false
  await page.route((url) => url.pathname === `/api/v1/units/${unit.id}/disposal-record`, async (route) => { if (blockRecord) await route.abort('failed'); else await route.continue() })
  await page.route((url) => url.pathname === `/api/v1/units/${unit.id}/disposal-corrections`, async (route) => {
    if (route.request().method() !== 'POST') { await route.continue(); return }
    const response = await route.fetch(); expect(response.status()).toBe(200)
    if (++attempts === 1) { blockRecord = true; const body = await response.json(); body.after.current.realized_proceeds_minor = 1; await route.fulfill({ response, body: JSON.stringify(body) }) }
    else await route.fulfill({ response })
  })
  await page.goto(`/assets/${unit.id}/disposal-corrections`); await select(page, text.action).selectOption('replace'); await input(page, text.reason).fill('核對原始請求'); await select(page, text.kind).selectOption('sold'); await input(page, text.proceeds).fill('0'); await input(page, text.disposedAt).fill(instant)
  await page.getByRole('button', { name: text.review, exact: true }).click(); await page.getByRole('alertdialog').getByRole('button', { name: text.confirmReplace, exact: true }).click()
  await expect(page.getByRole('alertdialog').getByRole('heading', { name: text.uncertain, exact: true })).toBeVisible(); await page.getByRole('alertdialog').getByRole('button', { name: text.cancel, exact: true }).click(); await expect(page.getByRole('alertdialog')).toHaveCount(0)
  await expect(page.getByText(assetText.failed, { exact: true })).toBeVisible(); await page.evaluate(() => window.dispatchEvent(new Event('online'))); expect(attempts).toBe(1)
  await page.getByRole('button', { name: text.openOriginal, exact: true }).click(); await page.getByRole('alertdialog').getByRole('button', { name: text.retryOriginal, exact: true }).click(); await expect(page.getByRole('alertdialog')).toHaveCount(0)
  await expect(page.getByText(text.done, { exact: true })).toBeVisible(); expect(attempts).toBe(2); expect(writes).toHaveLength(2); expect(writes[1]).toEqual(writes[0])
  blockRecord = false; await page.getByRole('button', { name: assetText.refresh, exact: true }).click(); await expect(page.getByText(`${text.historyCount}1`, { exact: true })).toBeVisible()
  expect((await record(page, unit.id)).current).toMatchObject({ realized_proceeds_minor: 0 }); expect(await (await page.request.get(`/api/v1/units/${unit.id}`)).json()).toEqual(unit)
})

test('keeps a volatile offline draft but preserves only safe scope through actual login', async ({ page, application }) => {
  const password = await provision(page, application); const { unit } = await seed(page, application.url), writes = watchDomainWrites(page, ['/api/v1/units'])
  await page.goto('/assets?currency=JPY&limit=1'); await page.getByRole('link', { name: `${assetText.identifier}：${unit.id}`, exact: true }).click(); await page.getByRole('link', { name: text.title, exact: true }).click()
  await select(page, text.action).selectOption('void'); await input(page, text.reason).fill('不應進入登入返回狀態')
  await page.context().setOffline(true); await expect(page.getByText(text.offline, { exact: true })).toBeVisible(); await expect(input(page, text.reason)).toHaveValue('不應進入登入返回狀態'); await expect(page.getByRole('button', { name: text.review, exact: true })).toBeDisabled()
  await page.context().setOffline(false); await expect(page.getByRole('button', { name: text.review, exact: true })).toBeEnabled(); expect(writes).toHaveLength(0)
  expect((await page.request.post('/api/v1/auth/logout', { headers: await writeContext(page, application.url) })).status()).toBe(200); await page.getByRole('button', { name: assetText.refresh, exact: true }).click(); await expect(page.getByRole('heading', { name: messages.auth.loginTitle, exact: true })).toBeVisible()
  await input(page, messages.auth.username).fill('owner'); await input(page, messages.auth.password).fill(password); await page.getByRole('button', { name: messages.auth.login, exact: true }).click()
  await expect(page).toHaveURL(`${application.url}/assets/${unit.id}/disposal-corrections`); await expect(input(page, text.reason)).toHaveValue('')
  const state = await page.evaluate(() => window.history.state.usr); expect(state).toMatchObject({ assetList: '/assets?currency=JPY&limit=1' }); for (const key of ['reason', 'key', 'command', 'csrfToken']) expect(state).not.toHaveProperty(key); expect(writes).toHaveLength(0)
})

test('keeps long review and confirmation accessible at 200 percent text in both themes', async ({ page, application }, info) => {
  await provision(page, application); const { unit } = await seed(page, application.url, '財務處置長物品名稱'.repeat(25), '原始處置備註 é 😀 '.repeat(150)), writes = watchDomainWrites(page, ['/api/v1/units'])
  await page.goto(`/assets/${unit.id}/disposal-corrections`); await select(page, text.action).selectOption('replace'); await input(page, text.reason).fill('長財務更正原因 é 😀 '.repeat(150)); await select(page, text.kind).selectOption('sold'); await input(page, text.proceeds).fill('0'); await input(page, text.disposedAt).fill(instant); await input(page, text.note).fill('新的財務備註 é 😀 '.repeat(150)); await page.locator('html').evaluate((element) => { element.style.fontSize = '200%' })
  for (const theme of ['light', 'dark'] as const) {
    await page.getByRole('button', { name: messages.theme.label, exact: true }).click(); await page.getByRole('menuitemradio', { name: messages.theme[theme], exact: true }).click(); await expect(page.getByRole('menu')).toHaveCount(0)
    await page.evaluate(() => window.scrollTo(0, 0)); await noOverflow(page)
    const brand = page.getByRole('link', { name: messages.app.home, exact: true })
    expect(await brand.evaluate((element) => { const text = document.createRange(); text.selectNodeContents(element.querySelector('span')!); return element.scrollWidth <= element.clientWidth && element.getBoundingClientRect().right <= window.innerWidth && text.getClientRects().length === 1 })).toBe(true)
    await page.screenshot({ path: info.outputPath(`disposal-page-${theme}.png`) }); const heading = page.getByRole('heading', { name: text.title, exact: true }); await heading.scrollIntoViewIfNeeded()
    expect(await heading.evaluate((element) => { const range = document.createRange(); range.selectNodeContents(element); return range.getClientRects().length })).toBeLessThanOrEqual(2)
    await page.screenshot({ path: info.outputPath(`disposal-content-${theme}.png`) }); await page.getByRole('button', { name: text.review, exact: true }).click()
    const dialog = page.getByRole('alertdialog'), title = dialog.getByRole('heading', { name: text.reviewTitle, exact: true }), confirm = dialog.getByRole('button', { name: text.confirmReplace, exact: true })
    await expect(title).toBeFocused(); await expect(title).toBeInViewport(); await accessible(page); await page.screenshot({ path: info.outputPath(`disposal-review-${theme}.png`) })
    for (let count = 0; count < 12 && !await confirm.evaluate((element) => element === document.activeElement); count++) await page.keyboard.press('Tab')
    await expect(confirm).toBeFocused(); await expect(confirm).toBeInViewport(); expect(await confirm.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true); await page.screenshot({ path: info.outputPath(`disposal-confirm-${theme}.png`) }); await page.keyboard.press('Escape'); await expect(dialog).toHaveCount(0)
  }
  await page.getByRole('link', { name: assetText.backDetail, exact: true }).click(); const leave = page.getByRole('alertdialog'); await expect(leave.getByText(text.leaveHelp, { exact: true })).toBeVisible(); await leave.getByRole('button', { name: text.stay, exact: true }).click(); await expect(input(page, text.proceeds)).toHaveValue('0'); expect(writes).toHaveLength(0)
})
