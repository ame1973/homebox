import { useCallback, useEffect, useRef, useState } from 'react'
import { Button } from '@/components/ui/button'
import { mutationControl as control } from '@/components/mutation-review'
import { messages } from '@/lib/messages'
import { formatBytes } from '@/lib/format-bytes'

const text = messages.deviceStorage
const action = `${control} px-[10px]! aria-disabled:cursor-not-allowed aria-disabled:opacity-50`
type Observation = { usage: number | null; quota: number | null; persistent: boolean | null; canRequest: boolean }
const unavailable: Observation = { usage: null, quota: null, persistent: null, canRequest: false }

// Browser estimates are advisory and may be omitted or outside safe JS precision.
function bytes(value: unknown): number | null {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 ? value : null
}
function displayBytes(value: number | null): string {
  return value === null ? text.unknown : formatBytes(value)
}

// An estimate can stall independently of the app; a late read is never applied.
async function bounded<T>(read: () => Promise<T>): Promise<T | undefined> {
  let timer: ReturnType<typeof setTimeout> | undefined
  try {
    return await Promise.race([read(), new Promise<undefined>((resolve) => { timer = setTimeout(() => resolve(undefined), 8_000) })])
  } catch { return undefined } finally { if (timer !== undefined) clearTimeout(timer) }
}

async function observe(): Promise<Observation> {
  try {
    const manager = typeof navigator === 'undefined' ? undefined : navigator.storage
    if (!manager) return unavailable
    const canRequest = typeof manager.persist === 'function'
    const [estimate, persistent] = await Promise.all([
      typeof manager.estimate === 'function' ? bounded(() => manager.estimate()) : undefined,
      typeof manager.persisted === 'function' ? bounded(() => manager.persisted()) : undefined,
    ])
    return { usage: bytes(estimate?.usage), quota: bytes(estimate?.quota),
      persistent: typeof persistent === 'boolean' ? persistent : null, canRequest }
  } catch { return unavailable }
}

type State = { phase: 'reading' | 'requesting' | 'idle'; value: Observation; feedback: 'denied' | 'failed' | null }

export default function StorageStatus() {
  const [state, setState] = useState<State>({ phase: 'reading', value: unavailable, feedback: null })
  const mounted = useRef(false), busy = useRef(false), generation = useRef(0)
  const refresh = useCallback(async () => {
    if (busy.current) return
    busy.current = true
    const current = ++generation.current
    setState((previous) => ({ ...previous, phase: 'reading', feedback: null }))
    const value = await observe()
    if (!mounted.current || generation.current !== current) return
    busy.current = false
    setState({ phase: 'idle', value, feedback: null })
  }, [])
  useEffect(() => {
    mounted.current = true
    const invalidate = () => { mounted.current = false; busy.current = false; generation.current++ }
    void refresh()
    return () => { invalidate() }
  }, [refresh])

  async function request() {
    if (busy.current || !state.value.canRequest || state.value.persistent === true) return
    busy.current = true
    const current = ++generation.current
    setState((previous) => ({ ...previous, phase: 'requesting', feedback: null }))
    try {
      // Invoke directly in the click handler; do not request permission on load.
      const result = await navigator.storage.persist()
      if (typeof result !== 'boolean') throw new Error('Invalid persistence result')
      if (!mounted.current || generation.current !== current) return
      setState((previous) => ({ ...previous, phase: 'idle', value: { ...previous.value, persistent: result }, feedback: result ? null : 'denied' }))
    } catch {
      if (mounted.current && generation.current === current) setState((previous) => ({ ...previous, phase: 'idle', value: { ...previous.value, persistent: null }, feedback: 'failed' }))
    } finally { if (mounted.current && generation.current === current) busy.current = false }
  }

  const disabled = state.phase !== 'idle'
  return <section aria-labelledby="device-storage-title" className="space-y-4 rounded-lg border px-[12px] py-4 sm:p-5">
    <h2 id="device-storage-title" className="text-xl font-semibold">{text.title}</h2>
    <p className="text-base leading-relaxed">{text.intro}</p>
    {state.phase === 'reading' ? <p role="status">{text.loading}</p> : <dl className="space-y-3">
      <div><dt className="font-medium">{text.usage}</dt><dd className="tabular-nums">{displayBytes(state.value.usage)}</dd></div>
      <div><dt className="font-medium">{text.quota}</dt><dd className="tabular-nums">{displayBytes(state.value.quota)}</dd></div>
      <div><dt className="font-medium">{text.persistence}</dt><dd>{state.value.persistent === null ? text.unknown : state.value.persistent ? text.persistent : text.notPersistent}</dd></div>
    </dl>}
    <p className="text-base leading-relaxed">{text.notBackup}</p>
    <p className="text-base leading-relaxed text-muted-foreground">{text.requestHelp}</p>
    <div className="flex max-w-full flex-wrap gap-2">
      <Button type="button" variant="outline" className={action} aria-disabled={disabled} onClick={() => { void refresh() }}>{text.refresh}</Button>
      <Button type="button" variant="outline" className={action} aria-disabled={disabled || !state.value.canRequest || state.value.persistent === true} onClick={() => { void request() }}>{text.request}</Button>
    </div>
    {state.phase === 'requesting' && <p role="status">{text.waiting}</p>}
    {state.feedback && <p role="status">{state.feedback === 'denied' ? text.requestDenied : text.requestFailed}</p>}
  </section>
}
