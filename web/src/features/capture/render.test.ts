import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { captureVideoFrame, maxFrameBytes, type CapturedFrame } from '@/lib/capture-frame'
import { notifyPageSuspended } from '@/lib/session-lifecycle'
import { newPhotoGeometry, type PhotoGeometry } from './geometry'
import { normalizeSelectedImage, type ImportedFrame } from './image-normalize'
import { prepareSelectedImage } from './image-prepare-client'
import { renderCapturedPhoto } from './render'

const draw = vi.fn(), transform = vi.fn(), closeBitmap = vi.fn()
let callbacks: BlobCallback[], canvases: HTMLCanvasElement[], visibility: ReturnType<typeof vi.spyOn>
let bitmap: ImageBitmap
const jpeg = () => new Blob([new Uint8Array([0xff, 0xd8, 0xff, 0xd9])], { type: 'image/jpeg' })
function deferredPromise<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((accept) => { resolve = accept })
  return { promise, resolve }
}

beforeEach(() => {
  callbacks = []; canvases = []; draw.mockReset(); transform.mockReset(); closeBitmap.mockReset()
  bitmap = { width: 4, height: 3, close: closeBitmap } as unknown as ImageBitmap
  visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
  const create = document.createElement.bind(document)
  vi.spyOn(document, 'createElement').mockImplementation(((tag: string, options?: ElementCreationOptions) => {
    const element = create(tag, options)
    if (tag === 'canvas') canvases.push(element as HTMLCanvasElement)
    return element
  }) as typeof document.createElement)
  vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockImplementation((() => ({ drawImage: draw, setTransform: transform, imageSmoothingEnabled: true } as unknown as CanvasRenderingContext2D)) as unknown as typeof HTMLCanvasElement.prototype.getContext)
  vi.spyOn(HTMLCanvasElement.prototype, 'toBlob').mockImplementation(function (callback) { callbacks.push(callback) })
  vi.stubGlobal('createImageBitmap', vi.fn(async () => bitmap))
})
afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals(); vi.restoreAllMocks() })

async function capture(captureFrame = captureVideoFrame): Promise<CapturedFrame> {
  const track = { readyState: 'live', enabled: true, muted: false }
  const stream = { getVideoTracks: () => [track], getAudioTracks: () => [] } as unknown as MediaStream
  const video = { srcObject: stream, readyState: 2, paused: false, videoWidth: 4, videoHeight: 3 } as HTMLVideoElement
  const pending = captureFrame(video, stream, new AbortController().signal)
  callbacks.shift()?.(jpeg())
  const frame = await pending
  if (frame.kind !== 'captured') throw new Error('Capture fixture failed')
  draw.mockClear(); transform.mockClear(); canvases = []
  return frame
}
async function imported(mime: 'image/jpeg' | 'image/png'): Promise<ImportedFrame> {
  class WorkerFixture {
    onmessage: ((event: MessageEvent<unknown>) => void) | null = null
    postMessage() {
      queueMicrotask(() => this.onmessage?.({ data: { type: 'result', kind: 'prepared', mime, width: 4, height: 3,
        orientation: 1, blob: new Blob(['minimized'], { type: mime }) } } as MessageEvent<unknown>))
    }
    terminate() {}
  }
  vi.stubGlobal('Worker', WorkerFixture)
  const source = await prepareSelectedImage(new Blob(['selected image bytes']), new AbortController().signal)
  if (source.kind !== 'prepared') throw new Error('Preparation failed')
  const pending = normalizeSelectedImage(source, new AbortController().signal)
  await waitForEncoder()
  callbacks.shift()?.(new Blob(['frame'], { type: mime }))
  const result = await pending
  if (result.kind !== 'imported') throw new Error('Normalization failed')
  draw.mockClear(); transform.mockClear(); closeBitmap.mockClear(); canvases = []
  vi.mocked(createImageBitmap).mockClear(); vi.mocked(HTMLCanvasElement.prototype.getContext).mockClear()
  return result
}
function geometry(turns: 0 | 1 | 2 | 3 = 0): PhotoGeometry {
  const source = newPhotoGeometry(4, 3)
  if (!source) throw new Error('Geometry fixture failed')
  return { ...source, crop: { x: 1, y: 1, width: 3, height: 2 }, turns }
}
async function waitForEncoder() { await vi.waitFor(() => expect(callbacks).toHaveLength(1)) }

it.each([
  [0, 3, 2, [1, 0, 0, 1, 0, 0]], [1, 2, 3, [0, 1, -1, 0, 2, 0]],
  [2, 3, 2, [-1, 0, 0, -1, 3, 2]], [3, 2, 3, [0, -1, 1, 0, 0, 3]],
] as const)('draws crop and quarter-turn %s once from the original captured pixels', async (turns, width, height, matrix) => {
  const frame = await capture(), source = geometry(turns), controller = new AbortController()
  const pending = renderCapturedPhoto(frame, source, controller.signal)
  await waitForEncoder()
  expect(createImageBitmap).toHaveBeenCalledExactlyOnceWith(frame.blob, { imageOrientation: 'none' })
  expect(canvases[0]?.width).toBe(width); expect(canvases[0]?.height).toBe(height)
  expect(transform).toHaveBeenCalledExactlyOnceWith(...matrix)
  expect(draw).toHaveBeenCalledExactlyOnceWith(bitmap, 1, 1, 3, 2, 0, 0, 3, 2)
  expect(closeBitmap).toHaveBeenCalledOnce()
  expect(HTMLCanvasElement.prototype.toBlob).toHaveBeenLastCalledWith(expect.any(Function), 'image/jpeg', 0.9)
  const blob = jpeg(); callbacks.shift()?.(blob)
  expect(await pending).toEqual({ kind: 'rendered', blob, width, height })
  expect(canvases[0]?.width).toBe(0); expect(canvases[0]?.height).toBe(0)
  expect(source).toEqual(geometry(turns))
})

it.each([['image/png', true], ['image/jpeg', false]] as const)('renders branded imported %s from original pixels without losing transparency intent', async (mime, alpha) => {
  const frame = await imported(mime), pending = renderCapturedPhoto(frame, geometry(1), new AbortController().signal)
  await waitForEncoder()
  expect(createImageBitmap).toHaveBeenCalledExactlyOnceWith(frame.blob, { imageOrientation: 'none' })
  expect(HTMLCanvasElement.prototype.getContext).toHaveBeenCalledExactlyOnceWith('2d', { alpha })
  expect(transform).toHaveBeenCalledExactlyOnceWith(0, 1, -1, 0, 2, 0)
  expect(draw).toHaveBeenCalledExactlyOnceWith(bitmap, 1, 1, 3, 2, 0, 0, 3, 2)
  if (mime === 'image/png') expect(HTMLCanvasElement.prototype.toBlob).toHaveBeenLastCalledWith(expect.any(Function), mime)
  else expect(HTMLCanvasElement.prototype.toBlob).toHaveBeenLastCalledWith(expect.any(Function), mime, 0.9)
  const blob = new Blob(['output'], { type: mime }); callbacks.shift()?.(blob)
  expect(await pending).toEqual({ kind: 'rendered', blob, width: 2, height: 3 })
  expect(await renderCapturedPhoto({ ...frame }, geometry(), new AbortController().signal)).toEqual({ kind: 'invalid' })
})

it('refuses an altered imported MIME instead of flattening transparent pixels', async () => {
  const frame = await imported('image/png')
  Object.defineProperty(frame.blob, 'type', { value: 'image/jpeg', configurable: true })
  const pending = renderCapturedPhoto(frame, geometry(), new AbortController().signal)
  await Promise.resolve(); callbacks.shift()?.(jpeg())
  expect(await pending).toEqual({ kind: 'invalid' })
  expect(createImageBitmap).not.toHaveBeenCalled()
})

it('accepts only the unchanged camera capture and matching valid source geometry', async () => {
  const frame = await capture(), signal = new AbortController().signal
  for (const [candidate, shape] of [
    [{ ...frame }, geometry()], [frame, { ...geometry(), sourceWidth: 5 }],
    [frame, { ...geometry(), crop: { x: 3, y: 2, width: 2, height: 2 } }],
  ] as const) {
    expect(await renderCapturedPhoto(candidate, shape, signal)).toEqual({ kind: 'invalid' })
  }
  expect(createImageBitmap).not.toHaveBeenCalled()
  expect(canvases).toHaveLength(0)
})

it('does not start decoding when already canceled, hidden, or unsupported', async () => {
  const frame = await capture(), controller = new AbortController()
  expect(Object.isFrozen(frame)).toBe(true)
  controller.abort()
  expect(await renderCapturedPhoto(frame, geometry(), controller.signal)).toEqual({ kind: 'cancelled' })
  visibility.mockReturnValue('hidden')
  expect(await renderCapturedPhoto(frame, geometry(), new AbortController().signal)).toEqual({ kind: 'cancelled' })
  visibility.mockReturnValue('visible')
  vi.stubGlobal('createImageBitmap', undefined)
  expect(await renderCapturedPhoto(frame, geometry(), new AbortController().signal)).toEqual({ kind: 'failed' })
  expect(canvases).toHaveLength(0)
})

it('rejects changed decoded dimensions and closes the bitmap before allocating a canvas', async () => {
  const frame = await capture()
  bitmap = { width: 3, height: 4, close: closeBitmap } as unknown as ImageBitmap
  expect(await renderCapturedPhoto(frame, geometry(), new AbortController().signal)).toEqual({ kind: 'invalid' })
  expect(closeBitmap).toHaveBeenCalledOnce(); expect(canvases).toHaveLength(0)
})

it('snapshots geometry before awaiting the browser decoder', async () => {
  const frame = await capture(), original = geometry(1), mutable = { ...original, crop: { ...original.crop } }
  const pending = renderCapturedPhoto(frame, mutable, new AbortController().signal)
  mutable.crop.x = 0; mutable.crop.width = 1
  await waitForEncoder()
  expect(draw).toHaveBeenCalledExactlyOnceWith(bitmap, 1, 1, 3, 2, 0, 0, 3, 2)
  callbacks.shift()?.(jpeg())
  expect((await pending).kind).toBe('rendered')
})

it.each(['hidden', 'pagehide', 'session', 'abort'] as const)('discards an encoded result after %s and clears its canvas', async (reason) => {
  const frame = await capture(), controller = new AbortController()
  const pending = renderCapturedPhoto(frame, geometry(), controller.signal)
  await waitForEncoder()
  const late = callbacks.shift()
  if (reason === 'hidden') { visibility.mockReturnValue('hidden'); document.dispatchEvent(new Event('visibilitychange')); visibility.mockReturnValue('visible') }
  if (reason === 'pagehide') window.dispatchEvent(new Event('pagehide'))
  if (reason === 'session') notifyPageSuspended()
  if (reason === 'abort') controller.abort()
  expect(await pending).toEqual({ kind: 'cancelled' })
  expect(canvases[0]?.width).toBe(0); expect(canvases[0]?.height).toBe(0)
  expect(await renderCapturedPhoto(frame, geometry(), new AbortController().signal)).toEqual({ kind: 'busy' })
  late?.(jpeg())
  const next = renderCapturedPhoto(frame, geometry(), new AbortController().signal)
  await waitForEncoder(); callbacks.shift()?.(jpeg())
  expect((await next).kind).toBe('rendered')
})

it('discards a decoded bitmap after cancellation and does not free a newer slot on duplicate callbacks', async () => {
  const frame = await capture(), deferred = deferredPromise<ImageBitmap>(), controller = new AbortController()
  vi.mocked(createImageBitmap).mockReturnValueOnce(deferred.promise)
  const pending = renderCapturedPhoto(frame, geometry(), controller.signal)
  controller.abort(); expect(await pending).toEqual({ kind: 'cancelled' })
  expect(await renderCapturedPhoto(frame, geometry(), new AbortController().signal)).toEqual({ kind: 'busy' })
  deferred.resolve(bitmap); await vi.waitFor(() => expect(closeBitmap).toHaveBeenCalledOnce())
  const second = renderCapturedPhoto(frame, geometry(), new AbortController().signal)
  await waitForEncoder(); const old = callbacks.shift(); old?.(jpeg()); expect((await second).kind).toBe('rendered')
  const third = renderCapturedPhoto(frame, geometry(), new AbortController().signal)
  await waitForEncoder(); old?.(jpeg())
  expect(await renderCapturedPhoto(frame, geometry(), new AbortController().signal)).toEqual({ kind: 'busy' })
  callbacks.shift()?.(jpeg()); expect((await third).kind).toBe('rendered')
})

it.each(['decode', 'context', 'draw', 'encode', 'null', 'wrong type', 'empty', 'oversized'] as const)('handles %s failures without exposing private errors', async (failure) => {
  const frame = await capture()
  if (failure === 'decode') vi.mocked(createImageBitmap).mockRejectedValueOnce(new Error('/private decode'))
  if (failure === 'context') vi.mocked(HTMLCanvasElement.prototype.getContext).mockReturnValueOnce(null)
  if (failure === 'draw') draw.mockImplementationOnce(() => { throw new Error('/private draw') })
  if (failure === 'encode') vi.mocked(HTMLCanvasElement.prototype.toBlob).mockImplementationOnce(() => { throw new Error('/private encoder') })
  const pending = renderCapturedPhoto(frame, geometry(), new AbortController().signal)
  if (['null', 'wrong type', 'empty', 'oversized'].includes(failure)) {
    await waitForEncoder()
    const blob: Blob | null = failure === 'null' ? null : failure === 'wrong type' ? new Blob(['x'], { type: 'image/png' }) : failure === 'empty' ? new Blob([], { type: 'image/jpeg' }) : jpeg()
    if (failure === 'oversized') Object.defineProperty(blob, 'size', { value: maxFrameBytes + 1 })
    callbacks.shift()?.(blob)
  }
  const result = await pending
  expect(result).toEqual({ kind: failure === 'oversized' ? 'too-large' : 'failed' })
  expect(JSON.stringify(result)).not.toContain('/private')
  if (canvases[0]) { expect(canvases[0].width).toBe(0); expect(canvases[0].height).toBe(0) }
})

it('latches a hung encoder and ignores its late result until a real reload', async () => {
  vi.resetModules()
  const freshCapture = await import('@/lib/capture-frame'), freshRender = await import('./render')
  const frame = await capture(freshCapture.captureVideoFrame)
  vi.useFakeTimers()
  const pending = freshRender.renderCapturedPhoto(frame, geometry(), new AbortController().signal)
  await Promise.resolve()
  expect(callbacks).toHaveLength(1)
  const late = callbacks.shift()
  await vi.advanceTimersByTimeAsync(30_000)
  expect(await pending).toEqual({ kind: 'timeout' })
  late?.(jpeg())
  expect(await freshRender.renderCapturedPhoto(frame, geometry(), new AbortController().signal)).toEqual({ kind: 'timeout' })
})

it('latches a decoder that hangs after cancellation until a real reload', async () => {
  const frame = await capture(), deferred = deferredPromise<ImageBitmap>(), controller = new AbortController()
  vi.useFakeTimers(); vi.mocked(createImageBitmap).mockReturnValueOnce(deferred.promise)
  const pending = renderCapturedPhoto(frame, geometry(), controller.signal)
  controller.abort(); expect(await pending).toEqual({ kind: 'cancelled' })
  await vi.advanceTimersByTimeAsync(30_000)
  deferred.resolve(bitmap); await Promise.resolve()
  expect(await renderCapturedPhoto(frame, geometry(), new AbortController().signal)).toEqual({ kind: 'timeout' })
})
