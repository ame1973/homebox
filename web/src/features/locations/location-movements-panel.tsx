import { useLayoutEffect, useRef, useState } from 'react'
import { flushSync } from 'react-dom'
import { Button } from '@/components/ui/button'
import { mutationControl as control } from '@/components/mutation-review'
import { ApiError } from '@/lib/api/client'
import { fetchLocationMovements, type LocationMovement } from '@/lib/api/locations'
import { messages } from '@/lib/messages'
import { subscribeSessionCleared } from '@/lib/session-lifecycle'
import { quoteExactText } from '@/lib/text-literal'
import { appendMovementsPage, type MovementsTrail } from './movements-trail'

const text = messages.locations, pageSize = 20
type Phase = 'ready' | 'suspended' | 'restored' | 'cleared'
type Issue = 'error' | 'changed' | 'offline' | undefined
type Props = {
  id: string; revision?: number; online: boolean; checked: boolean; checking: boolean; invalid: boolean
  revalidate: () => Promise<boolean>; onSessionLost: (mandatory: boolean) => void
}

function Field({ label, value }: { label: string; value: string }) {
  return <div className="min-w-0 space-y-1"><dt className="text-sm text-muted-foreground">{label}</dt><dd className="whitespace-pre-wrap text-base [overflow-wrap:anywhere]">{value}</dd></div>
}
function MovementRows({ data }: { data: readonly LocationMovement[] }) {
  return <ul className="divide-y border-y">{data.map((event) => <li key={event.id} className="min-w-0 py-4">
    <dl className="grid min-w-0 gap-3 sm:grid-cols-2"><Field label={text.movementsEvent} value={quoteExactText(event.id)} /><Field label={text.movementsTime} value={quoteExactText(event.occurred_at)} />
      <Field label={text.movementsFrom} value={event.from_parent_id === null ? text.movementsRoot : quoteExactText(event.from_parent_id)} />
      <Field label={text.movementsTo} value={event.to_parent_id === null ? text.movementsRoot : quoteExactText(event.to_parent_id)} /></dl>
  </li>)}</ul>
}

export function LocationMovementsPanel({ id, revision, online, checked, checking, invalid, revalidate, onSessionLost }: Props) {
  const [phase, setPhase] = useState<Phase>('ready'), [opened, setOpened] = useState(false), [working, setWorking] = useState(false), [issue, setIssue] = useState<Issue>()
  const [trail, setTrail] = useState<MovementsTrail>([]), [index, setIndex] = useState(0)
  const phaseRef = useRef<Phase>('ready'), generation = useRef(0), mounted = useRef(false), pending = useRef(false)
  const request = useRef<AbortController | null>(null), trailRef = useRef<MovementsTrail>([])
  const heading = useRef<HTMLHeadingElement>(null), resumeButton = useRef<HTMLButtonElement>(null), focusAfterResume = useRef(false)

  useLayoutEffect(() => {
    mounted.current = true
    const cancel = () => { generation.current++; pending.current = false; request.current?.abort(); request.current = null; trailRef.current = [] }
    const redact = (next: Phase, synchronous: boolean) => {
      cancel(); phaseRef.current = next; focusAfterResume.current = false
      const apply = () => { setPhase(next); setTrail([]); setIndex(0); setOpened(false); setWorking(false); setIssue(undefined) }
      if (synchronous) flushSync(apply); else apply()
    }
    const cleared = (source: 'local' | 'remote') => { redact('cleared', source === 'remote') }
    const offline = () => {
      if (phaseRef.current !== 'ready') return
      cancel(); setTrail([]); setIndex(0); setWorking(false); setIssue('offline')
    }
    const pagehide = () => { if (phaseRef.current !== 'cleared') redact('suspended', true) }
    const pageshow = (event: PageTransitionEvent) => {
      if (event.persisted && phaseRef.current === 'suspended') { phaseRef.current = 'restored'; setPhase('restored') }
    }
    const unsubscribe = subscribeSessionCleared(cleared)
    window.addEventListener('offline', offline); window.addEventListener('pagehide', pagehide); window.addEventListener('pageshow', pageshow)
    return () => {
      mounted.current = false; cancel(); unsubscribe()
      window.removeEventListener('offline', offline); window.removeEventListener('pagehide', pagehide); window.removeEventListener('pageshow', pageshow)
    }
  }, [id])

  useLayoutEffect(() => {
    if (phase === 'ready' && focusAfterResume.current) { focusAfterResume.current = false; heading.current?.focus() }
  }, [phase])

  const canStart = online && checked && !checking && !working
  async function read(cursor: string | undefined, reset: boolean, restoring = false) {
    if (!canStart || !navigator.onLine || pending.current || phaseRef.current !== (restoring ? 'restored' : 'ready')) return
    pending.current = true; setWorking(true); setIssue(undefined)
    const current = ++generation.current, controller = new AbortController()
    request.current = controller
    if (reset) { trailRef.current = []; setTrail([]); setIndex(0); setOpened(true) }
    const stale = () => !mounted.current || generation.current !== current || controller.signal.aborted
    try {
      const valid = await revalidate()
      if (stale()) return
      if (!navigator.onLine) { setIssue('offline'); return }
      if (!valid) { setIssue('error'); return }
      const result = await fetchLocationMovements(id, { limit: pageSize, ...(cursor === undefined ? {} : { cursor }) }, controller.signal)
      if (stale()) return
      if (!navigator.onLine) { setIssue('offline'); return }
      const updated = appendMovementsPage(trailRef.current, result, cursor, id)
      trailRef.current = updated; setTrail(updated); setIndex(updated.length - 1)
      if (restoring) {
        focusAfterResume.current = document.activeElement === resumeButton.current
        phaseRef.current = 'ready'; setPhase('ready')
      }
    } catch (error) {
      if (stale()) return
      if (error instanceof ApiError && (error.status === 401 || error.code === 'PASSWORD_CHANGE_REQUIRED')) {
        onSessionLost(error.code === 'PASSWORD_CHANGE_REQUIRED')
      } else setIssue(error instanceof ApiError && error.code === 'INVALID_CURSOR' ? 'changed' : 'error')
    } finally {
      if (mounted.current && generation.current === current) { pending.current = false; request.current = null; setWorking(false) }
    }
  }

  const first = trail[0]?.read, changed = !!first && revision !== undefined && (first.revision !== revision || first.value.deleted)
  const page = trail[index]?.read.value
  const show = phase === 'ready' && online && checked && !checking && !invalid && !working && !issue && !!page && !changed
  const canPage = canStart && show
  function goNext() {
    if (!canPage || !page?.next_cursor) return
    if (index + 1 < trail.length) setIndex(index + 1)
    else void read(page.next_cursor, false)
  }

  return <section aria-labelledby="location-movements-title" className="min-w-0 space-y-4 rounded-lg border p-5">
    <h2 ref={heading} id="location-movements-title" tabIndex={-1} className="rounded-sm text-xl font-semibold focus-visible:outline-2 focus-visible:outline-ring">{text.movementsTitle}</h2>
    <p className="max-w-prose text-sm leading-relaxed text-muted-foreground">{text.movementsHelp}</p>
    {phase === 'cleared' ? <div className="space-y-3"><p role="status">{text.movementsCleared}</p><Button type="button" variant="outline" className={control} onClick={() => onSessionLost(false)}>{text.movementsLogin}</Button></div>
      : phase === 'suspended' || phase === 'restored' ? <div className="space-y-3"><p role="status">{text.movementsRestore}</p>
        {phase === 'restored' && <Button ref={resumeButton} type="button" variant="outline" className={`${control} aria-disabled:opacity-50`} aria-disabled={!canStart}
          onClick={() => { if (canStart) void read(undefined, true, true) }}>{text.movementsContinue}</Button>}
        {working && <p role="status">{text.movementsLoading}</p>}{issue === 'error' && <p role="alert">{text.movementsError}</p>}</div>
        : <div className="min-w-0 space-y-4" aria-busy={working}>
          {!online || issue === 'offline' ? <p role="status">{text.movementsOffline}</p>
            : opened && (checking || !checked || working) ? <p role="status">{text.movementsLoading}</p>
              : opened && (invalid || issue === 'error') ? <p role="alert">{text.movementsError}</p>
                : opened && (issue === 'changed' || changed) ? <p role="alert">{text.movementsChanged}</p> : null}
          {show && page && <div className="min-w-0 space-y-4"><p className="whitespace-pre-wrap text-sm [overflow-wrap:anywhere]">{text.movementsOwner}：{quoteExactText(id)}</p>
            {page.deleted && <p role="status">{text.movementsDeleted}</p>}
            <p role="status" className="text-sm leading-relaxed text-muted-foreground">{text.movementsPage}{index + 1}；{text.movementsCount}{page.data.length}</p>
            {page.data.length === 0 ? <p>{index === 0 ? text.movementsEmpty : text.movementsEmptyPage}</p> : <MovementRows data={page.data} />}
            <p className="text-sm leading-relaxed text-muted-foreground">{page.next_cursor === null ? text.movementsEnd : text.movementsMore}</p></div>}
          <nav aria-label={text.movementsPagination} className="flex max-w-full flex-wrap gap-2">
            {opened && <><Button type="button" variant="outline" className={`${control} aria-disabled:opacity-50`} aria-disabled={!canPage || index === 0} onClick={() => { if (canPage && index > 0) setIndex(index - 1) }}>{text.movementsPrevious}</Button>
              <Button type="button" variant="outline" className={`${control} aria-disabled:opacity-50`} aria-disabled={!canPage || !page?.next_cursor} onClick={goNext}>{text.movementsNext}</Button></>}
            <Button type="button" variant="outline" className={`${control} aria-disabled:opacity-50`} aria-disabled={!canStart} onClick={() => { if (canStart) void read(undefined, true) }}>{opened ? text.movementsRestart : text.movementsOpen}</Button>
          </nav>
        </div>}
  </section>
}
