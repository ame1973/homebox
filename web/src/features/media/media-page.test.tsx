import { QueryClientProvider } from '@tanstack/react-query'
import { createMemoryRouter } from 'react-router'
import { RouterProvider } from 'react-router/dom'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeAll, beforeEach, expect, it, vi } from 'vitest'
import App from '@/App'
import * as auth from '@/lib/api/auth'
import { ApiError } from '@/lib/api/client'
import { mediaOwnerRoute } from '@/lib/media-navigation'
import { messages } from '@/lib/messages'
import { createQueryClient } from '@/lib/query'
import { sendMediaCommand } from './commands'
import { readOwnerMedia, type OwnerMedia } from './model'

vi.mock('@/lib/api/auth', async (original) => ({ ...await original<typeof import('@/lib/api/auth')>(), fetchAdmin: vi.fn(), fetchCSRF: vi.fn() }))
vi.mock('./commands', async (original) => ({ ...await original<typeof import('./commands')>(), sendMediaCommand: vi.fn() }))
vi.mock('./model', async (original) => ({ ...await original<typeof import('./model')>(), readOwnerMedia: vi.fn() }))
const text = messages.media
const item = { id: 'owner/\0😀', revision: 4, name: 'Fixture owner', brand: null, model: '', category_id: null, description: '', notes: '', tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1, status: 'active' } as const
const image = { id: 'photo', revision: 7, sha256: 'a'.repeat(64), mime_type: 'image/heic', verification: 'unverified', width: 10, height: 20, bytes: 30, thumb_url: null, thumb_sha256: null } as const
const link = { id: 'first', revision: 2, media_id: image.id, owner_type: 'item', owner_id: item.id, is_cover: true, sort_order: -3, description: '\0Caption é\n', media: image } as const
const other = { ...link, id: 'second', revision: 5, is_cover: false, sort_order: 6 } as const
const initial: OwnerMedia = { owner: { kind: 'item', value: item }, links: [other, link] }
let current: OwnerMedia | null
const disposers: (() => void)[] = []
const path = mediaOwnerRoute({ kind: 'item', id: item.id })

beforeAll(async () => { await Promise.all([import('./media-page'), import('@/features/auth/account-page')]) })
beforeEach(() => {
  vi.clearAllMocks()
  current = structuredClone(initial)
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: true })
  vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
  vi.mocked(auth.fetchAdmin).mockReset().mockResolvedValue({ id: 'admin', username: 'owner', revision: 2, must_change_password: false })
  vi.mocked(auth.fetchCSRF).mockReset().mockResolvedValue('C'.repeat(43))
  vi.mocked(readOwnerMedia).mockReset().mockImplementation(async () => {
    if (!current) throw new ApiError('NOT_FOUND')
    return structuredClone(current)
  })
  vi.mocked(sendMediaCommand).mockReset().mockImplementation(async (command) => {
    if (command.kind !== 'unlink' || !current) throw new Error('Unexpected fixture command')
    const selected = command.source.links.find((value) => value.id === command.linkID)!
    current = { owner: { kind: 'item', value: { ...item, revision: item.revision + 1 } }, links: current.links.filter((value) => value.id !== selected.id) }
    return { ok: true, operation_id: 'event', revision: selected.revision + 1 }
  })
})
afterEach(() => { for (const dispose of disposers.splice(0)) dispose() })
function mount(url = path, state?: unknown) {
  const client = createQueryClient(), [pathname, search = ''] = url.split('?')
  const router = createMemoryRouter([{ path: '*', element: <App /> }], { initialEntries: [{ pathname, search: search ? `?${search}` : '', state }] })
  const view = render(<QueryClientProvider client={client}><RouterProvider router={router} /></QueryClientProvider>)
  disposers.push(() => { view.unmount(); router.dispose(); client.clear() })
  return { client, router }
}
async function ready() {
  await screen.findByRole('heading', { name: text.title, level: 1 })
  await screen.findByText('Fixture owner', { exact: true })
}
const removeName = (id: string) => `${text.remove} ${JSON.stringify(id)}`

it('shows the actual complete owner collection in sort order without pretending to display image bytes', async () => {
  mount(); await ready()
  expect(readOwnerMedia).toHaveBeenCalledWith({ kind: 'item', id: item.id }, expect.any(AbortSignal))
  const list = screen.getByRole('list', { name: text.list })
  const rows = within(list).getAllByRole('listitem')
  expect(rows).toHaveLength(2); expect(rows[0]).toHaveTextContent('first'); expect(rows[1]).toHaveTextContent('second')
  expect(within(rows[0]!).getByText(text.cover, { exact: true })).toBeVisible()
  expect(within(rows[0]!).getByText(text.unverified, { exact: true })).toBeVisible()
  expect(screen.getByText(text.storageUnavailable)).toBeVisible(); expect(screen.queryByRole('img')).not.toBeInTheDocument()
  expect(auth.fetchCSRF).not.toHaveBeenCalled(); expect(sendMediaCommand).not.toHaveBeenCalled()
})

it('never displays a thumbnail even when source metadata claims a verified image', async () => {
  const verified = { ...image, mime_type: 'image/png' as const, verification: 'verified' as const,
    thumb_url: '/api/v1/media/photo/thumbnail', thumb_sha256: 'b'.repeat(64) }
  current = { ...initial, links: [{ ...link, media: verified }] }
  mount(); await ready()
  expect(screen.getByText(text.verified, { exact: true })).toBeVisible()
  expect(screen.queryByRole('img')).not.toBeInTheDocument()
  expect(document.querySelector('img')).toBeNull()
})

it('reviews and unlinks one selected original link only after explicit confirmation', async () => {
  mount(); await ready()
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: removeName('first') }))
  const dialog = screen.getByRole('alertdialog')
  expect(within(dialog).getByRole('heading', { name: text.reviewTitle })).toHaveFocus()
  expect(dialog).toHaveTextContent(text.removeHelp); expect(dialog).toHaveTextContent('first')
  expect(sendMediaCommand).not.toHaveBeenCalled(); expect(auth.fetchCSRF).not.toHaveBeenCalled()
  await user.click(within(dialog).getByRole('button', { name: text.confirmRemove }))
  await screen.findByText(messages.mutations.done, { exact: true })
  await waitFor(() => expect(screen.queryByRole('button', { name: removeName('first') })).not.toBeInTheDocument())
  const command = vi.mocked(sendMediaCommand).mock.calls[0]?.[0]
  expect(command).toMatchObject({ kind: 'unlink', source: initial, linkID: 'first', key: expect.any(String) })
  expect(screen.getByRole('button', { name: removeName('second') })).toBeVisible()
  expect(sendMediaCommand).toHaveBeenCalledTimes(1)
})

it('requires a new review if the owner or selected link changes before confirmation', async () => {
  const work = mount(); await ready()
  const user = userEvent.setup(); await user.click(screen.getByRole('button', { name: removeName('first') }))
  current = { owner: { kind: 'item', value: { ...item, revision: 5 } }, links: [other, { ...link, revision: 3, description: 'Concurrent' }] }
  await act(async () => { await work.client.invalidateQueries({ queryKey: ['media'] }) })
  const dialog = screen.getByRole('alertdialog')
  await within(dialog).findByText(text.reviewStale)
  expect(within(dialog).getByRole('button', { name: text.confirmRemove })).toBeDisabled()
  expect(sendMediaCommand).not.toHaveBeenCalled()
  await user.click(within(dialog).getByRole('button', { name: messages.mutations.cancel }))
  await user.click(screen.getByRole('button', { name: removeName('first') }))
  expect(within(screen.getByRole('alertdialog')).getByRole('button', { name: text.confirmRemove })).toBeEnabled()
})

it('keeps a sent uncertain unlink after owner loss and retries only the identical original command', async () => {
  mount(); await ready()
  vi.mocked(sendMediaCommand).mockImplementationOnce(async () => { current = null; throw new ApiError('NETWORK_ERROR') })
    .mockResolvedValueOnce({ ok: true, operation_id: 'event', revision: 3 })
  const user = userEvent.setup(); await user.click(screen.getByRole('button', { name: removeName('first') }))
  await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: text.confirmRemove }))
  await screen.findByRole('heading', { name: messages.mutations.uncertain })
  const original = vi.mocked(sendMediaCommand).mock.calls[0]?.[0]
  await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: messages.mutations.cancel }))
  await screen.findByText(text.missing, { exact: true })
  await act(async () => { window.dispatchEvent(new Event('online')) })
  expect(sendMediaCommand).toHaveBeenCalledTimes(1)
  await user.click(screen.getByRole('button', { name: messages.mutations.openOriginal }))
  await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: messages.mutations.retryOriginal }))
  await screen.findByText(messages.mutations.done, { exact: true })
  expect(vi.mocked(sendMediaCommand).mock.calls[1]?.[0]).toBe(original)
})

it('shows a local missing owner as missing rather than an empty collection', async () => {
  current = null; mount()
  await screen.findByText(text.missing, { exact: true })
  expect(screen.queryByText(text.empty, { exact: true })).not.toBeInTheDocument()
})

it('reviews and unlinks a retained link with an exact query identity', async () => {
  const selected = { ...link, id: 'unsafe/link\0😀' }, source: OwnerMedia = { ...initial, links: [selected] }
  current = source; mount(); await ready()
  const query = new URLSearchParams({ owner_type: 'item', owner_id: item.id, link_id: selected.id })
  expect(screen.getByRole('link', { name: `${text.edit} ${JSON.stringify(selected.id)}` })).toHaveAttribute('href', `/photos/edit?${query}`)
  const user = userEvent.setup(), remove = screen.getByRole('button', { name: removeName(selected.id) })
  expect(remove).toBeEnabled(); await user.click(remove)
  const dialog = screen.getByRole('alertdialog')
  expect(dialog).toHaveTextContent(JSON.stringify(selected.id)); expect(sendMediaCommand).not.toHaveBeenCalled()
  await user.click(within(dialog).getByRole('button', { name: text.confirmRemove }))
  await screen.findByText(messages.mutations.done, { exact: true })
  expect(vi.mocked(sendMediaCommand).mock.calls[0]?.[0]).toMatchObject({ kind: 'unlink', source, linkID: selected.id, key: expect.any(String) })
})

it('returns reserved and path-unaddressable owners to a real list instead of an unsafe detail route', async () => {
  const location = { id: 'tree', revision: 6, parent_id: null, name: 'Reserved room', code: 'ROOM', kind: 'room', status: 'active', sort_order: 0, description: '' } as const
  current = { owner: { kind: 'location', value: location }, links: [] }
  mount(mediaOwnerRoute({ kind: 'location', id: location.id }))
  await screen.findByText(location.name, { exact: true })
  expect(screen.getByRole('link', { name: text.backLocations })).toHaveAttribute('href', '/locations')
})

it('retains only validated catalog navigation when the owner cannot be addressed as a detail path', async () => {
  mount(path, { catalogList: '/items?status=active', catalogCursors: ['cursor'], privateBody: 'never-forward' }); await ready()
  expect(screen.getByRole('link', { name: text.backItems })).toHaveAttribute('href', '/items?status=active')
})

it('refreshes the selected current source without sending a write', async () => {
  mount(); await ready()
  current = { ...initial, owner: { kind: 'item', value: { ...item, name: 'Current owner', revision: 5 } } }
  await userEvent.setup().click(screen.getByRole('button', { name: text.refresh }))
  await screen.findByText('Current owner', { exact: true })
  expect(sendMediaCommand).not.toHaveBeenCalled()
})

it.each([
  { kind: 'item', id: 'new', label: text.backItem, href: '/items/new/view' },
  { kind: 'unit', id: 'unit', label: text.backUnit, href: '/units/unit' },
  { kind: 'location', id: 'new', label: text.backLocation, href: '/locations/new/view' },
] as const)('preserves the safe $kind detail destination', async ({ kind, id, label, href }) => {
  const location = { id, revision: 3, parent_id: null, name: 'Room', code: 'R', kind: 'room', status: 'active', sort_order: 0, description: '' } as const
  const unit = { id, item_id: 'item', revision: 3, location_id: null, asset_code: 'AST', serial_number: null, notes: '', status: 'stored' } as const
  current = { owner: kind === 'item' ? { kind, value: { ...item, id } } : kind === 'unit' ? { kind, value: unit } : { kind, value: location }, links: [] }
  mount(mediaOwnerRoute({ kind, id }))
  expect(await screen.findByRole('link', { name: label })).toHaveAttribute('href', href)
})

it('rejects incomplete owner queries before requesting a collection', async () => {
  mount('/photos?owner_type=item')
  await screen.findByText(messages.locations.validation)
  expect(readOwnerMedia).not.toHaveBeenCalled()
})

it('withholds a failed read instead of calling it an empty owner collection', async () => {
  vi.mocked(readOwnerMedia).mockRejectedValueOnce(new ApiError('NETWORK_ERROR'))
  mount()
  await screen.findByText(messages.locations.network)
  expect(screen.queryByText(text.empty, { exact: true })).not.toBeInTheDocument()
})

it.each([false, true])('purges a sent command on session loss, mandatory=%s', async (mandatory) => {
  const work = mount(); await ready()
  vi.mocked(sendMediaCommand).mockRejectedValueOnce(new ApiError(mandatory ? 'PASSWORD_CHANGE_REQUIRED' : 'AUTHENTICATION_REQUIRED', { status: mandatory ? 403 : 401 }))
  if (mandatory) vi.mocked(auth.fetchAdmin).mockResolvedValue({ id: 'admin', username: 'owner', revision: 2, must_change_password: true })
  else vi.mocked(auth.fetchAdmin).mockRejectedValue(new ApiError('AUTHENTICATION_REQUIRED', { status: 401 }))
  const user = userEvent.setup(); await user.click(screen.getByRole('button', { name: removeName('first') }))
  await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: text.confirmRemove }))
  await waitFor(() => expect(work.router.state.location.pathname).toBe(mandatory ? '/settings/account' : '/login'))
  expect(work.router.state.location.state).toMatchObject({ returnTo: path })
  expect(work.client.getQueryData(['media', 'owner', 'item', item.id])).toBeUndefined()
})

it('reports invalid reviewed source instead of dispatching an unlink', async () => {
  current = { ...initial, links: [{ ...link, owner_id: 'different-owner' }] }
  mount(); await ready()
  await userEvent.setup().click(screen.getByRole('button', { name: removeName('first') }))
  await screen.findByText(messages.locations.validation)
  expect(sendMediaCommand).not.toHaveBeenCalled()
})

it('allows session purge and login navigation even with a reviewed operation open', async () => {
  const work = mount(path, { catalogList: '/items?status=active', catalogCursors: ['cursor'], privateBody: 'never-forward' }); await ready()
  await userEvent.setup().click(screen.getByRole('button', { name: removeName('first') }))
  vi.mocked(readOwnerMedia).mockRejectedValueOnce(new ApiError('AUTHENTICATION_REQUIRED', { status: 401 }))
  await act(async () => { await work.client.invalidateQueries({ queryKey: ['media'] }) })
  await screen.findByRole('heading', { name: messages.auth.loginHeading })
  expect(work.router.state.location.pathname).toBe('/login')
  expect(work.router.state.location.state).toMatchObject({ returnTo: path, catalogList: '/items?status=active', catalogCursors: ['cursor'] })
  expect(work.router.state.location.state).not.toHaveProperty('privateBody')
  expect(work.client.getQueryData(['media', 'owner', 'item', item.id])).toBeUndefined()
})
