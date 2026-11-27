import { beforeEach, describe, expect, it, vi } from 'vitest'

const fetchMock = vi.fn<typeof fetch>()
const signal = () => new AbortController().signal
const base = {
  database_bytes: 2048, media_bytes: null, free_bytes: 8192, backup_bytes: null,
  database_filesystem_total_bytes: 16384, measured_at: '2026-11-27T12:00:00Z',
} as const
const reply = (value: unknown, headers: Record<string, string> = {}) => Response.json(value, { headers: { 'Cache-Control': 'no-store', ...headers } })
const request = () => fetchMock.mock.lastCall?.[0] as Request

beforeEach(() => { vi.resetModules(); fetchMock.mockReset(); vi.stubGlobal('fetch', fetchMock) })

describe('system storage', () => {
  it('reads one protected live observation without persistence or retry', async () => {
    fetchMock.mockResolvedValueOnce(reply(base))
    const storage = [vi.spyOn(localStorage, 'getItem'), vi.spyOn(localStorage, 'setItem'), vi.spyOn(sessionStorage, 'getItem'), vi.spyOn(sessionStorage, 'setItem')]
    const { fetchSystemStorage } = await import('./system-storage')
    expect(await fetchSystemStorage(signal())).toEqual(base)
    const sent = request()
    expect(sent.url).toBe('http://localhost/api/v1/system/storage')
    expect(sent.method).toBe('GET'); expect(sent.cache).toBe('no-store'); expect(sent.credentials).toBe('same-origin'); expect(sent.redirect).toBe('error')
    for (const header of ['Cookie', 'Authorization', 'Origin', 'X-CSRF-Token', 'Idempotency-Key', 'If-Match', 'Content-Type']) expect(sent.headers.has(header)).toBe(false)
    expect(await sent.text()).toBe('')
    window.dispatchEvent(new Event('online'))
    expect(fetchMock).toHaveBeenCalledTimes(1)
    for (const spy of storage) expect(spy).not.toHaveBeenCalled()
  })

  it('keeps unknown Media and backups distinct from measured zero and safe values', async () => {
    const { fetchSystemStorage } = await import('./system-storage')
    for (const patch of [{}, { media_bytes: 0, backup_bytes: 0 }, { media_bytes: Number.MAX_SAFE_INTEGER, backup_bytes: 123 }]) {
      const value = { ...base, ...patch }
      fetchMock.mockResolvedValueOnce(reply(value))
      expect(await fetchSystemStorage(signal())).toEqual(value)
    }
  })

  it('withholds malformed, incomplete, unsafe, inconsistent or cacheable responses', async () => {
    const { fetchSystemStorage } = await import('./system-storage')
    const counts = ['database_bytes', 'media_bytes', 'free_bytes', 'backup_bytes', 'database_filesystem_total_bytes'] as const
    const invalid = [null, {}, { ...base, secret: 'private' }, JSON.parse(JSON.stringify(base).replace(/}$/, ',"__proto__":1}')),
      ...counts.flatMap((field) => [-1, 1.5, Number.MAX_SAFE_INTEGER + 1, '1', true].map((value) => ({ ...base, [field]: value }))),
      ...['database_bytes', 'free_bytes', 'database_filesystem_total_bytes'].map((field) => ({ ...base, [field]: null })),
      ...Object.keys(base).map((field) => Object.fromEntries(Object.entries(base).filter(([key]) => key !== field))),
      { ...base, free_bytes: base.database_filesystem_total_bytes + 1 },
      { ...base, measured_at: 'not-a-time' }, { ...base, measured_at: '2026-02-30T12:00:00Z' }, { ...base, measured_at: null },
    ]
    for (const value of invalid) {
      fetchMock.mockResolvedValueOnce(reply(value))
      await expect(fetchSystemStorage(signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
    fetchMock.mockResolvedValueOnce(reply(base, { ETag: '"1"' }))
    await expect(fetchSystemStorage(signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    fetchMock.mockResolvedValueOnce(Response.json(base))
    await expect(fetchSystemStorage(signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    fetchMock.mockResolvedValueOnce(Response.json(base, { status: 201, headers: { 'Cache-Control': 'no-store' } }))
    await expect(fetchSystemStorage(signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    expect(fetchMock).toHaveBeenCalledTimes(invalid.length + 3)
  })

  it('does not replay failed reads or expose server details', async () => {
    const { fetchSystemStorage } = await import('./system-storage')
    fetchMock.mockRejectedValueOnce(new TypeError('private filesystem path'))
    await expect(fetchSystemStorage(signal())).rejects.toMatchObject({ code: 'NETWORK_ERROR' })
    window.dispatchEvent(new Event('online'))
    fetchMock.mockResolvedValueOnce(Response.json({ error: { code: 'SERVICE_UNAVAILABLE', message: 'private error' } }, { status: 503 }))
    const error = await fetchSystemStorage(signal()).catch((reason: unknown) => reason)
    expect(error).toMatchObject({ code: 'SERVICE_UNAVAILABLE', status: 503 })
    expect(JSON.stringify(error)).not.toContain('private error')
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it('enforces an eight-second deadline and caller cancellation', async () => {
    vi.useFakeTimers()
    try {
      fetchMock.mockImplementation((input) => new Promise((_resolve, reject) => {
        (input as Request).signal.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')), { once: true })
      }))
      const { fetchSystemStorage } = await import('./system-storage')
      const timed = expect(fetchSystemStorage(signal())).rejects.toMatchObject({ code: 'REQUEST_TIMEOUT' })
      await vi.advanceTimersByTimeAsync(7999)
      expect(request().signal.aborted).toBe(false)
      await vi.advanceTimersByTimeAsync(1); await timed
      const controller = new AbortController()
      const canceled = expect(fetchSystemStorage(controller.signal)).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
      controller.abort(); await canceled
      const early = new AbortController(); early.abort()
      await expect(fetchSystemStorage(early.signal)).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
      expect(fetchMock).toHaveBeenCalledTimes(2); expect(vi.getTimerCount()).toBe(0)
    } finally { vi.useRealTimers() }
  })
})
