import { QueryClientProvider } from '@tanstack/react-query'
import { createMemoryRouter } from 'react-router'
import { RouterProvider } from 'react-router/dom'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeAll, beforeEach, expect, it, vi } from 'vitest'
import App from '@/App'
import * as auth from '@/lib/api/auth'
import * as versionAPI from '@/lib/api/system-version'
import { ApiError } from '@/lib/api/client'
import { messages } from '@/lib/messages'
import { createQueryClient } from '@/lib/query'
import { notifyPageSuspended } from '@/lib/session-lifecycle'
import { quoteExactText } from '@/lib/text-literal'
import { version as clientVersion } from '../../../package.json'

vi.mock('@/lib/api/client', async (original) => ({ ...await original<typeof import('@/lib/api/client')>(), fetchHealth: vi.fn().mockResolvedValue({ status: 'ok' }) }))
vi.mock('@/lib/api/auth', async (original) => ({ ...await original<typeof import('@/lib/api/auth')>(), fetchAdmin: vi.fn(), fetchCSRF: vi.fn() }))
vi.mock('@/lib/api/system-version', async (original) => ({ ...await original<typeof import('@/lib/api/system-version')>(), fetchSystemVersion: vi.fn() }))

const text = messages.versionStatus
const owner: auth.Admin = { id: 'owner', username: 'owner', revision: 2, must_change_password: false }
const status: versionAPI.SystemVersion = {
  api_version: '1', projection_version: 1, offline_projection_version: 1, dataset_epoch: 'epoch-1',
  server_instance_id: 'a'.repeat(32), web_build_id: 'b'.repeat(24), web_version: '0.1.0-dev', schema_version: 28,
  min_supported_client: null, min_web_build_id: null, model_index_versions: null,
  supported_write_client_versions: ['0.1.0', clientVersion],
}
const disposers: (() => void)[] = []
let meta: HTMLMetaElement

beforeAll(async () => { await Promise.all([import('./version-status-page'), import('@/features/taxonomy/settings-page'), import('@/features/auth/account-page')]) })
beforeEach(() => {
  vi.clearAllMocks()
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: true })
  vi.mocked(auth.fetchAdmin).mockReset().mockResolvedValue(owner)
  vi.mocked(auth.fetchCSRF).mockReset()
  vi.mocked(versionAPI.fetchSystemVersion).mockReset().mockResolvedValue(status)
  meta = document.createElement('meta'); meta.name = 'homebox-shell-worker'; meta.content = `/pwa-worker-${'b'.repeat(24)}.js`; document.head.append(meta)
})
afterEach(() => { for (const dispose of disposers.splice(0)) dispose(); meta.remove() })

function mount(path = '/settings/version') {
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

it('waits for a fresh completed session and clearly distinguishes unknown model and minimum states', async () => {
  const pending = deferred<auth.Admin>()
  vi.mocked(auth.fetchAdmin).mockReturnValueOnce(pending.promise)
  mount()
  expect(await screen.findByText(text.checking)).toBeVisible()
  expect(versionAPI.fetchSystemVersion).not.toHaveBeenCalled()
  await act(async () => { pending.resolve(owner); await pending.promise })
  expect(await screen.findByText(text.writeAccepted)).toBeVisible()
  expect(screen.getByText(text.pageMatches)).toBeVisible()
  expect(screen.getByText(text.modelsUnknown)).toBeVisible()
  expect(screen.getAllByText(text.minimumUnknown)).toHaveLength(2)
  expect(screen.getByText(text.writeHelp)).toBeVisible()
  expect(screen.getByText(text.readOnlyHelp)).toBeVisible()
  expect(versionAPI.fetchSystemVersion).toHaveBeenCalledTimes(1)
  expect(auth.fetchCSRF).not.toHaveBeenCalled()
})

it('reports mismatched builds and unsupported write versions without reloading', async () => {
  vi.mocked(versionAPI.fetchSystemVersion).mockResolvedValueOnce({ ...status, web_build_id: 'c'.repeat(24), supported_write_client_versions: ['0.9.0'] })
  mount()
  expect(await screen.findByText(text.writeRejected)).toBeVisible()
  expect(screen.getByText(text.writeRejectedHelp)).toBeVisible()
  expect(screen.getByText(text.pageDifferent)).toBeVisible()
  expect(screen.getByText(quoteExactText('0.9.0'))).toBeVisible()
  expect(screen.getByText('c'.repeat(24))).toBeVisible()
})

it('does not claim a matching page when the document has no worker identity', async () => {
  meta.remove()
  mount()
  expect(await screen.findByText(text.pageUnknown)).toBeVisible()
  expect(screen.queryByText(text.pageMatches)).not.toBeInTheDocument()
})

it('shows explicitly empty and reported model inventories without normalizing identifiers', async () => {
  vi.mocked(versionAPI.fetchSystemVersion).mockResolvedValueOnce({ ...status, model_index_versions: [] })
    .mockResolvedValueOnce({ ...status, dataset_epoch: `epoch-${String.fromCodePoint(0x202e, 0)}`,
      model_index_versions: [`model-${String.fromCodePoint(0x200f)}`], min_supported_client: 'minimum-1', min_web_build_id: 'older-build' })
  mount()
  expect(await screen.findByText(text.modelsEmpty)).toBeVisible()
  await userEvent.setup().click(screen.getByRole('button', { name: text.refresh }))
  expect(await screen.findByText(quoteExactText(`model-${String.fromCodePoint(0x200f)}`))).toBeVisible()
  expect(screen.getByText(quoteExactText(`epoch-${String.fromCodePoint(0x202e, 0)}`))).toBeVisible()
  expect(screen.getByText(quoteExactText('minimum-1'))).toBeVisible()
  expect(screen.getByText(quoteExactText('older-build'))).toBeVisible()
  expect(screen.getByText(text.modelsHelp)).toBeVisible()
})

it('hides a stale snapshot offline or after a failed explicit refresh', async () => {
  mount()
  await screen.findByText(text.writeAccepted)
  vi.mocked(versionAPI.fetchSystemVersion).mockRejectedValueOnce(new ApiError('NETWORK_ERROR'))
  await userEvent.setup().click(screen.getByRole('button', { name: text.refresh }))
  expect(await screen.findByRole('alert')).toHaveTextContent(text.readError)
  expect(screen.queryByText(text.writeAccepted)).not.toBeInTheDocument()
  await userEvent.setup().click(screen.getByRole('button', { name: text.refresh }))
  expect(await screen.findByText(text.writeAccepted)).toBeVisible()
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: false })
  act(() => { window.dispatchEvent(new Event('offline')) })
  expect(await screen.findByText(text.offlineHelp)).toBeVisible()
  expect(screen.queryByText(text.writeAccepted)).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: text.refresh })).toBeDisabled()
})

it('clears a pending result on delivered session loss and discards its late reply', async () => {
  const late = deferred<versionAPI.SystemVersion>()
  vi.mocked(versionAPI.fetchSystemVersion).mockReturnValueOnce(late.promise)
  const work = mount()
  await waitFor(() => expect(versionAPI.fetchSystemVersion).toHaveBeenCalledTimes(1))
  const signal = vi.mocked(versionAPI.fetchSystemVersion).mock.calls[0]?.[0]
  act(() => { notifyPageSuspended() })
  expect(await screen.findByText(text.sessionCleared)).toBeVisible()
  expect(signal?.aborted).toBe(true)
  await act(async () => { late.resolve(status); await late.promise })
  expect(screen.queryByText(text.writeAccepted)).not.toBeInTheDocument()
  expect(work.client.getQueryData(['system', 'version'])).toBeUndefined()
})

it('does not refresh version data when session clearing interrupts revalidation', async () => {
  mount()
  await screen.findByText(text.writeAccepted)
  const pending = deferred<auth.Admin>()
  vi.mocked(auth.fetchAdmin).mockReturnValueOnce(pending.promise)
  await userEvent.setup().click(screen.getByRole('button', { name: text.refresh }))
  await waitFor(() => expect(auth.fetchAdmin).toHaveBeenCalledTimes(2))
  act(() => { notifyPageSuspended() })
  expect(await screen.findByText(text.sessionCleared)).toBeVisible()
  await act(async () => { pending.resolve(owner); await pending.promise })
  expect(versionAPI.fetchSystemVersion).toHaveBeenCalledTimes(1)
})

it.each([
  { failure: new ApiError('AUTHENTICATION_REQUIRED', { status: 401 }), target: '/login' },
  { failure: new ApiError('PASSWORD_CHANGE_REQUIRED', { status: 403 }), target: '/settings/account' },
])('purges the snapshot when version authorization changes', async ({ failure, target }) => {
  vi.mocked(versionAPI.fetchSystemVersion).mockRejectedValueOnce(failure)
  const work = mount()
  await waitFor(() => expect(work.router.state.location.pathname).toBe(target))
  expect(work.router.state.location.state?.returnTo).toBe('/settings/version')
  expect(work.client.getQueryData(['system', 'version'])).toBeUndefined()
})

it.each([
  { admin: null, target: '/login' },
  { admin: { ...owner, must_change_password: true }, target: '/settings/account' },
])('requires a completed session before reading version data', async ({ admin, target }) => {
  if (admin) vi.mocked(auth.fetchAdmin).mockResolvedValueOnce(admin)
  else vi.mocked(auth.fetchAdmin).mockRejectedValueOnce(new ApiError('AUTHENTICATION_REQUIRED', { status: 401 }))
  const work = mount()
  await waitFor(() => expect(work.router.state.location.pathname).toBe(target))
  expect(work.router.state.location.state?.returnTo).toBe('/settings/version')
  expect(versionAPI.fetchSystemVersion).not.toHaveBeenCalled()
})

it.each(['/settings/version?private=value', '/settings/version#private'])('rejects unexpected route data', async (path) => {
  mount(path)
  expect(await screen.findByRole('alert')).toHaveTextContent(text.invalidRoute)
  expect(versionAPI.fetchSystemVersion).not.toHaveBeenCalled()
})

it('redacts on pagehide and requires fresh authorization and explicit continuation', async () => {
  mount()
  await screen.findByText(text.writeAccepted)
  act(() => { window.dispatchEvent(new PageTransitionEvent('pagehide', { persisted: true })) })
  expect(await screen.findByRole('heading', { name: text.restoreTitle })).toBeVisible()
  expect(screen.queryByText(text.writeAccepted)).not.toBeInTheDocument()
  act(() => { window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true })) })
  const resume = await screen.findByRole('button', { name: messages.scan.restoreContinue })
  expect(auth.fetchAdmin).toHaveBeenCalledTimes(2)
  await userEvent.setup().click(resume)
  expect(await screen.findByText(text.writeAccepted)).toBeVisible()
  expect(versionAPI.fetchSystemVersion).toHaveBeenCalledTimes(2)
})

it('is reachable from authenticated settings', async () => {
  const work = mount('/settings')
  await screen.findByRole('heading', { name: messages.settings.appearance })
  await userEvent.setup().click(within(screen.getByRole('list')).getByRole('link', { name: new RegExp(messages.settings.version) }))
  expect(await screen.findByText(text.writeAccepted)).toBeVisible()
  expect(work.router.state.location.pathname).toBe('/settings/version')
})
