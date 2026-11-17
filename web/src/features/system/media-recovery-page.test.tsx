import { QueryClientProvider } from '@tanstack/react-query'
import { createMemoryRouter } from 'react-router'
import { RouterProvider } from 'react-router/dom'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import App from '@/App'
import * as auth from '@/lib/api/auth'
import * as recovery from '@/lib/api/media-recovery'
import { ApiError } from '@/lib/api/client'
import { messages } from '@/lib/messages'
import { createQueryClient } from '@/lib/query'
import { notifyPageSuspended } from '@/lib/session-lifecycle'

vi.mock('@/lib/api/client', async (original) => ({ ...await original<typeof import('@/lib/api/client')>(), fetchHealth: vi.fn().mockResolvedValue({ status: 'ok' }) }))
vi.mock('@/lib/api/auth', async (original) => ({ ...await original<typeof import('@/lib/api/auth')>(), fetchAdmin: vi.fn(), fetchCSRF: vi.fn(), login: vi.fn() }))
vi.mock('@/lib/api/media-recovery', async (original) => ({ ...await original<typeof import('@/lib/api/media-recovery')>(), fetchMediaRecoveryStatus: vi.fn() }))
const text = messages.mediaRecovery
const owner: auth.Admin = { id: 'owner', username: 'owner', revision: 2, must_change_password: false }
const idle: recovery.MediaRecoveryStatus = { phase: 'idle', completed: 0, saturated: false, failure: '' }
const disposers: (() => void)[] = []

beforeAll(async () => { await Promise.all([import('./media-recovery-page'), import('@/features/auth/account-page')]) })
beforeEach(() => {
  vi.clearAllMocks()
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: true })
  vi.mocked(auth.fetchAdmin).mockReset().mockResolvedValue(owner)
  vi.mocked(auth.fetchCSRF).mockReset()
  vi.mocked(recovery.fetchMediaRecoveryStatus).mockReset().mockResolvedValue(idle)
})
afterEach(() => { for (const dispose of disposers.splice(0)) dispose() })

function mount(path = '/settings/media-recovery') {
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

it('does not read private status until a fresh completed session has been checked', async () => {
  const pending = deferred<auth.Admin>()
  vi.mocked(auth.fetchAdmin).mockReturnValueOnce(pending.promise)
  mount()
  expect(await screen.findByText(text.checking)).toBeVisible()
  expect(recovery.fetchMediaRecoveryStatus).not.toHaveBeenCalled()
  await act(async () => { pending.resolve(owner); await pending.promise })
  expect(await screen.findByRole('heading', { name: text.phases.idle })).toBeVisible()
  expect(recovery.fetchMediaRecoveryStatus).toHaveBeenCalledTimes(1)
  expect(auth.fetchCSRF).not.toHaveBeenCalled()
  expect(screen.getByText(text.readOnlyHelp)).toBeVisible()
})

it.each([
  { admin: null, target: '/login' },
  { admin: { ...owner, must_change_password: true }, target: '/settings/account' },
])('never reads status before an incomplete session is resolved', async ({ admin, target }) => {
  if (admin) vi.mocked(auth.fetchAdmin).mockResolvedValueOnce(admin)
  else vi.mocked(auth.fetchAdmin).mockRejectedValueOnce(new ApiError('AUTHENTICATION_REQUIRED', { status: 401 }))
  const work = mount()
  await waitFor(() => expect(work.router.state.location.pathname).toBe(target))
  expect(work.router.state.location.state?.returnTo).toBe('/settings/media-recovery')
  expect(recovery.fetchMediaRecoveryStatus).not.toHaveBeenCalled()
})

it('hides a snapshot while the completed session is revalidated or a recheck fails', async () => {
  const work = mount()
  await screen.findByRole('heading', { name: text.phases.idle })
  const pending = deferred<auth.Admin>()
  vi.mocked(auth.fetchAdmin).mockReturnValueOnce(pending.promise)
  act(() => { void work.client.invalidateQueries({ queryKey: ['auth', 'me'] }) })
  expect(await screen.findByText(text.checking)).toBeVisible()
  expect(screen.queryByRole('heading', { name: text.phases.idle })).not.toBeInTheDocument()
  await act(async () => { pending.resolve(owner); await pending.promise })
  expect(await screen.findByRole('heading', { name: text.phases.idle })).toBeVisible()
  expect(recovery.fetchMediaRecoveryStatus).toHaveBeenCalledTimes(1)
  vi.mocked(auth.fetchAdmin).mockRejectedValueOnce(new ApiError('NETWORK_ERROR'))
  act(() => { void work.client.invalidateQueries({ queryKey: ['auth', 'me'] }) })
  expect(await screen.findByRole('alert')).toHaveTextContent(text.readError)
  expect(screen.queryByRole('heading', { name: text.phases.idle })).not.toBeInTheDocument()
})

describe('bounded coordinator snapshots', () => {
  it.each(['queued', 'running', 'idle', 'stopped'] as const)('explains the %s state without inventing file health', async (phase) => {
    vi.mocked(recovery.fetchMediaRecoveryStatus).mockResolvedValueOnce({ ...idle, phase, completed: 12 })
    mount()
    expect(await screen.findByRole('heading', { name: text.phases[phase] })).toBeVisible()
    expect(screen.getByText(text.phaseHelp[phase])).toBeVisible()
    expect(screen.getByText('12')).toBeVisible()
    expect(screen.getByText(text.countHelp)).toBeVisible()
    expect(screen.getByText(text.readOnlyHelp)).toBeVisible()
  })

  it.each(['staging-unproven', 'staging-limit', 'object-unproven', 'storage-unavailable', 'inconsistent-state', 'recovery-failed'] as const)(
    'explains blocked category %s without exposing files or offering deletion', async (failure) => {
      vi.mocked(recovery.fetchMediaRecoveryStatus).mockResolvedValueOnce({ ...idle, phase: 'blocked', failure, completed: 3 })
      mount()
      expect(await screen.findByRole('heading', { name: text.phases.blocked })).toBeVisible()
      expect(screen.getByText(text.failures[failure])).toBeVisible()
      expect(screen.getByText(text.blockedHelp)).toBeVisible()
      expect(screen.queryByRole('button', { name: /刪除|復原|重試清理/ })).not.toBeInTheDocument()
    },
  )

  it('distinguishes an unavailable service and a saturated service-lifetime count', async () => {
    vi.mocked(recovery.fetchMediaRecoveryStatus).mockResolvedValueOnce({ phase: 'unavailable', completed: 0, saturated: false, failure: 'storage-unavailable' })
      .mockResolvedValueOnce({ ...idle, completed: Number.MAX_SAFE_INTEGER, saturated: true })
    mount()
    expect(await screen.findByRole('heading', { name: text.phases.unavailable })).toBeVisible()
    expect(screen.getByText(text.unavailableHelp)).toBeVisible()
    await userEvent.setup().click(screen.getByRole('button', { name: text.refresh }))
    expect(await screen.findByRole('heading', { name: text.phases.idle })).toBeVisible()
    expect(screen.getByText(new RegExp(text.atLeast))).toHaveTextContent('9,007,199,254,740,991')
    expect(recovery.fetchMediaRecoveryStatus).toHaveBeenCalledTimes(2)
    expect(auth.fetchCSRF).not.toHaveBeenCalled()
  })
})

it('hides a previous snapshot on failed refresh and requires an explicit fresh read', async () => {
  vi.mocked(recovery.fetchMediaRecoveryStatus).mockResolvedValueOnce(idle)
    .mockRejectedValueOnce(new ApiError('NETWORK_ERROR'))
    .mockResolvedValueOnce({ ...idle, phase: 'blocked', failure: 'recovery-failed' })
  mount()
  await screen.findByRole('heading', { name: text.phases.idle })
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: text.refresh }))
  expect(await screen.findByRole('alert')).toHaveTextContent(text.readError)
  expect(screen.queryByRole('heading', { name: text.phases.idle })).not.toBeInTheDocument()
  expect(recovery.fetchMediaRecoveryStatus).toHaveBeenCalledTimes(2)
  await user.click(screen.getByRole('button', { name: text.refresh }))
  expect(await screen.findByRole('heading', { name: text.phases.blocked })).toBeVisible()
  expect(recovery.fetchMediaRecoveryStatus).toHaveBeenCalledTimes(3)
})

it('hides stale status while offline and reads current state again after reconnect', async () => {
  mount()
  await screen.findByRole('heading', { name: text.phases.idle })
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: false })
  act(() => { window.dispatchEvent(new Event('offline')) })
  expect(await screen.findByText(text.offlineHelp)).toBeVisible()
  expect(screen.queryByRole('heading', { name: text.phases.idle })).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: text.refresh })).toBeDisabled()
  vi.mocked(recovery.fetchMediaRecoveryStatus).mockResolvedValue({ ...idle, phase: 'blocked', failure: 'staging-unproven' })
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: true })
  act(() => { window.dispatchEvent(new Event('online')) })
  expect(await screen.findByRole('heading', { name: text.phases.blocked })).toBeVisible()
})

it('does not use a cached snapshot before a session check when mounted offline', async () => {
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: false })
  const work = mount()
  expect(await screen.findByText(text.offlineHelp)).toBeVisible()
  expect(auth.fetchAdmin).not.toHaveBeenCalled()
  expect(recovery.fetchMediaRecoveryStatus).not.toHaveBeenCalled()
  expect(work.client.getQueryData(['system', 'media-recovery'])).toBeUndefined()
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: true })
  act(() => { window.dispatchEvent(new Event('online')) })
  expect(await screen.findByRole('heading', { name: text.phases.idle })).toBeVisible()
})

it('purges the snapshot and invalidates a late read on a delivered session-clear signal', async () => {
  const late = deferred<recovery.MediaRecoveryStatus>()
  vi.mocked(recovery.fetchMediaRecoveryStatus).mockReturnValueOnce(late.promise)
  const work = mount()
  await waitFor(() => expect(recovery.fetchMediaRecoveryStatus).toHaveBeenCalledTimes(1))
  const signal = vi.mocked(recovery.fetchMediaRecoveryStatus).mock.calls[0]?.[0]
  act(() => { notifyPageSuspended() })
  expect(await screen.findByText(text.sessionCleared)).toBeVisible()
  expect(screen.getByRole('link', { name: messages.scan.signIn })).toHaveAttribute('href', '/login')
  expect(signal?.aborted).toBe(true)
  await act(async () => { late.resolve({ ...idle, phase: 'blocked', failure: 'staging-unproven' }); await late.promise })
  expect(screen.queryByRole('heading', { name: text.phases.blocked })).not.toBeInTheDocument()
  expect(work.client.getQueryData(['system', 'media-recovery'])).toBeUndefined()
})

it('does not start a status read when session clearing interrupts manual revalidation', async () => {
  mount()
  await screen.findByRole('heading', { name: text.phases.idle })
  const pending = deferred<auth.Admin>()
  vi.mocked(auth.fetchAdmin).mockReturnValueOnce(pending.promise)
  await userEvent.setup().click(screen.getByRole('button', { name: text.refresh }))
  await waitFor(() => expect(auth.fetchAdmin).toHaveBeenCalledTimes(2))
  act(() => { notifyPageSuspended() })
  expect(await screen.findByText(text.sessionCleared)).toBeVisible()
  await act(async () => { pending.resolve(owner); await pending.promise })
  expect(recovery.fetchMediaRecoveryStatus).toHaveBeenCalledTimes(1)
})

it.each([
  { failure: new ApiError('AUTHENTICATION_REQUIRED', { status: 401 }), path: '/login' },
  { failure: new ApiError('PASSWORD_CHANGE_REQUIRED', { status: 403 }), path: '/settings/account' },
])('returns safely to the status page when authorization changes', async ({ failure, path }) => {
  vi.mocked(recovery.fetchMediaRecoveryStatus).mockRejectedValueOnce(failure)
  const work = mount()
  await waitFor(() => expect(work.router.state.location.pathname).toBe(path))
  expect(work.router.state.location.state?.returnTo).toBe('/settings/media-recovery')
  expect(work.client.getQueryData(['system', 'media-recovery'])).toBeUndefined()
})

it('rejects extraneous return data without requesting recovery status', async () => {
  mount('/settings/media-recovery?failure=private')
  expect(await screen.findByRole('alert')).toHaveTextContent(text.invalidRoute)
  expect(recovery.fetchMediaRecoveryStatus).not.toHaveBeenCalled()
})

it('redacts status on page suspension and requires fresh authorization and explicit continuation', async () => {
  mount()
  await screen.findByRole('heading', { name: text.phases.idle })
  act(() => { window.dispatchEvent(new PageTransitionEvent('pagehide', { persisted: true })) })
  expect(await screen.findByRole('heading', { name: text.restoreTitle })).toBeVisible()
  expect(screen.queryByRole('heading', { name: text.phases.idle })).not.toBeInTheDocument()
  act(() => { window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true })) })
  const resume = await screen.findByRole('button', { name: messages.scan.restoreContinue })
  expect(auth.fetchAdmin).toHaveBeenCalledTimes(2)
  await userEvent.setup().click(resume)
  expect(await screen.findByRole('heading', { name: text.phases.idle })).toBeVisible()
  expect(recovery.fetchMediaRecoveryStatus).toHaveBeenCalledTimes(2)
})

it('is reachable from the authenticated settings overview', async () => {
  const work = mount('/settings')
  await screen.findByRole('heading', { name: messages.settings.appearance })
  await userEvent.setup().click(within(screen.getByRole('list')).getByRole('link', { name: new RegExp(messages.settings.recovery) }))
  await screen.findByRole('heading', { name: text.phases.idle })
  expect(work.router.state.location.pathname).toBe('/settings/media-recovery')
})
