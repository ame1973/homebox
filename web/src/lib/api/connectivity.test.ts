import { beforeEach, describe, expect, it, vi } from 'vitest'

const fetchMock = vi.fn<typeof fetch>()
const signal = () => new AbortController().signal
const request = () => fetchMock.mock.lastCall?.[0] as Request

beforeEach(() => { vi.resetModules(); fetchMock.mockReset(); vi.stubGlobal('fetch', fetchMock) })

describe('authenticated connectivity', () => {
  it('sends one bounded, same-origin, non-cached read with no write credentials', async () => {
    const { probeConnectivity } = await import('./connectivity')
    fetchMock.mockResolvedValueOnce(Response.json({ status: 'ok' }))
    expect(await probeConnectivity(signal())).toEqual({ status: 'ok' })
    const sent = request()
    expect(sent.url).toBe('http://localhost/api/v1/system/connectivity')
    expect(sent.method).toBe('GET'); expect(sent.cache).toBe('no-store'); expect(sent.credentials).toBe('same-origin'); expect(sent.redirect).toBe('error')
    for (const header of ['Authorization', 'Origin', 'X-CSRF-Token', 'Idempotency-Key', 'If-Match', 'Content-Type']) expect(sent.headers.has(header)).toBe(false)
    expect(await sent.text()).toBe('')
    window.dispatchEvent(new Event('online'))
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('preserves the two bounded reachability states without implying data health', async () => {
    const { probeConnectivity } = await import('./connectivity')
    for (const status of ['ok', 'degraded'] as const) {
      fetchMock.mockResolvedValueOnce(Response.json({ status }))
      expect(await probeConnectivity(signal())).toEqual({ status })
    }
  })

  it('withholds missing, extra, malformed or ETagged replies instead of repairing them', async () => {
    const { probeConnectivity } = await import('./connectivity')
    const invalid = [null, {}, [], { status: 'unknown' }, { status: true }, { status: 'ok', private: 'secret' },
      JSON.parse('{"status":"ok","__proto__":"private"}'), JSON.parse('{"status":"ok","constructor":"private"}')]
    for (const value of invalid) {
      fetchMock.mockResolvedValueOnce(Response.json(value))
      await expect(probeConnectivity(signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
    fetchMock.mockResolvedValueOnce(Response.json({ status: 'ok' }, { headers: { ETag: '"1"' } }))
    await expect(probeConnectivity(signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    fetchMock.mockResolvedValueOnce(new Response(null, { status: 204 }))
    await expect(probeConnectivity(signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    expect(fetchMock).toHaveBeenCalledTimes(invalid.length + 2)
  })

  it('does not retry or disclose arbitrary network and server failure details', async () => {
    const { probeConnectivity } = await import('./connectivity')
    fetchMock.mockRejectedValueOnce(new TypeError('private filesystem path'))
    await expect(probeConnectivity(signal())).rejects.toMatchObject({ code: 'NETWORK_ERROR' })
    window.dispatchEvent(new Event('online'))
    fetchMock.mockResolvedValueOnce(Response.json({ error: { code: 'RATE_LIMITED', message: 'private error' } }, { status: 429, headers: { 'Retry-After': '60' } }))
    const error = await probeConnectivity(signal()).catch((reason: unknown) => reason)
    expect(error).toMatchObject({ code: 'RATE_LIMITED', status: 429, retryAfterSeconds: 60 })
    expect(JSON.stringify(error)).not.toContain('private error')
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it('honors caller cancellation and the eight-second deadline', async () => {
    vi.useFakeTimers()
    try {
      fetchMock.mockImplementation((input) => new Promise((_resolve, reject) => {
        (input as Request).signal.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')), { once: true })
      }))
      const { probeConnectivity } = await import('./connectivity')
      const timed = expect(probeConnectivity(signal())).rejects.toMatchObject({ code: 'REQUEST_TIMEOUT' })
      await vi.advanceTimersByTimeAsync(7999)
      expect(request().signal.aborted).toBe(false)
      await vi.advanceTimersByTimeAsync(1); await timed
      const controller = new AbortController()
      const canceled = expect(probeConnectivity(controller.signal)).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
      controller.abort(); await canceled
      const early = new AbortController(); early.abort()
      await expect(probeConnectivity(early.signal)).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
      expect(fetchMock).toHaveBeenCalledTimes(2); expect(vi.getTimerCount()).toBe(0)
    } finally { vi.useRealTimers() }
  })
})
