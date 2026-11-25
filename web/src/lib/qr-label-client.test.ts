import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { notifyPageSuspended } from './session-lifecycle'
import { encodeQRLabel } from './qr-label-encode'

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

const input = ['location', 'room-1', 'https://homebox.example'] as const
function response() {
  const matrix = encodeQRLabel(...input)
  return { type: 'result', size: matrix.size, modules: matrix.modules.buffer }
}

beforeEach(() => {
  vi.resetModules(); vi.stubGlobal('Worker', FakeWorker)
  FakeWorker.instances = []; FakeWorker.failConstruction = false; FakeWorker.failPosting = false; FakeWorker.failTermination = false
  Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' })
})
afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals(); Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' }) })

describe('local QR label worker client', () => {
  it('sends one exact target to a same-origin worker and validates its bounded result', async () => {
    const { prepareQRLabel } = await import('./qr-label-client')
    const pending = prepareQRLabel(...input, new AbortController().signal), worker = FakeWorker.instances[0]!
    expect(worker.url.pathname).toMatch(/qr-label-worker\.ts$/); expect(worker.options).toEqual({ type: 'module' })
    expect(worker.messages).toEqual([{ type: 'encode', kind: input[0], id: input[1], origin: input[2] }])
    const expected = response(); worker.emit(expected)
    const result = await pending
    expect(result.kind).toBe('ready')
    if (result.kind !== 'ready') throw new Error('Expected QR matrix')
    expect(result.link.url).toBe('https://homebox.example/l/room-1'); expect(result.size).toBe(expected.size)
    expect(result.modules).toEqual(new Uint8Array(expected.modules)); expect(Object.isFrozen(result)).toBe(true)
    expect(worker.terminated).toBe(true)
  })

  it('rejects invalid, pre-aborted and hidden requests without starting a worker', async () => {
    const { prepareQRLabel } = await import('./qr-label-client'), controller = new AbortController()
    expect(await prepareQRLabel('location', 'tree', input[2], controller.signal)).toEqual({ kind: 'invalid' })
    controller.abort(); expect(await prepareQRLabel(...input, controller.signal)).toEqual({ kind: 'cancelled' })
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'hidden' })
    expect(await prepareQRLabel(...input, new AbortController().signal)).toEqual({ kind: 'cancelled' })
    expect(FakeWorker.instances).toHaveLength(0)
  })

  it('holds one slot across calls, terminates on abort and ignores an older result', async () => {
    const { prepareQRLabel } = await import('./qr-label-client'), controller = new AbortController()
    const first = prepareQRLabel(...input, controller.signal), old = FakeWorker.instances[0]!, late = old.onmessage!
    expect(await prepareQRLabel(...input, new AbortController().signal)).toEqual({ kind: 'busy' })
    controller.abort(); expect(await first).toEqual({ kind: 'cancelled' }); expect(old.terminated).toBe(true)
    const second = prepareQRLabel(...input, new AbortController().signal), current = FakeWorker.instances[1]!
    late({ data: response() } as MessageEvent<unknown>)
    expect(await prepareQRLabel(...input, new AbortController().signal)).toEqual({ kind: 'busy' })
    current.emit(response()); expect((await second).kind).toBe('ready'); expect(current.terminated).toBe(true)
  })

  it.each(['pagehide', 'hidden', 'same-page session', 'remote session'] as const)('discards pending work on %s', async (mode) => {
    const { prepareQRLabel } = await import('./qr-label-client')
    const pending = prepareQRLabel(...input, new AbortController().signal), worker = FakeWorker.instances[0]!
    if (mode === 'pagehide') window.dispatchEvent(new Event('pagehide'))
    else if (mode === 'hidden') { Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'hidden' }); document.dispatchEvent(new Event('visibilitychange')) }
    else if (mode === 'same-page session') notifyPageSuspended()
    else window.dispatchEvent(new StorageEvent('storage', { key: 'homebox:session-cleared', newValue: 'cleared' }))
    expect(await pending).toEqual({ kind: 'cancelled' }); expect(worker.terminated).toBe(true)
    worker.emit(response())
  })

  it('times out and terminates a stalled worker without replay', async () => {
    vi.useFakeTimers()
    const { prepareQRLabel } = await import('./qr-label-client')
    const pending = prepareQRLabel(...input, new AbortController().signal), worker = FakeWorker.instances[0]!
    await vi.advanceTimersByTimeAsync(8_000)
    expect(await pending).toEqual({ kind: 'timeout' }); expect(worker.terminated).toBe(true)
    expect(FakeWorker.instances).toHaveLength(1)
  })

  it.each(['null', 'unknown field', 'invalid size', 'short buffer', 'bad value', 'quiet zone', 'finder', 'blank'] as const)('refuses a malformed %s response without returning private data', async (kind) => {
    const { prepareQRLabel } = await import('./qr-label-client')
    const pending = prepareQRLabel(...input, new AbortController().signal), worker = FakeWorker.instances[0]!
    const valid = response(), bytes = new Uint8Array(valid.modules)
    let value: unknown = valid
    if (kind === 'null') value = null
    if (kind === 'unknown field') value = { ...valid, privateDetail: 'do not expose' }
    if (kind === 'invalid size') value = { ...valid, size: 30 }
    if (kind === 'short buffer') value = { ...valid, modules: new ArrayBuffer(1) }
    if (kind === 'bad value') bytes[valid.size * 10 + 10] = 2
    if (kind === 'quiet zone') bytes[0] = 1
    if (kind === 'finder') bytes[valid.size * 4 + 4] = 0
    if (kind === 'blank') bytes.fill(0)
    worker.emit(value)
    expect(await pending).toEqual({ kind: 'failed' }); expect(worker.terminated).toBe(true)
  })

  it.each(['construction', 'posting', 'error', 'messageerror'] as const)('handles worker %s failure without raw errors', async (kind) => {
    const { prepareQRLabel } = await import('./qr-label-client')
    FakeWorker.failConstruction = kind === 'construction'; FakeWorker.failPosting = kind === 'posting'
    const pending = prepareQRLabel(...input, new AbortController().signal), worker = FakeWorker.instances[0]
    if (kind === 'error') worker?.onerror?.({ preventDefault: vi.fn() } as unknown as ErrorEvent)
    if (kind === 'messageerror') worker?.onmessageerror?.()
    expect(await pending).toEqual({ kind: 'failed' })
    if (worker) expect(worker.terminated).toBe(true)
  })

  it('latches closed until reload if a worker cannot be terminated', async () => {
    const { prepareQRLabel } = await import('./qr-label-client'), controller = new AbortController()
    const pending = prepareQRLabel(...input, controller.signal)
    FakeWorker.failTermination = true; controller.abort()
    expect(await pending).toEqual({ kind: 'failed' })
    FakeWorker.failTermination = false
    expect(await prepareQRLabel(...input, new AbortController().signal)).toEqual({ kind: 'failed' })
    expect(FakeWorker.instances).toHaveLength(1)
  })
})
