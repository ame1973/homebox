import { ApiError } from '@/lib/api/client'
import { probeConnectivity } from '@/lib/api/connectivity'
import { subscribeSessionCleared } from '@/lib/session-lifecycle'

export type ConnectivityObservation = Readonly<{ phase: 'checking' | 'reachable' | 'degraded' | 'unavailable' | 'rate-limited' | 'session-required' | 'access-blocked' | 'paused' | 'stopped' }>

// Call only after a fresh completed-session check; create a new monitor after a
// session transition. This controller neither authenticates nor changes data.
export function monitorForegroundConnectivity(publish: (observation: ConnectivityObservation) => void) {
  let stopped = false, generation = 0, failures = 0, nextAt = 0, retryAfterUntil = 0
  let timer: ReturnType<typeof setTimeout> | undefined
  let current: AbortController | undefined
  let lastPhase: ConnectivityObservation['phase'] = 'checking'
  const visible = () => document.visibilityState === 'visible'
  const now = () => performance.now()
  const clearTimer = () => { if (timer !== undefined) clearTimeout(timer); timer = undefined }
  const report = (phase: ConnectivityObservation['phase']) => { lastPhase = phase; publish({ phase }) }

  function finish(phase?: ConnectivityObservation['phase']) {
    if (stopped) return
    stopped = true; generation++; clearTimer(); current?.abort()
    document.removeEventListener('visibilitychange', changedVisibility)
    window.removeEventListener('online', onOnline)
    window.removeEventListener('pagehide', onPageHide)
    unsubscribe()
    if (phase) report(phase)
  }

  function schedule() {
    clearTimer()
    if (stopped || !visible() || current) return
    const remaining = Math.max(0, nextAt - now())
    if (remaining > 0) { timer = setTimeout(schedule, remaining); return }
    const controller = new AbortController(), attempt = ++generation
    current = controller
    report('checking')
    void Promise.resolve().then(() => probeConnectivity(controller.signal)).then((result) => {
      if (stopped || controller.signal.aborted || attempt !== generation) return
      failures = 0; retryAfterUntil = 0; nextAt = now() + 60_000
      report(result.status === 'ok' ? 'reachable' : 'degraded')
    }).catch((error: unknown) => {
      if (stopped || controller.signal.aborted || attempt !== generation) return
      const failure = error instanceof ApiError ? error : undefined
      if (failure?.status === 401 || failure?.code === 'AUTHENTICATION_REQUIRED' || failure?.code === 'PASSWORD_CHANGE_REQUIRED') {
        finish('session-required')
        return
      }
      failures++
      const backoff = Math.min(300_000, 15_000 * 2 ** Math.min(failures - 1, 5))
      const seconds = failure?.retryAfterSeconds
      const requested = typeof seconds === 'number' && Number.isSafeInteger(seconds) && seconds >= 0 ? Math.min(seconds, 3600) * 1000 : 0
      const delay = Math.max(backoff, requested), failedAt = now()
      nextAt = failedAt + delay
      retryAfterUntil = failedAt + requested
      if (failure?.status === 429 || failure?.code === 'RATE_LIMITED') {
        retryAfterUntil = nextAt
        report('rate-limited')
      } else if (failure?.status === 403 || failure?.code === 'ORIGIN_REJECTED') {
        report('access-blocked')
      } else {
        report('unavailable')
      }
    }).finally(() => {
      if (current === controller) current = undefined
      if (!stopped && visible()) schedule()
    })
  }

  function changedVisibility() {
    if (stopped) return
    if (!visible()) {
      generation++; clearTimer(); current?.abort()
      publish({ phase: 'paused' })
    } else {
      publish({ phase: lastPhase })
      schedule()
    }
  }
  function onOnline() { schedule() }
  function onPageHide() { finish('stopped') }
  const unsubscribe = subscribeSessionCleared(() => finish('stopped'))
  document.addEventListener('visibilitychange', changedVisibility)
  window.addEventListener('online', onOnline)
  window.addEventListener('pagehide', onPageHide)
  if (visible()) schedule()
  else publish({ phase: 'paused' })

  return {
    check() {
      if (stopped || !visible() || current || now() < retryAfterUntil) return false
      nextAt = 0; schedule()
      return true
    },
    stop() { finish() },
  }
}
