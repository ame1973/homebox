import { beforeEach, expect, it, vi } from 'vitest'

const fetchMock = vi.fn<typeof fetch>(), signal = () => new AbortController().signal
const exact = 'x'.repeat(2044) + '😀', overflow = `${exact}x`
const stockExact = 'x'.repeat(4092) + '😀', stockOverflow = `${stockExact}x`
const location = { id: 'location', revision: 1, parent_id: null, name: 'Location', code: 'LOC', kind: 'other', status: 'active', sort_order: 0, description: '' }
const category = { id: 'category', revision: 1, parent_id: null, name: 'Category' }, tag = { id: 'tag', revision: 1, name: 'Tag' }
const item = { id: 'item', revision: 1, name: 'Item', brand: null, model: null, category_id: null, description: '', notes: '', status: 'active', tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1 }
const barcode = { id: 'barcode', item_id: item.id, revision: 1, symbology: 'OTHER', raw_value: 'raw', normalized_value: 'raw' }
const alias = { id: 'alias', item_id: item.id, revision: 1, alias: 'Alias' }, attribute = { id: 'attribute', item_id: item.id, revision: 1, attribute_key: 'Key', attribute_value: '' }
const unit = { id: 'unit', item_id: item.id, revision: 1, location_id: null, asset_code: 'AST', status: 'stored', serial_number: null, notes: '' }
const stock = { item_id: item.id, location_id: 'location', revision: 1, quantity_base: 0, unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1 }
const purchase = { id: 'purchase', revision: 1, merchant_name: 'Merchant', purchased_at: '2026-11-07T00:00:00Z', currency: 'JPY', receipt_note: '' }
const line = { id: 'line', revision: 1, purchase_id: purchase.id, item_id: item.id, item_unit_id: null, quantity_base: 1, paid_minor: 0, note: '' }
const purchaseCorrection = { id: 'purchase-correction', revision: 1, purchase_id: purchase.id, action: 'void', reason: 'Reason', created_at: '2026-11-07T00:00:00Z', before: line, after: null }
const asset = { item_unit_id: unit.id, revision: 1, cost_basis: 'manual', currency: 'JPY', manual_acquisition_minor: 0, in_service_at: null, retired_at: null, estimated_residual_minor: null, usage_unit: 'times' }
const usage = { id: 'usage', item_unit_id: unit.id, revision: 1, delta: 1, usage_unit: 'times', event_type: 'use', occurred_at: '2026-11-07T00:00:00Z', correction_of: null, note: '' }
const cost = { id: 'cost', item_unit_id: unit.id, revision: 1, amount_minor: 1, currency: 'JPY', kind: 'repair', direction: 'expense', occurred_at: '2026-11-07T00:00:00Z', correction_of: null, note: '' }
const disposal = { item_unit_id: unit.id, kind: 'sold', disposed_at: '2026-11-07T00:00:00Z', realized_proceeds_minor: 0, currency: 'JPY', note: '' }
const disposalRecord = { item_unit_id: unit.id, revision: 1, original: disposal, current: disposal }
const disposalCorrection = { id: 'disposal-correction', revision: 1, item_unit_id: unit.id, action: 'void', reason: 'Reason', created_at: '2026-11-07T00:00:00Z', before: disposalRecord, after: { ...disposalRecord, revision: 2, current: null } }

beforeEach(() => { vi.resetModules(); fetchMock.mockReset(); vi.stubGlobal('fetch', fetchMock) })

it('counts complete UTF-8 bytes without repairing Unicode, preserving Stock’s larger limit', async () => {
  const { validCursor, cursorSchema } = await import('./domain')
  for (const [value, expected] of [[exact, true], [overflow, false], ['é'.repeat(1024), true], ['é'.repeat(1025), false], ['\ud800', false], ['', false], ['x'.repeat(2049), false]] as const) {
    expect(validCursor(value)).toBe(expected); expect(cursorSchema().safeParse(value).success).toBe(expected)
  }
  expect(validCursor(stockExact, 4096)).toBe(true); expect(validCursor(stockOverflow, 4096)).toBe(false)
  expect(cursorSchema(4096).safeParse(stockExact).success).toBe(true); expect(cursorSchema(4096).safeParse(stockOverflow).success).toBe(false)
})

it('enforces the shared byte boundary on every live collection request and returned continuation', async () => {
  const [locations, taxonomy, catalog, links, holdings, purchases, assets, corrections] = await Promise.all([
    import('./locations'), import('./taxonomy'), import('./catalog'), import('./catalog-links'), import('./holdings'), import('./purchases'), import('./assets'), import('./financial-corrections'),
  ])
  type Query = { limit?: number; cursor?: string }
  const cases: { name: string; row: unknown; read: (query: Query) => Promise<{ next_cursor?: string | null }> }[] = [
    { name: 'locations', row: location, read: (q) => locations.fetchLocationsPage(q, signal()) },
    { name: 'categories', row: category, read: (q) => taxonomy.fetchCategoriesPage(q, signal()) },
    { name: 'tags', row: tag, read: (q) => taxonomy.fetchTagsPage(q, signal()) },
    { name: 'items', row: item, read: (q) => catalog.fetchItemsPage(q, signal()) },
    { name: 'barcodes', row: barcode, read: (q) => links.fetchItemBarcodesPage(item.id, q, signal()) },
    { name: 'aliases', row: alias, read: (q) => links.fetchItemAliasesPage(item.id, q, signal()) },
    { name: 'attributes', row: attribute, read: (q) => links.fetchItemAttributesPage(item.id, q, signal()) },
    { name: 'item tags', row: tag, read: (q) => links.fetchItemTagsPage(item.id, q, signal()) },
    { name: 'units', row: unit, read: (q) => holdings.fetchItemUnitsPage(item.id, q, signal()) },
    { name: 'purchases', row: purchase, read: (q) => purchases.fetchPurchasesPage(q, signal()) },
    { name: 'item purchases', row: purchase, read: (q) => purchases.fetchItemPurchasesPage(item.id, q, signal()) },
    { name: 'purchase lines', row: line, read: (q) => purchases.fetchPurchaseLinesPage(purchase.id, q, signal()) },
    { name: 'purchase corrections', row: purchaseCorrection, read: (q) => corrections.fetchPurchaseCorrectionsPage(purchase.id, q, signal()) },
    { name: 'assets', row: asset, read: (q) => assets.fetchAssetsPage(q, signal()) },
    { name: 'usage', row: usage, read: (q) => assets.fetchAssetUsagePage(unit.id, q, signal()) },
    { name: 'costs', row: cost, read: (q) => assets.fetchAssetCostsPage(unit.id, q, signal()) },
    { name: 'disposal corrections', row: disposalCorrection, read: (q) => corrections.fetchDisposalCorrectionsPage(unit.id, q, signal()) },
  ]
  for (const test of cases) {
    fetchMock.mockResolvedValueOnce(Response.json({ data: [test.row], next_cursor: exact }))
    await expect(test.read({ limit: 1 }), test.name).resolves.toMatchObject({ next_cursor: exact })
    fetchMock.mockResolvedValueOnce(Response.json({ data: [test.row], next_cursor: overflow }))
    await expect(test.read({ limit: 1 }), test.name).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    fetchMock.mockResolvedValueOnce(Response.json({ data: [], next_cursor: null }))
    await expect(test.read({ limit: 1, cursor: exact }), test.name).resolves.toMatchObject({ next_cursor: null })
    const count = fetchMock.mock.calls.length
    await expect(test.read({ cursor: overflow }), test.name).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    await expect(test.read({ cursor: '\ud800' }), test.name).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    expect(fetchMock.mock.calls.length, test.name).toBe(count)
  }
  fetchMock.mockResolvedValueOnce(Response.json({ data: [stock], next_cursor: stockExact }))
  await expect(holdings.fetchStockPage({ limit: 1 }, signal())).resolves.toMatchObject({ next_cursor: stockExact })
  fetchMock.mockResolvedValueOnce(Response.json({ data: [stock], next_cursor: stockOverflow }))
  await expect(holdings.fetchStockPage({ limit: 1 }, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  fetchMock.mockResolvedValueOnce(Response.json({ data: [], next_cursor: null }))
  await expect(holdings.fetchStockPage({ cursor: stockExact }, signal())).resolves.toMatchObject({ next_cursor: null })
  const count = fetchMock.mock.calls.length
  await expect(holdings.fetchStockPage({ cursor: stockOverflow }, signal())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
  expect(fetchMock.mock.calls.length).toBe(count)
})

it('retains only byte-valid cursor trails through catalog, purchase and Asset navigation', async () => {
  const { catalogNavigationState } = await import('../catalog-navigation'), { purchaseCursorTrail } = await import('../purchase-navigation'), { assetNavigationState } = await import('../asset-navigation')
  expect(catalogNavigationState('/items', [null, exact]).catalogCursors).toEqual([null, exact])
  expect(catalogNavigationState('/items', [null, overflow]).catalogCursors).toEqual([])
  expect(purchaseCursorTrail([null, exact])).toEqual([null, exact]); expect(purchaseCursorTrail([null, overflow])).toEqual([])
  expect(assetNavigationState('/assets', [null, exact]).assetCursors).toEqual([null, exact]); expect(assetNavigationState('/assets', [null, overflow]).assetCursors).toEqual([])
})
