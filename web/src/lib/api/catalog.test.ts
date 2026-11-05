import { beforeEach, describe, expect, it, vi } from 'vitest'
import { version } from '../../../package.json'

type CatalogAPI = typeof import('./catalog')
const fetchMock = vi.fn<typeof fetch>()
const item = { id: 'item-1', revision: 1, name: '  物品 😀 é  ', brand: null, model: '', category_id: null, description: '', notes: '',
  tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1, status: 'active' } as const
const ack = { ok: true, operation_id: 'event-1', revision: 2 }
const csrf = 'A'.repeat(43)
const signal = () => new AbortController().signal
const write = () => ({ csrfToken: csrf, idempotencyKey: 'explicit-catalog-key', signal: signal() })
const proof = () => ({ ...write(), expectedRevision: 1 })

function reply(data: unknown, etag?: string, status = 200) {
  const revision = data && typeof data === 'object' && 'revision' in data ? data.revision : undefined
  return Response.json(data, { status, headers: etag === undefined && typeof revision !== 'number' ? {} : { ETag: etag ?? `"${String(revision)}"` } })
}

beforeEach(() => {
  vi.resetModules()
  fetchMock.mockReset()
  vi.stubGlobal('fetch', fetchMock)
})

describe('typed stateless catalog transport', () => {
  const cases: { name: string; method: string; path: string; body?: unknown; result: unknown; revision?: number; invoke: (api: CatalogAPI) => Promise<unknown> }[] = [
    { name: 'complete filtered page', method: 'GET', path: '/items?from=2026-01-01&to=2026-12-31&currency=TWD&category_id=category&location_id=location&scope=direct&status=archived&merchant=Exact&q=Query&limit=2&cursor=opaque', result: { data: [{ ...item, category_id: 'category', status: 'archived' }], next_cursor: null },
      invoke: (api) => api.fetchItemsPage({ from: '2026-01-01', to: '2026-12-31', currency: 'TWD', category_id: 'category', location_id: 'location', scope: 'direct', status: 'archived', merchant: 'Exact', q: 'Query', limit: 2, cursor: 'opaque' }, signal()) },
    { name: 'current item', method: 'GET', path: '/items/item-1', result: item, invoke: (api) => api.fetchItem(item.id, signal()) },
    { name: 'minimal creation', method: 'POST', path: '/items', body: { name: item.name }, result: item, invoke: (api) => api.createItem({ name: item.name }, write()) },
    { name: 'explicit default subset', method: 'POST', path: '/items', body: { name: item.name, unit_code: 'pcs' }, result: item, invoke: (api) => api.createItem({ name: item.name, unit_code: 'pcs' }, write()) },
    { name: 'explicit custom tuple', method: 'POST', path: '/items', body: { name: item.name, tracking_mode: 'quantity', unit_code: 'kg', base_unit: 'g', scale_factor: 1000, category_id: 'category', brand: null }, result: { ...item, tracking_mode: 'quantity', unit_code: 'kg', base_unit: 'g', scale_factor: 1000, category_id: 'category' },
      invoke: (api) => api.createItem({ name: item.name, tracking_mode: 'quantity', unit_code: 'kg', base_unit: 'g', scale_factor: 1000, category_id: 'category', brand: null }, write()) },
    { name: 'partial rename', method: 'PATCH', path: '/items/item-1', body: { name: 'Renamed' }, result: { ...item, name: 'Renamed', revision: 2 }, revision: 1, invoke: (api) => api.patchItem(item.id, { name: 'Renamed' }, proof()) },
    { name: 'exact null and empty partial fields', method: 'PATCH', path: '/items/item-1', body: { brand: null, model: '', category_id: null, notes: '' }, result: item, revision: 1,
      invoke: (api) => api.patchItem(item.id, { brand: null, model: '', category_id: null, notes: '' }, proof()) },
    { name: 'archival', method: 'POST', path: '/items/item-1/archive', body: { reason: '  保留歷史 😀  ' }, result: { ...item, status: 'archived', revision: 2 }, revision: 1,
      invoke: (api) => api.archiveItem(item.id, { reason: '  保留歷史 😀  ' }, proof()) },
    { name: 'empty archival object', method: 'POST', path: '/items/item-1/archive', body: {}, result: { ...item, status: 'archived' }, revision: 1, invoke: (api) => api.archiveItem(item.id, {}, proof()) },
    { name: 'explicit metadata restoration', method: 'PATCH', path: '/items/item-1', body: { status: 'active' }, result: item, revision: 1, invoke: (api) => api.patchItem(item.id, { status: 'active' }, proof()) },
    { name: 'guarded deletion', method: 'DELETE', path: '/items/item-1', result: ack, revision: 1, invoke: (api) => api.deleteItem(item.id, proof()) },
  ]
  it.each(cases)('sends one $name request with caller-owned original context', async (test) => {
    fetchMock.mockResolvedValue(reply(test.result))
    const api = await import('./catalog')
    expect(await test.invoke(api)).toEqual(test.result)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const request = fetchMock.mock.calls[0]?.[0] as Request
    const actual = new URL(request.url)
    const expected = new URL(`http://localhost/api/v1${test.path}`)
    actual.searchParams.sort()
    expected.searchParams.sort()
    expect(actual.href).toBe(expected.href)
    expect(request.method).toBe(test.method)
    expect(request.cache).toBe('no-store')
    expect(request.credentials).toBe('same-origin')
    expect(request.redirect).toBe('error')
    expect(request.headers.has('Cookie')).toBe(false)
    expect(request.headers.has('Authorization')).toBe(false)
    expect(request.headers.get('Origin')).toBe(test.method === 'GET' ? null : 'http://localhost')
    expect(request.headers.get('X-CSRF-Token')).toBe(test.method === 'GET' ? null : csrf)
    expect(request.headers.get('Idempotency-Key')).toBe(test.method === 'GET' ? null : 'explicit-catalog-key')
    expect(request.headers.get('X-HomeBox-Client-Version')).toBe(test.method === 'GET' ? null : version)
    expect(request.headers.get('If-Match')).toBe(test.revision === undefined ? null : `"${test.revision}"`)
    if (test.body === undefined) {
      expect(await request.text()).toBe('')
      expect(request.headers.has('Content-Type')).toBe(false)
    } else expect(await request.json()).toEqual(test.body)
  })

  it('encodes opaque cursors and literal filters without interpreting or following them', async () => {
    const { fetchItemsPage } = await import('./catalog')
    fetchMock.mockResolvedValue(reply({ data: [item], next_cursor: 'next' }))
    expect((await fetchItemsPage({ limit: 1, cursor: 'opaque?x=1&limit=99+#', merchant: ' Exact & + #? ', q: '  濾芯 & + #?  ' }, signal())).next_cursor).toBe('next')
    const request = fetchMock.mock.calls[0]?.[0] as Request
    const url = new URL(request.url)
    expect(url.searchParams.get('cursor')).toBe('opaque?x=1&limit=99+#')
    expect(url.searchParams.get('merchant')).toBe(' Exact & + #? ')
    expect(url.searchParams.get('q')).toBe('  濾芯 & + #?  ')
    expect(url.searchParams.getAll('limit')).toEqual(['1'])
    expect(url.searchParams.has('x')).toBe(false)
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('snapshots original fields, key and deletion proof rather than rebasing on explicit retries', async () => {
    const { patchItem, deleteItem } = await import('./catalog')
    const body = { name: '  Original é  ' }
    const context = proof()
    fetchMock.mockImplementation(async () => reply({ ...item, ...body, revision: 2 }))
    await patchItem(item.id, body, context)
    await patchItem(item.id, body, context)
    const first = fetchMock.mock.calls[0]?.[0] as Request
    const second = fetchMock.mock.calls[1]?.[0] as Request
    expect(await second.text()).toBe(await first.text())
    expect(second.headers.get('If-Match')).toBe('"1"')
    expect(second.headers.get('Idempotency-Key')).toBe(first.headers.get('Idempotency-Key'))
    let release: ((response: Response) => void) | undefined
    fetchMock.mockImplementation(() => new Promise((resolve) => { release = resolve }))
    const pending = deleteItem(item.id, context)
    context.expectedRevision = 99
    context.idempotencyKey = 'changed-after-sending'
    release?.(reply(ack))
    expect(await pending).toEqual(ack)
    const deletion = fetchMock.mock.calls[2]?.[0] as Request
    expect(deletion.headers.get('If-Match')).toBe('"1"')
    expect(deletion.headers.get('Idempotency-Key')).toBe('explicit-catalog-key')
  })

  it('never accesses storage, cookies, an offline replica or background mutation queues', async () => {
    const spies = [vi.spyOn(window.localStorage, 'getItem'), vi.spyOn(window.localStorage, 'setItem'),
      vi.spyOn(window.sessionStorage, 'getItem'), vi.spyOn(window.sessionStorage, 'setItem'), vi.spyOn(document, 'cookie', 'get'), vi.spyOn(document, 'cookie', 'set')]
    const database = vi.fn()
    const cache = vi.fn()
    vi.stubGlobal('indexedDB', { open: database })
    vi.stubGlobal('caches', { open: cache })
    const { createItem } = await import('./catalog')
    fetchMock.mockResolvedValue(reply(item))
    await createItem({ name: item.name }, write())
    window.dispatchEvent(new Event('offline'))
    window.dispatchEvent(new Event('online'))
    for (const spy of [...spies, database, cache]) expect(spy).not.toHaveBeenCalled()
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })
})

describe('strict catalog resource and page responses', () => {
  it.each([
    null, {}, [], { ...item, id: '' }, { ...item, id: '😀'.repeat(129) }, { ...item, id: '\uD800' },
    { ...item, revision: 0 }, { ...item, revision: 1.1 }, { ...item, revision: Number.MAX_SAFE_INTEGER + 1 },
    { ...item, name: '' }, { ...item, name: '\uD800' }, { ...item, name: '😀'.repeat(301) },
    { ...item, brand: 'x'.repeat(301) }, { ...item, model: '\uD800' }, { ...item, model: false },
    { ...item, category_id: '' }, { ...item, category_id: '😀'.repeat(129) }, { ...item, category_id: '\uD800' },
    { ...item, description: null }, { ...item, description: 'x'.repeat(10001) }, { ...item, notes: '\uD800' },
    { ...item, tracking_mode: 'mixed' }, { ...item, unit_code: '' }, { ...item, unit_code: '😀'.repeat(33) },
    { ...item, base_unit: '1g' }, { ...item, base_unit: 'g\n' }, { ...item, base_unit: '克' },
    { ...item, scale_factor: 0 }, { ...item, scale_factor: 0.5 }, { ...item, scale_factor: Number.MAX_SAFE_INTEGER + 1 },
    { ...item, status: 'stored' }, { ...item, raw_ocr: 'private' }, { ...item, session_token: 'private' },
  ])('rejects malformed/private resource %j', async (data) => {
    fetchMock.mockResolvedValue(reply(data))
    const { fetchItem } = await import('./catalog')
    await expect(fetchItem(item.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  })

  it.each(Object.keys(item))('requires the declared response field %s even when it is nullable', async (field) => {
    fetchMock.mockResolvedValue(reply({ ...item, [field]: undefined }))
    const { fetchItem } = await import('./catalog')
    await expect(fetchItem(item.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  })

  it('requires exact identity/strong ETag and original noop-or-next committed revisions', async () => {
    const { fetchItem, patchItem, archiveItem, createItem } = await import('./catalog')
    for (const etag of ['', '1', 'W/"1"', '"2"', '"1", "1"', '*']) {
      fetchMock.mockResolvedValueOnce(reply(item, etag))
      await expect(fetchItem(item.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
    for (const data of [{ ...item, id: 'another' }, { ...item, revision: 3 }]) {
      fetchMock.mockResolvedValueOnce(reply(data))
      await expect(patchItem(item.id, { name: item.name }, proof())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
    fetchMock.mockResolvedValueOnce(Response.json(item))
    await expect(fetchItem(item.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    fetchMock.mockResolvedValueOnce(reply({ ...item, revision: 2 }))
    await expect(createItem({ name: item.name }, write())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    for (const revision of [1, 2]) {
      fetchMock.mockResolvedValueOnce(reply({ ...item, revision }))
      expect((await patchItem(item.id, { name: item.name }, proof())).revision).toBe(revision)
      fetchMock.mockResolvedValueOnce(reply({ ...item, status: 'archived', revision }))
      expect((await archiveItem(item.id, {}, proof())).revision).toBe(revision)
    }
  })

  it('validates required nullable cursors, unique IDs, requested bounds and known direct filter semantics', async () => {
    const { fetchItemsPage } = await import('./catalog')
    for (const data of [null, {}, { data: [], next_cursor: undefined }, { data: null, next_cursor: null }, { data: [item, item], next_cursor: null },
      { data: [item], next_cursor: '' }, { data: [item], next_cursor: '\uD800' }, { data: [item], next_cursor: 'x'.repeat(2049) },
      { data: [], next_cursor: 'next' }, { data: [item], next_cursor: 'same' }, { data: [item, { ...item, id: 'second' }], next_cursor: null },
      { data: [], next_cursor: null, private: true }, { data: Array.from({ length: 101 }, (_, index) => ({ ...item, id: `item-${index}` })), next_cursor: null }]) {
      fetchMock.mockResolvedValueOnce(reply(data))
      await expect(fetchItemsPage({ limit: 1, cursor: 'same' }, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
    fetchMock.mockResolvedValueOnce(reply({ data: [{ ...item, status: 'archived' }], next_cursor: null }))
    await expect(fetchItemsPage({ status: 'active' }, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    fetchMock.mockResolvedValueOnce(reply({ data: [item], next_cursor: null }))
    await expect(fetchItemsPage({ category_id: 'category', scope: 'direct' }, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    fetchMock.mockResolvedValueOnce(reply({ data: [item], next_cursor: null }))
    expect(await fetchItemsPage({ category_id: 'category' }, signal())).toEqual({ data: [item], next_cursor: null })
    fetchMock.mockResolvedValueOnce(reply({ data: [], next_cursor: null }))
    expect(await fetchItemsPage({}, signal())).toEqual({ data: [], next_cursor: null })
    fetchMock.mockResolvedValueOnce(reply({ data: Array.from({ length: 51 }, (_, index) => ({ ...item, id: `item-${index}` })), next_cursor: null }))
    await expect(fetchItemsPage({}, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  })

  it('accepts exact Unicode/nullable/measurement bounds without imposing an individual-pcs policy', async () => {
    const { createItem, patchItem } = await import('./catalog')
    const resource = { ...item, id: '😀'.repeat(128), name: '😀'.repeat(300), brand: '😀'.repeat(300), model: null,
      description: '😀'.repeat(10000), notes: '😀'.repeat(10000), unit_code: '😀'.repeat(32), base_unit: 'g' + '_'.repeat(31), scale_factor: Number.MAX_SAFE_INTEGER }
    fetchMock.mockResolvedValueOnce(reply(resource))
    expect(await createItem({ name: resource.name, unit_code: resource.unit_code, base_unit: resource.base_unit, scale_factor: resource.scale_factor }, write())).toEqual(resource)
    fetchMock.mockResolvedValueOnce(reply({ ...resource, revision: Number.MAX_SAFE_INTEGER }))
    expect((await patchItem(resource.id, { name: resource.name }, { ...write(), expectedRevision: Number.MAX_SAFE_INTEGER })).revision).toBe(Number.MAX_SAFE_INTEGER)
  })

  it('requires a strict deletion acknowledgement at the original proof plus one', async () => {
    const { deleteItem } = await import('./catalog')
    for (const data of [{ ...ack, ok: false }, { ok: true, operation_id: 'event' }, { ...ack, operation_id: '' }, { ...ack, operation_id: '\uD800' },
      { ...ack, revision: 1 }, { ...ack, revision: 3 }, { ...ack, revision: 2.1 }, { ...ack, extra: true }]) {
      fetchMock.mockResolvedValueOnce(reply(data))
      await expect(deleteItem(item.id, proof())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
  })

  it.each([201, 204])('rejects unexpected HTTP success status %i', async (status) => {
    fetchMock.mockResolvedValue(status === 204 ? new Response(null, { status }) : reply(item, undefined, status))
    const { createItem } = await import('./catalog')
    await expect(createItem({ name: item.name }, write())).rejects.toMatchObject({ code: 'INVALID_RESPONSE', status })
  })
})

describe('catalog caller validation and explicit write ownership', () => {
  it('rejects invalid/custom-incomplete creation, empty patches and internal wrappers before transport', async () => {
    const api = await import('./catalog')
    const create = api.createItem as unknown as (body: unknown, context: unknown) => Promise<unknown>
    const patch = api.patchItem as unknown as (id: string, body: unknown, context: unknown) => Promise<unknown>
    const archive = api.archiveItem as unknown as (id: string, body: unknown, context: unknown) => Promise<unknown>
    for (const body of [{}, { name: undefined }, { name: '' }, { name: null }, { name: '\uD800' }, { name: 'x'.repeat(301) },
      { name: 'Item', unit_code: 'kg' }, { name: 'Item', base_unit: 'g' }, { name: 'Item', scale_factor: 1000 },
      { name: 'Item', unit_code: 'kg', base_unit: 'g' }, { name: 'Item', unit_code: 'kg', scale_factor: 1000 }, { name: 'Item', base_unit: 'g', scale_factor: 1000 },
      { name: 'Item', brand: { set: true, value: null } }, { name: 'Item', category_id: '' }, { name: 'Item', model: 'x'.repeat(301) },
      { name: 'Item', description: null }, { name: 'Item', notes: 'x'.repeat(10001) }, { name: 'Item', tracking_mode: 'mixed' },
      { name: 'Item', unit_code: null }, { name: 'Item', base_unit: 'g\n' }, { name: 'Item', scale_factor: 0 }, { name: 'Item', scale_factor: 0.5 },
      { name: 'Item', scale_factor: Number.MAX_SAFE_INTEGER + 1 }, { name: 'Item', status: 'disposed' }, { name: 'Item', quantity_base: 1 }]) {
      await expect(create(body, write())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    }
    for (const body of [{}, { name: undefined }, { brand: undefined }, { category_id: undefined }, { brand: { set: true, value: null } },
      { model: '\uD800' }, { category_id: { set: true, value: null } }, { scale_factor: null }, { scale_factor: 0.5 }, { raw_ocr: 'private' }]) {
      await expect(patch(item.id, body, proof())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    }
    for (const body of [{ reason: null }, { reason: '\uD800' }, { reason: 'x'.repeat(10001) }, { archive_children: true }]) {
      await expect(archive(item.id, body, proof())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    }
    await expect(create({ name: item.name }, proof())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it.each(['', '.', '..', 'a/b', 'a\\b', 'a\0b', '😀'.repeat(129), '\uD800'])('rejects unsafe path %j without a request', async (id) => {
    const api = await import('./catalog')
    for (const invoke of [() => api.fetchItem(id, signal()), () => api.patchItem(id, { name: 'Name' }, proof()), () => api.archiveItem(id, {}, proof()), () => api.deleteItem(id, proof())]) {
      await expect(invoke()).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    }
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('preserves imported new/tree/unicode IDs without treating them as static routes or injecting query/hash', async () => {
    const { fetchItem } = await import('./catalog')
    for (const id of ['new', 'tree', '保留 é #?%+&']) {
      fetchMock.mockResolvedValueOnce(reply({ ...item, id }))
      await fetchItem(id, signal())
    }
    for (const [index, [request]] of fetchMock.mock.calls.entries()) {
      const url = new URL((request as Request).url)
      expect(url.search).toBe('')
      expect(url.hash).toBe('')
      expect(decodeURIComponent(url.pathname.split('/').at(-1)!)).toBe(['new', 'tree', '保留 é #?%+&'][index])
    }
  })

  it('validates every civil/filter/page bound without normalizing raw query ownership', async () => {
    const { fetchItemsPage, patchItem, archiveItem, deleteItem } = await import('./catalog')
    const fetchPage = fetchItemsPage as unknown as (query: unknown, signal: AbortSignal) => Promise<unknown>
    for (const query of [{ from: '0000-01-01' }, { from: '2026-02-29' }, { from: '2026-1-01' }, { to: '2026-01-32' },
      { from: '2026-11-06', to: '2026-11-05' }, { currency: 'usd' }, { currency: 'USD\n' }, { currency: 'US' },
      { category_id: '' }, { location_id: '\uD800' }, { scope: 'all' }, { status: 'disposed' }, { merchant: '' }, { merchant: 'x'.repeat(301) },
      { q: '\uD800' }, { q: 'x'.repeat(501) }, { limit: 0 }, { limit: 101 }, { limit: 0.5 }, { cursor: '' }, { cursor: 'x'.repeat(2049) }, { unknown: true }]) {
      await expect(fetchPage(query, signal())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    }
    for (const expectedRevision of [0, -1, 1.1, Number.NaN, Number.POSITIVE_INFINITY, Number.MAX_SAFE_INTEGER + 1]) {
      for (const invoke of [() => patchItem(item.id, { name: 'Name' }, { ...write(), expectedRevision }),
        () => archiveItem(item.id, {}, { ...write(), expectedRevision }), () => deleteItem(item.id, { ...write(), expectedRevision })]) {
        await expect(invoke()).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
      }
    }
    expect(fetchMock).not.toHaveBeenCalled()
    fetchMock.mockImplementation(async () => reply({ data: [], next_cursor: null }))
    expect(await fetchItemsPage({ from: '0001-01-01', to: '9999-12-31' }, signal())).toEqual({ data: [], next_cursor: null })
    expect(await fetchItemsPage({ from: '2024-02-29', to: '2024-02-29' }, signal())).toEqual({ data: [], next_cursor: null })
  })
})

describe('safe catalog failures and interruption', () => {
  it.each(['ITEM_HAS_HISTORY', 'REVISION_CONFLICT', 'IDEMPOTENCY_EXPIRED', 'IDEMPOTENCY_CONFLICT', 'INVALID_CATALOG_URL', 'INVALID_CURSOR'])('retains allowlisted %s without messages/details or retry', async (code) => {
    fetchMock.mockResolvedValue(Response.json({ error: { code, request_id: 'safe-request', message: 'secret=do-not-retain', details: { path: '/private/database' } } },
      { status: code.startsWith('INVALID_') ? 400 : 409 }))
    const { deleteItem } = await import('./catalog')
    const failure = await deleteItem(item.id, proof()).catch((error: unknown) => error)
    expect(failure).toMatchObject({ code, requestId: 'safe-request' })
    expect(JSON.stringify(failure)).not.toMatch(/secret=|private\/database|message|details|cause/)
    window.dispatchEvent(new Event('online'))
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('classifies malformed JSON and transport failures without retaining unsafe causes', async () => {
    const { fetchItem } = await import('./catalog')
    fetchMock.mockResolvedValueOnce(new Response('{not-json', { headers: { 'Content-Type': 'application/json' } }))
    await expect(fetchItem(item.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    fetchMock.mockRejectedValueOnce(new Error('private cookie=secret transport'))
    const failure = await fetchItem(item.id, signal()).catch((error: unknown) => error)
    expect(failure).toMatchObject({ code: 'NETWORK_ERROR' })
    expect(JSON.stringify(failure)).not.toMatch(/cookie=|secret|cause/)
  })

  it.each(['page', 'item', 'write'])('honors caller cancellation and deadlines for %s without replay', async (kind) => {
    vi.useFakeTimers()
    fetchMock.mockImplementation((request) => new Promise((_resolve, reject) => {
      ;(request as Request).signal.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')), { once: true })
    }))
    const api = await import('./catalog')
    const controller = new AbortController()
    const invoke = () => kind === 'page' ? api.fetchItemsPage({}, controller.signal) : kind === 'item' ? api.fetchItem(item.id, controller.signal) : api.patchItem(item.id, { name: item.name }, { ...proof(), signal: controller.signal })
    const pending = invoke()
    const aborted = expect(pending).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
    controller.abort()
    await aborted
    expect(vi.getTimerCount()).toBe(0)
    const other = kind === 'page' ? api.fetchItemsPage({}, signal()) : kind === 'item' ? api.fetchItem(item.id, signal()) : api.createItem({ name: item.name }, write())
    const timedOut = expect(other).rejects.toMatchObject({ code: 'REQUEST_TIMEOUT' })
    await vi.advanceTimersByTimeAsync(kind === 'write' ? 30_000 : 8_000)
    await timedOut
    expect(vi.getTimerCount()).toBe(0)
    window.dispatchEvent(new Event('online'))
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it('rejects already-aborted and late successful responses without dispatching a replacement', async () => {
    const { fetchItem } = await import('./catalog')
    const before = new AbortController()
    before.abort()
    await expect(fetchItem(item.id, before.signal)).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
    expect(fetchMock).not.toHaveBeenCalled()
    let release: ((response: Response) => void) | undefined
    fetchMock.mockImplementation(() => new Promise((resolve) => { release = resolve }))
    const controller = new AbortController()
    const pending = fetchItem(item.id, controller.signal)
    controller.abort()
    release?.(reply(item))
    await expect(pending).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })
})
