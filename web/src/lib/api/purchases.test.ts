import { beforeEach, describe, expect, it, vi } from 'vitest'
import { version } from '../../../package.json'
import type { Item } from './catalog'
import type { Purchase, PurchaseLine } from './purchases'

type PurchaseAPI = typeof import('./purchases')
const fetchMock = vi.fn<typeof fetch>()
const signal = () => new AbortController().signal
const csrf = 'A'.repeat(43)
const item: Item = { id: 'item-1', revision: 7, name: 'Reviewed quantity', brand: null, model: '', category_id: null, description: '', notes: '',
  tracking_mode: 'quantity', unit_code: 'kg', base_unit: 'g', scale_factor: 1000, status: 'active' }
const purchase: Purchase = { id: 'purchase-1', revision: 12, merchant_name: '  Exact é merchant  ', currency: 'JPY',
  purchased_at: '2026-11-06T12:00:00.123456789+08:00', receipt_note: 'Original receipt' }
const header = { merchant_name: purchase.merchant_name, currency: purchase.currency, purchased_at: purchase.purchased_at }
const created: Purchase = { ...purchase, revision: 1, purchased_at: '2026-11-06T04:00:00.123456789Z', receipt_note: '' }
const allocation = { item_id: item.id, item_revision: item.revision, item_unit_id: null, quantity_base: 1500, paid_minor: 0 } as const
const line: PurchaseLine = { id: 'line-1', revision: 1, purchase_id: purchase.id, item_id: item.id, item_unit_id: null, quantity_base: 1500, paid_minor: 0, note: '' }
const context = () => ({ csrfToken: csrf, idempotencyKey: 'original-purchase-key', signal: signal() })
const purchaseContext = () => ({ ...context(), purchase: { ...purchase } })
const lineContext = () => ({ ...purchaseContext(), item: { ...item } })
const reply = (value: unknown, etag?: string, status = 200) => Response.json(value, { status, headers: etag === undefined ? {} : { ETag: etag } })
function capturedRequest(index = fetchMock.mock.calls.length - 1): Request {
  const value = fetchMock.mock.calls[index]?.[0]
  if (!(value instanceof Request)) throw new Error('Expected a captured purchase request')
  return value
}

beforeEach(() => { vi.resetModules(); fetchMock.mockReset(); vi.stubGlobal('fetch', fetchMock) })

const cases: { name: string; method: string; path: string; body?: unknown; result: unknown; etag?: string; proof?: number; invoke: (api: PurchaseAPI) => Promise<unknown> }[] = [
  { name: 'global header page', method: 'GET', path: '/purchases', result: { data: [purchase], next_cursor: null }, invoke: (api) => api.fetchPurchasesPage({}, signal()) },
  { name: 'header detail', method: 'GET', path: '/purchases/purchase-1', result: purchase, etag: '"12"', invoke: (api) => api.fetchPurchase(purchase.id, signal()) },
  { name: 'Item-associated headers', method: 'GET', path: '/items/item-1/purchases', result: { data: [purchase], next_cursor: null }, invoke: (api) => api.fetchItemPurchasesPage(item.id, {}, signal()) },
  { name: 'independent owned Lines', method: 'GET', path: '/purchases/purchase-1/lines', result: { data: [line], next_cursor: null }, invoke: (api) => api.fetchPurchaseLinesPage(purchase.id, {}, signal()) },
  { name: 'header creation', method: 'POST', path: '/purchases', body: header, result: created, etag: '"1"', invoke: (api) => api.createPurchase(header, context()) },
  { name: 'partial correction', method: 'PATCH', path: '/purchases/purchase-1', body: { receipt_note: '' }, result: { ...purchase, revision: 13, receipt_note: '' }, proof: 12, etag: '"13"', invoke: (api) => api.patchPurchase(purchase.id, { receipt_note: '' }, purchaseContext()) },
  { name: 'immutable allocation', method: 'POST', path: '/purchases/purchase-1/lines', body: allocation, result: line, proof: 12, etag: '"1"', invoke: (api) => api.createPurchaseLine(purchase.id, allocation, lineContext()) },
]

describe('seven stateless purchase transports', () => {
  it.each(cases)('sends exactly one $name with original independent proofs and no defaults', async (test) => {
    fetchMock.mockResolvedValue(reply(test.result, test.etag))
    const api = await import('./purchases')
    expect(await test.invoke(api)).toEqual(test.result)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const request = fetchMock.mock.calls[0]?.[0] as Request
    expect(request.url).toBe(`http://localhost/api/v1${test.path}`)
    expect(request.method).toBe(test.method)
    expect(request.cache).toBe('no-store'); expect(request.credentials).toBe('same-origin'); expect(request.redirect).toBe('error')
    expect(request.headers.has('Cookie')).toBe(false); expect(request.headers.has('Authorization')).toBe(false)
    expect(request.headers.get('Origin')).toBe(test.method === 'GET' ? null : 'http://localhost')
    expect(request.headers.get('X-CSRF-Token')).toBe(test.method === 'GET' ? null : csrf)
    expect(request.headers.get('Idempotency-Key')).toBe(test.method === 'GET' ? null : 'original-purchase-key')
    expect(request.headers.get('X-HomeBox-Client-Version')).toBe(test.method === 'GET' ? null : version)
    expect(request.headers.get('If-Match')).toBe(test.proof === undefined ? null : `"${test.proof}"`)
    expect(request.headers.has('X-Item-Revision')).toBe(false)
    if (test.body === undefined) { expect(await request.text()).toBe(''); expect(request.headers.has('Content-Type')).toBe(false) }
    else expect(await request.json()).toEqual(test.body)
  })

  it.each(cases)('never retries a failed $name on reconnect', async (test) => {
    fetchMock.mockRejectedValue(new TypeError('private transport diagnostic'))
    const api = await import('./purchases')
    await expect(test.invoke(api)).rejects.toMatchObject({ code: 'NETWORK_ERROR' })
    window.dispatchEvent(new Event('online'))
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it.each(cases)('bounds the $name deadline without replay', async (test) => {
    vi.useFakeTimers()
    try {
      fetchMock.mockImplementation((request) => new Promise((_resolve, reject) => {
        (request as Request).signal.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')), { once: true })
      }))
      const api = await import('./purchases')
      const pending = expect(test.invoke(api)).rejects.toMatchObject({ code: 'REQUEST_TIMEOUT' })
      const timeout = test.method === 'GET' ? 8_000 : 30_000
      await vi.advanceTimersByTimeAsync(timeout - 1)
      expect(capturedRequest(0).signal.aborted).toBe(false)
      await vi.advanceTimersByTimeAsync(1); await pending
      expect(fetchMock).toHaveBeenCalledTimes(1); expect(vi.getTimerCount()).toBe(0)
    } finally { vi.useRealTimers() }
  })

  it('preserves exact filters, imported IDs, encoded cursors and the actual raw query budget', async () => {
    const api = await import('./purchases')
    fetchMock.mockImplementation(async () => reply({ data: [], next_cursor: null }))
    const query = { from: '0001-01-01', to: '9999-12-31', currency: 'ZZZ', category_id: '分類 +?#', location_id: '位置 +?#',
      scope: 'direct', status: 'archived', merchant: '  Exact é 😀  ', limit: 100, cursor: 'opaque?x=1&limit=99+#' } as const
    await api.fetchPurchasesPage(query, signal())
    const url = new URL(capturedRequest().url)
    for (const [name, value] of Object.entries(query)) expect(url.searchParams.getAll(name)).toEqual([String(value)])
    expect(url.searchParams.has('q')).toBe(false)
    for (const id of ['new', 'tree', '採購 +?# é']) {
      await api.fetchPurchaseLinesPage(id, { limit: 1, cursor: query.cursor }, signal())
      let request = fetchMock.mock.lastCall?.[0] as Request
      expect(decodeURIComponent(new URL(request.url).pathname)).toBe(`/api/v1/purchases/${id}/lines`)
      expect(new URL(request.url).searchParams.get('cursor')).toBe(query.cursor)
      await api.fetchItemPurchasesPage(id, {}, signal())
      request = fetchMock.mock.lastCall?.[0] as Request
      expect(decodeURIComponent(new URL(request.url).pathname)).toBe(`/api/v1/items/${id}/purchases`)
    }
    // 1,364 encoded astral characters plus nine ASCII characters fill 16 KiB
    // exactly, including cursor=. No custom serializer or production override.
    const cursor = '😀'.repeat(1364) + 'x'.repeat(9)
    await api.fetchPurchasesPage({ cursor }, signal())
    expect(new URL(capturedRequest().url).search.slice(1)).toHaveLength(16_384)
    await expect(api.fetchPurchasesPage({ cursor: cursor + 'x' }, signal())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    await api.fetchItemPurchasesPage(item.id, { cursor }, signal())
    await api.fetchPurchaseLinesPage(purchase.id, { cursor }, signal())
    for (const invoke of [() => api.fetchItemPurchasesPage(item.id, { cursor: cursor + 'x' }, signal()), () => api.fetchPurchaseLinesPage(purchase.id, { cursor: cursor + 'x' }, signal())]) await expect(invoke()).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    expect(fetchMock).toHaveBeenCalledTimes(10)
  })

  it('snapshots both whole original interpretations, body, key and proofs before an in-flight result', async () => {
    const api = await import('./purchases')
    let complete: ((response: Response) => void) | undefined
    fetchMock.mockImplementation(() => new Promise((resolve) => { complete = resolve }))
    const original = lineContext(), body = { ...allocation, quantity_base: 1500, paid_minor: 0 }
    const pending = api.createPurchaseLine(purchase.id, body, original)
    original.purchase.id = 'changed-parent'; original.purchase.revision = 99; original.purchase.currency = 'USD'
    original.item.id = 'changed-item'; original.item.revision = 99; original.item.base_unit = 'ml'; original.item.scale_factor = 1
    original.idempotencyKey = 'changed-key'; body.quantity_base = 1; body.paid_minor = 999
    complete?.(reply(line, '"1"'))
    expect(await pending).toEqual(line)
    const request = fetchMock.mock.calls[0]?.[0] as Request
    expect(await request.json()).toEqual(allocation)
    expect(request.headers.get('If-Match')).toBe('"12"'); expect(request.headers.get('Idempotency-Key')).toBe('original-purchase-key')
  })

  it('uses no credential, replica, cache or draft storage and never queues a financial write', async () => {
    const spies = [vi.spyOn(localStorage, 'getItem'), vi.spyOn(localStorage, 'setItem'), vi.spyOn(sessionStorage, 'getItem'), vi.spyOn(sessionStorage, 'setItem'), vi.spyOn(document, 'cookie', 'get'), vi.spyOn(document, 'cookie', 'set')]
    const database = vi.fn(), cache = vi.fn()
    vi.stubGlobal('indexedDB', { open: database }); vi.stubGlobal('caches', { open: cache })
    const api = await import('./purchases')
    fetchMock.mockImplementation(async () => reply(line, '"1"'))
    await api.createPurchaseLine(purchase.id, allocation, lineContext())
    window.dispatchEvent(new Event('online'))
    for (const spy of [...spies, database, cache]) expect(spy).not.toHaveBeenCalled()
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })
})

describe('exact purchase input and original source boundaries', () => {
  it.each([
    {}, { ...header, merchant_name: '' }, { ...header, merchant_name: null }, { ...header, merchant_name: '😀'.repeat(301) },
    { ...header, merchant_name: '\uD800' }, { ...header, currency: 'jpy' }, { ...header, currency: 'JPY\n' }, { ...header, currency: null },
    { ...header, receipt_note: null }, { ...header, receipt_note: '\uD800' }, { ...header, receipt_note: '\0' + '界'.repeat(10000) },
    { ...header, paid_minor: 120000 }, { ...header, quantity_base: 1 }, { ...header, item_id: item.id }, { ...header, revision: 1 },
  ])('rejects invalid header input %# before sending', async (body) => {
    const api = await import('./purchases')
    await expect(api.createPurchase(body as never, context())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it.each([
    '2026-11-06', '0000-12-31T23:59:59-00:01', '10000-01-01T00:00:00Z', '1900-02-29T00:00:00Z', '2026-02-30T00:00:00Z',
    '2026-11-06t00:00:00z', '2026-11-06T0:00:00Z', '2026-11-06T00:00:60Z', '2026-11-06T24:00:00Z',
    '2026-11-06T00:00:00,1Z', '2026-11-06T00:00:00.1234567890Z', '2026-11-06T00:00:00+24:00', '2026-11-06T00:00:00+00:60',
    '2026-11-06T00:00:00+0800', '2026-11-06T00:00:00', '2026-11-06T00:00:00Z\n',
    '0001-01-01T00:00:00.000000001+00:01', '9999-12-31T23:59:59.999999999-00:01',
  ])('rejects an invalid, lossy or UTC-overflow instant %s', async (purchased_at) => {
    const api = await import('./purchases')
    await expect(api.createPurchase({ ...header, purchased_at }, context())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    await expect(api.patchPurchase(purchase.id, { purchased_at }, purchaseContext())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it.each([
    ['0001-01-01T00:01:00+00:01', '0001-01-01T00:00:00.000000000Z'],
    ['9999-12-31T23:58:59.123456789-00:01', '9999-12-31T23:59:59.123456789Z'],
    ['2000-02-29T00:00:00.000000001Z', '2000-02-29T00:00:00.000000001Z'],
    ['1969-12-31T23:59:59.999999999Z', '1969-12-31T23:59:59.999999999Z'],
    ['2026-11-06T12:00:00.123456789+08:00', '2026-11-06T04:00:00.123456789Z'],
    ['2026-11-06T00:00:00-00:00', '2026-11-06T00:00:00.000000000Z'],
    ['2026-11-06T00:00:00+23:59', '2026-11-05T00:01:00.000000000Z'],
  ])('preserves every fractional digit while checking canonical %s', async (purchased_at, canonical) => {
    const api = await import('./purchases')
    fetchMock.mockResolvedValueOnce(reply({ ...created, purchased_at: canonical }, '"1"'))
    expect((await api.createPurchase({ ...header, purchased_at }, context())).purchased_at).toBe(canonical)
    expect((await capturedRequest(0).json()).purchased_at).toBe(purchased_at)
  })

  it.each([{}, { receipt_note: undefined }, { receipt_note: null }, { currency: null }, { merchant_name: '' }, { receipt_note: '\uD800' }, { paid_minor: 1 }, { item_unit_id: null }])('requires an actual nonempty partial correction %#', async (body) => {
    const api = await import('./purchases')
    await expect(api.patchPurchase(purchase.id, body as never, purchaseContext())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('preserves maximum exact Unicode/control text, supplied empty notes and absence without defaults', async () => {
    const api = await import('./purchases')
    const merchant_name = '😀'.repeat(300), receipt_note = '\0' + '界'.repeat(9999)
    fetchMock.mockResolvedValueOnce(reply({ ...created, merchant_name, receipt_note }, '"1"'))
    await api.createPurchase({ ...header, merchant_name, receipt_note }, context())
    expect(await capturedRequest(0).json()).toEqual({ ...header, merchant_name, receipt_note })
    const note = '\0' + '😀'.repeat(9999)
    fetchMock.mockResolvedValueOnce(reply({ ...line, note, quantity_base: Number.MAX_SAFE_INTEGER, paid_minor: Number.MAX_SAFE_INTEGER }, '"1"'))
    await api.createPurchaseLine(purchase.id, { ...allocation, quantity_base: Number.MAX_SAFE_INTEGER, paid_minor: Number.MAX_SAFE_INTEGER, note }, lineContext())
    expect((await capturedRequest().json()).note).toBe(note)
    fetchMock.mockResolvedValueOnce(reply({ ...purchase, revision: 13, merchant_name: 'Changed' }, '"13"'))
    await api.patchPurchase(purchase.id, { merchant_name: 'Changed', receipt_note: undefined }, purchaseContext())
    expect(await capturedRequest().json()).toEqual({ merchant_name: 'Changed' })
  })

  it('requires original whole Purchase and Item interpretations and rejects every loose/changed proof', async () => {
    const api = await import('./purchases')
    for (const value of [undefined, 0, -1, 1.5, Number.MAX_SAFE_INTEGER + 1]) {
      await expect(api.patchPurchase(purchase.id, { receipt_note: '' }, { ...purchaseContext(), purchase: { ...purchase, revision: value } } as never)).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
      await expect(api.createPurchaseLine(purchase.id, allocation, { ...lineContext(), item: { ...item, revision: value } } as never)).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    }
    for (const value of [{ ...context(), expectedRevision: 12 }, { ...purchaseContext(), expectedRevision: 12 }, { ...purchaseContext(), purchase: { ...purchase, id: 'other' } }, { ...purchaseContext(), purchase: { ...purchase, currency: 'jpy' } }]) {
      await expect(api.patchPurchase(purchase.id, { receipt_note: '' }, value as never)).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    }
    for (const value of [{ ...purchaseContext() }, { ...lineContext(), expectedRevision: 12 }, { ...lineContext(), item: { ...item, id: 'other' } }, { ...lineContext(), item: { ...item, revision: 8 } }, { ...lineContext(), item: { ...item, base_unit: 'g\n' } }, { ...lineContext(), item: { ...item, scale_factor: 0 } }]) await expect(api.createPurchaseLine(purchase.id, allocation, value as never)).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    await expect(api.createPurchase(header, { ...context(), expectedRevision: 12 } as never)).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    for (const id of ['', '.', '..', 'x/y', 'x\\y', '\0', '\uD800', '😀'.repeat(129)]) {
      await expect(api.fetchPurchase(id, signal())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
      await expect(api.fetchItemPurchasesPage(id, {}, signal())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
      await expect(api.fetchPurchaseLinesPage(id, {}, signal())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
      await expect(api.patchPurchase(id, { receipt_note: '' }, purchaseContext())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
      await expect(api.createPurchaseLine(id, allocation, lineContext())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    }
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('requires explicit nullable allocation, independent safe numbers and known tracking without conversion', async () => {
    const api = await import('./purchases')
    for (const body of [{}, { ...allocation, item_revision: undefined }, { ...allocation, item_unit_id: undefined }, { ...allocation, item_unit_id: '' },
      { ...allocation, item_unit_id: 'unit', quantity_base: 2 }, { ...allocation, item_id: 'other' }, { ...allocation, note: null },
      { ...allocation, note: '\0' + '界'.repeat(10000) }, { ...allocation, note: '\uD800' }, { ...allocation, purchase_id: 'other' },
      { ...allocation, currency: 'USD' }, { ...allocation, base_unit: 'g' }, { ...allocation, item_revision: 8 }]) {
      await expect(api.createPurchaseLine(purchase.id, body as never, lineContext())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    }
    for (const field of ['item_revision', 'quantity_base', 'paid_minor']) {
      for (const value of [-1, 0.5, Number.MAX_SAFE_INTEGER + 1, Infinity, NaN, '1000', true, null]) {
        await expect(api.createPurchaseLine(purchase.id, { ...allocation, [field]: value } as never, lineContext())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
      }
    }
    for (const field of ['item_revision', 'quantity_base']) await expect(api.createPurchaseLine(purchase.id, { ...allocation, [field]: 0 }, lineContext())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    await expect(api.createPurchaseLine(purchase.id, { ...allocation, item_unit_id: 'unit', quantity_base: 1 }, lineContext())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    expect(fetchMock).not.toHaveBeenCalled()
    const individual = { ...item, tracking_mode: 'individual' as const, unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1 }
    fetchMock.mockResolvedValueOnce(reply({ ...line, item_unit_id: 'unit', quantity_base: 1, paid_minor: 120000 }, '"1"'))
    expect((await api.createPurchaseLine(purchase.id, { ...allocation, item_unit_id: 'unit', quantity_base: 1, paid_minor: 120000 }, { ...lineContext(), item: individual })).paid_minor).toBe(120000)
    fetchMock.mockResolvedValueOnce(reply(line, '"1"'))
    expect((await api.createPurchaseLine(purchase.id, allocation, { ...lineContext(), item: individual })).quantity_base).toBe(1500)
  })

  it('rejects unrelated/unsafe query fields while preserving the complete owned scopes', async () => {
    const api = await import('./purchases')
    for (const query of [{ limit: 0 }, { limit: 101 }, { limit: '50' }, { cursor: '' }, { cursor: '\uD800' }, { cursor: 'x'.repeat(2049) }, { unknown: true }]) {
      await expect(api.fetchPurchasesPage(query as never, signal())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
      await expect(api.fetchItemPurchasesPage(item.id, query as never, signal())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
      await expect(api.fetchPurchaseLinesPage(purchase.id, query as never, signal())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    }
    for (const query of [{ from: '0000-01-01' }, { from: '2026-02-30' }, { from: '2026-11-06', to: '2026-11-05' }, { currency: 'jpy' }, { currency: 'JPY\n' }, { status: 'disposed' }, { scope: 'other' }, { merchant: '' }, { merchant: '\uD800' }, { category_id: null }, { location_id: '😀'.repeat(129) }, { item_id: item.id }, { q: '' }, { q: undefined }]) await expect(api.fetchPurchasesPage(query as never, signal())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    for (const query of [{ currency: 'JPY' }, { merchant: 'M' }, { status: 'active' }, { from: '2026-11-06' }]) {
      await expect(api.fetchItemPurchasesPage(item.id, query as never, signal())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
      await expect(api.fetchPurchaseLinesPage(purchase.id, query as never, signal())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    }
    expect(fetchMock).not.toHaveBeenCalled()
  })
})

describe('complete independent purchase replies', () => {
  it.each([null, [], {}, { ...purchase, id: 'other' }, { ...purchase, revision: 0 }, { ...purchase, revision: Number.MAX_SAFE_INTEGER + 1 },
    { ...purchase, merchant_name: '' }, { ...purchase, currency: 'jpy' }, { ...purchase, receipt_note: undefined }, { ...purchase, receipt_note: null },
    { ...purchase, receipt_note: '\uD800' }, { ...purchase, purchased_at: '2026-02-30T00:00:00Z' }, { ...purchase, purchased_at: '9999-12-31T23:59:59-00:01' },
    { ...purchase, paid_minor: 120000 }, { ...purchase, raw_ocr: 'Unreviewed' }])('rejects malformed/unrelated header reply %#', async (value) => {
    fetchMock.mockResolvedValue(reply(value, '"12"'))
    const api = await import('./purchases')
    await expect(api.fetchPurchase(purchase.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  })

  it('requires strong own ETags, creation revision one and exact promised values rather than silent date/money repair', async () => {
    const api = await import('./purchases')
    for (const etag of [undefined, '', '12', 'W/"12"', '"13"', '"12", "12"', '*']) {
      fetchMock.mockResolvedValueOnce(reply(purchase, etag))
      await expect(api.fetchPurchase(purchase.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
    for (const value of [{ ...created, revision: 2 }, { ...created, merchant_name: 'Different' }, { ...created, receipt_note: 'Inserted' }, { ...created, currency: 'USD' }, { ...created, purchased_at: '2026-11-06T04:00:00.123456788Z' }, { ...created, purchased_at: header.purchased_at }]) {
      fetchMock.mockResolvedValueOnce(reply(value, `"${value.revision}"`))
      await expect(api.createPurchase(header, context())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
    for (const value of [{ ...line, revision: 2 }, { ...line, purchase_id: 'other' }, { ...line, item_id: 'other' }, { ...line, item_unit_id: 'unit' }, { ...line, quantity_base: 1 }, { ...line, paid_minor: 1 }, { ...line, note: 'Inserted' }]) {
      fetchMock.mockResolvedValueOnce(reply(value, `"${value.revision}"`))
      await expect(api.createPurchaseLine(purchase.id, allocation, lineContext())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
    for (const etag of [undefined, 'W/"1"', '"12"', '"13"']) {
      fetchMock.mockResolvedValueOnce(reply(line, etag))
      await expect(api.createPurchaseLine(purchase.id, allocation, lineContext())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
  })

  it('retains same-instant spelling and omitted source but accepts canonical actual changes and exact no-op replies', async () => {
    const api = await import('./purchases')
    fetchMock.mockResolvedValueOnce(reply(purchase, '"12"'))
    expect(await api.patchPurchase(purchase.id, { purchased_at: '2026-11-06T04:00:00.123456789Z' }, purchaseContext())).toEqual(purchase)
    const changed = { ...purchase, revision: 13, purchased_at: '2026-11-06T04:00:00.123456788Z' }
    fetchMock.mockResolvedValueOnce(reply(changed, '"13"'))
    expect(await api.patchPurchase(purchase.id, { purchased_at: '2026-11-06T12:00:00.123456788+08:00' }, purchaseContext())).toEqual(changed)
    for (const value of [{ ...purchase, revision: 11 }, { ...purchase, revision: 14 }, { ...purchase, merchant_name: 'Not requested' }, { ...purchase, purchased_at: created.purchased_at }, { ...purchase, currency: 'USD' }, { ...purchase, receipt_note: '' }]) {
      fetchMock.mockResolvedValueOnce(reply(value, `"${value.revision}"`))
      await expect(api.patchPurchase(purchase.id, { merchant_name: purchase.merchant_name }, purchaseContext())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
    const retained = { ...purchase, revision: Number.MAX_SAFE_INTEGER, currency: 'ZZZ' }
    fetchMock.mockResolvedValueOnce(reply(retained, `"${retained.revision}"`))
    expect(await api.patchPurchase(purchase.id, { receipt_note: retained.receipt_note }, { ...context(), purchase: retained })).toEqual(retained)
  })

  it('requires complete pages, independent ownership and retained representation without inventing new-write eligibility', async () => {
    const api = await import('./purchases')
    const invalidPages = [null, {}, { data: [] }, { data: [], next_cursor: '' }, { data: [], next_cursor: 'x'.repeat(2049) }, { data: [], next_cursor: '\uD800' },
      { data: [], next_cursor: 'next' }, { data: [purchase], next_cursor: 'next' }, { data: [purchase, purchase], next_cursor: null },
      { data: [purchase], next_cursor: null, watermark: 7 }, { data: Array.from({ length: 101 }, (_, index) => ({ ...purchase, id: String(index) })), next_cursor: null }]
    for (const page of invalidPages) {
      fetchMock.mockResolvedValueOnce(reply(page))
      await expect(api.fetchPurchasesPage({}, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
      fetchMock.mockResolvedValueOnce(reply(page))
      await expect(api.fetchItemPurchasesPage(item.id, {}, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
    for (const page of [{ data: [line], next_cursor: 'next' }, { data: [line, line], next_cursor: null }, { data: [{ ...line, purchase_id: 'other' }], next_cursor: null },
      { data: [{ ...line, item_unit_id: undefined }], next_cursor: null }, { data: [{ ...line, quantity_base: 0 }], next_cursor: null },
      { data: [{ ...line, paid_minor: -1 }], next_cursor: null }, { data: [{ ...line, revision: Number.MAX_SAFE_INTEGER + 1 }], next_cursor: null },
      { data: [{ ...line, raw_ocr: 'Private' }], next_cursor: null }]) {
      fetchMock.mockResolvedValueOnce(reply(page))
      await expect(api.fetchPurchaseLinesPage(purchase.id, {}, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
    for (const [read, data] of [[() => api.fetchPurchasesPage({}, signal()), purchase], [() => api.fetchItemPurchasesPage(item.id, {}, signal()), purchase], [() => api.fetchPurchaseLinesPage(purchase.id, {}, signal()), line]] as const) {
      fetchMock.mockResolvedValueOnce(reply({ data: [data], next_cursor: null }, '"1"'))
      await expect(read()).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
    for (const [read, data] of [[() => api.fetchPurchasesPage({ limit: 1, cursor: 'same' }, signal()), purchase], [() => api.fetchItemPurchasesPage(item.id, { limit: 1, cursor: 'same' }, signal()), purchase], [() => api.fetchPurchaseLinesPage(purchase.id, { limit: 1, cursor: 'same' }, signal()), line]] as const) {
      fetchMock.mockResolvedValueOnce(reply({ data: [data], next_cursor: 'same' }))
      await expect(read()).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
      fetchMock.mockResolvedValueOnce(reply({ data: [data], next_cursor: 'next' }))
      expect(await read()).toEqual({ data: [data], next_cursor: 'next' })
    }
    for (const query of [{ currency: 'USD' }, { merchant: 'Different' }]) {
      fetchMock.mockResolvedValueOnce(reply({ data: [purchase], next_cursor: null }))
      await expect(api.fetchPurchasesPage(query, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
    const retained = { ...line, item_unit_id: 'legacy-unit', quantity_base: 2, paid_minor: 0, revision: Number.MAX_SAFE_INTEGER }
    fetchMock.mockResolvedValueOnce(reply({ data: [retained], next_cursor: null }))
    expect(await api.fetchPurchaseLinesPage(purchase.id, {}, signal())).toEqual({ data: [retained], next_cursor: null })
    fetchMock.mockResolvedValueOnce(reply({ data: [{ ...purchase, currency: 'ZZZ' }], next_cursor: null }))
    expect((await api.fetchPurchasesPage({ currency: 'ZZZ' }, signal())).data[0]?.currency).toBe('ZZZ')
    const page = { data: Array.from({ length: 50 }, (_, index) => ({ ...purchase, id: String(index) })), next_cursor: 'default-next' }
    fetchMock.mockResolvedValueOnce(reply(page))
    expect(await api.fetchPurchasesPage({}, signal())).toEqual(page)
  })

  it('keeps financial conflicts safe, preserves caller metadata and reconciles only an explicit identical request', async () => {
    const api = await import('./purchases')
    for (const code of ['REVISION_CONFLICT', 'IDEMPOTENCY_CONFLICT', 'IDEMPOTENCY_EXPIRED', 'PURCHASE_CURRENCY_LOCKED', 'PURCHASE_CURRENCY_CONFLICT', 'ASSET_COST_BASIS_CONFLICT', 'ASSET_LIFECYCLE_REQUIRED', 'ASSET_TOTAL_OVERFLOW', 'TRACKING_MODE_CONFLICT']) {
      fetchMock.mockResolvedValueOnce(reply({ error: { code, request_id: 'safe-request', message: 'private amount/source', details: { path: '/private/homebox.sqlite' } } }, undefined, 409))
      const error = await api.createPurchaseLine(purchase.id, allocation, lineContext()).catch((error: unknown) => error)
      expect(error).toMatchObject({ code, status: 409, requestId: 'safe-request' })
      expect(JSON.stringify(error)).not.toContain('private'); expect(error).not.toHaveProperty('details'); expect(error).not.toHaveProperty('cause')
    }
    fetchMock.mockReset()
    fetchMock.mockResolvedValueOnce(reply({ ...line, paid_minor: 1500 }, '"1"'))
    fetchMock.mockResolvedValueOnce(reply(line, '"1"'))
    const original = lineContext()
    await expect(api.createPurchaseLine(purchase.id, allocation, original)).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    window.dispatchEvent(new Event('online'))
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(await api.createPurchaseLine(purchase.id, allocation, original)).toEqual(line)
    const [first, second] = fetchMock.mock.calls.map((call) => call[0] as Request)
    expect(second?.headers.get('If-Match')).toBe(first?.headers.get('If-Match'))
    expect(second?.headers.get('Idempotency-Key')).toBe(first?.headers.get('Idempotency-Key'))
    expect(await second?.json()).toEqual(await first?.json())
  })

  it('honors pre-aborted and live caller cancellation without issuing another request', async () => {
    const api = await import('./purchases')
    const aborted = new AbortController(); aborted.abort()
    await expect(api.fetchPurchasesPage({}, aborted.signal)).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
    await expect(api.createPurchase(header, { ...context(), signal: aborted.signal })).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
    await expect(api.patchPurchase(purchase.id, { receipt_note: '' }, { ...purchaseContext(), signal: aborted.signal })).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
    await expect(api.createPurchaseLine(purchase.id, allocation, { ...lineContext(), signal: aborted.signal })).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
    expect(fetchMock).not.toHaveBeenCalled()
    fetchMock.mockImplementation((request) => new Promise((_resolve, reject) => { (request as Request).signal.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')), { once: true }) }))
    const controller = new AbortController()
    const pending = expect(api.createPurchaseLine(purchase.id, allocation, { ...lineContext(), signal: controller.signal })).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
    controller.abort(); await pending
    window.dispatchEvent(new Event('online')); expect(fetchMock).toHaveBeenCalledTimes(1)
  })
})
