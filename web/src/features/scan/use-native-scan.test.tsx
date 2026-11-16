import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import * as native from '@/lib/scan-native'
import { notifyPageSuspended } from '@/lib/session-lifecycle'
import { useNativeScan } from './use-native-scan'

vi.mock('@/lib/scan-native', async (original) => ({ ...await original<typeof import('@/lib/scan-native')>(), createNativeBarcodeDecoder: vi.fn() }))
type Decoder = NonNullable<Awaited<ReturnType<typeof native.createNativeBarcodeDecoder>>>
const detect = vi.fn<Decoder['detect']>()
const decoder: Decoder = { formats: ['QR_CODE', 'EAN_13', 'UPC_A'], detect }
const video = { current: { readyState: 2, videoWidth: 640, videoHeight: 480 } as HTMLVideoElement }
function stream() {
  const track = { readyState: 'live' } as MediaStreamTrack
  const value = { getVideoTracks: () => [track] } as unknown as MediaStream
  video.current.srcObject = value
  return value
}
let visibility: ReturnType<typeof vi.spyOn>
beforeEach(() => {
  detect.mockReset()
  vi.mocked(native.createNativeBarcodeDecoder).mockReset().mockResolvedValue(decoder)
  visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
})
afterEach(() => { vi.useRealTimers(); vi.restoreAllMocks() })

it('reports actual native formats and requires an explicit retry after unavailable discovery', async () => {
  vi.mocked(native.createNativeBarcodeDecoder).mockResolvedValueOnce(undefined).mockResolvedValueOnce(decoder)
  const halt = vi.fn(), hook = renderHook(() => useNativeScan({ stream: null, video, active: true, onHalt: halt }))
  await waitFor(() => expect(hook.result.current.status).toBe('unavailable'))
  expect(hook.result.current.formats).toEqual([])
  expect(native.createNativeBarcodeDecoder).toHaveBeenCalledOnce()
  act(() => { hook.result.current.retryDiscovery() })
  await waitFor(() => expect(hook.result.current.status).toBe('ready'))
  expect(hook.result.current.formats).toEqual(['QR_CODE', 'EAN_13', 'UPC_A'])
  expect(native.createNativeBarcodeDecoder).toHaveBeenCalledTimes(2)
  expect(detect).not.toHaveBeenCalled()
  expect(halt).not.toHaveBeenCalled()
  hook.unmount()
})

it('times out unavailable discovery without accepting its late reply', async () => {
  vi.useFakeTimers()
  let finish!: (value: Decoder | undefined) => void
  vi.mocked(native.createNativeBarcodeDecoder).mockReturnValueOnce(new Promise((resolve) => { finish = resolve })).mockResolvedValueOnce(decoder)
  const hook = renderHook(() => useNativeScan({ stream: null, video, active: true, onHalt: vi.fn() }))
  await act(async () => { await vi.advanceTimersByTimeAsync(8_000) })
  expect(hook.result.current.status).toBe('unavailable')
  await act(async () => { finish(decoder); await Promise.resolve() })
  expect(hook.result.current.status).toBe('unavailable')
  act(() => { hook.result.current.retryDiscovery() })
  await act(async () => { await Promise.resolve() })
  expect(hook.result.current.status).toBe('ready')
  hook.unmount()
})

it('ignores an older discovery response after an explicit retry', async () => {
  let finish!: (value: Decoder | undefined) => void
  vi.mocked(native.createNativeBarcodeDecoder).mockReturnValueOnce(new Promise((resolve) => { finish = resolve })).mockResolvedValueOnce(decoder)
  const hook = renderHook(() => useNativeScan({ stream: null, video, active: true, onHalt: vi.fn() }))
  await waitFor(() => expect(native.createNativeBarcodeDecoder).toHaveBeenCalledOnce())
  act(() => { hook.result.current.retryDiscovery() })
  await waitFor(() => expect(hook.result.current.status).toBe('ready'))
  await act(async () => { finish(undefined); await Promise.resolve() })
  expect(hook.result.current.status).toBe('ready')
  expect(hook.result.current.formats).toEqual(decoder.formats)
  hook.unmount()
})

it('stops continuous scanning after exact deduplicated results and reports truncation', async () => {
  const unique: native.ScannedCode[] = Array.from({ length: 21 }, (_, index) => ({ format: 'UPC_A', rawValue: `000${index}` }))
  detect.mockResolvedValueOnce([unique[0]!, unique[0]!, ...unique.slice(1)])
  const halt = vi.fn(), input = stream()
  const hook = renderHook(() => useNativeScan({ stream: input, video, active: true, onHalt: halt }))
  await waitFor(() => expect(hook.result.current.results?.length).toBe(20))
  expect(hook.result.current.results?.[0]).toEqual({ format: 'UPC_A', rawValue: '0000' })
  expect(hook.result.current.truncated).toBe(true)
  expect(halt).toHaveBeenCalledOnce()
  expect(detect).toHaveBeenCalledOnce()
  act(() => { hook.result.current.clear() })
  expect(hook.result.current.results).toBeUndefined()
  hook.unmount()
})

it('ignores a late detection after pause and resumes only after an explicit action', async () => {
  let finish!: (codes: native.ScannedCode[]) => void
  detect.mockReturnValueOnce(new Promise((resolve) => { finish = resolve })).mockResolvedValueOnce([{ format: 'UPC_A', rawValue: '000123' }])
  const halt = vi.fn(), input = stream()
  const hook = renderHook(() => useNativeScan({ stream: input, video, active: true, onHalt: halt }))
  await waitFor(() => expect(detect).toHaveBeenCalledOnce())
  act(() => { hook.result.current.pause() })
  await act(async () => { finish([{ format: 'UPC_A', rawValue: 'stale' }]); await Promise.resolve() })
  expect(hook.result.current.results).toBeUndefined()
  expect(halt).not.toHaveBeenCalled()
  expect(detect).toHaveBeenCalledOnce()
  act(() => { hook.result.current.resume() })
  await waitFor(() => expect(hook.result.current.results).toEqual([{ format: 'UPC_A', rawValue: '000123' }]))
  expect(halt).toHaveBeenCalledOnce()
  hook.unmount()
})

it.each(['hidden', 'pagehide', 'session', 'unmount'] as const)('ignores a detection returned after %s', async (reason) => {
  let finish!: (codes: native.ScannedCode[]) => void
  detect.mockReturnValueOnce(new Promise((resolve) => { finish = resolve }))
  const halt = vi.fn(), input = stream()
  const hook = renderHook(() => useNativeScan({ stream: input, video, active: true, onHalt: halt }))
  await waitFor(() => expect(detect).toHaveBeenCalledOnce())
  act(() => {
    if (reason === 'hidden') { visibility.mockReturnValue('hidden'); document.dispatchEvent(new Event('visibilitychange')) }
    if (reason === 'pagehide') window.dispatchEvent(new Event('pagehide'))
    if (reason === 'session') notifyPageSuspended()
    if (reason === 'unmount') hook.unmount()
  })
  await act(async () => { finish([{ format: 'QR_CODE', rawValue: 'private late code' }]); await Promise.resolve() })
  expect(halt).not.toHaveBeenCalled()
  if (reason !== 'unmount') { expect(hook.result.current.results).toBeUndefined(); hook.unmount() }
})

it('clears already detected private text when the session becomes inactive', async () => {
  detect.mockResolvedValueOnce([{ format: 'QR_CODE', rawValue: 'private previous session' }])
  const input = stream(), halt = vi.fn()
  const hook = renderHook(({ active }) => useNativeScan({ stream: input, video, active, onHalt: halt }), { initialProps: { active: true } })
  await waitFor(() => expect(hook.result.current.results).toHaveLength(1))
  hook.rerender({ active: false })
  expect(hook.result.current.results).toBeUndefined()
  hook.rerender({ active: true })
  expect(hook.result.current.results).toBeUndefined()
  expect(detect).toHaveBeenCalledOnce()
  hook.unmount()
})

it('serializes detection across a stream switch and discards the old result', async () => {
  let finish!: (codes: native.ScannedCode[]) => void
  detect.mockReturnValueOnce(new Promise((resolve) => { finish = resolve })).mockResolvedValueOnce([{ format: 'EAN_13', rawValue: '0001234567890' }])
  const halt = vi.fn(), first = stream(), second = stream()
  video.current.srcObject = first
  const hook = renderHook(({ current }) => useNativeScan({ stream: current, video, active: true, onHalt: halt }), { initialProps: { current: first } })
  await waitFor(() => expect(detect).toHaveBeenCalledOnce())
  video.current.srcObject = second
  hook.rerender({ current: second })
  expect(detect).toHaveBeenCalledOnce()
  await act(async () => { finish([{ format: 'UPC_A', rawValue: 'stale first stream' }]); await Promise.resolve() })
  await waitFor(() => expect(hook.result.current.results).toEqual([{ format: 'EAN_13', rawValue: '0001234567890' }]))
  expect(detect).toHaveBeenCalledTimes(2)
  expect(halt).toHaveBeenCalledOnce()
  hook.unmount()
})

it('waits until the video is bound to the current stream before detecting', async () => {
  vi.useFakeTimers()
  detect.mockResolvedValue([])
  const input = stream(), halt = vi.fn()
  video.current.srcObject = null
  const hook = renderHook(() => useNativeScan({ stream: input, video, active: true, onHalt: halt }))
  await act(async () => { await Promise.resolve() })
  await act(async () => { await vi.advanceTimersByTimeAsync(1_000) })
  expect(detect).not.toHaveBeenCalled()
  video.current.srcObject = input
  await act(async () => { await vi.advanceTimersByTimeAsync(250) })
  expect(detect).toHaveBeenCalledOnce()
  hook.unmount()
})

it('throttles empty frame detection and never schedules overlapping calls', async () => {
  vi.useFakeTimers()
  detect.mockResolvedValue([])
  const halt = vi.fn(), input = stream()
  const hook = renderHook(() => useNativeScan({ stream: input, video, active: true, onHalt: halt }))
  await act(async () => { await Promise.resolve() })
  await act(async () => { await vi.advanceTimersByTimeAsync(0) })
  expect(detect).toHaveBeenCalledTimes(1)
  await act(async () => { await vi.advanceTimersByTimeAsync(249) })
  expect(detect).toHaveBeenCalledTimes(1)
  await act(async () => { await vi.advanceTimersByTimeAsync(1) })
  expect(detect).toHaveBeenCalledTimes(2)
  hook.unmount()
  await act(async () => { await vi.advanceTimersByTimeAsync(1_000) })
  expect(detect).toHaveBeenCalledTimes(2)
  expect(halt).not.toHaveBeenCalled()
})

it('stops on a runtime detection failure without retaining private diagnostics', async () => {
  detect.mockRejectedValueOnce(new Error('/private decoder diagnostics'))
  const halt = vi.fn(), input = stream()
  const hook = renderHook(() => useNativeScan({ stream: input, video, active: true, onHalt: halt }))
  await waitFor(() => expect(hook.result.current.failed).toBe(true))
  expect(JSON.stringify(hook.result.current)).not.toContain('/private decoder diagnostics')
  expect(halt).toHaveBeenCalledOnce()
  expect(detect).toHaveBeenCalledOnce()
  act(() => { hook.result.current.reset() })
  expect(hook.result.current.failed).toBe(false)
  hook.unmount()
})

// The timeout gate intentionally lasts until module reload; run this last.
it('keeps a hung native detector single-flight and latched across route remounts', async () => {
  vi.useFakeTimers()
  let finish!: (codes: native.ScannedCode[]) => void
  detect.mockReturnValueOnce(new Promise((resolve) => { finish = resolve }))
  const first = stream(), second = stream(), firstHalt = vi.fn(), secondHalt = vi.fn()
  video.current.srcObject = first
  const original = renderHook(() => useNativeScan({ stream: first, video, active: true, onHalt: firstHalt }))
  await act(async () => { await Promise.resolve() })
  await act(async () => { await vi.advanceTimersByTimeAsync(0) })
  expect(detect).toHaveBeenCalledOnce()
  original.unmount()
  video.current.srcObject = second
  const reopened = renderHook(() => useNativeScan({ stream: second, video, active: true, onHalt: secondHalt }))
  await act(async () => { await Promise.resolve() })
  await act(async () => { await vi.advanceTimersByTimeAsync(7_999) })
  expect(detect).toHaveBeenCalledOnce()
  await act(async () => { await vi.advanceTimersByTimeAsync(1) })
  expect(firstHalt).not.toHaveBeenCalled()
  expect(secondHalt).toHaveBeenCalledOnce()
  expect(reopened.result.current.failure).toBe('timeout')
  act(() => { reopened.result.current.reset(); reopened.result.current.retryDiscovery() })
  expect(reopened.result.current.failure).toBe('timeout')
  await act(async () => { finish([{ format: 'UPC_A', rawValue: 'private late timeout' }]); await Promise.resolve() })
  expect(reopened.result.current.results).toBeUndefined()
  reopened.unmount()
  const after = renderHook(() => useNativeScan({ stream: second, video, active: true, onHalt: vi.fn() }))
  expect(after.result.current.failure).toBe('timeout')
  await act(async () => { await vi.advanceTimersByTimeAsync(1_000) })
  expect(detect).toHaveBeenCalledOnce()
  after.unmount()
})
