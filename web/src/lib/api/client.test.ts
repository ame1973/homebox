import { beforeEach, describe, expect, it, vi } from 'vitest'
import { z } from 'zod'

const fetchMock = vi.fn<typeof fetch>()
beforeEach(() => { vi.resetModules(); fetchMock.mockReset(); vi.stubGlobal('fetch', fetchMock) })

async function request(signal = new AbortController().signal) {
  const { fetchHealth } = await import('./client')
  return fetchHealth(signal)
}

describe('same-origin health boundary', () => {
  it.each(['ok', 'degraded'])('validates %s and disables HTTP caching and redirects', async (status) => {
    fetchMock.mockResolvedValue(Response.json({ status, future_field: 'ignored' }))
    expect(await request()).toEqual({ status })
    const input = fetchMock.mock.calls[0]?.[0] as Request
    expect(input.url).toBe('http://localhost/api/v1/health/live')
    expect(input.method).toBe('GET')
    expect(input.cache).toBe('no-store')
    expect(input.credentials).toBe('same-origin')
    expect(input.redirect).toBe('error')
  })

  it.each([null, {}, { status: true }, { status: 'unknown' }])('rejects malformed data %j', async (payload) => {
    fetchMock.mockResolvedValue(Response.json(payload))
    await expect(request()).rejects.toThrow()
  })

  it('rejects an HTTP error even when its JSON resembles success', async () => {
    fetchMock.mockResolvedValue(Response.json({ status: 'ok' }, { status: 503 }))
    await expect(request()).rejects.toThrow('503')
  })

  it('rejects malformed JSON and network failures', async () => {
    fetchMock.mockResolvedValueOnce(new Response('<html>proxy error</html>'))
    await expect(request()).rejects.toThrow()
    fetchMock.mockRejectedValueOnce(new TypeError('Network unavailable'))
    await expect(request()).rejects.toThrow('Network unavailable')
  })

  function abortableFetch() {
    fetchMock.mockImplementation((input) => new Promise((_, reject) => {
      const signal = (input as Request).signal
      const rejectAbort = () => reject(new DOMException('Aborted', 'AbortError'))
      if (signal.aborted) rejectAbort()
      else signal.addEventListener('abort', rejectAbort, { once: true })
    }))
  }

  it('cancels an in-flight request and clears its deadline', async () => {
    vi.useFakeTimers()
    abortableFetch()
    const { fetchHealth } = await import('./client')
    const controller = new AbortController()
    const result = fetchHealth(controller.signal)
    const rejection = expect(result).rejects.toThrow('Aborted')
    controller.abort()
    await rejection
    expect(vi.getTimerCount()).toBe(0)
  })

  it('honors an already-aborted caller', async () => {
    abortableFetch()
    const controller = new AbortController()
    controller.abort()
    await expect(request(controller.signal)).rejects.toThrow('Aborted')
  })

  it('times out rather than leaving the UI permanently pending', async () => {
    vi.useFakeTimers()
    abortableFetch()
    const { fetchHealth } = await import('./client')
    const result = fetchHealth(new AbortController().signal)
    const rejection = expect(result).rejects.toThrow('Request timed out')
    await vi.advanceTimersByTimeAsync(8_000)
    await rejection
    expect(vi.getTimerCount()).toBe(0)
  })
})

describe('safe domain error boundary', () => {
  it.each([
    ['INVALID_TAXONOMY_URL', 400], ['CATEGORY_CYCLE', 409],
    ['CATEGORY_HAS_CHILDREN', 409], ['TAG_NAME_CONFLICT', 409],
    ['INVALID_CATALOG_URL', 400], ['ITEM_HAS_HISTORY', 409],
    ['ARBITRARY_PRIVATE_DIAGNOSTIC', 503],
  ] as const)('retains only allowlisted metadata for %s', async (code, status) => {
    const { ApiError, requestData } = await import('./client')
    const payload = { error: { code, request_id: 'safe-request',
      message: 'secret=do-not-retain', details: { path: '/private/database' } } }
    const failure = await requestData(new AbortController().signal,
      async () => ({ error: payload, response: Response.json(payload, { status }) }), z.never(),
    ).catch((error: unknown) => error)
    expect(failure).toBeInstanceOf(ApiError)
    expect(failure).toMatchObject({ code: code === 'ARBITRARY_PRIVATE_DIAGNOSTIC' ? 'UNKNOWN_ERROR' : code, status,
      requestId: code === 'ARBITRARY_PRIVATE_DIAGNOSTIC' ? undefined : 'safe-request' })
    expect(String(failure)).not.toContain('secret=')
    expect(JSON.stringify(failure)).not.toMatch(/secret=|private\/database|message|details|cause/)
  })
})
