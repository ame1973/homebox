import { beforeEach, describe, expect, it, vi } from 'vitest'

const fetchMock = vi.fn<typeof fetch>()
const signal = () => new AbortController().signal
const base = { phase: 'idle', completed: 0, saturated: false, failure: '' } as const
const failures = ['staging-unproven', 'staging-limit', 'object-unproven', 'storage-unavailable', 'inconsistent-state', 'recovery-failed'] as const
const request = () => fetchMock.mock.lastCall?.[0] as Request

beforeEach(() => { vi.resetModules(); fetchMock.mockReset(); vi.stubGlobal('fetch', fetchMock) })

describe('Media recovery status', () => {
  it('reads one live same-origin snapshot without a write, local persistence or retry', async () => {
    fetchMock.mockResolvedValueOnce(Response.json(base))
    const storage = [vi.spyOn(localStorage, 'getItem'), vi.spyOn(localStorage, 'setItem'), vi.spyOn(sessionStorage, 'getItem'), vi.spyOn(sessionStorage, 'setItem')]
    const { fetchMediaRecoveryStatus } = await import('./media-recovery')
    expect(await fetchMediaRecoveryStatus(signal())).toEqual(base)
    const sent = request()
    expect(sent.url).toBe('http://localhost/api/v1/system/media-recovery')
    expect(sent.method).toBe('GET'); expect(sent.cache).toBe('no-store'); expect(sent.credentials).toBe('same-origin'); expect(sent.redirect).toBe('error')
    for (const header of ['Cookie', 'Authorization', 'Origin', 'X-CSRF-Token', 'Idempotency-Key', 'If-Match', 'Content-Type']) expect(sent.headers.has(header)).toBe(false)
    expect(await sent.text()).toBe('')
    window.dispatchEvent(new Event('online'))
    expect(fetchMock).toHaveBeenCalledTimes(1)
    for (const spy of storage) expect(spy).not.toHaveBeenCalled()
  })

  it('accepts only coherent bounded phase, failure and lifetime-count snapshots', async () => {
    const { fetchMediaRecoveryStatus } = await import('./media-recovery')
    const values = [
      ...['queued', 'running', 'idle', 'stopped'].map((phase) => ({ ...base, phase })),
      ...failures.flatMap((failure) => [{ ...base, phase: 'blocked', failure }, { ...base, phase: 'stopped', failure }]),
      { ...base, phase: 'unavailable', failure: 'storage-unavailable' },
      { ...base, completed: Number.MAX_SAFE_INTEGER, saturated: true },
      { ...base, completed: Number.MAX_SAFE_INTEGER },
    ]
    for (const value of values) {
      fetchMock.mockResolvedValueOnce(Response.json(value))
      expect(await fetchMediaRecoveryStatus(signal())).toEqual(value)
    }
    expect(fetchMock).toHaveBeenCalledTimes(values.length)
  })

  it('withholds incomplete, inconsistent, unsafe or non-contract replies and ETags', async () => {
    const { fetchMediaRecoveryStatus } = await import('./media-recovery')
    const invalid = [
      null, {}, { ...base, private_path: '/private' }, { ...base, phase: 'future-phase' },
      { ...base, failure: 'raw private error' }, { ...base, phase: 'blocked' },
      { ...base, failure: 'recovery-failed' }, { ...base, phase: 'unavailable' },
      { ...base, phase: 'unavailable', failure: 'storage-unavailable', completed: 1 },
      { ...base, phase: 'unavailable', failure: 'storage-unavailable', saturated: true },
      { ...base, completed: -1 }, { ...base, completed: 0.5 }, { ...base, completed: Number.MAX_SAFE_INTEGER + 1 },
      { ...base, completed: true }, { ...base, saturated: true },
      ...Object.keys(base).map((field) => Object.fromEntries(Object.entries(base).filter(([key]) => key !== field))),
    ]
    for (const value of invalid) {
      fetchMock.mockResolvedValueOnce(Response.json(value))
      await expect(fetchMediaRecoveryStatus(signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
    fetchMock.mockResolvedValueOnce(Response.json(base, { headers: { ETag: '"1"' } }))
    await expect(fetchMediaRecoveryStatus(signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    expect(fetchMock).toHaveBeenCalledTimes(invalid.length + 1)
  })

  it('does not replay a network failure or disclose arbitrary server errors', async () => {
    const { fetchMediaRecoveryStatus } = await import('./media-recovery')
    fetchMock.mockRejectedValueOnce(new TypeError('private filesystem path'))
    await expect(fetchMediaRecoveryStatus(signal())).rejects.toMatchObject({ code: 'NETWORK_ERROR' })
    window.dispatchEvent(new Event('online'))
    fetchMock.mockResolvedValueOnce(Response.json({ error: { code: 'SERVICE_UNAVAILABLE', message: 'private error' } }, { status: 503 }))
    const error = await fetchMediaRecoveryStatus(signal()).catch((reason: unknown) => reason)
    expect(error).toMatchObject({ code: 'SERVICE_UNAVAILABLE', status: 503 })
    expect(JSON.stringify(error)).not.toContain('private error')
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it('bounds the read deadline and honors caller cancellation without retry', async () => {
    vi.useFakeTimers()
    try {
      fetchMock.mockImplementation((input) => new Promise((_resolve, reject) => {
        (input as Request).signal.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')), { once: true })
      }))
      const { fetchMediaRecoveryStatus } = await import('./media-recovery')
      const timed = expect(fetchMediaRecoveryStatus(signal())).rejects.toMatchObject({ code: 'REQUEST_TIMEOUT' })
      await vi.advanceTimersByTimeAsync(7999)
      expect(request().signal.aborted).toBe(false)
      await vi.advanceTimersByTimeAsync(1); await timed
      const controller = new AbortController()
      const canceled = expect(fetchMediaRecoveryStatus(controller.signal)).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
      controller.abort(); await canceled
      const early = new AbortController(); early.abort()
      await expect(fetchMediaRecoveryStatus(early.signal)).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
      expect(fetchMock).toHaveBeenCalledTimes(2); expect(vi.getTimerCount()).toBe(0)
    } finally { vi.useRealTimers() }
  })
})
