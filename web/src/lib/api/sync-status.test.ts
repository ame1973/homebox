import { beforeEach, describe, expect, it, vi } from 'vitest'

const fetchMock = vi.fn<typeof fetch>()
const signal = () => new AbortController().signal
const base = {
  dataset_epoch: 'epoch-1', server_instance_id: 'a'.repeat(32), projection_version: 1,
  min_retained_seq: 0, high_watermark: 0, counts: {}, counts_scope: 'materialized_projections',
  last_change_at: null, retention_days: 30, bootstrap_ready: false, retention_enabled: false,
} as const
const request = () => fetchMock.mock.lastCall?.[0] as Request

beforeEach(() => { vi.resetModules(); fetchMock.mockReset(); vi.stubGlobal('fetch', fetchMock) })

describe('sync status', () => {
  it('reads one protected live diagnostic without persistence, writes or retry', async () => {
    fetchMock.mockResolvedValueOnce(Response.json(base))
    const storage = [vi.spyOn(localStorage, 'getItem'), vi.spyOn(localStorage, 'setItem'), vi.spyOn(sessionStorage, 'getItem'), vi.spyOn(sessionStorage, 'setItem')]
    const { fetchSyncStatus } = await import('./sync-status')
    expect(await fetchSyncStatus(signal())).toEqual(base)
    const sent = request()
    expect(sent.url).toBe('http://localhost/api/v1/sync/status')
    expect(sent.method).toBe('GET'); expect(sent.cache).toBe('no-store'); expect(sent.credentials).toBe('same-origin'); expect(sent.redirect).toBe('error')
    for (const header of ['Cookie', 'Authorization', 'Origin', 'X-CSRF-Token', 'Idempotency-Key', 'If-Match', 'Content-Type']) expect(sent.headers.has(header)).toBe(false)
    expect(await sent.text()).toBe('')
    window.dispatchEvent(new Event('online'))
    expect(fetchMock).toHaveBeenCalledTimes(1)
    for (const spy of storage) expect(spy).not.toHaveBeenCalled()
  })

  it('accepts partial materialization, a pruned log and future coherent readiness states', async () => {
    const { fetchSyncStatus } = await import('./sync-status')
    const values = [base,
      { ...base, high_watermark: 3, min_retained_seq: 2, counts: { location: 1, item_tag: 2 }, last_change_at: '2026-01-01T00:00:00.123456789Z' },
      { ...base, high_watermark: 3, min_retained_seq: 3, counts: { item: Number.MAX_SAFE_INTEGER } },
      { ...base, high_watermark: Number.MAX_SAFE_INTEGER, bootstrap_ready: true, retention_enabled: true, models: ['model-1'] },
      { ...base, dataset_epoch: '\u0000家😀', models: [] },
    ]
    for (const value of values) {
      fetchMock.mockResolvedValueOnce(Response.json(value))
      expect(await fetchSyncStatus(signal())).toEqual(value)
    }
  })

  it('withholds malformed, unsafe, inconsistent and unexpected responses or ETags', async () => {
    const { fetchSyncStatus } = await import('./sync-status')
    const invalid = [
      null, {}, { ...base, secret: 'private' }, JSON.parse(JSON.stringify(base).replace(/}$/, ',"__proto__":1}')),
      { ...base, dataset_epoch: '' }, { ...base, dataset_epoch: '\ud800' },
      { ...base, server_instance_id: 'A'.repeat(32) }, { ...base, server_instance_id: 'a'.repeat(32) + '\n' },
      { ...base, projection_version: 2 }, { ...base, min_retained_seq: -1 }, { ...base, high_watermark: Number.MAX_SAFE_INTEGER + 1 },
      { ...base, min_retained_seq: 1 }, { ...base, last_change_at: '2026-01-01T00:00:00Z' },
      { ...base, high_watermark: 2, min_retained_seq: 1, last_change_at: 'invalid' },
      { ...base, counts_scope: 'all_sources' }, { ...base, counts: { private: 1 } }, { ...base, counts: { item: 0 } },
      { ...base, counts: JSON.parse('{"__proto__": 1}') }, { ...base, counts: { constructor: 1 } },
      { ...base, counts: { item: 1.5 } }, { ...base, counts: { item: Number.MAX_SAFE_INTEGER + 1 } },
      { ...base, retention_days: 0 }, { ...base, bootstrap_ready: 1 }, { ...base, retention_enabled: 'false' },
      { ...base, models: [''] }, { ...base, models: ['x'.repeat(257)] }, { ...base, models: Array(501).fill('x') },
      ...Object.keys(base).map((field) => Object.fromEntries(Object.entries(base).filter(([key]) => key !== field))),
    ]
    for (const value of invalid) {
      fetchMock.mockResolvedValueOnce(Response.json(value))
      await expect(fetchSyncStatus(signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
    fetchMock.mockResolvedValueOnce(Response.json(base, { headers: { ETag: '"1"' } }))
    await expect(fetchSyncStatus(signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    expect(fetchMock).toHaveBeenCalledTimes(invalid.length + 1)
  })

  it('does not replay failed reads or disclose server error details', async () => {
    const { fetchSyncStatus } = await import('./sync-status')
    fetchMock.mockRejectedValueOnce(new TypeError('private filesystem path'))
    await expect(fetchSyncStatus(signal())).rejects.toMatchObject({ code: 'NETWORK_ERROR' })
    window.dispatchEvent(new Event('online'))
    fetchMock.mockResolvedValueOnce(Response.json({ error: { code: 'SERVICE_UNAVAILABLE', message: 'private error' } }, { status: 503 }))
    const error = await fetchSyncStatus(signal()).catch((reason: unknown) => reason)
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
      const { fetchSyncStatus } = await import('./sync-status')
      const timed = expect(fetchSyncStatus(signal())).rejects.toMatchObject({ code: 'REQUEST_TIMEOUT' })
      await vi.advanceTimersByTimeAsync(7999)
      expect(request().signal.aborted).toBe(false)
      await vi.advanceTimersByTimeAsync(1); await timed
      const controller = new AbortController()
      const canceled = expect(fetchSyncStatus(controller.signal)).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
      controller.abort(); await canceled
      const early = new AbortController(); early.abort()
      await expect(fetchSyncStatus(early.signal)).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
      expect(fetchMock).toHaveBeenCalledTimes(2); expect(vi.getTimerCount()).toBe(0)
    } finally { vi.useRealTimers() }
  })
})
