import { useEffect, useRef, useState } from 'react'
import { CircleCheck, LoaderCircle, ShieldAlert } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { messages } from '@/lib/messages'
import { monitorShell, type ShellObservation } from '@/lib/shell-monitor'

const text = messages.shell

export function ShellIndicator() {
  const [state, setState] = useState<ShellObservation>({ phase: 'checking', busy: true })
  const monitor = useRef<ReturnType<typeof monitorShell>>(undefined)
  useEffect(() => {
    let active = true
    const current = monitorShell((value) => { if (active) setState(value) })
    monitor.current = current
    return () => { active = false; if (monitor.current === current) monitor.current = undefined; current.stop() }
  }, [])

  const preparing = state.phase === 'available' || state.phase === 'missing' || state.phase === 'error'
  const unavailable = state.busy || state.phase === 'paused'
  const label = state.busy ? state.phase === 'installing' ? text.busyInstall : text.busy
    : state.phase === 'available' ? text.prepareUpdate : state.phase === 'missing' ? text.prepare : state.phase === 'error' ? text.retry : text.check
  const Icon = state.phase === 'checking' || state.phase === 'installing' ? LoaderCircle : state.phase === 'ready' ? CircleCheck : ShieldAlert

  return <aside aria-label={text.label} className="flex min-w-0 flex-wrap items-center justify-between gap-3 border-b py-3">
    <div aria-live="polite" aria-atomic="true" className="min-w-0 space-y-1">
      <p className="flex items-center gap-2 text-sm font-medium"><Icon aria-hidden="true" className={`size-4 shrink-0 ${state.phase === 'checking' || state.phase === 'installing' ? 'animate-spin motion-reduce:animate-none' : ''}`} />{text.phases[state.phase]}</p>
      <p className="max-w-prose text-sm leading-relaxed text-muted-foreground">{text.help[state.phase]}</p>
    </div>
    {state.phase !== 'unsupported' && <Button type="button" variant="outline" aria-disabled={unavailable || undefined} className="min-h-11 h-auto max-w-full whitespace-normal py-2 aria-disabled:opacity-70" onClick={() => {
      if (unavailable) return
      if (preparing) monitor.current?.prepare()
      else monitor.current?.check()
    }}>{label}</Button>}
  </aside>
}
