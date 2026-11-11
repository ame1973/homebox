import { StrictMode, type ReactNode } from 'react'
import { act, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { clearSessionQueries } from '@/features/auth/use-auth-action'
import { createQueryClient } from '@/lib/query'
import { useCameraStream } from './use-camera-stream'

class FakeTrack extends EventTarget {
  readonly kind: 'video' | 'audio'
  readyState: 'live' | 'ended' = 'live'
  stop = vi.fn(() => { this.readyState = 'ended' })
  constructor(kind: 'video' | 'audio' = 'video') { super(); this.kind = kind }
  end() { this.readyState = 'ended'; this.dispatchEvent(new Event('ended')) }
}

function fakeStream(...tracks: FakeTrack[]) {
  return {
    getTracks: () => tracks,
    getVideoTracks: () => tracks.filter((track) => track.kind === 'video'),
    getAudioTracks: () => tracks.filter((track) => track.kind === 'audio'),
  } as unknown as MediaStream
}

const acquire = vi.fn<(constraints?: MediaStreamConstraints) => Promise<MediaStream>>()
const originalDevices = Object.getOwnPropertyDescriptor(navigator, 'mediaDevices')
let visibility: ReturnType<typeof vi.spyOn>

beforeEach(() => {
  acquire.mockReset()
  vi.stubGlobal('isSecureContext', true)
  Object.defineProperty(navigator, 'mediaDevices', { configurable: true, value: { getUserMedia: acquire } })
  visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
})

afterEach(() => {
  if (originalDevices) Object.defineProperty(navigator, 'mediaDevices', originalDevices)
  else Reflect.deleteProperty(navigator, 'mediaDevices')
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

it('starts only on request with the live rear stream, and stops every track on completion', async () => {
  const video = new FakeTrack(), stream = fakeStream(video)
  acquire.mockResolvedValue(stream)
  const hook = renderHook(() => useCameraStream(true))
  expect(hook.result.current.status).toBe('idle')
  expect(acquire).not.toHaveBeenCalled()
  await act(async () => { await hook.result.current.start() })
  expect(acquire).toHaveBeenCalledExactlyOnceWith({ audio: false, video: { facingMode: { ideal: 'environment' } } })
  expect(hook.result.current).toMatchObject({ status: 'active', stream, facing: 'environment', error: null })
  act(() => { hook.result.current.stop() })
  expect(video.stop).toHaveBeenCalledOnce()
  expect(hook.result.current).toMatchObject({ status: 'idle', stream: null, error: null })
  act(() => { hook.result.current.stop() })
  expect(video.stop).toHaveBeenCalledOnce()
  hook.unmount()
})

it('stops the old camera before requesting the other facing mode, and requires explicit retry after failure', async () => {
  const rear = new FakeTrack(), front = new FakeTrack()
  acquire.mockResolvedValueOnce(fakeStream(rear)).mockImplementationOnce(async () => {
    expect(rear.readyState).toBe('ended')
    throw new DOMException('private device detail', 'NotReadableError')
  }).mockResolvedValueOnce(fakeStream(front))
  const hook = renderHook(() => useCameraStream(true))
  await act(async () => { await hook.result.current.start() })
  await act(async () => { await hook.result.current.start('user') })
  expect(hook.result.current).toMatchObject({ status: 'error', facing: 'user', error: 'busy', stream: null })
  expect(JSON.stringify(hook.result.current)).not.toContain('private device detail')
  expect(acquire).toHaveBeenCalledTimes(2)
  await act(async () => { await hook.result.current.start('user') })
  expect(acquire).toHaveBeenLastCalledWith({ audio: false, video: { facingMode: { ideal: 'user' } } })
  expect(hook.result.current.status).toBe('active')
  hook.unmount()
  expect(front.stop).toHaveBeenCalledOnce()
})

it.each([
  ['NotAllowedError', 'permission-denied'], ['SecurityError', 'permission-denied'],
  ['NotFoundError', 'not-found'], ['OverconstrainedError', 'not-found'],
  ['NotReadableError', 'busy'], ['AbortError', 'interrupted'], ['TypeError', 'unsupported'], ['NotSupportedError', 'unsupported'],
  ['UnexpectedError', 'unknown'],
] as const)('maps %s to a safe %s state without retry', async (name, expected) => {
  acquire.mockRejectedValue(new DOMException('private diagnostic', name))
  const hook = renderHook(() => useCameraStream(true))
  await act(async () => { await hook.result.current.start() })
  expect(hook.result.current).toMatchObject({ status: 'error', error: expected, stream: null })
  expect(JSON.stringify(hook.result.current)).not.toContain('private diagnostic')
  expect(acquire).toHaveBeenCalledOnce()
  hook.unmount()
})

it('refuses insecure, unsupported and hidden starts without requesting a device or restarting on visibility', async () => {
  const hook = renderHook(() => useCameraStream(true))
  vi.stubGlobal('isSecureContext', false)
  await act(async () => { await hook.result.current.start() })
  expect(hook.result.current.error).toBe('insecure')
  vi.stubGlobal('isSecureContext', true)
  Reflect.deleteProperty(navigator, 'mediaDevices')
  await act(async () => { await hook.result.current.start() })
  expect(hook.result.current.error).toBe('unsupported')
  Object.defineProperty(navigator, 'mediaDevices', { configurable: true, value: { getUserMedia: acquire } })
  visibility.mockReturnValue('hidden')
  await act(async () => { await hook.result.current.start() })
  expect(hook.result.current.status).toBe('idle')
  visibility.mockReturnValue('visible')
  act(() => { document.dispatchEvent(new Event('visibilitychange')) })
  expect(acquire).not.toHaveBeenCalled()
  hook.unmount()
})

it.each(['stop', 'hidden', 'pagehide', 'logout', 'inactive', 'unmount'] as const)(
  'releases a stream that resolves after %s and never starts again automatically', async (reason) => {
    let resolve!: (stream: MediaStream) => void
    acquire.mockImplementation(() => new Promise((done) => { resolve = done }))
    const hook = renderHook(({ active }) => useCameraStream(active), { initialProps: { active: true } })
    let pending!: Promise<void>
    await act(async () => { pending = hook.result.current.start() })
    expect(hook.result.current.status).toBe('requesting')
    const client = createQueryClient()
    act(() => {
      if (reason === 'stop') hook.result.current.stop()
      if (reason === 'hidden') { visibility.mockReturnValue('hidden'); document.dispatchEvent(new Event('visibilitychange')) }
      if (reason === 'pagehide') window.dispatchEvent(new Event('pagehide'))
      if (reason === 'logout') clearSessionQueries(client)
      if (reason === 'inactive') hook.rerender({ active: false })
      if (reason === 'unmount') hook.unmount()
    })
    const video = new FakeTrack()
    await act(async () => { resolve(fakeStream(video)); await pending })
    expect(video.stop).toHaveBeenCalledOnce()
    if (reason !== 'unmount') expect(hook.result.current.status).toBe('idle')
    visibility.mockReturnValue('visible')
    act(() => { document.dispatchEvent(new Event('visibilitychange')); window.dispatchEvent(new Event('pageshow')) })
    expect(acquire).toHaveBeenCalledOnce()
    if (reason !== 'unmount') hook.unmount()
    client.clear()
  },
)

it.each(['hidden', 'pagehide'] as const)('stops an active stream on %s and needs a new gesture to restart', async (reason) => {
  const first = new FakeTrack(), second = new FakeTrack()
  acquire.mockResolvedValueOnce(fakeStream(first)).mockResolvedValueOnce(fakeStream(second))
  const hook = renderHook(() => useCameraStream(true))
  await act(async () => { await hook.result.current.start() })
  act(() => {
    if (reason === 'hidden') { visibility.mockReturnValue('hidden'); document.dispatchEvent(new Event('visibilitychange')) }
    else window.dispatchEvent(new Event('pagehide'))
  })
  expect(first.stop).toHaveBeenCalledOnce()
  expect(hook.result.current).toMatchObject({ status: 'idle', stream: null })
  visibility.mockReturnValue('visible')
  act(() => { document.dispatchEvent(new Event('visibilitychange')); window.dispatchEvent(new Event('pageshow')) })
  expect(acquire).toHaveBeenCalledOnce()
  await act(async () => { await hook.result.current.start() })
  expect(hook.result.current.status).toBe('active')
  hook.unmount()
  expect(second.stop).toHaveBeenCalledOnce()
})

it('blocks starts while a session is inactive without starting when it becomes active', async () => {
  const hook = renderHook(({ active }) => useCameraStream(active), { initialProps: { active: false } })
  await act(async () => { await hook.result.current.start() })
  act(() => { hook.rerender({ active: true }) })
  expect(hook.result.current.status).toBe('idle')
  expect(acquire).not.toHaveBeenCalled()
  hook.unmount()
})

it('invalidates an older pending request when a newer camera wins', async () => {
  const finish: ((stream: MediaStream) => void)[] = []
  acquire.mockImplementation(() => new Promise((resolve) => { finish.push(resolve) }))
  const hook = renderHook(() => useCameraStream(true))
  let older!: Promise<void>, newer!: Promise<void>
  await act(async () => { older = hook.result.current.start(); newer = hook.result.current.start('user') })
  const first = new FakeTrack(), second = new FakeTrack(), finishOlder = finish[0], finishNewer = finish[1]
  if (!finishOlder || !finishNewer) throw new Error('Both camera requests must be pending')
  await act(async () => { finishNewer(fakeStream(second)); await newer })
  await act(async () => { finishOlder(fakeStream(first)); await older })
  expect(first.stop).toHaveBeenCalledOnce()
  expect(second.stop).not.toHaveBeenCalled()
  expect(hook.result.current).toMatchObject({ status: 'active', facing: 'user' })
  hook.unmount()
  expect(second.stop).toHaveBeenCalledOnce()
})

it('stops an acquired stream if visibility changed before the hidden event arrived', async () => {
  let resolve!: (stream: MediaStream) => void
  acquire.mockImplementation(() => new Promise((done) => { resolve = done }))
  const hook = renderHook(() => useCameraStream(true))
  let pending!: Promise<void>
  await act(async () => { pending = hook.result.current.start() })
  visibility.mockReturnValue('hidden')
  const video = new FakeTrack()
  await act(async () => { resolve(fakeStream(video)); await pending })
  expect(video.stop).toHaveBeenCalledOnce()
  expect(hook.result.current.status).toBe('idle')
  hook.unmount()
})

it('ignores a stale request error after a newer camera has started', async () => {
  let reject!: (reason: unknown) => void
  const live = new FakeTrack()
  acquire.mockImplementationOnce(() => new Promise((_, fail) => { reject = fail })).mockResolvedValueOnce(fakeStream(live))
  const hook = renderHook(() => useCameraStream(true))
  let older!: Promise<void>
  await act(async () => { older = hook.result.current.start() })
  await act(async () => { await hook.result.current.start('user') })
  await act(async () => { reject(new DOMException('private', 'NotAllowedError')); await older })
  expect(hook.result.current).toMatchObject({ status: 'active', facing: 'user', error: null })
  expect(live.stop).not.toHaveBeenCalled()
  hook.unmount()
  expect(live.stop).toHaveBeenCalledOnce()
})

it('stops an active stream immediately on session purge and blocks reacquisition until a new active session', async () => {
  const first = new FakeTrack(), second = new FakeTrack()
  acquire.mockResolvedValueOnce(fakeStream(first)).mockResolvedValueOnce(fakeStream(second))
  const hook = renderHook(({ active }) => useCameraStream(active), { initialProps: { active: true } })
  const client = createQueryClient()
  await act(async () => { await hook.result.current.start() })
  act(() => { clearSessionQueries(client) })
  expect(first.stop).toHaveBeenCalledOnce()
  await act(async () => { await hook.result.current.start() })
  expect(acquire).toHaveBeenCalledOnce()
  act(() => { hook.rerender({ active: false }) })
  act(() => { hook.rerender({ active: true }) })
  await act(async () => { await hook.result.current.start() })
  expect(acquire).toHaveBeenCalledTimes(2)
  expect(hook.result.current.status).toBe('active')
  hook.unmount()
  expect(second.stop).toHaveBeenCalledOnce()
  client.clear()
})

it('stops on a cross-tab BroadcastChannel signal and closes its listener on unmount', async () => {
  const channels: FakeChannel[] = []
  class FakeChannel {
    onmessage: ((event: { data: unknown }) => void) | null = null
    readonly name: string
    close = vi.fn()
    postMessage = vi.fn()
    constructor(name: string) { this.name = name; channels.push(this) }
  }
  vi.stubGlobal('BroadcastChannel', FakeChannel)
  const video = new FakeTrack()
  acquire.mockResolvedValue(fakeStream(video))
  const hook = renderHook(() => useCameraStream(true))
  const receiver = channels[0]
  if (!receiver) throw new Error('Missing session listener')
  await act(async () => { await hook.result.current.start() })
  act(() => { receiver.onmessage?.({ data: 'unrelated' }) })
  expect(video.stop).not.toHaveBeenCalled()
  act(() => { receiver.onmessage?.({ data: 'cleared' }) })
  expect(video.stop).toHaveBeenCalledOnce()
  expect(hook.result.current.status).toBe('idle')
  await act(async () => { await hook.result.current.start() })
  expect(acquire).toHaveBeenCalledOnce()
  const client = createQueryClient()
  act(() => { clearSessionQueries(client) })
  const sender = channels[1]
  if (!sender) throw new Error('Missing session sender')
  expect(sender.name).toBe('homebox:session-cleared')
  expect(sender.postMessage).toHaveBeenCalledExactlyOnceWith('cleared')
  expect(sender.close).toHaveBeenCalledOnce()
  expect(localStorage.getItem('homebox:session-cleared')).toBeNull()
  hook.unmount()
  expect(receiver.close).toHaveBeenCalledOnce()
  client.clear()
})

it('stops on the transient cross-tab storage signal when BroadcastChannel is unavailable', async () => {
  vi.stubGlobal('BroadcastChannel', undefined)
  const video = new FakeTrack()
  acquire.mockResolvedValue(fakeStream(video))
  const hook = renderHook(() => useCameraStream(true))
  await act(async () => { await hook.result.current.start() })
  act(() => {
    window.dispatchEvent(new StorageEvent('storage', { key: 'homebox:session-cleared', newValue: null }))
    window.dispatchEvent(new StorageEvent('storage', { key: 'other', newValue: 'cleared' }))
  })
  expect(video.stop).not.toHaveBeenCalled()
  act(() => { window.dispatchEvent(new StorageEvent('storage', { key: 'homebox:session-cleared', newValue: 'cleared' })) })
  expect(video.stop).toHaveBeenCalledOnce()
  await act(async () => { await hook.result.current.start() })
  expect(acquire).toHaveBeenCalledOnce()
  hook.unmount()
})

it('releases a pending stream acquired after a cross-tab session signal', async () => {
  vi.stubGlobal('BroadcastChannel', undefined)
  let resolve!: (stream: MediaStream) => void
  acquire.mockImplementation(() => new Promise((done) => { resolve = done }))
  const hook = renderHook(() => useCameraStream(true))
  let pending!: Promise<void>
  await act(async () => { pending = hook.result.current.start() })
  act(() => { window.dispatchEvent(new StorageEvent('storage', { key: 'homebox:session-cleared', newValue: 'cleared' })) })
  const video = new FakeTrack()
  await act(async () => { resolve(fakeStream(video)); await pending })
  expect(video.stop).toHaveBeenCalledOnce()
  expect(hook.result.current.status).toBe('idle')
  hook.unmount()
})

it('still purges a local camera when both cross-tab transports fail', async () => {
  vi.stubGlobal('BroadcastChannel', class { constructor() { throw new Error('disabled') } })
  vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new DOMException('disabled', 'SecurityError') })
  const video = new FakeTrack()
  acquire.mockResolvedValue(fakeStream(video))
  const hook = renderHook(() => useCameraStream(true))
  const client = createQueryClient()
  await act(async () => { await hook.result.current.start() })
  act(() => { clearSessionQueries(client) })
  expect(video.stop).toHaveBeenCalledOnce()
  expect(hook.result.current.status).toBe('idle')
  expect(localStorage.getItem('homebox:session-cleared')).toBeNull()
  hook.unmount()
  client.clear()
})

it('stops an active stream when its session becomes inactive without reopening it on reactivation', async () => {
  const video = new FakeTrack()
  acquire.mockResolvedValue(fakeStream(video))
  const hook = renderHook(({ active }) => useCameraStream(active), { initialProps: { active: true } })
  await act(async () => { await hook.result.current.start() })
  act(() => { hook.rerender({ active: false }) })
  expect(video.stop).toHaveBeenCalledOnce()
  expect(hook.result.current.status).toBe('idle')
  act(() => { hook.rerender({ active: true }) })
  expect(acquire).toHaveBeenCalledOnce()
  expect(hook.result.current.status).toBe('idle')
  hook.unmount()
})

it('stops an acquired stream if inspecting the returned tracks unexpectedly fails', async () => {
  const video = new FakeTrack()
  const stream = { getTracks: () => [video], getVideoTracks: () => { throw new Error('private browser detail') } } as unknown as MediaStream
  acquire.mockResolvedValue(stream)
  const hook = renderHook(() => useCameraStream(true))
  await act(async () => { await hook.result.current.start() })
  expect(video.stop).toHaveBeenCalledOnce()
  expect(hook.result.current).toMatchObject({ status: 'error', error: 'unknown', stream: null })
  expect(JSON.stringify(hook.result.current)).not.toContain('private browser detail')
  hook.unmount()
})

it('stops the remaining tracks if the device ends a video track', async () => {
  const first = new FakeTrack(), second = new FakeTrack()
  acquire.mockResolvedValue(fakeStream(first, second))
  const hook = renderHook(() => useCameraStream(true))
  await act(async () => { await hook.result.current.start() })
  act(() => { first.end() })
  expect(first.stop).toHaveBeenCalledOnce()
  expect(second.stop).toHaveBeenCalledOnce()
  expect(hook.result.current).toMatchObject({ status: 'error', error: 'interrupted', stream: null })
  hook.unmount()
})

it.each(['empty', 'ended', 'audio'] as const)('rejects an unusable or unexpected %s stream and releases it', async (kind) => {
  const video = new FakeTrack(), audio = new FakeTrack('audio')
  if (kind === 'ended') video.readyState = 'ended'
  const tracks = kind === 'empty' ? [] : kind === 'audio' ? [video, audio] : [video]
  acquire.mockResolvedValue(fakeStream(...tracks))
  const hook = renderHook(() => useCameraStream(true))
  await act(async () => { await hook.result.current.start() })
  expect(hook.result.current).toMatchObject({ status: 'error', error: 'not-found', stream: null })
  for (const track of tracks) expect(track.stop).toHaveBeenCalledOnce()
  hook.unmount()
})

it('survives Strict Mode remount checks while releasing the actual stream on unmount', async () => {
  const video = new FakeTrack()
  acquire.mockResolvedValue(fakeStream(video))
  const wrapper = ({ children }: { children: ReactNode }) => <StrictMode>{children}</StrictMode>
  const hook = renderHook(() => useCameraStream(true), { wrapper })
  await act(async () => { await hook.result.current.start() })
  expect(hook.result.current.status).toBe('active')
  hook.unmount()
  expect(video.stop).toHaveBeenCalledOnce()
})
