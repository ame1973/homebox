import { QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import type { Admin } from '@/lib/api/auth'
import { createQueryClient } from '@/lib/query'
import { notifyPageSuspended, notifySessionCleared } from '@/lib/session-lifecycle'
import { monitorForegroundConnectivity, type ConnectivityObservation } from '@/lib/foreground-connectivity'
import { messages } from '@/lib/messages'
import { ConnectivityIndicator } from './connectivity-indicator'

vi.mock('@/lib/foreground-connectivity', () => ({ monitorForegroundConnectivity: vi.fn() }))
const monitor = vi.mocked(monitorForegroundConnectivity)
const owner: Admin = { id: 'owner', username: 'owner', revision: 2, must_change_password: false }
const text = messages.connectivityIndicator
const disposers: (() => void)[] = []
let reports: ((value: ConnectivityObservation) => void)[]
let stops: ReturnType<typeof vi.fn>[]

beforeEach(() => {
  vi.clearAllMocks()
  reports = []; stops = []
  monitor.mockImplementation((publish) => {
    const stop = vi.fn(), check = vi.fn().mockReturnValue(true)
    reports.push(publish); stops.push(stop); publish({ phase: 'checking' })
    return { stop, check }
  })
})
afterEach(() => { for (const dispose of disposers.splice(0)) dispose() })

function mount(preload = false) {
  const client = createQueryClient()
  if (preload) client.setQueryData(['auth', 'me'], owner)
  const view = render(<QueryClientProvider client={client}><MemoryRouter><ConnectivityIndicator /></MemoryRouter></QueryClientProvider>)
  disposers.push(() => { view.unmount(); client.clear() })
  return client
}
function complete(client: ReturnType<typeof createQueryClient>, value: Admin | null = owner) {
  act(() => { client.setQueryData(['auth', 'me'], value) })
}
function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (error: unknown) => void
  const promise = new Promise<T>((done, fail) => { resolve = done; reject = fail })
  return { promise, resolve, reject }
}

it('starts no probe from an absent or pre-mount cached session', async () => {
  const client = mount(true)
  expect(monitor).not.toHaveBeenCalled()
  expect(screen.queryByLabelText(text.label)).not.toBeInTheDocument()
  complete(client)
  await waitFor(() => expect(monitor).toHaveBeenCalledTimes(1))
  expect(screen.getByText(text.phases.checking)).toBeVisible()
})

it('shows bounded monitor phases without using them as offline-data or write authority', async () => {
  const client = mount()
  complete(client)
  await waitFor(() => expect(monitor).toHaveBeenCalledTimes(1))
  for (const phase of ['reachable', 'degraded', 'unavailable', 'rate-limited', 'access-blocked', 'paused'] as const) {
    act(() => { reports[0]!({ phase }) })
    expect(screen.getByText(text.phases[phase])).toBeVisible()
    expect(screen.getByText(text.help)).toBeVisible()
  }
})

it('keeps the last observation during routine checks to avoid repetitive announcements', async () => {
  const client = mount()
  complete(client)
  await waitFor(() => expect(monitor).toHaveBeenCalledTimes(1))
  act(() => { reports[0]!({ phase: 'reachable' }) })
  act(() => { reports[0]!({ phase: 'checking' }) })
  expect(screen.getByText(text.phases.reachable)).toBeVisible()
  act(() => { reports[0]!({ phase: 'unavailable' }) })
  expect(screen.getByText(text.phases.unavailable)).toBeVisible()
  act(() => { reports[0]!({ phase: 'checking' }) })
  expect(screen.getByText(text.phases.unavailable)).toBeVisible()
})

it('hides the old observation during revalidation and keeps one monitor for the same completed session', async () => {
  const client = mount()
  complete(client)
  await waitFor(() => expect(monitor).toHaveBeenCalledTimes(1))
  act(() => { reports[0]!({ phase: 'reachable' }) })
  const pending = deferred<Admin>()
  let query!: Promise<Admin>
  act(() => { query = client.fetchQuery({ queryKey: ['auth', 'me'], queryFn: () => pending.promise, staleTime: 0 }) })
  await screen.findByText(text.phases.checking)
  expect(screen.queryByText(text.phases.reachable)).not.toBeInTheDocument()
  await act(async () => { pending.resolve(owner); await query })
  expect(await screen.findByText(text.phases.reachable)).toBeVisible()
  expect(monitor).toHaveBeenCalledTimes(1)
  expect(stops[0]).not.toHaveBeenCalled()
})

it('stops and withholds the old observation when a current session check fails', async () => {
  const client = mount()
  complete(client)
  await waitFor(() => expect(monitor).toHaveBeenCalledTimes(1))
  act(() => { reports[0]!({ phase: 'reachable' }) })
  const pending = deferred<Admin>()
  let query!: Promise<Admin>
  act(() => { query = client.fetchQuery({ queryKey: ['auth', 'me'], queryFn: () => pending.promise, staleTime: 0 }) })
  void query.catch(() => {})
  await act(async () => { pending.reject(new Error('private network detail')); await query.catch(() => {}) })
  await waitFor(() => expect(stops[0]).toHaveBeenCalledTimes(1))
  expect(screen.queryByText(text.phases.reachable)).not.toBeInTheDocument()
  expect(screen.queryByText(/private network detail/)).not.toBeInTheDocument()
})

it('cancels an old auth read on session clear and ignores its late success', async () => {
  const client = mount()
  complete(client)
  await waitFor(() => expect(monitor).toHaveBeenCalledTimes(1))
  const pending = deferred<Admin>()
  let signal: AbortSignal | undefined
  let query!: Promise<Admin>
  act(() => { query = client.fetchQuery({ queryKey: ['auth', 'me'], queryFn: (context) => { signal = context.signal; return pending.promise }, staleTime: 0 }) })
  void query.catch(() => {})
  act(() => { notifyPageSuspended() })
  await waitFor(() => expect(signal?.aborted).toBe(true))
  expect(stops[0]).toHaveBeenCalledTimes(1)
  await act(async () => { pending.resolve(owner); await query.catch(() => {}) })
  expect(monitor).toHaveBeenCalledTimes(1)
  expect(screen.queryByLabelText(text.label)).not.toBeInTheDocument()
  complete(client)
  await waitFor(() => expect(monitor).toHaveBeenCalledTimes(2))
  act(() => { reports[0]!({ phase: 'reachable' }); reports[0]!({ phase: 'session-required' }) })
  expect(screen.queryByText(text.phases.reachable)).not.toBeInTheDocument()
  expect(screen.queryByText(text.phases['session-required'])).not.toBeInTheDocument()
  expect(screen.getByText(text.phases.checking)).toBeVisible()
  complete(client)
  expect(monitor).toHaveBeenCalledTimes(2)
})

it('keeps a newly completed session after its own delayed broadcast', async () => {
  class LoopbackChannel extends EventTarget {
    static instances: LoopbackChannel[] = []
    closed = false
    constructor(_name: string) { super(); LoopbackChannel.instances.push(this) }
    postMessage(data: unknown) {
      for (const target of LoopbackChannel.instances) {
        if (target !== this && !target.closed) queueMicrotask(() => { if (!target.closed) target.dispatchEvent(new MessageEvent('message', { data })) })
      }
    }
    close() { this.closed = true }
  }
  vi.stubGlobal('BroadcastChannel', LoopbackChannel)
  const client = mount()
  complete(client)
  await waitFor(() => expect(monitor).toHaveBeenCalledTimes(1))
  await act(async () => {
    notifySessionCleared()
    client.setQueryData(['auth', 'me'], { ...owner, revision: 3 })
    await Promise.resolve()
  })
  await waitFor(() => expect(monitor).toHaveBeenCalledTimes(2))
  expect(screen.getByText(text.phases.checking)).toBeVisible()
  expect(stops[0]).toHaveBeenCalledOnce()
  const foreign = new LoopbackChannel('homebox:session-cleared')
  await act(async () => { foreign.postMessage('cleared'); await Promise.resolve() })
  await waitFor(() => expect(screen.queryByLabelText(text.label)).not.toBeInTheDocument())
  foreign.close()
})

it('redacts the last observation synchronously before pagehide until a fresh session check', async () => {
  const client = mount()
  complete(client)
  await waitFor(() => expect(monitor).toHaveBeenCalledTimes(1))
  act(() => { reports[0]!({ phase: 'reachable' }) })
  const pending = deferred<Admin>()
  let signal: AbortSignal | undefined
  let query!: Promise<Admin>
  act(() => { query = client.fetchQuery({ queryKey: ['auth', 'me'], queryFn: (context) => { signal = context.signal; return pending.promise }, staleTime: 0 }) })
  void query.catch(() => {})
  act(() => { window.dispatchEvent(new Event('pagehide')) })
  expect(signal?.aborted).toBe(true)
  expect(screen.getByText(text.phases.stopped)).toBeVisible()
  act(() => { reports[0]!({ phase: 'reachable' }) })
  expect(screen.getByText(text.phases.stopped)).toBeVisible()
  await act(async () => { pending.resolve(owner); await query.catch(() => {}) })
  expect(monitor).toHaveBeenCalledTimes(1)
  complete(client)
  await waitFor(() => expect(monitor).toHaveBeenCalledTimes(2))
  expect(stops[0]).toHaveBeenCalledTimes(1)
  expect(screen.getByText(text.phases.checking)).toBeVisible()
})

it.each(['session-required', 'stopped'] as const)('requires a fresh completed check after %s', async (phase) => {
  const client = mount()
  complete(client)
  await waitFor(() => expect(monitor).toHaveBeenCalledTimes(1))
  act(() => { reports[0]!({ phase }) })
  expect(screen.getByText(text.phases[phase])).toBeVisible()
  expect(screen.getByRole('link', { name: text.account })).toHaveAttribute('href', '/settings/account')
  complete(client)
  await waitFor(() => expect(monitor).toHaveBeenCalledTimes(2))
  expect(stops[0]).toHaveBeenCalledTimes(1)
})

it('does not monitor incomplete, missing or malformed cached administrator data', async () => {
  const client = mount()
  complete(client, { ...owner, must_change_password: true })
  complete(client, null)
  act(() => { client.setQueryData(['auth', 'me'], { must_change_password: false, id: 'broken' }) })
  expect(monitor).not.toHaveBeenCalled()
  expect(screen.queryByLabelText(text.label)).not.toBeInTheDocument()
})

it('stops the previous monitor before observing another administrator identity', async () => {
  const client = mount()
  complete(client)
  await waitFor(() => expect(monitor).toHaveBeenCalledTimes(1))
  complete(client, { ...owner, revision: 3 })
  await waitFor(() => expect(monitor).toHaveBeenCalledTimes(2))
  expect(stops[0]).toHaveBeenCalledTimes(1)
})
