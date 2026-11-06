import { beforeEach, describe, expect, it, vi } from 'vitest'
import { version } from '../../../package.json'
import type { Item } from './catalog'

type HoldingAPI = typeof import('./holdings')
const fetchMock = vi.fn<typeof fetch>()
const signal = () => new AbortController().signal
const csrf = 'A'.repeat(43)
const item: Item = { id: 'item-1', revision: 7, name: 'Individual', brand: null, model: '', category_id: null, description: '', notes: '',
  tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1, status: 'active' }
const quantity: Item = { ...item, id: 'quantity-1', name: 'Quantity', tracking_mode: 'quantity', unit_code: 'kg', base_unit: 'g', scale_factor: 1000 }
const unit = { id: 'unit-1', item_id: item.id, revision: 2, location_id: 'source', asset_code: '  Retained AST é  ', serial_number: null, notes: 'Original', status: 'stored' } as const
const stock = { item_id: quantity.id, location_id: 'target', revision: 2, quantity_base: 1500, unit_code: 'kg', base_unit: 'g', scale_factor: 1000 } as const
const baseContext = () => ({ csrfToken: csrf, idempotencyKey: 'original-holding-key', signal: signal() })
const unitContext = (expectedRevision = 2) => ({ ...baseContext(), itemID: item.id, expectedRevision })
const stockContext = () => ({ ...baseContext(), item: { ...quantity } })
const reply = (body: unknown, etag?: string, status = 200) => Response.json(body, { status, headers: etag === undefined ? {} : { ETag: etag } })
const receive = { item_id: quantity.id, target_location_id: 'target', quantity_base: 1000 }

beforeEach(() => { vi.resetModules(); fetchMock.mockReset(); vi.stubGlobal('fetch', fetchMock) })

describe('twelve explicit holding transports', () => {
  const cases: { name: string; method: string; path: string; body?: unknown; result: unknown; proof?: number; etag?: string; invoke: (api: HoldingAPI) => Promise<unknown> }[] = [
    { name: 'Unit detail', method: 'GET', path: '/units/unit-1', result: unit, etag: '"2"', invoke: (api) => api.fetchUnit(unit.id, signal()) },
    { name: 'owner Unit page', method: 'GET', path: '/items/item-1/units', result: { data: [unit], next_cursor: null }, invoke: (api) => api.fetchItemUnitsPage(item.id, {}, signal()) },
    { name: 'Stock page', method: 'GET', path: '/inventory', result: { data: [stock], next_cursor: null }, invoke: (api) => api.fetchStockPage({}, signal()) },
    { name: 'Unit creation', method: 'POST', path: '/items/item-1/units', body: { location_id: null }, result: { ...unit, revision: 1, location_id: null, notes: '' }, proof: 7, etag: '"1"', invoke: (api) => api.createUnit(item.id, { location_id: null }, { ...baseContext(), expectedRevision: 7 }) },
    { name: 'partial Unit edit', method: 'PATCH', path: '/units/unit-1', body: { serial_number: '', notes: 'Changed' }, result: { ...unit, revision: 3, serial_number: '', notes: 'Changed' }, proof: 2, etag: '"3"', invoke: (api) => api.patchUnit(unit.id, { serial_number: '', notes: 'Changed' }, unitContext()) },
    { name: 'Unit move', method: 'POST', path: '/units/unit-1/move', body: { target_location_id: 'target' }, result: { ...unit, revision: 3, location_id: 'target' }, proof: 2, etag: '"3"', invoke: (api) => api.moveUnit(unit.id, { target_location_id: 'target' }, unitContext()) },
    { name: 'explicit Unit disposal', method: 'POST', path: '/units/unit-1/status', body: { status: 'disposed', disposal_kind: 'recycled' }, result: { ...unit, revision: 3, status: 'disposed' }, proof: 2, etag: '"3"', invoke: (api) => api.setUnitStatus(unit.id, { status: 'disposed', disposal_kind: 'recycled' }, unitContext()) },
    { name: 'Unit deletion', method: 'DELETE', path: '/units/unit-1', result: { ok: true, operation_id: 'event', revision: 3 }, proof: 2, invoke: (api) => api.deleteUnit(unit.id, unitContext()) },
    { name: 'stock receipt', method: 'POST', path: '/stock/receive', body: receive, result: stock, proof: 7, etag: '"2"', invoke: (api) => api.receiveStock(receive, stockContext()) },
    { name: 'stock consumption', method: 'POST', path: '/stock/consume', body: { item_id: quantity.id, source_location_id: 'source', quantity_base: 500 }, result: { ...stock, location_id: 'source', quantity_base: 1000, revision: 9 }, proof: 7, etag: '"9"', invoke: (api) => api.consumeStock({ item_id: quantity.id, source_location_id: 'source', quantity_base: 500 }, stockContext()) },
    { name: 'stock transfer target', method: 'POST', path: '/stock/transfer', body: { ...receive, source_location_id: 'source' }, result: stock, proof: 7, etag: '"2"', invoke: (api) => api.transferStock({ ...receive, source_location_id: 'source' }, stockContext()) },
    { name: 'absolute stock zero', method: 'POST', path: '/stock/adjust', body: { ...receive, quantity_base: 0, reason: 'Explicit count' }, result: { ...stock, quantity_base: 0 }, proof: 7, etag: '"2"', invoke: (api) => api.adjustStock({ ...receive, quantity_base: 0, reason: 'Explicit count' }, stockContext()) },
  ]
  it.each(cases)('sends one $name under the caller original proof and no hidden defaults', async (test) => {
    fetchMock.mockResolvedValue(reply(test.result, test.etag))
    const api = await import('./holdings')
    expect(await test.invoke(api)).toEqual(test.result)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const request = fetchMock.mock.calls[0]?.[0] as Request
    expect(request.url).toBe(`http://localhost/api/v1${test.path}`)
    expect(request.method).toBe(test.method)
    expect(request.cache).toBe('no-store')
    expect(request.credentials).toBe('same-origin')
    expect(request.redirect).toBe('error')
    expect(request.headers.has('Cookie')).toBe(false)
    expect(request.headers.has('Authorization')).toBe(false)
    expect(request.headers.get('Origin')).toBe(test.method === 'GET' ? null : 'http://localhost')
    expect(request.headers.get('X-CSRF-Token')).toBe(test.method === 'GET' ? null : csrf)
    expect(request.headers.get('Idempotency-Key')).toBe(test.method === 'GET' ? null : 'original-holding-key')
    expect(request.headers.get('X-HomeBox-Client-Version')).toBe(test.method === 'GET' ? null : version)
    expect(request.headers.get('If-Match')).toBe(test.proof === undefined ? null : `"${test.proof}"`)
    expect(request.headers.has('X-Owner-Revision')).toBe(false)
    if (test.body === undefined) { expect(await request.text()).toBe(''); expect(request.headers.has('Content-Type')).toBe(false) }
    else expect(await request.json()).toEqual(test.body)
  })

  it.each(cases)('does not retry a failed $name or submit on reconnect', async (test) => {
    fetchMock.mockRejectedValue(new TypeError('private transport diagnostic'))
    const api = await import('./holdings')
    await expect(test.invoke(api)).rejects.toMatchObject({ code: 'NETWORK_ERROR' })
    window.dispatchEvent(new Event('online'))
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it.each(cases)('enforces the bounded $name deadline without a hidden retry', async (test) => {
    vi.useFakeTimers()
    try {
      fetchMock.mockImplementation((request) => new Promise((_resolve, reject) => {
        (request as Request).signal.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')), { once: true })
      }))
      const api = await import('./holdings')
      const pending = expect(test.invoke(api)).rejects.toMatchObject({ code: 'REQUEST_TIMEOUT' })
      const deadline = test.method === 'GET' ? 8_000 : 30_000
      await vi.advanceTimersByTimeAsync(deadline - 1)
      const request = fetchMock.mock.calls[0]?.[0] as Request
      expect(request.signal.aborted).toBe(false)
      await vi.advanceTimersByTimeAsync(1)
      await pending
      expect(fetchMock).toHaveBeenCalledTimes(1)
      expect(vi.getTimerCount()).toBe(0)
    } finally { vi.useRealTimers() }
  })

  it('encodes imported new/tree IDs, every exact filter and literal cursors without hidden paging', async () => {
    fetchMock.mockImplementation(async () => reply({ data: [], next_cursor: null }))
    const api = await import('./holdings')
    for (const id of ['new', 'tree', '物品 +?#']) {
      await api.fetchItemUnitsPage(id, { limit: 1, cursor: 'opaque?x=1&limit=99+#' }, signal())
      const request = fetchMock.mock.lastCall?.[0] as Request
      const url = new URL(request.url)
      expect(decodeURIComponent(url.pathname)).toBe(`/api/v1/items/${id}/units`)
      expect(url.searchParams.get('cursor')).toBe('opaque?x=1&limit=99+#')
      expect(url.searchParams.getAll('limit')).toEqual(['1'])
    }
    const query = { item_id: '物品 +?# é', from: '0001-01-01', to: '9999-12-31', currency: 'TWD', category_id: 'category', location_id: 'location', scope: 'direct', status: 'archived', merchant: '  Exact é 😀  ', limit: 100, cursor: 'x'.repeat(4096) } as const
    await api.fetchStockPage(query, signal())
    const request = fetchMock.mock.lastCall?.[0] as Request
    const url = new URL(request.url)
    for (const [key, value] of Object.entries(query)) expect(url.searchParams.getAll(key)).toEqual([String(value)])
    expect(url.searchParams.has('q')).toBe(false)
    expect(fetchMock).toHaveBeenCalledTimes(4)
  })

  it('snapshots the whole reviewed Item interpretation, body, key and proof before an in-flight result', async () => {
    const api = await import('./holdings')
    let complete: ((response: Response) => void) | undefined
    fetchMock.mockImplementation(() => new Promise((resolve) => { complete = resolve }))
    const context = stockContext()
    const body = { ...receive }
    const pending = api.receiveStock(body, context)
    context.item.id = 'changed-owner'; context.item.revision = 99; context.item.scale_factor = 1
    context.idempotencyKey = 'changed-after-sending'
    body.item_id = 'changed-owner'; body.quantity_base = 1
    complete?.(reply(stock, '"2"'))
    expect(await pending).toEqual(stock)
    const request = fetchMock.mock.calls[0]?.[0] as Request
    expect(await request.json()).toEqual(receive)
    expect(request.headers.get('If-Match')).toBe('"7"')
    expect(request.headers.get('Idempotency-Key')).toBe('original-holding-key')
  })

  it('does not use credential, replica, cache or draft storage or replay on reconnect', async () => {
    const spies = [vi.spyOn(localStorage, 'getItem'), vi.spyOn(localStorage, 'setItem'), vi.spyOn(sessionStorage, 'getItem'), vi.spyOn(sessionStorage, 'setItem'), vi.spyOn(document, 'cookie', 'get'), vi.spyOn(document, 'cookie', 'set')]
    const database = vi.fn(), cache = vi.fn()
    vi.stubGlobal('indexedDB', { open: database }); vi.stubGlobal('caches', { open: cache })
    fetchMock.mockImplementation(async () => reply(stock, '"2"'))
    const api = await import('./holdings')
    await api.receiveStock(receive, stockContext())
    window.dispatchEvent(new Event('online'))
    for (const spy of [...spies, database, cache]) expect(spy).not.toHaveBeenCalled()
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })
})

describe('strict holding input boundaries', () => {
  it('rejects invalid path/owner IDs, proofs and independent loose stock proofs before sending', async () => {
    const api = await import('./holdings')
    for (const id of ['', '.', '..', 'x/y', 'x\\y', '\0', '😀'.repeat(129), '\uD800']) {
      await expect(api.fetchUnit(id, signal())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
      await expect(api.fetchItemUnitsPage(id, {}, signal())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
      await expect(api.createUnit(id, { location_id: null }, { ...baseContext(), expectedRevision: 7 })).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
      await expect(api.patchUnit(id, { notes: '' }, unitContext())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
      await expect(api.moveUnit(id, { target_location_id: 'target' }, unitContext())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
      await expect(api.setUnitStatus(id, { status: 'lost' }, unitContext())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
      await expect(api.deleteUnit(id, unitContext())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    }
    for (const expectedRevision of [undefined, 0, -1, 1.5, Number.MAX_SAFE_INTEGER + 1]) {
      const context = { ...unitContext(), expectedRevision } as never
      await expect(api.createUnit(item.id, { location_id: null }, context)).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
      await expect(api.patchUnit(unit.id, { notes: '' }, context)).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
      await expect(api.deleteUnit(unit.id, context)).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
      await expect(api.receiveStock(receive, { ...stockContext(), item: { ...quantity, revision: expectedRevision } } as never)).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    }
    for (const invalid of [{ ...stockContext(), expectedRevision: 7 }, { ...stockContext(), item: { ...quantity, tracking_mode: 'individual' } }, { ...stockContext(), item: { ...quantity, id: 'other' } }, { ...stockContext(), item: { ...quantity, base_unit: 'g\n' } }, { ...stockContext(), item: { ...quantity, scale_factor: 0 } }, { ...baseContext() }]) {
      await expect(api.receiveStock(receive, invalid as never)).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    }
    await expect(api.patchUnit(unit.id, { notes: '' }, { ...unitContext(), itemID: '\uD800' })).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it.each([
    {}, { location_id: undefined }, { location_id: '' }, { location_id: null, status: 'retired' }, { location_id: null, status: 'disposed' },
    { location_id: null, serial_number: 123 }, { location_id: null, notes: null }, { location_id: null, asset_code: 'caller' },
    { location_id: null, item_id: 'other' }, { location_id: null, serial_number: '\uD800' }, { location_id: null, notes: '\0' + '界'.repeat(10000) },
  ])('rejects invalid Unit creation %j', async (body) => {
    const api = await import('./holdings')
    await expect(api.createUnit(item.id, body as never, { ...baseContext(), expectedRevision: 7 })).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it.each([{}, { notes: undefined }, { serial_number: undefined }, { notes: null }, { serial_number: 1 }, { location_id: null }, { status: 'lost' }, { asset_code: 'caller' }, { item_id: 'other' }, { notes: '\uD800' }])('rejects invalid partial Unit edit %j', async (body) => {
    const api = await import('./holdings')
    await expect(api.patchUnit(unit.id, body as never, unitContext())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('requires a complete exact nonnull move target without metadata or lifecycle fields', async () => {
    const api = await import('./holdings')
    for (const body of [{}, { target_location_id: null }, { target_location_id: '' }, { target_location_id: '\uD800' }, { target_location_id: 'target', notes: 'Implicit edit' }, { target_location_id: 'target', status: 'stored' }]) {
      await expect(api.moveUnit(unit.id, body as never, unitContext())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    }
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it.each([{}, { status: 'unknown' }, { status: 'stored', note: null }, { status: 'disposed' }, { status: 'disposed', disposal_kind: null }, { status: 'disposed', disposal_kind: 'other' }, { status: 'stored', disposal_kind: 'discarded' }, { status: 'disposed', disposal_kind: 'sold', currency: 'TWD' }])('rejects invalid Unit lifecycle %j', async (body) => {
    const api = await import('./holdings')
    await expect(api.setUnitStatus(unit.id, body as never, unitContext())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('rejects unsafe/converted stock, incomplete transfer/adjustment and cursor/filter scopes', async () => {
    const api = await import('./holdings')
    for (const quantity_base of [-1, 0, 0.1, Number.MAX_SAFE_INTEGER + 1, Infinity, NaN, '1000', null]) {
      await expect(api.receiveStock({ ...receive, quantity_base } as never, stockContext())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
      await expect(api.consumeStock({ item_id: quantity.id, source_location_id: 'source', quantity_base } as never, stockContext())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    }
    for (const body of [{ ...receive, scale_factor: 1000 }, { ...receive, item_id: '' }, { ...receive, target_location_id: null }, { ...receive, note: '\uD800' }]) await expect(api.receiveStock(body as never, stockContext())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    await expect(api.transferStock(receive as never, stockContext())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    await expect(api.transferStock({ ...receive, source_location_id: 'target' }, stockContext())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    for (const body of [{ ...receive, quantity_base: 0 }, { ...receive, quantity_base: -1, reason: 'Count' }, { ...receive, reason: '' }, { ...receive, reason: '\0' + '界'.repeat(10000) }]) await expect(api.adjustStock(body as never, stockContext())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    for (const query of [{ limit: 0 }, { limit: 101 }, { cursor: '' }, { cursor: '\uD800' }, { unknown: true }]) {
      await expect(api.fetchStockPage(query as never, signal())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
      await expect(api.fetchItemUnitsPage(item.id, query as never, signal())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    }
    for (const query of [{ from: '0000-01-01' }, { from: '2026-02-30' }, { from: '2026-11-06', to: '2026-11-05' }, { currency: 'twd' }, { status: 'stored' }, { scope: 'other' }, { merchant: '' }, { item_id: '' }, { item_id: null }, { item_id: '😀'.repeat(129) }, { item_id: '\uD800' }, { q: 'uncontracted' }, { cursor: 'x'.repeat(4097) }]) await expect(api.fetchStockPage(query as never, signal())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    await expect(api.fetchItemUnitsPage(item.id, { cursor: 'x'.repeat(2049) }, signal())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    expect(fetchMock).not.toHaveBeenCalled()
  })
})

describe('strict holding replies and caller-owned proof scopes', () => {
  it.each([
    { ...unit, id: '' }, { ...unit, id: 'other' }, { ...unit, item_id: '' }, { ...unit, revision: 0 },
    { ...unit, revision: Number.MAX_SAFE_INTEGER + 1 }, { ...unit, asset_code: '' }, { ...unit, asset_code: 'x'.repeat(10001) },
    { ...unit, serial_number: 1 }, { ...unit, notes: null }, { ...unit, notes: '\uD800' }, { ...unit, status: 'archived' },
    { ...unit, location_id: undefined }, { ...unit, raw_ocr: 'Private' }, null, [], {},
  ])('rejects malformed or unrelated Unit reply %#', async (value) => {
    fetchMock.mockResolvedValue(reply(value, '"2"'))
    const api = await import('./holdings')
    await expect(api.fetchUnit(unit.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  })

  it('requires exact owners, promised fields, noop-or-next Unit proofs and strong ETags', async () => {
    const api = await import('./holdings')
    for (const etag of [undefined, '', '2', 'W/"2"', '"3"', '"2", "2"', '*']) {
      fetchMock.mockResolvedValueOnce(reply(unit, etag))
      await expect(api.patchUnit(unit.id, { notes: unit.notes }, unitContext())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
    for (const value of [{ ...unit, item_id: 'other-owner' }, { ...unit, revision: 1 }, { ...unit, revision: 4 }, { ...unit, notes: 'Not requested' }]) {
      fetchMock.mockResolvedValueOnce(reply(value, `"${value.revision}"`))
      await expect(api.patchUnit(unit.id, { notes: unit.notes }, unitContext())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
    fetchMock.mockResolvedValueOnce(reply(unit, '"2"'))
    expect(await api.patchUnit(unit.id, { notes: unit.notes }, unitContext())).toEqual(unit)
    fetchMock.mockResolvedValueOnce(reply({ ...unit, revision: 3, serial_number: null }, '"3"'))
    expect((await api.patchUnit(unit.id, { serial_number: null }, unitContext())).serial_number).toBeNull()
    fetchMock.mockResolvedValueOnce(reply({ ...unit, revision: 3 }, '"3"'))
    await expect(api.moveUnit(unit.id, { target_location_id: 'target' }, unitContext())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    fetchMock.mockResolvedValueOnce(reply({ ...unit, revision: 3 }, '"3"'))
    await expect(api.setUnitStatus(unit.id, { status: 'lost' }, unitContext())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    for (const value of [{ ...unit, revision: 1, location_id: null, notes: '', status: 'lost' }, { ...unit, revision: 2, location_id: null, notes: '' }, { ...unit, revision: 1, item_id: 'other', location_id: null, notes: '' }, { ...unit, revision: 1, location_id: null, notes: '', serial_number: '' }]) {
      fetchMock.mockResolvedValueOnce(reply(value, `"${value.revision}"`))
      await expect(api.createUnit(item.id, { location_id: null }, { ...baseContext(), expectedRevision: 7 })).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
  })

  it.each([
    { ...stock, item_id: 'other' }, { ...stock, location_id: 'source' }, { ...stock, unit_code: 'L' },
    { ...stock, base_unit: 'ml' }, { ...stock, base_unit: 'g\n' }, { ...stock, scale_factor: 1 },
    { ...stock, quantity_base: 999 }, { ...stock, quantity_base: 0.5 }, { ...stock, quantity_base: Number.MAX_SAFE_INTEGER + 1 },
    { ...stock, revision: 0 }, { ...stock, revision: Number.MAX_SAFE_INTEGER + 1 }, { ...stock, raw_ocr: 'Private' }, null, {},
  ])('rejects wrong Stock pair, interpretation or quantity reply %#', async (value) => {
    fetchMock.mockResolvedValue(reply(value, '"2"'))
    const api = await import('./holdings')
    await expect(api.receiveStock(receive, stockContext())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  })

  it('keeps Stock counters independent and validates affected source/target/absolute quantities', async () => {
    const api = await import('./holdings')
    for (const revision of [1, 2, 7, 8, Number.MAX_SAFE_INTEGER]) {
      const value = { ...stock, revision }
      fetchMock.mockResolvedValueOnce(reply(value, `"${revision}"`))
      expect(await api.receiveStock(receive, stockContext())).toEqual(value)
    }
    for (const etag of [undefined, '', '2', 'W/"2"', '"3"', '"2", "2"']) {
      fetchMock.mockResolvedValueOnce(reply(stock, etag))
      await expect(api.receiveStock(receive, stockContext())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
    fetchMock.mockResolvedValueOnce(reply(stock, '"2"'))
    await expect(api.consumeStock({ item_id: quantity.id, source_location_id: 'source', quantity_base: 1 }, stockContext())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    fetchMock.mockResolvedValueOnce(reply({ ...stock, location_id: 'source', quantity_base: Number.MAX_SAFE_INTEGER }, '"2"'))
    await expect(api.consumeStock({ item_id: quantity.id, source_location_id: 'source', quantity_base: 1 }, stockContext())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    fetchMock.mockResolvedValueOnce(reply({ ...stock, quantity_base: 999 }, '"2"'))
    await expect(api.transferStock({ ...receive, source_location_id: 'source' }, stockContext())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    fetchMock.mockResolvedValueOnce(reply(stock, '"2"'))
    await expect(api.adjustStock({ ...receive, quantity_base: 0, reason: 'Counted' }, stockContext())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  })

  it('requires every Item-scoped Stock row to match the exact selected identity rather than filtering a page locally', async () => {
    const api = await import('./holdings')
    const page = { data: [stock], next_cursor: null }
    fetchMock.mockResolvedValueOnce(reply(page))
    expect(await api.fetchStockPage({ item_id: quantity.id }, signal())).toEqual(page)
    const request = fetchMock.mock.calls[0]?.[0] as Request
    expect(new URL(request.url).searchParams.get('item_id')).toBe(quantity.id)
    fetchMock.mockResolvedValueOnce(reply({ ...page, data: [{ ...stock, item_id: 'another' }] }))
    await expect(api.fetchStockPage({ item_id: quantity.id }, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    for (const id of ['new', 'tree', '😀'.repeat(128), '\0'.repeat(128)]) {
      fetchMock.mockResolvedValueOnce(reply({ data: [], next_cursor: null }))
      expect(await api.fetchStockPage({ item_id: id }, signal())).toEqual({ data: [], next_cursor: null })
    }
    expect(fetchMock).toHaveBeenCalledTimes(6)
  })

  it('preserves full retained Unit text, null/empty states and maximum canonical-pair components', async () => {
    const api = await import('./holdings')
    const id = '😀'.repeat(128), text = '\0' + '界'.repeat(9999)
    const retained = { ...unit, id, item_id: id, revision: Number.MAX_SAFE_INTEGER, location_id: null, asset_code: text, serial_number: '', notes: text, status: 'disposed' }
    fetchMock.mockResolvedValueOnce(reply({ data: [retained], next_cursor: null }))
    expect((await api.fetchItemUnitsPage(id, {}, signal())).data[0]).toEqual(retained)
    const maximum = { ...stock, item_id: '\u0001'.repeat(128), location_id: '\u0002'.repeat(128), quantity_base: 0, revision: Number.MAX_SAFE_INTEGER }
    fetchMock.mockResolvedValueOnce(reply({ data: [maximum], next_cursor: null }))
    expect((await api.fetchStockPage({}, signal())).data[0]).toEqual(maximum)
  })

  it('requires complete bounded live pages, distinct identities and no collection ETag', async () => {
    const api = await import('./holdings')
    for (const page of [{ data: [] }, { data: [], next_cursor: '' }, { data: [], next_cursor: 'next' }, { data: [unit], next_cursor: 'same' }, { data: [unit, unit], next_cursor: null }, { data: [{ ...unit, item_id: 'other' }], next_cursor: null }, { data: [], next_cursor: 'x'.repeat(2049) }, { data: [], next_cursor: null, total: 1 }]) {
      fetchMock.mockResolvedValueOnce(reply(page))
      await expect(api.fetchItemUnitsPage(item.id, { limit: 1, cursor: 'same' }, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
    fetchMock.mockResolvedValueOnce(reply({ data: [unit], next_cursor: null }, '"2"'))
    await expect(api.fetchItemUnitsPage(item.id, {}, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    for (const page of [{ data: [] }, { data: [], next_cursor: '' }, { data: [], next_cursor: 'next' }, { data: [stock], next_cursor: 'same' }, { data: [stock, stock], next_cursor: null }, { data: [], next_cursor: 'x'.repeat(4097) }, { data: [], next_cursor: null, total_quantity_base: 1 }]) {
      fetchMock.mockResolvedValueOnce(reply(page))
      await expect(api.fetchStockPage({ limit: 1, cursor: 'same' }, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
    fetchMock.mockResolvedValueOnce(reply({ data: [stock], next_cursor: null }, '"2"'))
    await expect(api.fetchStockPage({}, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    fetchMock.mockResolvedValueOnce(reply({ data: [stock], next_cursor: null }))
    await expect(api.fetchStockPage({ location_id: 'other', scope: 'direct' }, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    for (const count of [51, 101]) {
      fetchMock.mockResolvedValueOnce(reply({ data: Array.from({ length: count }, (_, index) => ({ ...unit, id: `unit-${index}` })), next_cursor: null }))
      await expect(api.fetchItemUnitsPage(item.id, {}, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
    const pair = { data: [stock, { ...stock, location_id: 'other' }], next_cursor: null }
    fetchMock.mockResolvedValueOnce(reply(pair))
    expect(await api.fetchStockPage({ limit: 2 }, signal())).toEqual(pair)
    const full = { data: Array.from({ length: 50 }, (_, index) => ({ ...unit, id: `unit-${index}` })), next_cursor: 'next' }
    fetchMock.mockResolvedValueOnce(reply(full))
    expect(await api.fetchItemUnitsPage(item.id, {}, signal())).toEqual(full)
    fetchMock.mockResolvedValueOnce(reply({ data: [stock], next_cursor: 'x'.repeat(4096) }))
    expect((await api.fetchStockPage({ limit: 1 }, signal())).next_cursor).toHaveLength(4096)
  })

  it('requires exact next Unit deletion acknowledgements and never a resource ETag', async () => {
    const api = await import('./holdings')
    for (const body of [{ ok: true, operation_id: 'event' }, { ok: false, operation_id: 'event', revision: 3 }, { ok: true, operation_id: '', revision: 3 }, { ok: true, operation_id: 'event', revision: 2 }, { ok: true, operation_id: 'event', revision: 4 }, { ok: true, operation_id: 'event', revision: 3, private: 'x' }]) {
      fetchMock.mockResolvedValueOnce(reply(body))
      await expect(api.deleteUnit(unit.id, unitContext())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
    fetchMock.mockResolvedValueOnce(reply({ ok: true, operation_id: 'event', revision: 3 }, '"3"'))
    await expect(api.deleteUnit(unit.id, unitContext())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  })

  it('retains safe failures for only one request and permits only an explicit identical retry', async () => {
    const api = await import('./holdings')
    const error = { error: { code: 'INSUFFICIENT_STOCK', request_id: 'safe-request', message: 'password=private', details: { path: '/private/database' } } }
    fetchMock.mockResolvedValueOnce(reply(error, undefined, 409))
    const context = stockContext()
    const failure = await api.receiveStock(receive, context).catch((error: unknown) => error)
    expect(failure).toMatchObject({ code: 'INSUFFICIENT_STOCK', status: 409, requestId: 'safe-request' })
    expect(JSON.stringify(failure)).not.toMatch(/password=|private|message|details|cause/)
    window.dispatchEvent(new Event('online'))
    expect(fetchMock).toHaveBeenCalledTimes(1)
    fetchMock.mockResolvedValueOnce(reply(stock, '"2"'))
    expect(await api.receiveStock(receive, context)).toEqual(stock)
    const first = fetchMock.mock.calls[0]?.[0] as Request, second = fetchMock.mock.calls[1]?.[0] as Request
    expect(await first.text()).toBe(await second.text())
    expect(first.headers.get('If-Match')).toBe(second.headers.get('If-Match'))
    expect(first.headers.get('Idempotency-Key')).toBe(second.headers.get('Idempotency-Key'))
  })

  it('honors cancellation, read/write deadlines and late replies without replay', async () => {
    vi.useFakeTimers()
    try {
      const api = await import('./holdings')
      fetchMock.mockImplementation((request) => new Promise((_resolve, reject) => {
        (request as Request).signal.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')), { once: true })
      }))
      const aborted = new AbortController(); aborted.abort()
      await expect(api.fetchStockPage({}, aborted.signal)).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
      expect(fetchMock).not.toHaveBeenCalled()
      const read = expect(api.fetchUnit(unit.id, signal())).rejects.toMatchObject({ code: 'REQUEST_TIMEOUT' })
      await vi.advanceTimersByTimeAsync(8_000); await read
      const write = expect(api.receiveStock(receive, stockContext())).rejects.toMatchObject({ code: 'REQUEST_TIMEOUT' })
      await vi.advanceTimersByTimeAsync(30_000); await write
      let release: ((response: Response) => void) | undefined
      fetchMock.mockImplementation(() => new Promise((resolve) => { release = resolve }))
      const controller = new AbortController()
      const late = expect(api.deleteUnit(unit.id, { ...unitContext(), signal: controller.signal })).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
      controller.abort(); release?.(reply({ ok: true, operation_id: 'event', revision: 3 }))
      await late
      expect(fetchMock).toHaveBeenCalledTimes(3)
      expect(vi.getTimerCount()).toBe(0)
    } finally { vi.useRealTimers() }
  })
})
