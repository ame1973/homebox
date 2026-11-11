import { QueryClientProvider } from '@tanstack/react-query'
import { createMemoryRouter } from 'react-router'
import { RouterProvider } from 'react-router/dom'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeAll, beforeEach, expect, it, vi } from 'vitest'
import App from '@/App'
import * as auth from '@/lib/api/auth'
import { ApiError } from '@/lib/api/client'
import { mediaEditRoute, mediaOwnerRoute } from '@/lib/media-navigation'
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
const other = { ...link, id: 'second', revision: 5, media_id: 'other', is_cover: true, sort_order: 6, media: { ...image, id: 'other' } } as const
const initial: OwnerMedia = { owner: { kind: 'item', value: item }, links: [other, link] }
const scope = { kind: 'item', id: item.id, linkID: link.id } as const
const path = mediaEditRoute(scope), list = mediaOwnerRoute({ kind: scope.kind, id: scope.id })
let current: OwnerMedia | null
const disposers: (() => void)[] = []

beforeAll(async () => { await Promise.all([import('./media-page'), import('@/features/auth/account-page')]) })
beforeEach(() => {
  vi.clearAllMocks(); current = structuredClone(initial)
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: true })
  vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
  vi.mocked(auth.fetchAdmin).mockReset().mockResolvedValue({ id: 'admin', username: 'owner', revision: 2, must_change_password: false })
  vi.mocked(auth.fetchCSRF).mockReset().mockResolvedValue('C'.repeat(43))
  vi.mocked(readOwnerMedia).mockReset().mockImplementation(async () => {
    if (!current) throw new ApiError('NOT_FOUND')
    return structuredClone(current)
  })
  vi.mocked(sendMediaCommand).mockReset().mockImplementation(async (command) => {
    if (command.kind !== 'patch-link' || !current) throw new Error('Unexpected fixture command')
    const selected = current.links.find((value) => value.id === command.linkID)!
    const changed = { ...selected, ...command.body, revision: selected.revision + 1, media: { ...selected.media, revision: selected.media.revision + 1 } }
    current = { owner: { kind: 'item', value: { ...item, revision: item.revision + 1 } }, links: current.links.map((value) => value.id === selected.id ? changed : value) }
    const { media: _media, ...reply } = changed
    return reply
  })
})
afterEach(() => { for (const dispose of disposers.splice(0)) dispose() })
function mount(url = path, state?: unknown, cached?: OwnerMedia) {
  const client = createQueryClient(), [pathname, search = ''] = url.split('?')
  if (cached) client.setQueryData(['media', 'owner', 'item', item.id], cached)
  const router = createMemoryRouter([{ path: '*', element: <App /> }], { initialEntries: [{ pathname, search: search ? `?${search}` : '', state }] })
  const view = render(<QueryClientProvider client={client}><RouterProvider router={router} /></QueryClientProvider>)
  disposers.push(() => { view.unmount(); router.dispose(); client.clear() })
  return { client, router }
}
async function ready() {
  await screen.findByRole('heading', { name: text.editTitle, level: 1 })
  await screen.findByRole('textbox', { name: text.order })
}

it('waits for the first current source rather than initializing from an older cached review', async () => {
  let release: ((value: OwnerMedia) => void) | undefined
  vi.mocked(readOwnerMedia).mockImplementationOnce(() => new Promise((resolve) => { release = resolve }))
  const cached: OwnerMedia = { ...initial, links: [other, { ...link, revision: 1, description: 'Old cached value' }] }
  mount(path, undefined, cached)
  await waitFor(() => expect(readOwnerMedia).toHaveBeenCalled())
  expect(screen.queryByRole('textbox', { name: text.description })).not.toBeInTheDocument()
  await act(async () => { release?.(structuredClone(initial)) })
  await ready()
  expect(screen.getByRole('textbox', { name: text.description })).toHaveValue(link.description)
})

it('opens the selected original link and confirms only changed fields with its independent proof', async () => {
  const work = mount(list); await screen.findByText('Fixture owner', { exact: true })
  await userEvent.setup().click(screen.getByRole('link', { name: `${text.edit} ${JSON.stringify(link.id)}` }))
  await ready()
  expect(screen.getByRole('checkbox', { name: text.cover })).toBeChecked()
  expect(screen.getByRole('textbox', { name: text.order })).toHaveValue('-3')
  expect(screen.queryByRole('img')).not.toBeInTheDocument()
  const user = userEvent.setup()
  await user.clear(screen.getByRole('textbox', { name: text.description }))
  await user.click(screen.getByRole('button', { name: text.reviewEdit }))
  const dialog = screen.getByRole('alertdialog')
  expect(within(dialog).getByRole('heading', { name: text.editReviewTitle })).toHaveFocus()
  expect(dialog).toHaveTextContent(text.previous); expect(dialog).toHaveTextContent(text.proposed)
  expect(sendMediaCommand).not.toHaveBeenCalled(); expect(auth.fetchCSRF).not.toHaveBeenCalled()
  await user.click(within(dialog).getByRole('button', { name: text.confirmEdit }))
  await waitFor(() => expect(work.router.state.location.pathname).toBe('/photos'))
  expect(screen.getByText(messages.mutations.done, { exact: true })).toBeVisible()
  expect(vi.mocked(sendMediaCommand).mock.calls[0]?.[0]).toMatchObject({ kind: 'patch-link', source: initial, linkID: 'first', body: { description: '' }, key: expect.any(String) })
})

it('keeps invalid raw edits during explicit adoption of a concurrent link', async () => {
  const work = mount(); await ready()
  const user = userEvent.setup(), order = screen.getByRole('textbox', { name: text.order }), description = screen.getByRole('textbox', { name: text.description })
  await user.clear(order); await user.type(order, '01')
  await user.clear(description); await user.type(description, 'Local caption')
  current = { owner: { kind: 'item', value: { ...item, revision: 5 } }, links: [other, { ...link, revision: 3, is_cover: false, sort_order: 6, description: 'Concurrent' }] }
  await act(async () => { await work.client.invalidateQueries({ queryKey: ['media'] }) })
  expect(await screen.findByText(text.changed, { exact: true })).toBeVisible()
  expect(screen.getByRole('button', { name: text.reviewEdit })).toBeDisabled()
  await user.click(screen.getByRole('button', { name: text.keepEdits }))
  expect(order).toHaveValue('01'); expect(description).toHaveValue('Local caption')
  expect(screen.getByRole('checkbox', { name: text.cover })).not.toBeChecked()
  await user.click(screen.getByRole('button', { name: text.reviewEdit }))
  expect(await screen.findByText(text.orderError, { exact: true })).toBeVisible()
  expect(sendMediaCommand).not.toHaveBeenCalled()
  await user.clear(order); await user.type(order, '0')
  await user.click(screen.getByRole('button', { name: text.reviewEdit }))
  await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: text.confirmEdit }))
  await waitFor(() => expect(work.router.state.location.pathname).toBe('/photos'))
  expect(vi.mocked(sendMediaCommand).mock.calls[0]?.[0]).toMatchObject({ kind: 'patch-link', linkID: 'first', body: { sort_order: 0, description: 'Local caption' }, source: { owner: { value: { revision: 5 } }, links: expect.arrayContaining([expect.objectContaining({ id: 'first', revision: 3 })]) } })
})

it('explicitly discards draft fields when adopting the current link', async () => {
  const work = mount(); await ready()
  const user = userEvent.setup(), order = screen.getByRole('textbox', { name: text.order }), description = screen.getByRole('textbox', { name: text.description })
  await user.clear(order); await user.type(order, '01')
  await user.clear(description); await user.type(description, 'Draft')
  current = { owner: { kind: 'item', value: { ...item, revision: 5 } }, links: [other, { ...link, revision: 3, is_cover: false, sort_order: 6, description: 'Current' }] }
  await act(async () => { await work.client.invalidateQueries({ queryKey: ['media'] }) })
  await screen.findByText(text.changed, { exact: true })
  await user.click(screen.getByRole('button', { name: text.discardEdits }))
  expect(order).toHaveValue('6'); expect(description).toHaveValue('Current')
  expect(screen.getByRole('checkbox', { name: text.cover })).not.toBeChecked()
  await user.click(screen.getByRole('button', { name: text.reviewEdit }))
  expect(screen.getByText(text.noChanges, { exact: true })).toBeVisible()
  expect(sendMediaCommand).not.toHaveBeenCalled()
})

it('retains an uncertain original request when the selected owner disappears', async () => {
  const work = mount(); await ready()
  vi.mocked(sendMediaCommand).mockImplementationOnce(async () => { current = null; throw new ApiError('NETWORK_ERROR') })
    .mockResolvedValueOnce({ id: link.id, revision: 3, media_id: image.id, owner_type: 'item', owner_id: item.id, is_cover: true, sort_order: -3, description: '' })
  const user = userEvent.setup(); await user.clear(screen.getByRole('textbox', { name: text.description }))
  await user.click(screen.getByRole('button', { name: text.reviewEdit }))
  await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: text.confirmEdit }))
  await screen.findByRole('heading', { name: messages.mutations.uncertain })
  const original = vi.mocked(sendMediaCommand).mock.calls[0]?.[0]
  await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: messages.mutations.cancel }))
  await screen.findByText(text.missing, { exact: true })
  expect(screen.getByRole('textbox', { name: text.description })).toBeDisabled()
  await act(async () => { window.dispatchEvent(new Event('online')) })
  expect(sendMediaCommand).toHaveBeenCalledTimes(1)
  await user.click(screen.getByRole('button', { name: messages.mutations.openOriginal }))
  await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: messages.mutations.retryOriginal }))
  await waitFor(() => expect(work.router.state.location.pathname).toBe('/photos'))
  expect(vi.mocked(sendMediaCommand).mock.calls[1]?.[0]).toBe(original)
})

it('preserves an initialized draft through failed reads without permitting a write', async () => {
  const work = mount(); await ready()
  const user = userEvent.setup(), description = screen.getByRole('textbox', { name: text.description })
  await user.clear(description); await user.type(description, 'Draft')
  vi.mocked(readOwnerMedia).mockRejectedValueOnce(new ApiError('NETWORK_ERROR'))
  await act(async () => { await work.client.invalidateQueries({ queryKey: ['media'] }) })
  expect(await screen.findByText(messages.locations.network)).toBeVisible()
  expect(description).toHaveValue('Draft'); expect(screen.getByRole('button', { name: text.reviewEdit })).toBeDisabled()
  expect(sendMediaCommand).not.toHaveBeenCalled()
})

it('shows a missing selected link as missing rather than initializing an empty editor', async () => {
  current = { ...initial, links: [other] }; mount()
  expect(await screen.findByText(text.linkMissing, { exact: true })).toBeVisible()
  expect(screen.queryByRole('textbox', { name: text.order })).not.toBeInTheDocument()
})

it('preserves a draft when the selected link is removed and refuses a new write', async () => {
  const work = mount(); await ready()
  const user = userEvent.setup(), description = screen.getByRole('textbox', { name: text.description })
  await user.clear(description); await user.type(description, 'Retained draft')
  current = { owner: { kind: 'item', value: { ...item, revision: 5 } }, links: [other] }
  await act(async () => { await work.client.invalidateQueries({ queryKey: ['media'] }) })
  expect(await screen.findByText(text.linkMissing, { exact: true })).toBeVisible()
  expect(description).toHaveValue('Retained draft')
  expect(screen.getByRole('button', { name: text.reviewEdit })).toBeDisabled()
  expect(sendMediaCommand).not.toHaveBeenCalled()
})

it('requires an explicit leave decision for a modified draft', async () => {
  const work = mount(); await ready()
  const user = userEvent.setup(); await user.clear(screen.getByRole('textbox', { name: text.description }))
  await user.click(screen.getByRole('link', { name: text.backLinks }))
  const dialog = screen.getByRole('alertdialog')
  expect(dialog).toHaveTextContent(text.leaveTitle)
  await user.click(within(dialog).getByRole('button', { name: text.stay }))
  expect(work.router.state.location.pathname).toBe('/photos/edit')
  expect(screen.getByRole('textbox', { name: text.description })).toHaveValue('')
})

it('purges a dirty draft on an expired session rather than blocking login', async () => {
  const work = mount(path, { catalogList: '/items?status=active', catalogCursors: ['cursor'], privateBody: 'never-forward' }); await ready()
  await userEvent.setup().clear(screen.getByRole('textbox', { name: text.description }))
  vi.mocked(readOwnerMedia).mockRejectedValueOnce(new ApiError('AUTHENTICATION_REQUIRED', { status: 401 }))
  await act(async () => { await work.client.invalidateQueries({ queryKey: ['media'] }) })
  await screen.findByRole('heading', { name: messages.auth.loginHeading })
  expect(work.router.state.location.state).toMatchObject({ returnTo: path, catalogList: '/items?status=active', catalogCursors: ['cursor'] })
  expect(work.router.state.location.state).not.toHaveProperty('privateBody')
  expect(work.client.getQueryData(['media', 'owner', 'item', item.id])).toBeUndefined()
})

it('clears the draft and preserves only the safe selected scope when password change becomes mandatory', async () => {
  const work = mount(path, { catalogList: '/items?status=active', catalogCursors: ['cursor'], privateBody: 'never-forward' }); await ready()
  vi.mocked(sendMediaCommand).mockRejectedValueOnce(new ApiError('PASSWORD_CHANGE_REQUIRED', { status: 403 }))
  vi.mocked(auth.fetchAdmin).mockResolvedValue({ id: 'admin', username: 'owner', revision: 2, must_change_password: true })
  const user = userEvent.setup(); await user.clear(screen.getByRole('textbox', { name: text.description }))
  await user.click(screen.getByRole('button', { name: text.reviewEdit }))
  await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: text.confirmEdit }))
  await waitFor(() => expect(work.router.state.location.pathname).toBe('/settings/account'))
  expect(work.router.state.location.state).toMatchObject({ returnTo: path, catalogList: '/items?status=active', catalogCursors: ['cursor'] })
  expect(work.router.state.location.state).not.toHaveProperty('privateBody')
  expect(work.client.getQueryData(['media', 'owner', 'item', item.id])).toBeUndefined()
})

it('does not dispatch an unchanged form or introduce a conflicting new cover', async () => {
  current = { ...initial, links: [other, { ...link, is_cover: false }] }
  mount(); await ready()
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: text.reviewEdit }))
  expect(screen.getByText(text.noChanges, { exact: true })).toBeVisible()
  await user.click(screen.getByRole('checkbox', { name: text.cover }))
  await user.click(screen.getByRole('button', { name: text.reviewEdit }))
  expect(await screen.findByText(text.coverConflict, { exact: true })).toBeVisible()
  expect(sendMediaCommand).not.toHaveBeenCalled(); expect(auth.fetchCSRF).not.toHaveBeenCalled()
})

it('disables an open review when the actual selected link changes', async () => {
  const work = mount(); await ready()
  const user = userEvent.setup(); await user.clear(screen.getByRole('textbox', { name: text.description }))
  await user.click(screen.getByRole('button', { name: text.reviewEdit }))
  current = { owner: { kind: 'item', value: { ...item, revision: 5 } }, links: [other, { ...link, revision: 3, description: 'Concurrent' }] }
  await act(async () => { await work.client.invalidateQueries({ queryKey: ['media'] }) })
  const dialog = screen.getByRole('alertdialog')
  expect(await within(dialog).findByText(text.reviewStale)).toBeVisible()
  expect(within(dialog).getByRole('button', { name: text.confirmEdit })).toBeDisabled()
  expect(sendMediaCommand).not.toHaveBeenCalled()
})

it('keeps a draft but refuses to adopt a different Media identity under the same link ID', async () => {
  const work = mount(); await ready()
  const user = userEvent.setup(), description = screen.getByRole('textbox', { name: text.description })
  await user.clear(description); await user.type(description, 'Retain this draft')
  current = { owner: { kind: 'item', value: { ...item, revision: 5 } }, links: [other, { ...link, revision: 3, media_id: 'replacement', media: { ...image, id: 'replacement' } }] }
  await act(async () => { await work.client.invalidateQueries({ queryKey: ['media'] }) })
  expect(await screen.findByText(text.changed, { exact: true })).toBeVisible()
  expect(description).toHaveValue('Retain this draft')
  expect(screen.getByRole('button', { name: text.reviewEdit })).toBeDisabled()
  expect(screen.queryByRole('button', { name: text.keepEdits })).not.toBeInTheDocument()
  expect(screen.queryByRole('button', { name: text.discardEdits })).not.toBeInTheDocument()
})

it('does not replay an offline draft and still requires a fresh explicit review', async () => {
  mount(); await ready()
  const user = userEvent.setup(), description = screen.getByRole('textbox', { name: text.description })
  await user.clear(description); await user.type(description, 'Offline draft')
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: false })
  await act(async () => { window.dispatchEvent(new Event('offline')) })
  expect(screen.getByText(text.offline, { exact: true })).toBeVisible()
  expect(description).toHaveValue('Offline draft'); expect(screen.getByRole('button', { name: text.reviewEdit })).toBeDisabled()
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: true })
  await act(async () => { window.dispatchEvent(new Event('online')) })
  await waitFor(() => expect(screen.getByRole('button', { name: text.reviewEdit })).toBeEnabled())
  expect(sendMediaCommand).not.toHaveBeenCalled(); expect(auth.fetchCSRF).not.toHaveBeenCalled()
})
