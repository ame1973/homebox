import { QueryClientProvider } from '@tanstack/react-query'
import { createMemoryRouter } from 'react-router'
import { RouterProvider } from 'react-router/dom'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeAll, beforeEach, expect, it, vi } from 'vitest'
import App from '@/App'
import * as auth from '@/lib/api/auth'
import * as storageAPI from '@/lib/api/system-storage'
import { ApiError } from '@/lib/api/client'
import { messages } from '@/lib/messages'
import { createQueryClient } from '@/lib/query'
import { notifyPageSuspended } from '@/lib/session-lifecycle'

vi.mock('@/lib/api/client', async (original) => ({ ...await original<typeof import('@/lib/api/client')>(), fetchHealth: vi.fn().mockResolvedValue({ status: 'ok' }) }))
vi.mock('@/lib/api/auth', async (original) => ({ ...await original<typeof import('@/lib/api/auth')>(), fetchAdmin: vi.fn(), fetchCSRF: vi.fn() }))
vi.mock('@/lib/api/system-storage', async (original) => ({ ...await original<typeof import('@/lib/api/system-storage')>(), fetchSystemStorage: vi.fn() }))

const text = messages.serverStorage
const owner: auth.Admin = { id: 'owner', username: 'owner', revision: 2, must_change_password: false }
const status: storageAPI.SystemStorage = { database_bytes: 2048, media_bytes: null, free_bytes: 8192, backup_bytes: null,
  database_filesystem_total_bytes: 16384, measured_at: '2026-11-27T12:00:00Z' }
const disposers: (() => void)[] = []

beforeAll(async () => { await Promise.all([import('./server-storage-page'), import('@/features/taxonomy/settings-page'), import('@/features/auth/account-page')]) })
beforeEach(() => {
  vi.clearAllMocks()
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: true })
  vi.mocked(auth.fetchAdmin).mockReset().mockResolvedValue(owner)
  vi.mocked(auth.fetchCSRF).mockReset()
  vi.mocked(storageAPI.fetchSystemStorage).mockReset().mockResolvedValue(status)
})
afterEach(() => { for (const dispose of disposers.splice(0)) dispose() })

function mount(path = '/settings/storage') {
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

it('waits for a fresh session and does not claim unmeasured scopes are empty', async () => {
  const pending = deferred<auth.Admin>()
  vi.mocked(auth.fetchAdmin).mockReturnValueOnce(pending.promise)
  mount()
  expect(await screen.findByText(text.checking)).toBeVisible()
  expect(storageAPI.fetchSystemStorage).not.toHaveBeenCalled()
  await act(async () => { pending.resolve(owner); await pending.promise })
  expect(await screen.findByRole('heading', { name: text.title })).toBeVisible()
  expect(await screen.findAllByText(text.unknown)).toHaveLength(2)
  expect(screen.getByText(text.database).parentElement).toHaveTextContent('2 KiB （2,048 位元組）')
  expect(screen.getByText(text.free).parentElement).toHaveTextContent('8 KiB （8,192 位元組）')
  expect(screen.getByText(text.total).parentElement).toHaveTextContent('16 KiB （16,384 位元組）')
  expect(screen.getByText(status.measured_at)).toHaveAttribute('datetime', status.measured_at)
  expect(screen.getByText(text.filesystemHelp)).toBeVisible()
  expect(screen.getByText(text.otherHelp)).toBeVisible()
  expect(screen.getByText(text.readOnlyHelp)).toBeVisible()
  expect(storageAPI.fetchSystemStorage).toHaveBeenCalledTimes(1)
  expect(auth.fetchCSRF).not.toHaveBeenCalled()
})

it('distinguishes explicit measured zero from unknown and keeps full safe counts', async () => {
  vi.mocked(storageAPI.fetchSystemStorage).mockResolvedValueOnce({ ...status, media_bytes: 0, backup_bytes: Number.MAX_SAFE_INTEGER })
  mount()
  await screen.findByText(text.filesystemHelp)
  expect(screen.queryByText(text.unknown)).not.toBeInTheDocument()
  expect(screen.getByText(text.media).parentElement).toHaveTextContent('0 B （0 位元組）')
  expect(screen.getByText(text.backup).parentElement).toHaveTextContent('9,007,199,254,740,991 位元組')
})

it('retains focus during an explicit refresh and ignores duplicate actions', async () => {
  mount()
  await screen.findByText(text.filesystemHelp)
  const pending = deferred<storageAPI.SystemStorage>()
  vi.mocked(storageAPI.fetchSystemStorage).mockReturnValueOnce(pending.promise)
  const user = userEvent.setup(), refresh = screen.getByRole('button', { name: text.refresh })
  await user.click(refresh)
  await waitFor(() => expect(storageAPI.fetchSystemStorage).toHaveBeenCalledTimes(2))
  expect(refresh).toHaveFocus()
  expect(refresh).toHaveAttribute('aria-disabled', 'true')
  expect(screen.queryByText(text.filesystemHelp)).not.toBeInTheDocument()
  await user.click(refresh)
  expect(storageAPI.fetchSystemStorage).toHaveBeenCalledTimes(2)
  await act(async () => { pending.resolve({ ...status, database_bytes: 4096 }); await pending.promise })
  expect(await screen.findByText(text.filesystemHelp)).toBeVisible()
  expect(screen.getByText(text.database).parentElement).toHaveTextContent('4 KiB （4,096 位元組）')
  expect(refresh).toHaveFocus()
})

it('hides stale values after a failed refresh and while offline', async () => {
  mount()
  await screen.findByText(text.filesystemHelp)
  vi.mocked(storageAPI.fetchSystemStorage).mockRejectedValueOnce(new ApiError('NETWORK_ERROR'))
  const user = userEvent.setup(), refresh = screen.getByRole('button', { name: text.refresh })
  await user.click(refresh)
  expect(await screen.findByRole('alert')).toHaveTextContent(text.readError)
  expect(screen.queryByText(text.filesystemHelp)).not.toBeInTheDocument()
  await user.click(refresh)
  expect(await screen.findByText(text.filesystemHelp)).toBeVisible()
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: false })
  act(() => { window.dispatchEvent(new Event('offline')) })
  expect(await screen.findByText(text.offlineHelp)).toBeVisible()
  expect(screen.queryByText(text.filesystemHelp)).not.toBeInTheDocument()
  expect(refresh).toHaveAttribute('aria-disabled', 'true')
  const calls = vi.mocked(storageAPI.fetchSystemStorage).mock.calls.length
  await user.click(refresh)
  expect(storageAPI.fetchSystemStorage).toHaveBeenCalledTimes(calls)
})

it('clears a pending result on delivered session loss and discards its late reply', async () => {
  const pending = deferred<storageAPI.SystemStorage>()
  vi.mocked(storageAPI.fetchSystemStorage).mockReturnValueOnce(pending.promise)
  const work = mount()
  await waitFor(() => expect(storageAPI.fetchSystemStorage).toHaveBeenCalledTimes(1))
  const signal = vi.mocked(storageAPI.fetchSystemStorage).mock.calls[0]?.[0]
  act(() => { notifyPageSuspended() })
  expect(await screen.findByText(text.sessionCleared)).toBeVisible()
  expect(signal?.aborted).toBe(true)
  await act(async () => { pending.resolve(status); await pending.promise })
  expect(screen.queryByText(text.filesystemHelp)).not.toBeInTheDocument()
  expect(work.client.getQueryData(['system', 'storage'])).toBeUndefined()
})

it('does not read storage if session clearing interrupts a refresh', async () => {
  mount()
  await screen.findByText(text.filesystemHelp)
  const pending = deferred<auth.Admin>()
  vi.mocked(auth.fetchAdmin).mockReturnValueOnce(pending.promise)
  await userEvent.setup().click(screen.getByRole('button', { name: text.refresh }))
  await waitFor(() => expect(auth.fetchAdmin).toHaveBeenCalledTimes(2))
  act(() => { notifyPageSuspended() })
  expect(await screen.findByText(text.sessionCleared)).toBeVisible()
  await act(async () => { pending.resolve(owner); await pending.promise })
  expect(storageAPI.fetchSystemStorage).toHaveBeenCalledTimes(1)
})

it.each([
  { failure: new ApiError('AUTHENTICATION_REQUIRED', { status: 401 }), target: '/login' },
  { failure: new ApiError('PASSWORD_CHANGE_REQUIRED', { status: 403 }), target: '/settings/account' },
])('purges storage data when authorization changes', async ({ failure, target }) => {
  vi.mocked(storageAPI.fetchSystemStorage).mockRejectedValueOnce(failure)
  const work = mount()
  await waitFor(() => expect(work.router.state.location.pathname).toBe(target))
  expect(work.router.state.location.state?.returnTo).toBe('/settings/storage')
  expect(work.client.getQueryData(['system', 'storage'])).toBeUndefined()
})

it.each([
  { admin: null, target: '/login' },
  { admin: { ...owner, must_change_password: true }, target: '/settings/account' },
])('requires a completed session before reading storage', async ({ admin, target }) => {
  if (admin) vi.mocked(auth.fetchAdmin).mockResolvedValueOnce(admin)
  else vi.mocked(auth.fetchAdmin).mockRejectedValueOnce(new ApiError('AUTHENTICATION_REQUIRED', { status: 401 }))
  const work = mount()
  await waitFor(() => expect(work.router.state.location.pathname).toBe(target))
  expect(work.router.state.location.state?.returnTo).toBe('/settings/storage')
  expect(storageAPI.fetchSystemStorage).not.toHaveBeenCalled()
})

it.each(['/settings/storage?private=value', '/settings/storage#private'])('rejects unexpected route data', async (path) => {
  mount(path)
  expect(await screen.findByRole('alert')).toHaveTextContent(text.invalidRoute)
  expect(storageAPI.fetchSystemStorage).not.toHaveBeenCalled()
})

it('redacts on pagehide and requires fresh authorization and explicit continuation', async () => {
  mount()
  await screen.findByText(text.filesystemHelp)
  act(() => { window.dispatchEvent(new PageTransitionEvent('pagehide', { persisted: true })) })
  expect(await screen.findByRole('heading', { name: text.restoreTitle })).toBeVisible()
  expect(screen.queryByText(text.filesystemHelp)).not.toBeInTheDocument()
  act(() => { window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true })) })
  const resume = await screen.findByRole('button', { name: messages.scan.restoreContinue })
  expect(auth.fetchAdmin).toHaveBeenCalledTimes(2)
  await userEvent.setup().click(resume)
  expect(await screen.findByText(text.filesystemHelp)).toBeVisible()
  expect(storageAPI.fetchSystemStorage).toHaveBeenCalledTimes(2)
})

it('is reachable from authenticated settings', async () => {
  const work = mount('/settings')
  await screen.findByRole('heading', { name: messages.settings.appearance })
  await userEvent.setup().click(within(screen.getByRole('list')).getByRole('link', { name: new RegExp(messages.settings.storage) }))
  expect(await screen.findByText(text.filesystemHelp)).toBeVisible()
  expect(work.router.state.location.pathname).toBe('/settings/storage')
})
