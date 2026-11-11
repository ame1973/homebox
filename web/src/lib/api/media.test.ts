import { beforeEach, describe, expect, it, vi } from 'vitest'

type MediaAPI = typeof import('./media')
const fetchMock = vi.fn<typeof fetch>()
const signal = () => new AbortController().signal
const digest = 'a'.repeat(64)
const image = { id: 'photo', revision: 7, sha256: digest, mime_type: 'image/heic', verification: 'unverified', width: 10, height: 20,
  bytes: 30, thumb_url: null, thumb_sha256: null } as const
const verified = { ...image, mime_type: 'image/png', verification: 'verified' } as const
const link = { id: 'link-1', revision: 3, media_id: image.id, owner_type: 'item', owner_id: 'item-1', is_cover: true, sort_order: -3,
  description: '\0Exact é\n', media: image } as const
const page = { data: [link], next_cursor: null }
const reply = (data: unknown, etag?: string, status = 200) => Response.json(data, { status, headers: etag === undefined ? {} : { ETag: etag } })
const request = () => fetchMock.mock.lastCall?.[0] as Request

beforeEach(() => { vi.resetModules(); fetchMock.mockReset(); vi.stubGlobal('fetch', fetchMock) })

describe('protected stateless Media reads', () => {
  const cases: { name: string; path: string; value: unknown; etag?: string; invoke: (api: MediaAPI) => Promise<unknown> }[] = [
    { name: 'metadata', path: '/media/photo', value: image, etag: '"7"', invoke: (api) => api.fetchMedia('photo', signal()) },
    { name: 'reserved metadata', path: '/media/links/view', value: { ...image, id: 'links' }, etag: '"7"', invoke: (api) => api.fetchMedia('links', signal()) },
    { name: 'exact metadata', path: '/media-by-identity?id=legacy%2Fphoto', value: { ...image, id: 'legacy/photo' }, etag: '"7"', invoke: (api) => api.fetchMedia('legacy/photo', signal()) },
    { name: 'Media membership', path: '/media/photo/links', value: page, invoke: (api) => api.fetchMediaLinksPage('photo', {}, signal()) },
    { name: 'exact Media membership', path: '/media-by-identity/links?id=legacy%2Fphoto', value: { data: [], next_cursor: null }, invoke: (api) => api.fetchMediaLinksPage('legacy/photo', {}, signal()) },
    { name: 'owner membership', path: '/media/links?owner_type=item&owner_id=item-1', value: page, invoke: (api) => api.fetchOwnerMediaLinksPage('item', 'item-1', {}, signal()) },
  ]
  it.each(cases)('performs one $name read with no private credentials, cache or defaults', async (test) => {
    fetchMock.mockResolvedValue(reply(test.value, test.etag))
    const api = await import('./media')
    expect(await test.invoke(api)).toEqual(test.value)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const sent = request()
    expect(sent.url).toBe(`http://localhost/api/v1${test.path}`)
    expect(sent.method).toBe('GET')
    expect(sent.cache).toBe('no-store'); expect(sent.credentials).toBe('same-origin'); expect(sent.redirect).toBe('error')
    for (const header of ['Cookie', 'Authorization', 'Origin', 'X-CSRF-Token', 'Idempotency-Key', 'If-Match', 'Content-Type']) expect(sent.headers.has(header)).toBe(false)
    expect(await sent.text()).toBe('')
  })

  it('encodes scope, query-only native NUL, Unicode IDs and UTF-8 cursor bytes without repair', async () => {
    const api = await import('./media')
    fetchMock.mockImplementation(async () => reply({ data: [], next_cursor: null }))
    const cursor = '😀'.repeat(512)
    for (const owner of ['new', 'tree', '物品 +?#', 'NUL\0owner']) {
      await api.fetchOwnerMediaLinksPage('location', owner, { limit: 1, cursor }, signal())
      const url = new URL(request().url)
      expect(url.pathname).toBe('/api/v1/media/links')
      expect(url.searchParams.getAll('owner_type')).toEqual(['location'])
      expect(url.searchParams.getAll('owner_id')).toEqual([owner])
      expect(url.searchParams.getAll('limit')).toEqual(['1'])
      expect(url.searchParams.getAll('cursor')).toEqual([cursor])
    }
    for (const id of ['new', 'tree', '照片 +?#']) {
      await api.fetchMediaLinksPage(id, { cursor: 'opaque?x=1&limit=99+#' }, signal())
      const url = new URL(request().url)
      expect(decodeURIComponent(url.pathname)).toBe(`/api/v1/media/${id}/links`)
      expect(url.searchParams.getAll('cursor')).toEqual(['opaque?x=1&limit=99+#'])
      expect(url.searchParams.has('limit')).toBe(false)
    }
    await api.fetchMediaLinksPage('links', {}, signal())
    expect(new URL(request().url).pathname).toBe('/api/v1/media/links/links')
    expect(fetchMock).toHaveBeenCalledTimes(8)
  })

  it.each(['.', '..', 'legacy/photo', 'legacy\\photo', '\0', 'legacy/\0😀\r\n', 'percent%2F/plus+ space?&=##', '😀'.repeat(127) + '/'])('reads exact retained Media identity %j without repairing it', async (id) => {
    const api = await import('./media'), selected = { ...image, id }, joined = { ...link, media_id: id, media: selected }
    fetchMock.mockResolvedValueOnce(reply(selected, '"7"'))
    expect(await api.fetchMedia(id, signal())).toEqual(selected)
    let url = new URL(request().url)
    expect(url.pathname).toBe('/api/v1/media-by-identity')
    expect([...url.searchParams]).toEqual([['id', id]])
    const cursor = '😀'.repeat(512)
    fetchMock.mockResolvedValueOnce(reply({ data: [joined], next_cursor: 'next' }))
    expect(await api.fetchMediaLinksPage(id, { limit: 1, cursor }, signal())).toEqual({ data: [joined], next_cursor: 'next' })
    url = new URL(request().url)
    expect(url.pathname).toBe('/api/v1/media-by-identity/links')
    expect(url.searchParams.getAll('id')).toEqual([id]); expect(url.searchParams.getAll('cursor')).toEqual([cursor])
    expect(url.searchParams.getAll('limit')).toEqual(['1']); expect(url.search.length - 1).toBeLessThanOrEqual(16_384)
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it('withholds an exact-identity response with a different Media or wrong independent ETag', async () => {
    const api = await import('./media'), id = 'legacy/\0photo', selected = { ...image, id }
    fetchMock.mockResolvedValueOnce(reply({ ...selected, id: 'other' }, '"7"'))
    await expect(api.fetchMedia(id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    fetchMock.mockResolvedValueOnce(reply(selected, 'W/"7"'))
    await expect(api.fetchMedia(id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    fetchMock.mockResolvedValueOnce(reply({ data: [{ ...link, media_id: id, media: image }], next_cursor: null }))
    await expect(api.fetchMediaLinksPage(id, {}, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    fetchMock.mockResolvedValueOnce(reply({ data: [], next_cursor: null }, '"7"'))
    await expect(api.fetchMediaLinksPage(id, {}, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  })

  it.each(cases)('does not retry a failed $name on reconnect', async (test) => {
    fetchMock.mockRejectedValue(new TypeError('private transport diagnostic'))
    const api = await import('./media')
    await expect(test.invoke(api)).rejects.toMatchObject({ code: 'NETWORK_ERROR' })
    window.dispatchEvent(new Event('online'))
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it.each(cases)('bounds the $name deadline and honors cancellation', async (test) => {
    vi.useFakeTimers()
    try {
      fetchMock.mockImplementation((input) => new Promise((_resolve, reject) => {
        (input as Request).signal.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')), { once: true })
      }))
      const api = await import('./media')
      const pending = expect(test.invoke(api)).rejects.toMatchObject({ code: 'REQUEST_TIMEOUT' })
      await vi.advanceTimersByTimeAsync(7999)
      expect(request().signal.aborted).toBe(false)
      await vi.advanceTimersByTimeAsync(1); await pending
      expect(fetchMock).toHaveBeenCalledTimes(1); expect(vi.getTimerCount()).toBe(0)
    } finally { vi.useRealTimers() }
  })

  it('honors an already-aborted or later-aborted caller without replay', async () => {
    const api = await import('./media')
    for (const read of [
      (abort: AbortSignal) => api.fetchMedia('photo', abort),
      (abort: AbortSignal) => api.fetchMediaLinksPage('photo', {}, abort),
      (abort: AbortSignal) => api.fetchOwnerMediaLinksPage('item', 'item-1', {}, abort),
    ]) {
      const early = new AbortController()
      early.abort()
      await expect(read(early.signal)).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
      fetchMock.mockImplementationOnce((input) => new Promise((_resolve, reject) => {
        (input as Request).signal.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')), { once: true })
      }))
      const later = new AbortController()
      const pending = expect(read(later.signal)).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
      later.abort()
      await pending
    }
    expect(fetchMock).toHaveBeenCalledTimes(3)
  })

  it('retains no credential, replica, cache or draft storage', async () => {
    const spies = [vi.spyOn(localStorage, 'getItem'), vi.spyOn(localStorage, 'setItem'), vi.spyOn(sessionStorage, 'getItem'),
      vi.spyOn(sessionStorage, 'setItem'), vi.spyOn(document, 'cookie', 'get'), vi.spyOn(document, 'cookie', 'set')]
    const database = vi.fn(), cache = vi.fn()
    vi.stubGlobal('indexedDB', { open: database }); vi.stubGlobal('caches', { open: cache })
    const api = await import('./media')
    fetchMock.mockResolvedValue(reply(image, '"7"'))
    await api.fetchMedia('photo', signal())
    for (const spy of [...spies, database, cache]) expect(spy).not.toHaveBeenCalled()
  })
})

describe('Media response and owner isolation', () => {
  it('keeps retained claims and independently validates verified thumbnail identity', async () => {
    const api = await import('./media')
    for (const result of [image, verified, { ...verified, id: 'legacy/\0photo' }, { ...verified, thumb_url: '/api/v1/media/photo/thumbnail', thumb_sha256: digest },
      { ...verified, id: 'a@b', thumb_url: '/api/v1/media/a%40b/thumbnail', thumb_sha256: digest },
      { ...verified, id: 'a@b', thumb_url: '/api/v1/media/a@b/thumbnail', thumb_sha256: digest }]) {
      fetchMock.mockResolvedValueOnce(reply(result, '"7"'))
      expect(await api.fetchMedia(result.id, signal())).toEqual(result)
    }
  })

  it('does not accept a claimed path thumbnail for a path-unaddressable Media ID', async () => {
    const api = await import('./media'), id = 'legacy/photo'
    fetchMock.mockResolvedValueOnce(reply({ ...verified, id, thumb_url: '/api/v1/media/legacy%2Fphoto/thumbnail', thumb_sha256: digest }, '"7"'))
    await expect(api.fetchMedia(id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('rejects malformed or unsafe claimed Media without exposing the response', async () => {
    const api = await import('./media')
    const bad = [
      { ...image, id: 'another' }, { ...image, revision: 0 }, { ...image, revision: Number.MAX_SAFE_INTEGER + 1 },
      { ...image, sha256: 'A'.repeat(64) }, { ...image, sha256: `${digest}\n` }, { ...image, mime_type: '' },
      { ...image, mime_type: '\uD800' }, { ...image, width: 0 }, { ...image, height: 1.5 }, { ...image, bytes: 0 },
      { ...image, verification: 'unknown' }, { ...image, verification: 'verified' }, { ...image, thumb_url: '/api/v1/media/photo/thumbnail', thumb_sha256: digest },
      { ...verified, thumb_url: '/api/v1/media/photo/thumbnail' }, { ...verified, thumb_sha256: digest },
      { ...verified, thumb_url: '/api/v1/media/other/thumbnail', thumb_sha256: digest },
      { ...verified, thumb_url: '//attacker.invalid/api/v1/media/photo/thumbnail', thumb_sha256: digest },
      { ...verified, thumb_url: '/api/v1/media/photo/thumbnail?x=1', thumb_sha256: digest },
      { ...verified, thumb_url: '/api/v1/media/%FF/thumbnail', thumb_sha256: digest },
      { ...verified, thumb_url: '/api/v1/media/photo/thumbnail', thumb_sha256: 'secret' },
      { ...image, storage_key: 'private' }, { ...image, raw_ocr: 'unreviewed' },
    ]
    for (const value of bad) {
      fetchMock.mockResolvedValueOnce(reply(value, '"7"'))
      await expect(api.fetchMedia('photo', signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
    for (const etag of [undefined, 'W/"7"', '"8"', '7']) {
      fetchMock.mockResolvedValueOnce(reply(image, etag))
      await expect(api.fetchMedia('photo', signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
  })

  it('rejects malformed identities and queries before fetching', async () => {
    const api = await import('./media')
    for (const id of ['', '😀'.repeat(129), '\uD800']) {
      await expect(api.fetchMedia(id, signal())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
      await expect(api.fetchMediaLinksPage(id, {}, signal())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    }
    for (const id of ['', '😀'.repeat(129), '\uD800']) await expect(api.fetchOwnerMediaLinksPage('item', id, {}, signal())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    await expect(api.fetchOwnerMediaLinksPage('purchase' as never, 'owner', {}, signal())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    for (const query of [{ limit: 0 }, { limit: 101 }, { cursor: '' }, { cursor: '😀'.repeat(512) + 'x' }, { cursor: '\uD800' }, { owner_id: 'other' }]) {
      await expect(api.fetchMediaLinksPage('photo', query as never, signal())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
      await expect(api.fetchMediaLinksPage('legacy/photo', query as never, signal())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
      await expect(api.fetchOwnerMediaLinksPage('item', 'item-1', query as never, signal())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    }
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('withholds incomplete, wrong-scope, mismatched, duplicated or falsely ETagged pages', async () => {
    const api = await import('./media')
    const invalid = [
      {}, { data: [link] }, { data: [link], next_cursor: '' }, { data: [link], next_cursor: '😀'.repeat(512) + 'x' },
      { data: [link], next_cursor: 'nonterminal' }, { data: [link, link], next_cursor: null },
      { data: [link, { ...link, id: 'link-2', media: { ...image, revision: 8 } }], next_cursor: null },
      { data: [{ ...link, media_id: 'other' }], next_cursor: null }, { data: [{ ...link, media: { ...image, id: 'other' } }], next_cursor: null },
      { data: [{ ...link, owner_type: 'unit' }], next_cursor: null }, { data: [{ ...link, owner_id: 'other' }], next_cursor: null },
      { data: [{ ...link, revision: 0 }], next_cursor: null }, { data: [{ ...link, sort_order: Number.MAX_SAFE_INTEGER + 1 }], next_cursor: null },
      { data: [{ ...link, description: '\uD800' }], next_cursor: null }, { data: [{ ...link, private_key: 'secret' }], next_cursor: null },
    ]
    for (const value of invalid) {
      fetchMock.mockResolvedValueOnce(reply(value))
      await expect(api.fetchOwnerMediaLinksPage('item', 'item-1', {}, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
    fetchMock.mockResolvedValueOnce(reply(page, '"3"'))
    await expect(api.fetchOwnerMediaLinksPage('item', 'item-1', {}, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    fetchMock.mockResolvedValueOnce(reply({ data: [{ ...link, media_id: 'other', media: { ...image, id: 'other' } }], next_cursor: null }))
    await expect(api.fetchMediaLinksPage('photo', {}, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    fetchMock.mockResolvedValueOnce(reply({ data: [link], next_cursor: 'same' }))
    await expect(api.fetchMediaLinksPage('photo', { cursor: 'same', limit: 1 }, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    fetchMock.mockResolvedValueOnce(reply({ data: [link], next_cursor: 'next' }))
    expect(await api.fetchMediaLinksPage('photo', { limit: 1 }, signal())).toEqual({ data: [link], next_cursor: 'next' })
  })

  it('preserves only allowlisted safe Media errors', async () => {
    const api = await import('./media')
    for (const code of ['INVALID_MEDIA_URL', 'INVALID_CURSOR', 'NOT_FOUND', 'AUTHENTICATION_REQUIRED']) {
      fetchMock.mockResolvedValueOnce(reply({ error: { code, message: 'private storage path', details: { token: 'secret' }, request_id: 'safe-id' } }, undefined, 400))
      const error = await api.fetchMedia('photo', signal()).catch((failure: unknown) => failure)
      expect(error).toMatchObject({ code, status: 400, requestId: 'safe-id' })
      expect(JSON.stringify(error)).not.toContain('secret')
      expect(JSON.stringify(error)).not.toContain('private storage path')
    }
  })
})
