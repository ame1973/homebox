import { QueryClientProvider } from '@tanstack/react-query'
import { createMemoryRouter } from 'react-router'
import { RouterProvider } from 'react-router/dom'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeAll, beforeEach, expect, it, vi } from 'vitest'
import App from '@/App'
import * as auth from '@/lib/api/auth'
import * as sync from '@/lib/api/sync-status'
import { ApiError } from '@/lib/api/client'
import { messages } from '@/lib/messages'
import { createQueryClient } from '@/lib/query'
import { notifyPageSuspended } from '@/lib/session-lifecycle'
import { quoteExactText } from '@/lib/text-literal'

vi.mock('@/lib/api/client', async (original) => ({ ...await original<typeof import('@/lib/api/client')>(), fetchHealth: vi.fn().mockResolvedValue({ status: 'ok' }) }))
vi.mock('@/lib/api/auth', async (original) => ({ ...await original<typeof import('@/lib/api/auth')>(), fetchAdmin: vi.fn(), fetchCSRF: vi.fn(), login: vi.fn() }))
vi.mock('@/lib/api/sync-status', async (original) => ({ ...await original<typeof import('@/lib/api/sync-status')>(), fetchSyncStatus: vi.fn() }))

const text = messages.syncStatus
const owner: auth.Admin = { id: 'owner', username: 'owner', revision: 2, must_change_password: false }
const status: sync.SyncStatus = {
  dataset_epoch: 'epoch-1', server_instance_id: '0123456789abcdef0123456789abcdef', projection_version: 1,
  min_retained_seq: 0, high_watermark: 0, counts: {}, counts_scope: 'materialized_projections', last_change_at: null,
  retention_days: 30, bootstrap_ready: false, retention_enabled: false,
}
const disposers: (() => void)[] = []

beforeAll(async () => { await Promise.all([import('./sync-status-page'), import('@/features/auth/account-page')]) })
beforeEach(() => {
  vi.clearAllMocks()
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: true })
  vi.mocked(auth.fetchAdmin).mockReset().mockResolvedValue(owner)
  vi.mocked(auth.fetchCSRF).mockReset()
  vi.mocked(sync.fetchSyncStatus).mockReset().mockResolvedValue(status)
})
afterEach(() => { for (const dispose of disposers.splice(0)) dispose() })

function mount(path = '/settings/sync-status') {
  const client = createQueryClient()
  const router = createMemoryRouter([{ path: '*', element: <App /> }], { initialEntries: [path] })
  const view = render(<QueryClientProvider client={client}><RouterProvider router={router} /></QueryClientProvider>)
  disposers.push(() => { view.unmount(); router.dispose(); client.clear() })
  return { client, router }
}

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((done) => { resolve = done })
  return { promise, resolve }
}

it('waits for a fresh completed session and explains incomplete server diagnostics without claiming offline coverage', async () => {
  const pending = deferred<auth.Admin>()
  vi.mocked(auth.fetchAdmin).mockReturnValueOnce(pending.promise)
  mount()
  expect(await screen.findByText(text.checking)).toBeVisible()
  expect(sync.fetchSyncStatus).not.toHaveBeenCalled()
  await act(async () => { pending.resolve(owner); await pending.promise })
  expect(await screen.findByText(text.bootstrapUnavailable)).toBeVisible()
  expect(screen.getByText(text.bootstrapHelp)).toBeVisible()
  expect(screen.getByText(text.retentionDisabled)).toBeVisible()
  expect(screen.getByText(text.noCounts)).toBeVisible()
  expect(screen.getByText(text.noLastChange)).toBeVisible()
  expect(screen.getByText(text.lastChangeHelp)).toBeVisible()
  expect(screen.getByText(text.readOnlyHelp)).toBeVisible()
  expect(sync.fetchSyncStatus).toHaveBeenCalledTimes(1)
  expect(auth.fetchCSRF).not.toHaveBeenCalled()
})

it.each([
  { admin: null, target: '/login' },
  { admin: { ...owner, must_change_password: true }, target: '/settings/account' },
])('does not request diagnostics for an incomplete session', async ({ admin, target }) => {
  if (admin) vi.mocked(auth.fetchAdmin).mockResolvedValueOnce(admin)
  else vi.mocked(auth.fetchAdmin).mockRejectedValueOnce(new ApiError('AUTHENTICATION_REQUIRED', { status: 401 }))
  const work = mount()
  await waitFor(() => expect(work.router.state.location.pathname).toBe(target))
  expect(work.router.state.location.state?.returnTo).toBe('/settings/sync-status')
  expect(sync.fetchSyncStatus).not.toHaveBeenCalled()
})

it('shows exact retained sequence, time, model identifiers and sparse projection counts without treating them as totals', async () => {
  const epoch = `epoch-${String.fromCodePoint(0x202e, 0)}`, model = `model-${String.fromCodePoint(0x200f)}`
  const retained = '2026-11-18T04:05:06.123456789+08:00'
  vi.mocked(sync.fetchSyncStatus).mockResolvedValueOnce({ ...status, dataset_epoch: epoch,
    min_retained_seq: 23, high_watermark: 1000, last_change_at: retained,
    counts: { item: 12, inventory_event: 8 }, models: [model] })
  mount()
  expect(await screen.findByText(text.bootstrapUnavailable)).toBeVisible()
  expect(screen.getByText('1,000')).toBeVisible()
  expect(screen.getByText('23')).toBeVisible()
  expect(screen.getByText(retained)).toHaveAttribute('datetime', retained)
  expect(screen.getByText(quoteExactText(epoch))).toBeVisible()
  expect(screen.getByText(quoteExactText(model))).toBeVisible()
  expect(screen.getByText(text.modelsHelp)).toBeVisible()
  const counts = screen.getByRole('region', { name: text.counts })
  expect(within(counts).getByText(text.kinds.item)).toBeVisible()
  expect(within(counts).getByText('12')).toBeVisible()
  expect(within(counts).getByText(text.kinds.inventory_event)).toBeVisible()
  expect(within(counts).getByText('8')).toBeVisible()
  expect(within(counts).queryByText(text.kinds.unit)).not.toBeInTheDocument()
  expect(within(counts).getByText(text.countsHelp)).toBeVisible()
  expect(screen.getByText(text.watermarkHelp)).toBeVisible()
  expect(screen.getByText(text.floorHelp)).toBeVisible()
})

it('shows every supported projection kind with its independent materialized count', async () => {
  const counts = Object.fromEntries(sync.syncProjectionKinds.map((kind, index) => [kind, index + 1]))
  vi.mocked(sync.fetchSyncStatus).mockResolvedValueOnce({ ...status, counts })
  mount()
  const region = await screen.findByRole('region', { name: text.counts })
  for (const [index, kind] of sync.syncProjectionKinds.entries()) {
    expect(within(region).getByText(text.kinds[kind], { exact: true })).toBeVisible()
    expect(within(region).getByText(String(index + 1), { exact: true })).toBeVisible()
  }
})

it('does not equate a fully pruned retained log with a never-changed dataset', async () => {
  vi.mocked(sync.fetchSyncStatus).mockResolvedValueOnce({ ...status, min_retained_seq: 91, high_watermark: 91 })
  mount()
  expect(await screen.findByText(text.noLastChange)).toBeVisible()
  expect(screen.getAllByText('91')).toHaveLength(2)
  expect(screen.getByText(text.lastChangeHelp)).toBeVisible()
})

it('reports only the server claim when the optional readiness flags are enabled', async () => {
  vi.mocked(sync.fetchSyncStatus).mockResolvedValueOnce({ ...status, bootstrap_ready: true, retention_enabled: true })
  mount()
  expect(await screen.findByText(text.bootstrapReady)).toBeVisible()
  expect(screen.getByText(text.retentionEnabled)).toBeVisible()
  expect(screen.getByText(text.bootstrapHelp)).toBeVisible()
})

it('hides a previous snapshot while revalidating and after a failed refresh', async () => {
  const work = mount()
  await screen.findByText(text.bootstrapUnavailable)
  const pending = deferred<auth.Admin>()
  vi.mocked(auth.fetchAdmin).mockReturnValueOnce(pending.promise)
  act(() => { void work.client.invalidateQueries({ queryKey: ['auth', 'me'] }) })
  expect(await screen.findByText(text.checking)).toBeVisible()
  expect(screen.queryByText(text.bootstrapUnavailable)).not.toBeInTheDocument()
  await act(async () => { pending.resolve(owner); await pending.promise })
  expect(await screen.findByText(text.bootstrapUnavailable)).toBeVisible()
  vi.mocked(sync.fetchSyncStatus).mockRejectedValueOnce(new ApiError('NETWORK_ERROR'))
  await userEvent.setup().click(screen.getByRole('button', { name: text.refresh }))
  expect(await screen.findByRole('alert')).toHaveTextContent(text.readError)
  expect(screen.queryByText(text.bootstrapUnavailable)).not.toBeInTheDocument()
  await userEvent.setup().click(screen.getByRole('button', { name: text.refresh }))
  expect(await screen.findByText(text.bootstrapUnavailable)).toBeVisible()
})

it('withholds stale diagnostics offline and obtains a fresh snapshot on reconnect', async () => {
  mount()
  await screen.findByText(text.bootstrapUnavailable)
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: false })
  act(() => { window.dispatchEvent(new Event('offline')) })
  expect(await screen.findByText(text.offlineHelp)).toBeVisible()
  expect(screen.queryByText(text.bootstrapUnavailable)).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: text.refresh })).toBeDisabled()
  vi.mocked(sync.fetchSyncStatus).mockResolvedValue({ ...status, high_watermark: 32 })
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: true })
  act(() => { window.dispatchEvent(new Event('online')) })
  expect(await screen.findByText('32')).toBeVisible()
})

it('purges the diagnostic and discards a late read after a delivered session-clear signal', async () => {
  const late = deferred<sync.SyncStatus>()
  vi.mocked(sync.fetchSyncStatus).mockReturnValueOnce(late.promise)
  const work = mount()
  await waitFor(() => expect(sync.fetchSyncStatus).toHaveBeenCalledTimes(1))
  const signal = vi.mocked(sync.fetchSyncStatus).mock.calls[0]?.[0]
  act(() => { notifyPageSuspended() })
  expect(await screen.findByText(text.sessionCleared)).toBeVisible()
  expect(signal?.aborted).toBe(true)
  await act(async () => { late.resolve({ ...status, high_watermark: 42 }); await late.promise })
  expect(screen.queryByText('42')).not.toBeInTheDocument()
  expect(work.client.getQueryData(['system', 'sync-status'])).toBeUndefined()
})

it('does not read diagnostics when session clearing interrupts manual revalidation', async () => {
  mount()
  await screen.findByText(text.bootstrapUnavailable)
  const pending = deferred<auth.Admin>()
  vi.mocked(auth.fetchAdmin).mockReturnValueOnce(pending.promise)
  await userEvent.setup().click(screen.getByRole('button', { name: text.refresh }))
  await waitFor(() => expect(auth.fetchAdmin).toHaveBeenCalledTimes(2))
  act(() => { notifyPageSuspended() })
  expect(await screen.findByText(text.sessionCleared)).toBeVisible()
  await act(async () => { pending.resolve(owner); await pending.promise })
  expect(sync.fetchSyncStatus).toHaveBeenCalledTimes(1)
})

it.each([
  { failure: new ApiError('AUTHENTICATION_REQUIRED', { status: 401 }), path: '/login' },
  { failure: new ApiError('PASSWORD_CHANGE_REQUIRED', { status: 403 }), path: '/settings/account' },
])('purges and returns safely when authorization changes', async ({ failure, path }) => {
  vi.mocked(sync.fetchSyncStatus).mockRejectedValueOnce(failure)
  const work = mount()
  await waitFor(() => expect(work.router.state.location.pathname).toBe(path))
  expect(work.router.state.location.state?.returnTo).toBe('/settings/sync-status')
  expect(work.client.getQueryData(['system', 'sync-status'])).toBeUndefined()
})

it.each(['/settings/sync-status?epoch=private', '/settings/sync-status#private'])('rejects extra URL data without requesting diagnostics', async (path) => {
  mount(path)
  expect(await screen.findByRole('alert')).toHaveTextContent(text.invalidRoute)
  expect(sync.fetchSyncStatus).not.toHaveBeenCalled()
})

it('redacts before page suspension and requires fresh authorization and explicit continuation', async () => {
  mount()
  await screen.findByText(text.bootstrapUnavailable)
  act(() => { window.dispatchEvent(new PageTransitionEvent('pagehide', { persisted: true })) })
  expect(await screen.findByRole('heading', { name: text.restoreTitle })).toBeVisible()
  expect(screen.queryByText(text.bootstrapUnavailable)).not.toBeInTheDocument()
  act(() => { window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true })) })
  const resume = await screen.findByRole('button', { name: messages.scan.restoreContinue })
  expect(auth.fetchAdmin).toHaveBeenCalledTimes(2)
  await userEvent.setup().click(resume)
  expect(await screen.findByText(text.bootstrapUnavailable)).toBeVisible()
  expect(sync.fetchSyncStatus).toHaveBeenCalledTimes(2)
})

it('is reachable from authenticated settings', async () => {
  const work = mount('/settings')
  await screen.findByRole('heading', { name: messages.settings.appearance })
  await userEvent.setup().click(within(screen.getByRole('list')).getByRole('link', { name: new RegExp(messages.settings.sync) }))
  await screen.findByText(text.bootstrapUnavailable)
  expect(work.router.state.location.pathname).toBe('/settings/sync-status')
})
