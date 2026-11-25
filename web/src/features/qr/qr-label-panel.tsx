import { useId, useLayoutEffect, useRef, useState } from 'react'
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
type Label = { matrix: PreparedQRLabel; code: string; revision: number; path: string; downloadURL?: string }

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

function matrixImage(size: number, path: string): SVGSVGElement {
  const namespace = 'http://www.w3.org/2000/svg', svg = document.createElementNS(namespace, 'svg')
  svg.setAttribute('viewBox', `0 0 ${size} ${size}`); svg.setAttribute('width', String(size)); svg.setAttribute('height', String(size)); svg.setAttribute('shape-rendering', 'crispEdges')
  const background = document.createElementNS(namespace, 'rect'), modules = document.createElementNS(namespace, 'path')
  background.setAttribute('width', String(size)); background.setAttribute('height', String(size)); background.setAttribute('fill', '#ffffff')
  modules.setAttribute('d', path); modules.setAttribute('fill', '#000000'); svg.append(background, modules)
  return svg
}

function revokeURL(url: string) { try { URL.revokeObjectURL(url) } catch { /* Cleanup is best effort. */ } }
function clearPrintDocument(doc: Document | undefined) {
  try { doc?.body?.replaceChildren() } catch { /* A discarded document may be inaccessible. */ }
}

export function QRLabelPanel({ kind, id, revision, online, checked, checking, invalid, onSessionLost }: Props) {
  const headingID = useId(), [phase, setPhase] = useState<Phase>('ready'), [issue, setIssue] = useState<Issue>()
  const [visible, setVisible] = useState(document.visibilityState === 'visible'), [working, setWorking] = useState(false), [label, setLabel] = useState<Label>()
  const [printError, setPrintError] = useState(false)
  const phaseRef = useRef<Phase>('ready'), generation = useRef(0), mounted = useRef(false), pending = useRef(false), request = useRef<AbortController | null>(null)
  const downloadURL = useRef<string | undefined>(undefined), heading = useRef<HTMLHeadingElement>(null), printButton = useRef<HTMLButtonElement>(null)
  const downloadLink = useRef<HTMLAnchorElement>(null), printFrame = useRef<HTMLIFrameElement>(null), actionFocus = useRef({ active: false })
  const printAttempt = useRef<{ frame: HTMLIFrameElement; doc: Document; token: object } | undefined>(undefined)
  const path = label?.path ?? ''
  let target: QRLabelLink | undefined
  try { target = makeQRLabelLink(kind, id, window.location.origin) } catch { /* Preserve unsupported legacy IDs without generating a label. */ }

  useLayoutEffect(() => {
    mounted.current = true
    const cancel = () => {
      generation.current++; pending.current = false; request.current?.abort(); request.current = null
      const url = downloadURL.current; downloadURL.current = undefined
      if (url) revokeURL(url)
      const attempt = printAttempt.current; printAttempt.current = undefined; clearPrintDocument(attempt?.doc)
    }
    const redact = (next: Phase, nextIssue: Issue, synchronous: boolean) => {
      phaseRef.current = next; cancel()
      const apply = () => { setPhase(next); setIssue(nextIssue); setLabel(undefined); setPrintError(false); setWorking(false) }
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
    if (!canStart || !target || (phaseRef.current !== 'ready' && phaseRef.current !== 'restored') || !navigator.onLine || document.visibilityState !== 'visible' || pending.current) return
    const oldURL = downloadURL.current; downloadURL.current = undefined
    if (oldURL) revokeURL(oldURL)
    const oldPrint = printAttempt.current; printAttempt.current = undefined; clearPrintDocument(oldPrint?.doc)
    pending.current = true; setWorking(true); setIssue(undefined); setLabel(undefined); setPrintError(false)
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
      const matrix = { ...result, modules: result.modules.slice() }, imagePath = matrixPath(matrix.size, matrix.modules)
      let url: string | undefined
      try {
        const markup = new XMLSerializer().serializeToString(matrixImage(matrix.size, imagePath))
        url = URL.createObjectURL(new Blob([markup], { type: 'image/svg+xml' }))
      } catch { /* The preview and printing can still work without downloads. */ }
      if (stale() || !navigator.onLine) { if (url) revokeURL(url); if (!stale()) setIssue('offline'); return }
      downloadURL.current = url
      setLabel({ matrix, code: after.code, revision: after.revision, path: imagePath, ...(url ? { downloadURL: url } : {}) })
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
  useLayoutEffect(() => {
    if (!show) return
    const button = printButton.current, link = downloadLink.current, title = heading.current, focus = actionFocus.current, frame = printFrame.current
    const release = () => {
      const attempt = printAttempt.current
      if (attempt?.frame === frame) { printAttempt.current = undefined; clearPrintDocument(attempt.doc) }
    }
    return () => {
      const active = document.activeElement
      if (document.visibilityState === 'visible' && focus.active && (active === document.body || active === button || active === link)) title?.focus()
      focus.active = false; release()
    }
  }, [show])

  function print() {
    if (!show || !label || phaseRef.current !== 'ready' || !navigator.onLine || document.visibilityState !== 'visible') return
    const frame = printFrame.current, child = frame?.contentWindow, doc = frame?.contentDocument
    if (!frame || !child || !doc) { setPrintError(true); return }
    if (printAttempt.current?.frame === frame) return
    const previous = printAttempt.current; printAttempt.current = undefined; clearPrintDocument(previous?.doc)
    try {
      const style = doc.createElement('style')
      style.textContent = '@page{margin:12mm}body{margin:0;color:#000;background:#fff;font:12pt system-ui,sans-serif}h1{font-size:14pt;margin:0 0 4mm}svg{display:block;width:50mm;height:50mm}p{font-size:10pt;margin:3mm 0;white-space:pre-wrap;overflow-wrap:anywhere}'
      const title = doc.createElement('h1'), code = doc.createElement('p'), identity = doc.createElement('p')
      title.textContent = kind === 'location' ? text.printLocation : text.printUnit
      const quoted = quoteExactText(label.code)
      code.textContent = quoted.length <= 256 ? `${text.code}：${quoted}` : text.printCodeOmitted
      identity.textContent = `${text.identity}：${quoteExactText(id)}`
      const documentTitle = doc.createElement('title'); documentTitle.textContent = 'HomeBox QR'
      doc.head.replaceChildren(documentTitle, style)
      doc.body.replaceChildren(title, doc.importNode(matrixImage(label.matrix.size, path), true), code, identity)
      const token = {}; printAttempt.current = { frame, doc, token }
      const clear = () => {
        clearPrintDocument(doc)
        if (printAttempt.current?.token === token) printAttempt.current = undefined
      }
      child.addEventListener('afterprint', clear, { once: true })
      try { child.print() } catch (error) { child.removeEventListener('afterprint', clear); clear(); throw error }
      setPrintError(false)
    } catch { printAttempt.current = undefined; clearPrintDocument(doc); setPrintError(true) }
  }

  return <section aria-labelledby={headingID} className="min-w-0 space-y-4 rounded-lg border p-3 sm:p-5">
    <h2 ref={heading} id={headingID} tabIndex={-1} className="rounded-sm text-xl font-semibold focus-visible:outline-2 focus-visible:outline-ring">{text.title}</h2>
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
            <p className="text-sm leading-relaxed text-muted-foreground">{text.printHelp}</p>
          </div>}
          {phase !== 'suspended' && <Button type="button" variant="outline" className={`${control} aria-disabled:opacity-50`} aria-disabled={!canStart}
            onClick={() => { if (canStart) void generate() }}>{phase === 'restored' ? text.continue : label ? text.again : text.create}</Button>}
          {show && label && <div className="min-w-0 space-y-3">
            <div className="flex max-w-full flex-wrap gap-2">
              <Button ref={printButton} type="button" variant="outline" className={control} onClick={print}
                onFocus={() => { actionFocus.current.active = true }} onBlur={() => { actionFocus.current.active = false }}>{text.print}</Button>
              {label.downloadURL ? <Button asChild variant="outline" className={control}><a ref={downloadLink} href={label.downloadURL} download={`homebox-${kind}-qr.svg`}
                onFocus={() => { actionFocus.current.active = true }} onBlur={() => { actionFocus.current.active = false }}
                onClick={(event) => { if (phaseRef.current !== 'ready' || downloadURL.current !== label.downloadURL || !navigator.onLine || document.visibilityState !== 'visible') event.preventDefault() }}>{text.download}</a></Button> : null}
            </div>
            {!label.downloadURL && <p role="status" className="text-sm">{text.downloadError}</p>}
            {printError && <p role="alert" className="text-sm">{text.printError}</p>}
            <iframe ref={printFrame} title={text.printFrame} aria-hidden="true" tabIndex={-1} className="pointer-events-none fixed size-px opacity-0" />
          </div>}
        </div>}
  </section>
}
