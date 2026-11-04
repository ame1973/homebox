import { useCallback, useEffect, useRef, useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { ApiError } from '@/lib/api/client'
import { fetchCSRF } from '@/lib/api/auth'
import { freezeLocationCommand, sendLocationCommand, uncertainLocationFailure, type LocationCommand, type LocationCommandInput, type LocationResult } from './commands'

export function useLocationMutation(onConfirmed: (command: LocationCommand, result: LocationResult) => void, onSessionLost: (mandatory: boolean) => void) {
  const client = useQueryClient()
  const [command, setCommand] = useState<LocationCommand>()
  const commandRef = useRef<LocationCommand | undefined>(undefined)
  const [phase, setPhase] = useState<'idle' | 'review' | 'pending' | 'rejected' | 'uncertain'>('idle')
  const [open, setOpen] = useState(false)
  const [error, setError] = useState<ApiError>()
  const [confirmed, setConfirmed] = useState(false)
  const active = useRef<AbortController | null>(null)
  const mounted = useRef(false)
  const leaveAllowed = useRef(false)
  const reviewTrigger = useRef<HTMLElement | null>(null)
  const suspended = useRef(false)
  const callbacks = useRef({ onConfirmed, onSessionLost })
  useEffect(() => { callbacks.current = { onConfirmed, onSessionLost } }, [onConfirmed, onSessionLost])
  const { mutateAsync, reset } = useMutation({ mutationFn: (work: () => Promise<LocationResult>) => work(), retry: false, networkMode: 'always', gcTime: 0 })

  useEffect(() => {
    mounted.current = true
    const interrupt = () => { active.current?.abort() }
    const visibility = () => { if (document.visibilityState === 'hidden') interrupt() }
    window.addEventListener('offline', interrupt)
    document.addEventListener('visibilitychange', visibility)
    return () => {
      mounted.current = false
      window.removeEventListener('offline', interrupt)
      document.removeEventListener('visibilitychange', visibility)
      active.current?.abort()
    }
  }, [])

  function review(input: LocationCommandInput) {
    if (active.current || phase === 'uncertain') return
    const frozen = freezeLocationCommand(input)
    reviewTrigger.current = document.activeElement instanceof HTMLElement ? document.activeElement : null
    suspended.current = false
    commandRef.current = frozen
    leaveAllowed.current = false
    setCommand(frozen)
    setPhase('review')
    setError(undefined)
    setConfirmed(false)
    setOpen(true)
  }

  function dismiss() {
    if (active.current) { active.current.abort(); return }
    setOpen(false)
    if (phase !== 'uncertain') {
      commandRef.current = undefined
      setCommand(undefined)
      setPhase('idle')
      setError(undefined)
    }
  }

  async function confirm() {
    const original = commandRef.current
    if (!original || active.current) return
    const controller = new AbortController()
    active.current = controller
    let sent = false
    setPhase('pending')
    setError(undefined)
    try {
      const result = await mutateAsync(async () => {
        const csrf = await fetchCSRF(controller.signal)
        if (controller.signal.aborted) throw new ApiError('REQUEST_ABORTED')
        sent = true
        return sendLocationCommand(original, csrf, controller.signal)
      })
      if (!mounted.current || active.current !== controller) return
      if (controller.signal.aborted) throw new ApiError('REQUEST_ABORTED')
      active.current = null
      leaveAllowed.current = true
      commandRef.current = undefined
      setCommand(undefined)
      setPhase('idle')
      setOpen(false)
      setConfirmed(true)
      // A frozen acknowledgement may predate another write. Never overwrite
      // authoritative read state with the returned historical resource.
      void client.invalidateQueries({ queryKey: ['locations'] })
      callbacks.current.onConfirmed(original, result)
    } catch (cause) {
      if (!mounted.current || active.current !== controller) return
      const failure = controller.signal.aborted ? new ApiError('REQUEST_ABORTED') : cause instanceof ApiError ? cause : new ApiError('UNKNOWN_ERROR')
      active.current = null
      if (failure.status === 401 || failure.code === 'PASSWORD_CHANGE_REQUIRED') {
        leaveAllowed.current = true
        commandRef.current = undefined
        setCommand(undefined)
        setPhase('idle')
        setOpen(false)
        callbacks.current.onSessionLost(failure.code === 'PASSWORD_CHANGE_REQUIRED')
      } else {
        setError(failure)
        setPhase(sent && uncertainLocationFailure(failure) ? 'uncertain' : 'rejected')
        // Reads may reconcile after an unknown result or show a fresh conflict;
        // this never submits, refreshes a revision, or changes the original key.
        if (sent) void client.invalidateQueries({ queryKey: ['locations'] })
      }
    } finally {
      if (active.current === controller) active.current = null
      if (mounted.current) reset()
    }
  }

  const canLeave = useCallback(() => leaveAllowed.current, [])
  const allowNavigation = useCallback(() => { leaveAllowed.current = true; active.current?.abort() }, [])
  const reopen = useCallback(() => { if (commandRef.current) { suspended.current = false; setOpen(true) } }, [])
  const suspend = useCallback(() => { suspended.current = true; setOpen(false) }, [])
  const abort = useCallback(() => { active.current?.abort() }, [])
  const restoreFocus = useCallback(() => {
    if (suspended.current) return
    const target = reviewTrigger.current
    if (target?.isConnected && !(target instanceof HTMLButtonElement && target.disabled)) target.focus()
    else document.getElementById('main')?.focus()
  }, [])
  return { command, phase, open, error, confirmed, review, dismiss, confirm, canLeave, allowNavigation, reopen, suspend, abort, restoreFocus }
}

export type LocationMutation = ReturnType<typeof useLocationMutation>
