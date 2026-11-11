import { QueryClientProvider } from '@tanstack/react-query'
import { createMemoryRouter } from 'react-router'
import { RouterProvider } from 'react-router/dom'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeAll, beforeEach, expect, it, vi } from 'vitest'
import App from '@/App'
import * as auth from '@/lib/api/auth'
import { ApiError } from '@/lib/api/client'
import { mediaAttachRoute, mediaOwnerRoute } from '@/lib/media-navigation'
import { messages } from '@/lib/messages'
import { createQueryClient } from '@/lib/query'
import { sendMediaCommand } from './commands'
import { readMediaMembership, readOwnerMedia, type MediaMembership, type OwnerMedia } from './model'

vi.mock('@/lib/api/auth', async (original) => ({ ...await original<typeof import('@/lib/api/auth')>(), fetchAdmin: vi.fn(), fetchCSRF: vi.fn() }))
vi.mock('./commands', async (original) => ({ ...await original<typeof import('./commands')>(), sendMediaCommand: vi.fn() }))
vi.mock('./model', async (original) => ({ ...await original<typeof import('./model')>(), readOwnerMedia: vi.fn(), readMediaMembership: vi.fn() }))
const text = messages.media
const item = { id: 'owner/\0😀', revision: 4, name: 'Fixture owner', brand: null, model: '', category_id: null, description: '', notes: '', tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1, status: 'active' } as const
const image = { id: 'links', revision: 7, sha256: 'a'.repeat(64), mime_type: 'image/heic', verification: 'unverified', width: 10, height: 20, bytes: 30, thumb_url: null, thumb_sha256: null } as const
const sibling = { id: 'sibling', revision: 2, media_id: image.id, owner_type: 'item', owner_id: 'other', is_cover: true, sort_order: -3, description: 'Existing', media: image } as const
const initialOwner: OwnerMedia = { owner: { kind: 'item', value: item }, links: [] }
const initialMembership: MediaMembership = { media: image, links: [sibling] }
const scope = { kind: 'item', id: item.id } as const
const path = mediaAttachRoute(scope), list = mediaOwnerRoute(scope)
let owner: OwnerMedia | null, membership: MediaMembership | null
const disposers: (() => void)[] = []

beforeAll(async () => { await Promise.all([import('./media-page'), import('@/features/auth/account-page')]) })
beforeEach(() => {
  vi.clearAllMocks(); owner = structuredClone(initialOwner); membership = structuredClone(initialMembership)
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: true })
  vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
  vi.mocked(auth.fetchAdmin).mockReset().mockResolvedValue({ id: 'admin', username: 'owner', revision: 2, must_change_password: false })
  vi.mocked(auth.fetchCSRF).mockReset().mockResolvedValue('C'.repeat(43))
  vi.mocked(readOwnerMedia).mockReset().mockImplementation(async () => {
    if (!owner) throw new ApiError('NOT_FOUND')
    return structuredClone(owner)
  })
  vi.mocked(readMediaMembership).mockReset().mockImplementation(async (id) => {
    if (!membership || membership.media.id !== id) throw new ApiError('NOT_FOUND', { status: 404 })
    return structuredClone(membership)
  })
  vi.mocked(sendMediaCommand).mockReset().mockImplementation(async (command) => {
    if (command.kind !== 'attach') throw new Error('Unexpected fixture command')
    return { id: 'created', revision: 1, media_id: command.membership.media.id, owner_type: command.owner.owner.kind,
      owner_id: command.owner.owner.value.id, is_cover: command.body.is_cover ?? false, sort_order: command.body.sort_order ?? 0, description: command.body.description ?? '' }
  })
})
afterEach(() => { for (const dispose of disposers.splice(0)) dispose() })
function mount(url = path, state?: unknown, cached?: MediaMembership) {
  const client = createQueryClient(), [pathname, search = ''] = url.split('?')
  if (cached) client.setQueryData(['media', 'membership', cached.media.id], cached)
  const router = createMemoryRouter([{ path: '*', element: <App /> }], { initialEntries: [{ pathname, search: search ? `?${search}` : '', state }] })
  const view = render(<QueryClientProvider client={client}><RouterProvider router={router} /></QueryClientProvider>)
  disposers.push(() => { view.unmount(); router.dispose(); client.clear() })
  return { client, router }
}
async function ready() {
  await screen.findByRole('heading', { name: text.attachTitle, level: 1 })
  await screen.findByRole('textbox', { name: text.mediaID })
}
async function lookup(id: string = image.id) {
  const user = userEvent.setup()
  await user.clear(screen.getByRole('textbox', { name: text.mediaID }))
  await user.paste(JSON.stringify(id))
  await user.click(screen.getByRole('button', { name: text.lookupMedia }))
  await screen.findByText(text.selectedMedia, { exact: true })
}

it('opens from the owner list and attaches only an explicitly reviewed existing record with both original proofs', async () => {
  const work = mount(list)
  await screen.findByText('Fixture owner', { exact: true })
  await userEvent.setup().click(screen.getByRole('link', { name: text.attach }))
  await ready()
  expect(work.router.state.location.pathname).toBe('/photos/attach')
  expect(readMediaMembership).not.toHaveBeenCalled()
  await lookup()
  expect(readMediaMembership).toHaveBeenCalledWith('links', expect.any(AbortSignal))
  expect(screen.getByText(text.unverified, { exact: true })).toBeVisible()
  expect(screen.getByText(`${text.membershipCount}1`)).toBeVisible()
  expect(screen.queryByText(text.alreadyLinked)).not.toBeInTheDocument()
  expect(screen.queryByRole('img')).not.toBeInTheDocument()
  expect(screen.getByRole('checkbox', { name: text.cover })).not.toBeChecked()
  expect(screen.getByRole('textbox', { name: text.order })).toHaveValue('0')
  expect(screen.getByRole('textbox', { name: text.description })).toHaveValue('')
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: text.reviewAttach }))
  const dialog = screen.getByRole('alertdialog')
  expect(within(dialog).getByRole('heading', { name: text.attachReviewTitle })).toHaveFocus()
  expect(dialog).toHaveTextContent(text.attachReviewHelp); expect(dialog).toHaveTextContent('links')
  expect(sendMediaCommand).not.toHaveBeenCalled(); expect(auth.fetchCSRF).not.toHaveBeenCalled()
  await user.click(within(dialog).getByRole('button', { name: text.confirmAttach }))
  await waitFor(() => expect(work.router.state.location.pathname).toBe('/photos'))
  expect(screen.getByText(messages.mutations.done, { exact: true })).toBeVisible()
  expect(vi.mocked(sendMediaCommand).mock.calls[0]?.[0]).toMatchObject({ kind: 'attach', owner: initialOwner, membership: initialMembership,
    body: { is_cover: false, sort_order: 0, description: '' }, key: expect.any(String) })
})

it('does not use a cached Media membership before its first current bracketed read', async () => {
  let release: ((value: MediaMembership) => void) | undefined
  vi.mocked(readMediaMembership).mockImplementationOnce(() => new Promise((resolve) => { release = resolve }))
  mount(path, undefined, { media: { ...image, revision: 1 }, links: [] }); await ready()
  const user = userEvent.setup(); await user.type(screen.getByRole('textbox', { name: text.mediaID }), '"links"')
  await user.click(screen.getByRole('button', { name: text.lookupMedia }))
  await waitFor(() => expect(readMediaMembership).toHaveBeenCalled())
  expect(screen.queryByText(text.selectedMedia, { exact: true })).not.toBeInTheDocument()
  await act(async () => { release?.(structuredClone(initialMembership)) })
  expect(await screen.findByText(text.selectedMedia, { exact: true })).toBeVisible()
  expect(screen.getByText(`${text.membershipCount}1`)).toBeVisible()
})

it('rechecks a previously selected Media instead of reusing its cached membership', async () => {
  mount(); await ready(); await lookup()
  const other = { ...image, id: 'other', revision: 2 }
  membership = { media: other, links: [] }
  await lookup('other')
  let release: ((value: MediaMembership) => void) | undefined
  vi.mocked(readMediaMembership).mockImplementationOnce(() => new Promise((resolve) => { release = resolve }))
  const user = userEvent.setup(), id = screen.getByRole('textbox', { name: text.mediaID })
  await user.clear(id); await user.paste(JSON.stringify(image.id))
  await user.click(screen.getByRole('button', { name: text.lookupMedia }))
  await waitFor(() => expect(readMediaMembership).toHaveBeenLastCalledWith(image.id, expect.any(AbortSignal)))
  expect(screen.queryByText(text.selectedMedia, { exact: true })).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: text.reviewAttach })).toBeDisabled()
  await act(async () => { release?.(structuredClone(initialMembership)) })
  expect(await screen.findByText(text.selectedMedia, { exact: true })).toBeVisible()
})

it.each(['.', '..', 'unsafe/id', 'unsafe\\id', '\0', ' photo/\0😀\r\n ', 'percent%2F/plus+?&=#', '😀'.repeat(127) + '/'])(
  'preserves and explicitly reviews the exact retained Media ID %j', async (exact) => {
    const selected = { ...image, id: exact }
    membership = { media: selected, links: [] }
    const work = mount(); await ready()
    const id = screen.getByRole('textbox', { name: text.mediaID }), user = userEvent.setup()
    fireEvent.change(id, { target: { value: JSON.stringify(exact) } })
    await user.click(screen.getByRole('button', { name: text.lookupMedia }))
    await screen.findByRole('heading', { name: text.selectedMedia })
    expect(id).toHaveValue(JSON.stringify(exact)); expect(readMediaMembership).toHaveBeenCalledWith(exact, expect.any(AbortSignal))
    await user.click(screen.getByRole('button', { name: text.reviewAttach }))
    const dialog = screen.getByRole('alertdialog')
    expect(dialog).toHaveTextContent(JSON.stringify(exact)); expect(sendMediaCommand).not.toHaveBeenCalled()
    await user.click(within(dialog).getByRole('button', { name: text.confirmAttach }))
    await waitFor(() => expect(work.router.state.location.pathname).toBe('/photos'))
    expect(vi.mocked(sendMediaCommand).mock.calls[0]?.[0]).toMatchObject({ kind: 'attach', owner: initialOwner, membership: { media: selected, links: [] } })
  },
)

it('rejects malformed JSON, non-strings and invalid decoded IDs without reading', async () => {
  mount(); await ready()
  const user = userEvent.setup(), id = screen.getByRole('textbox', { name: text.mediaID })
  for (const value of ['links', 'null', '42', '"\\ud800"', '""', JSON.stringify('😀'.repeat(129)), '{"id":"links"}', '"\\uXYZ"']) {
    fireEvent.change(id, { target: { value } })
    await user.click(screen.getByRole('button', { name: text.lookupMedia }))
    expect(screen.getByText(text.mediaIDError, { exact: true })).toBeVisible()
  }
  expect(readMediaMembership).not.toHaveBeenCalled()
})

it('uses the explicitly selected text format without treating literal quotes as JSON', async () => {
  const quoted = '"links"', selected = { ...image, id: quoted }
  membership = { media: selected, links: [] }
  mount(); await ready()
  const user = userEvent.setup()
  await user.selectOptions(screen.getByRole('combobox', { name: text.mediaIDFormat }), 'text')
  expect(screen.getByText(text.mediaLookupHelp)).toHaveTextContent('瀏覽器可能改變換行')
  await user.type(screen.getByRole('textbox', { name: text.mediaID }), quoted)
  await user.click(screen.getByRole('button', { name: text.lookupMedia }))
  await screen.findByText(text.selectedMedia, { exact: true })
  expect(readMediaMembership).toHaveBeenCalledWith(quoted, expect.any(AbortSignal))
})

it('requires a new lookup when the Media ID input changes', async () => {
  mount(); await ready(); await lookup()
  const user = userEvent.setup(), id = screen.getByRole('textbox', { name: text.mediaID })
  await user.type(id, 'x')
  expect(screen.getByRole('button', { name: text.reviewAttach })).toBeDisabled()
  expect(sendMediaCommand).not.toHaveBeenCalled()
})

it('retains invalid raw fields until explicit adoption of both changed exact Media scopes', async () => {
  const exact = 'legacy/\0😀\r\n', selected = { ...image, id: exact }
  membership = { media: selected, links: [{ ...sibling, media_id: exact, media: selected }] }
  const work = mount(); await ready(); await lookup(exact)
  const user = userEvent.setup(), order = screen.getByRole('textbox', { name: text.order }), description = screen.getByRole('textbox', { name: text.description })
  await user.clear(order); await user.type(order, '01'); await user.type(description, 'Draft é')
  owner = { owner: { kind: 'item', value: { ...item, revision: 5, name: 'Changed owner' } }, links: [] }
  const changed = { ...selected, revision: 8 }
  membership = { media: changed, links: [{ ...sibling, media_id: exact, media: changed }] }
  await act(async () => { await work.client.invalidateQueries({ queryKey: ['media'] }) })
  expect(await screen.findByText(text.attachChanged, { exact: true })).toBeVisible()
  expect(screen.getByRole('button', { name: text.reviewAttach })).toBeDisabled()
  await user.click(screen.getByRole('button', { name: text.adoptSources }))
  expect(order).toHaveValue('01'); expect(description).toHaveValue('Draft é')
  await user.click(screen.getByRole('button', { name: text.reviewAttach }))
  expect(await screen.findByText(text.attachOrderError, { exact: true })).toBeVisible()
  expect(sendMediaCommand).not.toHaveBeenCalled()
  await user.clear(order); await user.type(order, '0')
  await user.click(screen.getByRole('button', { name: text.reviewAttach }))
  await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: text.confirmAttach }))
  await waitFor(() => expect(work.router.state.location.pathname).toBe('/photos'))
  expect(vi.mocked(sendMediaCommand).mock.calls[0]?.[0]).toMatchObject({ kind: 'attach', owner: { owner: { value: { revision: 5 } } }, membership: { media: { revision: 8 } }, body: { sort_order: 0, description: 'Draft é' } })
})

it('withholds independently read scopes whose overlapping links do not agree', async () => {
  const selected = { ...sibling, id: 'owned', owner_id: item.id }
  owner = { ...initialOwner, links: [selected] }
  const work = mount(); await ready()
  const user = userEvent.setup(); await user.type(screen.getByRole('textbox', { name: text.mediaID }), '"links"')
  await user.click(screen.getByRole('button', { name: text.lookupMedia }))
  expect(await screen.findByText(text.scopeMismatch, { exact: true })).toBeVisible()
  expect(screen.getByRole('button', { name: text.reviewAttach })).toBeDisabled()
  membership = { media: image, links: [sibling, selected] }
  await act(async () => { await work.client.invalidateQueries({ queryKey: ['media'] }) })
  await waitFor(() => expect(screen.getByRole('button', { name: text.reviewAttach })).toBeEnabled())
  expect(sendMediaCommand).not.toHaveBeenCalled()
})

it('warns when the same owner already links this Media but allows another independently reviewed link', async () => {
  const existing = { ...sibling, id: 'owned', owner_id: item.id, is_cover: false }
  owner = { ...initialOwner, links: [existing] }; membership = { media: image, links: [sibling, existing] }
  const work = mount(); await ready()
  const warning = text.alreadyLinked
  expect(screen.queryByText(warning)).not.toBeInTheDocument()
  await lookup()
  expect(screen.getByText(warning)).toBeVisible()
  const user = userEvent.setup(); await user.click(screen.getByRole('button', { name: text.reviewAttach }))
  const dialog = screen.getByRole('alertdialog')
  expect(within(dialog).getByText(warning)).toBeVisible()
  expect(sendMediaCommand).not.toHaveBeenCalled(); expect(auth.fetchCSRF).not.toHaveBeenCalled()
  await user.click(within(dialog).getByRole('button', { name: text.confirmAttach }))
  await waitFor(() => expect(work.router.state.location.pathname).toBe('/photos'))
  expect(sendMediaCommand).toHaveBeenCalledTimes(1)
  expect(vi.mocked(sendMediaCommand).mock.calls[0]?.[0]).toMatchObject({ kind: 'attach', owner: { links: [existing] }, membership: { links: [sibling, existing] } })
})

it('rejects a new cover when another owner link is already a cover', async () => {
  const existing = { ...sibling, id: 'owned', owner_id: item.id }
  owner = { ...initialOwner, links: [existing] }; membership = { media: image, links: [sibling, existing] }
  mount(); await ready(); await lookup()
  const user = userEvent.setup(); await user.click(screen.getByRole('checkbox', { name: text.cover }))
  await user.click(screen.getByRole('button', { name: text.reviewAttach }))
  expect(await screen.findByText(text.coverConflict, { exact: true })).toBeVisible()
  expect(sendMediaCommand).not.toHaveBeenCalled(); expect(auth.fetchCSRF).not.toHaveBeenCalled()
})

it('keeps a draft through a failed or missing Media read without inventing an empty membership', async () => {
  const work = mount(); await ready(); await lookup()
  const user = userEvent.setup(), description = screen.getByRole('textbox', { name: text.description })
  await user.type(description, 'Retained draft')
  vi.mocked(readMediaMembership).mockRejectedValueOnce(new ApiError('NETWORK_ERROR'))
  await act(async () => { await work.client.invalidateQueries({ queryKey: ['media'] }) })
  expect(await screen.findByText(messages.locations.network)).toBeVisible()
  expect(description).toHaveValue('Retained draft'); expect(screen.getByRole('button', { name: text.reviewAttach })).toBeDisabled()
  membership = null
  await act(async () => { await work.client.invalidateQueries({ queryKey: ['media'] }) })
  expect(await screen.findByText(text.mediaMissing, { exact: true })).toBeVisible()
  expect(description).toHaveValue('Retained draft'); expect(screen.getByRole('button', { name: text.reviewAttach })).toBeDisabled()
  expect(sendMediaCommand).not.toHaveBeenCalled()
})

it('allows selecting a different Media after a failed lookup without discarding the draft', async () => {
  const work = mount(); await ready(); await lookup()
  const user = userEvent.setup(), description = screen.getByRole('textbox', { name: text.description })
  await user.type(description, 'Retained draft')
  vi.mocked(readMediaMembership).mockRejectedValueOnce(new ApiError('NETWORK_ERROR'))
  await act(async () => { await work.client.invalidateQueries({ queryKey: ['media'] }) })
  await screen.findByText(messages.locations.network)
  const other = { ...image, id: 'other/\0😀\r\n', revision: 2 }
  membership = { media: other, links: [] }
  const id = screen.getByRole('textbox', { name: text.mediaID })
  await user.clear(id); await user.paste(JSON.stringify(other.id))
  expect(screen.getByRole('button', { name: text.lookupMedia })).toBeEnabled()
  await user.click(screen.getByRole('button', { name: text.lookupMedia }))
  await waitFor(() => expect(screen.getByText(`${text.membershipCount}0`, { exact: true })).toBeVisible())
  expect(description).toHaveValue('Retained draft')
  expect(screen.getByRole('button', { name: text.reviewAttach })).toBeEnabled()
  expect(sendMediaCommand).not.toHaveBeenCalled()
})

it('disables an open review if either actual scope changes', async () => {
  const work = mount(); await ready(); await lookup()
  const user = userEvent.setup(); await user.click(screen.getByRole('button', { name: text.reviewAttach }))
  const changed = { ...image, revision: 8 }
  membership = { media: changed, links: [{ ...sibling, media: changed }] }
  await act(async () => { await work.client.invalidateQueries({ queryKey: ['media'] }) })
  const dialog = screen.getByRole('alertdialog')
  expect(await within(dialog).findByText(text.attachReviewStale)).toBeVisible()
  expect(within(dialog).getByRole('button', { name: text.confirmAttach })).toBeDisabled()
  expect(sendMediaCommand).not.toHaveBeenCalled()
})

it('retains an uncertain exact Media request after the owner and Media disappear and retries only explicitly', async () => {
  const exact = 'legacy/\0😀\r\n', selected = { ...image, id: exact }
  membership = { media: selected, links: [] }
  const work = mount(); await ready(); await lookup(exact)
  vi.mocked(sendMediaCommand).mockImplementationOnce(async () => { owner = null; membership = null; throw new ApiError('NETWORK_ERROR') })
    .mockResolvedValueOnce({ id: 'created', revision: 1, media_id: exact, owner_type: 'item', owner_id: item.id, is_cover: false, sort_order: 0, description: '' })
  const user = userEvent.setup(); await user.click(screen.getByRole('button', { name: text.reviewAttach }))
  await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: text.confirmAttach }))
  await screen.findByRole('heading', { name: messages.mutations.uncertain })
  const original = vi.mocked(sendMediaCommand).mock.calls[0]?.[0]
  await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: messages.mutations.cancel }))
  await screen.findByText(text.missing, { exact: true })
  await act(async () => { window.dispatchEvent(new Event('online')) })
  expect(sendMediaCommand).toHaveBeenCalledTimes(1)
  await user.click(screen.getByRole('button', { name: messages.mutations.openOriginal }))
  await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: messages.mutations.retryOriginal }))
  await waitFor(() => expect(work.router.state.location.pathname).toBe('/photos'))
  expect(vi.mocked(sendMediaCommand).mock.calls[1]?.[0]).toBe(original)
})

it('guards a dirty draft but permits session purge from a failed Media read', async () => {
  const work = mount(path, { catalogList: '/items?status=active', catalogCursors: ['cursor'], privateBody: 'never-forward' }); await ready()
  const user = userEvent.setup(); await user.type(screen.getByRole('textbox', { name: text.mediaID }), '"links"')
  await user.click(screen.getByRole('link', { name: text.backLinks }))
  expect(screen.getByRole('alertdialog')).toHaveTextContent(text.leaveTitle)
  await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: text.stay }))
  vi.mocked(readMediaMembership).mockRejectedValueOnce(new ApiError('AUTHENTICATION_REQUIRED', { status: 401 }))
  await user.click(screen.getByRole('button', { name: text.lookupMedia }))
  await screen.findByRole('heading', { name: messages.auth.loginHeading })
  expect(work.router.state.location.state).toMatchObject({ returnTo: path, catalogList: '/items?status=active', catalogCursors: ['cursor'] })
  expect(work.router.state.location.state).not.toHaveProperty('privateBody')
  expect(work.client.getQueryData(['media', 'owner', 'item', item.id])).toBeUndefined()
})

it('retains an offline exact Media draft without sending or replaying a write', async () => {
  const exact = 'legacy/\0😀', selected = { ...image, id: exact }
  membership = { media: selected, links: [] }
  mount(); await ready(); await lookup(exact)
  const user = userEvent.setup(), description = screen.getByRole('textbox', { name: text.description })
  await user.type(description, 'Offline draft')
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: false })
  await act(async () => { window.dispatchEvent(new Event('offline')) })
  expect(screen.getByText(text.offline, { exact: true })).toBeVisible()
  expect(description).toHaveValue('Offline draft'); expect(screen.getByRole('button', { name: text.reviewAttach })).toBeDisabled()
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: true })
  await act(async () => { window.dispatchEvent(new Event('online')) })
  await waitFor(() => expect(screen.getByRole('button', { name: text.reviewAttach })).toBeEnabled())
  expect(sendMediaCommand).not.toHaveBeenCalled(); expect(auth.fetchCSRF).not.toHaveBeenCalled()
})
