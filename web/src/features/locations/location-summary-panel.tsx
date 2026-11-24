import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { flushSync } from 'react-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Button } from '@/components/ui/button'
import { mutationControl as control } from '@/components/mutation-review'
import { ApiError } from '@/lib/api/client'
import { fetchLocationSummary, type LocationSummary } from '@/lib/api/locations'
import { messages } from '@/lib/messages'
import { subscribeSessionCleared } from '@/lib/session-lifecycle'

const text = messages.locations

type Phase = 'ready' | 'suspended' | 'restored' | 'cleared'
type Props = {
  id: string
  revision: number
  online: boolean
  checked: boolean
  checking: boolean
  invalid: boolean
  revalidate: () => Promise<boolean>
  onSessionLost: (mandatory: boolean) => void
}

function Quantities({ title, values }: { title: string; values: LocationSummary['direct_quantities'] }) {
  return <section className="min-w-0 space-y-2" aria-label={title}>
    <h3 className="text-base font-semibold">{title}</h3>
    {values.length === 0 ? <p className="text-sm text-muted-foreground">{text.summaryNoStock}</p> :
      <ul className="space-y-1">{values.map((group) => <li key={group.base_unit} className="flex min-w-0 flex-wrap gap-x-2 text-base [overflow-wrap:anywhere]">
        <span>{group.base_unit}</span><span>{String(group.quantity_base)}</span>
      </li>)}</ul>}
  </section>
}

export function LocationSummaryPanel({ id, revision, online, checked, checking, invalid, revalidate, onSessionLost }: Props) {
  const client = useQueryClient(), [phase, setPhase] = useState<Phase>('ready')
  const [working, setWorking] = useState(false), [refreshError, setRefreshError] = useState(false)
  const phaseRef = useRef<Phase>('ready'), generation = useRef(0), mounted = useRef(false), pending = useRef(false), reported = useRef(false)
  const heading = useRef<HTMLHeadingElement>(null), resumeButton = useRef<HTMLButtonElement>(null), focusAfterResume = useRef(false)
  const query = useQuery({ queryKey: ['locations', 'summary', id], queryFn: ({ signal }) => fetchLocationSummary(id, signal),
    enabled: online && checked && phase === 'ready', staleTime: 0, gcTime: 0, retry: false, refetchOnWindowFocus: false, refetchOnReconnect: false })

  useLayoutEffect(() => {
    mounted.current = true
    const invalidate = () => { generation.current++ }
    const redact = (next: Phase, synchronous: boolean) => {
      invalidate(); pending.current = false; focusAfterResume.current = false; phaseRef.current = next
      const apply = () => { setPhase(next); setWorking(false); setRefreshError(false) }
      if (synchronous) flushSync(apply); else apply()
      void client.cancelQueries({ queryKey: ['locations', 'summary', id], exact: true })
      client.removeQueries({ queryKey: ['locations', 'summary', id], exact: true })
    }
    const cleared = (source: 'local' | 'remote') => { redact('cleared', source === 'remote') }
    const pagehide = () => { if (phaseRef.current !== 'cleared') redact('suspended', true) }
    const pageshow = (event: PageTransitionEvent) => {
      if (event.persisted && phaseRef.current === 'suspended') { phaseRef.current = 'restored'; setPhase('restored') }
    }
    const unsubscribe = subscribeSessionCleared(cleared)
    window.addEventListener('pagehide', pagehide); window.addEventListener('pageshow', pageshow)
    return () => {
      mounted.current = false; invalidate(); pending.current = false
      unsubscribe(); window.removeEventListener('pagehide', pagehide); window.removeEventListener('pageshow', pageshow)
      void client.cancelQueries({ queryKey: ['locations', 'summary', id], exact: true })
      client.removeQueries({ queryKey: ['locations', 'summary', id], exact: true })
    }
  }, [client, id])

  useLayoutEffect(() => {
    if (phase === 'ready' && focusAfterResume.current) { focusAfterResume.current = false; heading.current?.focus() }
  }, [phase])

  useEffect(() => {
    const error = query.error
    if (!reported.current && phase === 'ready' && error instanceof ApiError && (error.status === 401 || error.code === 'PASSWORD_CHANGE_REQUIRED')) {
      reported.current = true
      onSessionLost(error.code === 'PASSWORD_CHANGE_REQUIRED')
    }
  }, [onSessionLost, phase, query.error])

  async function refresh(restoring: boolean) {
    if (!online || pending.current) return
    pending.current = true; setWorking(true); setRefreshError(false)
    const current = generation.current
    let valid = false
    try { valid = await revalidate() } catch { /* Do not expose a raw transport error. */ }
    if (!mounted.current || generation.current !== current || phaseRef.current === 'cleared') return
    if (!valid) {
      pending.current = false; setWorking(false); setRefreshError(true); return
    }
    if (restoring) {
      focusAfterResume.current = document.activeElement === resumeButton.current
      phaseRef.current = 'ready'; setPhase('ready')
    } else {
      await query.refetch()
    }
    if (mounted.current && generation.current === current) { pending.current = false; setWorking(false) }
  }

  const data = query.data
  const changed = !!data && data.revision !== revision
  const unavailable = !online || !checked || checking || working || query.isPending || query.isFetching
  const show = phase === 'ready' && online && checked && !checking && !invalid && !working && query.isFetchedAfterMount && !query.isFetching && !query.isError && !!data && !changed
  return <section aria-labelledby="location-summary-title" className="min-w-0 space-y-4 rounded-lg border p-5">
    <h2 ref={heading} id="location-summary-title" tabIndex={-1} className="rounded-sm text-xl font-semibold focus-visible:outline-2 focus-visible:outline-ring">{text.summaryTitle}</h2>
    <p className="text-sm leading-relaxed text-muted-foreground">{text.summaryHelp}</p>
    {phase === 'cleared' ? <div className="space-y-3"><p role="status">{text.summaryCleared}</p><Button type="button" variant="outline" className={control} onClick={() => onSessionLost(false)}>{text.summaryLogin}</Button></div>
      : phase === 'suspended' || phase === 'restored' ? <div className="space-y-3"><p role="status">{text.summaryRestore}</p>
        {phase === 'restored' && <Button ref={resumeButton} type="button" variant="outline" className={`${control} aria-disabled:opacity-50`} aria-disabled={!online || working}
          onClick={() => { if (online && !working) void refresh(true) }}>{text.summaryContinue}</Button>}
        {working && <p role="status">{text.summaryChecking}</p>}{refreshError && <p role="alert">{text.summaryResumeError}</p>}</div>
        : !online ? <p role="status">{text.summaryOffline}</p>
          : checking || !checked || working || query.isPending || query.isFetching ? <p role="status">{working ? text.summaryChecking : text.summaryLoading}</p>
            : invalid || query.isError || refreshError ? <p role="alert">{text.summaryError}</p>
              : changed ? <p role="alert">{text.summaryChanged}</p>
                : null}
    {show && <div className="min-w-0 space-y-4">
      <dl className="grid min-w-0 gap-4 sm:grid-cols-2">
        {[[text.summaryDirectUnits, data.value.direct_units], [text.summarySubtreeUnits, data.value.subtree_units], [text.summaryDistinctItems, data.value.distinct_item_count]].map(([label, amount]) =>
          <div key={label} className="min-w-0 space-y-1"><dt className="text-sm text-muted-foreground">{label}</dt><dd className="text-lg font-semibold [overflow-wrap:anywhere]">{String(amount)}</dd></div>)}
      </dl>
      <div className="grid min-w-0 gap-4 sm:grid-cols-2"><Quantities title={text.summaryDirectStock} values={data.value.direct_quantities} /><Quantities title={text.summarySubtreeStock} values={data.value.subtree_quantities} /></div>
    </div>}
    {phase === 'ready' && <Button type="button" variant="outline" className={`${control} aria-disabled:opacity-50`} aria-disabled={unavailable}
      onClick={() => { if (!unavailable) void refresh(false) }}>{text.summaryRefresh}</Button>}
  </section>
}
