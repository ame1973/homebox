import AxeBuilder from '@axe-core/playwright'
import { DatabaseSync } from 'node:sqlite'
import type { Page } from '@playwright/test'
import { messages } from '../../src/lib/messages.ts'
import { test, expect } from './auth-fixture.ts'
import { noOverflow, provision, watchDomainWrites, writeContext } from './domain-helpers.ts'

const text = messages.scan, catalog = messages.catalog
const value = '036000291452', controlValue = '\0legacy é\r\n00123'
const legacyID = 'legacy/item\0😀', ambiguousID = 'percent%2F', longID = '長'.repeat(120)
const owners = [
  { id: legacyID, name: 'Legacy shared owner', status: 'archived' },
  { id: 'owner-1', name: 'Shared first', status: 'active' },
  { id: ambiguousID, name: 'Ambiguous route', status: 'active' },
  { id: 'new', name: 'Reserved new item', status: 'active' },
  { id: 'owner-2', name: 'Second page owner', status: 'active' },
  { id: longID, name: '極長物品'.repeat(60), status: 'active' },
] as const

function seedBarcodeFixture(database: string) {
  const db = new DatabaseSync(database), now = '2026-11-16T00:00:00Z'
  try {
    db.exec('PRAGMA foreign_keys=ON; BEGIN IMMEDIATE')
    const counter = db.prepare('INSERT INTO projection_revisions(entity_type,entity_id,revision) VALUES(?,?,?)')
    const current = db.prepare('INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at) VALUES(?,?,?,?,?)')
    const baseline = (kind: string, id: string, revision: number, payload: object) => { counter.run(kind, id, revision); current.run(kind, id, revision, JSON.stringify(payload), now) }
    const item = db.prepare('INSERT INTO items(id,name,tracking_mode,status,created_at,updated_at) VALUES(?,?,?,?,?,?)')
    for (const owner of owners) {
      item.run(owner.id, owner.name, 'individual', owner.status, now, now)
      baseline('item', owner.id, 7, { ...owner, brand: null, model: null, category_id: null, description: '', notes: '', tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1 })
      counter.run('search_doc', JSON.stringify(['item', owner.id]), 1)
    }
    const insert = db.prepare('INSERT INTO item_barcodes(id,item_id,symbology,raw_value,normalized_value,created_at) VALUES(?,?,?,?,?,?)')
    const rows = [
      ['00-legacy', legacyID, 'UPC_A', value], ['01-owner', 'owner-1', 'UPC_A', value], ['02-shared-owner', 'owner-1', 'OTHER', value],
      ['03-ambiguous', ambiguousID, 'UPC_A', value], ['04-new', 'new', 'UPC_A', value], ['05-second', 'owner-2', 'UPC_A', value],
      ['06-long', longID, 'UPC_A', value], ['07-control', 'owner-2', 'EAN_13', controlValue],
    ] as const
    for (const [id, owner, symbology, raw] of rows) {
      insert.run(id, owner, symbology, raw, raw, now)
      baseline('barcode', id, 5, { id, item_id: owner, symbology, raw_value: raw, normalized_value: raw })
    }
    db.exec('COMMIT')
    expect(db.prepare('PRAGMA foreign_key_check').all()).toEqual([])
  } finally { db.close() }
}

test.use({ trace: 'off', screenshot: 'off', video: 'off', applicationDatabaseFixture: { prepare: seedBarcodeFixture } })

async function openScan(page: Page) {
  await page.goto('/scan')
  await expect(page.getByRole('textbox', { name: text.input })).toBeVisible()
  await expect(page.getByRole('button', { name: text.search })).toBeEnabled()
}
async function search(page: Page, input = value) {
  await page.getByRole('textbox', { name: text.input }).fill(input)
  await page.getByRole('button', { name: text.search }).click()
}

test('shows real shared Item choices in bounded explicit pages without writes', async ({ page, application }) => {
  await provision(page, application)
  const writes = watchDomainWrites(page, ['/api/v1/items', '/api/v1/barcodes', '/api/v1/search'])
  const requests: URL[] = []
  page.on('request', (request) => { const url = new URL(request.url()); if (url.pathname === '/api/v1/search/barcodes' || url.pathname === '/api/v1/items-by-identity') requests.push(url) })
  await openScan(page)
  expect(requests).toHaveLength(0)
  await search(page)
  const results = page.getByRole('list', { name: text.results })
  await expect(results.getByRole('listitem')).toHaveCount(5)
  const legacy = results.getByRole('listitem').filter({ has: page.getByRole('heading', { name: JSON.stringify('Legacy shared owner') }) })
  await expect(legacy).toContainText(catalog.archived)
  await expect(legacy).toContainText(JSON.stringify(legacyID))
  await expect(legacy.getByText(text.unavailableItem)).toBeVisible()
  const ambiguous = results.getByRole('listitem').filter({ has: page.getByRole('heading', { name: JSON.stringify('Ambiguous route') }) })
  await expect(ambiguous.getByText(text.unavailableItem)).toBeVisible()
  await expect(results.getByRole('link', { name: /Reserved new item/ })).toHaveAttribute('href', '/items/new/view')
  const initial = requests.filter((url) => url.pathname.endsWith('/items-by-identity'))
  expect(initial).toHaveLength(4)
  expect(initial.filter((url) => url.searchParams.get('id') === 'owner-1')).toHaveLength(1)
  expect(requests.find((url) => url.pathname.endsWith('/search/barcodes'))?.searchParams.get('limit')).toBe('5')
  await page.getByRole('button', { name: catalog.nextPage }).click()
  await expect(results.getByRole('listitem')).toHaveCount(2)
  await expect(results.getByRole('heading', { name: JSON.stringify('Second page owner') })).toBeVisible()
  expect(requests.filter((url) => url.pathname.endsWith('/search/barcodes'))).toHaveLength(2)
  await page.getByRole('button', { name: catalog.previousPage }).click()
  await expect(results.getByRole('listitem')).toHaveCount(5)
  expect(requests.filter((url) => url.pathname.endsWith('/search/barcodes'))).toHaveLength(3)
  expect(writes).toEqual([])
})

test('does not suggest a new Item when a continuation becomes empty after real deletions', async ({ page, application }) => {
  await provision(page, application)
  await openScan(page); await search(page)
  await expect(page.getByRole('list', { name: text.results }).getByRole('listitem')).toHaveCount(5)
  for (const id of ['05-second', '06-long']) {
    const response = await page.request.delete(`/api/v1/barcodes/${id}`, { headers: await writeContext(page, application.url, 5) })
    expect(response.status()).toBe(200)
  }
  await page.getByRole('button', { name: catalog.nextPage }).click()
  await expect(page.getByRole('heading', { name: text.emptyPage })).toBeVisible()
  await expect(page.getByRole('heading', { name: text.empty })).toHaveCount(0)
  await expect(page.getByRole('button', { name: text.create })).toHaveCount(0)
})

test('looks up retained JSON-escaped controls exactly and distinguishes an empty result', async ({ page, application }) => {
  await provision(page, application)
  const writes = watchDomainWrites(page, ['/api/v1/items', '/api/v1/search'])
  const values: string[] = []
  page.on('request', (request) => { const url = new URL(request.url()); if (url.pathname === '/api/v1/search/barcodes') values.push(url.searchParams.get('value') ?? '') })
  await openScan(page)
  await page.getByRole('combobox', { name: text.inputFormat }).selectOption('json')
  await search(page, JSON.stringify(controlValue))
  await expect(page.getByRole('heading', { name: JSON.stringify('Second page owner') })).toBeVisible()
  expect(values).toEqual([controlValue])
  expect(new URL(page.url()).search).toBe('')
  await page.getByRole('combobox', { name: text.inputFormat }).selectOption('plain')
  await search(page, '36000291452')
  await expect(page.getByRole('heading', { name: text.empty })).toBeVisible()
  await expect(page.getByText(text.createHelp)).toBeVisible()
  await expect(page.getByRole('button', { name: text.create })).toBeEnabled()
  expect(values).toEqual([controlValue, '36000291452'])
  expect(writes).toEqual([])
})

test('accepts a fully escaped 512-code-point JSON lookup without browser truncation', async ({ page, application }) => {
  await provision(page, application)
  const encoded = `"${'\\ud83d\\ude00'.repeat(512)}"`, values: string[] = []
  page.on('request', (request) => { const url = new URL(request.url()); if (url.pathname === '/api/v1/search/barcodes') values.push(url.searchParams.get('value') ?? '') })
  await openScan(page)
  await page.getByRole('combobox', { name: text.inputFormat }).selectOption('json')
  await search(page, encoded)
  await expect(page.getByRole('textbox', { name: text.input })).toHaveValue(encoded)
  await expect(page.getByRole('heading', { name: text.empty })).toBeVisible()
  expect(values).toEqual(['😀'.repeat(512)])
})

test('prefills a separate reviewed barcode after creating an Item, without automatic writes', async ({ page, application }) => {
  await provision(page, application)
  const scanned = '012345678905', owned = messages.owned
  const writes = watchDomainWrites(page, ['/api/v1/items', '/api/v1/barcodes', '/api/v1/stock', '/api/v1/inventory'])
  await openScan(page)
  await page.getByRole('combobox', { name: text.symbology }).selectOption('UPC_A')
  await search(page, scanned)
  await expect(page.getByRole('heading', { name: text.empty })).toBeVisible()
  await page.getByRole('button', { name: text.create }).click()
  await expect(page).toHaveURL(/\/items\/new$/)
  await expect(page.getByText(text.handoffHelp)).toBeVisible()
  expect(JSON.stringify(await page.evaluate(() => history.state))).not.toContain(scanned)
  await page.getByRole('textbox', { name: new RegExp(`^${catalog.fields.name}`) }).fill('Scanned new Item')
  await page.getByRole('button', { name: catalog.review }).click()
  await expect(page.getByRole('alertdialog')).toContainText(text.reviewHandoffHelp)
  await expect(page.getByRole('alertdialog')).toContainText(JSON.stringify(scanned))
  expect(writes).toEqual([])
  await page.getByRole('alertdialog').getByRole('button', { name: catalog.confirmCreate }).click()
  await expect(page).toHaveURL(/\/items\/[^/]+\/barcodes\/new$/)
  const itemID = new URL(page.url()).pathname.split('/')[2]!
  const raw = page.getByRole('textbox', { name: new RegExp(`^${owned.fields.raw_value}`) })
  await expect(raw).toHaveValue(scanned)
  await expect(page.getByRole('combobox', { name: new RegExp(`^${owned.fields.symbology}`) })).toHaveValue('UPC_A')
  await expect(page.getByText(text.barcodeDraftHelp)).toBeVisible()
  expect(writes.filter((request) => request.method === 'POST')).toHaveLength(1)
  expect(writes[0]?.path).toBe('/api/v1/items')
  expect(JSON.parse(writes[0]!.body!)).not.toHaveProperty('barcodes')
  await page.getByRole('button', { name: owned.review }).click()
  await expect(page.getByRole('alertdialog')).toContainText(JSON.stringify(scanned))
  expect(writes.filter((request) => request.method === 'POST')).toHaveLength(1)
  await page.getByRole('alertdialog').getByRole('button', { name: owned.confirmCreate.barcodes }).click()
  await expect(page).toHaveURL(new RegExp(`/items/${itemID}/barcodes$`))
  const barcodeWrites = writes.filter((request) => request.method === 'POST' && request.path === `/api/v1/items/${itemID}/barcodes`)
  expect(barcodeWrites).toHaveLength(1)
  expect(barcodeWrites[0]?.proof).toBe('"1"')
  expect(JSON.parse(barcodeWrites[0]!.body!)).toEqual({ symbology: 'UPC_A', raw_value: scanned })
  const response = await page.request.get(`/api/v1/items/${itemID}/barcodes`)
  expect(response.status()).toBe(200)
  expect((await response.json()).data).toEqual([expect.objectContaining({ symbology: 'UPC_A', raw_value: scanned, normalized_value: scanned })])
  expect(writes.some((request) => request.path.startsWith('/api/v1/stock') || request.path.startsWith('/api/v1/inventory'))).toBe(false)
})

test('preserves escaped control characters and requires an explicit format after an all-format lookup', async ({ page, application }) => {
  await provision(page, application)
  const scanned = '\0new\r\n001', owned = messages.owned
  const writes = watchDomainWrites(page, ['/api/v1/items', '/api/v1/barcodes'])
  await openScan(page)
  await page.getByRole('combobox', { name: text.inputFormat }).selectOption('json')
  await search(page, JSON.stringify(scanned))
  await expect(page.getByRole('heading', { name: text.empty })).toBeVisible()
  await page.getByRole('button', { name: text.create }).click()
  await expect(page.getByText(text.chooseFormatLater)).toBeVisible()
  await page.getByRole('textbox', { name: new RegExp(`^${catalog.fields.name}`) }).fill('Control barcode Item')
  await page.getByRole('button', { name: catalog.review }).click()
  await page.getByRole('alertdialog').getByRole('button', { name: catalog.confirmCreate }).click()
  await expect(page).toHaveURL(/\/items\/[^/]+\/barcodes\/new$/)
  const itemID = new URL(page.url()).pathname.split('/')[2]!
  const format = page.getByRole('combobox', { name: new RegExp(`^${owned.fields.symbology}`) })
  const raw = page.getByRole('textbox', { name: new RegExp(`^${owned.fields.raw_value}`) })
  await expect(raw).toHaveValue(JSON.stringify(scanned))
  await expect(format).toHaveValue('')
  await expect(page.getByText(text.encodedBarcodeHelp)).toBeVisible()
  await expect(page.getByText(text.encodedReviewHelp)).toBeVisible()
  await page.getByRole('button', { name: owned.review }).click()
  await expect(page.getByText(owned.symbologyError)).toBeVisible()
  expect(writes.filter((request) => request.method === 'POST')).toHaveLength(1)
  await format.selectOption('CODE_128')
  await page.getByRole('button', { name: owned.review }).click()
  await expect(page.getByRole('alertdialog')).toContainText(JSON.stringify(scanned))
  await page.getByRole('alertdialog').getByRole('button', { name: owned.confirmCreate.barcodes }).click()
  await expect(page).toHaveURL(new RegExp(`/items/${itemID}/barcodes$`))
  const barcode = writes.find((request) => request.method === 'POST' && request.path === `/api/v1/items/${itemID}/barcodes`)
  expect(JSON.parse(barcode!.body!)).toEqual({ symbology: 'CODE_128', raw_value: scanned })
  const response = await page.request.get(`/api/v1/items/${itemID}/barcodes`)
  expect((await response.json()).data).toEqual([expect.objectContaining({ raw_value: scanned, normalized_value: scanned })])
})

test('shows default-ignorable marks as escapes and saves the exact reviewed barcode', async ({ page, application }) => {
  await provision(page, application)
  const scanned = `000${String.fromCodePoint(0x034f)}123${String.fromCodePoint(0xfe0f, 0xe0100)}\u0085${String.fromCodePoint(0x202e)}b${String.fromCodePoint(0xe0001)}`, literal = '"000\\u034f123\\ufe0f\\udb40\\udd00\\u0085\\u202eb\\udb40\\udc01"', owned = messages.owned
  const writes = watchDomainWrites(page, ['/api/v1/items', '/api/v1/barcodes'])
  await openScan(page)
  await page.getByRole('combobox', { name: text.inputFormat }).selectOption('json')
  await page.getByRole('combobox', { name: text.symbology }).selectOption('QR_CODE')
  await search(page, JSON.stringify(scanned))
  await expect(page.getByRole('region', { name: text.results })).toContainText(literal)
  await page.getByRole('button', { name: text.create }).click()
  await expect(page.getByText(text.handoffHelp).locator('..')).toContainText(literal)
  await page.getByRole('textbox', { name: new RegExp(`^${catalog.fields.name}`) }).fill('Invisible control barcode')
  await page.getByRole('button', { name: catalog.review }).click()
  await page.getByRole('alertdialog').getByRole('button', { name: catalog.confirmCreate }).click()
  await expect(page).toHaveURL(/\/items\/[^/]+\/barcodes\/new$/)
  const itemID = new URL(page.url()).pathname.split('/')[2]!
  await expect(page.getByRole('textbox', { name: new RegExp(`^${owned.fields.raw_value}`) })).toHaveValue(literal)
  await page.getByRole('button', { name: owned.review }).click()
  await expect(page.getByRole('alertdialog')).toContainText(literal)
  await page.getByRole('alertdialog').getByRole('button', { name: owned.confirmCreate.barcodes }).click()
  await expect(page).toHaveURL(new RegExp(`/items/${itemID}/barcodes$`))
  const barcode = writes.find((request) => request.method === 'POST' && request.path === `/api/v1/items/${itemID}/barcodes`)
  expect(JSON.parse(barcode!.body!)).toEqual({ symbology: 'QR_CODE', raw_value: scanned })
})

test('retains a fully escaped 512-code-point barcode through the real editor and server', async ({ page, application }) => {
  await provision(page, application)
  const scanned = '\0' + '😀'.repeat(511), encoded = `"\\u0000${'\\ud83d\\ude00'.repeat(511)}"`, owned = messages.owned
  await openScan(page)
  await page.getByRole('combobox', { name: text.inputFormat }).selectOption('json')
  await page.getByRole('combobox', { name: text.symbology }).selectOption('QR_CODE')
  await search(page, encoded)
  await expect(page.getByRole('heading', { name: text.empty })).toBeVisible()
  await page.getByRole('button', { name: text.create }).click()
  await page.getByRole('textbox', { name: new RegExp(`^${catalog.fields.name}`) }).fill('Maximum Unicode barcode')
  await page.getByRole('button', { name: catalog.review }).click()
  await page.getByRole('alertdialog').getByRole('button', { name: catalog.confirmCreate }).click()
  await expect(page).toHaveURL(/\/items\/[^/]+\/barcodes\/new$/)
  const itemID = new URL(page.url()).pathname.split('/')[2]!
  const raw = page.getByRole('textbox', { name: new RegExp(`^${owned.fields.raw_value}`) })
  await raw.fill(encoded)
  await expect(raw).toHaveValue(encoded)
  await page.getByRole('button', { name: owned.review }).click()
  await page.getByRole('alertdialog').getByRole('button', { name: owned.confirmCreate.barcodes }).click()
  await expect(page).toHaveURL(new RegExp(`/items/${itemID}/barcodes$`))
  const response = await page.request.get(`/api/v1/items/${itemID}/barcodes`)
  expect(response.status()).toBe(200)
  expect((await response.json()).data).toEqual([expect.objectContaining({ raw_value: scanned, normalized_value: scanned })])
})

test('warns when a reload loses the volatile barcode before Item creation', async ({ page, application }) => {
  await provision(page, application)
  const scanned = '012345678905', writes = watchDomainWrites(page, ['/api/v1/items', '/api/v1/barcodes'])
  await openScan(page); await search(page, scanned)
  await page.getByRole('button', { name: text.create }).click()
  await expect(page.getByText(text.handoffHelp)).toBeVisible()
  await page.reload()
  await expect(page.getByText(text.handoffLost)).toBeVisible()
  await expect(page.getByText(text.handoffTitle)).toHaveCount(0)
  await page.getByRole('textbox', { name: new RegExp(`^${catalog.fields.name}`) }).fill('Item after reload')
  await page.getByRole('button', { name: catalog.review }).click()
  await page.getByRole('alertdialog').getByRole('button', { name: catalog.confirmCreate }).click()
  await expect(page.getByText(text.handoffUnavailable)).toBeVisible()
  expect(writes.filter((request) => request.method === 'POST')).toHaveLength(1)
})

test('warns and leaves the barcode form blank if a reload loses its handoff', async ({ page, application }) => {
  await provision(page, application)
  await openScan(page); await search(page, '012345678905')
  await page.getByRole('button', { name: text.create }).click()
  await page.getByRole('textbox', { name: new RegExp(`^${catalog.fields.name}`) }).fill('Item before barcode reload')
  await page.getByRole('button', { name: catalog.review }).click()
  await page.getByRole('alertdialog').getByRole('button', { name: catalog.confirmCreate }).click()
  await expect(page).toHaveURL(/\/items\/[^/]+\/barcodes\/new$/)
  await expect(page.getByText(text.barcodeDraftHelp)).toBeVisible()
  await page.reload()
  await expect(page.getByText(text.barcodeDraftLost)).toBeVisible()
  await expect(page.getByRole('textbox', { name: new RegExp(`^${messages.owned.fields.raw_value}`) })).toHaveValue('')
  await expect(page.getByRole('combobox', { name: new RegExp(`^${messages.owned.fields.symbology}`) })).toHaveValue('')
})

test('clears a displayed Item-creation handoff after logout in another tab', async ({ page, context, application }) => {
  await provision(page, application)
  const scanned = '012345678905'
  await openScan(page); await search(page, scanned)
  await page.getByRole('button', { name: text.create }).click()
  await expect(page.getByText(text.handoffHelp)).toBeVisible()
  const other = await context.newPage()
  try {
    await other.goto('/settings/account')
    await other.getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await other.getByRole('alertdialog').getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await expect(page.getByText(text.handoffLost)).toBeVisible()
    await expect(page.getByText(text.handoffHelp)).toHaveCount(0)
    await expect(page.locator('body')).not.toContainText(scanned)
  } finally { await other.close() }
})

test('clears an idle prefilled barcode editor after logout in another tab', async ({ page, context, application }) => {
  await provision(page, application)
  const scanned = '012345678905', owned = messages.owned
  const writes = watchDomainWrites(page, ['/api/v1/items', '/api/v1/barcodes'])
  await openScan(page); await search(page, scanned)
  await page.getByRole('button', { name: text.create }).click()
  await page.getByRole('textbox', { name: new RegExp(`^${catalog.fields.name}`) }).fill('Item before cross-tab logout')
  await page.getByRole('button', { name: catalog.review }).click()
  await page.getByRole('alertdialog').getByRole('button', { name: catalog.confirmCreate }).click()
  await expect(page).toHaveURL(/\/items\/[^/]+\/barcodes\/new$/)
  const raw = page.getByRole('textbox', { name: new RegExp(`^${owned.fields.raw_value}`) })
  await expect(raw).toHaveValue(scanned)
  const other = await context.newPage()
  try {
    await other.goto('/settings/account')
    await other.getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await other.getByRole('alertdialog').getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await expect(page.getByText(text.barcodeDraftLost)).toBeVisible()
    await expect(raw).toHaveValue('')
    expect(writes.filter((request) => request.method === 'POST')).toHaveLength(1)
  } finally { await other.close() }
})

test('reports the cleared code if a real session expires before Item creation', async ({ page, application }) => {
  await provision(page, application)
  const scanned = '012345678905', writes = watchDomainWrites(page, ['/api/v1/items', '/api/v1/barcodes'])
  await openScan(page); await search(page, scanned)
  await page.getByRole('button', { name: text.create }).click()
  await expect(page.getByText(text.handoffHelp)).toBeVisible()
  expect((await page.request.post('/api/v1/auth/logout', { headers: await writeContext(page, application.url) })).status()).toBe(200)
  await page.getByRole('textbox', { name: new RegExp(`^${catalog.fields.name}`) }).fill('Unsaved Item')
  await page.getByRole('button', { name: catalog.review }).click()
  await page.getByRole('alertdialog').getByRole('button', { name: catalog.confirmCreate }).click()
  await expect(page).toHaveURL(/\/login$/)
  await expect(page.getByText(messages.auth.scanHandoffLost)).toBeVisible()
  expect(JSON.stringify(await page.evaluate(() => history.state))).not.toContain(scanned)
  expect(writes).toEqual([])
})

test('reports the cleared code if a real session expires before barcode confirmation', async ({ page, application }) => {
  const password = await provision(page, application), scanned = '012345678905', owned = messages.owned
  const writes = watchDomainWrites(page, ['/api/v1/items', '/api/v1/barcodes'])
  await openScan(page); await search(page, scanned)
  await page.getByRole('button', { name: text.create }).click()
  await page.getByRole('textbox', { name: new RegExp(`^${catalog.fields.name}`) }).fill('Item before logout')
  await page.getByRole('button', { name: catalog.review }).click()
  await page.getByRole('alertdialog').getByRole('button', { name: catalog.confirmCreate }).click()
  await expect(page).toHaveURL(/\/items\/[^/]+\/barcodes\/new$/)
  const itemID = new URL(page.url()).pathname.split('/')[2]!
  await expect(page.getByText(text.barcodeDraftHelp)).toBeVisible()
  expect((await page.request.post('/api/v1/auth/logout', { headers: await writeContext(page, application.url) })).status()).toBe(200)
  await page.getByRole('combobox', { name: new RegExp(`^${owned.fields.symbology}`) }).selectOption('UPC_A')
  await page.getByRole('button', { name: owned.review }).click()
  await page.getByRole('alertdialog').getByRole('button', { name: owned.confirmCreate.barcodes }).click()
  await expect(page).toHaveURL(/\/login$/)
  await expect(page.getByText(messages.auth.scanHandoffLost)).toBeVisible()
  expect(JSON.stringify(await page.evaluate(() => history.state))).not.toContain(scanned)
  expect(writes.filter((request) => request.method === 'POST')).toHaveLength(1)
  expect((await page.request.post('/api/v1/auth/login', { headers: { Origin: application.url }, data: { username: 'owner', password } })).status()).toBe(200)
  const response = await page.request.get(`/api/v1/items/${itemID}/barcodes`)
  expect(response.status()).toBe(200)
  expect((await response.json()).data).toEqual([])
})

test('retains an edited prefilled barcode offline without replaying it on reconnect', async ({ page, context, application }) => {
  await provision(page, application)
  const scanned = '\0draft\r\n001', edited = '\0edited\r\n002', owned = messages.owned
  const writes = watchDomainWrites(page, ['/api/v1/items', '/api/v1/barcodes'])
  await openScan(page)
  await page.getByRole('combobox', { name: text.inputFormat }).selectOption('json')
  await page.getByRole('combobox', { name: text.symbology }).selectOption('CODE_128')
  await search(page, JSON.stringify(scanned))
  await page.getByRole('button', { name: text.create }).click()
  await page.getByRole('textbox', { name: new RegExp(`^${catalog.fields.name}`) }).fill('Offline barcode draft')
  await page.getByRole('button', { name: catalog.review }).click()
  await page.getByRole('alertdialog').getByRole('button', { name: catalog.confirmCreate }).click()
  await expect(page).toHaveURL(/\/items\/[^/]+\/barcodes\/new$/)
  const itemID = new URL(page.url()).pathname.split('/')[2]!
  const input = page.getByRole('textbox', { name: new RegExp(`^${owned.fields.raw_value}`) })
  await expect(input).toHaveValue(JSON.stringify(scanned))
  await context.setOffline(true)
  try {
    await input.fill(JSON.stringify(edited))
    await expect(page.getByRole('button', { name: owned.review })).toBeDisabled()
    expect(writes.filter((request) => request.method === 'POST')).toHaveLength(1)
  } finally { await context.setOffline(false) }
  await expect(page.getByRole('button', { name: owned.review })).toBeEnabled()
  await expect(input).toHaveValue(JSON.stringify(edited))
  expect(writes.filter((request) => request.method === 'POST')).toHaveLength(1)
  await page.getByRole('button', { name: owned.review }).click()
  await page.getByRole('alertdialog').getByRole('button', { name: owned.confirmCreate.barcodes }).click()
  await expect(page).toHaveURL(new RegExp(`/items/${itemID}/barcodes$`))
  const barcode = writes.find((request) => request.method === 'POST' && request.path === `/api/v1/items/${itemID}/barcodes`)
  expect(JSON.parse(barcode!.body!)).toEqual({ symbology: 'CODE_128', raw_value: edited })
})

test('keeps the unsaved barcode visible when the newly created Item is deleted before its first read', async ({ page, application }) => {
  await provision(page, application)
  const scanned = '012345678905', writes = watchDomainWrites(page, ['/api/v1/items', '/api/v1/barcodes'])
  let deleted = false
  await page.route((url) => /^\/api\/v1\/items\/[a-f0-9-]+$/.test(url.pathname), async (route) => {
    if (deleted || route.request().method() !== 'GET') return route.continue()
    deleted = true
    const response = await page.request.delete(new URL(route.request().url()).pathname, { headers: await writeContext(page, application.url, 1) })
    expect(response.status()).toBe(200)
    await route.continue()
  })
  await openScan(page); await search(page, scanned)
  await page.getByRole('button', { name: text.create }).click()
  await page.getByRole('textbox', { name: new RegExp(`^${catalog.fields.name}`) }).fill('Item removed before barcode read')
  await page.getByRole('button', { name: catalog.review }).click()
  await page.getByRole('alertdialog').getByRole('button', { name: catalog.confirmCreate }).click()
  await expect(page.getByRole('heading', { name: catalog.missing })).toBeVisible()
  await expect(page.getByText(text.barcodeDraftHelp).locator('..')).toContainText(JSON.stringify(scanned))
  expect(deleted).toBe(true)
  expect(writes.filter((request) => request.method === 'POST' && request.path.endsWith('/barcodes'))).toHaveLength(0)
})

test('keeps the barcode through an owner read failure and loads the editor only after explicit refresh', async ({ page, application }) => {
  await provision(page, application)
  const scanned = '012345678905', owned = messages.owned
  let fail = true
  const ownerPath = (url: URL) => /^\/api\/v1\/items\/[a-f0-9-]+$/.test(url.pathname)
  await page.route(ownerPath, async (route) => {
    if (!fail || route.request().method() !== 'GET') return route.continue()
    fail = false
    await route.fulfill({ status: 503, contentType: 'application/json', body: JSON.stringify({ error: { code: 'SERVICE_UNAVAILABLE' } }) })
  })
  await openScan(page); await search(page, scanned)
  await page.getByRole('button', { name: text.create }).click()
  await page.getByRole('textbox', { name: new RegExp(`^${catalog.fields.name}`) }).fill('Item after source retry')
  await page.getByRole('button', { name: catalog.review }).click()
  await page.getByRole('alertdialog').getByRole('button', { name: catalog.confirmCreate }).click()
  await expect(page.getByText(messages.locations.busy)).toBeVisible()
  await expect(page.getByText(text.barcodeDraftHelp).locator('..')).toContainText(JSON.stringify(scanned))
  await expect(page.getByRole('textbox', { name: new RegExp(`^${owned.fields.raw_value}`) })).toHaveCount(0)
  await page.getByRole('button', { name: owned.refresh }).click()
  await expect(page.getByRole('textbox', { name: new RegExp(`^${owned.fields.raw_value}`) })).toHaveValue(scanned)
})

test('withholds a page if an actual Item read disappears and retries only on request', async ({ page, application }) => {
  await provision(page, application)
  let corrupt = true, searches = 0
  page.on('request', (request) => { if (new URL(request.url()).pathname === '/api/v1/search/barcodes') searches++ })
  await page.route((url) => url.pathname === '/api/v1/items-by-identity', async (route) => {
    if (!corrupt) return route.continue()
    corrupt = false
    await route.fulfill({ status: 404, contentType: 'application/json', body: JSON.stringify({ error: { code: 'NOT_FOUND' } }) })
  })
  await openScan(page); await search(page)
  await expect(page.getByText(catalog.invalidRead)).toBeVisible()
  await expect(page.getByRole('list', { name: text.results })).toHaveCount(0)
  await expect(page.getByRole('heading', { name: text.empty })).toHaveCount(0)
  expect(searches).toBe(1)
  await page.getByRole('button', { name: text.refresh }).click()
  await expect(page.getByRole('list', { name: text.results }).getByRole('listitem')).toHaveCount(5)
  expect(searches).toBe(2)
})

test('does not retry a failed search when a real session is revalidated', async ({ page, application }) => {
  await provision(page, application)
  let searches = 0, holdAuth = false, release: (() => void) | undefined
  await page.route((url) => url.pathname === '/api/v1/search/barcodes', async (route) => {
    searches++
    if (searches === 1) return route.fulfill({ status: 429, contentType: 'application/json', body: JSON.stringify({ error: { code: 'RATE_LIMITED' } }) })
    return route.continue()
  })
  await page.route((url) => url.pathname === '/api/v1/auth/me', async (route) => {
    if (!holdAuth) return route.continue()
    holdAuth = false
    const response = await route.fetch()
    await new Promise<void>((resolve) => { release = resolve })
    await route.fulfill({ response })
  })
  await openScan(page); await search(page)
  await expect(page.getByText(messages.locations.rate)).toBeVisible()
  holdAuth = true
  await page.evaluate(() => {
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'hidden' })
    window.dispatchEvent(new Event('visibilitychange'))
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' })
    window.dispatchEvent(new Event('visibilitychange'))
  })
  try {
    await expect.poll(() => !!release).toBe(true)
    await expect(page.getByText(text.checking)).toBeVisible()
    const response = page.waitForResponse((response) => new URL(response.url()).pathname === '/api/v1/auth/me' && response.ok())
    release?.(); await response
    await expect(page.getByText(messages.locations.rate)).toBeVisible()
    expect(searches).toBe(1)
  } finally { release?.() }
  await page.getByRole('button', { name: text.refresh }).click()
  await expect(page.getByRole('list', { name: text.results }).getByRole('listitem')).toHaveCount(5)
  expect(searches).toBe(2)
})

test('clears query results on offline and never replays the lookup on reconnect', async ({ page, context, application }) => {
  await provision(page, application)
  let searches = 0
  page.on('request', (request) => { if (new URL(request.url()).pathname === '/api/v1/search/barcodes') searches++ })
  await openScan(page); await search(page)
  await expect(page.getByRole('list', { name: text.results }).getByRole('listitem')).toHaveCount(5)
  await context.setOffline(true)
  try {
    await expect(page.getByText(text.offlineHelp)).toBeVisible()
    await expect(page.getByRole('list', { name: text.results })).toHaveCount(0)
    await expect(page.getByRole('textbox', { name: text.input })).toHaveValue(value)
  } finally { await context.setOffline(false) }
  await expect(page.getByRole('button', { name: text.search })).toBeEnabled()
  expect(searches).toBe(1)
  await page.getByRole('button', { name: text.search }).click()
  await expect(page.getByRole('list', { name: text.results }).getByRole('listitem')).toHaveCount(5)
  expect(searches).toBe(2)
})

test('clears visible lookup data after real logout in another tab', async ({ page, context, application }) => {
  await provision(page, application)
  await openScan(page); await search(page)
  await expect(page.getByRole('list', { name: text.results }).getByRole('listitem')).toHaveCount(5)
  const other = await context.newPage()
  try {
    await other.goto('/settings/account')
    await other.getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await other.getByRole('alertdialog').getByRole('button', { name: messages.auth.logout, exact: true }).click()
    await expect(page.getByText(text.sessionClearedHelp)).toBeVisible()
    await expect(page.getByRole('textbox', { name: text.input })).toHaveCount(0)
    await expect(page.getByRole('list', { name: text.results })).toHaveCount(0)
  } finally { await other.close() }
})

test('moves an expired session to login without putting the input in its return path', async ({ page, application }) => {
  await provision(page, application)
  await openScan(page)
  await page.getByRole('textbox', { name: text.input }).fill('private-barcode-value')
  expect((await page.request.post('/api/v1/auth/logout', { headers: await writeContext(page, application.url) })).status()).toBe(200)
  await page.getByRole('button', { name: text.search }).click()
  await expect(page).toHaveURL(/\/login$/)
  expect(JSON.stringify(await page.evaluate(() => history.state))).not.toContain('private-barcode-value')
})

test('keeps long barcode handoff reviews accessible at 320px and 200% text', async ({ page, application }) => {
  await provision(page, application)
  const scanned = '\0' + '😀'.repeat(511), owned = messages.owned
  await page.setViewportSize({ width: 320, height: 568 })
  await openScan(page)
  await page.locator('html').evaluate((element) => { element.style.fontSize = '200%' })
  await page.getByRole('combobox', { name: text.inputFormat }).selectOption('json')
  await page.getByRole('combobox', { name: text.symbology }).selectOption('QR_CODE')
  await search(page, JSON.stringify(scanned))
  await page.getByRole('button', { name: text.create }).click()
  await page.getByRole('textbox', { name: new RegExp(`^${catalog.fields.name}`) }).fill('Long reviewed barcode')
  await page.getByRole('button', { name: catalog.review }).click()
  const itemDialog = page.getByRole('alertdialog')
  await expect(itemDialog.getByRole('heading', { name: catalog.reviewTitle })).toBeFocused()
  for (const colorScheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme }); await expect(page.locator('html')).toHaveAttribute('data-theme', colorScheme)
    expect(await page.evaluate(() => innerWidth)).toBe(320)
    await noOverflow(page)
    expect(await itemDialog.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)
    expect(await itemDialog.getByRole('button', { name: catalog.confirmCreate }).evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)
    expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze()).violations).toEqual([])
  }
  await itemDialog.getByRole('button', { name: catalog.confirmCreate }).click()
  await expect(page).toHaveURL(/\/items\/[^/]+\/barcodes\/new$/)
  await expect(page.getByText(text.encodedBarcodeHelp)).toBeVisible()
  await page.getByRole('button', { name: owned.review }).click()
  const barcodeDialog = page.getByRole('alertdialog')
  await expect(barcodeDialog.getByRole('heading', { name: owned.reviewTitle })).toBeFocused()
  for (const colorScheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme }); await expect(page.locator('html')).toHaveAttribute('data-theme', colorScheme)
    expect(await page.evaluate(() => innerWidth)).toBe(320)
    await noOverflow(page)
    expect(await barcodeDialog.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)
    expect(await barcodeDialog.getByRole('button', { name: owned.confirmCreate.barcodes }).evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)
    expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze()).violations).toEqual([])
  }
})

test('keeps long results usable at 320px and 200% text in both themes', async ({ page, application }) => {
  await provision(page, application)
  await page.setViewportSize({ width: 320, height: 568 })
  await openScan(page); await page.locator('html').evaluate((element) => { element.style.fontSize = '200%' })
  await search(page)
  const list = page.getByRole('list', { name: text.results })
  await expect(list.getByRole('listitem')).toHaveCount(5)
  await list.getByText(text.itemDetails).first().click()
  for (const colorScheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme }); await expect(page.locator('html')).toHaveAttribute('data-theme', colorScheme)
    await noOverflow(page)
    expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze()).violations).toEqual([])
    expect(await page.getByRole('button', { name: text.search }).evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)
  }
  await page.getByRole('button', { name: catalog.nextPage }).click()
  await expect(list.getByRole('listitem')).toHaveCount(2)
  await noOverflow(page)
  expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze()).violations).toEqual([])
})
