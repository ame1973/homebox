import { QueryClientProvider } from '@tanstack/react-query'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { act, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeAll, beforeEach, expect, it, vi } from 'vitest'
import App from '@/App'
import * as auth from '@/lib/api/auth'
import { ApiError } from '@/lib/api/client'
import { mediaCaptureRoute } from '@/lib/media-navigation'
import { messages } from '@/lib/messages'
import { createQueryClient } from '@/lib/query'
import { notifySessionCleared } from '@/lib/session-lifecycle'
import { quoteExactText } from '@/lib/text-literal'

vi.mock('@/lib/api/auth', async (original) => ({ ...await original<typeof import('@/lib/api/auth')>(), fetchAdmin: vi.fn() }))
const admin: auth.Admin = { id: 'admin', username: 'owner', must_change_password: false, revision: 2 }
const disposers: (() => void)[] = []
const originalDevices = Object.getOwnPropertyDescriptor(navigator, 'mediaDevices')
const originalOnline = Object.getOwnPropertyDescriptor(navigator, 'onLine')
const acquire = vi.fn()
beforeAll(async () => { await Promise.all([import('./capture-page'), import('@/features/auth/account-page')]) })
beforeEach(() => {
  vi.mocked(auth.fetchAdmin).mockReset().mockResolvedValue(admin)
  acquire.mockReset()
  Object.defineProperty(navigator, 'mediaDevices', { configurable: true, value: { getUserMedia: acquire } })
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: true })
})
afterEach(() => {
  for (const dispose of disposers.splice(0)) dispose()
  if (originalDevices) Object.defineProperty(navigator, 'mediaDevices', originalDevices); else Reflect.deleteProperty(navigator, 'mediaDevices')
  if (originalOnline) Object.defineProperty(navigator, 'onLine', originalOnline); else Reflect.deleteProperty(navigator, 'onLine')
})
function mount(path = mediaCaptureRoute({ kind: 'item', id: 'owner' })) {
  const client = createQueryClient(), url = new URL(path, 'http://homebox.test')
  const router = createMemoryRouter([{ path: '*', element: <App /> }], { initialEntries: [{ pathname: url.pathname, search: url.search, hash: url.hash }] })
  const view = render(<QueryClientProvider client={client}><RouterProvider router={router} /></QueryClientProvider>)
  disposers.push(() => { view.unmount(); router.dispose(); client.clear() })
  return { client, router }
}

it('waits for a completed session before exposing a camera gesture and explains the unsaved state', async () => {
  let finish!: (value: auth.Admin) => void
  vi.mocked(auth.fetchAdmin).mockReturnValueOnce(new Promise((resolve) => { finish = resolve }))
  mount()
  expect(await screen.findByText(messages.capture.checking)).toBeVisible()
  expect(screen.queryByRole('button', { name: messages.scan.cameraStart })).not.toBeInTheDocument()
  expect(acquire).not.toHaveBeenCalled()
  act(() => { finish(admin) })
  expect(await screen.findByRole('button', { name: messages.scan.cameraStart })).toBeEnabled()
  expect(screen.getByText(messages.capture.unsavedHelp)).toBeVisible()
  expect(screen.getByText(messages.capture.ownerHelp)).toBeVisible()
  expect(acquire).not.toHaveBeenCalled()
  act(() => { notifySessionCleared() })
  expect(await screen.findByText(messages.capture.sessionCleared)).toBeVisible()
  expect(screen.queryByRole('button', { name: messages.scan.cameraStart })).not.toBeInTheDocument()
  expect(acquire).not.toHaveBeenCalled()
})

it('retains an exact owner only as a validated scope and never accepts draft data in the route', async () => {
  const id = 'item/\0😀', path = mediaCaptureRoute({ kind: 'item', id })
  mount(path)
  expect(await screen.findByText(quoteExactText(id))).toBeVisible()
  expect(screen.getByRole('link', { name: messages.capture.back })).toHaveAttribute('href', expect.stringContaining('owner_id='))
  expect(acquire).not.toHaveBeenCalled()
  for (const dispose of disposers.splice(0)) dispose()
  mount(`${path}&description=private`)
  expect(await screen.findByText(messages.capture.invalidRoute)).toBeVisible()
  expect(screen.queryByRole('button', { name: messages.scan.cameraStart })).not.toBeInTheDocument()
})

it.each([
  { error: new ApiError('AUTHENTICATION_REQUIRED', { status: 401 }), target: '/login', notice: 'expired' },
  { error: new ApiError('PASSWORD_CHANGE_REQUIRED', { status: 403 }), target: '/settings/account', notice: undefined },
])('returns to only the validated owner scope after session transition: $target', async ({ error, target, notice }) => {
  vi.mocked(auth.fetchAdmin).mockRejectedValue(error)
  const path = mediaCaptureRoute({ kind: 'location', id: 'room/\0😀' }), { router } = mount(path)
  await waitFor(() => expect(router.state.location.pathname).toBe(target))
  expect(router.state.location.state).toEqual({ ...(notice ? { notice } : {}), returnTo: path })
  expect(acquire).not.toHaveBeenCalled()
})
