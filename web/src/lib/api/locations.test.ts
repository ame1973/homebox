import { beforeEach, describe, expect, it, vi } from 'vitest'
import { version } from '../../../package.json'

type LocationAPI = typeof import('./locations')
const fetchMock = vi.fn<typeof fetch>()
const location = { id: 'location-1', revision: 1, parent_id: null, name: '  書房 📦 é  ', code: 'BOX-1', kind: 'other', status: 'active', sort_order: -2, description: '<notes>\n' }
const updated = { ...location, revision: 2, name: 'Updated' }
const csrf = 'A'.repeat(43)
const signal = () => new AbortController().signal
const write = () => ({ csrfToken: csrf, idempotencyKey: 'explicit-location-key', signal: signal() })
const proof = () => ({ ...write(), expectedRevision: 1 })
const rootBody = { name: location.name, code: location.code }

function reply(data: unknown, status = 200, etag?: string): Response {
  const revision = data && typeof data === 'object' && 'revision' in data ? data.revision : undefined
  return Response.json(data, { status, headers: etag === undefined && typeof revision !== 'number' ? {} : { ETag: etag ?? `"${String(revision)}"` } })
}

beforeEach(() => {
  vi.resetModules()
  fetchMock.mockReset()
  vi.stubGlobal('fetch', fetchMock)
})

function abortableFetch() {
  fetchMock.mockImplementation((input) => new Promise((_, reject) => {
    const requestSignal = (input as Request).signal
    const abort = () => reject(new DOMException('Aborted', 'AbortError'))
    if (requestSignal.aborted) abort()
    else requestSignal.addEventListener('abort', abort, { once: true })
  }))
}

describe('typed location transport', () => {
  const cases: { name: string; method: string; path: string; body?: unknown; result: unknown; revision?: number; invoke: (api: LocationAPI) => Promise<unknown> }[] = [
    { name: 'page', method: 'GET', path: '/locations/tree?limit=2&cursor=opaque', result: { data: [location], next_cursor: null }, invoke: (api) => api.fetchLocationsPage({ limit: 2, cursor: 'opaque' }, signal()) },
    { name: 'detail', method: 'GET', path: '/locations/location-1', result: location, invoke: (api) => api.fetchLocation('location-1', signal()) },
    { name: 'root creation', method: 'POST', path: '/locations', body: rootBody, result: location, invoke: (api) => api.createLocation(rootBody, write()) },
    { name: 'child creation', method: 'POST', path: '/locations', body: { ...rootBody, parent_id: 'parent' }, result: { ...location, parent_id: 'parent' }, revision: 7, invoke: (api) => api.createLocation({ ...rootBody, parent_id: 'parent' }, { ...write(), expectedRevision: 7 }) },
    { name: 'patch', method: 'PATCH', path: '/locations/location-1', body: { name: 'Updated' }, result: updated, revision: 1, invoke: (api) => api.patchLocation('location-1', { name: 'Updated' }, proof()) },
    { name: 'move', method: 'POST', path: '/locations/location-1/move', body: { target_parent_id: null }, result: location, revision: 1, invoke: (api) => api.moveLocation('location-1', { target_parent_id: null }, proof()) },
    { name: 'archive', method: 'POST', path: '/locations/location-1/archive', body: { archive_children: true, reason: 'Reviewed' }, result: { ...location, revision: 2, status: 'archived' }, revision: 1, invoke: (api) => api.archiveLocation('location-1', { archive_children: true, reason: 'Reviewed' }, proof()) },
    { name: 'delete', method: 'DELETE', path: '/locations/location-1', result: { ok: true, operation_id: 'event-1', revision: 2 }, revision: 1, invoke: (api) => api.deleteLocation('location-1', proof()) },
  ]
  it.each(cases)('sends exactly one explicit $name request', async (test) => {
    fetchMock.mockResolvedValue(reply(test.result))
    const api = await import('./locations')
    expect(await test.invoke(api)).toEqual(test.result)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const request = fetchMock.mock.calls[0]?.[0] as Request
    const actualURL = new URL(request.url)
    const expectedURL = new URL(`http://localhost/api/v1${test.path}`)
    actualURL.searchParams.sort()
    expectedURL.searchParams.sort()
    expect(actualURL.href).toBe(expectedURL.href)
    expect(request.method).toBe(test.method)
    expect(request.cache).toBe('no-store')
    expect(request.credentials).toBe('same-origin')
    expect(request.redirect).toBe('error')
    expect(request.headers.has('Cookie')).toBe(false)
    expect(request.headers.has('Authorization')).toBe(false)
    expect(request.headers.get('Origin')).toBe(test.method === 'GET' ? null : 'http://localhost')
    expect(request.headers.get('X-CSRF-Token')).toBe(test.method === 'GET' ? null : csrf)
    expect(request.headers.get('Idempotency-Key')).toBe(test.method === 'GET' ? null : 'explicit-location-key')
    expect(request.headers.get('X-HomeBox-Client-Version')).toBe(test.method === 'GET' ? null : version)
    expect(request.headers.get('If-Match')).toBe(test.revision === undefined ? null : `"${test.revision}"`)
    if (test.body === undefined) {
      expect(await request.text()).toBe('')
      expect(request.headers.has('Content-Type')).toBe(false)
    } else expect(await request.json()).toEqual(test.body)
  })

  it('uses explicit pages without following cursors or injecting query parameters', async () => {
    fetchMock.mockResolvedValue(reply({ data: [location], next_cursor: 'next-page' }))
    const { fetchLocationsPage } = await import('./locations')
    const page = await fetchLocationsPage({ limit: 1, cursor: 'opaque?x=1&limit=99+#' }, signal())
    expect(page.next_cursor).toBe('next-page')
    const request = fetchMock.mock.calls[0]?.[0] as Request
    const url = new URL(request.url)
    expect(url.searchParams.get('cursor')).toBe('opaque?x=1&limit=99+#')
    expect(url.searchParams.getAll('limit')).toEqual(['1'])
    expect(url.searchParams.has('x')).toBe(false)
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('preserves the exact original key, body, and stale precondition on explicit retries', async () => {
    fetchMock.mockImplementation(async () => reply(updated))
    const { patchLocation } = await import('./locations')
    const context = proof()
    const body = { name: 'Updated', description: '  原樣 é  ' }
    expect(await patchLocation(location.id, body, context)).toEqual(updated)
    expect(await patchLocation(location.id, body, context)).toEqual(updated)
    expect(fetchMock).toHaveBeenCalledTimes(2)
    const requests = fetchMock.mock.calls.map(([request]) => request as Request)
    expect(requests[1]?.headers.get('If-Match')).toBe('"1"')
    expect(requests[1]?.headers.get('Idempotency-Key')).toBe(requests[0]?.headers.get('Idempotency-Key'))
    expect(await requests[1]!.text()).toBe(await requests[0]!.text())
  })

  it('does not re-read a mutable deletion proof after sending', async () => {
    let release: ((response: Response) => void) | undefined
    fetchMock.mockImplementation(() => new Promise((resolve) => { release = resolve }))
    const { deleteLocation } = await import('./locations')
    const context = proof()
    const result = deleteLocation(location.id, context)
    context.expectedRevision = 99
    context.idempotencyKey = 'changed-after-send'
    release?.(reply({ ok: true, operation_id: 'event', revision: 2 }))
    expect(await result).toMatchObject({ revision: 2 })
    const request = fetchMock.mock.calls[0]?.[0] as Request
    expect(request.headers.get('If-Match')).toBe('"1"')
    expect(request.headers.get('Idempotency-Key')).toBe('explicit-location-key')
  })

  it('never reads or persists credentials, query data, cursors, or pending writes', async () => {
    const spies = [vi.spyOn(window.localStorage, 'getItem'), vi.spyOn(window.localStorage, 'setItem'),
      vi.spyOn(window.sessionStorage, 'getItem'), vi.spyOn(window.sessionStorage, 'setItem'),
      vi.spyOn(document, 'cookie', 'get'), vi.spyOn(document, 'cookie', 'set')]
    const database = vi.fn()
    const cache = vi.fn()
    vi.stubGlobal('indexedDB', { open: database })
    vi.stubGlobal('caches', { open: cache })
    fetchMock.mockImplementation(async () => reply(location))
    const { createLocation } = await import('./locations')
    await createLocation(rootBody, write())
    for (const spy of [...spies, database, cache]) expect(spy).not.toHaveBeenCalled()
  })
})

describe('runtime location response invariants', () => {
  it.each([
    null, {}, { ...location, revision: 0 }, { ...location, revision: 1.1 },
    { ...location, revision: Number.MAX_SAFE_INTEGER + 1 }, { ...location, id: '' },
    { ...location, parent_id: location.id }, { ...location, name: '' }, { ...location, name: '\uD800' },
    { ...location, name: '📦'.repeat(201) }, { ...location, kind: '' }, { ...location, kind: 'k'.repeat(33) },
    { ...location, sort_order: 0.1 }, { ...location, sort_order: Number.MAX_SAFE_INTEGER + 1 },
    { ...location, status: 'deleted' }, { ...location, description: null },
    { ...location, description: 'd'.repeat(10001) }, { ...location, session_token: 'not allowed' },
  ])('rejects invalid or secret-bearing resource %j', async (data) => {
    fetchMock.mockResolvedValue(reply(data))
    const { fetchLocation } = await import('./locations')
    await expect(fetchLocation(location.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE', status: 200 })
  })

  it.each(['', '1', 'W/"1"', '"2"'])('requires a matching strong response ETag %s', async (etag) => {
    fetchMock.mockResolvedValue(reply(location, 200, etag))
    const { fetchLocation } = await import('./locations')
    await expect(fetchLocation(location.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  })

  it('rejects a missing ETag, another resource, or a mutation response outside the committed revision range', async () => {
    const api = await import('./locations')
    fetchMock.mockResolvedValueOnce(Response.json(location))
    await expect(api.fetchLocation(location.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    fetchMock.mockResolvedValueOnce(reply({ ...location, id: 'wrong-location' }))
    await expect(api.fetchLocation(location.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    fetchMock.mockResolvedValueOnce(reply({ ...updated, revision: 3 }))
    await expect(api.patchLocation(location.id, { name: 'Updated' }, proof())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    fetchMock.mockResolvedValueOnce(reply(updated))
    await expect(api.createLocation(rootBody, write())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  })

  it.each([201, 204])('rejects unexpected success status %i', async (status) => {
    fetchMock.mockResolvedValue(status === 204 ? new Response(null, { status }) : reply(location, status))
    const { fetchLocation } = await import('./locations')
    await expect(fetchLocation(location.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE', status })
  })

  it.each([
    { data: null }, { data: [location, location] }, { data: [location], next_cursor: '' },
    { data: [location], next_cursor: 'x'.repeat(2049) }, { data: [location], next_cursor: null, extra: true },
    { data: [location, { ...location, id: 'location-2' }], next_cursor: null },
  ])('rejects invalid, duplicate, or oversized page %j', async (data) => {
    fetchMock.mockResolvedValue(reply(data))
    const { fetchLocationsPage } = await import('./locations')
    await expect(fetchLocationsPage({ limit: 1 }, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  })

  it('accepts a default empty page and safe Unicode/integer boundaries without normalization', async () => {
    const api = await import('./locations')
    fetchMock.mockResolvedValueOnce(reply({ data: [] }))
    expect(await api.fetchLocationsPage({}, signal())).toEqual({ data: [] })
    const data = { ...location, name: '📦'.repeat(200), code: '📦'.repeat(100), kind: '📦'.repeat(32),
      description: '📦'.repeat(10000), revision: Number.MAX_SAFE_INTEGER, sort_order: -Number.MAX_SAFE_INTEGER }
    fetchMock.mockResolvedValueOnce(reply(data))
    expect(await api.fetchLocation(location.id, signal())).toEqual(data)
  })

  it.each([{ ok: false, operation_id: 'event', revision: 2 }, { ok: true, operation_id: 'event' },
    { ok: true, operation_id: '', revision: 2 }, { ok: true, operation_id: 'event', revision: 1 },
    { ok: true, operation_id: 'event', revision: 3 }, { ok: true, operation_id: 'event', revision: 2, extra: true }])('validates the exact deletion acknowledgement %j', async (data) => {
    fetchMock.mockResolvedValue(reply(data))
    const { deleteLocation } = await import('./locations')
    await expect(deleteLocation(location.id, proof())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  })
})

describe('caller-owned validation and error reconciliation', () => {
  it.each(['REVISION_CONFLICT', 'PRECONDITION_REQUIRED', 'IDEMPOTENCY_EXPIRED', 'LOCATION_CODE_CONFLICT', 'LOCATION_CYCLE',
    'LOCATION_ARCHIVED', 'LOCATION_HAS_CHILDREN', 'LOCATION_IN_USE', 'INVALID_REVISION', 'INVALID_QUERY', 'INVALID_CURSOR', 'INVALID_LOCATION_URL'])('keeps safe %s metadata but never diagnostics or an automatic write replay', async (code) => {
    vi.useFakeTimers()
    const privateText = 'private server path or submitted credential'
    fetchMock.mockResolvedValue(Response.json({ error: { code, message: privateText, details: { password: privateText }, request_id: 'request-123' } }, { status: 409, headers: { 'Retry-After': '12' } }))
    const { patchLocation } = await import('./locations')
    const result = patchLocation(location.id, { name: 'Updated' }, proof())
    await expect(result).rejects.toMatchObject({ code, status: 409, retryAfterSeconds: 12, requestId: 'request-123' })
    await result.catch((error: unknown) => {
      expect(String(error)).not.toContain(privateText)
      expect(JSON.stringify(error)).not.toContain(privateText)
      expect(error).not.toHaveProperty('cause')
      expect(error).not.toHaveProperty('response')
    })
    window.dispatchEvent(new Event('online'))
    await vi.advanceTimersByTimeAsync(120_000)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(vi.getTimerCount()).toBe(0)
  })

  it.each(['', '.', '..', 'tree', 'a/b', 'a\\b', 'a\0b', 'x'.repeat(129), '\uD800'])('rejects unsafe or unsupported resource path %j without a request', async (id) => {
    const { fetchLocation } = await import('./locations')
    await expect(fetchLocation(id, signal())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('validates bodies, query scopes, and required parent proofs before sending', async () => {
    const api = await import('./locations')
    const invalidCreate = api.createLocation as unknown as (body: unknown, context: unknown) => Promise<unknown>
    for (const body of [{ name: '', code: 'BOX' }, { ...rootBody, kind: '' }, { ...rootBody, sort_order: 1.1 }, { ...rootBody, extra: true }]) {
      await expect(invalidCreate(body, write())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    }
    await expect(invalidCreate({ ...rootBody, parent_id: 'parent' }, write())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    await expect(invalidCreate(rootBody, proof())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    for (const expectedRevision of [0, -1, 1.1, Number.MAX_SAFE_INTEGER + 1, Number.NaN]) {
      await expect(api.patchLocation(location.id, { name: 'Updated' }, { ...write(), expectedRevision })).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    }
    await expect(api.patchLocation(location.id, {}, proof())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    await expect(api.patchLocation(location.id, { name: undefined }, proof())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    await expect(api.fetchLocationsPage({ limit: 0 }, signal())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    await expect(api.fetchLocationsPage({ cursor: '' }, signal())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    expect(fetchMock).not.toHaveBeenCalled()
  })
})

describe('location cancellation and no hidden retry', () => {
  it('does not send a pre-canceled mutation', async () => {
    const controller = new AbortController()
    controller.abort('private reason')
    const { createLocation } = await import('./locations')
    await expect(createLocation(rootBody, { ...write(), signal: controller.signal })).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('bounds reads at eight seconds and writes at thirty seconds', async () => {
    vi.useFakeTimers()
    abortableFetch()
    const { fetchLocation, patchLocation } = await import('./locations')
    const read = expect(fetchLocation(location.id, signal())).rejects.toMatchObject({ code: 'REQUEST_TIMEOUT' })
    await vi.advanceTimersByTimeAsync(8_000)
    await read
    const write = expect(patchLocation(location.id, { name: 'Updated' }, proof())).rejects.toMatchObject({ code: 'REQUEST_TIMEOUT' })
    await vi.advanceTimersByTimeAsync(29_999)
    expect(fetchMock).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(1)
    await write
    await vi.advanceTimersByTimeAsync(120_000)
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('discards even a valid late response after cancellation and clears listeners', async () => {
    let release: ((response: Response) => void) | undefined
    fetchMock.mockImplementation(() => new Promise((resolve) => { release = resolve }))
    const controller = new AbortController()
    const remove = vi.spyOn(controller.signal, 'removeEventListener')
    const { patchLocation } = await import('./locations')
    const pending = patchLocation(location.id, { name: 'Updated' }, { ...proof(), signal: controller.signal })
    const rejected = expect(pending).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
    controller.abort()
    release?.(reply(updated))
    await rejected
    expect(remove).toHaveBeenCalledWith('abort', expect.any(Function))
  })

  it('normalizes network/invalid-response failures without inventing a new key or reconciling writes automatically', async () => {
    vi.useFakeTimers()
    const { createLocation } = await import('./locations')
    fetchMock.mockRejectedValueOnce(new TypeError('private host details'))
    await expect(createLocation(rootBody, write())).rejects.toMatchObject({ code: 'NETWORK_ERROR', message: 'HomeBox API request failed' })
    fetchMock.mockResolvedValueOnce(new Response('<html>private proxy output</html>'))
    await expect(createLocation(rootBody, write())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    await vi.advanceTimersByTimeAsync(120_000)
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(vi.getTimerCount()).toBe(0)
  })
})
