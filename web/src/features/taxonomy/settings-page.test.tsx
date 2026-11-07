import { QueryClientProvider } from '@tanstack/react-query'
import { createMemoryRouter } from 'react-router'
import { RouterProvider } from 'react-router/dom'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeAll, beforeEach, expect, it, vi } from 'vitest'
import App from '@/App'
import * as auth from '@/lib/api/auth'
import * as api from '@/lib/api/taxonomy'
import { ApiError } from '@/lib/api/client'
import { messages } from '@/lib/messages'
import { createQueryClient } from '@/lib/query'

vi.mock('@/lib/api/client', async (original) => ({ ...await original<typeof import('@/lib/api/client')>(), fetchHealth: vi.fn().mockResolvedValue({ status: 'ok' }) }))
vi.mock('@/lib/api/auth', async (original) => ({ ...await original<typeof import('@/lib/api/auth')>(), fetchAdmin: vi.fn(), fetchCSRF: vi.fn(), login: vi.fn() }))
vi.mock('@/lib/api/taxonomy', async (original) => ({ ...await original<typeof import('@/lib/api/taxonomy')>(), fetchCategoriesPage: vi.fn(), fetchTagsPage: vi.fn(), createCategory: vi.fn(), patchCategory: vi.fn(), deleteCategory: vi.fn(), createTag: vi.fn(), patchTag: vi.fn(), deleteTag: vi.fn() }))
const text = messages.taxonomy
const root: api.Category = { id: 'parent', parent_id: null, name: 'Parent', revision: 7 }
const child: api.Category = { id: 'child', parent_id: 'parent', name: 'Child', revision: 1 }
const tag: api.Tag = { id: 'tag', name: 'Tag', revision: 2 }
let administrator: auth.Admin | null
let categories: Map<string, api.Category>
let tags: Map<string, api.Tag>
const disposers: (() => void)[] = []
const field = (label: string) => screen.getByLabelText(new RegExp(`^${label}`))

// Wait for real module compilation outside state-transition assertions. The
// browser suite still loads the actual production chunks and their recovery UI.
beforeAll(async () => { await Promise.all([import('./settings-page'), import('@/features/auth/account-page')]) })

beforeEach(() => {
  vi.clearAllMocks()
  administrator = { id: 'owner', username: 'owner', revision: 2, must_change_password: false }
  categories = new Map([[root.id, root]])
  tags = new Map([[tag.id, tag]])
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: true })
  vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
  vi.mocked(auth.fetchAdmin).mockReset().mockImplementation(async () => { if (!administrator) throw new ApiError('AUTHENTICATION_REQUIRED', { status: 401 }); return administrator })
  vi.mocked(auth.fetchCSRF).mockReset().mockResolvedValue('C'.repeat(43))
  vi.mocked(auth.login).mockReset().mockImplementation(async () => { administrator = { id: 'owner', username: 'owner', revision: 2, must_change_password: false }; return administrator })
  vi.mocked(api.fetchCategoriesPage).mockReset().mockImplementation(async () => ({ data: Array.from(categories.values()), next_cursor: null }))
  vi.mocked(api.fetchTagsPage).mockReset().mockImplementation(async () => ({ data: Array.from(tags.values()), next_cursor: null }))
  vi.mocked(api.createCategory).mockReset().mockImplementation(async (body) => { const value = { ...body, id: 'created-category', parent_id: body.parent_id ?? null, revision: 1 }; categories.set(value.id, value); return value })
  vi.mocked(api.patchCategory).mockReset().mockImplementation(async (id, body) => { const value = { ...categories.get(id)!, ...body, revision: categories.get(id)!.revision + 1 }; categories.set(id, value); return value })
  vi.mocked(api.deleteCategory).mockReset().mockImplementation(async (id, context) => { categories.delete(id); return { ok: true, operation_id: 'delete-category-event', revision: context.expectedRevision + 1 } })
  vi.mocked(api.createTag).mockReset().mockImplementation(async (body) => { const value = { ...body, id: 'created-tag', revision: 1 }; tags.set(value.id, value); return value })
  vi.mocked(api.patchTag).mockReset().mockImplementation(async (id, body) => { const value = { ...tags.get(id)!, ...body, revision: tags.get(id)!.revision + 1 }; tags.set(id, value); return value })
  vi.mocked(api.deleteTag).mockReset().mockImplementation(async (id, context) => { tags.delete(id); return { ok: true, operation_id: 'delete-tag-event', revision: context.expectedRevision + 1 } })
})
afterEach(() => { for (const dispose of disposers.splice(0)) dispose() })
function mount(path = '/settings/categories') {
  const client = createQueryClient()
  const router = createMemoryRouter([{ path: '*', element: <App /> }], { initialEntries: [path] })
  const view = render(<QueryClientProvider client={client}><RouterProvider router={router} /></QueryClientProvider>)
  disposers.push(() => { view.unmount(); router.dispose(); client.clear() })
  return { client, router }
}
async function editor(kind: 'categories' | 'tags' = 'categories') {
  await screen.findByLabelText(new RegExp(`^${kind === 'categories' ? text.categoryName : text.tagName}`))
  await waitFor(() => expect(screen.getByRole('button', { name: text.review })).toBeEnabled())
}
async function confirm(label: string) {
  const dialog = await screen.findByRole('alertdialog')
  await userEvent.setup().click(within(dialog).getByRole('button', { name: label }))
  await screen.findByText(text.done, { exact: true })
  await waitFor(() => expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument())
}

it('provides authenticated settings navigation and actual client version without fetching invented domain data', async () => {
  mount('/settings')
  expect(await screen.findByRole('heading', { name: messages.settings.appearance })).toBeVisible()
  expect(screen.getByText(/前端版本：0\.1\.0-dev/)).toBeVisible()
  expect(api.fetchCategoriesPage).not.toHaveBeenCalled()
  expect(api.fetchTagsPage).not.toHaveBeenCalled()
  await userEvent.setup().click(within(screen.getByRole('navigation', { name: messages.settings.navigation })).getByRole('link', { name: messages.settings.categories }))
  expect(await screen.findByRole('link', { name: '「Parent」' })).toBeVisible()
  expect(api.fetchCategoriesPage).toHaveBeenCalledWith({ limit: 100 }, expect.any(AbortSignal))
  expect(auth.fetchCSRF).not.toHaveBeenCalled()
})

it('creates a root only after explicit review with no unrelated parent revision', async () => {
  mount('/settings/categories/new')
  await editor()
  fireEvent.change(field(text.categoryName), { target: { value: '  Created é  ' } })
  await userEvent.setup().click(screen.getByRole('button', { name: text.review }))
  expect(api.createCategory).not.toHaveBeenCalled()
  await confirm(text.confirmCreateCategory)
  expect(api.createCategory).toHaveBeenCalledWith({ name: '  Created é  ', parent_id: null }, expect.objectContaining({ idempotencyKey: expect.any(String) }))
  expect(vi.mocked(api.createCategory).mock.calls[0]?.[1]).not.toHaveProperty('expectedRevision')
  expect(await screen.findByRole('link', { name: '「 Created é 」' })).toBeVisible()
})

it('captures child-parent proof and requires explicit review after a live parent change', async () => {
  const user = userEvent.setup()
  const work = mount('/settings/categories/new?parent=parent')
  await editor()
  fireEvent.change(field(text.categoryName), { target: { value: 'Created child' } })
  categories.set(root.id, { ...root, name: 'Concurrent parent', revision: 8 })
  await act(async () => { await work.client.invalidateQueries({ queryKey: ['taxonomy'] }) })
  await screen.findByRole('button', { name: text.reviewParent })
  expect(screen.getByRole('button', { name: text.review })).toBeDisabled()
  await user.click(screen.getByRole('button', { name: text.reviewParent }))
  await user.click(screen.getByRole('button', { name: text.review }))
  expect(screen.getByRole('alertdialog')).toHaveTextContent(`${text.parentProof}8`)
  await confirm(text.confirmCreateCategory)
  expect(api.createCategory).toHaveBeenCalledWith({ name: 'Created child', parent_id: 'parent' }, expect.objectContaining({ expectedRevision: 8 }))
})

it('retains an unknown prefilled parent visibly instead of silently making a root', async () => {
  mount('/settings/categories/new?parent=missing')
  await screen.findByLabelText(new RegExp(`^${text.categoryName}`))
  expect(screen.getByRole('option', { name: text.parentUnavailable })).toBeDisabled()
  expect(screen.getByRole('combobox')).toHaveValue('missing')
  expect(screen.getByRole('button', { name: text.review })).toBeDisabled()
  expect(api.createCategory).not.toHaveBeenCalled()
})

it('validates original form text, detects no-ops, and sends only a nullable-parent patch', async () => {
  const user = userEvent.setup()
  categories.set(child.id, child)
  mount('/settings/categories/child/edit')
  await editor()
  await user.click(screen.getByRole('button', { name: text.review }))
  expect(screen.getByRole('status')).toHaveTextContent(text.noChanges)
  fireEvent.change(field(text.categoryName), { target: { value: '' } })
  await user.click(screen.getByRole('button', { name: text.review }))
  expect(await screen.findByText(text.nameError)).toBeVisible()
  fireEvent.change(field(text.categoryName), { target: { value: child.name } })
  await user.selectOptions(screen.getByRole('combobox'), '')
  await user.click(screen.getByRole('button', { name: text.review }))
  await confirm(text.confirmSave)
  expect(api.patchCategory).toHaveBeenCalledWith(child.id, { parent_id: null }, expect.objectContaining({ expectedRevision: 1 }))
})

it('keeps modified fields and adopts untouched current metadata only after explicit conflict comparison', async () => {
  const user = userEvent.setup()
  const work = mount('/settings/categories/parent/edit')
  await editor()
  fireEvent.change(field(text.categoryName), { target: { value: 'Local edit' } })
  categories.set('other', { ...root, id: 'other', name: 'Other' })
  categories.set(root.id, { ...root, name: 'Remote edit', parent_id: 'other', revision: 8 })
  await act(async () => { await work.client.invalidateQueries({ queryKey: ['taxonomy'] }) })
  await screen.findByText(text.changed)
  expect(field(text.categoryName)).toHaveValue('Local edit')
  expect(screen.getByRole('button', { name: text.review })).toBeDisabled()
  await user.click(screen.getByRole('button', { name: text.keepEdits }))
  expect(screen.getByRole('combobox')).toHaveValue('other')
  await user.click(screen.getByRole('button', { name: text.review }))
  await confirm(text.confirmSave)
  expect(api.patchCategory).toHaveBeenCalledWith(root.id, { name: 'Local edit' }, expect.objectContaining({ expectedRevision: 8 }))
})

it('discards a draft only through its explicit current-state adoption action', async () => {
  const user = userEvent.setup()
  const work = mount('/settings/tags/tag/edit')
  await editor('tags')
  fireEvent.change(field(text.tagName), { target: { value: 'Draft' } })
  tags.set(tag.id, { ...tag, name: 'Current', revision: 3 })
  await act(async () => { await work.client.invalidateQueries({ queryKey: ['taxonomy'] }) })
  await screen.findByText(text.changed)
  await user.click(screen.getByRole('button', { name: text.discardEdits }))
  expect(field(text.tagName)).toHaveValue('Current')
  expect(api.patchTag).not.toHaveBeenCalled()
})

it('prevents nonleaf deletion, then explicitly reviews and deletes a leaf without inventing impact counts', async () => {
  categories.set(child.id, child)
  const work = mount('/settings/categories/parent/edit')
  await editor()
  expect(screen.getByRole('button', { name: text.deleteCategory })).toBeDisabled()
  expect(screen.getByText(text.children)).toBeVisible()
  await act(async () => { await work.router.navigate('/settings/categories/child/edit') })
  await editor()
  await userEvent.setup().click(screen.getByRole('button', { name: text.deleteCategory }))
  expect(screen.getByRole('alertdialog')).toHaveTextContent(text.deleteCategoryHelp)
  expect(api.deleteCategory).not.toHaveBeenCalled()
  await confirm(text.confirmDeleteCategory)
  expect(api.deleteCategory).toHaveBeenCalledWith(child.id, expect.objectContaining({ expectedRevision: 1 }))
})

it('creates, renames and deletes tags through explicit reviews with original keys and proofs', async () => {
  const user = userEvent.setup()
  const work = mount('/settings/tags/new')
  await editor('tags')
  fireEvent.change(field(text.tagName), { target: { value: 'Created' } })
  await user.click(screen.getByRole('button', { name: text.review }))
  await confirm(text.confirmCreateTag)
  expect(work.router.state.location).toMatchObject({ pathname: '/settings/tags', state: { taxonomyNotice: 'confirmed' } })
  expect(api.createTag).toHaveBeenCalledWith({ name: 'Created' }, expect.objectContaining({ csrfToken: 'C'.repeat(43) }))
  await act(async () => { await work.router.navigate('/settings/tags/created-tag/edit') })
  await editor('tags')
  fireEvent.change(field(text.tagName), { target: { value: 'Renamed' } })
  await user.click(screen.getByRole('button', { name: text.review }))
  await confirm(text.confirmSave)
  expect(work.router.state.location).toMatchObject({ pathname: '/settings/tags', state: { taxonomyNotice: 'confirmed' } })
  expect(api.patchTag).toHaveBeenCalledWith('created-tag', { name: 'Renamed' }, expect.objectContaining({ expectedRevision: 1 }))
  await act(async () => { await work.router.navigate('/settings/tags/created-tag/edit') })
  await editor('tags')
  await user.click(screen.getByRole('button', { name: text.deleteTag }))
  await confirm(text.confirmDeleteTag)
  expect(work.router.state.location).toMatchObject({ pathname: '/settings/tags', state: { taxonomyNotice: 'confirmed' } })
  expect(api.deleteTag).toHaveBeenCalledWith('created-tag', expect.objectContaining({ expectedRevision: 2 }))
  expect(await screen.findByRole('heading', { name: messages.settings.tags, level: 1 })).toBeVisible()
  expect(await screen.findByRole('link', { name: /^「Tag」$/ })).toBeVisible()
  expect(screen.queryByRole('link', { name: /^「Renamed」$/ })).not.toBeInTheDocument()
  await act(async () => { await work.client.invalidateQueries({ queryKey: ['taxonomy'] }) })
  expect(work.router.state.location).toMatchObject({ pathname: '/settings/tags', state: { taxonomyNotice: 'confirmed' } })
  expect(screen.getByText(text.done, { exact: true })).toBeVisible()
})

it('leaves a known exact-name conflict editable without retrying or changing the request by itself', async () => {
  vi.mocked(api.createTag).mockRejectedValueOnce(new ApiError('TAG_NAME_CONFLICT', { status: 409 }))
  mount('/settings/tags/new')
  await editor('tags')
  fireEvent.change(field(text.tagName), { target: { value: 'Existing' } })
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: text.review }))
  await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: text.confirmCreateTag }))
  await screen.findByText(text.tagConflict)
  expect(within(screen.getByRole('alertdialog')).getByRole('button', { name: text.confirmCreateTag })).toBeDisabled()
  await user.click(screen.getByRole('button', { name: text.cancel }))
  await waitFor(() => expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument())
  expect(field(text.tagName)).toHaveValue('Existing')
  expect(api.createTag).toHaveBeenCalledTimes(1)
})

it('retains an uncertain committed deletion even after the target disappears from current reads', async () => {
  const acknowledgement = { ok: true as const, operation_id: 'frozen-delete', revision: 3 }
  vi.mocked(api.deleteTag).mockImplementationOnce(async () => { tags.delete(tag.id); throw new ApiError('INVALID_RESPONSE') }).mockResolvedValueOnce(acknowledgement)
  const user = userEvent.setup()
  mount('/settings/tags/tag/edit')
  await editor('tags')
  await user.click(screen.getByRole('button', { name: text.deleteTag }))
  await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: text.confirmDeleteTag }))
  await screen.findByRole('heading', { name: text.uncertain })
  const firstContext = vi.mocked(api.deleteTag).mock.calls[0]?.[1]
  expect(screen.getByText(firstContext!.idempotencyKey)).toBeVisible()
  await user.click(screen.getByRole('button', { name: text.cancel }))
  await waitFor(() => expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument())
  expect(field(text.tagName)).toHaveValue(tag.name)
  expect(field(text.tagName)).toBeDisabled()
  await user.click(screen.getByRole('button', { name: text.readCurrent }))
  expect(api.deleteTag).toHaveBeenCalledTimes(1)
  await user.click(screen.getByRole('button', { name: text.openOriginal }))
  await confirm(text.retryOriginal)
  expect(vi.mocked(api.deleteTag).mock.calls[1]?.[1].idempotencyKey).toBe(firstContext!.idempotencyKey)
  expect(vi.mocked(api.deleteTag).mock.calls[1]?.[1].expectedRevision).toBe(2)
})

it('keeps volatile input offline and never submits after reconnect without a foreground confirmation', async () => {
  const work = mount('/settings/tags/new')
  await editor('tags')
  fireEvent.change(field(text.tagName), { target: { value: 'Draft' } })
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: false })
  fireEvent(window, new Event('offline'))
  await waitFor(() => expect(screen.getByRole('button', { name: text.review })).toBeDisabled())
  expect(field(text.tagName)).toHaveValue('Draft')
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: true })
  fireEvent(window, new Event('online'))
  await act(async () => { await work.client.invalidateQueries({ queryKey: ['taxonomy'] }) })
  await waitFor(() => expect(screen.getByRole('button', { name: text.review })).toBeEnabled())
  expect(api.createTag).not.toHaveBeenCalled()
  expect(field(text.tagName)).toHaveValue('Draft')
})

it('does not invent empty metadata on an offline first visit or a failed live graph', async () => {
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: false })
  const work = mount()
  expect(await screen.findByText(text.offline, { exact: true })).toBeVisible()
  expect(screen.queryByRole('heading', { name: text.emptyCategories })).not.toBeInTheDocument()
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: true })
  vi.mocked(api.fetchCategoriesPage).mockResolvedValue({ data: [{ ...root, parent_id: 'missing' }] })
  fireEvent(window, new Event('online'))
  await act(async () => { await work.client.invalidateQueries({ queryKey: ['taxonomy'] }) })
  expect(await screen.findByText(text.invalidRead)).toBeVisible()
})

it('protects dirty navigation with stay/leave and never writes while opening a review or leaving', async () => {
  const user = userEvent.setup()
  mount('/settings/tags/new')
  await editor('tags')
  fireEvent.change(field(text.tagName), { target: { value: 'Draft' } })
  await user.click(screen.getByRole('link', { name: text.backTags }))
  expect(await screen.findByRole('alertdialog')).toHaveTextContent(text.leaveTitle)
  await user.click(screen.getByRole('button', { name: text.stay }))
  expect(field(text.tagName)).toHaveValue('Draft')
  await user.click(screen.getByRole('link', { name: text.backTags }))
  await user.click(screen.getByRole('button', { name: text.leave }))
  expect(await screen.findByRole('link', { name: '「Tag」' })).toBeVisible()
  expect(api.createTag).not.toHaveBeenCalled()
})

it('preserves supported imported IDs and reports missing/invalid edit targets without an unbounded spinner', async () => {
  categories.set('new', { ...root, id: 'new', name: 'Imported' })
  const work = mount('/settings/categories/new/edit')
  await editor()
  expect(field(text.categoryName)).toHaveValue('Imported')
  await act(async () => { await work.router.navigate('/settings/categories/missing/edit') })
  expect(await screen.findByRole('heading', { name: text.missing })).toBeVisible()
  await act(async () => { await work.router.navigate('/settings/categories/parent/edit?unknown=1') })
  expect(await screen.findByRole('heading', { name: text.missing })).toBeVisible()
  expect(screen.queryByText(text.loading)).not.toBeInTheDocument()
})

it.each([false, true])('redirects unauthorized settings before private reads, mandatory=%s', async (mandatory) => {
  administrator = mandatory ? { ...administrator!, must_change_password: true } : null
  mount('/settings/tags/tag/edit')
  expect(await screen.findByRole('heading', { name: mandatory ? messages.auth.passwordHeading : messages.auth.loginHeading })).toBeVisible()
  if (mandatory) expect(await screen.findByText(messages.auth.mandatory)).toBeVisible()
  expect(api.fetchTagsPage).not.toHaveBeenCalled()
})

it.each([false, true])('clears a reviewed mutation and private caches when authoritative write authorization is lost, mandatory=%s', async (mandatory) => {
  vi.mocked(api.patchTag).mockImplementationOnce(async () => {
    administrator = mandatory ? { ...administrator!, must_change_password: true } : null
    throw new ApiError(mandatory ? 'PASSWORD_CHANGE_REQUIRED' : 'AUTHENTICATION_REQUIRED', { status: mandatory ? 403 : 401 })
  })
  const work = mount('/settings/tags/tag/edit')
  await editor('tags')
  work.client.setQueryData(['private-other'], { sensitive: true })
  fireEvent.change(field(text.tagName), { target: { value: 'Draft' } })
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: text.review }))
  await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: text.confirmSave }))
  await screen.findByRole('heading', { name: mandatory ? messages.auth.passwordHeading : messages.auth.loginHeading })
  expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  expect(work.client.getQueryData(['private-other'])).toBeUndefined()
  expect(work.client.getQueryData(['taxonomy', 'tags'])).toBeUndefined()
})

it('refreshes current metadata explicitly and shows a genuine empty list', async () => {
  tags.clear()
  mount('/settings/tags')
  await screen.findByRole('heading', { name: text.emptyTags })
  await userEvent.setup().click(screen.getByRole('button', { name: text.refresh }))
  await waitFor(() => expect(api.fetchTagsPage).toHaveBeenCalledTimes(2))
  expect(api.createTag).not.toHaveBeenCalled()
})

it('shows a safe account-read failure instead of inventing an authenticated empty list', async () => {
  vi.mocked(auth.fetchAdmin).mockRejectedValue(new ApiError('SERVICE_UNAVAILABLE', { status: 503 }))
  mount('/settings/tags')
  expect(await screen.findByRole('alert')).toHaveTextContent(messages.locations.busy)
  expect(api.fetchTagsPage).not.toHaveBeenCalled()
  expect(screen.queryByRole('heading', { name: text.emptyTags })).not.toBeInTheDocument()
})

it('purges all private query data if SQL read authorization is lost after the account gate', async () => {
  vi.mocked(api.fetchCategoriesPage).mockImplementationOnce(async () => { administrator = null; throw new ApiError('AUTHENTICATION_REQUIRED', { status: 401 }) })
  const work = mount()
  work.client.setQueryData(['private-other'], { sensitive: true })
  await screen.findByRole('heading', { name: messages.auth.loginHeading })
  expect(work.client.getQueryData(['private-other'])).toBeUndefined()
  expect(work.client.getQueryData(['taxonomy', 'categories'])).toBeUndefined()
})
