import { useLayoutEffect, useRef, useState } from 'react'
import { flushSync } from 'react-dom'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { mutationControl as control } from '@/components/mutation-review'
import { idSchema } from '@/lib/api/domain'
import { messages } from '@/lib/messages'
import { subscribeSessionCleared } from '@/lib/session-lifecycle'
import { LocationMovementsPanel } from './location-movements-panel'

const text = messages.locations
type Phase = 'ready' | 'suspended' | 'restored' | 'cleared'
type Props = {
  online: boolean; checked: boolean; checking: boolean; invalid: boolean
  revalidate: () => Promise<boolean>; onSessionLost: (mandatory: boolean) => void
}

export function LocationMovementLookup({ online, checked, checking, invalid, revalidate, onSessionLost }: Props) {
  const [phase, setPhase] = useState<Phase>('ready'), [raw, setRaw] = useState(''), [format, setFormat] = useState<'plain' | 'json'>('plain')
  const [selected, setSelected] = useState<{ id: string; generation: number }>(), [error, setError] = useState(false), [working, setWorking] = useState(false), [resumeError, setResumeError] = useState(false)
  const phaseRef = useRef<Phase>('ready'), generation = useRef(0), selectedGeneration = useRef(0), mounted = useRef(false), pending = useRef(false)
  const heading = useRef<HTMLHeadingElement>(null), resumeButton = useRef<HTMLButtonElement>(null), focusAfterResume = useRef(false)

  useLayoutEffect(() => {
    mounted.current = true
    const invalidate = () => { generation.current++; pending.current = false }
    const redact = (next: Phase, synchronous: boolean) => {
      invalidate(); phaseRef.current = next; focusAfterResume.current = false
      const apply = () => { setPhase(next); setRaw(''); setFormat('plain'); setSelected(undefined); setError(false); setWorking(false); setResumeError(false) }
      if (synchronous) flushSync(apply); else apply()
    }
    const cleared = (source: 'local' | 'remote') => { redact('cleared', source === 'remote') }
    const pagehide = () => { if (phaseRef.current !== 'cleared') redact('suspended', true) }
    const pageshow = (event: PageTransitionEvent) => {
      if (event.persisted && phaseRef.current === 'suspended') { phaseRef.current = 'restored'; setPhase('restored') }
    }
    const unsubscribe = subscribeSessionCleared(cleared)
    window.addEventListener('pagehide', pagehide); window.addEventListener('pageshow', pageshow)
    return () => { mounted.current = false; invalidate(); unsubscribe(); window.removeEventListener('pagehide', pagehide); window.removeEventListener('pageshow', pageshow) }
  }, [])

  useLayoutEffect(() => {
    if (phase === 'ready' && focusAfterResume.current) { focusAfterResume.current = false; heading.current?.focus() }
  }, [phase])

  const canChoose = online && checked && !checking && !working
  function choose() {
    if (!canChoose || !navigator.onLine || phaseRef.current !== 'ready') return
    let value: unknown = raw
    if (format === 'json') {
      try { value = JSON.parse(raw) } catch { setError(true); return }
    }
    const result = idSchema.safeParse(value)
    if (!result.success) { setError(true); return }
    setError(false); setSelected({ id: result.data, generation: ++selectedGeneration.current })
  }
  async function resume() {
    if (!canChoose || !navigator.onLine || pending.current || phaseRef.current !== 'restored') return
    pending.current = true; setWorking(true); setResumeError(false)
    const current = ++generation.current
    let valid = false
    try { valid = await revalidate() } catch { /* Do not expose a raw transport error. */ }
    if (!mounted.current || generation.current !== current) return
    pending.current = false; setWorking(false)
    if (!valid || !navigator.onLine) { setResumeError(true); return }
    focusAfterResume.current = document.activeElement === resumeButton.current
    phaseRef.current = 'ready'; setPhase('ready')
  }
  function edit(value: string) { setSelected(undefined); setError(false); setRaw(value) }
  function selectFormat(value: 'plain' | 'json') { setSelected(undefined); setError(false); setFormat(value) }

  return <section aria-labelledby="location-movement-lookup-title" className="min-w-0 space-y-4 rounded-lg border p-5">
    <h2 ref={heading} id="location-movement-lookup-title" tabIndex={-1} className="rounded-sm text-xl font-semibold focus-visible:outline-2 focus-visible:outline-ring">{text.movementLookupTitle}</h2>
    <p className="max-w-prose text-sm leading-relaxed text-muted-foreground">{text.movementLookupHelp}</p>
    {phase === 'cleared' ? <div className="space-y-3"><p role="status">{text.movementLookupCleared}</p><Button type="button" variant="outline" className={control} onClick={() => onSessionLost(false)}>{text.movementsLogin}</Button></div>
      : phase === 'suspended' || phase === 'restored' ? <div className="space-y-3"><p role="status">{text.movementLookupRestore}</p>
        {phase === 'restored' && <Button ref={resumeButton} type="button" variant="outline" className={`${control} aria-disabled:opacity-50`} aria-disabled={!canChoose}
          onClick={() => { if (canChoose) void resume() }}>{text.movementLookupContinue}</Button>}
        {working && <p role="status">{text.movementLookupChecking}</p>}{resumeError && <p role="alert">{text.movementLookupError}</p>}</div>
        : <div className="min-w-0 space-y-4">
          <fieldset className="min-w-0 space-y-2"><legend className="font-semibold">{text.movementLookupFormat}</legend>
            <div className="flex min-w-0 flex-wrap gap-x-5">{(['plain', 'json'] as const).map((value) => <label key={value} className="flex min-h-12 min-w-0 items-center gap-2 py-2 text-base leading-relaxed">
              <input type="radio" name="location-movement-format" value={value} checked={format === value} onChange={() => selectFormat(value)} className="size-4 shrink-0 accent-primary focus-visible:outline-2 focus-visible:outline-ring" />
              <span>{value === 'plain' ? text.movementLookupPlain : text.movementLookupJSON}</span></label>)}</div>
            <p className="max-w-prose text-sm leading-relaxed text-muted-foreground">{text.movementLookupFormatHelp}</p></fieldset>
          <div className="min-w-0 space-y-2"><Label htmlFor="location-movement-id">{text.movementLookupID}</Label>
            <Textarea id="location-movement-id" value={raw} maxLength={1538} onChange={(event) => edit(event.target.value)} aria-invalid={error} aria-describedby={error ? 'location-movement-id-error' : undefined} className="min-h-24 w-full [overflow-wrap:anywhere]" />
            {error && <p id="location-movement-id-error" role="alert" className="text-sm text-destructive">{text.movementLookupInvalid}</p>}</div>
          <Button type="button" variant="outline" className={`${control} aria-disabled:opacity-50`} aria-disabled={!canChoose} onClick={choose}>{text.movementLookupChoose}</Button>
          {selected && <><p role="status" className="text-sm leading-relaxed text-muted-foreground">{text.movementLookupSelected}</p>
            <LocationMovementsPanel key={selected.generation} id={selected.id} online={online} checked={checked} checking={checking} invalid={invalid} revalidate={revalidate} onSessionLost={onSessionLost} /></>}
        </div>}
  </section>
}
