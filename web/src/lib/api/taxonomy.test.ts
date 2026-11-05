import { beforeEach, describe, expect, it, vi } from 'vitest'
import { version } from '../../../package.json'

type TaxonomyAPI = typeof import('./taxonomy')
const fetchMock = vi.fn<typeof fetch>()
const category = { id: 'category-1', revision: 1, parent_id: null, name: '  家用品 😀 é  ' }
const tag = { id: 'tag-1', revision: 1, name: '  隨身 😀 é  ' }
const csrf = 'A'.repeat(43)
const signal = () => new AbortController().signal
const write = () => ({ csrfToken: csrf, idempotencyKey: 'explicit-taxonomy-key', signal: signal() })
const proof = () => ({ ...write(), expectedRevision: 1 })
const ack = { ok: true, operation_id: 'event-1', revision: 2 }

function reply(data: unknown, etag?: string, status = 200) {
  const revision = data && typeof data === 'object' && 'revision' in data ? data.revision : undefined
  return Response.json(data, { status, headers: etag === undefined && typeof revision !== 'number' ? {} : { ETag: etag ?? `"${String(revision)}"` } })
}

beforeEach(() => {
  vi.resetModules()
  fetchMock.mockReset()
  vi.stubGlobal('fetch', fetchMock)
})

describe('typed taxonomy transport', () => {
  const cases: { name: string; method: string; path: string; body?: unknown; result: unknown; revision?: number; invoke: (api: TaxonomyAPI) => Promise<unknown> }[] = [
    { name: 'category page', method: 'GET', path: '/categories?limit=2&cursor=opaque', result: { data: [category], next_cursor: null }, invoke: (api) => api.fetchCategoriesPage({ limit: 2, cursor: 'opaque' }, signal()) },
    { name: 'tag page', method: 'GET', path: '/tags?limit=2&cursor=opaque', result: { data: [tag], next_cursor: null }, invoke: (api) => api.fetchTagsPage({ limit: 2, cursor: 'opaque' }, signal()) },
    { name: 'root category creation', method: 'POST', path: '/categories', body: { name: category.name }, result: category, invoke: (api) => api.createCategory({ name: category.name }, write()) },
    { name: 'explicit-null root creation', method: 'POST', path: '/categories', body: { name: category.name, parent_id: null }, result: category, invoke: (api) => api.createCategory({ name: category.name, parent_id: null }, write()) },
    { name: 'child category creation', method: 'POST', path: '/categories', body: { name: category.name, parent_id: 'parent' }, result: { ...category, parent_id: 'parent' }, revision: 7, invoke: (api) => api.createCategory({ name: category.name, parent_id: 'parent' }, { ...write(), expectedRevision: 7 }) },
    { name: 'category rename', method: 'PATCH', path: '/categories/category-1', body: { name: 'Changed' }, result: { ...category, revision: 2, name: 'Changed' }, revision: 1, invoke: (api) => api.patchCategory(category.id, { name: 'Changed' }, proof()) },
    { name: 'partial category move to root', method: 'PATCH', path: '/categories/category-1', body: { parent_id: null }, result: category, revision: 1, invoke: (api) => api.patchCategory(category.id, { parent_id: null }, proof()) },
    { name: 'category deletion', method: 'DELETE', path: '/categories/category-1', result: ack, revision: 1, invoke: (api) => api.deleteCategory(category.id, proof()) },
    { name: 'tag creation', method: 'POST', path: '/tags', body: { name: tag.name }, result: tag, invoke: (api) => api.createTag({ name: tag.name }, write()) },
    { name: 'tag rename', method: 'PATCH', path: '/tags/tag-1', body: { name: 'Changed' }, result: { ...tag, revision: 2, name: 'Changed' }, revision: 1, invoke: (api) => api.patchTag(tag.id, { name: 'Changed' }, proof()) },
    { name: 'tag deletion', method: 'DELETE', path: '/tags/tag-1', result: ack, revision: 1, invoke: (api) => api.deleteTag(tag.id, proof()) },
  ]
  it.each(cases)('sends one explicit $name request with original context', async (test) => {
    fetchMock.mockResolvedValue(reply(test.result))
    const api = await import('./taxonomy')
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
    expect(request.headers.get('Idempotency-Key')).toBe(test.method === 'GET' ? null : 'explicit-taxonomy-key')
    expect(request.headers.get('X-HomeBox-Client-Version')).toBe(test.method === 'GET' ? null : version)
    expect(request.headers.get('If-Match')).toBe(test.revision === undefined ? null : `"${test.revision}"`)
    if (test.body === undefined) {
      expect(await request.text()).toBe('')
      expect(request.headers.has('Content-Type')).toBe(false)
    } else expect(await request.json()).toEqual(test.body)
  })

  it.each(['category', 'tag'])('requests explicit %s pages without following or interpreting cursors', async (kind) => {
    const api = await import('./taxonomy')
    const fetchPage = kind === 'category' ? api.fetchCategoriesPage : api.fetchTagsPage
    fetchMock.mockResolvedValue(reply({ data: [kind === 'category' ? category : tag], next_cursor: 'next' }))
    expect((await fetchPage({ limit: 1, cursor: 'opaque?x=1&limit=99+#' }, signal())).next_cursor).toBe('next')
    const request = fetchMock.mock.calls[0]?.[0] as Request
    const url = new URL(request.url)
    expect(url.searchParams.get('cursor')).toBe('opaque?x=1&limit=99+#')
    expect(url.searchParams.getAll('limit')).toEqual(['1'])
    expect(url.searchParams.has('x')).toBe(false)
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it.each(['category', 'tag'])('preserves the original %s request on explicit retry and snapshots deletion proof', async (kind) => {
    const api = await import('./taxonomy')
    const patch = kind === 'category' ? api.patchCategory : api.patchTag
    const remove = kind === 'category' ? api.deleteCategory : api.deleteTag
    const resource = kind === 'category' ? category : tag
    const body = { name: '  Changed é  ' }
    const context = proof()
    fetchMock.mockImplementation(async () => reply({ ...resource, ...body, revision: 2 }))
    await patch(resource.id, body, context)
    await patch(resource.id, body, context)
    const first = fetchMock.mock.calls[0]?.[0] as Request
    const second = fetchMock.mock.calls[1]?.[0] as Request
    expect(second.headers.get('If-Match')).toBe('"1"')
    expect(second.headers.get('Idempotency-Key')).toBe(first.headers.get('Idempotency-Key'))
    expect(await second.text()).toBe(await first.text())
    let release: ((response: Response) => void) | undefined
    fetchMock.mockImplementation(() => new Promise((resolve) => { release = resolve }))
    const pending = remove(resource.id, context)
    context.expectedRevision = 99
    context.idempotencyKey = 'changed-after-sending'
    release?.(reply(ack))
    expect(await pending).toEqual(ack)
    const deletion = fetchMock.mock.calls[2]?.[0] as Request
    expect(deletion.headers.get('If-Match')).toBe('"1"')
    expect(deletion.headers.get('Idempotency-Key')).toBe('explicit-taxonomy-key')
  })

  it('never reads or persists cookies, session material, metadata, cursors, or writes', async () => {
    const spies = [vi.spyOn(window.localStorage, 'getItem'), vi.spyOn(window.localStorage, 'setItem'),
      vi.spyOn(window.sessionStorage, 'getItem'), vi.spyOn(window.sessionStorage, 'setItem'),
      vi.spyOn(document, 'cookie', 'get'), vi.spyOn(document, 'cookie', 'set')]
    const database = vi.fn()
    const cache = vi.fn()
    vi.stubGlobal('indexedDB', { open: database })
    vi.stubGlobal('caches', { open: cache })
    const { createCategory, createTag } = await import('./taxonomy')
    fetchMock.mockResolvedValueOnce(reply(category)).mockResolvedValueOnce(reply(tag))
    await createCategory({ name: category.name }, write())
    await createTag({ name: tag.name }, write())
    for (const spy of [...spies, database, cache]) expect(spy).not.toHaveBeenCalled()
  })
})

describe('taxonomy runtime responses', () => {
  it.each([
    null, {}, { ...category, revision: 0 }, { ...category, revision: 1.1 },
    { ...category, revision: Number.MAX_SAFE_INTEGER + 1 }, { ...category, id: '' },
    { ...category, id: '😀'.repeat(129) }, { ...category, id: '\uD800' },
    { ...category, parent_id: category.id }, { ...category, parent_id: '' }, { ...category, parent_id: '\uD800' },
    { ...category, parent_id: '😀'.repeat(129) }, { ...category, parent_id: undefined },
    { ...category, name: '' }, { ...category, name: '\uD800' }, { ...category, name: '😀'.repeat(201) },
    { ...category, session_token: 'not allowed' }, { ...category, raw_ocr: 'not published' },
  ])('rejects malformed or private-bearing category %j', async (data) => {
    fetchMock.mockResolvedValue(reply(data))
    const { createCategory } = await import('./taxonomy')
    await expect(createCategory({ name: category.name }, write())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  })

  it.each([null, {}, { ...tag, revision: 0 }, { ...tag, revision: 1.1 }, { ...tag, revision: Number.MAX_SAFE_INTEGER + 1 },
    { ...tag, id: '' }, { ...tag, name: '' }, { ...tag, name: '\uD800' }, { ...tag, parent_id: null }, { ...tag, session_token: 'private' }])('rejects malformed or extra-bearing tag %j', async (data) => {
    fetchMock.mockResolvedValue(reply(data))
    const { createTag } = await import('./taxonomy')
    await expect(createTag({ name: tag.name }, write())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  })

  it.each(['category', 'tag'])('requires matching strong ETags, exact identity and a valid %s committed revision', async (kind) => {
    const api = await import('./taxonomy')
    const resource = kind === 'category' ? category : tag
    const patch = kind === 'category' ? api.patchCategory : api.patchTag
    const create = kind === 'category' ? api.createCategory : api.createTag
    for (const etag of ['', '1', 'W/"1"', '"2"', '"1", "1"']) {
      fetchMock.mockResolvedValueOnce(reply(resource, etag))
      await expect(patch(resource.id, { name: resource.name }, proof())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
    for (const data of [{ ...resource, id: 'wrong-resource' }, { ...resource, revision: 3 }]) {
      fetchMock.mockResolvedValueOnce(reply(data))
      await expect(patch(resource.id, { name: resource.name }, proof())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
    fetchMock.mockResolvedValueOnce(Response.json(resource))
    await expect(patch(resource.id, { name: resource.name }, proof())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    fetchMock.mockResolvedValueOnce(reply({ ...resource, revision: 2 }))
    await expect(create({ name: resource.name }, write())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    for (const revision of [1, 2]) {
      fetchMock.mockResolvedValueOnce(reply({ ...resource, revision }))
      expect((await patch(resource.id, { name: resource.name }, proof())).revision).toBe(revision)
    }
  })

  it.each(['category', 'tag'])('validates %s page shape, identity uniqueness and requested bounds', async (kind) => {
    const api = await import('./taxonomy')
    const fetchPage = kind === 'category' ? api.fetchCategoriesPage : api.fetchTagsPage
    const resource = kind === 'category' ? category : tag
    for (const data of [null, {}, { data: null }, { data: [resource, resource] },
      { data: [resource], next_cursor: '' }, { data: [resource], next_cursor: '\uD800' },
      { data: [resource], next_cursor: 'x'.repeat(2049) }, { data: [], next_cursor: 'next' },
      { data: [resource], next_cursor: null, private: true },
      { data: [resource, { ...resource, id: 'another' }], next_cursor: null },
      { data: Array.from({ length: 101 }, (_, index) => ({ ...resource, id: `resource-${index}` })) }]) {
      fetchMock.mockResolvedValueOnce(reply(data))
      await expect(fetchPage({ limit: 1 }, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
    fetchMock.mockResolvedValueOnce(reply({ data: [] }))
    expect(await fetchPage({}, signal())).toEqual({ data: [] })
  })

  it.each(['category', 'tag'])('accepts exact Unicode boundaries and safe %s no-op revisions without normalization', async (kind) => {
    const api = await import('./taxonomy')
    const patch = kind === 'category' ? api.patchCategory : api.patchTag
    const resource = { ...(kind === 'category' ? category : tag), id: '😀'.repeat(128), name: '😀'.repeat(200), revision: Number.MAX_SAFE_INTEGER }
    fetchMock.mockResolvedValue(reply(resource))
    expect(await patch(resource.id, { name: resource.name }, { ...write(), expectedRevision: Number.MAX_SAFE_INTEGER })).toEqual(resource)
  })

  it.each(['category', 'tag'])('requires a strict %s deletion acknowledgement at the original revision plus one', async (kind) => {
    const api = await import('./taxonomy')
    const remove = kind === 'category' ? api.deleteCategory : api.deleteTag
    for (const data of [{ ...ack, ok: false }, { ok: true, operation_id: 'event' }, { ...ack, operation_id: '' },
      { ...ack, revision: 1 }, { ...ack, revision: 3 }, { ...ack, revision: 2.1 }, { ...ack, extra: true }]) {
      fetchMock.mockResolvedValueOnce(reply(data))
      await expect(remove('resource', proof())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
  })

  it.each([201, 204])('rejects unexpected HTTP success status %i', async (status) => {
    fetchMock.mockResolvedValue(status === 204 ? new Response(null, { status }) : reply(tag, undefined, status))
    const { createTag } = await import('./taxonomy')
    await expect(createTag({ name: tag.name }, write())).rejects.toMatchObject({ code: 'INVALID_RESPONSE', status })
  })
})

describe('taxonomy caller validation and original-proof safety', () => {
  it('rejects empty patches, internal wrappers, unknown fields and unreviewed creation proofs before a request', async () => {
    const api = await import('./taxonomy')
    const createCategory = api.createCategory as unknown as (body: unknown, context: unknown) => Promise<unknown>
    const createTag = api.createTag as unknown as (body: unknown, context: unknown) => Promise<unknown>
    const patchCategory = api.patchCategory as unknown as (id: string, body: unknown, context: unknown) => Promise<unknown>
    for (const body of [{}, { name: '' }, { name: null }, { name: '\uD800' }, { name: '😀'.repeat(201) }, { name: 'Valid', extra: true }, { parent_id: null }]) {
      await expect(createCategory(body, write())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
      await expect(createTag(body, write())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    }
    for (const body of [{}, { name: undefined }, { parent_id: undefined }, { parent_id: { set: true, value: null } }, { parent_id: '' }]) {
      await expect(patchCategory(category.id, body, proof())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    }
    await expect(createCategory({ name: 'Child', parent_id: 'parent' }, write())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    await expect(createCategory({ name: 'Root' }, proof())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    await expect(createTag({ name: 'Tag' }, proof())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it.each(['', '.', '..', 'a/b', 'a\\b', 'a\0b', '😀'.repeat(129), '\uD800'])('rejects unsafe path %j without transport', async (id) => {
    const api = await import('./taxonomy')
    for (const invoke of [() => api.patchCategory(id, { name: 'Name' }, proof()), () => api.deleteCategory(id, proof()),
      () => api.patchTag(id, { name: 'Name' }, proof()), () => api.deleteTag(id, proof())]) {
      await expect(invoke()).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    }
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('does not treat imported new/tree IDs as static API routes and encodes identity without injection', async () => {
    const api = await import('./taxonomy')
    for (const id of ['new', 'tree', '保留 é #?%+&']) {
      fetchMock.mockResolvedValueOnce(reply({ ...category, id })).mockResolvedValueOnce(reply({ ...tag, id }))
      await api.patchCategory(id, { name: category.name }, proof())
      await api.patchTag(id, { name: tag.name }, proof())
    }
    for (const [index, [request]] of fetchMock.mock.calls.entries()) {
      const url = new URL((request as Request).url)
      expect(url.search).toBe('')
      expect(url.hash).toBe('')
      expect(decodeURIComponent(url.pathname.split('/').at(-1)!)).toBe(['new', 'tree', '保留 é #?%+&'][Math.floor(index / 2)])
    }
  })

  it('rejects invalid revisions, page scope and query fields before sending', async () => {
    const api = await import('./taxonomy')
    for (const expectedRevision of [0, -1, 1.1, Number.NaN, Number.POSITIVE_INFINITY, Number.MAX_SAFE_INTEGER + 1]) {
      for (const invoke of [() => api.patchCategory(category.id, { name: 'Name' }, { ...write(), expectedRevision }),
        () => api.deleteCategory(category.id, { ...write(), expectedRevision }),
        () => api.patchTag(tag.id, { name: 'Name' }, { ...write(), expectedRevision }), () => api.deleteTag(tag.id, { ...write(), expectedRevision })]) {
        await expect(invoke()).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
      }
    }
    for (const query of [{ limit: 0 }, { limit: 101 }, { limit: 1.1 }, { cursor: '' }, { cursor: '\uD800' }, { cursor: 'x'.repeat(2049) }, { limit: 1, purpose: 'other' }]) {
      await expect(api.fetchCategoriesPage(query, signal())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
      await expect(api.fetchTagsPage(query, signal())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    }
    expect(fetchMock).not.toHaveBeenCalled()
  })
})

describe('taxonomy safe failures, cancellation and no hidden replay', () => {
  it.each(['CATEGORY_CYCLE', 'CATEGORY_HAS_CHILDREN', 'TAG_NAME_CONFLICT', 'REVISION_CONFLICT', 'PRECONDITION_REQUIRED',
    'IDEMPOTENCY_CONFLICT', 'IDEMPOTENCY_EXPIRED', 'INVALID_CURSOR', 'INVALID_TAXONOMY_URL', 'PASSWORD_CHANGE_REQUIRED', 'AUTHENTICATION_REQUIRED'])('keeps safe %s metadata without diagnostics or replay', async (code) => {
    vi.useFakeTimers()
    const { patchCategory } = await import('./taxonomy')
    const payload = { error: { code, message: 'private credential', details: { path: '/private/database' }, request_id: 'safe-request' } }
    fetchMock.mockResolvedValue(Response.json(payload, { status: 409, headers: { 'Retry-After': '12' } }))
    const failure = await patchCategory(category.id, { name: 'Changed' }, proof()).catch((error: unknown) => error)
    expect(failure).toMatchObject({ code, status: 409, retryAfterSeconds: 12, requestId: 'safe-request' })
    expect(JSON.stringify(failure)).not.toMatch(/private|credential|message|details|cause|response/)
    window.dispatchEvent(new Event('online'))
    await vi.advanceTimersByTimeAsync(120_000)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('does not send pre-canceled mutations or synthesize a replacement key', async () => {
    const api = await import('./taxonomy')
    const controller = new AbortController()
    controller.abort('private reason')
    for (const invoke of [() => api.createCategory({ name: 'Root' }, { ...write(), signal: controller.signal }),
      () => api.createTag({ name: 'Tag' }, { ...write(), signal: controller.signal })]) {
      await expect(invoke()).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
    }
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('bounds both collections at eight-second reads and thirty-second writes without retry', async () => {
    vi.useFakeTimers()
    fetchMock.mockImplementation((input) => new Promise((_, reject) => {
      const requestSignal = (input as Request).signal
      const abort = () => reject(new DOMException('Aborted', 'AbortError'))
      if (requestSignal.aborted) abort()
      else requestSignal.addEventListener('abort', abort, { once: true })
    }))
    const api = await import('./taxonomy')
    const reads = [expect(api.fetchCategoriesPage({}, signal())).rejects.toMatchObject({ code: 'REQUEST_TIMEOUT' }),
      expect(api.fetchTagsPage({}, signal())).rejects.toMatchObject({ code: 'REQUEST_TIMEOUT' })]
    await vi.advanceTimersByTimeAsync(8_000)
    await Promise.all(reads)
    const writes = [expect(api.createCategory({ name: 'Root' }, write())).rejects.toMatchObject({ code: 'REQUEST_TIMEOUT' }),
      expect(api.createTag({ name: 'Tag' }, write())).rejects.toMatchObject({ code: 'REQUEST_TIMEOUT' })]
    await vi.advanceTimersByTimeAsync(30_000)
    await Promise.all(writes)
    window.dispatchEvent(new Event('online'))
    await vi.advanceTimersByTimeAsync(120_000)
    expect(fetchMock).toHaveBeenCalledTimes(4)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('discards a valid late response after cancellation and removes abort listeners', async () => {
    let release: ((response: Response) => void) | undefined
    fetchMock.mockImplementation(() => new Promise((resolve) => { release = resolve }))
    const controller = new AbortController()
    const remove = vi.spyOn(controller.signal, 'removeEventListener')
    const { patchTag } = await import('./taxonomy')
    const pending = patchTag(tag.id, { name: 'Changed' }, { ...proof(), signal: controller.signal })
    const rejected = expect(pending).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
    controller.abort()
    release?.(reply({ ...tag, name: 'Changed', revision: 2 }))
    await rejected
    expect(remove).toHaveBeenCalledWith('abort', expect.any(Function))
  })

  it('normalizes network/proxy failures without refreshing CSRF, replaying or persisting a write', async () => {
    vi.useFakeTimers()
    const { createCategory, createTag } = await import('./taxonomy')
    fetchMock.mockRejectedValueOnce(new TypeError('private transport'))
    await expect(createCategory({ name: 'Root' }, write())).rejects.toMatchObject({ code: 'NETWORK_ERROR', message: 'HomeBox API request failed' })
    fetchMock.mockResolvedValueOnce(new Response('<html>private proxy error</html>'))
    await expect(createTag({ name: 'Tag' }, write())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    await vi.advanceTimersByTimeAsync(120_000)
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(vi.getTimerCount()).toBe(0)
  })
})
