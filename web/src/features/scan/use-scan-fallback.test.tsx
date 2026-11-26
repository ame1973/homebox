import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import * as scan from '@/lib/scan-decoder'
import { notifyPageSuspended } from '@/lib/session-lifecycle'
import type { ScannedCode } from '@/lib/scan-native'
import { useNativeScan } from './use-native-scan'

vi.mock('@/lib/scan-decoder', () => ({ createScanDecoder: vi.fn() }))
type Decoder = NonNullable<Awaited<ReturnType<typeof scan.createScanDecoder>>>
const detect = vi.fn<Decoder['detect']>(), cancel = vi.fn(), close = vi.fn()
const decoder: Decoder = { formats: ['QR_CODE', 'EAN_13', 'UPC_A'], nativeFormats: ['EAN_13'], fallbackFormats: ['QR_CODE', 'UPC_A'], fallbackUnavailable: false, detect, cancel, close }
const video = { current: Object.assign(new EventTarget(), { readyState: 2, videoWidth: 640, videoHeight: 480, paused: false, ended: false }) as HTMLVideoElement }
function stream() {
  const track = Object.assign(new EventTarget(), { kind: 'video', readyState: 'live', enabled: true, muted: false }) as MediaStreamTrack
  const value = Object.assign(new EventTarget(), { getVideoTracks: () => [track], getTracks: () => [track] }) as unknown as MediaStream
  video.current.srcObject = value
  return value
}
let visibility: ReturnType<typeof vi.spyOn>
beforeEach(() => {
  detect.mockReset(); cancel.mockReset(); close.mockReset()
  vi.mocked(scan.createScanDecoder).mockReset().mockResolvedValue(decoder)
  visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
})
afterEach(() => { vi.useRealTimers(); vi.restoreAllMocks() })

it('reports native and worker coverage separately and preserves their exact results', async () => {
  detect.mockResolvedValueOnce([{ format: 'EAN_13', rawValue: '0042100005264' }, { format: 'UPC_A', rawValue: '042100005264' }])
  const halt = vi.fn(), input = stream()
  const hook = renderHook(() => useNativeScan({ stream: input, video, active: true, onHalt: halt }))
  await waitFor(() => expect(hook.result.current.results).toHaveLength(2))
  expect(hook.result.current.nativeFormats).toEqual(['EAN_13'])
  expect(hook.result.current.fallbackFormats).toEqual(['QR_CODE', 'UPC_A'])
  expect(hook.result.current.results).toEqual([{ format: 'EAN_13', rawValue: '0042100005264' }, { format: 'UPC_A', rawValue: '042100005264' }])
  expect(halt).toHaveBeenCalledOnce()
  hook.unmount(); expect(close).toHaveBeenCalledOnce()
})

it.each(['pause', 'hidden', 'pagehide', 'session', 'inactive', 'unmount'] as const)('aborts a worker frame synchronously on %s and rejects its late result', async (reason) => {
  let finish!: (codes: ScannedCode[]) => void
  detect.mockReturnValueOnce(new Promise((resolve) => { finish = resolve }))
  const input = stream(), halt = vi.fn()
  const hook = renderHook(({ active }) => useNativeScan({ stream: input, video, active, onHalt: halt }), { initialProps: { active: true } })
  await waitFor(() => expect(detect).toHaveBeenCalledOnce())
  const signal = detect.mock.calls[0]![1]
  expect(signal.aborted).toBe(false)
  act(() => {
    if (reason === 'pause') hook.result.current.pause()
    if (reason === 'hidden') { visibility.mockReturnValue('hidden'); document.dispatchEvent(new Event('visibilitychange')) }
    if (reason === 'pagehide') window.dispatchEvent(new Event('pagehide'))
    if (reason === 'session') notifyPageSuspended()
    if (reason === 'inactive') hook.rerender({ active: false })
    if (reason === 'unmount') hook.unmount()
  })
  expect(signal.aborted).toBe(true)
  if (reason !== 'inactive') expect(cancel).toHaveBeenCalled()
  await act(async () => { finish([{ format: 'QR_CODE', rawValue: 'private late worker result' }]); await Promise.resolve() })
  expect(halt).not.toHaveBeenCalled()
  if (reason !== 'unmount') { expect(hook.result.current.results).toBeUndefined(); hook.unmount() }
})

it.each(['pause', 'mute', 'addtrack', 'removetrack'] as const)('cancels a pending frame immediately on %s and scans a fresh frame after recovery', async (reason) => {
  let finish!: (codes: ScannedCode[]) => void
  detect.mockReturnValueOnce(new Promise((resolve) => { finish = resolve })).mockResolvedValueOnce([{ format: 'QR_CODE', rawValue: 'fresh' }])
  const input = stream(), track = input.getVideoTracks()[0]!, halt = vi.fn()
  const hook = renderHook(() => useNativeScan({ stream: input, video, active: true, onHalt: halt }))
  await waitFor(() => expect(detect).toHaveBeenCalledOnce())
  const signal = detect.mock.calls[0]![1]
  act(() => {
    if (reason === 'pause') { Object.defineProperty(video.current, 'paused', { configurable: true, writable: true, value: true }); video.current.dispatchEvent(new Event('pause')); Object.defineProperty(video.current, 'paused', { configurable: true, writable: true, value: false }) }
    else if (reason === 'mute') { Object.defineProperty(track, 'muted', { configurable: true, writable: true, value: true }); track.dispatchEvent(new Event('mute')); Object.defineProperty(track, 'muted', { configurable: true, writable: true, value: false }) }
    else input.dispatchEvent(new Event(reason))
  })
  expect(signal.aborted).toBe(true)
  expect(cancel).toHaveBeenCalled()
  await act(async () => { finish([{ format: 'QR_CODE', rawValue: 'stale' }]); await Promise.resolve() })
  expect(hook.result.current.results).toBeUndefined()
  expect(halt).not.toHaveBeenCalled()
  await waitFor(() => expect(hook.result.current.results).toEqual([{ format: 'QR_CODE', rawValue: 'fresh' }]))
  expect(halt).toHaveBeenCalledOnce()
  hook.unmount()
})

it.each(['result', 'error'] as const)('withholds a pending %s while a track remains disabled and resumes with a fresh frame', async (outcome) => {
  let finish!: (codes: ScannedCode[]) => void, fail!: (error: Error) => void
  detect.mockReturnValueOnce(new Promise((resolve, reject) => { finish = resolve; fail = reject })).mockResolvedValueOnce([{ format: 'QR_CODE', rawValue: 'fresh' }])
  const input = stream(), track = input.getVideoTracks()[0]!, halt = vi.fn()
  const hook = renderHook(() => useNativeScan({ stream: input, video, active: true, onHalt: halt }))
  await waitFor(() => expect(detect).toHaveBeenCalledOnce())
  await act(async () => {
    track.enabled = false
    if (outcome === 'result') finish([{ format: 'QR_CODE', rawValue: 'stale' }]); else fail(new Error('private stale error'))
    await Promise.resolve()
  })
  expect(hook.result.current.results).toBeUndefined()
  expect(hook.result.current.failed).toBe(false)
  expect(halt).not.toHaveBeenCalled()
  track.enabled = true
  await waitFor(() => expect(hook.result.current.results).toEqual([{ format: 'QR_CODE', rawValue: 'fresh' }]))
  hook.unmount()
})

it('aborts discovery on page suspension and closes a late worker', async () => {
  let finish!: (value: Decoder) => void
  vi.mocked(scan.createScanDecoder).mockReturnValueOnce(new Promise((resolve) => { finish = resolve }))
  const hook = renderHook(() => useNativeScan({ stream: null, video, active: true, onHalt: vi.fn() }))
  await waitFor(() => expect(scan.createScanDecoder).toHaveBeenCalledOnce())
  const signal = vi.mocked(scan.createScanDecoder).mock.calls[0]![0]
  act(() => { window.dispatchEvent(new Event('pagehide')) })
  expect(signal.aborted).toBe(true)
  await act(async () => { finish(decoder); await Promise.resolve() })
  expect(close).toHaveBeenCalledOnce()
  expect(hook.result.current.status).not.toBe('ready')
  hook.unmount()
})
