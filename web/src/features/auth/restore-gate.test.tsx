import { QueryClientProvider } from '@tanstack/react-query'
import { createMemoryRouter, MemoryRouter, RouterProvider, useLocation } from 'react-router'
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeAll, beforeEach, expect, it, vi } from 'vitest'
import * as auth from '@/lib/api/auth'
import { ApiError } from '@/lib/api/client'
import { messages } from '@/lib/messages'
import { createQueryClient } from '@/lib/query'
import { RestoreGate } from './restore-gate'

vi.mock('@/lib/api/auth', async (original) => ({ ...await original<typeof import('@/lib/api/auth')>(), fetchAdmin: vi.fn() }))
const owner: auth.Admin = { id: 'owner', username: 'owner', revision: 1, must_change_password: false }
const disposers: (() => void)[] = []
beforeAll(async () => { await import('@/lib/settings-navigation') })
beforeEach(() => { vi.mocked(auth.fetchAdmin).mockReset().mockResolvedValue(owner) })
afterEach(() => { for (const dispose of disposers.splice(0)) dispose() })

function mount(restored = true, generation = 0, returnPath = '/items/new') {
  const client = createQueryClient(), onLeave = vi.fn()
  const element = (active: boolean, version: number) => <QueryClientProvider client={client}><MemoryRouter><RestoreGate restored={active} generation={version} returnPath={returnPath} onLeave={onLeave} /></MemoryRouter></QueryClientProvider>
  const view = render(element(restored, generation))
  disposers.push(() => { view.unmount(); client.clear() })
  return { client, onLeave, rerender: (active: boolean, version: number) => view.rerender(element(active, version)) }
}

it('waits for pageshow and a fresh completed-session read before allowing continuation', async () => {
  let finish!: (value: auth.Admin) => void
  vi.mocked(auth.fetchAdmin).mockReturnValueOnce(new Promise((resolve) => { finish = resolve }))
  const view = mount(false)
  expect(screen.getByRole('heading', { name: messages.scan.restoreTitle })).toHaveFocus()
  expect(screen.getByText(messages.scan.restoreWaiting)).toBeVisible()
  expect(auth.fetchAdmin).not.toHaveBeenCalled()
  view.rerender(true, 0)
  await waitFor(() => expect(auth.fetchAdmin).toHaveBeenCalledOnce())
  expect(screen.getByText(messages.auth.checking)).toBeVisible()
  expect(screen.queryByRole('button', { name: messages.scan.restoreContinue })).not.toBeInTheDocument()
  act(() => { finish(owner) })
  await userEvent.setup().click(await screen.findByRole('button', { name: messages.scan.restoreContinue }))
  expect(view.onLeave).toHaveBeenCalledOnce()
})

it('requires an explicit retry after a failed session check', async () => {
  vi.mocked(auth.fetchAdmin).mockRejectedValueOnce(new ApiError('NETWORK_ERROR')).mockResolvedValueOnce(owner)
  mount()
  expect(await screen.findByText(messages.scan.restoreFailed)).toBeVisible()
  act(() => { window.dispatchEvent(new Event('online')); window.dispatchEvent(new Event('focus')) })
  expect(auth.fetchAdmin).toHaveBeenCalledOnce()
  await userEvent.setup().click(screen.getByRole('button', { name: messages.auth.checkAgain }))
  expect(await screen.findByText(messages.scan.restoreReady)).toBeVisible()
  expect(auth.fetchAdmin).toHaveBeenCalledTimes(2)
})

it.each([
  { admin: null, error: new ApiError('AUTHENTICATION_REQUIRED', { status: 401 }), label: messages.scan.signIn, target: '/login' },
  { admin: { ...owner, must_change_password: true }, error: undefined, label: messages.scan.restorePassword, target: '/settings/account' },
  { admin: null, error: new ApiError('PASSWORD_CHANGE_REQUIRED', { status: 403 }), label: messages.scan.restorePassword, target: '/settings/account' },
])('requires authentication or password rotation without forwarding an unsafe return path: $target', async ({ admin, error, label, target }) => {
  if (error) vi.mocked(auth.fetchAdmin).mockRejectedValueOnce(error)
  else vi.mocked(auth.fetchAdmin).mockResolvedValueOnce(admin!)
  const client = createQueryClient(), onLeave = vi.fn()
  function Destination() { const location = useLocation(); return <p>{JSON.stringify(location.state)}</p> }
  const router = createMemoryRouter([{ path: '/', element: <RestoreGate restored generation={0} returnPath="/scan?value=private-code" onLeave={onLeave} /> },
    { path: '/login', element: <Destination /> }, { path: '/settings/account', element: <Destination /> }])
  const view = render(<QueryClientProvider client={client}><RouterProvider router={router} /></QueryClientProvider>)
  disposers.push(() => { view.unmount(); router.dispose(); client.clear() })
  await userEvent.setup().click(await screen.findByRole('link', { name: label }))
  expect(router.state.location.pathname).toBe(target)
  expect(router.state.location.state).toEqual({ notice: 'scanHandoffLost', returnTo: '/items' })
  expect(JSON.stringify(router.state.location.state)).not.toContain('private-code')
  expect(onLeave).toHaveBeenCalledOnce()
})

it.each([
  { admin: null, error: new ApiError('AUTHENTICATION_REQUIRED', { status: 401 }), label: messages.scan.signIn, target: '/login', state: { notice: 'expired', returnTo: '/items/new' } },
  { admin: { ...owner, must_change_password: true }, error: undefined, label: messages.scan.restorePassword, target: '/settings/account', state: { returnTo: '/items/new' } },
  { admin: null, error: new ApiError('NETWORK_ERROR'), label: messages.scan.signIn, target: '/login', state: { returnTo: '/items' } },
])('uses Item-specific restoration guidance and safe navigation on $target', async ({ admin, error, label, target, state }) => {
  if (error) vi.mocked(auth.fetchAdmin).mockRejectedValueOnce(error)
  else vi.mocked(auth.fetchAdmin).mockResolvedValueOnce(admin!)
  const client = createQueryClient(), onLeave = vi.fn()
  function Destination() { const location = useLocation(); return <p>{JSON.stringify(location.state)}</p> }
  const router = createMemoryRouter([{ path: '/', element: <RestoreGate kind="item" restored generation={0} returnPath="/items/new" onLeave={onLeave} /> },
    { path: '/login', element: <Destination /> }, { path: '/settings/account', element: <Destination /> }])
  const view = render(<QueryClientProvider client={client}><RouterProvider router={router} /></QueryClientProvider>)
  disposers.push(() => { view.unmount(); router.dispose(); client.clear() })
  expect(screen.getByRole('heading', { name: messages.catalog.restorePageTitle })).toHaveFocus()
  expect(screen.getByText(messages.catalog.restorePageHelp)).toBeVisible()
  expect(screen.queryByText(messages.scan.restoreHelp)).not.toBeInTheDocument()
  await userEvent.setup().click(await screen.findByRole('link', { name: label }))
  expect(router.state.location.pathname).toBe(target)
  expect(router.state.location.state).toEqual(state)
  expect(onLeave).toHaveBeenCalledOnce()
})

it('ignores a stale session response after a newer generation replaces it', async () => {
  let reject!: (cause: ApiError) => void, oldSignal!: AbortSignal
  vi.mocked(auth.fetchAdmin).mockImplementationOnce((signal) => { oldSignal = signal; return new Promise((_resolve, fail) => { reject = fail }) }).mockResolvedValueOnce(owner)
  const view = mount(true, 1)
  await waitFor(() => expect(auth.fetchAdmin).toHaveBeenCalledOnce())
  view.rerender(true, 2)
  expect(await screen.findByText(messages.scan.restoreReady)).toBeVisible()
  expect(oldSignal.aborted).toBe(true)
  act(() => { reject(new ApiError('AUTHENTICATION_REQUIRED', { status: 401 })) })
  await waitFor(() => expect(screen.getByText(messages.scan.restoreReady)).toBeVisible())
  expect(screen.queryByRole('link', { name: messages.scan.signIn })).not.toBeInTheDocument()
})
