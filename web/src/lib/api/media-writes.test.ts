import { beforeEach, describe, expect, it, vi } from 'vitest'
import { version } from '../../../package.json'

type MediaWrites = typeof import('./media-writes')
const fetchMock = vi.fn<typeof fetch>()
const signal = () => new AbortController().signal
const context = () => ({ csrfToken: 'C'.repeat(43), idempotencyKey: 'original-media-key', signal: signal() })
const image = { id: 'links', revision: 7, sha256: 'a'.repeat(64), mime_type: 'image/heic', verification: 'unverified', width: 10, height: 20,
  bytes: 30, thumb_url: null, thumb_sha256: null } as const
const item = { id: 'item/\0😀', revision: 4, name: 'Owner', brand: null, model: '', category_id: null, description: '', notes: '',
  tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1, status: 'active' } as const
const unit = { id: 'unit-1', item_id: item.id, revision: 3, location_id: null, asset_code: 'Asset', status: 'stored', serial_number: null, notes: '' } as const
const location = { id: 'tree', revision: 6, parent_id: null, name: 'Room', code: 'R', kind: 'room', status: 'active', sort_order: 0, description: '' } as const
const owner = { kind: 'item', value: item } as const
const oldLink = { id: 'view', revision: 5, media_id: image.id, owner_type: 'item', owner_id: item.id, is_cover: true, sort_order: -3,
  description: '\0Exact é\n' } as const
const view = { ...oldLink, media: image } as const
const ack = { ok: true, operation_id: 'event-1', revision: 6 } as const
const reply = (data: unknown, etag?: string, status = 200) => Response.json(data, { status, headers: etag === undefined ? {} : { ETag: etag } })
const sent = () => fetchMock.mock.lastCall?.[0] as Request

beforeEach(() => { vi.resetModules(); fetchMock.mockReset(); vi.stubGlobal('fetch', fetchMock) })

describe('reviewed MediaLink writes', () => {
  const owners = [
    { selected: owner, field: 'item_id', id: item.id, revision: item.revision },
    { selected: { kind: 'unit', value: unit } as const, field: 'item_unit_id', id: unit.id, revision: unit.revision },
    { selected: { kind: 'location', value: location } as const, field: 'location_id', id: location.id, revision: location.revision },
  ] as const

  it.each(owners)('attaches to the reviewed $field with separate original Media and owner proofs', async ({ selected, field, id, revision }) => {
    const created = { ...oldLink, id: 'new-link', revision: 1, owner_type: selected.kind, owner_id: id, is_cover: false, sort_order: 0, description: '' }
    fetchMock.mockResolvedValueOnce(reply(created, '"1"'))
    const api = await import('./media-writes')
    expect(await api.attachMedia(image.id, {}, { ...context(), media: image, owner: selected })).toEqual(created)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const request = sent()
    expect(request.url).toBe('http://localhost/api/v1/media/links/links')
    expect(request.method).toBe('POST')
    expect(request.cache).toBe('no-store'); expect(request.credentials).toBe('same-origin'); expect(request.redirect).toBe('error')
    expect(request.headers.get('If-Match')).toBe('"7"')
    expect(request.headers.get('Origin')).toBe('http://localhost')
    expect(request.headers.get('X-CSRF-Token')).toBe('C'.repeat(43))
    expect(request.headers.get('X-HomeBox-Client-Version')).toBe(version)
    expect(request.headers.get('Idempotency-Key')).toBe('original-media-key')
    expect(request.headers.has('Cookie')).toBe(false); expect(request.headers.has('Authorization')).toBe(false)
    expect(await request.json()).toEqual({ [field]: id, owner_revision: revision })
  })

  it('preserves explicit defaults and exact Unicode while leaving omitted metadata absent on the wire', async () => {
    const api = await import('./media-writes')
    const created = { ...oldLink, id: 'created', revision: 1, is_cover: false, sort_order: 0, description: '' }
    for (const body of [{}, { is_cover: false, sort_order: 0, description: '' }]) {
      fetchMock.mockResolvedValueOnce(reply(created, '"1"'))
      await api.attachMedia(image.id, body, { ...context(), media: image, owner })
      expect(await sent().json()).toEqual({ ...body, item_id: item.id, owner_revision: item.revision })
    }
    const description = '\0Exact é 😀\n'
    fetchMock.mockResolvedValueOnce(reply({ ...created, is_cover: true, sort_order: Number.MAX_SAFE_INTEGER, description }, '"1"'))
    await api.attachMedia(image.id, { is_cover: true, sort_order: Number.MAX_SAFE_INTEGER, description }, { ...context(), media: image, owner })
    expect(await sent().json()).toEqual({ item_id: item.id, owner_revision: item.revision, is_cover: true, sort_order: Number.MAX_SAFE_INTEGER, description })
  })

  it('patches only the independent link, preserving omitted legacy values and unchanged identities', async () => {
    const api = await import('./media-writes')
    fetchMock.mockResolvedValueOnce(reply({ ...oldLink, revision: 6, description: '' }, '"6"'))
    expect(await api.patchMediaLink('view', { description: '' }, { ...context(), link: view })).toEqual({ ...oldLink, revision: 6, description: '' })
    expect(sent().url).toBe('http://localhost/api/v1/media/links/view')
    expect(sent().method).toBe('PATCH')
    expect(sent().headers.get('If-Match')).toBe('"5"')
    expect(sent().headers.has('X-Owner-Revision')).toBe(false)
    expect(await sent().json()).toEqual({ description: '' })
    fetchMock.mockResolvedValueOnce(reply(oldLink, '"5"'))
    expect(await api.patchMediaLink('view', { is_cover: true }, { ...context(), link: oldLink })).toEqual(oldLink)
    expect(await sent().json()).toEqual({ is_cover: true })
    fetchMock.mockResolvedValueOnce(reply({ ...oldLink, revision: 6, is_cover: false, sort_order: 0 }, '"6"'))
    await api.patchMediaLink('view', { is_cover: false, sort_order: 0 }, { ...context(), link: oldLink })
    expect(await sent().json()).toEqual({ is_cover: false, sort_order: 0 })
  })

  it('unlinks with only the reviewed independent link proof and no body or response ETag', async () => {
    const api = await import('./media-writes')
    fetchMock.mockResolvedValueOnce(reply(ack))
    expect(await api.unlinkMediaLink('view', { ...context(), link: view })).toEqual(ack)
    expect(sent().url).toBe('http://localhost/api/v1/media/links/view')
    expect(sent().method).toBe('DELETE')
    expect(sent().headers.get('If-Match')).toBe('"5"')
    expect(sent().headers.get('Idempotency-Key')).toBe('original-media-key')
    expect(sent().headers.has('Content-Type')).toBe(false)
    expect(await sent().text()).toBe('')
  })

  it('dispatches retained link ID links while preserving path-unaddressable Media and owner identities', async () => {
    const api = await import('./media-writes'), link = { ...oldLink, id: 'links', media_id: 'legacy/\0media' }
    fetchMock.mockResolvedValueOnce(reply({ ...link, revision: 6, description: '' }, '"6"'))
    await api.patchMediaLink('links', { description: '' }, { ...context(), link })
    expect(sent().url).toBe('http://localhost/api/v1/media/links/links'); expect(sent().method).toBe('PATCH')
    fetchMock.mockResolvedValueOnce(reply(ack))
    await api.unlinkMediaLink('links', { ...context(), link })
    expect(sent().url).toBe('http://localhost/api/v1/media/links/links'); expect(sent().method).toBe('DELETE')
  })

  it('snapshots original bodies, owner and Media or link proofs before the request can settle', async () => {
    const api = await import('./media-writes')
    let release: ((response: Response) => void) | undefined
    fetchMock.mockImplementation(() => new Promise((resolve) => { release = resolve }))
    const body = { description: 'Original' }, media = { ...image, revision: Number(image.revision) }
    const originalOwner = { ...item, id: String(item.id), revision: Number(item.revision) }, selected = { kind: 'item' as const, value: originalOwner }
    const originalContext = { ...context(), media, owner: selected }
    const pending = api.attachMedia(image.id, body, originalContext)
    body.description = 'Changed'; media.revision = 99; originalOwner.id = 'other'; originalOwner.revision = 99; originalContext.idempotencyKey = 'changed-key'
    release?.(reply({ ...oldLink, id: 'created', revision: 1, is_cover: false, sort_order: 0, description: 'Original' }, '"1"'))
    expect((await pending).description).toBe('Original')
    expect(sent().headers.get('If-Match')).toBe('"7"'); expect(sent().headers.get('Idempotency-Key')).toBe('original-media-key')
    expect(await sent().json()).toEqual({ item_id: item.id, owner_revision: 4, description: 'Original' })

    const link = { ...oldLink, revision: Number(oldLink.revision), owner_id: String(oldLink.owner_id), sort_order: Number(oldLink.sort_order) }
    const patch = { description: 'Reviewed' }, linkContext = { ...context(), link }
    const changed = api.patchMediaLink('view', patch, linkContext)
    link.revision = 99; link.owner_id = 'other'; link.sort_order = 77; patch.description = 'Changed'; linkContext.idempotencyKey = 'changed-key'
    release?.(reply({ ...oldLink, revision: 6, description: 'Reviewed' }, '"6"'))
    expect((await changed).owner_id).toBe(item.id)
    expect(sent().headers.get('If-Match')).toBe('"5"'); expect(sent().headers.get('Idempotency-Key')).toBe('original-media-key')
    expect(await sent().json()).toEqual({ description: 'Reviewed' })

    const unlinkSource = { ...oldLink, revision: Number(oldLink.revision) }, unlinkContext = { ...context(), link: unlinkSource }
    const removed = api.unlinkMediaLink('view', unlinkContext)
    unlinkSource.revision = 99; unlinkContext.idempotencyKey = 'changed-key'
    release?.(reply(ack))
    expect(await removed).toEqual(ack)
    expect(sent().headers.get('If-Match')).toBe('"5"'); expect(sent().headers.get('Idempotency-Key')).toBe('original-media-key')
  })
})

describe('MediaLink admission and response validation', () => {
  it('rejects unsafe paths, malformed proofs, extra proofs and inconsistent joined views before sending', async () => {
    const api = await import('./media-writes')
    for (const id of ['', '.', '..', 'a/b', 'a\\b', '\0', '\uD800', '😀'.repeat(129)]) {
      await expect(api.attachMedia(id, {}, { ...context(), media: image, owner })).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
      await expect(api.patchMediaLink(id, { description: '' }, { ...context(), link: oldLink })).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
      await expect(api.unlinkMediaLink(id, { ...context(), link: oldLink })).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    }
    for (const bad of [
      { ...context(), media: { ...image, id: 'other' }, owner }, { ...context(), media: { ...image, revision: 0 }, owner },
      { ...context(), media: image, owner: { kind: 'item', value: { ...item, revision: 0 } } },
      { ...context(), media: image, owner: { kind: 'purchase', value: item } }, { ...context(), media: image, owner, expectedRevision: 7 },
      { ...context(), media: { ...image, storage_key: 'private' }, owner },
    ]) await expect(api.attachMedia(image.id, {}, bad as never)).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    for (const bad of [
      { ...oldLink, id: 'another' }, { ...oldLink, revision: 0 }, { ...oldLink, owner_type: 'purchase' },
      { ...oldLink, owner_id: '\uD800' }, { ...view, media: { ...image, id: 'other' } }, { ...oldLink, private_key: 'secret' },
    ]) {
      await expect(api.patchMediaLink('view', { description: '' }, { ...context(), link: bad } as never)).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
      await expect(api.unlinkMediaLink('view', { ...context(), link: bad } as never)).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    }
    for (const action of [api.patchMediaLink('view', { description: '' }, { ...context(), link: oldLink, expectedRevision: 5 } as never),
      api.unlinkMediaLink('view', { ...context(), link: oldLink, expectedRevision: 5 } as never)]) {
      await expect(action).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    }
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('rejects null, negative, unsafe, extra and malformed input without inventing owner or defaults', async () => {
    const api = await import('./media-writes')
    for (const body of [{ is_cover: null }, { is_cover: 'true' }, { sort_order: -1 }, { sort_order: 1.5 }, { sort_order: Number.MAX_SAFE_INTEGER + 1 },
      { description: null }, { description: '\uD800' }, { description: '😀'.repeat(10001) }, { owner_revision: 4 }, { item_id: item.id }, { media_id: image.id }]) {
      await expect(api.attachMedia(image.id, body as never, { ...context(), media: image, owner })).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
      await expect(api.patchMediaLink('view', body as never, { ...context(), link: oldLink })).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    }
    for (const body of [{}, { is_cover: undefined, sort_order: undefined, description: undefined }]) {
      await expect(api.patchMediaLink('view', body as never, { ...context(), link: oldLink })).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    }
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('withholds wrong attachment identity, defaults, revision, proof and unexpected private fields', async () => {
    const api = await import('./media-writes')
    const created = { ...oldLink, id: 'created', revision: 1, is_cover: false, sort_order: 0, description: '' }
    for (const value of [{ ...created, media_id: 'other' }, { ...created, owner_type: 'unit' }, { ...created, owner_id: 'other' },
      { ...created, revision: 2 }, { ...created, is_cover: true }, { ...created, sort_order: -1 }, { ...created, description: 'other' },
      { ...created, storage_key: 'private' }]) {
      fetchMock.mockResolvedValueOnce(reply(value, '"1"'))
      await expect(api.attachMedia(image.id, {}, { ...context(), media: image, owner })).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
    for (const etag of [undefined, 'W/"1"', '"2"']) {
      fetchMock.mockResolvedValueOnce(reply(created, etag))
      await expect(api.attachMedia(image.id, {}, { ...context(), media: image, owner })).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
  })

  it('withholds altered immutable or omitted fields, false revisions and ETags on patch/unlink', async () => {
    const api = await import('./media-writes')
    const changed = { ...oldLink, revision: 6, description: '' }
    for (const value of [{ ...changed, id: 'other' }, { ...changed, media_id: 'other' }, { ...changed, owner_type: 'unit' },
      { ...changed, owner_id: 'other' }, { ...changed, is_cover: false }, { ...changed, sort_order: 0 },
      { ...changed, description: 'wrong' }, { ...changed, revision: 5 }, { ...changed, revision: 7 }, { ...changed, raw_ocr: 'private' }]) {
      fetchMock.mockResolvedValueOnce(reply(value, `"${value.revision}"`))
      await expect(api.patchMediaLink('view', { description: '' }, { ...context(), link: oldLink })).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
    fetchMock.mockResolvedValueOnce(reply(changed, '"5"'))
    await expect(api.patchMediaLink('view', { description: '' }, { ...context(), link: oldLink })).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    fetchMock.mockResolvedValueOnce(reply({ ...oldLink, revision: 6 }, '"6"'))
    await expect(api.patchMediaLink('view', { is_cover: true }, { ...context(), link: oldLink })).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    for (const [value, etag] of [[{ ...ack, revision: 5 }, undefined], [{ ...ack, revision: 7 }, undefined], [ack, '"6"'], [{ ...ack, private_key: 'secret' }, undefined]] as const) {
      fetchMock.mockResolvedValueOnce(reply(value, etag))
      await expect(api.unlinkMediaLink('view', { ...context(), link: oldLink })).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
  })

  it('retains only safe errors, never queues a failed write and reconciles only on explicit same-key retry', async () => {
    const api = await import('./media-writes')
    for (const code of ['MEDIA_COVER_CONFLICT', 'REVISION_CONFLICT', 'IDEMPOTENCY_CONFLICT', 'IDEMPOTENCY_EXPIRED', 'AUTHENTICATION_REQUIRED']) {
      fetchMock.mockResolvedValueOnce(reply({ error: { code, request_id: 'safe-id', message: 'private path', details: { token: 'secret' } } }, undefined, 409))
      const failure = await api.patchMediaLink('view', { description: '' }, { ...context(), link: oldLink }).catch((error: unknown) => error)
      expect(failure).toMatchObject({ code, status: 409, requestId: 'safe-id' })
      expect(JSON.stringify(failure)).not.toMatch(/private path|secret|details|cause/)
    }
    fetchMock.mockRejectedValueOnce(new TypeError('private transport'))
    const original = { ...context(), link: oldLink }
    await expect(api.unlinkMediaLink('view', original)).rejects.toMatchObject({ code: 'NETWORK_ERROR' })
    window.dispatchEvent(new Event('online'))
    expect(fetchMock).toHaveBeenCalledTimes(6)
    fetchMock.mockResolvedValueOnce(reply(ack))
    expect(await api.unlinkMediaLink('view', original)).toEqual(ack)
    expect(sent().headers.get('Idempotency-Key')).toBe('original-media-key')
    expect(fetchMock).toHaveBeenCalledTimes(7)
  })

  it('honors caller cancellation and the 30-second write deadline without replay', async () => {
    vi.useFakeTimers()
    try {
      const api = await import('./media-writes')
      fetchMock.mockImplementation((request) => new Promise((_resolve, reject) => {
        (request as Request).signal.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')), { once: true })
      }))
      const early = new AbortController(); early.abort()
      await expect(api.unlinkMediaLink('view', { ...context(), link: oldLink, signal: early.signal })).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
      expect(fetchMock).not.toHaveBeenCalled()
      const later = new AbortController()
      const aborted = expect(api.unlinkMediaLink('view', { ...context(), link: oldLink, signal: later.signal })).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
      later.abort(); await aborted
      const timeout = expect(api.patchMediaLink('view', { description: '' }, { ...context(), link: oldLink })).rejects.toMatchObject({ code: 'REQUEST_TIMEOUT' })
      await vi.advanceTimersByTimeAsync(29_999); expect(sent().signal.aborted).toBe(false)
      await vi.advanceTimersByTimeAsync(1); await timeout
      expect(fetchMock).toHaveBeenCalledTimes(2); expect(vi.getTimerCount()).toBe(0)
    } finally { vi.useRealTimers() }
  })

  it('does not inspect or retain credentials, drafts, replica or cache storage', async () => {
    const spies = [vi.spyOn(localStorage, 'getItem'), vi.spyOn(localStorage, 'setItem'), vi.spyOn(sessionStorage, 'getItem'),
      vi.spyOn(sessionStorage, 'setItem'), vi.spyOn(document, 'cookie', 'get'), vi.spyOn(document, 'cookie', 'set')]
    const database = vi.fn(), cache = vi.fn()
    vi.stubGlobal('indexedDB', { open: database }); vi.stubGlobal('caches', { open: cache })
    const api: MediaWrites = await import('./media-writes')
    fetchMock.mockResolvedValueOnce(reply(ack))
    await api.unlinkMediaLink('view', { ...context(), link: oldLink })
    for (const spy of [...spies, database, cache]) expect(spy).not.toHaveBeenCalled()
  })
})
