import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createZXingBarcodeDecoder } from './scan-zxing-client'

type Request = { id: number; pixels: Uint8ClampedArray; width: number; height: number; formats: string[] }
class FakeWorker {
  static instances: FakeWorker[] = []
  static ready = true
  static reply: unknown = []
  static hold = false
  onmessage: ((event: MessageEvent<unknown>) => void) | null = null
  onerror: ((event: ErrorEvent) => void) | null = null
  onmessageerror: (() => void) | null = null
  terminated = false
  messages: Request[] = []
  readonly url: URL
  readonly options: WorkerOptions
  constructor(url: URL, options: WorkerOptions) {
    this.url = url; this.options = options
    FakeWorker.instances.push(this)
    if (FakeWorker.ready) queueMicrotask(() => this.emit({ type: 'ready' }))
  }
  emit(data: unknown) { this.onmessage?.({ data } as MessageEvent<unknown>) }
  postMessage(value: Request, transfer: Transferable[]) {
    const clone = structuredClone(value, { transfer })
    this.messages.push(clone)
    if (!FakeWorker.hold) queueMicrotask(() => this.emit({ type: 'result', id: clone.id, codes: FakeWorker.reply }))
  }
  terminate() { this.terminated = true }
}

let lastCanvas: HTMLCanvasElement | undefined
let track: { kind: string; readyState: string }
let video: HTMLVideoElement
let draw: ReturnType<typeof vi.fn>
function rememberCanvas(canvas: HTMLCanvasElement) { lastCanvas = canvas }

beforeEach(() => {
  FakeWorker.instances = []; FakeWorker.ready = true; FakeWorker.reply = []; FakeWorker.hold = false
  vi.stubGlobal('Worker', FakeWorker)
  track = { kind: 'video', readyState: 'live' }
  const stream = { getTracks: () => [track] }
  video = document.createElement('video')
  Object.defineProperties(video, {
    srcObject: { configurable: true, value: stream, writable: true },
    videoWidth: { configurable: true, value: 80 }, videoHeight: { configurable: true, value: 40 },
    readyState: { configurable: true, value: 4 },
  })
  Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' })
  draw = vi.fn()
  vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockImplementation(function (this: HTMLCanvasElement) {
    rememberCanvas(this)
    const rgba = new Uint8ClampedArray(this.width * this.height * 4)
    for (let i = 0; i < rgba.length; i += 4) { rgba[i] = 255; rgba[i + 3] = i === 0 ? 0 : 255 }
    return { drawImage: draw, getImageData: () => ({ data: rgba }) } as unknown as CanvasRenderingContext2D
  })
})
afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals(); vi.restoreAllMocks(); lastCanvas = undefined })

describe('ZXing worker adapter', () => {
  it('starts a same-origin module worker, transfers only bounded grayscale pixels and preserves results', async () => {
    FakeWorker.reply = [{ format: 'EAN_13', rawValue: '0042100005264' }, { format: 'UPC_A', rawValue: '042100005264' }]
    const decoder = await createZXingBarcodeDecoder(['EAN_13', 'UPC_A'], new AbortController().signal)
    const worker = FakeWorker.instances[0]!
    expect(worker.options).toEqual({ type: 'module' })
    expect(worker.url.pathname).toMatch(/scan-zxing-worker\.ts$/)
    expect(worker.messages).toEqual([])
    const result = await decoder.detect(video, new AbortController().signal)
    expect(result).toEqual(FakeWorker.reply)
    expect(draw).toHaveBeenCalledOnce()
    expect(worker.messages[0]).toMatchObject({ width: 80, height: 40, formats: ['EAN_13', 'UPC_A'] })
    expect(worker.messages[0]?.pixels.length).toBe(3200)
    expect([...worker.messages[0]!.pixels.slice(0, 2)]).toEqual([255, 76])
    expect(lastCanvas?.width).toBe(0)
    expect(lastCanvas?.height).toBe(0)
    await decoder.detect(video, new AbortController().signal)
    expect(FakeWorker.instances).toHaveLength(1)
    decoder.cancel(); expect(worker.terminated).toBe(true)
    await decoder.detect(video, new AbortController().signal)
    expect(FakeWorker.instances).toHaveLength(2)
    decoder.close(); expect(FakeWorker.instances[1]?.terminated).toBe(true)
    await expect(decoder.detect(video, new AbortController().signal)).rejects.toMatchObject({ name: 'AbortError' })
  })

  it('bounds large camera frames before reading pixels', async () => {
    Object.defineProperties(video, { videoWidth: { configurable: true, value: 3840 }, videoHeight: { configurable: true, value: 2160 } })
    const decoder = await createZXingBarcodeDecoder(['QR_CODE'], new AbortController().signal)
    await decoder.detect(video, new AbortController().signal)
    const message = FakeWorker.instances[0]!.messages[0]!
    expect([message.width, message.height, message.pixels.length]).toEqual([1920, 1080, 1920 * 1080])
    decoder.close()
  })

  it('terminates pending work on abort and rejects stale results before a new worker starts', async () => {
    FakeWorker.hold = true
    const decoder = await createZXingBarcodeDecoder(['QR_CODE'], new AbortController().signal)
    const controller = new AbortController(), worker = FakeWorker.instances[0]!
    const pending = decoder.detect(video, controller.signal)
    await vi.waitFor(() => expect(worker.messages).toHaveLength(1))
    await expect(decoder.detect(video, new AbortController().signal)).rejects.toThrow('Barcode decoder unavailable')
    controller.abort()
    await expect(pending).rejects.toMatchObject({ name: 'AbortError' })
    expect(worker.terminated).toBe(true)
    worker.emit({ type: 'result', id: 1, codes: [{ format: 'QR_CODE', rawValue: 'stale private result' }] })
    FakeWorker.hold = false; FakeWorker.reply = [{ format: 'QR_CODE', rawValue: 'fresh' }]
    expect(await decoder.detect(video, new AbortController().signal)).toEqual(FakeWorker.reply)
    expect(FakeWorker.instances).toHaveLength(2)
    decoder.close()
  })

  it('terminates a worker whose initial load is aborted or times out', async () => {
    FakeWorker.ready = false
    const controller = new AbortController(), loading = createZXingBarcodeDecoder(['QR_CODE'], controller.signal)
    controller.abort()
    await expect(loading).rejects.toMatchObject({ name: 'AbortError' })
    expect(FakeWorker.instances[0]?.terminated).toBe(true)
    vi.useFakeTimers()
    const timed = createZXingBarcodeDecoder(['EAN_8'], new AbortController().signal)
    const assertion = expect(timed).rejects.toThrow('Barcode decoder unavailable')
    await vi.advanceTimersByTimeAsync(8000)
    await assertion
    expect(FakeWorker.instances[1]?.terminated).toBe(true)
  })

  it('terminates an individual stalled frame after its bounded deadline', async () => {
    const decoder = await createZXingBarcodeDecoder(['QR_CODE'], new AbortController().signal)
    const worker = FakeWorker.instances[0]!
    FakeWorker.hold = true
    vi.useFakeTimers()
    const pending = decoder.detect(video, new AbortController().signal)
    const assertion = expect(pending).rejects.toThrow('Barcode decoder unavailable')
    await vi.advanceTimersByTimeAsync(8000)
    await assertion
    expect(worker.terminated).toBe(true)
    decoder.close()
  })

  it.each([
    [{ format: 'EAN_8', rawValue: 'other format' }], [{ format: 'QR_CODE', rawValue: '' }],
    [{ format: 'QR_CODE', rawValue: '\ud800' }], [{ format: 'QR_CODE', rawValue: 'x'.repeat(4097) }],
    [{ format: 'QR_CODE', rawValue: 'same' }, { format: 'QR_CODE', rawValue: 'again' }], {},
  ])('rejects malformed worker results without exposing them: %j', async (response) => {
    FakeWorker.reply = response
    const decoder = await createZXingBarcodeDecoder(['QR_CODE'], new AbortController().signal)
    await expect(decoder.detect(video, new AbortController().signal)).rejects.toThrow('Barcode decoder unavailable')
    expect(FakeWorker.instances[0]?.terminated).toBe(true)
    decoder.close()
  })

  it('rejects invalid formats, inactive/hidden streams and unreasonable frame dimensions', async () => {
    const signal = new AbortController().signal
    await expect(createZXingBarcodeDecoder([], signal)).rejects.toThrow()
    await expect(createZXingBarcodeDecoder(['QR_CODE', 'QR_CODE'], signal)).rejects.toThrow()
    await expect(createZXingBarcodeDecoder(['OTHER' as never], signal)).rejects.toThrow()
    const decoder = await createZXingBarcodeDecoder(['QR_CODE'], signal)
    track.readyState = 'ended'
    await expect(decoder.detect(video, signal)).rejects.toThrow()
    track.readyState = 'live'
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'hidden' })
    await expect(decoder.detect(video, signal)).rejects.toThrow()
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' })
    Object.defineProperty(video, 'videoWidth', { configurable: true, value: 16385 })
    await expect(decoder.detect(video, signal)).rejects.toThrow()
    expect(FakeWorker.instances[0]!.messages).toEqual([])
    decoder.close()
  })

  it('stops a worker on malformed messages, errors and failed posting', async () => {
    for (const kind of ['unexpected', 'error', 'messageerror', 'post'] as const) {
      FakeWorker.hold = true
      const decoder = await createZXingBarcodeDecoder(['QR_CODE'], new AbortController().signal)
      const worker = FakeWorker.instances.at(-1)!
      if (kind === 'post') vi.spyOn(worker, 'postMessage').mockImplementation(() => { throw new Error('private detail') })
      const result = decoder.detect(video, new AbortController().signal)
      if (kind !== 'post') {
        await vi.waitFor(() => expect(worker.messages).toHaveLength(1))
        if (kind === 'unexpected') worker.emit({ type: 'result', id: 999, codes: [] })
        if (kind === 'error') worker.onerror?.({ preventDefault: vi.fn() } as unknown as ErrorEvent)
        if (kind === 'messageerror') worker.onmessageerror?.()
      }
      await expect(result).rejects.toThrow('Barcode decoder unavailable')
      expect(worker.terminated).toBe(true)
      decoder.close()
    }
  })
})
