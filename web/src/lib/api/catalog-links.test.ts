import { beforeEach, describe, expect, it, vi } from 'vitest'
import { version } from '../../../package.json'

type OwnedAPI = typeof import('./catalog-links')
const fetchMock = vi.fn<typeof fetch>()
const owner = 'item-1'
const barcode = { id: 'barcode-1', item_id: owner, revision: 1, symbology: 'UPC_A', raw_value: '036000291452', normalized_value: '036000291452' } as const
const alias = { id: 'alias-1', item_id: owner, revision: 1, alias: '  Exact é 😀  ' } as const
const attribute = { id: 'attribute-1', item_id: owner, revision: 1, attribute_key: 'Key', attribute_value: '' } as const
const tag = { id: 'tag-1', name: '  Tag 😀  ', revision: 1 } as const
const item = { id: owner, revision: 2, name: 'Owner', brand: null, model: '', category_id: null, description: '', notes: '',
  tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1, status: 'active' } as const
const ack = { ok: true, operation_id: 'event-1', revision: 2 }
const csrf = 'A'.repeat(43)
const signal = () => new AbortController().signal
const context = (expectedRevision = 1) => ({ csrfToken: csrf, idempotencyKey: 'original-owned-key', expectedRevision, signal: signal() })
const rowContext = () => ({ ...context(), itemID: owner })
const reply = (data: unknown, etag?: string, status = 200) => Response.json(data, { status, headers: etag === undefined ? {} : { ETag: etag } })
const barcodeBody = { symbology: barcode.symbology, raw_value: barcode.raw_value }

beforeEach(() => {
  vi.resetModules()
  fetchMock.mockReset()
  vi.stubGlobal('fetch', fetchMock)
})

describe('fourteen explicit owned transports', () => {
  const cases: { name: string; method: string; path: string; body?: unknown; result: unknown; proof?: number; etag?: string; invoke: (api: OwnedAPI) => Promise<unknown> }[] = [
    { name: 'barcode page', method: 'GET', path: '/items/item-1/barcodes', result: { data: [barcode], next_cursor: null }, invoke: (api) => api.fetchItemBarcodesPage(owner, {}, signal()) },
    { name: 'alias page', method: 'GET', path: '/items/item-1/aliases', result: { data: [alias], next_cursor: null }, invoke: (api) => api.fetchItemAliasesPage(owner, {}, signal()) },
    { name: 'attribute page', method: 'GET', path: '/items/item-1/attributes', result: { data: [attribute], next_cursor: null }, invoke: (api) => api.fetchItemAttributesPage(owner, {}, signal()) },
    { name: 'assigned tags', method: 'GET', path: '/items/item-1/tags', result: { data: [tag], next_cursor: null }, invoke: (api) => api.fetchItemTagsPage(owner, {}, signal()) },
    { name: 'barcode creation', method: 'POST', path: '/items/item-1/barcodes', body: barcodeBody, result: barcode, proof: 7, etag: '"1"', invoke: (api) => api.createBarcode(owner, barcodeBody, context(7)) },
    { name: 'alias creation', method: 'POST', path: '/items/item-1/aliases', body: { alias: alias.alias }, result: alias, proof: 7, etag: '"1"', invoke: (api) => api.createAlias(owner, { alias: alias.alias }, context(7)) },
    { name: 'attribute creation', method: 'POST', path: '/items/item-1/attributes', body: { attribute_key: 'Key', attribute_value: '' }, result: attribute, proof: 7, etag: '"1"', invoke: (api) => api.createAttribute(owner, { attribute_key: 'Key', attribute_value: '' }, context(7)) },
    { name: 'barcode replacement', method: 'PATCH', path: '/barcodes/barcode-1', body: barcodeBody, result: { ...barcode, revision: 2 }, proof: 1, etag: '"2"', invoke: (api) => api.patchBarcode(barcode.id, barcodeBody, rowContext()) },
    { name: 'alias replacement', method: 'PATCH', path: '/aliases/alias-1', body: { alias: 'New' }, result: { ...alias, alias: 'New', revision: 2 }, proof: 1, etag: '"2"', invoke: (api) => api.patchAlias(alias.id, { alias: 'New' }, rowContext()) },
    { name: 'partial attribute clearing', method: 'PATCH', path: '/attributes/attribute-1', body: { attribute_value: '' }, result: attribute, proof: 1, etag: '"1"', invoke: (api) => api.patchAttribute(attribute.id, { attribute_value: '' }, rowContext()) },
    { name: 'barcode deletion', method: 'DELETE', path: '/barcodes/barcode-1', result: ack, proof: 1, invoke: (api) => api.deleteBarcode(barcode.id, rowContext()) },
    { name: 'alias deletion', method: 'DELETE', path: '/aliases/alias-1', result: ack, proof: 1, invoke: (api) => api.deleteAlias(alias.id, rowContext()) },
    { name: 'attribute deletion', method: 'DELETE', path: '/attributes/attribute-1', result: ack, proof: 1, invoke: (api) => api.deleteAttribute(attribute.id, rowContext()) },
    { name: 'complete tag replacement', method: 'PUT', path: '/items/item-1/tags', body: { tag_ids: [] }, result: item, proof: 1, etag: '"2"', invoke: (api) => api.replaceItemTags(owner, { tag_ids: [] }, context()) },
  ]
  it.each(cases)('sends one $name with exact caller-owned original context', async (test) => {
    fetchMock.mockResolvedValue(reply(test.result, test.etag))
    const api = await import('./catalog-links')
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
    expect(request.headers.get('Idempotency-Key')).toBe(test.method === 'GET' ? null : 'original-owned-key')
    expect(request.headers.get('X-HomeBox-Client-Version')).toBe(test.method === 'GET' ? null : version)
    expect(request.headers.get('If-Match')).toBe(test.proof === undefined ? null : `"${test.proof}"`)
    expect(request.headers.has('X-Owner-Revision')).toBe(false)
    if (test.body === undefined) {
      expect(await request.text()).toBe('')
      expect(request.headers.has('Content-Type')).toBe(false)
    } else expect(await request.json()).toEqual(test.body)
  })

  it('encodes literal cursors and imported new/tree IDs without adding hidden pagination/defaults', async () => {
    const api = await import('./catalog-links')
    fetchMock.mockImplementation(async () => reply({ data: [], next_cursor: null }))
    for (const id of ['new', 'tree', '物品 +?#']) {
      await api.fetchItemTagsPage(id, { limit: 1, cursor: 'opaque?x=1&limit=99+#' }, signal())
      const request = fetchMock.mock.lastCall?.[0] as Request
      const url = new URL(request.url)
      expect(decodeURIComponent(url.pathname)).toBe(`/api/v1/items/${id}/tags`)
      expect(url.searchParams.get('cursor')).toBe('opaque?x=1&limit=99+#')
      expect(url.searchParams.getAll('limit')).toEqual(['1'])
      expect(url.searchParams.has('x')).toBe(false)
    }
    expect(fetchMock).toHaveBeenCalledTimes(3)
  })

  it('snapshots original bodies, ordered tag IDs, owner and proof without rebasing or automatic replay', async () => {
    const api = await import('./catalog-links')
    let release: ((response: Response) => void) | undefined
    fetchMock.mockImplementation(() => new Promise((resolve) => { release = resolve }))
    const body = { tag_ids: ['tag-2', 'tag-1'] }
    const proof = context()
    const pending = api.replaceItemTags(owner, body, proof)
    body.tag_ids.reverse()
    proof.expectedRevision = 99
    proof.idempotencyKey = 'changed-after-sending'
    release?.(reply(item, '"2"'))
    expect(await pending).toEqual(item)
    const request = fetchMock.mock.calls[0]?.[0] as Request
    expect(await request.json()).toEqual({ tag_ids: ['tag-2', 'tag-1'] })
    expect(request.headers.get('If-Match')).toBe('"1"')
    expect(request.headers.get('Idempotency-Key')).toBe('original-owned-key')
    fetchMock.mockImplementation(async () => reply(alias, '"1"'))
    const rowProof = rowContext()
    const original = { alias: alias.alias }
    await api.patchAlias(alias.id, original, rowProof)
    await api.patchAlias(alias.id, original, rowProof)
    const first = fetchMock.mock.calls[1]?.[0] as Request
    const second = fetchMock.mock.calls[2]?.[0] as Request
    expect(await first.text()).toBe(await second.text())
    window.dispatchEvent(new Event('online'))
    expect(fetchMock).toHaveBeenCalledTimes(3)
  })

  it('uses no cookie, persistent replica/cache, draft or queued-mutation storage', async () => {
    const spies = [vi.spyOn(window.localStorage, 'getItem'), vi.spyOn(window.localStorage, 'setItem'), vi.spyOn(window.sessionStorage, 'getItem'),
      vi.spyOn(window.sessionStorage, 'setItem'), vi.spyOn(document, 'cookie', 'get'), vi.spyOn(document, 'cookie', 'set')]
    const database = vi.fn()
    const cache = vi.fn()
    vi.stubGlobal('indexedDB', { open: database })
    vi.stubGlobal('caches', { open: cache })
    const api = await import('./catalog-links')
    fetchMock.mockResolvedValue(reply(alias, '"1"'))
    await api.createAlias(owner, { alias: alias.alias }, context())
    for (const spy of [...spies, database, cache]) expect(spy).not.toHaveBeenCalled()
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })
})

describe('owned input and explicit error/cancellation boundaries', () => {
  it('rejects invalid identity, owner, query, proofs and context before sending any request', async () => {
    const api = await import('./catalog-links')
    for (const id of ['', '.', '..', 'x/y', 'x\\y', '\0', '😀'.repeat(129), '\uD800']) {
      for (const read of [api.fetchItemBarcodesPage, api.fetchItemAliasesPage, api.fetchItemAttributesPage, api.fetchItemTagsPage]) {
        await expect(read(id, {}, signal())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
      }
      await expect(api.createAlias(id, { alias: 'Alias' }, context())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
      await expect(api.patchAlias(id, { alias: 'Alias' }, rowContext())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
      await expect(api.deleteAlias(id, rowContext())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
      await expect(api.patchAlias(alias.id, { alias: 'Alias' }, { ...rowContext(), itemID: id })).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
      await expect(api.deleteAlias(alias.id, { ...rowContext(), itemID: id })).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    }
    for (const query of [{ limit: 0 }, { limit: 101 }, { cursor: '' }, { cursor: 'x'.repeat(2049) }, { cursor: '\uD800' }, { unknown: true }]) {
      for (const read of [api.fetchItemBarcodesPage, api.fetchItemAliasesPage, api.fetchItemAttributesPage, api.fetchItemTagsPage]) {
        await expect(read(owner, query as never, signal())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
      }
    }
    for (const expectedRevision of [undefined, 0, 1.5, Number.MAX_SAFE_INTEGER + 1]) {
      const proof = { ...context(), expectedRevision } as never
      await expect(api.createBarcode(owner, barcodeBody, proof)).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
      await expect(api.createAlias(owner, { alias: 'Alias' }, proof)).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
      await expect(api.createAttribute(owner, { attribute_key: 'Key', attribute_value: '' }, proof)).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
      await expect(api.replaceItemTags(owner, { tag_ids: [] }, proof)).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    }
    for (const mutationContext of [{ ...rowContext(), expectedRevision: undefined }, { ...rowContext(), itemID: undefined }]) {
      await expect(api.patchAttribute(attribute.id, { attribute_value: '' }, mutationContext as never)).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    }
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('passes original caller credentials/keys unchanged and leaves their authority to the server', async () => {
    const api = await import('./catalog-links')
    for (const test of [{ csrfToken: '', idempotencyKey: 'original-owned-key', code: 'CSRF_REJECTED', status: 403 },
      { csrfToken: csrf, idempotencyKey: 'short', code: 'INVALID_IDEMPOTENCY_KEY', status: 400 }]) {
      fetchMock.mockResolvedValueOnce(reply({ error: { code: test.code } }, undefined, test.status))
      await expect(api.createAlias(owner, { alias: 'Alias' }, { ...context(), csrfToken: test.csrfToken, idempotencyKey: test.idempotencyKey }))
        .rejects.toMatchObject({ code: test.code, status: test.status })
      const request = fetchMock.mock.lastCall?.[0] as Request
      expect(request.headers.get('X-CSRF-Token')).toBe(test.csrfToken)
      expect(request.headers.get('Idempotency-Key')).toBe(test.idempotencyKey)
    }
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it('rejects unknown/private, nullable, incomplete and unbounded bodies without installing defaults', async () => {
    const api = await import('./catalog-links')
    for (const body of [{}, { ...barcodeBody, raw_value: 36000291452 }, { ...barcodeBody, raw_value: '０36000291452' },
      { ...barcodeBody, raw_value: '036000291452\n' }, { symbology: 'UPC_E', raw_value: '24252614' }, { symbology: 'CODE_128', raw_value: '條碼' },
      { symbology: 'OTHER', raw_value: '😀'.repeat(513) }, { symbology: 'OTHER', raw_value: '\uD800' }, { symbology: 'WRONG', raw_value: '00123' },
      { ...barcodeBody, normalized_value: 'caller' }, { ...barcodeBody, item_id: 'other' }]) {
      await expect(api.createBarcode(owner, body as never, context())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
      await expect(api.patchBarcode(barcode.id, body as never, rowContext())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    }
    for (const body of [{}, { alias: '' }, { alias: null }, { alias: '😀'.repeat(301) }, { alias: 'Alias', item_id: 'other' }]) {
      await expect(api.createAlias(owner, body as never, context())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
      await expect(api.patchAlias(alias.id, body as never, rowContext())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    }
    for (const body of [{ attribute_key: 'Key' }, { attribute_key: '', attribute_value: '' }, { attribute_key: '😀'.repeat(101), attribute_value: '' },
      { attribute_key: 'Key', attribute_value: null }, { attribute_key: 'Key', attribute_value: 'x'.repeat(10001) }]) {
      await expect(api.createAttribute(owner, body as never, context())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    }
    for (const body of [{}, { attribute_key: undefined, attribute_value: undefined }, { attribute_value: null }, { attribute_key: '' }, { attribute_value: '', id: 'caller' }]) {
      await expect(api.patchAttribute(attribute.id, body as never, rowContext())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    }
    for (const body of [{}, { tag_ids: null }, { tag_ids: ['', 'tag'] }, { tag_ids: ['same', 'same'] }, { tag_ids: ['\uD800'] },
      { tag_ids: Array.from({ length: 101 }, (_, index) => `tag-${index}`) }, { tag_ids: [], revision: 1 }]) {
      await expect(api.replaceItemTags(owner, body as never, context())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    }
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('preserves all supported request alphabets and leaves checksum arithmetic server-authoritative', async () => {
    const api = await import('./catalog-links')
    const values = [
      { symbology: 'EAN_13', raw_value: '4006381333931' }, { symbology: 'EAN_8', raw_value: '96385074' },
      { symbology: 'UPC_A', raw_value: '036000291452' }, { symbology: 'UPC_E', raw_value: '04252614' },
      { symbology: 'CODE_128', raw_value: String.fromCharCode(...Array.from({ length: 128 }, (_, index) => index)) },
      { symbology: 'QR_CODE', raw_value: '😀'.repeat(512) }, { symbology: 'OTHER', raw_value: '  https://example.invalid/opaque?code=00123  ' },
    ] as const
    for (const body of values) {
      fetchMock.mockResolvedValueOnce(reply({ ...barcode, ...body, normalized_value: body.raw_value }, '"1"'))
      expect((await api.createBarcode(owner, body, context())).raw_value).toBe(body.raw_value)
      const request = fetchMock.mock.lastCall?.[0] as Request
      expect(await request.json()).toEqual(body)
    }
    fetchMock.mockResolvedValueOnce(reply({ error: { code: 'VALIDATION_ERROR' } }, undefined, 422))
    await expect(api.createBarcode(owner, { symbology: 'EAN_13', raw_value: '4006381333932' }, context())).rejects.toMatchObject({ code: 'VALIDATION_ERROR', status: 422 })
    expect(fetchMock).toHaveBeenCalledTimes(values.length + 1)
  })

  it.each(['BARCODE_CONFLICT', 'ALIAS_CONFLICT', 'REVISION_CONFLICT', 'IDEMPOTENCY_EXPIRED', 'AUTHENTICATION_REQUIRED', 'SERVICE_UNAVAILABLE'])('keeps %s safe and never refreshes, retries or rebases', async (code) => {
    const api = await import('./catalog-links')
    fetchMock.mockResolvedValue(reply({ error: { code, request_id: 'safe-request', message: 'secret=Private', details: { file: '/private/database' } } }, undefined, 409))
    const failure = await api.patchAlias(alias.id, { alias: 'New' }, rowContext()).catch((error: unknown) => error)
    expect(failure).toMatchObject({ code, requestId: 'safe-request', status: 409 })
    expect(JSON.stringify(failure)).not.toMatch(/secret=|private\/database|message|details|cause/)
    window.dispatchEvent(new Event('online'))
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('honors aborts and distinct read/write deadlines without treating a late reply as success', async () => {
    vi.useFakeTimers()
    try {
      const api = await import('./catalog-links')
      fetchMock.mockImplementation((request) => new Promise((_resolve, reject) => {
        const activeRequest = request as Request
        activeRequest.signal.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')), { once: true })
      }))
      const controller = new AbortController()
      controller.abort()
      await expect(api.fetchItemTagsPage(owner, {}, controller.signal)).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
      expect(fetchMock).not.toHaveBeenCalled()
      const read = expect(api.fetchItemAliasesPage(owner, {}, signal())).rejects.toMatchObject({ code: 'REQUEST_TIMEOUT' })
      await vi.advanceTimersByTimeAsync(8_000)
      await read
      const proof = rowContext()
      const write = expect(api.patchAlias(alias.id, { alias: 'New' }, proof)).rejects.toMatchObject({ code: 'REQUEST_TIMEOUT' })
      await vi.advanceTimersByTimeAsync(30_000)
      await write
      let release: ((response: Response) => void) | undefined
      fetchMock.mockImplementation(() => new Promise((resolve) => { release = resolve }))
      const active = new AbortController()
      const pending = expect(api.deleteAlias(alias.id, { ...rowContext(), signal: active.signal })).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
      active.abort()
      release?.(reply(ack))
      await pending
      expect(fetchMock).toHaveBeenCalledTimes(3)
      expect(vi.getTimerCount()).toBe(0)
    } finally { vi.useRealTimers() }
  })
})

describe('strict owned resource and page responses', () => {
  it.each([
    { ...barcode, id: '' }, { ...barcode, item_id: 'other' }, { ...barcode, raw_value: '' }, { ...barcode, raw_value: '\uD800' },
    { ...barcode, normalized_value: undefined }, { ...barcode, normalized_value: 'x'.repeat(513) }, { ...barcode, symbology: 'UNKNOWN' },
    { ...barcode, revision: Number.MAX_SAFE_INTEGER + 1 }, { ...barcode, private_ocr: 'Private' }, null, [], {},
  ])('rejects malformed/private/unrelated barcode response %j', async (value) => {
    fetchMock.mockResolvedValue(reply(value, '"1"'))
    const api = await import('./catalog-links')
    await expect(api.createBarcode(owner, barcodeBody, context())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  })

  it('separates retained representation from new-input rules without repairing historical values', async () => {
    const api = await import('./catalog-links')
    const retained = { ...barcode, raw_value: 'legacy manual raw', normalized_value: 'different retained normalization' }
    fetchMock.mockResolvedValue(reply({ data: [retained], next_cursor: null }))
    expect(await api.fetchItemBarcodesPage(owner, {}, signal())).toEqual({ data: [retained], next_cursor: null })
    const maximum = { ...retained, id: '😀'.repeat(128), item_id: '😀'.repeat(128), revision: Number.MAX_SAFE_INTEGER,
      raw_value: '😀'.repeat(512), normalized_value: '😀'.repeat(512) }
    fetchMock.mockResolvedValue(reply({ data: [maximum], next_cursor: null }))
    expect((await api.fetchItemBarcodesPage(maximum.item_id, {}, signal())).data[0]).toEqual(maximum)
    fetchMock.mockResolvedValue(reply({ data: [{ ...alias, alias: '😀'.repeat(300) }], next_cursor: null }))
    expect((await api.fetchItemAliasesPage(owner, {}, signal())).data[0]?.alias).toHaveLength(600)
    fetchMock.mockResolvedValue(reply({ data: [{ ...attribute, attribute_key: '😀'.repeat(100), attribute_value: '😀'.repeat(10000) }], next_cursor: null }))
    expect((await api.fetchItemAttributesPage(owner, {}, signal())).data[0]?.attribute_value).toHaveLength(20000)
  })

  it('requires exact independent IDs/owners, original noop-or-next revisions and strong resource ETags', async () => {
    const api = await import('./catalog-links')
    for (const etag of [undefined, '', '1', 'W/"1"', '"2"', '"1", "1"', '*']) {
      fetchMock.mockResolvedValueOnce(reply(alias, etag))
      await expect(api.patchAlias(alias.id, { alias: alias.alias }, rowContext())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
    for (const value of [{ ...alias, id: 'other-row' }, { ...alias, item_id: 'other-owner' }, { ...alias, revision: 3 }]) {
      fetchMock.mockResolvedValueOnce(reply(value, `"${value.revision}"`))
      await expect(api.patchAlias(alias.id, { alias: alias.alias }, rowContext())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
    fetchMock.mockResolvedValueOnce(reply({ ...attribute, revision: 2 }, '"2"'))
    await expect(api.createAttribute(owner, { attribute_key: 'Key', attribute_value: '' }, context())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    for (const revision of [1, 2]) {
      fetchMock.mockResolvedValueOnce(reply({ ...attribute, revision }, `"${revision}"`))
      expect((await api.patchAttribute(attribute.id, { attribute_key: 'Key' }, rowContext())).revision).toBe(revision)
    }
  })

  it.each(['barcode', 'alias', 'attribute', 'tag'] as const)('validates the complete %s page and known scope without a fake list ETag', async (kind) => {
    const api = await import('./catalog-links')
    const entry = { barcode, alias, attribute, tag }[kind]
    const read = { barcode: api.fetchItemBarcodesPage, alias: api.fetchItemAliasesPage, attribute: api.fetchItemAttributesPage, tag: api.fetchItemTagsPage }[kind]
    for (const value of [{ data: [] }, { data: null, next_cursor: null }, { data: [entry, entry], next_cursor: null }, { data: [], next_cursor: 'next' },
      { data: [entry], next_cursor: 'same' }, { data: [entry], next_cursor: '' }, { data: [entry], next_cursor: '\uD800' },
      { data: [entry], next_cursor: 'x'.repeat(2049) }, { data: [entry, { ...entry, id: 'second' }], next_cursor: null }, { data: [], next_cursor: null, raw_ocr: 'Private' }]) {
      fetchMock.mockResolvedValueOnce(reply(value))
      await expect(read(owner, { limit: 1, cursor: 'same' }, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
    fetchMock.mockResolvedValueOnce(reply({ data: [entry], next_cursor: null }, '"1"'))
    await expect(read(owner, {}, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    fetchMock.mockResolvedValueOnce(reply({ data: Array.from({ length: 51 }, (_, index) => ({ ...entry, id: `row-${index}` })), next_cursor: null }))
    await expect(read(owner, {}, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    fetchMock.mockResolvedValueOnce(reply({ data: [], next_cursor: null }))
    expect(await read(owner, {}, signal())).toEqual({ data: [], next_cursor: null })
  })

  it('rejects unrelated alias/attribute owners, missing values and invalid assigned Tag records', async () => {
    const api = await import('./catalog-links')
    for (const value of [{ ...alias, item_id: 'other' }, { ...alias, alias: undefined }, { ...alias, alias: null }]) {
      fetchMock.mockResolvedValueOnce(reply({ data: [value], next_cursor: null }))
      await expect(api.fetchItemAliasesPage(owner, {}, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
    for (const value of [{ ...attribute, item_id: 'other' }, { ...attribute, attribute_value: null }, { ...attribute, attribute_key: '' }]) {
      fetchMock.mockResolvedValueOnce(reply({ data: [value], next_cursor: null }))
      await expect(api.fetchItemAttributesPage(owner, {}, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
    fetchMock.mockResolvedValueOnce(reply({ data: [{ ...tag, name: '' }], next_cursor: null }))
    await expect(api.fetchItemTagsPage(owner, {}, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  })

  it('requires the frozen owning Item for replacement and exact independent deletion acknowledgements', async () => {
    const api = await import('./catalog-links')
    for (const value of [{ data: [tag], next_cursor: null }, { ...item, id: 'other' }, { ...item, revision: 3 }, { ...item, model: undefined }]) {
      fetchMock.mockResolvedValueOnce(reply(value, '"2"'))
      await expect(api.replaceItemTags(owner, { tag_ids: [] }, context())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
    for (const remove of [api.deleteBarcode, api.deleteAlias, api.deleteAttribute]) {
      for (const value of [{ ...ack, revision: 1 }, { ...ack, revision: 3 }, { ...ack, operation_id: '' }, { ...ack, extra: true }]) {
        fetchMock.mockResolvedValueOnce(reply(value))
        await expect(remove('row', rowContext())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
      }
      fetchMock.mockResolvedValueOnce(reply(ack, '"2"'))
      await expect(remove('row', rowContext())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
  })
})
