import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as versions from './shell-version'
import type { ShellObservation } from './shell-monitor'

vi.mock('./shell-version', async (original) => {
  const actual = await original<typeof import('./shell-version')>()
  return { ...actual, documentShellWorker: vi.fn(), readShellVersion: vi.fn() }
})

const current = '/pwa-worker-0123456789abcdef01234567.js'
const older = '/pwa-worker-aaaaaaaaaaaaaaaaaaaaaaaa.js'
const newer = '/pwa-worker-bbbbbbbbbbbbbbbbbbbbbbbb.js'
const read = vi.mocked(versions.readShellVersion)
const documentWorker = vi.mocked(versions.documentShellWorker)

class WorkerStub extends EventTarget {
  state: ServiceWorkerState = 'installing'
  scriptURL: string
  constructor(path: string, state: ServiceWorkerState = 'installing') {
    super(); this.scriptURL = new URL(path, window.location.origin).href; this.state = state
  }
  transition(state: ServiceWorkerState) { this.state = state; this.dispatchEvent(new Event('statechange')) }
}
class RegistrationStub extends EventTarget {
  scope = `${window.location.origin}/`
  installing: WorkerStub | null = null
  waiting: WorkerStub | null = null
  active: WorkerStub | null = null
}
class ServiceStub extends EventTarget {
  root: RegistrationStub | undefined
  matching: RegistrationStub | undefined
  controller: WorkerStub | null = null
  getRegistration = vi.fn(async (scope: string) => scope === `${window.location.origin}/` ? this.root : this.matching ?? this.root)
  register = vi.fn(async (path: string) => {
    const registration = this.root ?? new RegistrationStub()
    registration.installing = new WorkerStub(path)
    this.root = registration
    return registration
  })
}

type ShellMonitor = ReturnType<typeof import('./shell-monitor').monitorShell>
let service: ServiceStub
let lock: ReturnType<typeof vi.fn>
const monitors: ShellMonitor[] = []
const dispose = () => { for (const monitor of monitors.splice(0)) monitor.stop() }
const settle = async () => { await vi.advanceTimersByTimeAsync(0) }
const deferred = <T,>() => {
  let resolve!: (value: T) => void, reject!: (error: unknown) => void
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}

beforeEach(() => {
  vi.resetModules(); vi.useFakeTimers(); vi.clearAllMocks()
  service = new ServiceStub()
  lock = vi.fn(async (_name: string, options: { signal: AbortSignal }, callback: (value: object) => Promise<unknown>) => {
    if (options.signal.aborted) throw new DOMException('Aborted', 'AbortError')
    return callback({})
  })
  Object.defineProperty(navigator, 'serviceWorker', { configurable: true, value: service })
  Object.defineProperty(navigator, 'locks', { configurable: true, value: { request: lock } })
  Object.defineProperty(window, 'isSecureContext', { configurable: true, value: true })
  Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' })
  documentWorker.mockReturnValue(current)
  read.mockResolvedValue(current)
})
afterEach(() => { dispose(); vi.useRealTimers() })

async function start() {
  const { monitorShell } = await import('./shell-monitor')
  const states: ShellObservation[] = []
  const monitor = monitorShell((value) => { states.push(value) })
  monitors.push(monitor)
  await settle()
  return { monitor, states, latest: () => states.at(-1) }
}
function activate(registration: RegistrationStub) {
  const worker = registration.installing!
  registration.installing = null; registration.active = worker
  worker.transition('activated')
}
function hide(value: boolean) {
  Object.defineProperty(document, 'visibilityState', { configurable: true, value: value ? 'hidden' : 'visible' })
  document.dispatchEvent(new Event('visibilitychange'))
}

describe('public shell registration monitor', () => {
  it('automatically registers only an empty root with a fresh matching descriptor', async () => {
    const { latest } = await start()
    expect(service.register).toHaveBeenCalledExactlyOnceWith(current, { scope: '/', updateViaCache: 'none' })
    expect(lock).toHaveBeenCalledTimes(1)
    expect(lock.mock.calls[0]?.[0]).toBe('homebox-shell-registration-v1')
    expect(read).toHaveBeenCalledTimes(1)
    expect(latest()).toEqual({ phase: 'installing', busy: false })
    activate(service.root!)
    await settle()
    expect(latest()).toEqual({ phase: 'ready', busy: false })
    expect(service.register).toHaveBeenCalledTimes(1)
  })

  it.each(['current', 'installing', 'foreign'] as const)('checks a %s root installed between the two scope reads', async (kind) => {
    service.getRegistration.mockImplementationOnce(async () => {
      service.root = new RegistrationStub()
      if (kind === 'installing') service.root.installing = new WorkerStub(current)
      else service.root.active = new WorkerStub(kind === 'current' ? current : '/foreign-worker.js', 'activated')
      return undefined
    })
    const { latest } = await start()
    expect(latest()).toEqual({ phase: kind === 'current' ? 'ready' : kind, busy: false })
    expect(service.register).not.toHaveBeenCalled()
    if (kind === 'installing') { activate(service.root!); await settle(); expect(latest()?.phase).toBe('ready') }
  })

  it('withholds a nested registration appearing between the two scope reads', async () => {
    const nested = new RegistrationStub(); nested.scope += 'items/'
    service.getRegistration.mockResolvedValueOnce(undefined).mockResolvedValueOnce(nested)
    const { latest } = await start()
    expect(latest()?.phase).toBe('foreign')
    expect(service.register).not.toHaveBeenCalled()
  })

  it('withholds a newly foreign root even if the earlier root was trusted', async () => {
    const root = new RegistrationStub(); root.active = new WorkerStub(current, 'activated')
    const foreign = new RegistrationStub(); foreign.active = new WorkerStub('/foreign-worker.js', 'activated')
    service.getRegistration.mockResolvedValueOnce(root).mockResolvedValueOnce(foreign)
    const { latest } = await start()
    expect(latest()?.phase).toBe('foreign')
    expect(service.register).not.toHaveBeenCalled()
  })

  it('observes an existing active worker without re-registering and polls only after five minutes', async () => {
    service.root = new RegistrationStub(); service.root.active = new WorkerStub(current, 'activated')
    const { latest, monitor } = await start()
    expect(latest()?.phase).toBe('ready')
    expect(service.register).not.toHaveBeenCalled()
    expect(lock).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(299_999); expect(read).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1); expect(read).toHaveBeenCalledTimes(2)
    expect(latest()).toEqual({ phase: 'ready', busy: false })
    expect(monitor.check()).toBe(true); await settle()
    expect(read).toHaveBeenCalledTimes(3)
    expect(service.register).not.toHaveBeenCalled()
  })

  it('offers a new shell only from a current document and requires an explicit preparation action', async () => {
    service.root = new RegistrationStub(); service.root.active = new WorkerStub(older, 'activated')
    service.controller = service.root.active
    const { latest, monitor } = await start()
    expect(latest()?.phase).toBe('available')
    expect(service.register).not.toHaveBeenCalled()
    expect(monitor.prepare()).toBe(true)
    expect(monitor.prepare()).toBe(false)
    await settle()
    expect(service.register).toHaveBeenCalledExactlyOnceWith(current, { scope: '/', updateViaCache: 'none' })
    expect(latest()).toEqual({ phase: 'installing', busy: false })
    const installed = service.root.installing!
    service.root.installing = null; service.root.waiting = installed
    installed.transition('installed'); await settle()
    expect(latest()?.phase).toBe('waiting')
    expect(monitor.prepare()).toBe(false)
    expect(service.root.active.scriptURL).toBe(new URL(older, window.location.origin).href)
  })

  it('does not replace a different waiting or installing worker', async () => {
    service.root = new RegistrationStub(); service.root.active = new WorkerStub(older, 'activated')
    service.root.waiting = new WorkerStub(newer, 'installed')
    const { latest, monitor } = await start()
    expect(latest()?.phase).toBe('waiting')
    expect(monitor.prepare()).toBe(false)
    service.root.waiting = null; service.root.installing = new WorkerStub(newer)
    service.root.dispatchEvent(new Event('updatefound')); await settle()
    expect(latest()?.phase).toBe('installing')
    expect(monitor.prepare()).toBe(false)
    expect(service.register).not.toHaveBeenCalled()
  })

  it('does not register a stale page after the server deploys a different version', async () => {
    read.mockResolvedValue(newer)
    const { latest, monitor } = await start()
    expect(latest()?.phase).toBe('outdated')
    expect(monitor.prepare()).toBe(false)
    expect(service.register).not.toHaveBeenCalled()
    read.mockResolvedValue(current)
    expect(monitor.check()).toBe(true); await settle()
    expect(latest()?.phase).toBe('missing')
    expect(service.register).not.toHaveBeenCalled()
    expect(monitor.prepare()).toBe(true); await settle()
    expect(service.register).toHaveBeenCalledTimes(1)
  })

  it('rechecks registration after reading the descriptor under the lock', async () => {
    read.mockImplementationOnce(async () => {
      service.root = new RegistrationStub(); service.root.active = new WorkerStub(older, 'activated')
      return current
    })
    const { latest } = await start()
    expect(service.register).not.toHaveBeenCalled()
    expect(latest()?.phase).toBe('available')
  })

  it('rejects a changed or duplicated document worker before registration', async () => {
    documentWorker.mockReturnValueOnce(current).mockReturnValue(undefined)
    const { latest } = await start()
    expect(latest()?.phase).toBe('error')
    expect(service.register).not.toHaveBeenCalled()
  })

  it.each(['foreign active', 'foreign waiting', 'foreign installing', 'foreign scope', 'nested scope', 'foreign controller'] as const)(
    'never overwrites an existing %s', async (scenario) => {
      service.root = new RegistrationStub(); service.root.active = new WorkerStub(current, 'activated')
      if (scenario === 'foreign active') service.root.active = new WorkerStub('/other-worker.js', 'activated')
      if (scenario === 'foreign waiting') service.root.waiting = new WorkerStub('/other-worker.js', 'installed')
      if (scenario === 'foreign installing') service.root.installing = new WorkerStub('/other-worker.js')
      if (scenario === 'foreign scope') service.root.scope = `${window.location.origin}/wrong/`
      if (scenario === 'nested scope') {
        service.matching = new RegistrationStub(); service.matching.scope += 'items/'
        service.getRegistration.mockResolvedValueOnce(service.root).mockResolvedValueOnce(service.matching)
      }
      if (scenario === 'foreign controller') service.controller = new WorkerStub('/other-worker.js', 'activated')
      const { latest, monitor } = await start()
      expect(latest()?.phase).toBe('foreign')
      expect(monitor.prepare()).toBe(false)
      expect(service.register).not.toHaveBeenCalled()
      expect(read).not.toHaveBeenCalled()
    },
  )

  it('fails safely when browser support, secure context or built metadata is absent', async () => {
    Object.defineProperty(navigator, 'locks', { configurable: true, value: undefined })
    const first = await start()
    expect(first.latest()?.phase).toBe('unsupported')
    expect(first.monitor.check()).toBe(false)
    dispose()
    Object.defineProperty(navigator, 'locks', { configurable: true, value: { request: lock } })
    Object.defineProperty(window, 'isSecureContext', { configurable: true, value: false })
    expect((await start()).latest()?.phase).toBe('unsupported')
    dispose()
    Object.defineProperty(window, 'isSecureContext', { configurable: true, value: true })
    documentWorker.mockReturnValue(undefined)
    expect((await start()).latest()?.phase).toBe('unsupported')
    expect(service.register).not.toHaveBeenCalled()
  })

  it('keeps a failed installation explicit across an SPA remount', async () => {
    service.register.mockRejectedValueOnce(new Error('private browser error'))
    const first = await start()
    expect(first.latest()?.phase).toBe('error')
    expect(service.register).toHaveBeenCalledTimes(1)
    first.monitor.stop()
    const second = await start()
    expect(second.latest()?.phase).toBe('missing')
    expect(service.register).toHaveBeenCalledTimes(1)
    expect(second.monitor.prepare()).toBe(true); await settle()
    expect(service.register).toHaveBeenCalledTimes(2)
  })

  it('reports an asynchronously failed install and permits only an explicit retry', async () => {
    const { latest, monitor } = await start()
    const worker = service.root!.installing!
    service.root!.installing = null; service.root = undefined
    worker.transition('redundant'); await settle()
    expect(latest()?.phase).toBe('error')
    expect(service.register).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(300_000)
    expect(service.register).toHaveBeenCalledTimes(1)
    expect(monitor.prepare()).toBe(true); await settle()
    expect(service.register).toHaveBeenCalledTimes(2)
  })

  it('recognizes a successful installation by another tab after a local failure', async () => {
    const { latest, monitor } = await start()
    const worker = service.root!.installing!
    service.root!.installing = null; service.root = undefined
    worker.transition('redundant'); await settle()
    expect(latest()?.phase).toBe('error')
    service.root = new RegistrationStub(); service.root.active = new WorkerStub(current, 'activated')
    expect(monitor.check()).toBe(true); await settle()
    expect(latest()?.phase).toBe('ready')
    expect(service.register).toHaveBeenCalledTimes(1)
  })

  it('survives immediate effect cleanup without suppressing the real first attempt', async () => {
    const { monitorShell } = await import('./shell-monitor')
    const transient = monitorShell(() => {})
    transient.stop()
    const states: ShellObservation[] = []
    const actual = monitorShell((value) => { states.push(value) }); monitors.push(actual)
    await settle()
    expect(service.register).toHaveBeenCalledTimes(1)
    expect(states.at(-1)?.phase).toBe('installing')
  })

  it('does not start even a registration read after a reentrant initial suspension', async () => {
    const { monitorShell } = await import('./shell-monitor')
    const monitor = monitorShell((state) => {
      if (state.phase === 'checking' && state.busy) window.dispatchEvent(new Event('pagehide'))
    })
    monitors.push(monitor)
    await settle()
    expect(service.getRegistration).not.toHaveBeenCalled()
    expect(service.register).not.toHaveBeenCalled()
  })

  it('does not register after a reentrant page suspension during its status update', async () => {
    const { monitorShell } = await import('./shell-monitor')
    const monitor = monitorShell((state) => {
      if (state.phase === 'installing' && state.busy) window.dispatchEvent(new Event('pagehide'))
    })
    monitors.push(monitor)
    await settle()
    expect(service.register).not.toHaveBeenCalled()
  })

  it('aborts a late version read on suspension and does not register after BFCache return', async () => {
    const pending = deferred<string>()
    read.mockReturnValueOnce(pending.promise)
    const { latest, monitor } = await start()
    expect(latest()).toEqual({ phase: 'checking', busy: true })
    window.dispatchEvent(new Event('pagehide'))
    expect(read.mock.calls[0]?.[0].aborted).toBe(true)
    expect(latest()?.phase).toBe('paused')
    pending.resolve(current); await settle()
    expect(service.register).not.toHaveBeenCalled()
    window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true })); await settle()
    expect(latest()?.phase).toBe('missing')
    expect(monitor.prepare()).toBe(true); await settle()
    expect(service.register).toHaveBeenCalledTimes(1)
  })

  it('does not overlap an aborted read across background and foreground transitions', async () => {
    service.root = new RegistrationStub(); service.root.active = new WorkerStub(current, 'activated')
    const pending = deferred<string>()
    read.mockReturnValueOnce(pending.promise)
    const { latest } = await start()
    hide(true); hide(false); await settle()
    expect(read).toHaveBeenCalledTimes(1)
    pending.resolve(older); await settle()
    expect(read).toHaveBeenCalledTimes(2)
    expect(latest()?.phase).toBe('ready')
  })

  it.each(['installing', 'waiting'] as const)('keeps a known %s state if the version read fails', async (state) => {
    service.root = new RegistrationStub(); service.root.active = new WorkerStub(older, 'activated')
    service.root[state] = new WorkerStub(current, state === 'waiting' ? 'installed' : 'installing')
    read.mockRejectedValue(new Error('offline'))
    const { latest } = await start()
    expect(latest()?.phase).toBe(state)
    expect(service.register).not.toHaveBeenCalled()
  })

  it('does not start while hidden and begins its first attempt when visible', async () => {
    hide(true)
    const { latest, monitor } = await start()
    expect(latest()?.phase).toBe('paused')
    expect(read).not.toHaveBeenCalled()
    hide(false); await settle()
    expect(service.register).toHaveBeenCalledTimes(1)
    expect(latest()?.phase).toBe('installing')
    monitor.stop(); monitor.stop()
  })

  it.each(['not a url', 'https://another.test/pwa-worker-0123456789abcdef01234567.js', `${current}?version=1`])(
    'rejects a foreign worker script URL %s', async (url) => {
      service.root = new RegistrationStub(); service.root.active = new WorkerStub(current, 'activated')
      service.root.active.scriptURL = url
      const { latest } = await start()
      expect(latest()?.phase).toBe('foreign')
      expect(service.register).not.toHaveBeenCalled()
    },
  )

  it('keeps the lock until an unabortable registration settles after unmount', async () => {
    let last = Promise.resolve<unknown>(undefined)
    lock.mockImplementation((_name: string, options: { signal: AbortSignal }, callback: (value: object) => Promise<unknown>) => {
      const result = last.then(() => {
        if (options.signal.aborted) throw new DOMException('Aborted', 'AbortError')
        return callback({})
      })
      last = result.then(() => undefined, () => undefined)
      return result
    })
    const pending = deferred<RegistrationStub>()
    service.register.mockReturnValueOnce(pending.promise)
    const first = await start()
    expect(service.register).toHaveBeenCalledTimes(1)
    first.monitor.stop()
    const second = await start()
    expect(second.latest()?.phase).toBe('missing')
    expect(second.monitor.prepare()).toBe(true); await settle()
    expect(service.register).toHaveBeenCalledTimes(1)
    const created = new RegistrationStub(); created.installing = new WorkerStub(current)
    service.root = created; pending.resolve(created); await settle()
    expect(service.register).toHaveBeenCalledTimes(1)
    expect(second.latest()?.phase).toBe('installing')
  })

  it('times out while waiting for a cross-tab lock without starting registration', async () => {
    lock.mockImplementation((_name: string, options: { signal: AbortSignal }) => new Promise((_resolve, reject) => {
      options.signal.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')), { once: true })
    }))
    const { latest } = await start()
    await vi.advanceTimersByTimeAsync(8_000)
    expect(latest()).toEqual({ phase: 'error', busy: false })
    expect(service.register).not.toHaveBeenCalled()
  })
})
