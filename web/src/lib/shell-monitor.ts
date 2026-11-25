import { documentShellWorker, readShellVersion, shellWorkerPath } from './shell-version'

export type ShellPhase = 'checking' | 'installing' | 'ready' | 'available' | 'waiting' | 'outdated' | 'missing' | 'error' | 'foreign' | 'unsupported' | 'paused'
export type ShellObservation = Readonly<{ phase: ShellPhase; busy: boolean }>

// A failed or canceled first attempt is not silently repeated on an SPA remount.
let initialAttempted = false

class ForeignRegistration extends Error {}

function scriptPath(worker: ServiceWorker | null | undefined): string | undefined {
  if (!worker) return undefined
  try {
    const url = new URL(worker.scriptURL)
    if (url.origin !== window.location.origin || url.username || url.password || url.search || url.hash) return undefined
    return shellWorkerPath(url.pathname)
  } catch { return undefined }
}

function ensureCurrent(signal: AbortSignal) {
  if (signal.aborted) throw new DOMException('Aborted', 'AbortError')
}

// The browser owns registration and activation. This coordinator never claims
// clients, skips waiting, reloads pages, deletes caches or sends domain writes.
export function monitorShell(publish: (value: ShellObservation) => void) {
  const path = documentShellWorker()
  const service = navigator.serviceWorker
  const locks = navigator.locks
  const supported = !!path && window.isSecureContext && !!service && !!locks
  let observation: ShellObservation = { phase: 'checking', busy: false }
  let stopped = false, suspended = false, rerun = false, failedInstall = false
  let current: AbortController | undefined
  let timer: ReturnType<typeof setTimeout> | undefined
  let registration: ServiceWorkerRegistration | undefined
  let attempted: ServiceWorker | undefined
  const workers = new Map<ServiceWorker, () => void>()
  const visible = () => document.visibilityState === 'visible'
  const eligible = () => !stopped && !suspended && visible()
  const clearTimer = () => { if (timer !== undefined) clearTimeout(timer); timer = undefined }
  const report = (phase: ShellPhase, busy = observation.busy) => {
    if (phase === observation.phase && busy === observation.busy) return
    observation = { phase, busy }; publish(observation)
  }

  function detach() {
    registration?.removeEventListener('updatefound', changed)
    for (const [worker, listener] of workers) worker.removeEventListener('statechange', listener)
    workers.clear(); registration = undefined
  }
  function track(worker: ServiceWorker | null | undefined) {
    if (!worker || worker.state === 'redundant' || workers.has(worker)) return
    const listener = () => {
      if (worker === attempted && worker.state === 'redundant') { failedInstall = true; attempted = undefined }
      if (worker === attempted && (worker.state === 'installed' || worker.state === 'activated')) { failedInstall = false; attempted = undefined }
      if (worker.state === 'redundant') { worker.removeEventListener('statechange', listener); workers.delete(worker) }
      changed()
    }
    workers.set(worker, listener)
    worker.addEventListener('statechange', listener)
  }
  function watch(value: ServiceWorkerRegistration | undefined) {
    if (registration !== value) {
      detach(); registration = value
      value?.addEventListener('updatefound', changed)
    }
    track(value?.installing); track(value?.waiting); track(value?.active)
  }

  async function registrations(signal: AbortSignal) {
    ensureCurrent(signal)
    const root = await service.getRegistration(`${window.location.origin}/`)
    ensureCurrent(signal)
    const matching = await service.getRegistration(window.location.href)
    ensureCurrent(signal)
    const scope = `${window.location.origin}/`
    if ([root, matching].some((value) => value && (value.scope !== scope ||
      [value.installing, value.waiting, value.active].some((worker) => worker && !scriptPath(worker))))) throw new ForeignRegistration()
    // Another tab can register the root between these two browser reads.
    const currentRoot = matching ?? root
    const controller = service.controller
    if (controller && (!scriptPath(controller) || !currentRoot?.active || controller.scriptURL !== currentRoot.active.scriptURL)) throw new ForeignRegistration()
    watch(currentRoot)
    return currentRoot
  }

  function phase(root: ServiceWorkerRegistration | undefined, live: string): ShellPhase {
    if (root?.installing) return 'installing'
    if (root?.waiting) return 'waiting'
    if (live !== path) return 'outdated'
    if (root?.active?.state === 'activated' && scriptPath(root.active) === path) { failedInstall = false; return 'ready' }
    if (failedInstall) return 'error'
    if (!root?.active) return 'missing'
    if (root.active.state !== 'activated') return 'installing'
    return 'available'
  }

  async function inspect(signal: AbortSignal, first: boolean): Promise<ShellPhase> {
    const root = await registrations(signal)
    if (first && !root && !initialAttempted) return install(signal, true)
    try {
      const live = await readShellVersion(signal)
      ensureCurrent(signal)
      return phase(root, live)
    } catch (error) {
      ensureCurrent(signal)
      if (root?.installing) return 'installing'
      if (root?.waiting) return 'waiting'
      throw error
    }
  }

  async function install(signal: AbortSignal, first: boolean): Promise<ShellPhase> {
    ensureCurrent(signal)
    const waiting = new AbortController()
    const cancel = () => waiting.abort()
    signal.addEventListener('abort', cancel, { once: true })
    const deadline = setTimeout(cancel, 8_000)
    try {
      return await locks.request('homebox-shell-registration-v1', { mode: 'exclusive', signal: waiting.signal }, async () => {
        clearTimeout(deadline)
        ensureCurrent(signal)
        let root = await registrations(signal)
        if (first && (root || initialAttempted)) return inspect(signal, false)
        if (first) initialAttempted = true
        const live = await readShellVersion(signal)
        ensureCurrent(signal)
        root = await registrations(signal)
        ensureCurrent(signal)
        if (documentShellWorker() !== path) throw new Error('Changed shell document')
        if (root?.installing || root?.waiting) return phase(root, live)
        if (live !== path) return 'outdated'
        if (root?.active && scriptPath(root.active) === path) return phase(root, live)
        if (first && root) return phase(root, live)
        initialAttempted = true; failedInstall = false
        report('installing', true)
        ensureCurrent(signal)
        if (documentShellWorker() !== path) throw new Error('Changed shell document')
        const installed = await service.register(path!, { scope: '/', updateViaCache: 'none' })
        ensureCurrent(signal)
        if (installed.scope !== `${window.location.origin}/` || [installed.installing, installed.waiting, installed.active].some((worker) => worker && !scriptPath(worker))) throw new ForeignRegistration()
        watch(installed)
        attempted = installed.installing ?? undefined
        if (attempted?.state === 'redundant') { attempted = undefined; failedInstall = true }
        return phase(installed, live)
      })
    } finally {
      clearTimeout(deadline)
      signal.removeEventListener('abort', cancel)
    }
  }

  function run(kind: 'initial' | 'check' | 'prepare', announce: boolean) {
    if (!eligible() || current || !supported) return false
    clearTimer()
    const controller = new AbortController()
    current = controller
    if (announce) report(kind === 'prepare' ? 'installing' : 'checking', true)
    else report(observation.phase, true)
    void (kind === 'prepare' ? install(controller.signal, false) : inspect(controller.signal, kind === 'initial'))
      .then((next) => { if (!controller.signal.aborted && eligible()) report(next, true) })
      .catch((error: unknown) => {
        if (!controller.signal.aborted && eligible()) {
          if (kind === 'initial') initialAttempted = true
          report(error instanceof ForeignRegistration ? 'foreign' : 'error', true)
        }
      }).finally(() => {
        if (current !== controller) return
        current = undefined
        if (!eligible()) return
        report(observation.phase, false)
        if (rerun) { rerun = false; run(initialAttempted ? 'check' : 'initial', false) }
        else timer = setTimeout(() => { run('check', false) }, 300_000)
      })
    return true
  }

  function changed() {
    if (!eligible()) return
    if (current) { rerun = true; return }
    run('check', false)
  }
  function visibility() {
    if (stopped || suspended) return
    if (!visible()) { clearTimer(); current?.abort(); report('paused', false) }
    else if (current) rerun = true
    else run(initialAttempted ? 'check' : 'initial', false)
  }
  function pagehide() {
    suspended = true; initialAttempted = true; rerun = false
    clearTimer(); current?.abort(); report('paused', false)
  }
  function pageshow(event: PageTransitionEvent) {
    if (!event.persisted || stopped) return
    suspended = false
    if (current) rerun = true
    else run('check', false)
  }

  if (!supported) report('unsupported', false)
  else {
    service.addEventListener('controllerchange', changed)
    document.addEventListener('visibilitychange', visibility)
    window.addEventListener('pagehide', pagehide)
    window.addEventListener('pageshow', pageshow)
    if (visible()) run('initial', true)
    else report('paused', false)
  }
  return {
    check: () => run('check', true),
    prepare: () => ['available', 'missing', 'error'].includes(observation.phase) && run('prepare', true),
    stop() {
      if (stopped) return
      stopped = true; clearTimer(); current?.abort(); detach()
      service?.removeEventListener('controllerchange', changed)
      document.removeEventListener('visibilitychange', visibility)
      window.removeEventListener('pagehide', pagehide)
      window.removeEventListener('pageshow', pageshow)
    },
  }
}
