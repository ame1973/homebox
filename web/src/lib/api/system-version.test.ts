import { beforeEach, describe, expect, it, vi } from 'vitest'

const fetchMock = vi.fn<typeof fetch>()
const signal = () => new AbortController().signal
const base = {
  api_version: '1', projection_version: 1, offline_projection_version: 1,
  dataset_epoch: 'epoch-1', server_instance_id: 'a'.repeat(32), web_build_id: 'b'.repeat(24),
  web_version: '0.1.0-dev', schema_version: 28, min_supported_client: null, min_web_build_id: null,
  model_index_versions: null, supported_write_client_versions: ['0.1.0', '0.1.0-dev'],
} as const
const reply = (value: unknown, headers: Record<string, string> = {}) => Response.json(value, { headers: { 'Cache-Control': 'no-store', ...headers } })
const request = () => fetchMock.mock.lastCall?.[0] as Request

beforeEach(() => { vi.resetModules(); fetchMock.mockReset(); vi.stubGlobal('fetch', fetchMock) })

describe('system version', () => {
  it('reads one protected live observation without persistence or retry', async () => {
    fetchMock.mockResolvedValueOnce(reply(base))
    const storage = [vi.spyOn(localStorage, 'getItem'), vi.spyOn(localStorage, 'setItem'), vi.spyOn(sessionStorage, 'getItem'), vi.spyOn(sessionStorage, 'setItem')]
    const { fetchSystemVersion } = await import('./system-version')
    expect(await fetchSystemVersion(signal())).toEqual(base)
    const sent = request()
    expect(sent.url).toBe('http://localhost/api/v1/system/version')
    expect(sent.method).toBe('GET'); expect(sent.cache).toBe('no-store'); expect(sent.credentials).toBe('same-origin'); expect(sent.redirect).toBe('error')
    for (const header of ['Cookie', 'Authorization', 'Origin', 'X-CSRF-Token', 'Idempotency-Key', 'If-Match', 'Content-Type']) expect(sent.headers.has(header)).toBe(false)
    expect(await sent.text()).toBe('')
    window.dispatchEvent(new Event('online'))
    expect(fetchMock).toHaveBeenCalledTimes(1)
    for (const spy of storage) expect(spy).not.toHaveBeenCalled()
  })

  it('keeps unknown inventory distinct from an explicitly empty or reported inventory', async () => {
    const { fetchSystemVersion } = await import('./system-version')
    for (const models of [null, [], ['pinned-model-1']]) {
      const value = { ...base, model_index_versions: models }
      fetchMock.mockResolvedValueOnce(reply(value))
      expect(await fetchSystemVersion(signal())).toEqual(value)
    }
  })

  it('withholds malformed, incomplete, unknown-key or cacheable responses', async () => {
    const { fetchSystemVersion } = await import('./system-version')
    const invalid = [
      null, {}, { ...base, secret: 'private' }, JSON.parse(JSON.stringify(base).replace(/}$/, ',"__proto__":1}')),
      { ...base, api_version: '' }, { ...base, dataset_epoch: '' }, { ...base, dataset_epoch: '\ud800' },
      { ...base, server_instance_id: 'A'.repeat(32) }, { ...base, server_instance_id: 'a'.repeat(32) + '\n' },
      { ...base, web_build_id: 'A'.repeat(24) }, { ...base, web_build_id: 'b'.repeat(24) + '\n' },
      { ...base, web_version: '' }, { ...base, web_version: 'x'.repeat(33) },
      { ...base, projection_version: 2 }, { ...base, offline_projection_version: 2 },
      { ...base, schema_version: 0 }, { ...base, schema_version: Number.MAX_SAFE_INTEGER + 1 },
      { ...base, min_supported_client: '' }, { ...base, min_web_build_id: '' },
      { ...base, model_index_versions: [''] }, { ...base, model_index_versions: ['x'.repeat(257)] },
      { ...base, model_index_versions: Array(501).fill('x') }, { ...base, model_index_versions: 'not-an-array' },
      { ...base, supported_write_client_versions: null }, { ...base, supported_write_client_versions: '0.1.0' },
      { ...base, supported_write_client_versions: {} }, { ...base, supported_write_client_versions: [] },
      { ...base, supported_write_client_versions: ['1', '1'] },
      { ...base, supported_write_client_versions: [''] }, { ...base, supported_write_client_versions: ['x'.repeat(33)] },
      { ...base, supported_write_client_versions: Array.from({ length: 17 }, (_, i) => String(i)) },
      ...Object.keys(base).map((field) => Object.fromEntries(Object.entries(base).filter(([key]) => key !== field))),
    ]
    for (const value of invalid) {
      fetchMock.mockResolvedValueOnce(reply(value))
      await expect(fetchSystemVersion(signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
    fetchMock.mockResolvedValueOnce(reply(base, { ETag: '"1"' }))
    await expect(fetchSystemVersion(signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    fetchMock.mockResolvedValueOnce(Response.json(base))
    await expect(fetchSystemVersion(signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    expect(fetchMock).toHaveBeenCalledTimes(invalid.length + 2)
  })

  it('does not replay failed reads or expose server details', async () => {
    const { fetchSystemVersion } = await import('./system-version')
    fetchMock.mockRejectedValueOnce(new TypeError('private filesystem path'))
    await expect(fetchSystemVersion(signal())).rejects.toMatchObject({ code: 'NETWORK_ERROR' })
    window.dispatchEvent(new Event('online'))
    fetchMock.mockResolvedValueOnce(Response.json({ error: { code: 'SERVICE_UNAVAILABLE', message: 'private error' } }, { status: 503 }))
    const error = await fetchSystemVersion(signal()).catch((reason: unknown) => reason)
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
      const { fetchSystemVersion } = await import('./system-version')
      const timed = expect(fetchSystemVersion(signal())).rejects.toMatchObject({ code: 'REQUEST_TIMEOUT' })
      await vi.advanceTimersByTimeAsync(7999)
      expect(request().signal.aborted).toBe(false)
      await vi.advanceTimersByTimeAsync(1); await timed
      const controller = new AbortController()
      const canceled = expect(fetchSystemVersion(controller.signal)).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
      controller.abort(); await canceled
      const early = new AbortController(); early.abort()
      await expect(fetchSystemVersion(early.signal)).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
      expect(fetchMock).toHaveBeenCalledTimes(2); expect(vi.getTimerCount()).toBe(0)
    } finally { vi.useRealTimers() }
  })
})
