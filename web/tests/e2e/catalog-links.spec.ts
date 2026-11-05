import AxeBuilder from '@axe-core/playwright'
import { DatabaseSync } from 'node:sqlite'
import type { Page } from '@playwright/test'
import type { components } from '../../src/lib/api/schema.d.ts'
import { messages } from '../../src/lib/messages.ts'
import { test, expect } from './auth-fixture.ts'
import { noOverflow, provision, watchDomainWrites, writeContext } from './domain-helpers.ts'

test.use({ trace: 'off', screenshot: 'off', video: 'off' })
const text = messages.owned
type Item = components['schemas']['Item']
type Tag = components['schemas']['Tag']
type Alias = components['schemas']['Alias']
const input = (page: Page, label: string) => page.getByLabel(new RegExp(`^${label}`))
const watch = (page: Page) => watchDomainWrites(page, ['/api/v1/items', '/api/v1/barcodes', '/api/v1/aliases', '/api/v1/attributes'])
async function seedItem(page: Page, origin: string, name = 'Owned maintenance fixture'): Promise<Item> {
  const response = await page.request.post('/api/v1/items', { headers: await writeContext(page, origin), data: { name } })
  expect(response.status()).toBe(200)
  return response.json()
}
async function seedTag(page: Page, origin: string, name: string): Promise<Tag> {
  const response = await page.request.post('/api/v1/tags', { headers: await writeContext(page, origin), data: { name } })
  expect(response.status()).toBe(200)
  return response.json()
}
async function seedAlias(page: Page, origin: string, owner: Item, name = 'Original alias'): Promise<Alias> {
  const response = await page.request.post(`/api/v1/items/${owner.id}/aliases`, { headers: await writeContext(page, origin, owner.revision), data: { alias: name } })
  expect(response.status()).toBe(200)
  return response.json()
}
async function confirm(page: Page, label: string, title: string = text.reviewTitle) {
  const dialog = page.getByRole('alertdialog')
  await expect(dialog.getByRole('heading', { name: title, exact: true })).toBeFocused()
  await expect(dialog.getByRole('heading', { name: title, exact: true })).toBeInViewport()
  await dialog.getByRole('button', { name: label, exact: true }).click()
  await expect(page.getByText(text.done, { exact: true })).toBeVisible()
  await expect(dialog).toHaveCount(0)
}
async function accessible(page: Page) {
  expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze()).violations).toEqual([])
  await noOverflow(page)
}
async function collection(page: Page, kind: 'barcodes' | 'aliases' | 'attributes' | 'tags') {
  await page.getByRole('navigation', { name: text.navigation }).getByRole('link', { name: text.collections[kind], exact: true }).click()
}

for (const spec of [
  { kind: 'barcodes', first: { symbology: 'UPC_A', raw_value: '036000291452' }, changed: { symbology: 'UPC_E', raw_value: '04252614' }, invalid: 'raw_value' },
  { kind: 'aliases', first: { alias: '  家用 é\n別名  ' }, changed: { alias: '  Reviewed é  ' }, invalid: 'alias' },
  { kind: 'attributes', first: { attribute_key: '重複欄位', attribute_value: 'Original content' }, changed: { attribute_value: '' }, invalid: 'attribute_key' },
] as const) {
  test(`maintains independent ${spec.kind} through real reviewed screens and scoped returns`, async ({ page, application }, info) => {
    await provision(page, application)
    const owner = await seedItem(page, application.url)
    const writes = watch(page)
    await page.goto('/items?q=Owned&limit=1')
    await page.getByRole('link', { name: new RegExp(owner.name) }).click()
    await collection(page, spec.kind)
    let ownerRevision = owner.revision
    await page.getByRole('link', { name: text.create[spec.kind], exact: true }).click()
    if (spec.kind === 'barcodes') {
      await page.getByRole('button', { name: text.review, exact: true }).click()
      await expect(input(page, text.fields.symbology)).toBeFocused()
    }
    for (const [key, value] of Object.entries(spec.first)) {
      const field = input(page, text.fields[key as keyof typeof text.fields])
      if (key === 'symbology') await field.selectOption(value); else await field.fill(value)
    }
    const before = writes.length
    await page.getByRole('button', { name: text.review, exact: true }).click()
    expect(writes).toHaveLength(before)
    await expect(page.getByRole('alertdialog')).toContainText(`${text.ownerProof}${ownerRevision}`)
    await accessible(page)
    await confirm(page, text.confirmCreate[spec.kind])
    expect(writes.at(-1)?.proof).toBe(JSON.stringify(String(ownerRevision++)))
    expect(JSON.parse(writes.at(-1)?.body ?? '{}')).toEqual(spec.first)
    const created: { data: { id: string; revision: number; raw_value?: string; normalized_value?: string }[] } = await (await page.request.get(`/api/v1/items/${owner.id}/${spec.kind}`)).json()
    const row = created.data[0]!
    expect(row.revision).toBe(1)
    if (spec.kind === 'barcodes') {
      expect(row).toMatchObject({ raw_value: '036000291452', normalized_value: '036000291452' })
      for (const colorScheme of ['light', 'dark'] as const) {
        await page.emulateMedia({ colorScheme })
        await expect(page.locator('html')).toHaveAttribute('data-theme', colorScheme)
        await accessible(page)
        await page.screenshot({ path: info.outputPath(`owned-list-${colorScheme}.png`), fullPage: true })
      }
    }
    if (spec.kind === 'attributes') {
      await page.getByRole('link', { name: text.create.attributes, exact: true }).click()
      await input(page, text.fields.attribute_key).fill(spec.first.attribute_key)
      await input(page, text.fields.attribute_value).fill('Another independent row')
      await page.getByRole('button', { name: text.review, exact: true }).click()
      await confirm(page, text.confirmCreate.attributes)
      expect(writes.at(-1)?.proof).toBe(JSON.stringify(String(ownerRevision++)))
      await expect(page.getByText(`${text.count}2`, { exact: true })).toBeVisible()
    }
    await page.getByRole('link', { name: `${text.edit[spec.kind]}：${row.id}`, exact: true }).click()
    for (const [key, value] of Object.entries(spec.changed)) {
      const field = input(page, text.fields[key as keyof typeof text.fields])
      if (key === 'symbology') await field.selectOption(value); else await field.fill(value)
    }
    await page.getByRole('button', { name: text.review, exact: true }).click()
    await expect(page.getByRole('alertdialog')).toContainText(`${text.rowProof}1`)
    await confirm(page, text.confirmSave)
    ownerRevision++
    expect(writes.at(-1)?.proof).toBe('"1"')
    expect(JSON.parse(writes.at(-1)?.body ?? '{}')).toEqual(spec.changed)
    await page.getByRole('link', { name: `${text.edit[spec.kind]}：${row.id}`, exact: true }).click()
    await input(page, text.fields[spec.invalid]).fill('UNSAVED FORM CONTENT')
    await page.getByRole('button', { name: text.remove[spec.kind], exact: true }).click()
    await expect(page.getByRole('alertdialog')).toContainText(text.deleteHelp)
    await expect(page.getByRole('alertdialog')).not.toContainText('UNSAVED FORM CONTENT')
    await confirm(page, text.confirmDelete[spec.kind])
    ownerRevision++
    expect(writes.at(-1)?.proof).toBe('"2"')
    expect(writes.at(-1)?.body).toBeNull()
    expect((await (await page.request.get(`/api/v1/items/${owner.id}/${spec.kind}`)).json()).data.some((value: { id: string }) => value.id === row.id)).toBe(false)
    expect(writes).toHaveLength(spec.kind === 'attributes' ? 4 : 3)
    expect(new Set(writes.map((request) => request.key)).size).toBe(writes.length)
    await page.getByRole('link', { name: messages.catalog.back, exact: true }).click()
    expect(new URL(page.url()).search).toBe('?q=Owned&limit=1')
  })
}

test('reviews actual complete Tag assignment and clearing without deleting global Tags', async ({ page, application }) => {
  await provision(page, application)
  const owner = await seedItem(page, application.url)
  const tag = await seedTag(page, application.url, '家用備品')
  const writes = watch(page)
  let ownerRevision = owner.revision
  await page.goto(`/items/${owner.id}/tags`)
  await page.getByRole('checkbox', { name: /家用備品/ }).check()
  await page.getByRole('button', { name: text.review, exact: true }).click()
  await confirm(page, text.confirmTags)
  expect(writes.at(-1)?.method).toBe('PUT')
  expect(writes.at(-1)?.proof).toBe(JSON.stringify(String(ownerRevision++)))
  expect(JSON.parse(writes.at(-1)?.body ?? '{}')).toEqual({ tag_ids: [tag.id] })
  await page.getByRole('button', { name: text.clearSelection, exact: true }).click()
  await page.getByRole('button', { name: text.review, exact: true }).click()
  await expect(page.getByRole('alertdialog')).toContainText(text.clearHelp)
  await confirm(page, text.confirmTags)
  expect(writes.at(-1)?.proof).toBe(JSON.stringify(String(ownerRevision)))
  expect(JSON.parse(writes.at(-1)?.body ?? '{}')).toEqual({ tag_ids: [] })
  expect(writes).toHaveLength(2)
  expect(new Set(writes.map((request) => request.key)).size).toBe(2)
  expect((await (await page.request.get(`/api/v1/items/${owner.id}/tags`)).json()).data).toEqual([])
  expect((await (await page.request.get('/api/v1/tags')).json()).data).toHaveLength(1)
})

test('keeps actual checksum and duplicate conflicts authoritative without hidden normalization or retries', async ({ page, application }) => {
  await provision(page, application)
  const owner = await seedItem(page, application.url)
  const writes = watch(page)
  await page.goto(`/items/${owner.id}/barcodes/new`)
  await input(page, text.fields.symbology).selectOption('UPC_A')
  await input(page, text.fields.raw_value).fill('036000291453')
  await page.getByRole('button', { name: text.review, exact: true }).click()
  expect(writes).toHaveLength(0)
  await page.getByRole('alertdialog').getByRole('button', { name: text.confirmCreate.barcodes, exact: true }).click()
  await expect(page.getByText(messages.locations.validation, { exact: true })).toBeVisible()
  await expect(page.getByRole('alertdialog').getByRole('button', { name: text.confirmCreate.barcodes, exact: true })).toBeDisabled()
  expect(writes).toHaveLength(1)
  await page.keyboard.press('Escape')
  await expect(page.getByRole('alertdialog')).toHaveCount(0)
  await input(page, text.fields.raw_value).fill('036000291452')
  await page.getByRole('button', { name: text.review, exact: true }).click()
  await confirm(page, text.confirmCreate.barcodes)
  expect(writes[1]?.key).not.toBe(writes[0]?.key)
  await page.getByRole('link', { name: text.create.barcodes, exact: true }).click()
  await input(page, text.fields.symbology).selectOption('UPC_A')
  await input(page, text.fields.raw_value).fill('036000291452')
  await page.getByRole('button', { name: text.review, exact: true }).click()
  await page.getByRole('alertdialog').getByRole('button', { name: text.confirmCreate.barcodes, exact: true }).click()
  await expect(page.getByText(text.barcodeConflict, { exact: true })).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(page.getByRole('alertdialog')).toHaveCount(0)
  await page.getByRole('link', { name: text.back, exact: true }).click()
  await page.getByRole('alertdialog').getByRole('button', { name: text.leave, exact: true }).click()
  const current: Item = await (await page.request.get(`/api/v1/items/${owner.id}`)).json()
  await seedAlias(page, application.url, current, 'Exact duplicate')
  await collection(page, 'aliases')
  await page.getByRole('link', { name: text.create.aliases, exact: true }).click()
  await input(page, text.fields.alias).fill('Exact duplicate')
  await page.getByRole('button', { name: text.review, exact: true }).click()
  await page.getByRole('alertdialog').getByRole('button', { name: text.confirmCreate.aliases, exact: true }).click()
  await expect(page.getByText(text.aliasConflict, { exact: true })).toBeVisible()
  expect(writes).toHaveLength(4)
})

test('returns from real login and adopts a genuine row conflict explicitly without reconnect replay', async ({ page, application }) => {
  const password = await provision(page, application)
  const owner = await seedItem(page, application.url)
  const alias = await seedAlias(page, application.url, owner)
  await page.context().clearCookies()
  await page.goto(`/items/${owner.id}/aliases/${alias.id}/edit`)
  await expect(page.getByRole('heading', { name: messages.auth.loginHeading, exact: true })).toBeVisible()
  await input(page, messages.auth.username).fill('owner')
  await input(page, messages.auth.password).fill(password)
  await page.getByRole('button', { name: messages.auth.login, exact: true }).click()
  await expect(input(page, text.fields.alias)).toHaveValue(alias.alias)
  const writes = watch(page)
  await input(page, text.fields.alias).fill('Local reviewed alias')
  await page.getByRole('button', { name: text.review, exact: true }).click()
  const remote = await page.request.patch(`/api/v1/aliases/${alias.id}`, { headers: await writeContext(page, application.url, alias.revision), data: { alias: 'Concurrent alias' } })
  expect(remote.status()).toBe(200)
  await page.getByRole('alertdialog').getByRole('button', { name: text.confirmSave, exact: true }).click()
  await expect(page.getByText(messages.locations.conflict, { exact: true })).toBeVisible()
  expect(writes[0]?.proof).toBe('"1"')
  await page.keyboard.press('Escape')
  await expect(page.getByRole('alertdialog')).toHaveCount(0)
  await expect(page.getByText(text.changed, { exact: true })).toBeVisible()
  await expect(input(page, text.fields.alias)).toHaveValue('Local reviewed alias')
  await expect(page.getByRole('button', { name: text.review, exact: true })).toBeDisabled()
  await page.getByRole('button', { name: text.keepEdits, exact: true }).click()
  await page.context().setOffline(true)
  await expect(page.getByText(text.offline, { exact: true })).toBeVisible()
  await page.context().setOffline(false)
  await expect(page.getByRole('button', { name: text.review, exact: true })).toBeEnabled()
  expect(writes).toHaveLength(1)
  await page.getByRole('button', { name: text.review, exact: true }).click()
  await confirm(page, text.confirmSave)
  expect(writes).toHaveLength(2)
  expect(writes[1]?.proof).toBe('"2"')
  expect(writes[1]?.key).not.toBe(writes[0]?.key)
  expect(JSON.parse(writes[1]?.body ?? '{}')).toEqual({ alias: 'Local reviewed alias' })
})

test('retains a genuinely committed unknown deletion and reconciles only the identical original request', async ({ page, application }) => {
  await provision(page, application)
  const owner = await seedItem(page, application.url)
  const alias = await seedAlias(page, application.url, owner)
  const writes = watch(page)
  const replies: string[] = []
  await page.route(`**/api/v1/aliases/${alias.id}`, async (route) => {
    if (route.request().method() !== 'DELETE') { await route.continue(); return }
    const response = await route.fetch()
    replies.push(await response.text())
    if (replies.length === 1) await route.fulfill({ response, body: '{"ok":false}' }); else await route.fulfill({ response })
  })
  await page.goto(`/items/${owner.id}/aliases/${alias.id}/edit`)
  await page.getByRole('button', { name: text.remove.aliases, exact: true }).click()
  await page.getByRole('alertdialog').getByRole('button', { name: text.confirmDelete.aliases, exact: true }).click()
  await expect(page.getByRole('heading', { name: text.uncertain, exact: true })).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(page.getByRole('alertdialog')).toHaveCount(0)
  await expect(page.getByText(text.missing, { exact: true })).toBeVisible()
  await expect(input(page, text.fields.alias)).toHaveValue(alias.alias)
  await expect(page.getByRole('button', { name: text.remove.aliases, exact: true })).toBeDisabled()
  await page.getByRole('button', { name: text.readCurrent, exact: true }).click()
  await page.context().setOffline(true)
  await page.context().setOffline(false)
  expect(writes).toHaveLength(1)
  await page.getByRole('button', { name: text.openOriginal, exact: true }).click()
  await confirm(page, text.retryOriginal, text.uncertain)
  expect(writes).toHaveLength(2)
  expect(writes[1]).toEqual(writes[0])
  expect(replies[1]).toBe(replies[0])
  expect((await (await page.request.get(`/api/v1/items/${owner.id}/aliases`)).json()).data).toEqual([])
})

test('adopts complete Tag membership deltas and selected-label changes only after explicit review', async ({ page, application }) => {
  await provision(page, application)
  const owner = await seedItem(page, application.url)
  const first = await seedTag(page, application.url, 'Assigned tag')
  const second = await seedTag(page, application.url, 'Concurrent tag')
  const initial = await page.request.put(`/api/v1/items/${owner.id}/tags`, { headers: await writeContext(page, application.url, owner.revision), data: { tag_ids: [first.id] } })
  expect(initial.status()).toBe(200)
  const baseline: Item = await initial.json()
  await page.goto(`/items/${owner.id}/tags`)
  const writes = watch(page)
  await page.getByRole('checkbox', { name: /Assigned tag/ }).uncheck()
  const changed = await page.request.put(`/api/v1/items/${owner.id}/tags`, { headers: await writeContext(page, application.url, baseline.revision), data: { tag_ids: [first.id, second.id] } })
  expect(changed.status()).toBe(200)
  const current: Item = await changed.json()
  await page.getByRole('button', { name: text.refresh, exact: true }).click()
  await expect(page.getByText(text.changed, { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: text.review, exact: true })).toBeDisabled()
  await page.getByRole('button', { name: text.keepEdits, exact: true }).click()
  await expect(page.getByRole('checkbox', { name: /Assigned tag/ })).not.toBeChecked()
  await expect(page.getByRole('checkbox', { name: /Concurrent tag/ })).toBeChecked()
  expect(writes).toHaveLength(0)
  await page.getByRole('button', { name: text.review, exact: true }).click()
  await expect(page.getByRole('alertdialog').getByRole('heading', { name: `${text.beforeTags}（2）`, exact: true })).toBeVisible()
  await confirm(page, text.confirmTags)
  expect(writes[0]?.proof).toBe(JSON.stringify(String(current.revision)))
  expect(JSON.parse(writes[0]?.body ?? '{}')).toEqual({ tag_ids: [second.id] })
  await page.getByRole('checkbox', { name: /Assigned tag/ }).check()
  const tagPage: components['schemas']['TagList'] = await (await page.request.get('/api/v1/tags')).json()
  const latest = tagPage.data.find((tag) => tag.id === first.id)!
  const renamed = await page.request.patch(`/api/v1/tags/${first.id}`, { headers: await writeContext(page, application.url, latest.revision), data: { name: 'Changed selected label' } })
  expect(renamed.status()).toBe(200)
  await page.getByRole('button', { name: text.refresh, exact: true }).click()
  await expect(page.getByText(text.selectedLabelChanged, { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: text.review, exact: true })).toBeDisabled()
  await page.getByRole('button', { name: text.recheckTags, exact: true }).click()
  expect(writes).toHaveLength(1)
  await page.getByRole('button', { name: text.review, exact: true }).click()
  await expect(page.getByRole('alertdialog')).toContainText('Changed selected label')
  await accessible(page)
  await confirm(page, text.confirmTags)
  expect(JSON.parse(writes[1]?.body ?? '{}')).toEqual({ tag_ids: [second.id, first.id] })
})

test.describe('retained legacy Tag membership', () => {
  const itemID = 'legacy-owned-item'
  test.use({ applicationDatabaseFixture: { prepare(database) {
    // Seed an offline baseline, not fictional HTTP mutations. The real private
    // Go store still provisions credentials and validates/serves every request;
    // production rate limits and handlers are unchanged. Node is test-only.
    const db = new DatabaseSync(database)
    const now = '2026-11-06T00:00:00Z'
    const owner = { id: itemID, name: '長物品名稱'.repeat(58), brand: null, model: null, category_id: null, description: '', notes: '', tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1, status: 'active' }
    try {
      db.exec('PRAGMA foreign_keys=ON; BEGIN IMMEDIATE;')
      db.prepare('INSERT INTO items(id,name,tracking_mode,created_at,updated_at) VALUES(?,?,?,?,?)').run(itemID, owner.name, owner.tracking_mode, now, now)
      const counter = db.prepare('INSERT INTO projection_revisions(entity_type,entity_id,revision) VALUES(?,?,1)')
      const current = db.prepare('INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at) VALUES(?,?,1,?,?)')
      const baseline = (kind: string, id: string, payload: object) => { counter.run(kind, id); current.run(kind, id, JSON.stringify(payload), now) }
      baseline('item', itemID, owner)
      // Match retained legacy reservation without copying any unverified FTS.
      counter.run('search_doc', JSON.stringify(['item', itemID]))
      const tag = db.prepare('INSERT INTO tags(id,name) VALUES(?,?)')
      const link = db.prepare('INSERT INTO item_tags(item_id,tag_id) VALUES(?,?)')
      for (let index = 0; index < 205; index++) {
        const id = `legacy-tag-${String(index).padStart(3, '0')}`
        const name = `${String(index).padStart(3, '0')} ${'長標籤名稱'.repeat(35)}`
        tag.run(id, name)
        link.run(itemID, id)
        baseline('tag', id, { id, name })
        baseline('item_tag', JSON.stringify([itemID, id]), { item_id: itemID, tag_id: id })
      }
      db.exec('COMMIT')
      expect(db.prepare('PRAGMA foreign_key_check').all()).toEqual([])
    } finally { db.close() }
  } } })

  test('completes all 205 legacy members, enforces the new-set limit and reviews an explicit clear at 200 percent text', async ({ page, application }, info) => {
    test.setTimeout(90_000)
    await provision(page, application)
    const reads: URL[] = []
    page.on('request', (request) => { const url = new URL(request.url()); if (['/api/v1/tags', `/api/v1/items/${itemID}/tags`].includes(url.pathname) && request.method() === 'GET') reads.push(url) })
    const writes = watch(page)
    await page.goto(`/items/${itemID}/tags`)
    await expect(page.getByRole('checkbox')).toHaveCount(205)
    await expect(page.getByText(`${text.selectedTags}205`, { exact: true })).toBeVisible()
    expect(reads.filter((url) => url.searchParams.has('cursor'))).toHaveLength(4)
    await expect(page.getByRole('button', { name: text.review, exact: true })).toBeDisabled()
    await page.getByRole('button', { name: text.clearSelection, exact: true }).click()
    for (let index = 0; index < 101; index++) await page.getByRole('checkbox').nth(index).check()
    await expect(page.getByText(`${text.selectedTags}101`, { exact: true })).toBeVisible()
    await expect(page.getByText(text.tagLimit, { exact: true })).toBeVisible()
    await expect(page.getByRole('button', { name: text.review, exact: true })).toBeDisabled()
    await page.getByRole('checkbox').nth(100).uncheck()
    await expect(page.getByRole('button', { name: text.review, exact: true })).toBeEnabled()
    await page.getByRole('button', { name: text.clearSelection, exact: true }).click()
    await page.locator('html').evaluate((element) => { element.style.fontSize = '200%' })
    await page.getByRole('button', { name: text.review, exact: true }).click()
    const dialog = page.getByRole('alertdialog')
    await expect(dialog.getByRole('heading', { name: text.reviewTitle, exact: true })).toBeFocused()
    await expect(dialog.getByRole('heading', { name: text.reviewTitle, exact: true })).toBeInViewport()
    await expect(dialog.getByRole('heading', { name: `${text.beforeTags}（205）`, exact: true })).toBeAttached()
    await expect(dialog.getByRole('heading', { name: `${text.afterTags}（0）`, exact: true })).toBeAttached()
    for (const colorScheme of ['light', 'dark'] as const) {
      await page.emulateMedia({ colorScheme })
      await expect(page.locator('html')).toHaveAttribute('data-theme', colorScheme)
      await accessible(page)
      await page.screenshot({ path: info.outputPath(`owned-large-review-${colorScheme}.png`) })
    }
    await page.keyboard.press('Tab')
    await expect(dialog.getByRole('button', { name: text.cancel, exact: true })).toBeFocused()
    await expect(dialog.getByRole('button', { name: text.cancel, exact: true })).toBeInViewport()
    await dialog.getByRole('button', { name: text.confirmTags, exact: true }).click()
    await expect(dialog).toHaveCount(0)
    await expect(page.getByText(text.done, { exact: true })).toBeVisible()
    expect(writes).toHaveLength(1)
    expect(JSON.parse(writes[0]?.body ?? '{}')).toEqual({ tag_ids: [] })
    expect((await (await page.request.get(`/api/v1/items/${itemID}/tags?limit=100`)).json()).data).toEqual([])
    await expect(page.getByRole('checkbox')).toHaveCount(205)
    await expect(page.getByText(`${text.selectedTags}0`, { exact: true })).toBeVisible()
    await accessible(page)
  })
})

test('hands Back between owned review/leave dialogs and stops waiting without undoing an actual commit', async ({ page, application }) => {
  await provision(page, application)
  const owner = await seedItem(page, application.url)
  const alias = await seedAlias(page, application.url, owner)
  const writes = watch(page)
  await page.goto(`/items/${owner.id}/aliases`)
  await page.getByRole('link', { name: `${text.edit.aliases}：${alias.id}`, exact: true }).click()
  await input(page, text.fields.alias).fill('Actually committed while waiting')
  await page.getByRole('button', { name: text.review, exact: true }).click()
  await page.evaluate(() => history.back())
  const dialog = page.getByRole('alertdialog')
  await expect(dialog.getByRole('button', { name: text.stay, exact: true })).toBeFocused()
  await dialog.getByRole('button', { name: text.stay, exact: true }).click()
  await expect(dialog.getByRole('heading', { name: text.reviewTitle, exact: true })).toBeFocused()
  expect(writes).toHaveLength(0)
  let committed: (() => void) | undefined
  let release: (() => void) | undefined
  let handled: (() => void) | undefined
  const serverCommit = new Promise<void>((resolve) => { committed = resolve })
  const held = new Promise<void>((resolve) => { release = resolve })
  const routeHandled = new Promise<void>((resolve) => { handled = resolve })
  await page.route(`**/api/v1/aliases/${alias.id}`, async (route) => {
    if (route.request().method() !== 'PATCH') { await route.continue(); return }
    const response = await route.fetch()
    expect(response.status()).toBe(200)
    committed?.()
    await held
    await route.abort('aborted')
    handled?.()
  })
  try {
    await dialog.getByRole('button', { name: text.confirmSave, exact: true }).click()
    await serverCommit
    await expect(dialog.getByRole('button', { name: text.pending, exact: true })).toBeDisabled()
    await page.evaluate(() => history.back())
    await expect(dialog.getByRole('button', { name: text.stay, exact: true })).toBeFocused()
    await dialog.getByRole('button', { name: text.stay, exact: true }).click()
    await expect(dialog.getByRole('heading', { name: text.reviewTitle, exact: true })).toBeFocused()
    await page.keyboard.press('Escape')
    await expect(dialog.getByRole('heading', { name: text.uncertain, exact: true })).toBeVisible()
    release?.()
    await routeHandled
    await dialog.getByRole('button', { name: text.cancel, exact: true }).click()
    await expect(dialog).toHaveCount(0)
    await page.evaluate(() => history.back())
    await dialog.getByRole('button', { name: text.leave, exact: true }).click()
    await expect(page).toHaveURL(new RegExp(`/items/${owner.id}/aliases$`))
    await expect(page.getByText('"Actually committed while waiting"', { exact: true })).toBeVisible()
    const current: components['schemas']['AliasList'] = await (await page.request.get(`/api/v1/items/${owner.id}/aliases`)).json()
    expect(current.data).toEqual([{ ...alias, alias: 'Actually committed while waiting', revision: 2 }])
    expect(writes).toHaveLength(1)
  } finally { release?.() }
})
