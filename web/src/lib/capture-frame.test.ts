import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { notifyPageSuspended } from './session-lifecycle'
import { captureVideoFrame, maxFrameBytes, maxFrameDimension, maxFramePixels } from './capture-frame'

class Track {
  readyState: 'live' | 'ended' = 'live'
}
function source() {
  const track = new Track()
  const stream = { getVideoTracks: () => [track], getAudioTracks: () => [] } as unknown as MediaStream
  const video = { srcObject: stream, readyState: 2, videoWidth: 640, videoHeight: 480 } as HTMLVideoElement
  return { track, stream, video }
}
const drawing = vi.fn()
let callback: BlobCallback | undefined, canvas: HTMLCanvasElement | undefined
let visibility: ReturnType<typeof vi.spyOn>
beforeEach(() => {
  callback = undefined; canvas = undefined; drawing.mockReset()
  visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
  const create = document.createElement.bind(document)
  vi.spyOn(document, 'createElement').mockImplementation(((tag: string, options?: ElementCreationOptions) => {
    const element = create(tag, options)
    if (tag === 'canvas') canvas = element as HTMLCanvasElement
    return element
  }) as typeof document.createElement)
  vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockImplementation((() => ({ drawImage: drawing } as unknown as CanvasRenderingContext2D)) as unknown as typeof HTMLCanvasElement.prototype.getContext)
  vi.spyOn(HTMLCanvasElement.prototype, 'toBlob').mockImplementation(function (done) { callback = done })
})
afterEach(() => { vi.useRealTimers(); vi.restoreAllMocks() })
const jpeg = () => new Blob([new Uint8Array([0xff, 0xd8, 0xff, 0xd9])], { type: 'image/jpeg' })

it('captures the current live video frame into a bounded JPEG without opening or stopping a camera', async () => {
  const { track, stream, video } = source(), controller = new AbortController()
  const pending = captureVideoFrame(video, stream, controller.signal)
  expect(drawing).toHaveBeenCalledExactlyOnceWith(video, 0, 0, 640, 480)
  expect(canvas?.width).toBe(640); expect(canvas?.height).toBe(480)
  expect(HTMLCanvasElement.prototype.toBlob).toHaveBeenCalledExactlyOnceWith(expect.any(Function), 'image/jpeg', 0.9)
  const blob = jpeg()
  callback?.(blob)
  expect(await pending).toEqual({ kind: 'captured', blob, width: 640, height: 480 })
  expect(canvas?.width).toBe(0); expect(canvas?.height).toBe(0)
  expect(track.readyState).toBe('live')
})

it.each(['hidden', 'wrong stream', 'ended track', 'audio', 'not ready', 'empty width'] as const)('refuses a %s source without encoding', async (problem) => {
  const { track, stream, video } = source()
  if (problem === 'hidden') visibility.mockReturnValue('hidden')
  if (problem === 'wrong stream') video.srcObject = null
  if (problem === 'ended track') track.readyState = 'ended'
  if (problem === 'audio') vi.spyOn(stream, 'getAudioTracks').mockReturnValue([{} as MediaStreamTrack])
  if (problem === 'not ready') Reflect.set(video, 'readyState', 1)
  if (problem === 'empty width') Reflect.set(video, 'videoWidth', 0)
  expect(await captureVideoFrame(video, stream, new AbortController().signal)).toEqual({ kind: 'not-ready' })
  expect(drawing).not.toHaveBeenCalled()
  expect(HTMLCanvasElement.prototype.toBlob).not.toHaveBeenCalled()
})

it('rejects dimensions or pixel counts outside server limits before allocating a canvas', async () => {
  const { stream, video } = source()
  for (const [width, height] of [[maxFrameDimension + 1, 1], [1, maxFrameDimension + 1], [maxFrameDimension, maxFrameDimension], [NaN, 1], [1.5, 1]]) {
    Reflect.set(video, 'videoWidth', width!); Reflect.set(video, 'videoHeight', height!)
    expect(await captureVideoFrame(video, stream, new AbortController().signal)).toEqual({ kind: 'too-large' })
  }
  expect(maxFramePixels).toBe(64_000_000)
  expect(canvas).toBeUndefined()
})

it('accepts exact dimension and pixel ceilings without changing the frame', async () => {
  const { stream, video } = source()
  for (const [width, height] of [[maxFrameDimension, 1], [16_000, 4_000]]) {
    Reflect.set(video, 'videoWidth', width!); Reflect.set(video, 'videoHeight', height!)
    const pending = captureVideoFrame(video, stream, new AbortController().signal)
    callback?.(jpeg())
    expect(await pending).toMatchObject({ kind: 'captured', width, height })
  }
})

it.each(['missing canvas', 'missing encoder', 'canvas exception', 'drawing exception', 'encoding exception', 'null output', 'wrong MIME', 'empty output', 'large output'] as const)('returns a safe failure for %s', async (failure) => {
  if (failure === 'missing canvas') vi.mocked(HTMLCanvasElement.prototype.getContext).mockReturnValueOnce(null)
  if (failure === 'missing encoder') Reflect.set(HTMLCanvasElement.prototype, 'toBlob', undefined)
  if (failure === 'canvas exception') vi.mocked(HTMLCanvasElement.prototype.getContext).mockImplementationOnce(() => { throw new Error('/private canvas diagnostic') })
  if (failure === 'drawing exception') drawing.mockImplementationOnce(() => { throw new Error('/private image diagnostic') })
  if (failure === 'encoding exception') vi.mocked(HTMLCanvasElement.prototype.toBlob).mockImplementationOnce(() => { throw new Error('/private encoder diagnostic') })
  const { stream, video } = source(), pending = captureVideoFrame(video, stream, new AbortController().signal)
  if (failure === 'null output') callback?.(null)
  if (failure === 'wrong MIME') callback?.(new Blob(['not JPEG'], { type: 'image/png' }))
  if (failure === 'empty output') callback?.(new Blob([], { type: 'image/jpeg' }))
  if (failure === 'large output') { const blob = jpeg(); Object.defineProperty(blob, 'size', { value: maxFrameBytes + 1 }); callback?.(blob) }
  const result = await pending
  expect(result).toEqual({ kind: failure === 'large output' ? 'too-large' : 'failed' })
  expect(JSON.stringify(result)).not.toContain('/private')
  if (canvas) { expect(canvas.width).toBe(0); expect(canvas.height).toBe(0) }
})

it('rejects an already canceled request before accessing the video or allocating a canvas', async () => {
  const { stream, video } = source(), controller = new AbortController()
  controller.abort()
  expect(await captureVideoFrame(video, stream, controller.signal)).toEqual({ kind: 'cancelled' })
  expect(canvas).toBeUndefined()
  expect(drawing).not.toHaveBeenCalled()
})

it('discards a late canceled result and keeps encoding single-flight until the native callback completes', async () => {
  const { stream, video } = source(), controller = new AbortController()
  const pending = captureVideoFrame(video, stream, controller.signal), oldCallback = callback
  controller.abort()
  expect(await pending).toEqual({ kind: 'cancelled' })
  expect(canvas?.width).toBe(0); expect(canvas?.height).toBe(0)
  expect(await captureVideoFrame(video, stream, new AbortController().signal)).toEqual({ kind: 'busy' })
  oldCallback?.(jpeg())
  const next = captureVideoFrame(video, stream, new AbortController().signal)
  callback?.(jpeg())
  expect((await next).kind).toBe('captured')
})

it('does not release a newer encoder slot when an old callback fires twice', async () => {
  const { stream, video } = source(), controller = new AbortController()
  const first = captureVideoFrame(video, stream, controller.signal), oldCallback = callback
  oldCallback?.(jpeg())
  expect((await first).kind).toBe('captured')
  const second = captureVideoFrame(video, stream, controller.signal), currentCallback = callback
  oldCallback?.(jpeg())
  expect(await captureVideoFrame(video, stream, controller.signal)).toEqual({ kind: 'busy' })
  currentCallback?.(jpeg())
  expect((await second).kind).toBe('captured')
})

it.each(['hidden', 'pagehide', 'session', 'track stopped', 'stream changed'] as const)('discards a result returned after %s', async (reason) => {
  const { track, stream, video } = source(), controller = new AbortController()
  const pending = captureVideoFrame(video, stream, controller.signal)
  if (reason === 'hidden') { visibility.mockReturnValue('hidden'); document.dispatchEvent(new Event('visibilitychange')); visibility.mockReturnValue('visible') }
  if (reason === 'pagehide') window.dispatchEvent(new Event('pagehide'))
  if (reason === 'session') notifyPageSuspended()
  if (reason === 'track stopped') track.readyState = 'ended'
  if (reason === 'stream changed') video.srcObject = null
  callback?.(jpeg())
  expect(await pending).toEqual({ kind: 'cancelled' })
  expect(canvas?.width).toBe(0); expect(canvas?.height).toBe(0)
})

// A timed-out native encoder is intentionally locked until module reload.
it('latches an uncancelable hung encoder until the page is reloaded', async () => {
  vi.useFakeTimers()
  const { stream, video } = source(), controller = new AbortController()
  const pending = captureVideoFrame(video, stream, controller.signal), late = callback
  await vi.advanceTimersByTimeAsync(30_000)
  expect(await pending).toEqual({ kind: 'timeout' })
  late?.(jpeg())
  expect(await captureVideoFrame(video, stream, new AbortController().signal)).toEqual({ kind: 'timeout' })
})

it('also latches a hung encoder after its caller cancels', async () => {
  vi.useFakeTimers(); vi.resetModules()
  const fresh = await import('./capture-frame')
  const { stream, video } = source(), controller = new AbortController()
  const pending = fresh.captureVideoFrame(video, stream, controller.signal), late = callback
  controller.abort()
  expect(await pending).toEqual({ kind: 'cancelled' })
  expect(await fresh.captureVideoFrame(video, stream, new AbortController().signal)).toEqual({ kind: 'busy' })
  await vi.advanceTimersByTimeAsync(30_000)
  late?.(jpeg())
  expect(await fresh.captureVideoFrame(video, stream, new AbortController().signal)).toEqual({ kind: 'timeout' })
})
