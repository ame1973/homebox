import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/client'
import { notifyPageSuspended } from '@/lib/session-lifecycle'
import * as api from '@/lib/api/connectivity'
import { monitorForegroundConnectivity, type ConnectivityObservation } from './foreground-connectivity'

vi.mock('@/lib/api/connectivity', () => ({ probeConnectivity: vi.fn() }))
const read = vi.mocked(api.probeConnectivity)
const stops: (() => void)[] = []

beforeEach(() => {
  vi.useFakeTimers()
  vi.setSystemTime(new Date('2026-11-18T00:00:00Z'))
  vi.clearAllMocks()
  read.mockReset().mockResolvedValue({ status: 'ok' })
  Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' })
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: true })
})
afterEach(() => {
  for (const stop of stops.splice(0)) stop()
  vi.useRealTimers()
})

function start() {
  const states: ConnectivityObservation[] = []
  const monitor = monitorForegroundConnectivity((state) => { states.push(state) })
  stops.push(monitor.stop)
  return { states, ...monitor }
}
const settle = async () => { await vi.advanceTimersByTimeAsync(0) }
const phase = (states: ConnectivityObservation[]) => states.at(-1)?.phase

function visibility(value: 'visible' | 'hidden') {
  Object.defineProperty(document, 'visibilityState', { configurable: true, value })
  document.dispatchEvent(new Event('visibilitychange'))
}

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (error: unknown) => void
  const promise = new Promise<T>((done, fail) => { resolve = done; reject = fail })
  return { promise, resolve, reject }
}

describe('foreground authenticated connectivity monitor', () => {
  it('checks at most one time per minute while reachable without depending on navigator.onLine', async () => {
    Object.defineProperty(navigator, 'onLine', { configurable: true, value: false })
    const { states } = start()
    await settle()
    expect(read).toHaveBeenCalledTimes(1)
    expect(phase(states)).toBe('reachable')
    await vi.advanceTimersByTimeAsync(59_999)
    expect(read).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1)
    expect(read).toHaveBeenCalledTimes(2)
    expect(phase(states)).toBe('reachable')
  })

  it('backs off failed reads and resets its delay only after an actual success', async () => {
    read.mockRejectedValueOnce(new ApiError('NETWORK_ERROR'))
      .mockRejectedValueOnce(new ApiError('REQUEST_TIMEOUT'))
      .mockResolvedValueOnce({ status: 'degraded' })
      .mockRejectedValueOnce(new ApiError('NETWORK_ERROR'))
    const { states } = start()
    await settle()
    expect(phase(states)).toBe('unavailable')
    await vi.advanceTimersByTimeAsync(14_999); expect(read).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1); expect(read).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(29_999); expect(read).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(1); expect(read).toHaveBeenCalledTimes(3)
    expect(phase(states)).toBe('degraded')
    await vi.advanceTimersByTimeAsync(60_000); expect(read).toHaveBeenCalledTimes(4)
    await vi.advanceTimersByTimeAsync(15_000); expect(read).toHaveBeenCalledTimes(5)
    expect(phase(states)).toBe('reachable')
  })

  it('caps exponential backoff and does not let online events start overlapping reads', async () => {
    read.mockRejectedValue(new ApiError('NETWORK_ERROR'))
    const { check } = start()
    await settle()
    for (const [index, delay] of [15_000, 30_000, 60_000, 120_000, 240_000, 300_000, 300_000].entries()) {
      window.dispatchEvent(new Event('online'))
      await vi.advanceTimersByTimeAsync(delay - 1)
      expect(read).toHaveBeenCalledTimes(index + 1)
      await vi.advanceTimersByTimeAsync(1)
      expect(read).toHaveBeenCalledTimes(index + 2)
    }
    expect(check()).toBe(true)
    await settle()
    expect(read).toHaveBeenCalledTimes(9)
  })

  it('obeys Retry-After even on an explicit retry or online event', async () => {
    read.mockRejectedValueOnce(new ApiError('RATE_LIMITED', { status: 429, retryAfterSeconds: 120 }))
    const { states, check } = start()
    await settle()
    expect(phase(states)).toBe('rate-limited')
    expect(check()).toBe(false)
    window.dispatchEvent(new Event('online'))
    await vi.advanceTimersByTimeAsync(119_999); expect(read).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1); expect(read).toHaveBeenCalledTimes(2)
    expect(phase(states)).toBe('reachable')
  })

  it('bounds an invalidly large retry hint to one hour', async () => {
    read.mockRejectedValueOnce(new ApiError('RATE_LIMITED', { status: 429, retryAfterSeconds: 99_999 }))
    const { check } = start()
    await settle()
    await vi.advanceTimersByTimeAsync(3_599_999)
    expect(read).toHaveBeenCalledTimes(1)
    expect(check()).toBe(false)
    await vi.advanceTimersByTimeAsync(1)
    expect(read).toHaveBeenCalledTimes(2)
  })

  it('keeps rate-limit backoff across backgrounding and never polls while hidden', async () => {
    read.mockRejectedValueOnce(new ApiError('RATE_LIMITED', { status: 429, retryAfterSeconds: 120 }))
    const { states } = start()
    await settle()
    await vi.advanceTimersByTimeAsync(20_000)
    visibility('hidden')
    await vi.advanceTimersByTimeAsync(20_000)
    expect(read).toHaveBeenCalledTimes(1)
    visibility('visible')
    expect(phase(states)).toBe('rate-limited')
    await vi.advanceTimersByTimeAsync(79_999); expect(read).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1); expect(read).toHaveBeenCalledTimes(2)
    expect(phase(states)).toBe('reachable')
  })

  it('also respects Retry-After on a temporary service failure', async () => {
    read.mockRejectedValueOnce(new ApiError('SERVICE_UNAVAILABLE', { status: 503, retryAfterSeconds: 75 }))
    const { states, check } = start()
    await settle()
    expect(phase(states)).toBe('unavailable')
    expect(check()).toBe(false)
    await vi.advanceTimersByTimeAsync(74_999); expect(read).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1); expect(read).toHaveBeenCalledTimes(2)
    expect(phase(states)).toBe('reachable')
  })

  it('allows a deliberate retry of ordinary network failure without waiting for backoff', async () => {
    read.mockRejectedValueOnce(new ApiError('NETWORK_ERROR'))
    const { states, check } = start()
    await settle()
    expect(phase(states)).toBe('unavailable')
    expect(check()).toBe(true)
    await settle()
    expect(read).toHaveBeenCalledTimes(2)
    expect(phase(states)).toBe('reachable')
  })

  it('aborts on backgrounding, discards a late reply and never overlaps attempts', async () => {
    const late = deferred<api.ConnectivityStatus>()
    read.mockReturnValueOnce(late.promise)
    const { states } = start()
    await settle()
    expect(phase(states)).toBe('checking')
    const signal = read.mock.calls[0]?.[0]
    visibility('hidden')
    expect(signal?.aborted).toBe(true)
    expect(phase(states)).toBe('paused')
    visibility('visible')
    await settle()
    expect(read).toHaveBeenCalledTimes(1)
    late.resolve({ status: 'degraded' })
    await settle()
    expect(read).toHaveBeenCalledTimes(2)
    expect(phase(states)).toBe('reachable')
    expect(states.some((state) => state.phase === 'degraded')).toBe(false)
  })

  it('does not let an aborted old 401 block a resumed session', async () => {
    const late = deferred<api.ConnectivityStatus>()
    read.mockReturnValueOnce(late.promise)
    const { states } = start()
    await settle()
    visibility('hidden'); visibility('visible')
    late.reject(new ApiError('AUTHENTICATION_REQUIRED', { status: 401 }))
    await settle()
    expect(read).toHaveBeenCalledTimes(2)
    expect(phase(states)).toBe('reachable')
    expect(states.some((state) => state.phase === 'session-required')).toBe(false)
  })

  it('does not start while hidden and resumes only while the document is visible', async () => {
    visibility('hidden')
    const { states } = start()
    await settle()
    expect(phase(states)).toBe('paused')
    expect(read).not.toHaveBeenCalled()
    visibility('visible')
    await settle()
    expect(read).toHaveBeenCalledTimes(1)
    expect(phase(states)).toBe('reachable')
  })

  it.each([
    new ApiError('AUTHENTICATION_REQUIRED', { status: 401 }),
    new ApiError('AUTHENTICATION_REQUIRED'),
    new ApiError('PASSWORD_CHANGE_REQUIRED', { status: 403 }),
  ])('stops after an authentication failure until a caller establishes a new session', async (error) => {
    read.mockRejectedValueOnce(error)
    const { states, check } = start()
    await settle()
    expect(phase(states)).toBe('session-required')
    expect(check()).toBe(false)
    await vi.advanceTimersByTimeAsync(600_000)
    expect(read).toHaveBeenCalledTimes(1)
  })

  it('distinguishes an origin rejection from an offline claim', async () => {
    read.mockRejectedValueOnce(new ApiError('ORIGIN_REJECTED', { status: 403 }))
    const { states } = start()
    await settle()
    expect(phase(states)).toBe('access-blocked')
  })

  it('latches closed on session clear and rejects a late successful response', async () => {
    const late = deferred<api.ConnectivityStatus>()
    read.mockReturnValueOnce(late.promise)
    const { states, check } = start()
    await settle()
    const signal = read.mock.calls[0]?.[0]
    notifyPageSuspended()
    expect(signal?.aborted).toBe(true)
    expect(phase(states)).toBe('stopped')
    late.resolve({ status: 'ok' })
    await settle()
    visibility('hidden'); visibility('visible')
    await vi.advanceTimersByTimeAsync(600_000)
    expect(check()).toBe(false)
    expect(read).toHaveBeenCalledTimes(1)
    expect(states.some((state) => state.phase === 'reachable')).toBe(false)
  })

  it('stops on pagehide and teardown without letting late replies restart scheduling', async () => {
    const late = deferred<api.ConnectivityStatus>()
    read.mockReturnValueOnce(late.promise)
    const { states, stop } = start()
    await settle()
    window.dispatchEvent(new PageTransitionEvent('pagehide', { persisted: true }))
    expect(phase(states)).toBe('stopped')
    stop()
    late.reject(new ApiError('NETWORK_ERROR'))
    await settle()
    await vi.advanceTimersByTimeAsync(600_000)
    expect(read).toHaveBeenCalledTimes(1)
    expect(vi.getTimerCount()).toBe(0)
  })
})
