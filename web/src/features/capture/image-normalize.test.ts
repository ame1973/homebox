import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { captureVideoFrame, maxFrameBytes, type CapturedFrame } from '@/lib/capture-frame'
import { notifyPageSuspended } from '@/lib/session-lifecycle'
import { prepareSelectedImage, type PreparedSelectedImage } from './image-prepare-client'
import { normalizeSelectedImage, isImportedFrame } from './image-normalize'
import { newPhotoGeometry } from './geometry'
import { renderCapturedPhoto } from './render'

class FakeWorker {
  static mime = 'image/png'
  static orientation = 1
  onmessage: ((event: MessageEvent<unknown>) => void) | null = null
  onerror: ((event: ErrorEvent) => void) | null = null
  onmessageerror: (() => void) | null = null
  constructor(_url: URL, _options: WorkerOptions) {}
  postMessage() {
    queueMicrotask(() => this.onmessage?.({ data: { type: 'result', kind: 'prepared', mime: FakeWorker.mime, width: 4, height: 3,
      orientation: FakeWorker.orientation, blob: new Blob(['minimized'], { type: FakeWorker.mime }) } } as MessageEvent<unknown>))
  }
  terminate() {}
}
const draw = vi.fn(), transform = vi.fn(), closeBitmap = vi.fn(), context = vi.fn()
let callbacks: BlobCallback[], canvases: HTMLCanvasElement[], visibility: ReturnType<typeof vi.spyOn>, bitmap: ImageBitmap
const jpeg = () => new Blob(['JPEG'], { type: 'image/jpeg' })
const png = () => new Blob(['PNG'], { type: 'image/png' })
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((accept) => { resolve = accept })
  return { promise, resolve }
}

beforeEach(() => {
  FakeWorker.mime = 'image/png'; FakeWorker.orientation = 1; callbacks = []; canvases = []
  draw.mockReset(); transform.mockReset(); closeBitmap.mockReset(); context.mockReset()
  bitmap = { width: 4, height: 3, close: closeBitmap } as unknown as ImageBitmap
  visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
  const create = document.createElement.bind(document)
  vi.spyOn(document, 'createElement').mockImplementation(((tag: string, options?: ElementCreationOptions) => {
    const element = create(tag, options); if (tag === 'canvas') canvases.push(element as HTMLCanvasElement); return element
  }) as typeof document.createElement)
  vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockImplementation(function (_kind, options) {
    context(options); return { drawImage: draw, setTransform: transform, imageSmoothingEnabled: true } as unknown as CanvasRenderingContext2D
  } as typeof HTMLCanvasElement.prototype.getContext)
  vi.spyOn(HTMLCanvasElement.prototype, 'toBlob').mockImplementation(function (callback) { callbacks.push(callback) })
  vi.stubGlobal('createImageBitmap', vi.fn(async () => bitmap)); vi.stubGlobal('Worker', FakeWorker)
})
afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals(); vi.restoreAllMocks() })

async function prepare(orientation = 1, mime = 'image/png', client = prepareSelectedImage): Promise<PreparedSelectedImage> {
  FakeWorker.orientation = orientation; FakeWorker.mime = mime
  const result = await client(new Blob(['selected image data'], { type: 'ignored/type' }), new AbortController().signal)
  if (result.kind !== 'prepared') throw new Error(`Preparation failed: ${result.kind}`)
  return result
}
async function capture(client = captureVideoFrame): Promise<CapturedFrame> {
  const track = { readyState: 'live' }, stream = { getVideoTracks: () => [track], getAudioTracks: () => [] } as unknown as MediaStream
  const video = { srcObject: stream, readyState: 2, videoWidth: 4, videoHeight: 3 } as HTMLVideoElement
  const pending = client(video, stream, new AbortController().signal); callbacks.shift()?.(jpeg())
  const result = await pending
  if (result.kind !== 'captured') throw new Error('Capture failed')
  draw.mockClear(); transform.mockClear(); context.mockClear(); canvases = []
  return result
}
async function waitForEncoder() { await vi.waitFor(() => expect(callbacks).toHaveLength(1)) }

it.each([
  [1, 4, 3, [1, 0, 0, 1, 0, 0]], [2, 4, 3, [-1, 0, 0, 1, 4, 0]],
  [3, 4, 3, [-1, 0, 0, -1, 4, 3]], [4, 4, 3, [1, 0, 0, -1, 0, 3]],
  [5, 3, 4, [0, 1, 1, 0, 0, 0]], [6, 3, 4, [0, 1, -1, 0, 3, 0]],
  [7, 3, 4, [0, -1, -1, 0, 3, 4]], [8, 3, 4, [0, -1, 1, 0, 0, 4]],
] as const)('decodes the prepared image and applies EXIF orientation %s once', async (orientation, width, height, matrix) => {
  const source = await prepare(orientation), pending = normalizeSelectedImage(source, new AbortController().signal)
  await waitForEncoder()
  expect(createImageBitmap).toHaveBeenCalledExactlyOnceWith(source.blob, { imageOrientation: 'none' })
  expect(canvases[0]?.width).toBe(width); expect(canvases[0]?.height).toBe(height)
  expect(context).toHaveBeenCalledExactlyOnceWith({ alpha: true })
  expect(transform).toHaveBeenCalledExactlyOnceWith(...matrix)
  expect(draw).toHaveBeenCalledExactlyOnceWith(bitmap, 0, 0)
  expect(closeBitmap).toHaveBeenCalledOnce()
  expect(HTMLCanvasElement.prototype.toBlob).toHaveBeenLastCalledWith(expect.any(Function), 'image/png')
  const blob = png(); callbacks.shift()?.(blob)
  const result = await pending
  expect(result).toEqual({ kind: 'imported', blob, mime: 'image/png', width, height })
  expect(isImportedFrame(result)).toBe(true); expect(Object.isFrozen(result)).toBe(true)
  expect(isImportedFrame({ ...result })).toBe(false)
  expect(canvases[0]?.width).toBe(0); expect(canvases[0]?.height).toBe(0)
})

it.each([['image/jpeg', 'image/jpeg', false], ['image/png', 'image/png', true], ['image/webp', 'image/png', true]] as const)(
  'normalizes %s to %s while keeping alpha when applicable', async (input, output, alpha) => {
    const source = await prepare(1, input), pending = normalizeSelectedImage(source, new AbortController().signal)
    await waitForEncoder(); expect(context).toHaveBeenCalledExactlyOnceWith({ alpha })
    if (output === 'image/jpeg') expect(HTMLCanvasElement.prototype.toBlob).toHaveBeenLastCalledWith(expect.any(Function), output, 0.9)
    else expect(HTMLCanvasElement.prototype.toBlob).toHaveBeenLastCalledWith(expect.any(Function), output)
    callbacks.shift()?.(output === 'image/jpeg' ? jpeg() : png())
    const result = await pending
    expect(result.kind).toBe('imported')
    if (result.kind === 'imported') expect(result.blob.type).toBe(output)
  },
)

it('rejects unbranded sources and does not decode when cancelled, hidden or unavailable', async () => {
  const source = await prepare(), controller = new AbortController()
  expect(await normalizeSelectedImage({ ...source }, controller.signal)).toEqual({ kind: 'invalid' })
  expect(await normalizeSelectedImage(null as never, controller.signal)).toEqual({ kind: 'invalid' })
  controller.abort(); expect(await normalizeSelectedImage(source, controller.signal)).toEqual({ kind: 'cancelled' })
  visibility.mockReturnValue('hidden'); expect(await normalizeSelectedImage(source, new AbortController().signal)).toEqual({ kind: 'cancelled' })
  visibility.mockReturnValue('visible'); vi.stubGlobal('createImageBitmap', undefined)
  expect(await normalizeSelectedImage(source, new AbortController().signal)).toEqual({ kind: 'failed' })
  expect(canvases).toHaveLength(0)
})

it('rejects a decoded size mismatch before allocating a canvas', async () => {
  const source = await prepare(); bitmap = { width: 3, height: 4, close: closeBitmap } as unknown as ImageBitmap
  expect(await normalizeSelectedImage(source, new AbortController().signal)).toEqual({ kind: 'invalid' })
  expect(closeBitmap).toHaveBeenCalledOnce(); expect(canvases).toHaveLength(0)
})

it.each(['hidden', 'pagehide', 'session', 'abort'] as const)('discards a pending encoded photo after %s', async (reason) => {
  const source = await prepare(), controller = new AbortController(), pending = normalizeSelectedImage(source, controller.signal)
  await waitForEncoder(); const late = callbacks.shift()
  if (reason === 'hidden') { visibility.mockReturnValue('hidden'); document.dispatchEvent(new Event('visibilitychange')); visibility.mockReturnValue('visible') }
  if (reason === 'pagehide') window.dispatchEvent(new Event('pagehide'))
  if (reason === 'session') notifyPageSuspended()
  if (reason === 'abort') controller.abort()
  expect(await pending).toEqual({ kind: 'cancelled' }); expect(canvases[0]?.width).toBe(0)
  expect(await normalizeSelectedImage(source, new AbortController().signal)).toEqual({ kind: 'busy' })
  late?.(png())
  const next = normalizeSelectedImage(source, new AbortController().signal)
  await waitForEncoder(); callbacks.shift()?.(png()); expect((await next).kind).toBe('imported')
})

it('shares the noncancelable native slot with camera rendering across cancellation and duplicate callbacks', async () => {
  const frame = await capture(), source = await prepare(), geometry = newPhotoGeometry(4, 3)
  if (!geometry) throw new Error('Missing geometry')
  const pendingDecode = deferred<ImageBitmap>(), controller = new AbortController()
  vi.mocked(createImageBitmap).mockReturnValueOnce(pendingDecode.promise)
  const imported = normalizeSelectedImage(source, controller.signal)
  expect(await renderCapturedPhoto(frame, geometry, new AbortController().signal)).toEqual({ kind: 'busy' })
  controller.abort(); expect(await imported).toEqual({ kind: 'cancelled' })
  expect(await renderCapturedPhoto(frame, geometry, new AbortController().signal)).toEqual({ kind: 'busy' })
  pendingDecode.resolve(bitmap); await vi.waitFor(() => expect(closeBitmap).toHaveBeenCalledOnce())
  const rendered = renderCapturedPhoto(frame, geometry, new AbortController().signal)
  await waitForEncoder(); const old = callbacks.shift()
  expect(await normalizeSelectedImage(source, new AbortController().signal)).toEqual({ kind: 'busy' })
  old?.(jpeg()); expect((await rendered).kind).toBe('rendered')
  const next = normalizeSelectedImage(source, new AbortController().signal)
  await waitForEncoder(); old?.(jpeg())
  expect(await renderCapturedPhoto(frame, geometry, new AbortController().signal)).toEqual({ kind: 'busy' })
  callbacks.shift()?.(png()); expect((await next).kind).toBe('imported')
})

it.each(['decode', 'context', 'draw', 'encode', 'null', 'wrong type', 'empty', 'oversized'] as const)('returns safe %s failures without private details', async (failure) => {
  const source = await prepare()
  if (failure === 'decode') vi.mocked(createImageBitmap).mockRejectedValueOnce(new Error('/private decode'))
  if (failure === 'context') vi.mocked(HTMLCanvasElement.prototype.getContext).mockReturnValueOnce(null)
  if (failure === 'draw') draw.mockImplementationOnce(() => { throw new Error('/private draw') })
  if (failure === 'encode') vi.mocked(HTMLCanvasElement.prototype.toBlob).mockImplementationOnce(() => { throw new Error('/private encoder') })
  const pending = normalizeSelectedImage(source, new AbortController().signal)
  if (['null', 'wrong type', 'empty', 'oversized'].includes(failure)) {
    await waitForEncoder()
    const blob: Blob | null = failure === 'null' ? null : failure === 'wrong type' ? jpeg() : failure === 'empty' ? new Blob([], { type: 'image/png' }) : png()
    if (failure === 'oversized') Object.defineProperty(blob, 'size', { value: maxFrameBytes + 1 })
    callbacks.shift()?.(blob)
  }
  const result = await pending
  expect(result).toEqual({ kind: failure === 'oversized' ? 'too-large' : 'failed' })
  expect(JSON.stringify(result)).not.toContain('/private')
  if (canvases[0]) { expect(canvases[0].width).toBe(0); expect(canvases[0].height).toBe(0) }
})

it('latches a hung native decoder across normalization and rendering until reload', async () => {
  vi.resetModules()
  const freshClient = await import('./image-prepare-client'), freshNormalize = await import('./image-normalize')
  const freshCapture = await import('@/lib/capture-frame'), freshRender = await import('./render')
  const frame = await capture(freshCapture.captureVideoFrame), source = await prepare(1, 'image/png', freshClient.prepareSelectedImage)
  const geometry = newPhotoGeometry(4, 3)
  if (!geometry) throw new Error('Missing geometry')
  const pendingDecode = deferred<ImageBitmap>(), controller = new AbortController()
  vi.useFakeTimers(); vi.mocked(createImageBitmap).mockReturnValueOnce(pendingDecode.promise)
  const pending = freshNormalize.normalizeSelectedImage(source, controller.signal)
  controller.abort(); expect(await pending).toEqual({ kind: 'cancelled' })
  await vi.advanceTimersByTimeAsync(30_000)
  pendingDecode.resolve(bitmap); await Promise.resolve()
  expect(await freshNormalize.normalizeSelectedImage(source, new AbortController().signal)).toEqual({ kind: 'timeout' })
  expect(await freshRender.renderCapturedPhoto(frame, geometry, new AbortController().signal)).toEqual({ kind: 'timeout' })
})
