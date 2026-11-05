import { QueryClientProvider } from '@tanstack/react-query'
import { createMemoryRouter } from 'react-router'
import { RouterProvider } from 'react-router/dom'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeAll, beforeEach, expect, it, vi } from 'vitest'
import * as api from '@/lib/api/auth'
import { createQueryClient } from '@/lib/query'
import { messages } from '@/lib/messages'
import App from '@/App'

vi.mock('@/lib/api/client', async (original) => ({ ...await original<typeof import('@/lib/api/client')>(), fetchHealth: vi.fn().mockResolvedValue({ status: 'ok' }) }))
vi.mock('@/lib/api/auth', async (original) => ({
  ...await original<typeof import('@/lib/api/auth')>(), fetchAdmin: vi.fn(), fetchCSRF: vi.fn(),
  login: vi.fn(), setupAdmin: vi.fn(), changePassword: vi.fn(), logout: vi.fn(), reauthenticate: vi.fn(),
}))

const text = messages.auth
const initial = { id: 'admin-1', revision: 1, username: 'owner', must_change_password: true }
const secret = 'a private temporary password'
const replacement = 'a different private password'
let current: api.Admin | null
const disposers: (() => void)[] = []

// Real browser tests own cold lazy-chunk recovery; preload the actual module
// here so its compilation cannot consume the UI-state assertion deadline.
beforeAll(async () => { await import('./account-page') })

beforeEach(() => {
  vi.clearAllMocks()
  current = null
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: true })
  vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
  vi.mocked(api.fetchAdmin).mockImplementation(async () => { if (!current) throw new api.ApiError('AUTHENTICATION_REQUIRED', { status: 401 }); return current })
  vi.mocked(api.fetchCSRF).mockResolvedValue('A'.repeat(43))
  vi.mocked(api.setupAdmin).mockResolvedValue(initial)
  vi.mocked(api.login).mockImplementation(async (value) => { current = { ...initial, username: value.username }; return current })
  vi.mocked(api.changePassword).mockImplementation(async () => { current = null; return { ok: true, operation_id: 'password-event', revision: 2 } })
  vi.mocked(api.logout).mockImplementation(async () => { current = null; return { ok: true, operation_id: 'logout-event' } })
})
afterEach(() => { for (const dispose of disposers.splice(0)) dispose() })

function mount(path = '/login') {
  const client = createQueryClient()
  const router = createMemoryRouter([{ path: '*', element: <App /> }], { initialEntries: [path] })
  const view = render(<QueryClientProvider client={client}><RouterProvider router={router} /></QueryClientProvider>)
  disposers.push(() => { view.unmount(); router.dispose(); client.clear() })
  return { client, router, view }
}

function field(label: string) { return screen.getByLabelText(new RegExp(`^${label}`)) }
async function login(user: ReturnType<typeof userEvent.setup>) {
  await screen.findByLabelText(new RegExp(`^${text.username}`))
  await user.type(field(text.username), 'owner')
  await user.type(field(text.password), secret)
  await user.click(screen.getByRole('button', { name: text.login }))
  await screen.findByRole('heading', { name: text.passwordHeading })
}

it('provides real setup, fresh login, forced rotation, and authenticated account state', async () => {
  const user = userEvent.setup()
  const { client, router } = mount('/setup')
  await screen.findByLabelText(new RegExp(`^${text.bootstrap}`))
  await user.type(field(text.bootstrap), 'A'.repeat(43))
  await user.type(field(text.username), 'owner')
  await user.type(field(text.password), secret)
  await user.type(field(text.confirmation), secret)
  await user.click(screen.getByRole('button', { name: text.setup }))
  expect(await screen.findByText(text.setupDone)).toBeVisible()
  expect(api.login).not.toHaveBeenCalled()
  expect(client.getQueryData(['auth', 'me'])).toBeNull()
  expect(api.setupAdmin).toHaveBeenCalledWith({ bootstrap_token: 'A'.repeat(43), username: 'owner', password: secret }, expect.any(AbortSignal))
  await login(user)
  expect(screen.getByText(text.mandatory)).toBeVisible()
  expect(screen.getByText('owner', { exact: true })).toBeVisible()
  client.setQueryData(['items', 'old-session'], { private: true })
  await user.type(field(text.oldPassword), secret)
  await user.type(field(text.newPassword), replacement)
  await user.type(field(text.confirmation), replacement)
  await user.click(screen.getByRole('button', { name: text.changePassword }))
  expect(await screen.findByText(text.passwordDone)).toBeVisible()
  expect(router.state.location.pathname).toBe('/login')
  expect(api.changePassword).toHaveBeenCalledWith({ old_password: secret, new_password: replacement }, expect.objectContaining({ csrfToken: 'A'.repeat(43), signal: expect.any(AbortSignal), idempotencyKey: expect.any(String) }))
  expect(client.getQueryData(['items', 'old-session'])).toBeUndefined()
  expect(client.getMutationCache().getAll()).toHaveLength(0)
  expect(field(text.password)).toHaveValue('')
})

it('validates required fields, Unicode bounds, and matching confirmation before any write', async () => {
  const user = userEvent.setup()
  mount('/setup')
  await screen.findByLabelText(new RegExp(`^${text.bootstrap}`))
  await user.click(screen.getByRole('button', { name: text.setup }))
  expect(await screen.findByText(text.bootstrapBounds)).toBeVisible()
  expect(screen.getByText(text.usernameBounds)).toBeVisible()
  expect(field(text.bootstrap)).toHaveFocus()
  expect(api.setupAdmin).not.toHaveBeenCalled()
  await user.type(field(text.bootstrap), 'A'.repeat(43))
  await user.type(field(text.username), 'owner')
  fireEvent.change(field(text.password), { target: { value: '😀'.repeat(6) } })
  await user.type(field(text.confirmation), 'different')
  await user.click(screen.getByRole('button', { name: text.setup }))
  expect(await screen.findByText(text.confirmationError)).toBeVisible()
  expect(api.setupAdmin).not.toHaveBeenCalled()
})

it('supports password visibility without storing credentials and clears on backgrounding', async () => {
  const user = userEvent.setup()
  mount()
  await screen.findByLabelText(new RegExp(`^${text.password}`))
  const input = field(text.password)
  await user.type(input, secret)
  expect(input).toHaveAttribute('type', 'password')
  await user.click(screen.getByRole('button', { name: text.show + text.password }))
  expect(input).toHaveAttribute('type', 'text')
  expect(screen.getByRole('button', { name: text.hide + text.password })).toHaveAttribute('aria-pressed', 'true')
  vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden')
  fireEvent(document, new Event('visibilitychange'))
  expect(input).toHaveValue('')
  expect(input).toHaveAttribute('type', 'password')
  expect(api.login).not.toHaveBeenCalled()
  expect(localStorage.getItem('homebox.auth')).toBeNull()
})

it('blocks dirty SPA navigation and lets the user stay or explicitly clear and leave', async () => {
  const user = userEvent.setup()
  const { router } = mount()
  await screen.findByLabelText(new RegExp(`^${text.username}`))
  await user.type(field(text.username), 'owner')
  await user.click(screen.getByRole('link', { name: text.firstTime }))
  let dialog = await screen.findByRole('alertdialog')
  expect(within(dialog).getByText(text.leaveHelp)).toBeVisible()
  await user.click(within(dialog).getByRole('button', { name: text.stay }))
  expect(router.state.location.pathname).toBe('/login')
  expect(field(text.username)).toHaveValue('owner')
  await user.click(screen.getByRole('link', { name: text.firstTime }))
  dialog = await screen.findByRole('alertdialog')
  await user.click(within(dialog).getByRole('button', { name: text.leave }))
  await screen.findByLabelText(new RegExp(`^${text.bootstrap}`))
  expect(router.state.location.pathname).toBe('/setup')
  expect(field(text.username)).toHaveValue('')
})

it('warns before hard reload while dirty but does not intercept an untouched form', async () => {
  const user = userEvent.setup()
  mount()
  await screen.findByLabelText(new RegExp(`^${text.username}`))
  const clean = new Event('beforeunload', { cancelable: true })
  window.dispatchEvent(clean)
  expect(clean.defaultPrevented).toBe(false)
  await user.type(field(text.username), 'owner')
  const dirty = new Event('beforeunload', { cancelable: true })
  window.dispatchEvent(dirty)
  expect(dirty.defaultPrevented).toBe(true)
})

it('confirms logout, clears session caches, and preserves the theme preference', async () => {
  const user = userEvent.setup()
  current = { ...initial, must_change_password: false }
  const { client, router } = mount('/settings/account')
  await screen.findByRole('button', { name: text.logout })
  client.setQueryData(['stock', 'private'], { balance: 10 })
  await user.click(screen.getByRole('button', { name: messages.theme.label }))
  await user.click(screen.getByRole('menuitemradio', { name: messages.theme.dark }))
  const preference = localStorage.getItem('homebox.theme')
  await user.click(screen.getByRole('button', { name: text.logout }))
  let dialog = await screen.findByRole('alertdialog')
  await user.click(within(dialog).getByRole('button', { name: text.cancel }))
  expect(api.logout).not.toHaveBeenCalled()
  await user.click(screen.getByRole('button', { name: text.logout }))
  dialog = await screen.findByRole('alertdialog')
  await user.click(within(dialog).getByRole('button', { name: text.logout }))
  await waitFor(() => expect(api.logout).toHaveBeenCalledTimes(1))
  await waitFor(() => expect(router.state.location.pathname).toBe('/login'))
  expect(router.state.location.state).toEqual({ notice: 'logout' })
  expect(await screen.findByText(text.logoutDone)).toBeVisible()
  expect(router.state.location.pathname).toBe('/login')
  expect(api.logout).toHaveBeenCalledTimes(1)
  expect(client.getQueryData(['stock', 'private'])).toBeUndefined()
  expect(localStorage.getItem('homebox.theme')).toBe(preference)
})

it('does not queue a login offline or replay it on reconnect', async () => {
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: false })
  mount()
  expect(await screen.findByText(text.offline)).toBeVisible()
  expect(api.fetchAdmin).not.toHaveBeenCalled()
  expect(api.login).not.toHaveBeenCalled()
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: true })
  fireEvent(window, new Event('online'))
  await screen.findByLabelText(new RegExp(`^${text.username}`))
  expect(api.login).not.toHaveBeenCalled()
})

it('rejects duplicate submission, treats interruption as uncertain, and never replays', async () => {
  const user = userEvent.setup()
  let complete: ((value: api.Admin) => void) | undefined
  vi.mocked(api.login).mockImplementation(() => new Promise((resolve) => { complete = resolve }))
  const { router, client } = mount()
  await screen.findByLabelText(new RegExp(`^${text.username}`))
  await user.type(field(text.username), 'owner')
  await user.type(field(text.password), secret)
  await user.click(screen.getByRole('button', { name: text.login }))
  expect(screen.getByRole('button', { name: text.loginPending })).toBeDisabled()
  vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden')
  fireEvent(document, new Event('visibilitychange'))
  await act(async () => { complete?.(initial) })
  expect(await screen.findByText(text.uncertain)).toBeVisible()
  expect(router.state.location.pathname).toBe('/login')
  expect(api.login).toHaveBeenCalledTimes(1)
  expect(client.getMutationCache().getAll()).toHaveLength(0)
  expect(field(text.password)).toHaveValue('')
})

it('shows safe credential errors and clears submitted secrets', async () => {
  const user = userEvent.setup()
  vi.mocked(api.login).mockRejectedValue(new api.ApiError('AUTHENTICATION_REQUIRED', { status: 401 }))
  mount()
  await screen.findByLabelText(new RegExp(`^${text.username}`))
  await user.type(field(text.username), 'owner')
  await user.type(field(text.password), secret)
  await user.click(screen.getByRole('button', { name: text.login }))
  expect(await screen.findByText(text.credentials)).toBeVisible()
  await waitFor(() => expect(field(text.password)).toHaveValue(''))
  expect(screen.getByRole('alert')).not.toHaveTextContent(secret)
})

it('rechecks read-only session failures without inventing an authenticated account', async () => {
  const user = userEvent.setup()
  vi.mocked(api.fetchAdmin).mockRejectedValueOnce(new api.ApiError('SERVICE_UNAVAILABLE', { status: 503 }))
  mount('/settings/account')
  expect(await screen.findByText(text.busy)).toBeVisible()
  await user.click(screen.getByRole('button', { name: text.checkAgain }))
  expect(await screen.findByRole('heading', { name: text.loginHeading })).toBeVisible()
  expect(api.login).not.toHaveBeenCalled()
})
