import { useCallback, useEffect, useRef, useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { ApiError } from '@/lib/api/client'
import { fetchCSRF } from '@/lib/api/auth'
import { uncertainMutationFailure, type ReviewedCommand } from './reviewed-command'

export function useReviewedMutation<Input, Command extends ReviewedCommand, Result>(
  freeze: (input: Input) => Command, send: (command: Command, csrf: string, signal: AbortSignal) => Promise<Result>, queryKeys: readonly (readonly unknown[])[],
  onConfirmed: (command: Command, result: Result) => void, onSessionLost: (mandatory: boolean) => void,
) {
  const client = useQueryClient()
  const [command, setCommand] = useState<Command>()
  const commandRef = useRef<Command | undefined>(undefined)
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
  const { mutateAsync, reset } = useMutation({ mutationFn: (work: () => Promise<Result>) => work(), retry: false, networkMode: 'always', gcTime: 0 })

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

  function review(input: Input) {
    if (active.current || phase === 'uncertain') return
    const frozen = freeze(input)
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
        return send(original, csrf, controller.signal)
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
      // A frozen reply may predate another write. Invalidate reads only; never
      // install an acknowledgement as authoritative current state.
      for (const queryKey of queryKeys) void client.invalidateQueries({ queryKey })
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
        setPhase(sent && uncertainMutationFailure(failure) ? 'uncertain' : 'rejected')
        // Reconciliation reads never change the original request/key/proof.
        if (sent) for (const queryKey of queryKeys) void client.invalidateQueries({ queryKey })
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
