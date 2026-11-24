import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { maxFrameBytes } from '@/lib/capture-frame'
import { notifyPageSuspended } from '@/lib/session-lifecycle'

class FakeWorker {
  static instances: FakeWorker[] = []
  static failConstruction = false
  static failPosting = false
  static failTermination = false
  onmessage: ((event: MessageEvent<unknown>) => void) | null = null
  onerror: ((event: ErrorEvent) => void) | null = null
  onmessageerror: (() => void) | null = null
  readonly url: URL
  readonly options: WorkerOptions
  readonly messages: unknown[] = []
  terminated = false
  constructor(url: URL, options: WorkerOptions) {
    if (FakeWorker.failConstruction) throw new Error('private constructor detail')
    this.url = url; this.options = options; FakeWorker.instances.push(this)
  }
  postMessage(value: unknown) {
    if (FakeWorker.failPosting) throw new Error('private posting detail')
    this.messages.push(value)
  }
  terminate() {
    if (FakeWorker.failTermination) throw new Error('private termination detail')
    this.terminated = true
  }
  emit(data: unknown) { this.onmessage?.({ data } as MessageEvent<unknown>) }
}

const response = () => ({ type: 'result', kind: 'prepared', mime: 'image/png', width: 2, height: 3, orientation: 6,
  blob: new Blob(['clean'], { type: 'image/png' }) })
const source = () => new File(['private raw source bytes'], 'private-location-name.png', { type: 'image/png', lastModified: 123 })

beforeEach(() => {
  vi.resetModules(); vi.stubGlobal('Worker', FakeWorker)
  FakeWorker.instances = []; FakeWorker.failConstruction = false; FakeWorker.failPosting = false; FakeWorker.failTermination = false
  Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' })
})
afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals(); Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' }) })

describe('selected-image preparation worker client', () => {
  it('uses one same-origin module worker and sends a nameless bounded Blob only', async () => {
    const { prepareSelectedImage } = await import('./image-prepare-client')
    const input = source(), pending = prepareSelectedImage(input, new AbortController().signal), worker = FakeWorker.instances[0]!
    expect(worker.url.pathname).toMatch(/image-prepare-worker\.ts$/)
    expect(worker.options).toEqual({ type: 'module' })
    expect(worker.messages).toHaveLength(1)
    const message = worker.messages[0] as { type: string; blob: Blob }
    expect(message.type).toBe('prepare'); expect(message.blob).toBeInstanceOf(Blob)
    expect(message.blob).not.toBeInstanceOf(File); expect(message.blob.size).toBe(input.size); expect(message.blob.type).toBe('')
    expect(message.blob).not.toHaveProperty('name'); expect(message.blob).not.toHaveProperty('lastModified')
    const prepared = response(); worker.emit(prepared)
    expect(await pending).toEqual({ kind: 'prepared', mime: 'image/png', width: 2, height: 3, orientation: 6, blob: prepared.blob })
    expect(worker.terminated).toBe(true)
  })

  it('rejects empty, oversized and pre-cancelled sources without creating a worker', async () => {
    const { prepareSelectedImage } = await import('./image-prepare-client'), controller = new AbortController()
    expect(await prepareSelectedImage(null as never, controller.signal)).toEqual({ kind: 'invalid' })
    expect(await prepareSelectedImage(new Blob(), controller.signal)).toEqual({ kind: 'invalid' })
    const oversized = source(); Object.defineProperty(oversized, 'size', { value: maxFrameBytes + 1 })
    expect(await prepareSelectedImage(oversized, controller.signal)).toEqual({ kind: 'too-large' })
    controller.abort()
    expect(await prepareSelectedImage(source(), controller.signal)).toEqual({ kind: 'cancelled' })
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'hidden' })
    expect(await prepareSelectedImage(source(), new AbortController().signal)).toEqual({ kind: 'cancelled' })
    expect(FakeWorker.instances).toHaveLength(0)
  })

  it('does not create a worker if session-clear arrives while slicing the source', async () => {
    const { prepareSelectedImage } = await import('./image-prepare-client'), input = source(), native = input.slice.bind(input)
    vi.spyOn(input, 'slice').mockImplementation((...args) => { notifyPageSuspended(); return native(...args) })
    expect(await prepareSelectedImage(input, new AbortController().signal)).toEqual({ kind: 'cancelled' })
    expect(FakeWorker.instances).toHaveLength(0)
  })

  it('keeps one slot across calls, terminates on abort and ignores an older result', async () => {
    const { prepareSelectedImage } = await import('./image-prepare-client'), controller = new AbortController()
    const first = prepareSelectedImage(source(), controller.signal), old = FakeWorker.instances[0]!, late = old.onmessage!
    expect(await prepareSelectedImage(source(), new AbortController().signal)).toEqual({ kind: 'busy' })
    controller.abort(); expect(await first).toEqual({ kind: 'cancelled' }); expect(old.terminated).toBe(true)
    const second = prepareSelectedImage(source(), new AbortController().signal), current = FakeWorker.instances[1]!
    late({ data: response() } as MessageEvent<unknown>)
    expect(await prepareSelectedImage(source(), new AbortController().signal)).toEqual({ kind: 'busy' })
    current.emit(response()); expect((await second).kind).toBe('prepared'); expect(current.terminated).toBe(true)
  })

  it.each(['pagehide', 'hidden', 'same-page session', 'remote session'] as const)('discards pending work on %s', async (mode) => {
    const { prepareSelectedImage } = await import('./image-prepare-client')
    const pending = prepareSelectedImage(source(), new AbortController().signal), worker = FakeWorker.instances[0]!
    if (mode === 'pagehide') window.dispatchEvent(new Event('pagehide'))
    else if (mode === 'hidden') { Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'hidden' }); document.dispatchEvent(new Event('visibilitychange')) }
    else if (mode === 'same-page session') notifyPageSuspended()
    else window.dispatchEvent(new StorageEvent('storage', { key: 'homebox:session-cleared', newValue: 'cleared' }))
    expect(await pending).toEqual({ kind: 'cancelled' }); expect(worker.terminated).toBe(true)
    worker.emit(response())
  })

  it('times out and terminates a stalled worker without replay', async () => {
    vi.useFakeTimers()
    const { prepareSelectedImage } = await import('./image-prepare-client')
    const pending = prepareSelectedImage(source(), new AbortController().signal), worker = FakeWorker.instances[0]!
    await vi.advanceTimersByTimeAsync(30_000)
    expect(await pending).toEqual({ kind: 'timeout' }); expect(worker.terminated).toBe(true)
    expect(FakeWorker.instances).toHaveLength(1)
  })

  it.each(['invalid', 'unsupported', 'too-large', 'failed'] as const)('returns bounded worker result %s without diagnostics', async (kind) => {
    const { prepareSelectedImage } = await import('./image-prepare-client')
    const pending = prepareSelectedImage(source(), new AbortController().signal), worker = FakeWorker.instances[0]!
    worker.emit({ type: 'result', kind, privateDetail: 'do not expose' })
    expect(await pending).toEqual({ kind }); expect(worker.terminated).toBe(true)
  })

  it.each([
    null, {}, { type: 'result', kind: 'prepared' }, { ...response(), mime: 'image/gif' }, { ...response(), width: 0 },
    { ...response(), width: 16_385 }, { ...response(), width: 8_001, height: 8_000 }, { ...response(), orientation: 9 },
    { ...response(), blob: new Blob([''], { type: 'image/png' }) }, { ...response(), blob: new Blob(['clean'], { type: 'image/jpeg' }) },
    { ...response(), blob: new Blob(['a'.repeat(40)], { type: 'image/png' }) },
  ])('refuses an inconsistent worker response without returning its contents', async (value) => {
    const { prepareSelectedImage } = await import('./image-prepare-client')
    const pending = prepareSelectedImage(source(), new AbortController().signal), worker = FakeWorker.instances[0]!
    worker.emit(value)
    expect(await pending).toEqual({ kind: 'failed' }); expect(worker.terminated).toBe(true)
  })

  it.each(['construction', 'posting', 'error', 'messageerror'] as const)('handles worker %s failure without raw errors', async (kind) => {
    const { prepareSelectedImage } = await import('./image-prepare-client')
    FakeWorker.failConstruction = kind === 'construction'; FakeWorker.failPosting = kind === 'posting'
    const pending = prepareSelectedImage(source(), new AbortController().signal), worker = FakeWorker.instances[0]
    if (kind === 'error') worker?.onerror?.({ preventDefault: vi.fn() } as unknown as ErrorEvent)
    if (kind === 'messageerror') worker?.onmessageerror?.()
    expect(await pending).toEqual({ kind: 'failed' })
    if (worker) expect(worker.terminated).toBe(true)
  })

  it('latches closed until reload if a worker cannot be terminated', async () => {
    const { prepareSelectedImage } = await import('./image-prepare-client'), controller = new AbortController()
    const pending = prepareSelectedImage(source(), controller.signal)
    FakeWorker.failTermination = true; controller.abort()
    expect(await pending).toEqual({ kind: 'failed' })
    FakeWorker.failTermination = false
    expect(await prepareSelectedImage(source(), new AbortController().signal)).toEqual({ kind: 'failed' })
    expect(FakeWorker.instances).toHaveLength(1)
  })
})
