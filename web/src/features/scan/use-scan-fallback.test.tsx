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
const video = { current: { readyState: 2, videoWidth: 640, videoHeight: 480 } as HTMLVideoElement }
function stream() {
  const track = { readyState: 'live' } as MediaStreamTrack
  const value = { getVideoTracks: () => [track] } as unknown as MediaStream
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
