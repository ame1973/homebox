import { QueryClientProvider } from '@tanstack/react-query'
import { createMemoryRouter } from 'react-router'
import { RouterProvider } from 'react-router/dom'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import App from '@/App'
import * as auth from '@/lib/api/auth'
import { ApiError } from '@/lib/api/client'
import * as api from '@/lib/api/locations'
import { createQueryClient } from '@/lib/query'
import { messages } from '@/lib/messages'

vi.mock('@/lib/api/client', async (original) => ({ ...await original<typeof import('@/lib/api/client')>(), fetchHealth: vi.fn().mockResolvedValue({ status: 'ok' }) }))
vi.mock('@/lib/api/auth', async (original) => ({ ...await original<typeof import('@/lib/api/auth')>(), fetchAdmin: vi.fn(), fetchCSRF: vi.fn(), login: vi.fn() }))
vi.mock('@/lib/api/locations', async (original) => ({ ...await original<typeof import('@/lib/api/locations')>(), fetchLocation: vi.fn(), fetchLocationsPage: vi.fn(), createLocation: vi.fn(), patchLocation: vi.fn(), moveLocation: vi.fn(), archiveLocation: vi.fn(), deleteLocation: vi.fn() }))
const text = messages.locations
const root: api.Location = { id: 'room', name: '書房', code: 'ROOM-1', revision: 3, parent_id: null, kind: 'room', sort_order: -2, description: '房間說明', status: 'active' }
const child: api.Location = { ...root, id: 'box', name: '收納箱', code: 'BOX-1', parent_id: 'room', revision: 1, kind: '防潮容器', sort_order: 0, description: '' }
let administrator: auth.Admin | null
let records: Map<string, api.Location>
const disposers: (() => void)[] = []
const field = (label: string) => screen.getByLabelText(new RegExp(`^${label}`))

beforeEach(() => {
  vi.clearAllMocks()
  administrator = { id: 'admin', revision: 2, username: 'owner', must_change_password: false }
  records = new Map([[root.id, root]])
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: true })
  vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
  vi.mocked(auth.fetchAdmin).mockReset().mockImplementation(async () => { if (!administrator) throw new ApiError('AUTHENTICATION_REQUIRED', { status: 401 }); return administrator })
  vi.mocked(auth.fetchCSRF).mockReset().mockResolvedValue('C'.repeat(43))
  vi.mocked(auth.login).mockReset().mockImplementation(async () => { administrator = { id: 'admin', revision: 2, username: 'owner', must_change_password: false }; return administrator })
  vi.mocked(api.fetchLocationsPage).mockReset().mockImplementation(async () => ({ data: Array.from(records.values()), next_cursor: null }))
  vi.mocked(api.fetchLocation).mockReset().mockImplementation(async (id) => { const record = records.get(id); if (!record) throw new ApiError('NOT_FOUND', { status: 404 }); return record })
  vi.mocked(api.createLocation).mockReset().mockImplementation(async (body) => {
    const record: api.Location = { kind: 'other', sort_order: 0, description: '', ...body, parent_id: body.parent_id ?? null, id: 'new-location', revision: 1, status: 'active' }
    records.set(record.id, record)
    return record
  })
  vi.mocked(api.patchLocation).mockReset().mockImplementation(async (id, body) => {
    const record = { ...records.get(id)!, ...body, revision: records.get(id)!.revision + 1 }
    records.set(id, record)
    return record
  })
  vi.mocked(api.moveLocation).mockReset().mockImplementation(async (id, body) => {
    const record = { ...records.get(id)!, parent_id: body.target_parent_id, revision: records.get(id)!.revision + 1 }
    records.set(id, record)
    return record
  })
  vi.mocked(api.archiveLocation).mockReset().mockImplementation(async (id) => {
    const record = { ...records.get(id)!, status: 'archived' as const, revision: records.get(id)!.revision + 1 }
    records.set(id, record)
    return record
  })
  vi.mocked(api.deleteLocation).mockReset().mockImplementation(async (id) => {
    const revision = records.get(id)!.revision + 1
    records.delete(id)
    return { ok: true, operation_id: 'delete-event', revision }
  })
})
afterEach(() => { for (const dispose of disposers.splice(0)) dispose() })

function mount(path = '/locations', state?: unknown) {
  const client = createQueryClient()
  const router = createMemoryRouter([{ path: '*', element: <App /> }], { initialEntries: [state === undefined ? path : { pathname: path, state }] })
  const view = render(<QueryClientProvider client={client}><RouterProvider router={router} /></QueryClientProvider>)
  disposers.push(() => { view.unmount(); router.dispose(); client.clear() })
  return { client, router, view }
}
async function editor(mode: 'create' | 'edit' = 'create') {
  await screen.findByLabelText(new RegExp(`^${text.name}`))
  await waitFor(() => expect(screen.getByRole('button', { name: text.review })).toBeEnabled())
  if (mode === 'create') { fireEvent.change(field(text.name), { target: { value: '文件箱' } }); fireEvent.change(field(text.code), { target: { value: 'NEW-1' } }) }
}
async function confirm(user: ReturnType<typeof userEvent.setup>, label: string) {
  const dialog = await screen.findByRole('alertdialog')
  await user.click(within(dialog).getByRole('button', { name: label }))
  await screen.findByText(text.done, { exact: true })
  await waitFor(() => expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument())
}

it('loads actual metadata and keeps hierarchy navigation read-only', async () => {
  const user = userEvent.setup()
  records.set(child.id, child)
  mount()
  await screen.findByRole('heading', { name: text.tree })
  expect(api.fetchLocationsPage).toHaveBeenCalledWith({ limit: 100 }, expect.any(AbortSignal))
  expect(screen.getByRole('link', { name: /收納箱/ })).toHaveAttribute('href', '/locations/box')
  await user.click(screen.getByRole('button', { name: text.refresh }))
  await waitFor(() => expect(api.fetchLocationsPage).toHaveBeenCalledTimes(2))
  expect(api.createLocation).not.toHaveBeenCalled()
  expect(auth.fetchCSRF).not.toHaveBeenCalled()
})

it('shows a real empty state, and does not invent one on an offline first visit', async () => {
  records.clear()
  const work = mount()
  expect(await screen.findByRole('heading', { name: text.empty })).toBeVisible()
  await act(async () => { await work.router.navigate('/locations/new') })
  await editor()
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: false })
  fireEvent(window, new Event('offline'))
  expect(screen.getByText(text.offline, { exact: true })).toBeVisible()
  expect(field(text.name)).toHaveValue('文件箱')
  await waitFor(() => expect(screen.getByRole('button', { name: text.review })).toBeDisabled())
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: true })
  fireEvent(window, new Event('online'))
  await waitFor(() => expect(screen.getByRole('button', { name: text.review })).toBeEnabled())
  expect(api.createLocation).not.toHaveBeenCalled()
  expect(field(text.name)).toHaveValue('文件箱')
})

it('does not send reads or show a perpetual loading indicator on an offline first visit', async () => {
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: false })
  mount()
  expect(await screen.findByText(text.offline, { exact: true })).toBeVisible()
  expect(screen.queryByText(text.checking)).not.toBeInTheDocument()
  expect(screen.queryByRole('heading', { name: text.empty })).not.toBeInTheDocument()
  expect(auth.fetchAdmin).not.toHaveBeenCalled()
  expect(api.fetchLocationsPage).not.toHaveBeenCalled()
})

it.each([false, true])('redirects an unauthorized or mandatory session without publishing domain data (mandatory=%s)', async (mandatory) => {
  if (mandatory) administrator = { ...administrator!, must_change_password: true }
  else administrator = null
  const { router, client } = mount('/locations/room/edit')
  client.setQueryData(['catalog', 'private'], { private: true })
  await screen.findByRole('heading', { name: mandatory ? messages.auth.passwordHeading : messages.auth.loginHeading })
  expect(router.state.location.pathname).toBe(mandatory ? '/settings/account' : '/login')
  expect(router.state.location.state.returnTo).toBe('/locations/room/edit')
  expect(api.fetchLocation).not.toHaveBeenCalled()
  expect(api.fetchLocationsPage).not.toHaveBeenCalled()
  if (!mandatory) expect(client.getQueryData(['catalog', 'private'])).toBeUndefined()
})

it('creates only after review and confirmation, preserves cancellation focus and uses no parent proof at root', async () => {
  const user = userEvent.setup()
  const { router } = mount('/locations/new')
  await editor()
  await user.click(screen.getByRole('button', { name: text.review }))
  expect(api.createLocation).not.toHaveBeenCalled()
  expect(auth.fetchCSRF).not.toHaveBeenCalled()
  await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: text.cancel }))
  await waitFor(() => expect(screen.getByRole('button', { name: text.review })).toHaveFocus())
  expect(field(text.name)).toHaveValue('文件箱')
  await user.click(screen.getByRole('button', { name: text.review }))
  await confirm(user, text.confirmCreate)
  await screen.findByRole('heading', { level: 1, name: '文件箱' })
  expect(router.state.location.pathname).toBe('/locations/new-location')
  expect(api.createLocation).toHaveBeenCalledWith({ name: '文件箱', code: 'NEW-1', kind: 'other', description: '', sort_order: 0, parent_id: null }, { csrfToken: 'C'.repeat(43), signal: expect.any(AbortSignal), idempotencyKey: expect.any(String) })
  expect(screen.getByRole('main')).toHaveFocus()
})

it('requires an explicit recheck of a changed parent instead of silently refreshing the creation proof', async () => {
  const user = userEvent.setup()
  const { client } = mount('/locations/new?parent=room')
  await editor()
  expect(field(text.parent)).toHaveValue('room')
  expect(screen.getByText(`${text.parentProof}3`)).toBeVisible()
  records.set(root.id, { ...root, revision: 4, name: '重新命名的書房' })
  await act(async () => { await client.invalidateQueries({ queryKey: ['locations', 'tree'] }) })
  await waitFor(() => expect(screen.getByRole('button', { name: text.review })).toBeDisabled())
  expect(screen.getByText(`${text.parentProof}3`)).toBeVisible()
  expect(api.createLocation).not.toHaveBeenCalled()
  await user.click(screen.getByRole('button', { name: text.reviewParent }))
  expect(screen.getByText(`${text.parentProof}4`)).toBeVisible()
  await user.click(screen.getByRole('button', { name: text.review }))
  await confirm(user, text.confirmCreate)
  expect(api.createLocation).toHaveBeenCalledWith(expect.objectContaining({ parent_id: 'room' }), expect.objectContaining({ expectedRevision: 4 }))
})

it('keeps an unavailable parent explicit until the user selects a valid destination or root', async () => {
  const user = userEvent.setup()
  mount('/locations/new?parent=missing')
  await screen.findByLabelText(new RegExp(`^${text.name}`))
  expect(field(text.parent)).toHaveValue('missing')
  expect(screen.getByRole('option', { name: text.parentUnavailable })).toBeDisabled()
  await waitFor(() => expect(screen.getByRole('button', { name: text.review })).toBeDisabled())
  expect(screen.getByRole('button', { name: text.reviewParent })).toBeDisabled()
  await user.selectOptions(field(text.parent), 'room')
  expect(screen.getByText(`${text.parentProof}3`)).toBeVisible()
  await user.selectOptions(field(text.parent), '')
  expect(screen.queryByText(`${text.parentProof}3`)).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: text.review })).toBeEnabled()
})

it('validates visible form fields before issuing any request', async () => {
  const user = userEvent.setup()
  mount('/locations/new')
  await screen.findByLabelText(new RegExp(`^${text.name}`))
  await user.click(screen.getByRole('button', { name: text.review }))
  expect(await screen.findByText(text.nameError)).toBeVisible()
  expect(field(text.name)).toHaveFocus()
  fireEvent.change(field(text.name), { target: { value: '有效名稱' } })
  fireEvent.change(field(text.code), { target: { value: 'CODE' } })
  await user.selectOptions(field(text.kind), 'custom')
  fireEvent.change(field(text.customKind), { target: { value: 'x'.repeat(33) } })
  fireEvent.change(field(text.sortOrder), { target: { value: '9007199254740992' } })
  fireEvent.change(field(text.description), { target: { value: '\ud800' } })
  await user.click(screen.getByRole('button', { name: text.review }))
  expect(await screen.findByText(text.kindError)).toBeVisible()
  expect(screen.getByText(text.sortError)).toBeVisible()
  expect(screen.getByText(text.descriptionError)).toBeVisible()
  expect(api.createLocation).not.toHaveBeenCalled()
})

it('does not submit unchanged metadata and retains original typed values for explicit reconciliation', async () => {
  const user = userEvent.setup()
  records.set(child.id, child)
  const { client } = mount('/locations/box/edit')
  await editor('edit')
  expect(field(text.kind)).toHaveValue('custom')
  expect(field(text.customKind)).toHaveValue('防潮容器')
  await user.click(screen.getByRole('button', { name: text.review }))
  expect(await screen.findByText(text.noChanges)).toBeVisible()
  expect(api.patchLocation).not.toHaveBeenCalled()
  fireEvent.change(field(text.description), { target: { value: '我的說明' } })
  fireEvent.change(field(text.sortOrder), { target: { value: '1e' } })
  records.set(child.id, { ...child, revision: 2, name: '另一個裝置命名', code: 'NEW-CODE', kind: 'cabinet', sort_order: 9 })
  await act(async () => { await client.invalidateQueries({ queryKey: ['locations'] }) })
  expect(await screen.findByText(text.changed)).toBeVisible()
  expect(field(text.name)).toHaveValue('收納箱')
  expect(field(text.sortOrder)).toHaveValue('1e')
  await user.click(screen.getByRole('button', { name: text.keepEdits }))
  expect(field(text.name)).toHaveValue('另一個裝置命名')
  expect(field(text.code)).toHaveValue('NEW-CODE')
  expect(field(text.kind)).toHaveValue('cabinet')
  expect(field(text.description)).toHaveValue('我的說明')
  expect(field(text.sortOrder)).toHaveValue('1e')
  fireEvent.change(field(text.sortOrder), { target: { value: '-4' } })
  await user.click(screen.getByRole('button', { name: text.review }))
  await confirm(user, text.confirmSave)
  expect(api.patchLocation).toHaveBeenCalledWith('box', { sort_order: -4, description: '我的說明' }, expect.objectContaining({ expectedRevision: 2 }))
})

it('discards edits only on an explicit user choice and never includes parent/status in metadata patches', async () => {
  const user = userEvent.setup()
  const { client } = mount('/locations/room/edit')
  await editor('edit')
  fireEvent.change(field(text.name), { target: { value: '未送出的名稱' } })
  records.set(root.id, { ...root, revision: 4, name: '目前名稱', kind: 'box' })
  await act(async () => { await client.invalidateQueries({ queryKey: ['locations'] }) })
  await user.click(await screen.findByRole('button', { name: text.discardEdits }))
  expect(field(text.name)).toHaveValue('目前名稱')
  expect(field(text.kind)).toHaveValue('box')
  await user.selectOptions(field(text.kind), 'other')
  await user.click(screen.getByRole('button', { name: text.review }))
  await confirm(user, text.confirmSave)
  expect(api.patchLocation).toHaveBeenCalledWith('room', { kind: 'other' }, expect.objectContaining({ expectedRevision: 4 }))
})

it('retains a dirty editor on read failure but disables writes until an explicit healthy read', async () => {
  const user = userEvent.setup()
  const { client } = mount('/locations/room/edit')
  await editor('edit')
  fireEvent.change(field(text.description), { target: { value: '保留輸入' } })
  vi.mocked(api.fetchLocationsPage).mockRejectedValueOnce(new ApiError('NETWORK_ERROR'))
  await act(async () => { await client.invalidateQueries({ queryKey: ['locations', 'tree'] }) })
  expect(await screen.findByRole('alert')).toHaveTextContent(text.network)
  expect(field(text.description)).toHaveValue('保留輸入')
  await waitFor(() => expect(screen.getByRole('button', { name: text.review })).toBeDisabled())
  await user.click(screen.getByRole('button', { name: text.refresh }))
  await waitFor(() => expect(screen.getByRole('button', { name: text.review })).toBeEnabled())
  expect(field(text.description)).toHaveValue('保留輸入')
  expect(api.patchLocation).not.toHaveBeenCalled()
})

it('fails closed on inconsistent list/detail versions without discarding an existing draft', async () => {
  vi.mocked(api.fetchLocation).mockResolvedValue({ ...root, revision: 4 })
  mount('/locations/room/edit')
  await screen.findByLabelText(new RegExp(`^${text.name}`))
  expect(await screen.findByRole('alert')).toHaveTextContent(text.invalidRead)
  await waitFor(() => expect(screen.getByRole('button', { name: text.review })).toBeDisabled())
})

it.each([new ApiError('NETWORK_ERROR'), new ApiError('INVALID_CURSOR')])('provides explicit read recovery for $code without inventing complete data', async (error) => {
  const user = userEvent.setup()
  vi.mocked(api.fetchLocationsPage).mockRejectedValueOnce(error)
  mount()
  expect(await screen.findByRole('alert')).toHaveTextContent(error.code === 'NETWORK_ERROR' ? text.network : text.invalidRead)
  expect(screen.queryByRole('heading', { name: text.empty })).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: text.refresh }))
  await screen.findByRole('heading', { name: text.tree })
  expect(api.createLocation).not.toHaveBeenCalled()
})

it.each(['/locations/missing', '/locations/tree'])('shows a missing resource rather than a fake detail for %s', async (path) => {
  mount(path)
  expect(await screen.findByRole('heading', { name: text.missing })).toBeVisible()
  expect(screen.queryByRole('button', { name: text.delete })).not.toBeInTheDocument()
})

it('blocks back/reload with a dirty form and preserves or discards it only explicitly', async () => {
  const user = userEvent.setup()
  const { router } = mount('/locations/new')
  await screen.findByLabelText(new RegExp(`^${text.name}`))
  const clean = new Event('beforeunload', { cancelable: true })
  window.dispatchEvent(clean)
  expect(clean.defaultPrevented).toBe(false)
  await editor()
  const dirty = new Event('beforeunload', { cancelable: true })
  window.dispatchEvent(dirty)
  expect(dirty.defaultPrevented).toBe(true)
  await user.click(screen.getByRole('link', { name: text.back }))
  let dialog = await screen.findByRole('alertdialog')
  expect(within(dialog).getByText(text.leaveHelp)).toBeVisible()
  await user.click(within(dialog).getByRole('button', { name: text.stay }))
  expect(router.state.location.pathname).toBe('/locations/new')
  expect(field(text.name)).toHaveValue('文件箱')
  await user.click(screen.getByRole('link', { name: text.back }))
  dialog = await screen.findByRole('alertdialog')
  await user.click(within(dialog).getByRole('button', { name: text.leave }))
  await screen.findByRole('heading', { level: 1, name: text.title })
  expect(router.state.location.pathname).toBe('/locations')
  expect(api.createLocation).not.toHaveBeenCalled()
})

it('moves only after choosing an eligible non-descendant and keeps the reviewed subject revision', async () => {
  const user = userEvent.setup()
  records.set(child.id, child)
  mount('/locations/box')
  await screen.findByRole('heading', { level: 1, name: '收納箱' })
  await user.click(screen.getByRole('button', { name: text.move }))
  expect(within(field(text.destination)).queryByRole('option', { name: /收納箱/ })).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: text.review }))
  expect(await screen.findByText(text.noChanges)).toBeVisible()
  await user.selectOptions(field(text.destination), '')
  await user.click(screen.getByRole('button', { name: text.review }))
  expect(api.moveLocation).not.toHaveBeenCalled()
  await confirm(user, text.confirmMove)
  expect(api.moveLocation).toHaveBeenCalledWith('box', { target_parent_id: null }, expect.objectContaining({ expectedRevision: 1 }))
})

it('requires explicit subtree scope and retains a panel draft when the subject changes', async () => {
  const user = userEvent.setup()
  records.set(child.id, child)
  const { client } = mount('/locations/room')
  await screen.findByRole('heading', { level: 1, name: '書房' })
  await user.click(screen.getByRole('button', { name: text.archive }))
  expect(field(text.archiveChildren)).not.toBeChecked()
  await user.click(screen.getByRole('button', { name: text.review }))
  expect(await screen.findByText(text.children)).toBeVisible()
  await user.click(field(text.archiveChildren))
  fireEvent.change(field(text.reason), { target: { value: '我的封存原因' } })
  records.set(root.id, { ...root, revision: 4 })
  await act(async () => { await client.invalidateQueries({ queryKey: ['locations'] }) })
  await waitFor(() => expect(screen.getByRole('button', { name: text.review })).toBeDisabled())
  await user.click(screen.getByRole('button', { name: text.keepEdits }))
  expect(field(text.archiveChildren)).toBeChecked()
  expect(field(text.reason)).toHaveValue('我的封存原因')
  await user.click(screen.getByRole('button', { name: text.review }))
  await confirm(user, text.confirmArchive)
  expect(api.archiveLocation).toHaveBeenCalledWith('room', { archive_children: true, reason: '我的封存原因' }, expect.objectContaining({ expectedRevision: 4 }))
})

it('does not offer restoration beneath archived ancestors and requires separate confirmation for deletion', async () => {
  const user = userEvent.setup()
  records.set(root.id, { ...root, status: 'archived' })
  records.set(child.id, { ...child, status: 'archived' })
  mount('/locations/box')
  await screen.findByRole('heading', { level: 1, name: '收納箱' })
  expect(screen.getByRole('button', { name: text.restore })).toBeDisabled()
  expect(screen.getByText(text.restoreHelp)).toBeVisible()
  await user.click(screen.getByRole('button', { name: text.delete }))
  expect(api.deleteLocation).not.toHaveBeenCalled()
  await confirm(user, text.confirmDelete)
  expect(api.deleteLocation).toHaveBeenCalledWith('box', expect.objectContaining({ expectedRevision: 1 }))
  await screen.findByRole('heading', { level: 1, name: text.title })
})

it('restores only the reviewed node and cancels unsent action panels without a request', async () => {
  const user = userEvent.setup()
  records.set(root.id, { ...root, status: 'archived' })
  mount('/locations/room')
  await screen.findByRole('heading', { level: 1, name: '書房' })
  await user.click(screen.getByRole('button', { name: text.move }))
  await user.click(screen.getByRole('button', { name: text.cancel }))
  expect(screen.queryByLabelText(text.destination)).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: text.restore }))
  await confirm(user, text.confirmRestore)
  expect(api.patchLocation).toHaveBeenCalledWith('room', { status: 'active' }, expect.objectContaining({ expectedRevision: 3 }))
  expect(screen.getByText(text.active, { exact: true })).toBeVisible()
})

it.each([false, true])('purges private location state and dirty fields on session loss (mandatory=%s)', async (mandatory) => {
  const { client, router } = mount('/locations/room/edit')
  await editor('edit')
  fireEvent.change(field(text.description), { target: { value: '不應留在登入畫面的輸入' } })
  vi.mocked(api.fetchLocation).mockRejectedValue(new ApiError(mandatory ? 'PASSWORD_CHANGE_REQUIRED' : 'AUTHENTICATION_REQUIRED', { status: mandatory ? 403 : 401 }))
  if (mandatory) administrator = { ...administrator!, must_change_password: true }
  else administrator = null
  await act(async () => { await client.invalidateQueries({ queryKey: ['locations', 'detail'] }) })
  await screen.findByRole('heading', { name: mandatory ? messages.auth.passwordHeading : messages.auth.loginHeading })
  expect(router.state.location.state.returnTo).toBe('/locations/room/edit')
  expect(client.getQueryData(['locations', 'detail', 'room'])).toBeUndefined()
  expect(screen.queryByDisplayValue('不應留在登入畫面的輸入')).not.toBeInTheDocument()
})

it('purges a proposal when confirmation discovers session loss and preserves only the safe return route', async () => {
  const user = userEvent.setup()
  const { client, router } = mount('/locations/new')
  await editor()
  vi.mocked(auth.fetchCSRF).mockRejectedValueOnce(new ApiError('AUTHENTICATION_REQUIRED', { status: 401 }))
  administrator = null
  await user.click(screen.getByRole('button', { name: text.review }))
  await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: text.confirmCreate }))
  await screen.findByRole('heading', { name: messages.auth.loginHeading })
  expect(api.createLocation).not.toHaveBeenCalled()
  expect(client.getQueryData(['locations', 'tree'])).toBeUndefined()
  expect(router.state.location.state).toEqual({ notice: 'expired', returnTo: '/locations/new' })
})

it.each(['new', 'NEW'])('keeps the supported imported ID %s distinct from collection creation', async (id) => {
  const user = userEvent.setup()
  records.clear()
  records.set(id, { ...root, id })
  const { router } = mount(`/l/${id}`)
  await screen.findByRole('heading', { level: 1, name: '書房' })
  expect(router.state.location.pathname).toBe(`/locations/${id}/view`)
  expect(screen.getByRole('link', { name: text.nav })).toHaveAttribute('aria-current', 'page')
  await user.click(screen.getByRole('link', { name: text.edit }))
  await editor('edit')
  expect(router.state.location.pathname).toBe(`/locations/${id}/edit`)
  expect(field(text.name)).toHaveValue('書房')
})

it('resumes a whitelisted protected editor after explicit login without replaying a draft', async () => {
  const user = userEvent.setup()
  administrator = null
  records.set(child.id, child)
  const { router } = mount('/login', { returnTo: '/locations/box/edit' })
  await screen.findByLabelText(new RegExp(`^${messages.auth.username}`))
  fireEvent.change(field(messages.auth.username), { target: { value: 'owner' } })
  fireEvent.change(field(messages.auth.password), { target: { value: 'explicit private login password' } })
  await user.click(screen.getByRole('button', { name: messages.auth.login }))
  await editor('edit')
  expect(router.state.location.pathname).toBe('/locations/box/edit')
  expect(field(text.name)).toHaveValue('收納箱')
  expect(api.patchLocation).not.toHaveBeenCalled()
})

it.each(['https://evil.example/locations', '//evil.example/locations', '/locations/box#secret', '/login', '/locations/new?parent=box&secret=value'])('never follows the untrusted login return target %s', async (returnTo) => {
  const user = userEvent.setup()
  administrator = null
  const { router } = mount('/login', { returnTo })
  await screen.findByLabelText(new RegExp(`^${messages.auth.username}`))
  fireEvent.change(field(messages.auth.username), { target: { value: 'owner' } })
  fireEvent.change(field(messages.auth.password), { target: { value: 'explicit private login password' } })
  await user.click(screen.getByRole('button', { name: messages.auth.login }))
  await screen.findByRole('heading', { name: messages.auth.passwordHeading })
  expect(router.state.location.pathname).toBe('/settings/account')
  expect(router.state.location.state).toBeNull()
  expect(api.fetchLocation).not.toHaveBeenCalled()
})

it('uses the alias as a relative canonical protected route and rejects reserved aliases', async () => {
  const { router } = mount('/l/room')
  await screen.findByRole('heading', { level: 1, name: '書房' })
  expect(router.state.location.pathname).toBe('/locations/room')
  await act(async () => { await router.navigate('/l/tree') })
  expect(await screen.findByRole('heading', { name: text.missing })).toBeVisible()
})
