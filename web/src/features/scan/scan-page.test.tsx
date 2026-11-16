import { QueryClientProvider } from '@tanstack/react-query'
import { createMemoryRouter } from 'react-router'
import { RouterProvider } from 'react-router/dom'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeAll, beforeEach, expect, it, vi } from 'vitest'
import App from '@/App'
import * as auth from '@/lib/api/auth'
import type { Item } from '@/lib/api/catalog'
import { ApiError } from '@/lib/api/client'
import { messages } from '@/lib/messages'
import { createQueryClient } from '@/lib/query'
import { notifySessionCleared } from '@/lib/session-lifecycle'
import * as model from './barcode-model'

vi.mock('@/lib/api/auth', async (original) => ({ ...await original<typeof import('@/lib/api/auth')>(), fetchAdmin: vi.fn() }))
vi.mock('./barcode-model', async (original) => ({ ...await original<typeof import('./barcode-model')>(), readBarcodeChoices: vi.fn() }))

const text = messages.scan, catalog = messages.catalog
const item = (id: string, name: string, status: Item['status'] = 'active'): Item => ({ id, name, status, revision: 3, tracking_mode: 'individual', unit_code: 'pcs', category_id: null,
  brand: null, model: '', description: '', notes: '', base_unit: 'pcs', scale_factor: 1 })
const choice = (barcodeID: string, owner: Item, value = '036000291452') => ({ barcode: { barcode_id: barcodeID, item_id: owner.id, normalized_value: value, symbology: 'UPC_A' as const }, item: owner })
const disposers: (() => void)[] = []
const owner = item('item', 'First item'), legacy = item('legacy/item\0😀', 'Archived item', 'archived')

beforeAll(async () => { await Promise.all([import('./scan-page'), import('@/features/auth/account-page')]) })
beforeEach(() => {
  vi.clearAllMocks()
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: true })
  vi.mocked(auth.fetchAdmin).mockReset().mockResolvedValue({ id: 'admin', username: 'owner', must_change_password: false, revision: 2 })
  vi.mocked(model.readBarcodeChoices).mockReset().mockResolvedValue({ matches: [], next_cursor: null })
})
afterEach(() => { for (const dispose of disposers.splice(0)) dispose() })
function mount(path = '/scan') {
  const client = createQueryClient(), url = new URL(path, 'http://homebox.test')
  const router = createMemoryRouter([{ path: '*', element: <App /> }], { initialEntries: [{ pathname: url.pathname, search: url.search, hash: url.hash }] })
  const view = render(<QueryClientProvider client={client}><RouterProvider router={router} /></QueryClientProvider>)
  disposers.push(() => { view.unmount(); router.dispose(); client.clear() })
  return { client, router }
}
async function ready() {
  const input = await screen.findByRole('textbox', { name: text.input })
  await waitFor(() => expect(screen.getByRole('button', { name: text.search })).toBeEnabled())
  return input
}
async function search(value = '036000291452') {
  fireEvent.change(await ready(), { target: { value } })
  await userEvent.setup().click(screen.getByRole('button', { name: text.search }))
}

it('shows actual shared Item choices, including archived and path-unaddressable owners, without writes', async () => {
  vi.mocked(model.readBarcodeChoices).mockResolvedValueOnce({ matches: [choice('barcode-1', owner), choice('barcode-2', legacy), choice('barcode-3', owner)], next_cursor: null })
  mount()
  const input = await ready()
  expect(model.readBarcodeChoices).not.toHaveBeenCalled()
  fireEvent.change(input, { target: { value: '036000291452' } })
  await userEvent.setup().selectOptions(screen.getByRole('combobox', { name: text.symbology }), 'UPC_A')
  await userEvent.setup().click(screen.getByRole('button', { name: text.search }))
  const legacyHeading = await screen.findByRole('heading', { name: JSON.stringify(legacy.name) })
  expect(legacyHeading).toBeVisible()
  expect(model.readBarcodeChoices).toHaveBeenCalledExactlyOnceWith({ value: '036000291452', symbology: 'UPC_A' }, expect.any(AbortSignal))
  const results = screen.getByRole('list', { name: text.results })
  expect(within(results).getAllByRole('listitem')).toHaveLength(3)
  expect(screen.getByText(`${catalog.pageCount}3`)).toHaveAttribute('role', 'status')
  expect(within(results).getAllByRole('link', { name: /開啟物品資料/ })).toHaveLength(2)
  expect(within(results).getAllByRole('link', { name: /開啟物品資料/ })[0]).toHaveAttribute('href', '/items/item')
  expect(within(results).getByText(text.unavailableItem)).toBeVisible()
  expect(legacyHeading.closest('li')).toHaveTextContent(catalog.archived)
  expect(legacyHeading.closest('li')).toHaveTextContent(JSON.stringify(legacy.id))
  expect(screen.getByText(text.cameraUnavailable)).toBeVisible()
})

it('preserves JSON-escaped controls and shows empty results without inventing inventory', async () => {
  mount()
  const input = await ready(), user = userEvent.setup()
  await user.selectOptions(screen.getByRole('combobox', { name: text.inputFormat }), 'json')
  fireEvent.change(input, { target: { value: '"\\u0000é\\r\\n001"' } })
  await user.click(screen.getByRole('button', { name: text.search }))
  expect(await screen.findByRole('heading', { name: text.empty })).toBeVisible()
  expect(model.readBarcodeChoices).toHaveBeenCalledExactlyOnceWith({ value: '\0é\r\n001' }, expect.any(AbortSignal))
  expect(screen.getByText(text.createHelp)).toBeVisible()
  expect(screen.getByRole('link', { name: text.create })).toHaveAttribute('href', '/items/new')
  expect(screen.getByRole('region', { name: text.results })).toHaveTextContent(JSON.stringify('\0é\r\n001'))
})

it('rejects invalid text and JSON without querying and focuses the field', async () => {
  mount()
  const input = await ready(), user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: text.search }))
  expect(screen.getByText(text.invalid)).toBeVisible(); expect(input).toHaveFocus()
  await user.selectOptions(screen.getByRole('combobox', { name: text.inputFormat }), 'json')
  for (const raw of ['123', '"\\ud800"', '{"value":"001"}', '""', '"broken']) {
    fireEvent.change(input, { target: { value: raw } })
    await user.click(screen.getByRole('button', { name: text.search }))
    expect(screen.getByText(text.invalid)).toBeVisible()
  }
  expect(model.readBarcodeChoices).not.toHaveBeenCalled()
})

it('pages only on explicit actions and rejects a repeated identity across live pages', async () => {
  const first = Array.from({ length: 5 }, (_, index) => choice(`barcode-${index}`, item(`item-${index}`, `Item ${index}`)))
  vi.mocked(model.readBarcodeChoices).mockResolvedValueOnce({ matches: first, next_cursor: 'next' })
    .mockResolvedValueOnce({ matches: [first[0]!], next_cursor: null })
    .mockResolvedValueOnce({ matches: first, next_cursor: 'next' })
  mount(); await search()
  await screen.findByRole('heading', { name: JSON.stringify('Item 0') })
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: catalog.nextPage }))
  expect(await screen.findByText(text.changed)).toBeVisible()
  expect(screen.queryByRole('list', { name: text.results })).not.toBeInTheDocument()
  expect(model.readBarcodeChoices).toHaveBeenNthCalledWith(2, { value: '036000291452', cursor: 'next' }, expect.any(AbortSignal))
  await user.click(screen.getByRole('button', { name: text.reset }))
  expect(await screen.findByRole('heading', { name: JSON.stringify('Item 0') })).toBeVisible()
  expect(model.readBarcodeChoices).toHaveBeenCalledTimes(3)
})

it('rejects a cyclic continuation without offering the page for selection', async () => {
  const first = Array.from({ length: 5 }, (_, index) => choice(`first-${index}`, item(`first-${index}`, `First ${index}`)))
  const second = Array.from({ length: 5 }, (_, index) => choice(`second-${index}`, item(`second-${index}`, `Second ${index}`)))
  vi.mocked(model.readBarcodeChoices).mockResolvedValueOnce({ matches: first, next_cursor: 'cycle' }).mockResolvedValueOnce({ matches: second, next_cursor: 'cycle' })
  mount(); await search()
  await screen.findByRole('heading', { name: JSON.stringify('First 0') })
  await userEvent.setup().click(screen.getByRole('button', { name: catalog.nextPage }))
  expect(await screen.findByText(text.changed)).toBeVisible()
  expect(screen.queryByRole('list', { name: text.results })).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: catalog.nextPage })).toBeDisabled()
})

it('does not call an empty continuation page an entirely empty search', async () => {
  const first = Array.from({ length: 5 }, (_, index) => choice(`barcode-${index}`, item(`item-${index}`, `Item ${index}`)))
  vi.mocked(model.readBarcodeChoices).mockResolvedValueOnce({ matches: first, next_cursor: 'next' }).mockResolvedValueOnce({ matches: [], next_cursor: null })
  mount(); await search()
  await screen.findByRole('heading', { name: JSON.stringify('Item 0') })
  await userEvent.setup().click(screen.getByRole('button', { name: catalog.nextPage }))
  expect(await screen.findByRole('heading', { name: text.emptyPage })).toBeVisible()
  expect(screen.queryByRole('heading', { name: text.empty })).not.toBeInTheDocument()
  expect(screen.queryByRole('link', { name: text.create })).not.toBeInTheDocument()
})

it('discards late results after the input changes and cancels the old read', async () => {
  let finish: ((value: model.BarcodeChoices) => void) | undefined, observed: AbortSignal | undefined
  vi.mocked(model.readBarcodeChoices).mockImplementationOnce((_query, signal) => {
    observed = signal
    return new Promise((resolve) => { finish = resolve })
  }).mockResolvedValueOnce({ matches: [], next_cursor: null })
  mount(); await search('old')
  await waitFor(() => expect(finish).toBeDefined())
  fireEvent.change(screen.getByRole('textbox', { name: text.input }), { target: { value: 'new' } })
  expect(observed?.aborted).toBe(true)
  await act(async () => { finish?.({ matches: [choice('late', owner, 'old')], next_cursor: null }) })
  expect(screen.queryByRole('heading', { name: JSON.stringify(owner.name) })).not.toBeInTheDocument()
  await userEvent.setup().click(screen.getByRole('button', { name: text.search }))
  expect(await screen.findByRole('heading', { name: text.empty })).toBeVisible()
  expect(model.readBarcodeChoices).toHaveBeenLastCalledWith({ value: 'new' }, expect.any(AbortSignal))
})

it('withholds failed reads, retains input and requires an explicit retry', async () => {
  vi.mocked(model.readBarcodeChoices).mockRejectedValueOnce(new ApiError('RATE_LIMITED', { status: 429, retryAfterSeconds: 20 }))
    .mockResolvedValueOnce({ matches: [], next_cursor: null })
  mount(); await search('001')
  expect(await screen.findByText(messages.locations.rate)).toBeVisible()
  expect(screen.getByText(`${messages.auth.seconds}20`)).toBeVisible()
  expect(screen.getByRole('textbox', { name: text.input })).toHaveValue('001')
  expect(screen.queryByRole('heading', { name: text.empty })).not.toBeInTheDocument()
  await userEvent.setup().click(screen.getByRole('button', { name: text.refresh }))
  expect(await screen.findByRole('heading', { name: text.empty })).toBeVisible()
  expect(model.readBarcodeChoices).toHaveBeenCalledTimes(2)
})

it('does not retry a failed lookup when the session is revalidated', async () => {
  vi.mocked(model.readBarcodeChoices).mockRejectedValueOnce(new ApiError('NETWORK_ERROR'))
  const work = mount(); await search('001')
  await screen.findByText(messages.locations.network)
  let finish: ((admin: auth.Admin) => void) | undefined
  vi.mocked(auth.fetchAdmin).mockImplementationOnce(() => new Promise((resolve) => { finish = resolve }))
  act(() => { void work.client.invalidateQueries({ queryKey: ['auth', 'me'] }) })
  await waitFor(() => expect(finish).toBeDefined())
  await act(async () => { finish?.({ id: 'admin', username: 'owner', must_change_password: false, revision: 2 }) })
  await waitFor(() => expect(screen.getByRole('button', { name: text.search })).toBeEnabled())
  expect(model.readBarcodeChoices).toHaveBeenCalledTimes(1)
  expect(screen.getByText(messages.locations.network)).toBeVisible()
})

it('clears displayed results offline and does not automatically search again on reconnect', async () => {
  vi.mocked(model.readBarcodeChoices).mockResolvedValue({ matches: [choice('barcode', owner)], next_cursor: null })
  mount(); await search()
  await screen.findByRole('heading', { name: JSON.stringify(owner.name) })
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: false })
  act(() => { window.dispatchEvent(new Event('offline')) })
  expect(await screen.findByText(text.offlineHelp)).toBeVisible()
  expect(screen.queryByText(JSON.stringify(owner.name))).not.toBeInTheDocument()
  expect(screen.getByRole('textbox', { name: text.input })).toHaveValue('036000291452')
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: true })
  act(() => { window.dispatchEvent(new Event('online')) })
  await waitFor(() => expect(screen.getByRole('button', { name: text.search })).toBeEnabled())
  expect(model.readBarcodeChoices).toHaveBeenCalledTimes(1)
  await userEvent.setup().click(screen.getByRole('button', { name: text.search }))
  expect(await screen.findByRole('heading', { name: JSON.stringify(owner.name) })).toBeVisible()
  expect(model.readBarcodeChoices).toHaveBeenCalledTimes(2)
})

it('clears the current query and input when a session-clear signal arrives', async () => {
  vi.mocked(model.readBarcodeChoices).mockResolvedValue({ matches: [choice('barcode', owner)], next_cursor: null })
  const { client } = mount(); await search()
  await screen.findByRole('heading', { name: JSON.stringify(owner.name) })
  act(() => { notifySessionCleared() })
  expect(await screen.findByText(text.sessionClearedHelp)).toBeVisible()
  expect(screen.queryByText(JSON.stringify(owner.name))).not.toBeInTheDocument()
  expect(screen.queryByRole('textbox', { name: text.input })).not.toBeInTheDocument()
  const remaining = client.getQueryCache().findAll({ queryKey: ['catalog', 'barcode-choices'] })
  expect(remaining.every((query) => query.state.data === undefined && !JSON.stringify(query.queryKey).includes('036000291452'))).toBe(true)
  expect(screen.getByRole('link', { name: text.signIn })).toHaveAttribute('href', '/login')
})

it('does not install a late response after a session-clear signal', async () => {
  let finish: ((value: model.BarcodeChoices) => void) | undefined, observed: AbortSignal | undefined
  vi.mocked(model.readBarcodeChoices).mockImplementationOnce((_query, signal) => {
    observed = signal
    return new Promise((resolve) => { finish = resolve })
  })
  mount(); await search('private-value')
  await waitFor(() => expect(finish).toBeDefined())
  act(() => { notifySessionCleared() })
  expect(observed?.aborted).toBe(true)
  await act(async () => { finish?.({ matches: [choice('late', owner, 'private-value')], next_cursor: null }) })
  expect(await screen.findByText(text.sessionClearedHelp)).toBeVisible()
  expect(screen.queryByRole('heading', { name: JSON.stringify(owner.name) })).not.toBeInTheDocument()
  expect(screen.queryByRole('textbox', { name: text.input })).not.toBeInTheDocument()
})

it('redirects an expired search session using only the parameter-free safe return', async () => {
  vi.mocked(model.readBarcodeChoices).mockRejectedValueOnce(new ApiError('AUTHENTICATION_REQUIRED', { status: 401 }))
  const work = mount(); await search('private-value')
  await waitFor(() => expect(work.router.state.location.pathname).toBe('/login'))
  expect(work.router.state.location.state).toEqual({ notice: 'expired', returnTo: '/scan' })
  expect(JSON.stringify(work.router.state.location.state)).not.toContain('private-value')
})

it('routes a mandatory password-change session to the account page without querying', async () => {
  vi.mocked(auth.fetchAdmin).mockResolvedValue({ id: 'admin', username: 'owner', must_change_password: true, revision: 2 })
  const work = mount()
  await waitFor(() => expect(work.router.state.location.pathname).toBe('/settings/account'))
  expect(work.router.state.location.state).toEqual({ returnTo: '/scan' })
  expect(model.readBarcodeChoices).not.toHaveBeenCalled()
})

it('rejects query-bearing scan routes rather than reflecting their contents', async () => {
  mount('/scan?value=private-value')
  expect(await screen.findByText(text.invalidRoute)).toBeVisible()
  expect(screen.queryByRole('textbox', { name: text.input })).not.toBeInTheDocument()
  expect(screen.getByRole('link', { name: text.title })).toHaveAttribute('href', '/scan')
  expect(model.readBarcodeChoices).not.toHaveBeenCalled()
})
