import { useEffect, useRef, useState } from 'react'
import { useMutation, useQueryClient, type QueryClient } from '@tanstack/react-query'
import { ApiError, type Admin } from '@/lib/api/auth'
import { notifySessionCleared } from '@/lib/session-lifecycle'

export type AuthOutcome = { kind: 'login'; admin: Admin } | { kind: 'setup' | 'password' | 'logout' }
export type AuthNotice = 'setup' | 'password' | 'logout' | 'expired' | 'uncertain'
type AuthAction = AuthOutcome['kind']

export function clearSessionQueries(client: QueryClient) {
  notifySessionCleared()
  void client.cancelQueries()
  client.clear()
}

// Only explicit form/dialog actions call run. The mutation cache retains a
// short-lived closure rather than a serializable credential payload, then resets.
export function useAuthAction(onSuccess: (result: AuthOutcome) => void, onSessionLost: (notice: AuthNotice) => void) {
  const client = useQueryClient()
  const active = useRef<AbortController | null>(null)
  const mounted = useRef(false)
  const allowExit = useRef(false)
  const callbacks = useRef({ onSuccess, onSessionLost })
  useEffect(() => { callbacks.current = { onSuccess, onSessionLost } }, [onSuccess, onSessionLost])
  const [error, setError] = useState<ApiError>()
  const [pendingKind, setPendingKind] = useState<AuthAction>()
  const { mutateAsync, reset, isPending } = useMutation({
    mutationFn: (work: () => Promise<AuthOutcome>) => work(),
    retry: false, networkMode: 'always', gcTime: 0,
  })

  useEffect(() => {
    mounted.current = true
    const interrupt = () => { active.current?.abort() }
    const visibility = () => { if (document.visibilityState === 'hidden') interrupt() }
    document.addEventListener('visibilitychange', visibility)
    window.addEventListener('offline', interrupt)
    return () => {
      mounted.current = false
      document.removeEventListener('visibilitychange', visibility)
      window.removeEventListener('offline', interrupt)
      if (active.current) {
        active.current.abort()
        clearSessionQueries(client)
      }
    }
  }, [client])

  async function run(kind: AuthAction, work: (signal: AbortSignal) => Promise<AuthOutcome>) {
    if (active.current) return
    const controller = new AbortController()
    active.current = controller
    setPendingKind(kind)
    setError(undefined)
    try {
      const result = await mutateAsync(() => work(controller.signal))
      if (!mounted.current || active.current !== controller) return
      if (controller.signal.aborted) throw new ApiError('REQUEST_ABORTED')
      active.current = null
      allowExit.current = true
      callbacks.current.onSuccess(result)
    } catch (cause) {
      if (!mounted.current || active.current !== controller) return
      const failure = cause instanceof ApiError ? cause : new ApiError('UNKNOWN_ERROR')
      if (controller.signal.aborted || ['NETWORK_ERROR', 'REQUEST_TIMEOUT', 'REQUEST_ABORTED', 'INVALID_RESPONSE'].includes(failure.code)) {
        active.current = null
        allowExit.current = true
        callbacks.current.onSessionLost('uncertain')
      } else if (failure.status === 401 && (kind === 'password' || kind === 'logout')) {
        active.current = null
        allowExit.current = true
        callbacks.current.onSessionLost('expired')
      } else {
        setError(failure)
      }
    } finally {
      if (active.current === controller) active.current = null
      if (mounted.current) { reset(); setPendingKind(undefined) }
    }
  }

  function canLeave() { return allowExit.current }
  function allowNavigation() { allowExit.current = true }
  return { run, isPending, pendingKind, error, canLeave, allowNavigation }
}

export type AuthOperation = ReturnType<typeof useAuthAction>
