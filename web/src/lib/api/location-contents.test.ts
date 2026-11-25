import { beforeEach, describe, expect, it, vi } from 'vitest'

const fetchMock = vi.fn<typeof fetch>()
const signal = () => new AbortController().signal
const unit = { id: 'unit', item_id: 'individual', location_id: 'child', asset_code: 'AST-1', status: 'disposed', serial_number: null, notes: '', revision: 2 }
const stock = { item_id: 'quantity', location_id: 'child', quantity_base: 0, unit_code: 'g', base_unit: 'g', scale_factor: 1, revision: 4 }
const child = { id: 'child', parent_id: 'root', name: 'Child', code: 'CHILD', kind: 'box', status: 'active', sort_order: 0, description: '', revision: 3 }
const contents = { location_id: 'root', scope: 'subtree', units: [unit], stock: [stock], sub_locations: [child], next_cursor: null }

function reply(value: unknown, etag: string | null = '"7"', status = 200) {
  return Response.json(value, { status, headers: etag === null ? {} : { ETag: etag } })
}

beforeEach(() => { vi.resetModules(); fetchMock.mockReset(); vi.stubGlobal('fetch', fetchMock) })

describe('location contents live pages', () => {
  it('performs one scoped read and preserves actual rows and the Location aggregate revision', async () => {
    fetchMock.mockResolvedValue(reply(contents))
    const { fetchLocationContents } = await import('./locations')
    expect(await fetchLocationContents('root', {}, signal())).toEqual({ value: contents, revision: 7 })
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const request = fetchMock.mock.calls[0]![0] as Request
    expect(request.url).toBe('http://localhost/api/v1/locations/root/contents')
    expect(request.method).toBe('GET'); expect(request.cache).toBe('no-store')
    expect(request.credentials).toBe('same-origin'); expect(request.redirect).toBe('error')
    expect(await request.text()).toBe(''); expect(request.headers.has('If-Match')).toBe(false)
    expect(request.headers.has('Idempotency-Key')).toBe(false); expect(request.headers.has('X-CSRF-Token')).toBe(false)
  })

  it('keeps the requested direct scope, exact cursor and independent row identities', async () => {
    const value = { ...contents, scope: 'direct', units: [{ ...unit, location_id: 'root' }], stock: [], next_cursor: 'next-token' }
    fetchMock.mockResolvedValue(reply(value, '"9007199254740991"'))
    const { fetchLocationContents } = await import('./locations')
    expect(await fetchLocationContents('root', { scope: 'direct', limit: 2, cursor: 'first+/=' }, signal())).toEqual({ value, revision: Number.MAX_SAFE_INTEGER })
    const request = fetchMock.mock.calls[0]![0] as Request, url = new URL(request.url)
    expect(url.searchParams.get('scope')).toBe('direct'); expect(url.searchParams.get('limit')).toBe('2')
    expect(url.searchParams.get('cursor')).toBe('first+/=')
  })

  it.each(['', 'tree', '.', '..', 'a/b', 'a\\b', '\0', '\ud800', 'x'.repeat(129)])('rejects unsafe Location path %j before a request', async (id) => {
    const { fetchLocationContents } = await import('./locations')
    await expect(fetchLocationContents(id, {}, signal())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it.each([
    { scope: 'invalid' }, { limit: 0 }, { limit: 101 }, { limit: 1.5 }, { cursor: '' }, { cursor: 'x'.repeat(4097) },
    { cursor: '😀'.repeat(1025) }, { cursor: '\ud800' }, { extra: 'private' },
  ])('rejects an invalid query before fetching %#', async (query) => {
    const { fetchLocationContents } = await import('./locations')
    await expect(fetchLocationContents('root', query as never, signal())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it.each([null, '', '1', 'W/"7"', '"0"', '"01"', '"7", "8"', '"9007199254740992"'])('rejects a missing or invalid Location ETag %j', async (etag) => {
    fetchMock.mockResolvedValue(reply(contents, etag))
    const { fetchLocationContents } = await import('./locations')
    await expect(fetchLocationContents('root', {}, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE', status: 200 })
  })

  it.each([
    null, {}, { ...contents, location_id: 'elsewhere' }, { ...contents, scope: 'direct' },
    { ...contents, units: undefined }, { ...contents, stock: null }, { ...contents, sub_locations: undefined }, { ...contents, next_cursor: undefined },
    { ...contents, next_cursor: '' }, { ...contents, next_cursor: '😀'.repeat(1025) },
    { ...contents, units: [unit, unit] }, { ...contents, stock: [stock, stock] }, { ...contents, sub_locations: [child, child] },
    { ...contents, units: [{ ...unit, location_id: null }] }, { ...contents, sub_locations: [{ ...child, id: 'root', parent_id: 'elsewhere' }] },
    { ...contents, units: [{ ...unit, revision: 0 }] }, { ...contents, stock: [{ ...stock, quantity_base: -1 }] },
    { ...contents, extra: 'private' },
    JSON.parse('{"location_id":"root","scope":"subtree","units":[],"stock":[],"sub_locations":[],"next_cursor":null,"__proto__":{"secret":true}}'),
    { ...contents, units: [JSON.parse(JSON.stringify(unit).replace(/}$/, ',"__proto__":{"secret":true}}'))] },
    { ...contents, stock: [JSON.parse(JSON.stringify(stock).replace(/}$/, ',"__proto__":{"secret":true}}'))] },
    { ...contents, sub_locations: [JSON.parse(JSON.stringify(child).replace(/}$/, ',"__proto__":{"secret":true}}'))] },
  ])('rejects malformed or inconsistent page %#', async (value) => {
    fetchMock.mockResolvedValue(reply(value))
    const { fetchLocationContents } = await import('./locations')
    await expect(fetchLocationContents('root', {}, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE', status: 200 })
  })

  it.each([
    { ...contents, units: [{ ...unit, location_id: 'child' }], stock: [], sub_locations: [], scope: 'direct' },
    { ...contents, units: [], stock: [{ ...stock, location_id: 'child' }], sub_locations: [], scope: 'direct' },
    { ...contents, units: [], stock: [], sub_locations: [{ ...child, parent_id: 'other' }], scope: 'direct' },
  ])('rejects rows outside the direct scope %#', async (value) => {
    fetchMock.mockResolvedValue(reply(value))
    const { fetchLocationContents } = await import('./locations')
    await expect(fetchLocationContents('root', { scope: 'direct' }, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  })

  it('rejects oversized, incomplete or cyclic pages without automatic paging', async () => {
    const { fetchLocationContents } = await import('./locations')
    for (const value of [contents, { ...contents, units: [unit], stock: [], sub_locations: [], next_cursor: 'next' },
      { ...contents, units: [unit], stock: [stock], sub_locations: [], next_cursor: 'same' }]) {
      fetchMock.mockResolvedValueOnce(reply(value))
      await expect(fetchLocationContents('root', { limit: 2, cursor: 'same' }, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
    expect(fetchMock).toHaveBeenCalledTimes(3)
  })

  it('allows an explicit empty terminal page and never retries an abort or server error', async () => {
    const { fetchLocationContents } = await import('./locations')
    const empty = { location_id: 'root', scope: 'subtree', units: [], stock: [], sub_locations: [], next_cursor: null }
    fetchMock.mockResolvedValueOnce(reply(empty, '"1"'))
    expect(await fetchLocationContents('root', { cursor: 'prior' }, signal())).toEqual({ value: empty, revision: 1 })
    const controller = new AbortController()
    fetchMock.mockImplementationOnce((input) => new Promise((_, reject) => {
      (input as Request).signal.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')), { once: true })
    }))
    const pending = fetchLocationContents('root', {}, controller.signal)
    controller.abort()
    await expect(pending).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
    fetchMock.mockResolvedValueOnce(reply({ error: { code: 'SERVICE_UNAVAILABLE', request_id: 'safe-id', message: 'private detail' } }, null, 503))
    const error = await fetchLocationContents('root', {}, signal()).catch((cause: unknown) => cause)
    expect(error).toMatchObject({ code: 'SERVICE_UNAVAILABLE', status: 503, requestId: 'safe-id' })
    expect(JSON.stringify(error)).not.toContain('private detail')
    expect(fetchMock).toHaveBeenCalledTimes(3)
  })
})
