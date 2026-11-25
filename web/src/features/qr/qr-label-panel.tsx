import { useId, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { flushSync } from 'react-dom'
import { Button } from '@/components/ui/button'
import { mutationControl as control } from '@/components/mutation-review'
import { fetchAdmin } from '@/lib/api/auth'
import { ApiError } from '@/lib/api/client'
import { fetchUnit } from '@/lib/api/holdings'
import { fetchLocation } from '@/lib/api/locations'
import { messages } from '@/lib/messages'
import { prepareQRLabel, type PreparedQRLabel } from '@/lib/qr-label-client'
import { makeQRLabelLink, type QRLabelKind, type QRLabelLink } from '@/lib/qr-label-link'
import { subscribeSessionCleared } from '@/lib/session-lifecycle'
import { quoteExactText } from '@/lib/text-literal'

const text = messages.qr
type Phase = 'ready' | 'suspended' | 'restored' | 'cleared'
type Issue = 'changed' | 'error' | 'offline' | 'hidden' | undefined
type Props = {
  kind: QRLabelKind; id: string; revision: number; online: boolean; checked: boolean; checking: boolean; invalid: boolean
  onSessionLost: (mandatory: boolean) => void
}
type Label = { matrix: PreparedQRLabel; code: string; revision: number }

// Only numeric module coordinates enter the SVG path; source text is rendered
// separately by React and never interpolated into SVG markup.
function matrixPath(size: number, modules: Uint8Array): string {
  let result = ''
  for (let y = 0; y < size; y++) for (let x = 0; x < size;) {
    if (!modules[y * size + x]) { x++; continue }
    const start = x
    while (x < size && modules[y * size + x]) x++
    result += `M${start} ${y}h${x - start}v1h-${x - start}z`
  }
  return result
}

export function QRLabelPanel({ kind, id, revision, online, checked, checking, invalid, onSessionLost }: Props) {
  const headingID = useId(), [phase, setPhase] = useState<Phase>('ready'), [issue, setIssue] = useState<Issue>()
  const [visible, setVisible] = useState(document.visibilityState === 'visible'), [working, setWorking] = useState(false), [label, setLabel] = useState<Label>()
  const phaseRef = useRef<Phase>('ready'), generation = useRef(0), mounted = useRef(false), pending = useRef(false), request = useRef<AbortController | null>(null)
  const path = useMemo(() => label ? matrixPath(label.matrix.size, label.matrix.modules) : '', [label])
  let target: QRLabelLink | undefined
  try { target = makeQRLabelLink(kind, id, window.location.origin) } catch { /* Preserve unsupported legacy IDs without generating a label. */ }

  useLayoutEffect(() => {
    mounted.current = true
    const cancel = () => { generation.current++; pending.current = false; request.current?.abort(); request.current = null }
    const redact = (next: Phase, nextIssue: Issue, synchronous: boolean) => {
      cancel(); phaseRef.current = next
      const apply = () => { setPhase(next); setIssue(nextIssue); setLabel(undefined); setWorking(false) }
      if (synchronous) flushSync(apply); else apply()
    }
    const cleared = (source: 'local' | 'remote') => { redact('cleared', undefined, source === 'remote') }
    const visibility = () => {
      const nowVisible = document.visibilityState === 'visible'
      if (!nowVisible && (phaseRef.current === 'ready' || phaseRef.current === 'restored')) { redact(phaseRef.current, 'hidden', true); setVisible(false) }
      else setVisible(nowVisible)
    }
    const offline = () => { if (phaseRef.current === 'ready' || phaseRef.current === 'restored') redact(phaseRef.current, 'offline', true) }
    const pagehide = () => { if (phaseRef.current !== 'cleared') redact('suspended', undefined, true) }
    const pageshow = (event: PageTransitionEvent) => {
      if (event.persisted && phaseRef.current === 'suspended') { phaseRef.current = 'restored'; setPhase('restored') }
    }
    const unsubscribe = subscribeSessionCleared(cleared)
    document.addEventListener('visibilitychange', visibility)
    window.addEventListener('offline', offline); window.addEventListener('pagehide', pagehide); window.addEventListener('pageshow', pageshow)
    return () => {
      mounted.current = false; cancel(); unsubscribe()
      document.removeEventListener('visibilitychange', visibility)
      window.removeEventListener('offline', offline); window.removeEventListener('pagehide', pagehide); window.removeEventListener('pageshow', pageshow)
    }
  }, [id, kind, revision])

  const canStart = !!target && online && checked && !checking && !invalid && visible && !working && (phase === 'ready' || phase === 'restored')
  async function generate() {
    if (!canStart || !target || !navigator.onLine || document.visibilityState !== 'visible' || pending.current) return
    pending.current = true; setWorking(true); setIssue(undefined); setLabel(undefined)
    const current = ++generation.current, controller = new AbortController()
    request.current = controller
    const stale = () => !mounted.current || generation.current !== current || controller.signal.aborted || document.visibilityState !== 'visible'
    const read = async () => {
      const session = await fetchAdmin(controller.signal)
      if (stale()) return undefined
      if (session.must_change_password) { onSessionLost(true); return undefined }
      const record = kind === 'location' ? await fetchLocation(id, controller.signal) : await fetchUnit(id, controller.signal)
      if (stale()) return undefined
      return { principal: session.id, id: record.id, revision: record.revision, code: 'code' in record ? record.code : record.asset_code }
    }
    try {
      const before = await read()
      if (!before || stale()) return
      if (!navigator.onLine) { setIssue('offline'); return }
      if (before.id !== id || before.revision !== revision) { setIssue('changed'); return }
      const result = await prepareQRLabel(kind, id, window.location.origin, controller.signal)
      if (stale()) return
      if (result.kind !== 'ready') { setIssue(navigator.onLine ? 'error' : 'offline'); return }
      if (result.link.kind !== target.kind || result.link.id !== target.id || result.link.url !== target.url || result.link.path !== target.path) { setIssue('error'); return }
      const after = await read()
      if (!after || stale()) return
      if (!navigator.onLine) { setIssue('offline'); return }
      if (after.principal !== before.principal || after.id !== id || after.revision !== before.revision || after.code !== before.code) { setIssue('changed'); return }
      setLabel({ matrix: { ...result, modules: result.modules.slice() }, code: after.code, revision: after.revision })
      phaseRef.current = 'ready'; setPhase('ready')
    } catch (error) {
      if (stale()) return
      if (error instanceof ApiError && (error.status === 401 || error.code === 'PASSWORD_CHANGE_REQUIRED')) onSessionLost(error.code === 'PASSWORD_CHANGE_REQUIRED')
      else setIssue(navigator.onLine ? 'error' : 'offline')
    } finally {
      if (mounted.current && generation.current === current) { pending.current = false; request.current = null; setWorking(false) }
    }
  }

  const show = phase === 'ready' && online && checked && !checking && !invalid && visible && !working && !!label && label.revision === revision && label.matrix.link.kind === kind && label.matrix.link.id === id
  return <section aria-labelledby={headingID} className="min-w-0 space-y-4 rounded-lg border p-3 sm:p-5">
    <h2 id={headingID} className="text-xl font-semibold">{text.title}</h2>
    <p className="max-w-prose text-sm leading-relaxed text-muted-foreground">{text.help}</p>
    {phase === 'cleared' ? <div className="space-y-3"><p role="status">{text.cleared}</p><Button type="button" variant="outline" className={control} onClick={() => onSessionLost(false)}>{text.login}</Button></div>
      : !target ? <p role="status">{text.unsupported}</p>
        : <div className="min-w-0 space-y-4" aria-busy={working}>
          {(phase === 'suspended' || phase === 'restored') && <p role="status">{text.restore}</p>}
          {!online || issue === 'offline' ? <p role="status">{text.offline}</p>
            : issue === 'hidden' ? <p role="status">{text.hidden}</p>
              : issue === 'changed' ? <p role="alert">{text.changed}</p>
                : issue === 'error' ? <p role="alert">{text.error}</p>
                  : working || (checking && phase === 'ready') ? <p role="status">{text.loading}</p> : null}
          {show && label && <div className="min-w-0 space-y-3">
            <svg role="img" aria-label={kind === 'location' ? text.locationImage : text.unitImage} viewBox={`0 0 ${label.matrix.size} ${label.matrix.size}`}
              shapeRendering="crispEdges" className="block w-full max-w-64" xmlns="http://www.w3.org/2000/svg">
              <rect width={label.matrix.size} height={label.matrix.size} fill="#ffffff" /><path d={path} fill="#000000" />
            </svg>
            <dl className="space-y-2 text-sm"><div><dt className="text-muted-foreground">{text.code}</dt><dd className="whitespace-pre-wrap [overflow-wrap:anywhere]">{quoteExactText(label.code)}</dd></div>
              <div><dt className="text-muted-foreground">{text.identity}</dt><dd className="[overflow-wrap:anywhere]">{quoteExactText(id)}</dd></div>
              <div><dt className="text-muted-foreground">{text.address}</dt><dd className="[overflow-wrap:anywhere]">{label.matrix.link.url}</dd></div></dl>
            <p className="text-sm leading-relaxed text-muted-foreground">{text.future}</p>
          </div>}
          {phase !== 'suspended' && <Button type="button" variant="outline" className={`${control} aria-disabled:opacity-50`} aria-disabled={!canStart}
            onClick={() => { if (canStart) void generate() }}>{phase === 'restored' ? text.continue : label ? text.again : text.create}</Button>}
        </div>}
  </section>
}
