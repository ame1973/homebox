import { useLayoutEffect, useRef, useState } from 'react'
import { flushSync } from 'react-dom'
import { Link } from 'react-router'
import { Button } from '@/components/ui/button'
import { mutationControl as control } from '@/components/mutation-review'
import { ApiError } from '@/lib/api/client'
import { pathIDSchema } from '@/lib/api/domain'
import { fetchLocationContents, type LocationContents } from '@/lib/api/locations'
import { catalogRoute } from '@/lib/catalog-navigation'
import { unitRoute } from '@/lib/holding-navigation'
import { locationReturnPath, locationRoute } from '@/lib/location-navigation'
import { messages } from '@/lib/messages'
import { subscribeSessionCleared } from '@/lib/session-lifecycle'
import { quoteExactText } from '@/lib/text-literal'
import { baseQuantity } from '@/features/holdings/display'
import { appendContentsPage, type ContentsTrail } from './contents-trail'

const text = messages.locations, pageSize = 20
type Scope = 'direct' | 'subtree'
type Phase = 'ready' | 'suspended' | 'restored' | 'cleared'
type Issue = 'error' | 'changed' | 'offline' | undefined
type Props = {
  id: string; revision: number; online: boolean; checked: boolean; checking: boolean; invalid: boolean
  revalidate: () => Promise<boolean>; onSessionLost: (mandatory: boolean) => void
}

function addressable(id: string): boolean {
  return pathIDSchema.safeParse(id).success && !id.includes('%2F')
}
function locationLink(id: string): string | undefined {
  if (!addressable(id)) return undefined
  const path = locationRoute(id)
  return locationReturnPath(path) === path ? path : undefined
}
function Field({ label, value }: { label: string; value: string }) {
  return <div className="min-w-0 space-y-1"><dt className="text-sm text-muted-foreground">{label}</dt><dd className="whitespace-pre-wrap text-base [overflow-wrap:anywhere]">{value}</dd></div>
}
function ContentsRows({ data }: { data: LocationContents }) {
  return <div className="min-w-0 space-y-6">
    {data.units.length > 0 && <section aria-labelledby="contents-units" className="min-w-0 space-y-3"><h3 id="contents-units" className="text-lg font-semibold">{text.contentsUnits}</h3>
      <ul className="divide-y border-y">{data.units.map((unit) => {
        const canUnit = addressable(unit.id) && addressable(unit.item_id), canItem = addressable(unit.item_id)
        return <li key={unit.id} className="min-w-0 space-y-3 py-4"><h4 className="whitespace-pre-wrap font-semibold [overflow-wrap:anywhere]">{quoteExactText(unit.asset_code)}</h4>
          <dl className="grid min-w-0 gap-3 sm:grid-cols-2"><Field label={text.contentsUnitID} value={quoteExactText(unit.id)} /><Field label={text.contentsItemID} value={quoteExactText(unit.item_id)} />
            <Field label={text.contentsLocationID} value={quoteExactText(unit.location_id!)} /><Field label={messages.holdings.fields.status} value={messages.holdings.statuses[unit.status]} />
            <Field label={text.contentsSerial} value={unit.serial_number === null ? messages.catalog.notProvided : quoteExactText(unit.serial_number)} /><Field label={text.contentsNotes} value={quoteExactText(unit.notes)} /></dl>
          <div className="flex max-w-full flex-wrap gap-2">{canUnit && <Button asChild variant="outline" className={control}><Link to={unitRoute(unit.id)} aria-label={`${text.contentsUnitLink}：${quoteExactText(unit.id)}`}>{text.contentsUnitLink}</Link></Button>}
            {canItem && <Button asChild variant="outline" className={control}><Link to={catalogRoute(unit.item_id)} aria-label={`${text.contentsItemLink}：${quoteExactText(unit.item_id)}`}>{text.contentsItemLink}</Link></Button>}</div>
          {(!canUnit || !canItem) && <p className="text-sm leading-relaxed text-muted-foreground">{text.contentsUnaddressable}</p>}</li>
      })}</ul></section>}
    {data.stock.length > 0 && <section aria-labelledby="contents-stock" className="min-w-0 space-y-3"><h3 id="contents-stock" className="text-lg font-semibold">{text.contentsStock}</h3>
      <ul className="divide-y border-y">{data.stock.map((row) => <li key={JSON.stringify([row.item_id, row.location_id])} className="min-w-0 space-y-3 py-4">
        <dl className="grid min-w-0 gap-3 sm:grid-cols-2"><Field label={text.contentsItemID} value={quoteExactText(row.item_id)} /><Field label={text.contentsLocationID} value={quoteExactText(row.location_id)} />
          <Field label={text.contentsQuantity} value={baseQuantity(row.quantity_base, row.base_unit)} /><Field label={text.contentsMeasurement} value={`1 ${quoteExactText(row.unit_code)} = ${String(row.scale_factor)} ${quoteExactText(row.base_unit)}`} /></dl>
        {row.quantity_base === 0 && <p className="text-sm leading-relaxed text-muted-foreground">{text.contentsZero}</p>}
        {addressable(row.item_id) ? <Button asChild variant="outline" className={control}><Link to={catalogRoute(row.item_id)} aria-label={`${text.contentsItemLink}：${quoteExactText(row.item_id)}`}>{text.contentsItemLink}</Link></Button> : <p className="text-sm leading-relaxed text-muted-foreground">{text.contentsUnaddressable}</p>}
      </li>)}</ul></section>}
    {data.sub_locations.length > 0 && <section aria-labelledby="contents-locations" className="min-w-0 space-y-3"><h3 id="contents-locations" className="text-lg font-semibold">{text.contentsLocations}</h3>
      <ul className="divide-y border-y">{data.sub_locations.map((location) => {
        const path = locationLink(location.id)
        return <li key={location.id} className="min-w-0 space-y-3 py-4"><h4 className="whitespace-pre-wrap font-semibold [overflow-wrap:anywhere]">{quoteExactText(location.name)}</h4>
          <dl className="grid min-w-0 gap-3 sm:grid-cols-2"><Field label={text.code} value={quoteExactText(location.code)} /><Field label={text.status} value={location.status === 'active' ? text.active : text.archived} />
            <Field label={text.contentsLocationID} value={quoteExactText(location.id)} /><Field label={text.contentsParentID} value={location.parent_id === null ? messages.catalog.notProvided : quoteExactText(location.parent_id)} /></dl>
          {path ? <Button asChild variant="outline" className={control}><Link to={path} aria-label={`${text.contentsLocationLink}：${quoteExactText(location.id)}`}>{text.contentsLocationLink}</Link></Button> : <p className="text-sm leading-relaxed text-muted-foreground">{text.contentsUnaddressable}</p>}</li>
      })}</ul></section>}
  </div>
}

export function LocationContentsPanel({ id, revision, online, checked, checking, invalid, revalidate, onSessionLost }: Props) {
  const [phase, setPhase] = useState<Phase>('ready'), [scope, setScope] = useState<Scope>('subtree')
  const [opened, setOpened] = useState(false), [working, setWorking] = useState(false), [issue, setIssue] = useState<Issue>()
  const [trail, setTrail] = useState<ContentsTrail>([]), [index, setIndex] = useState(0)
  const phaseRef = useRef<Phase>('ready'), generation = useRef(0), mounted = useRef(false), pending = useRef(false)
  const request = useRef<AbortController | null>(null), trailRef = useRef<ContentsTrail>([])
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
  async function read(selected: Scope, cursor: string | undefined, reset: boolean, restoring = false) {
    if (!canStart || !navigator.onLine || pending.current || phaseRef.current !== (restoring ? 'restored' : 'ready')) return
    pending.current = true; setWorking(true); setIssue(undefined)
    const current = ++generation.current, controller = new AbortController()
    request.current = controller
    if (reset) { trailRef.current = []; setTrail([]); setIndex(0); setScope(selected); setOpened(true) }
    const stale = () => !mounted.current || generation.current !== current || controller.signal.aborted
    try {
      const valid = await revalidate()
      if (stale()) return
      if (!navigator.onLine) { setIssue('offline'); return }
      if (!valid) { setIssue('error'); return }
      const result = await fetchLocationContents(id, { scope: selected, limit: pageSize, ...(cursor ? { cursor } : {}) }, controller.signal)
      if (stale()) return
      if (!navigator.onLine) { setIssue('offline'); return }
      const updated = appendContentsPage(trailRef.current, result, cursor, id, selected)
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

  const changed = trail.length > 0 && trail[0]!.read.revision !== revision
  const page = trail[index]?.read.value
  const show = phase === 'ready' && online && checked && !checking && !invalid && !working && !issue && !!page && !changed
  const canPage = canStart && show
  function goPrevious() { if (canPage && index > 0) setIndex(index - 1) }
  function goNext() {
    if (!canPage || !page?.next_cursor) return
    if (index + 1 < trail.length) setIndex(index + 1)
    else void read(scope, page.next_cursor, false)
  }

  return <section aria-labelledby="location-contents-title" className="min-w-0 space-y-4 rounded-lg border p-5">
    <h2 ref={heading} id="location-contents-title" tabIndex={-1} className="rounded-sm text-xl font-semibold focus-visible:outline-2 focus-visible:outline-ring">{text.contentsTitle}</h2>
    <p className="max-w-prose text-sm leading-relaxed text-muted-foreground">{text.contentsHelp}</p>
    {phase === 'cleared' ? <div className="space-y-3"><p role="status">{text.contentsCleared}</p><Button type="button" variant="outline" className={control} onClick={() => onSessionLost(false)}>{text.contentsLogin}</Button></div>
      : phase === 'suspended' || phase === 'restored' ? <div className="space-y-3"><p role="status">{text.contentsRestore}</p>
        {phase === 'restored' && <Button ref={resumeButton} type="button" variant="outline" className={`${control} aria-disabled:opacity-50`} aria-disabled={!canStart}
          onClick={() => { if (canStart) void read(scope, undefined, true, true) }}>{text.contentsContinue}</Button>}
        {working && <p role="status">{text.contentsLoading}</p>}{issue === 'error' && <p role="alert">{text.contentsError}</p>}</div>
        : <div className="min-w-0 space-y-4" aria-busy={working}>
          <fieldset className="min-w-0 space-y-2"><legend className="font-semibold">{text.contentsScope}</legend>
            <div className="flex min-w-0 flex-wrap gap-x-5">{(['direct', 'subtree'] as const).map((value) => <label key={value} className="flex min-h-12 min-w-0 items-center gap-2 py-2 text-base leading-relaxed">
              <input type="radio" name={`contents-scope-${id}`} value={value} checked={scope === value} aria-disabled={!canStart} className="size-4 shrink-0 accent-primary focus-visible:outline-2 focus-visible:outline-ring"
                onChange={() => { if (canStart && scope !== value) void read(value, undefined, true) }} /><span>{value === 'direct' ? text.contentsDirect : text.contentsSubtree}</span></label>)}</div>
            <p className="max-w-prose text-sm leading-relaxed text-muted-foreground">{text.contentsScopeHelp}</p></fieldset>
          {!opened ? <Button type="button" variant="outline" className={`${control} aria-disabled:opacity-50`} aria-disabled={!canStart} onClick={() => { if (canStart) void read(scope, undefined, true) }}>{text.contentsOpen}</Button> : <>
            {!online || issue === 'offline' ? <p role="status">{text.contentsOffline}</p>
              : checking || !checked || working ? <p role="status">{text.contentsLoading}</p>
                : invalid || issue === 'error' ? <p role="alert">{text.contentsError}</p>
                  : issue === 'changed' || changed ? <p role="alert">{text.contentsChanged}</p> : null}
            {show && page && <div className="min-w-0 space-y-4"><p role="status" className="text-sm leading-relaxed text-muted-foreground">{text.contentsPage}{index + 1}；{text.contentsCount}{page.units.length + page.stock.length + page.sub_locations.length}</p>
              {page.units.length + page.stock.length + page.sub_locations.length === 0 ? <p>{index === 0 ? text.contentsEmpty : text.contentsEmptyPage}</p> : <ContentsRows data={page} />}
              <p className="text-sm leading-relaxed text-muted-foreground">{page.next_cursor === null ? text.contentsEnd : text.contentsMore}</p></div>}
            <nav aria-label={text.contentsPagination} className="flex max-w-full flex-wrap gap-2">
              <Button type="button" variant="outline" className={`${control} aria-disabled:opacity-50`} aria-disabled={!canPage || index === 0} onClick={goPrevious}>{text.contentsPrevious}</Button>
              <Button type="button" variant="outline" className={`${control} aria-disabled:opacity-50`} aria-disabled={!canPage || !page?.next_cursor} onClick={goNext}>{text.contentsNext}</Button>
              <Button type="button" variant="outline" className={`${control} aria-disabled:opacity-50`} aria-disabled={!canStart} onClick={() => { if (canStart) void read(scope, undefined, true) }}>{text.contentsRestart}</Button>
            </nav>
          </>}
        </div>}
  </section>
}
