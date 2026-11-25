import { beforeEach, describe, expect, it, vi } from 'vitest'

const fetchMock = vi.fn<typeof fetch>()
const signal = () => new AbortController().signal
const event = { id: 'event-a', location_id: 'root', occurred_at: 'legacy\0時間', from_parent_id: null, to_parent_id: 'box/legacy' }
const page = { location_id: 'root', deleted: false, data: [event], next_cursor: null }
const reply = (value: unknown, etag: string | null = '"7"', status = 200) => Response.json(value, { status, headers: etag === null ? {} : { ETag: etag } })

beforeEach(() => { vi.resetModules(); fetchMock.mockReset(); vi.stubGlobal('fetch', fetchMock) })

describe('location movement pages', () => {
  it('reads one explicit path page and keeps exact historical values and the retained Location revision', async () => {
    fetchMock.mockResolvedValue(reply(page))
    const { fetchLocationMovements } = await import('./locations')
    expect(await fetchLocationMovements('root', {}, signal())).toEqual({ value: page, revision: 7 })
    const request = fetchMock.mock.calls[0]![0] as Request
    expect(request.url).toBe('http://localhost/api/v1/locations/root/movements')
    expect(request.method).toBe('GET'); expect(request.cache).toBe('no-store'); expect(request.credentials).toBe('same-origin')
    expect(await request.text()).toBe(''); expect(request.headers.has('If-Match')).toBe(false); expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it.each(['legacy/location\0😀', '\0', '.', '..', 'back\\slash', 'literal%2F', 'name+plus', 'space here', 'what?now', 'unité',
    'semi;colon', "quote'group", 'star*value', 'paren(value)', 'line\n'])('uses the exact query alias for owner %j', async (id) => {
    const value = { ...page, location_id: id, deleted: true, data: [{ ...event, location_id: id }], next_cursor: 'next' }
    fetchMock.mockResolvedValue(reply(value, '"9007199254740991"'))
    const { fetchLocationMovements } = await import('./locations')
    expect(await fetchLocationMovements(id, { limit: 1, cursor: 'first+/=' }, signal())).toEqual({ value, revision: Number.MAX_SAFE_INTEGER })
    const request = fetchMock.mock.calls[0]![0] as Request, url = new URL(request.url)
    expect(url.pathname).toBe('/api/v1/location-movements'); expect(url.searchParams.get('id')).toBe(id)
    expect(url.searchParams.get('limit')).toBe('1'); expect(url.searchParams.get('cursor')).toBe('first+/=')
  })

  it.each(['new', 'tree', 'id-._~'])('uses the unshadowed movement subpath for retained owner %j', async (id) => {
    const value = { ...page, location_id: id, data: [{ ...event, location_id: id }] }
    fetchMock.mockResolvedValue(reply(value))
    const { fetchLocationMovements } = await import('./locations')
    await fetchLocationMovements(id, {}, signal())
    const url = new URL((fetchMock.mock.calls[0]![0] as Request).url)
    expect(url.pathname).toBe(`/api/v1/locations/${encodeURIComponent(id)}/movements`)
  })

  it.each(['', '\ud800', 'x'.repeat(129)])('rejects invalid owner identity %j without a request', async (id) => {
    const { fetchLocationMovements } = await import('./locations')
    await expect(fetchLocationMovements(id, {}, signal())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it.each([{ limit: 0 }, { limit: 101 }, { limit: 1.5 }, { cursor: '' }, { cursor: '😀'.repeat(513) }, { cursor: '\ud800' }, { extra: 'secret' }])('rejects invalid paging %#', async (query) => {
    const { fetchLocationMovements } = await import('./locations')
    await expect(fetchLocationMovements('root', query as never, signal())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it.each([null, '', '7', 'W/"7"', '"0"', '"01"', '"7", "8"', '"9007199254740992"'])('rejects missing or noncanonical ETag %j', async (etag) => {
    fetchMock.mockResolvedValue(reply(page, etag))
    const { fetchLocationMovements } = await import('./locations')
    await expect(fetchLocationMovements('root', {}, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE', status: 200 })
  })

  it.each([
    null, {}, { ...page, location_id: 'foreign' }, { ...page, deleted: null }, { ...page, data: null }, { ...page, next_cursor: undefined },
    { ...page, next_cursor: '' }, { ...page, next_cursor: '😀'.repeat(513) }, { ...page, data: [event, event] },
    { ...page, data: [{ ...event, location_id: 'foreign' }] }, { ...page, data: [{ ...event, occurred_at: '' }] },
    { ...page, data: [{ ...event, occurred_at: 'x'.repeat(10001) }] }, { ...page, data: [{ ...event, from_parent_id: '' }] },
    { ...page, data: [{ ...event, id: 'x'.repeat(129) }] }, { ...page, extra: 'secret' },
    JSON.parse('{"location_id":"root","deleted":false,"data":[],"next_cursor":null,"__proto__":{"secret":true}}'),
    { ...page, data: [JSON.parse(JSON.stringify(event).replace(/}$/, ',"__proto__":{"secret":true}}'))] },
  ])('rejects malformed or foreign response %#', async (value) => {
    fetchMock.mockResolvedValue(reply(value))
    const { fetchLocationMovements } = await import('./locations')
    await expect(fetchLocationMovements('root', {}, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE', status: 200 })
  })

  it('checks SQLite UTF-8 byte ordering rather than JavaScript UTF-16 ordering', async () => {
    const { fetchLocationMovements } = await import('./locations')
    const bmp = { ...event, id: '' }, astral = { ...event, id: '😀' }
    fetchMock.mockResolvedValueOnce(reply({ ...page, data: [bmp, astral] }))
    expect((await fetchLocationMovements('root', {}, signal())).value.data.map((row) => row.id)).toEqual(['', '😀'])
    fetchMock.mockResolvedValueOnce(reply({ ...page, data: [astral, bmp] }))
    await expect(fetchLocationMovements('root', {}, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  })

  it('rejects oversized, incomplete or cyclic pages and does not silently retry', async () => {
    const { fetchLocationMovements } = await import('./locations')
    for (const value of [{ ...page, data: [event, { ...event, id: 'event-b' }, { ...event, id: 'event-c' }] },
      { ...page, data: [event], next_cursor: 'next' }, { ...page, data: [event, { ...event, id: 'event-b' }], next_cursor: 'same' }]) {
      fetchMock.mockResolvedValueOnce(reply(value))
      await expect(fetchLocationMovements('root', { limit: 2, cursor: 'same' }, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
    expect(fetchMock).toHaveBeenCalledTimes(3)
  })

  it('allows an explicit empty continuation and does not replay aborts or errors', async () => {
    const { fetchLocationMovements } = await import('./locations')
    const empty = { location_id: 'root', deleted: true, data: [], next_cursor: null }
    fetchMock.mockResolvedValueOnce(reply(empty, '"1"'))
    expect(await fetchLocationMovements('root', { cursor: 'prior' }, signal())).toEqual({ value: empty, revision: 1 })
    const controller = new AbortController()
    fetchMock.mockImplementationOnce((input) => new Promise((_, reject) => { (input as Request).signal.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')), { once: true }) }))
    const pending = fetchLocationMovements('root', {}, controller.signal); controller.abort()
    await expect(pending).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
    fetchMock.mockResolvedValueOnce(reply({ error: { code: 'SERVICE_UNAVAILABLE', request_id: 'safe-id', message: 'private' } }, null, 503))
    const error = await fetchLocationMovements('root', {}, signal()).catch((cause: unknown) => cause)
    expect(error).toMatchObject({ code: 'SERVICE_UNAVAILABLE', status: 503, requestId: 'safe-id' })
    expect(JSON.stringify(error)).not.toContain('private'); expect(fetchMock).toHaveBeenCalledTimes(3)
  })
})
