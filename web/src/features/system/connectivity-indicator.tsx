import { useEffect, useRef, useState } from 'react'
import { flushSync } from 'react-dom'
import { useQueryClient } from '@tanstack/react-query'
import { Link } from 'react-router'
import { CircleCheck, LoaderCircle, ShieldAlert, WifiOff } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { monitorForegroundConnectivity, type ConnectivityObservation } from '@/lib/foreground-connectivity'
import { messages } from '@/lib/messages'
import { subscribeSessionCleared } from '@/lib/session-lifecycle'
import { validText } from '@/lib/text-literal'

const text = messages.connectivityIndicator
const authKey = ['auth', 'me'] as const

type Gate = { identity: string | null; generation: number; checking: boolean }

function completedIdentity(value: unknown): string | null {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) return null
  const admin = value as Record<string, unknown>
  if (typeof admin.id !== 'string' || !validText(admin.id, 1, 128) ||
    typeof admin.username !== 'string' || !validText(admin.username, 1, 64) ||
    typeof admin.revision !== 'number' || !Number.isSafeInteger(admin.revision) || admin.revision < 1 ||
    admin.must_change_password !== false) return null
  return JSON.stringify([admin.id, admin.username, admin.revision])
}

export function ConnectivityIndicator() {
  const client = useQueryClient()
  const [gate, setGate] = useState<Gate>({ identity: null, generation: 0, checking: false })
  const [observed, setObserved] = useState<{ generation: number; value: ConnectivityObservation }>()
  const [suspended, setSuspended] = useState(false)
  const terminal = useRef(false)

  useEffect(() => {
    const pagehide = () => {
      terminal.current = true
      void client.cancelQueries({ queryKey: authKey, exact: true })
      // Commit the redacted state before a browser can freeze this document.
      flushSync(() => setSuspended(true))
    }
    window.addEventListener('pagehide', pagehide)
    const clear = () => {
      terminal.current = true
      setGate((previous) => ({ identity: null, generation: previous.generation + 1, checking: false }))
      void client.cancelQueries({ queryKey: authKey, exact: true })
    }
    const unsubscribeSession = subscribeSessionCleared(clear)
    const unsubscribeCache = client.getQueryCache().subscribe((event) => {
      const key = event.query.queryKey
      if (key.length !== 2 || key[0] !== 'auth' || key[1] !== 'me') return
      if (event.type === 'removed') {
        terminal.current = true
        setGate((previous) => ({ identity: null, generation: previous.generation + 1, checking: false }))
      } else if (event.type === 'updated' && event.action.type === 'fetch') {
        setGate((previous) => previous.identity ? { ...previous, checking: true } : previous)
      } else if (event.type === 'updated' && event.action.type === 'success') {
        const identity = completedIdentity(event.query.state.data), restart = terminal.current
        terminal.current = identity === null
        setSuspended(false)
        setGate((previous) => ({ identity, generation: previous.generation + (previous.identity !== identity || restart ? 1 : 0), checking: false }))
      } else if (event.type === 'updated' && event.action.type === 'error') {
        terminal.current = true
        setGate((previous) => ({ identity: null, generation: previous.generation + 1, checking: false }))
      }
    })
    return () => { window.removeEventListener('pagehide', pagehide); unsubscribeCache(); unsubscribeSession() }
  }, [client])

  useEffect(() => {
    if (!gate.identity) return
    const generation = gate.generation
    let active = true
    const monitor = monitorForegroundConnectivity((value) => {
      if (!active) return
      if (value.phase === 'session-required' || value.phase === 'stopped') terminal.current = true
      // Keep the last result during routine probes so unchanged health is not
      // announced twice every minute; account revalidation is shown separately.
      setObserved((previous) => value.phase === 'checking' && previous?.generation === generation ? previous : { generation, value })
    })
    return () => { active = false; monitor.stop() }
  }, [gate.identity, gate.generation])

  if (!gate.identity) return null
  const phase = suspended ? 'stopped' : gate.checking || observed?.generation !== gate.generation ? 'checking' : observed.value.phase
  const Icon = phase === 'checking' ? LoaderCircle : phase === 'reachable' ? CircleCheck : phase === 'unavailable' ? WifiOff : ShieldAlert
  return <aside aria-label={text.label} className="flex min-w-0 flex-wrap items-center justify-between gap-3 border-b py-3">
    <div aria-live="polite" aria-atomic="true" className="min-w-0 space-y-1">
      <p className="flex items-center gap-2 text-sm font-medium"><Icon aria-hidden="true" className={`size-4 shrink-0 ${phase === 'checking' ? 'animate-spin motion-reduce:animate-none' : ''}`} />{text.phases[phase]}</p>
      <p className="max-w-prose text-sm leading-relaxed text-muted-foreground">{text.help}</p>
    </div>
    {(phase === 'session-required' || phase === 'stopped') && <Button asChild variant="outline" className="min-h-11 h-auto max-w-full whitespace-normal py-2"><Link to="/settings/account">{text.account}</Link></Button>}
  </aside>
}
