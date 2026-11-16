// @vitest-environment node
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { version } from '../../../package.json'

const fetchMock = vi.fn<typeof fetch>()
const context = () => ({ csrfToken: 'C'.repeat(43), idempotencyKey: 'original-upload-key', signal: new AbortController().signal })
const uploaded = { id: 'photo', revision: 7, sha256: 'a'.repeat(64), mime_type: 'image/png', verification: 'verified', width: 10, height: 20,
  bytes: 30, thumb_url: '/api/v1/media/photo/thumbnail', thumb_sha256: 'b'.repeat(64) } as const
const reply = (data: unknown, etag?: string, status = 200) => Response.json(data, { status, headers: etag === undefined ? {} : { ETag: etag } })
const request = () => fetchMock.mock.lastCall?.[0] as Request

beforeEach(() => {
  vi.resetModules(); fetchMock.mockReset(); vi.stubGlobal('fetch', fetchMock)
  vi.stubGlobal('window', Object.assign(new EventTarget(), { location: new URL('http://localhost/') }))
})
afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals() })

describe('explicit Media upload adapter', () => {
  it.each(['image/jpeg', 'image/png', 'image/webp'])('sends exactly one %s file without its original filename', async (type) => {
    const api = await import('./media-upload')
    const file = new File([new Uint8Array([0, 1, 2, 255])], 'private-personal-name.png', { type, lastModified: 123 })
    fetchMock.mockResolvedValueOnce(reply(uploaded, '"7"'))
    expect(await api.uploadMedia(file, context())).toEqual(uploaded)
    expect(fetchMock).toHaveBeenCalledOnce()
    const sent = request()
    expect(sent.url).toBe('http://localhost/api/v1/media')
    expect(sent.method).toBe('POST')
    expect(sent.cache).toBe('no-store'); expect(sent.credentials).toBe('same-origin'); expect(sent.redirect).toBe('error')
    expect(sent.headers.get('Origin')).toBe('http://localhost')
    expect(sent.headers.get('X-CSRF-Token')).toBe('C'.repeat(43))
    expect(sent.headers.get('X-HomeBox-Client-Version')).toBe(version)
    expect(sent.headers.get('Idempotency-Key')).toBe('original-upload-key')
    expect(sent.headers.has('If-Match')).toBe(false); expect(sent.headers.has('Authorization')).toBe(false)
    expect(sent.headers.get('Content-Type')).toMatch(/^multipart\/form-data;\s*boundary=/)
    const form = await sent.formData(), entries = [...form.entries()]
    expect(entries).toHaveLength(1); expect(entries[0]?.[0]).toBe('file')
    const part = entries[0]?.[1]
    expect(part).toBeInstanceOf(Blob)
    if (!(part instanceof Blob)) throw new Error('Expected a file part')
    expect(part.type).toBe(type)
    expect((part as File).name).toBe('media')
    expect([...new Uint8Array(await part.arrayBuffer())]).toEqual([0, 1, 2, 255])
  })

  it('accepts new revision one and previously verified deduplicated revisions', async () => {
    const api = await import('./media-upload'), file = new Blob(['not validated by the browser adapter'], { type: 'image/png' })
    fetchMock.mockResolvedValueOnce(reply({ ...uploaded, revision: 1 }, '"1"')).mockResolvedValueOnce(reply(uploaded, '"7"'))
    expect((await api.uploadMedia(file, context())).revision).toBe(1)
    expect((await api.uploadMedia(file, context())).revision).toBe(7)
  })

  it('rejects empty, oversized, unsupported and non-Blob inputs before sending', async () => {
    const api = await import('./media-upload')
    const tooLarge = new Blob([new Uint8Array(32 * 1024 * 1024 + 1)], { type: 'image/jpeg' })
    const invalid: unknown[] = [null, 'image', {}, new Blob([], { type: 'image/png' }), new Blob(['x']),
      new Blob(['x'], { type: 'image/svg+xml' }), new Blob(['x'], { type: 'image/png;charset=utf-8' }), tooLarge]
    for (const file of invalid) {
      await expect(api.uploadMedia(file as Blob, context())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    }
    await expect(api.uploadMedia(new Blob(['x'], { type: 'image/png' }), { ...context(), expectedRevision: 1 } as never)).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('accepts the exact 32 MiB file limit without assuming the bytes are an image', async () => {
    const api = await import('./media-upload')
    const file = new Blob([new Uint8Array(32 * 1024 * 1024)], { type: 'image/png' })
    fetchMock.mockResolvedValueOnce(reply(uploaded, '"7"'))
    await expect(api.uploadMedia(file, context())).resolves.toEqual(uploaded)
    expect(fetchMock).toHaveBeenCalledOnce()
  })

  it('rejects invalid, unverified, unsafe or unrelated thumbnail replies and wrong ETags', async () => {
    const api = await import('./media-upload'), file = new Blob(['x'], { type: 'image/png' })
    const invalid: [unknown, string | undefined, number?][] = [
      [{ ...uploaded, verification: 'unverified', thumb_url: null, thumb_sha256: null }, '"7"'],
      [{ ...uploaded, mime_type: 'image/heic' }, '"7"'],
      [{ ...uploaded, width: 16_385 }, '"7"'],
      [{ ...uploaded, width: 16_384, height: 16_384 }, '"7"'],
      [{ ...uploaded, bytes: 64_000_000 * 8 + 16 * 1024 * 1024 + 1 }, '"7"'],
      [{ ...uploaded, thumb_url: '/api/v1/media/another/thumbnail' }, '"7"'],
      [{ ...uploaded, thumb_url: 'https://outside.example/thumbnail' }, '"7"'],
      [{ ...uploaded, thumb_sha256: null }, '"7"'],
      [uploaded, undefined], [uploaded, 'W/"7"'], [uploaded, '"8"'], [uploaded, '"7"', 201],
    ]
    for (const [value, etag, status] of invalid) {
      fetchMock.mockResolvedValueOnce(reply(value, etag, status))
      await expect(api.uploadMedia(file, context())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
    expect(fetchMock).toHaveBeenCalledTimes(invalid.length)
  })

  it('never retries a failed upload on reconnect and preserves the explicit original key', async () => {
    const api = await import('./media-upload'), file = new File(['raw private bytes'], 'private.png', { type: 'image/png' }), original = context()
    fetchMock.mockRejectedValueOnce(new TypeError('private transport diagnostics'))
    await expect(api.uploadMedia(file, original)).rejects.toMatchObject({ code: 'NETWORK_ERROR' })
    window.dispatchEvent(new Event('online'))
    expect(fetchMock).toHaveBeenCalledOnce()
    fetchMock.mockResolvedValueOnce(reply(uploaded, '"7"'))
    await expect(api.uploadMedia(file, original)).resolves.toEqual(uploaded)
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(request().headers.get('Idempotency-Key')).toBe(original.idempotencyKey)
  })

  it('maps safe server errors without leaking arbitrary details', async () => {
    const api = await import('./media-upload'), file = new Blob(['x'], { type: 'image/png' })
    fetchMock.mockResolvedValueOnce(reply({ error: { code: 'BODY_TOO_LARGE', request_id: 'req-1', message: 'private filename' } }, undefined, 413))
    const error = await api.uploadMedia(file, context()).catch((value: unknown) => value)
    expect(error).toMatchObject({ code: 'BODY_TOO_LARGE', status: 413, requestId: 'req-1' })
    expect(JSON.stringify(error)).not.toContain('private filename')
  })

  it('honors caller cancellation before and during sending', async () => {
    const api = await import('./media-upload'), file = new Blob(['x'], { type: 'image/png' }), controller = new AbortController()
    controller.abort()
    await expect(api.uploadMedia(file, { ...context(), signal: controller.signal })).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
    expect(fetchMock).not.toHaveBeenCalled()
    const pending = new AbortController()
    fetchMock.mockImplementationOnce(async (input) => await new Promise((_resolve, reject) => {
      const signal = (input as Request).signal
      signal.addEventListener('abort', () => { reject(new DOMException('Aborted', 'AbortError')) }, { once: true })
    }))
    const result = api.uploadMedia(file, { ...context(), signal: pending.signal })
    pending.abort()
    await expect(result).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
    expect(fetchMock).toHaveBeenCalledOnce()
  })

  it('bounds a pending upload without silently retrying', async () => {
    vi.useFakeTimers()
    const api = await import('./media-upload'), file = new Blob(['x'], { type: 'image/png' })
    fetchMock.mockImplementationOnce(async (input) => await new Promise((_resolve, reject) => {
      const signal = (input as Request).signal
      signal.addEventListener('abort', () => { reject(new DOMException('Aborted', 'AbortError')) }, { once: true })
    }))
    const result = api.uploadMedia(file, context())
    const assertion = expect(result).rejects.toMatchObject({ code: 'REQUEST_TIMEOUT' })
    await vi.advanceTimersByTimeAsync(30_000)
    await assertion
    expect(fetchMock).toHaveBeenCalledOnce()
  })
})
