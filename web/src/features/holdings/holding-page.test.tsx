import { QueryClientProvider } from '@tanstack/react-query'
import { createMemoryRouter } from 'react-router'
import { RouterProvider } from 'react-router/dom'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeAll, beforeEach, expect, it, vi } from 'vitest'
import App from '@/App'
import * as auth from '@/lib/api/auth'
import * as catalog from '@/lib/api/catalog'
import * as api from '@/lib/api/holdings'
import * as locations from '@/lib/api/locations'
import * as taxonomy from '@/lib/api/taxonomy'
import { ApiError } from '@/lib/api/client'
import { messages } from '@/lib/messages'
import { createQueryClient } from '@/lib/query'
import { buildLocationTree } from '@/features/locations/tree'

vi.mock('@/lib/api/auth', async (original) => ({ ...await original<typeof import('@/lib/api/auth')>(), fetchAdmin: vi.fn(), fetchCSRF: vi.fn(), login: vi.fn() }))
vi.mock('@/lib/api/catalog', async (original) => ({ ...await original<typeof import('@/lib/api/catalog')>(), fetchItem: vi.fn(), fetchItemsPage: vi.fn() }))
vi.mock('@/lib/api/holdings', async (original) => ({ ...await original<typeof import('@/lib/api/holdings')>(), fetchUnit: vi.fn(), fetchItemUnitsPage: vi.fn(), fetchStockPage: vi.fn(), createUnit: vi.fn(), patchUnit: vi.fn(), moveUnit: vi.fn(), setUnitStatus: vi.fn(), deleteUnit: vi.fn(), receiveStock: vi.fn(), consumeStock: vi.fn(), transferStock: vi.fn(), adjustStock: vi.fn() }))
vi.mock('@/lib/api/locations', async (original) => ({ ...await original<typeof import('@/lib/api/locations')>(), fetchLocationsPage: vi.fn() }))
vi.mock('@/lib/api/taxonomy', async (original) => ({ ...await original<typeof import('@/lib/api/taxonomy')>(), fetchCategoriesPage: vi.fn() }))
const text = messages.holdings
const item: catalog.Item = { id: 'item', revision: 7, name: 'Individual owner', brand: null, model: '', category_id: null, description: '', notes: '', tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1, status: 'active' }
const quantity: catalog.Item = { ...item, id: 'quantity', name: 'Quantity owner', tracking_mode: 'quantity', unit_code: 'kg', base_unit: 'g', scale_factor: 1000 }
const source: locations.Location = { id: 'source', revision: 3, name: 'Original source', code: 'SRC', kind: 'other', parent_id: null, sort_order: 0, description: '', status: 'active' }
const target: locations.Location = { ...source, id: 'target', name: 'Original target', code: 'DST' }
const unit: api.Unit = { id: 'unit', item_id: item.id, revision: 2, location_id: source.id, asset_code: 'AST-Unit', serial_number: null, notes: 'Original notes', status: 'stored' }
const balance: api.Stock = { item_id: quantity.id, location_id: source.id, revision: 2, quantity_base: 1500, unit_code: 'kg', base_unit: 'g', scale_factor: 1000 }
let administrator: auth.Admin | null
let items: Map<string, catalog.Item>, units: Map<string, api.Unit>, stock: Map<string, api.Stock>, places: Map<string, locations.Location>
const key = (itemID: string, locationID: string) => JSON.stringify([itemID, locationID])
const disposers: (() => void)[] = []
const field = (label: string) => screen.getByLabelText(new RegExp(`^${label}`))
function page<T>(rows: T[], query: { cursor?: string; limit?: number }) {
  const start = Number(query.cursor ?? 0), end = start + (query.limit ?? 100)
  return { data: rows.slice(start, end), next_cursor: rows.length > end ? String(end) : null }
}
function touchOwner(id: string) { const current = items.get(id)!; const next = { ...current, revision: current.revision + 1 }; items.set(id, next); return next }
function touchPlaces(...ids: (string | null)[]) { for (const id of new Set(ids)) if (id && places.has(id)) { const current = places.get(id)!; places.set(id, { ...current, revision: current.revision + 1 }) } }
function requireProof(current: number, proof: number) { if (current !== proof) throw new ApiError('REVISION_CONFLICT', { status: 409 }) }
function changeUnit(id: string, body: Partial<api.Unit>, proof: number) {
  const current = units.get(id)!
  requireProof(current.revision, proof)
  if (Object.entries(body).every(([key, value]) => current[key as keyof api.Unit] === value)) return current
  const next = { ...current, ...body, revision: current.revision + 1 }; units.set(id, next); touchOwner(next.item_id); touchPlaces(current.location_id, next.location_id); return next
}
function writeStock(owner: catalog.Item, location: string, count: number) {
  const previous = stock.get(key(owner.id, location))
  const next = { item_id: owner.id, location_id: location, quantity_base: count, unit_code: owner.unit_code, base_unit: owner.base_unit, scale_factor: owner.scale_factor, revision: (previous?.revision ?? 0) + 1 }
  stock.set(key(owner.id, location), next); touchPlaces(location); return next
}
beforeAll(async () => { await Promise.all([import('./holding-page'), import('@/features/catalog/catalog-page'), import('@/features/auth/account-page')]) })
beforeEach(() => {
  vi.clearAllMocks()
  administrator = { id: 'owner', username: 'owner', revision: 2, must_change_password: false }
  items = new Map([[item.id, { ...item }], [quantity.id, { ...quantity }]])
  units = new Map([[unit.id, { ...unit }]])
  stock = new Map([[key(balance.item_id, balance.location_id), { ...balance }]])
  places = new Map([[source.id, { ...source }], [target.id, { ...target }]])
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: true })
  vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
  vi.mocked(auth.fetchAdmin).mockReset().mockImplementation(async () => { if (!administrator) throw new ApiError('AUTHENTICATION_REQUIRED', { status: 401 }); return administrator })
  vi.mocked(auth.fetchCSRF).mockReset().mockResolvedValue('C'.repeat(43))
  vi.mocked(auth.login).mockReset().mockImplementation(async () => { administrator = { id: 'owner', username: 'owner', revision: 2, must_change_password: false }; return administrator })
  vi.mocked(catalog.fetchItem).mockReset().mockImplementation(async (id) => { const current = items.get(id); if (!current) throw new ApiError('NOT_FOUND', { status: 404 }); return { ...current } })
  vi.mocked(catalog.fetchItemsPage).mockReset().mockImplementation(async () => ({ data: [...items.values()], next_cursor: null }))
  vi.mocked(taxonomy.fetchCategoriesPage).mockReset().mockResolvedValue({ data: [], next_cursor: null })
  vi.mocked(locations.fetchLocationsPage).mockReset().mockImplementation(async (query) => page([...places.values()], query))
  vi.mocked(api.fetchUnit).mockReset().mockImplementation(async (id) => { const current = units.get(id); if (!current) throw new ApiError('NOT_FOUND', { status: 404 }); return { ...current } })
  vi.mocked(api.fetchItemUnitsPage).mockReset().mockImplementation(async (id, query) => page([...units.values()].filter((row) => row.item_id === id), query))
  vi.mocked(api.fetchStockPage).mockReset().mockImplementation(async (query) => page([...stock.values()].filter((row) => query.item_id === undefined || row.item_id === query.item_id), query))
  vi.mocked(api.createUnit).mockReset().mockImplementation(async (id, body, proof) => { requireProof(items.get(id)!.revision, proof.expectedRevision); const next: api.Unit = { ...unit, id: 'created', item_id: id, revision: 1, location_id: body.location_id, serial_number: body.serial_number ?? null, notes: body.notes ?? '', status: body.status ?? 'stored' }; units.set(next.id, next); touchOwner(id); touchPlaces(next.location_id); return next })
  vi.mocked(api.patchUnit).mockReset().mockImplementation(async (id, body, proof) => changeUnit(id, body, proof.expectedRevision))
  vi.mocked(api.moveUnit).mockReset().mockImplementation(async (id, body, proof) => changeUnit(id, { location_id: body.target_location_id }, proof.expectedRevision))
  vi.mocked(api.setUnitStatus).mockReset().mockImplementation(async (id, body, proof) => changeUnit(id, { status: body.status }, proof.expectedRevision))
  vi.mocked(api.deleteUnit).mockReset().mockImplementation(async (id, proof) => { const current = units.get(id)!; requireProof(current.revision, proof.expectedRevision); units.delete(id); touchOwner(current.item_id); touchPlaces(current.location_id); return { ok: true, operation_id: 'deleted', revision: current.revision + 1 } })
  vi.mocked(api.receiveStock).mockReset().mockImplementation(async (body, proof) => { const owner = items.get(body.item_id)!; requireProof(owner.revision, proof.item.revision); const result = writeStock(owner, body.target_location_id, (stock.get(key(owner.id, body.target_location_id))?.quantity_base ?? 0) + body.quantity_base); touchOwner(owner.id); return result })
  vi.mocked(api.consumeStock).mockReset().mockImplementation(async (body, proof) => { const owner = items.get(body.item_id)!; requireProof(owner.revision, proof.item.revision); const result = writeStock(owner, body.source_location_id, stock.get(key(owner.id, body.source_location_id))!.quantity_base - body.quantity_base); touchOwner(owner.id); return result })
  vi.mocked(api.transferStock).mockReset().mockImplementation(async (body, proof) => { const owner = items.get(body.item_id)!; requireProof(owner.revision, proof.item.revision); writeStock(owner, body.source_location_id, stock.get(key(owner.id, body.source_location_id))!.quantity_base - body.quantity_base); const result = writeStock(owner, body.target_location_id, (stock.get(key(owner.id, body.target_location_id))?.quantity_base ?? 0) + body.quantity_base); touchOwner(owner.id); return result })
  vi.mocked(api.adjustStock).mockReset().mockImplementation(async (body, proof) => { const owner = items.get(body.item_id)!; requireProof(owner.revision, proof.item.revision); const previous = stock.get(key(owner.id, body.target_location_id)); if (previous?.quantity_base === body.quantity_base) return previous; const result = writeStock(owner, body.target_location_id, body.quantity_base); touchOwner(owner.id); return result })
})
afterEach(() => { for (const dispose of disposers.splice(0)) dispose() })
function mount(path = '/items/item/holdings', state?: unknown, prepare?: (client: ReturnType<typeof createQueryClient>) => void) {
  const client = createQueryClient(); prepare?.(client)
  const [pathname, search = ''] = path.split('?')
  const router = createMemoryRouter([{ path: '*', element: <App /> }], { initialEntries: [{ pathname, search: search ? `?${search}` : '', state }] })
  const view = render(<QueryClientProvider client={client}><RouterProvider router={router} /></QueryClientProvider>)
  disposers.push(() => { view.unmount(); router.dispose(); client.clear() })
  return { ...view, client, router }
}
async function ready() { await waitFor(() => expect(screen.getByRole('button', { name: text.review })).toBeEnabled()) }
async function confirm(label: string) {
  const dialog = await screen.findByRole('alertdialog')
  await userEvent.setup().click(within(dialog).getByRole('button', { name: label }))
  await screen.findByText(text.done, { exact: true })
  await waitFor(() => expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument())
}
async function refresh(work: ReturnType<typeof mount>) { await act(async () => { await Promise.all([work.client.invalidateQueries({ queryKey: ['holdings'] }), work.client.invalidateQueries({ queryKey: ['locations'] })]) }) }
function fill(label: string, value: string) { fireEvent.change(field(label), { target: { value } }) }
function select(label: string, value: string) { fireEvent.change(screen.getByRole('combobox', { name: label }), { target: { value } }) }

it('completes all 205 Unit pages and retains imported new identifiers and scoped catalog returns', async () => {
  units = new Map(Array.from({ length: 205 }, (_, index) => [`unit-${index}`, { ...unit, id: `unit-${index}`, asset_code: `AST-${index}` }]))
  mount('/items/item/holdings', { catalogList: '/items?status=archived', catalogCursors: [null, 'opaque'] })
  expect(await screen.findByText(`${text.unitCount}205`)).toBeVisible()
  expect(api.fetchItemUnitsPage).toHaveBeenCalledTimes(3)
  expect(screen.getByRole('link', { name: messages.catalog.back })).toHaveAttribute('href', '/items?status=archived')
  expect(screen.getByRole('link', { name: text.createUnit })).toHaveAttribute('href', '/items/item/units/new')
  fill(text.search, 'AST-204')
  expect(screen.getByRole('link', { name: '"AST-204"' })).toHaveAttribute('href', '/units/unit-204')
  expect(auth.fetchCSRF).not.toHaveBeenCalled()
})
it('reads only complete selected-Item Stock with exact measurement and zero rows', async () => {
  stock = new Map(Array.from({ length: 205 }, (_, index) => { const row = { ...balance, location_id: `place-${index}`, quantity_base: index }; places.set(row.location_id, { ...source, id: row.location_id, code: `L-${index}` }); return [key(row.item_id, row.location_id), row] }))
  mount('/items/quantity/holdings')
  expect(await screen.findByText(`${text.stockCount}205`)).toBeVisible()
  expect(api.fetchStockPage).toHaveBeenCalledTimes(3)
  expect(api.fetchStockPage).toHaveBeenCalledWith({ item_id: quantity.id, limit: 100 }, expect.any(AbortSignal))
  expect(api.fetchItemUnitsPage).not.toHaveBeenCalled()
  expect(screen.getByText(text.retainedZero)).toBeVisible()
})
it('exposes a real empty Stock set and an explicit new-zero adjustment route', async () => {
  stock.clear(); mount('/items/quantity/holdings')
  expect(await screen.findByRole('heading', { name: text.emptyStock })).toBeVisible()
  expect(screen.getByRole('link', { name: text.actions.adjust })).toHaveAttribute('href', '/items/quantity/stock/adjust')
})
it('creates a nullable-location Unit only after review with the original Item proof', async () => {
  mount('/items/item/units/new'); await ready()
  const user = userEvent.setup()
  await user.click(screen.getByRole('checkbox', { name: text.providedSerial }))
  fill(text.fields.serial_number, '')
  fill(text.fields.notes, '  Exact Unit é\n  ')
  await user.click(screen.getByRole('button', { name: text.review }))
  expect(api.createUnit).not.toHaveBeenCalled()
  expect(within(screen.getByRole('alertdialog')).getByRole('heading', { name: text.reviewTitle })).toHaveFocus()
  await confirm(text.confirmCreate)
  expect(api.createUnit).toHaveBeenCalledWith(item.id, { location_id: null, serial_number: '', notes: '  Exact Unit é\n  ', status: 'stored' }, expect.objectContaining({ expectedRevision: 7, idempotencyKey: expect.any(String) }))
})
it('edits only explicit Unit metadata under the independent proof and keeps unchanged edits local', async () => {
  mount('/units/unit/edit'); await ready()
  await userEvent.setup().click(screen.getByRole('button', { name: text.review }))
  expect(await screen.findByText(text.noChanges)).toBeVisible()
  expect(api.patchUnit).not.toHaveBeenCalled()
  fill(text.fields.notes, '')
  await userEvent.setup().click(screen.getByRole('button', { name: text.review }))
  await confirm(text.confirmSave)
  expect(api.patchUnit).toHaveBeenCalledWith(unit.id, { notes: '' }, expect.objectContaining({ expectedRevision: 2, itemID: item.id }))
})
it('moves only after a full active path choice and keeps same-placement no-ops local', async () => {
  mount('/units/unit/move'); await ready()
  await userEvent.setup().click(screen.getByRole('button', { name: text.review }))
  expect(await screen.findByText(text.noChanges)).toBeVisible()
  select(text.fields.target_location_id, target.id)
  await userEvent.setup().click(screen.getByRole('button', { name: text.review }))
  await confirm(text.confirmMove)
  expect(api.moveUnit).toHaveBeenCalledWith(unit.id, { target_location_id: target.id }, expect.objectContaining({ expectedRevision: 2 }))
})
it('keeps unchanged status local, requires disposal kind and sends no financial fields', async () => {
  mount('/units/unit/status'); await ready()
  await userEvent.setup().click(screen.getByRole('button', { name: text.review }))
  expect(await screen.findByText(text.noChanges)).toBeVisible()
  select(text.fields.status, 'disposed')
  await userEvent.setup().click(screen.getByRole('button', { name: text.review }))
  expect(screen.getByRole('combobox', { name: text.fields.disposal_kind })).toHaveFocus()
  expect(api.setUnitStatus).not.toHaveBeenCalled()
  select(text.fields.disposal_kind, 'recycled'); fill(text.fields.note, 'Physical note')
  await userEvent.setup().click(screen.getByRole('button', { name: text.review }))
  await confirm(text.confirmStatus)
  expect(api.setUnitStatus).toHaveBeenCalledWith(unit.id, { status: 'disposed', disposal_kind: 'recycled', note: 'Physical note' }, expect.objectContaining({ expectedRevision: 2 }))
})
it('deletes the reviewed Unit independently, never treating deletion as financial disposal', async () => {
  mount('/units/unit')
  await userEvent.setup().click(await screen.findByRole('button', { name: text.deleteUnit }))
  expect(screen.getByRole('alertdialog')).toHaveTextContent(text.deleteHelp)
  await confirm(text.confirmDelete)
  expect(api.deleteUnit).toHaveBeenCalledWith(unit.id, expect.objectContaining({ expectedRevision: 2, itemID: item.id }))
  expect(items.has(item.id)).toBe(true)
})
it.each(['receive', 'consume', 'transfer', 'adjust'] as const)('reviews %s with exact raw counts and the full original Item rather than a Stock proof', async (action) => {
  mount(`/items/quantity/stock/${action}?location=source`); await ready()
  if (action === 'transfer') select(text.fields.target_location_id, target.id)
  fill(text.fields.quantity_base, action === 'adjust' ? '0' : '1000')
  if (action === 'adjust') fill(text.fields.reason, '  Counted  ')
  fill(text.fields.note, '  Exact note  ')
  await userEvent.setup().click(screen.getByRole('button', { name: text.review }))
  expect(screen.getByRole('alertdialog')).toHaveTextContent('"g"')
  await confirm(text.confirms[action])
  const method = action === 'receive' ? api.receiveStock : action === 'consume' ? api.consumeStock : action === 'transfer' ? api.transferStock : api.adjustStock
  expect(method).toHaveBeenCalledWith(expect.objectContaining({ item_id: quantity.id, quantity_base: action === 'adjust' ? 0 : 1000, note: '  Exact note  ' }), expect.objectContaining({ item: quantity, idempotencyKey: expect.any(String) }))
  expect(vi.mocked(method).mock.calls[0]?.[1]).not.toHaveProperty('expectedRevision')
})
it('validates decimal/UTF/presence at the actual form fields without coercion or dispatch', async () => {
  mount('/items/quantity/stock/adjust'); await ready()
  fill(text.fields.quantity_base, '1e3')
  await userEvent.setup().click(screen.getByRole('button', { name: text.review }))
  expect(screen.getByRole('combobox', { name: text.fields.target_location_id })).toHaveFocus()
  select(text.fields.target_location_id, target.id)
  await userEvent.setup().click(screen.getByRole('button', { name: text.review }))
  expect(field(text.fields.quantity_base)).toHaveFocus()
  fill(text.fields.quantity_base, '0'); fill(text.fields.reason, '')
  await userEvent.setup().click(screen.getByRole('button', { name: text.review }))
  expect(field(text.fields.reason)).toHaveFocus()
  expect(api.adjustStock).not.toHaveBeenCalled()
})
it.each([true, false])('adopts a new Stock baseline only explicitly and preserves raw input only when selected, keep=%s', async (keep) => {
  const work = mount('/items/quantity/stock/adjust?location=source'); await ready()
  fill(text.fields.quantity_base, 'invalid raw draft'); fill(text.fields.reason, 'Local reason')
  stock.set(key(quantity.id, source.id), { ...balance, quantity_base: 2000, revision: 3 }); touchOwner(quantity.id)
  await refresh(work)
  expect(await screen.findByText(text.changed, { exact: true })).toBeVisible()
  expect(field(text.fields.quantity_base)).toHaveValue('invalid raw draft')
  await userEvent.setup().click(screen.getByRole('button', { name: keep ? text.keepEdits : text.discardEdits }))
  expect(field(text.fields.quantity_base)).toHaveValue(keep ? 'invalid raw draft' : '2000')
  expect(api.adjustStock).not.toHaveBeenCalled()
})
it('requires acknowledgement of the complete changed measurement without converting the retained count', async () => {
  stock.clear(); const work = mount('/items/quantity/stock/receive?location=target'); await ready()
  fill(text.fields.quantity_base, '1000')
  items.set(quantity.id, { ...quantity, revision: 8, unit_code: 'L', base_unit: 'ml' })
  await refresh(work)
  expect(await screen.findByText(text.measurementChanged)).toBeVisible()
  expect(screen.getByRole('button', { name: text.keepEdits })).toBeDisabled()
  await userEvent.setup().click(screen.getByRole('checkbox', { name: text.acknowledgeMeasurement }))
  await userEvent.setup().click(screen.getByRole('button', { name: text.keepEdits }))
  expect(field(text.fields.quantity_base)).toHaveValue('1000')
  await userEvent.setup().click(screen.getByRole('button', { name: text.review }))
  await confirm(text.confirms.receive)
  expect(api.receiveStock).toHaveBeenCalledWith(expect.objectContaining({ quantity_base: 1000 }), expect.objectContaining({ item: expect.objectContaining({ revision: 8, unit_code: 'L', base_unit: 'ml', scale_factor: 1000 }) }))
})
it.each([true, false])('adopts changed Unit data explicitly with logical nullable serial edits, keep=%s', async (keep) => {
  const work = mount('/units/unit/edit'); await ready()
  const user = userEvent.setup(); await user.click(screen.getByRole('checkbox', { name: text.providedSerial }))
  fill(text.fields.serial_number, ''); fill(text.fields.notes, '')
  units.set(unit.id, { ...unit, revision: 3, serial_number: 'Concurrent serial', notes: 'Concurrent notes' }); touchOwner(item.id)
  await refresh(work)
  expect(await screen.findByText(text.changed, { exact: true })).toBeVisible()
  await user.click(screen.getByRole('button', { name: keep ? text.keepEdits : text.discardEdits }))
  expect(field(text.fields.serial_number)).toHaveValue(keep ? '' : 'Concurrent serial')
  expect(field(text.fields.notes)).toHaveValue(keep ? '' : 'Concurrent notes')
  expect(api.patchUnit).not.toHaveBeenCalled()
})
it('rechecks a changed path explicitly while preserving Unit creation input and original Item proof', async () => {
  const work = mount('/items/item/units/new'); await ready()
  select(text.fields.location_id, target.id); fill(text.fields.notes, 'Local text')
  places.set(target.id, { ...target, revision: 4, name: 'Changed path' })
  await refresh(work)
  expect(await screen.findByText(text.pathChanged, { exact: true })).toBeVisible()
  expect(screen.getByRole('button', { name: text.review })).toBeDisabled()
  await userEvent.setup().click(screen.getByRole('button', { name: text.recheckPaths }))
  expect(field(text.fields.notes)).toHaveValue('Local text')
  await userEvent.setup().click(screen.getByRole('button', { name: text.review }))
  await confirm(text.confirmCreate)
  expect(api.createUnit).toHaveBeenCalledWith(item.id, expect.objectContaining({ location_id: target.id, notes: 'Local text' }), expect.objectContaining({ expectedRevision: 7 }))
})
it('rechecks Stock paths and preserves amount/reason text without refreshing its Item proof', async () => {
  const work = mount('/items/quantity/stock/receive?location=target'); await ready()
  fill(text.fields.quantity_base, '1000')
  places.set(target.id, { ...target, revision: 4, name: 'Changed stock destination' })
  await refresh(work)
  await userEvent.setup().click(await screen.findByRole('button', { name: text.recheckPaths }))
  expect(field(text.fields.quantity_base)).toHaveValue('1000')
  await userEvent.setup().click(screen.getByRole('button', { name: text.review }))
  await confirm(text.confirms.receive)
  expect(api.receiveStock).toHaveBeenCalledWith(expect.anything(), expect.objectContaining({ item: quantity }))
})
it('retains a removed Unit and its original deletion command through explicit original-key reconciliation', async () => {
  vi.mocked(api.deleteUnit).mockImplementationOnce(async () => { units.delete(unit.id); touchOwner(item.id); throw new ApiError('INVALID_RESPONSE') }).mockResolvedValueOnce({ ok: true, operation_id: 'frozen', revision: 3 })
  mount('/units/unit')
  await userEvent.setup().click(await screen.findByRole('button', { name: text.deleteUnit }))
  await userEvent.setup().click(within(screen.getByRole('alertdialog')).getByRole('button', { name: text.confirmDelete }))
  await screen.findByRole('heading', { name: text.uncertain })
  const original = vi.mocked(api.deleteUnit).mock.calls[0]![1]
  await userEvent.setup().click(within(screen.getByRole('alertdialog')).getByRole('button', { name: text.cancel }))
  expect(await screen.findByText(text.missing, { exact: true })).toBeVisible()
  await userEvent.setup().click(screen.getByRole('button', { name: text.openOriginal }))
  await confirm(text.retryOriginal)
  expect(api.deleteUnit).toHaveBeenNthCalledWith(2, unit.id, expect.objectContaining({ idempotencyKey: original.idempotencyKey, expectedRevision: original.expectedRevision, itemID: original.itemID }))
})
it('retains unknown Stock body/proof despite current zero counts and never replays on reconnect', async () => {
  vi.mocked(api.consumeStock).mockImplementationOnce(async () => { stock.set(key(quantity.id, source.id), { ...balance, quantity_base: 0, revision: 3 }); touchOwner(quantity.id); throw new ApiError('INVALID_RESPONSE') }).mockResolvedValueOnce({ ...balance, quantity_base: 0, revision: 3 })
  mount('/items/quantity/stock/consume?location=source'); await ready()
  fill(text.fields.quantity_base, '1500')
  await userEvent.setup().click(screen.getByRole('button', { name: text.review }))
  await userEvent.setup().click(within(screen.getByRole('alertdialog')).getByRole('button', { name: text.confirms.consume }))
  await screen.findByRole('heading', { name: text.uncertain })
  const original = vi.mocked(api.consumeStock).mock.calls[0]![1]
  await act(async () => { window.dispatchEvent(new Event('online')) })
  expect(api.consumeStock).toHaveBeenCalledTimes(1)
  await confirm(text.retryOriginal)
  expect(api.consumeStock).toHaveBeenNthCalledWith(2, expect.objectContaining({ quantity_base: 1500 }), expect.objectContaining({ item: quantity, idempotencyKey: original.idempotencyKey }))
})
it('preserves initialized offline input and never pauses/replays an inventory write', async () => {
  mount('/items/quantity/stock/receive?location=target'); await ready()
  fill(text.fields.quantity_base, '1000')
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: false }); await act(async () => { window.dispatchEvent(new Event('offline')) })
  expect(screen.getByText(text.offline, { exact: true })).toBeVisible()
  expect(screen.getByRole('button', { name: text.review })).toBeDisabled()
  fill(text.fields.quantity_base, '2000')
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: true }); await act(async () => { window.dispatchEvent(new Event('online')) })
  await ready(); expect(field(text.fields.quantity_base)).toHaveValue('2000'); expect(api.receiveStock).not.toHaveBeenCalled()
})
it.each([null, { id: 'owner', username: 'owner', revision: 2, must_change_password: true }])('protects holding deep links before private reads, account=%j', async (account) => {
  administrator = account
  const work = mount('/items/quantity/stock/receive?location=target')
  await waitFor(() => expect(work.router.state.location.pathname).toBe(account ? '/settings/account' : '/login'))
  expect(api.fetchStockPage).not.toHaveBeenCalled()
  expect(api.receiveStock).not.toHaveBeenCalled()
  expect(work.router.state.location.state.returnTo).toBe('/items/quantity/stock/receive?location=target')
})
it('explains missing and wrong-tracking source without inventing empty data or discarding an existing draft', async () => {
  const work = mount('/items/quantity/stock/receive?location=target'); await ready()
  fill(text.fields.quantity_base, '1000'); stock.clear(); items.set(quantity.id, { ...quantity, revision: 8, tracking_mode: 'individual' })
  await refresh(work)
  expect(await screen.findByText(text.wrongMode)).toBeVisible()
  expect(field(text.fields.quantity_base)).toHaveValue('1000')
  expect(screen.getByRole('button', { name: text.review })).toBeDisabled()
  expect(api.receiveStock).not.toHaveBeenCalled()
})
it('waits for the first current read before initializing a newly mounted editor from cache', async () => {
  let release: ((unit: api.Unit) => void) | undefined
  const fresh = { ...unit, revision: 3, notes: 'Fresh after mount' }
  units.set(unit.id, fresh)
  vi.mocked(api.fetchUnit).mockImplementationOnce(() => new Promise((resolve) => { release = resolve }))
  mount('/units/unit/edit', undefined, (client) => { client.setQueryData(['auth', 'me'], administrator); client.setQueryData(['holdings', 'unit', unit.id], { kind: 'unit', item, unit }); client.setQueryData(['locations', 'tree'], buildLocationTree([...places.values()])) })
  expect(screen.queryByRole('button', { name: text.review })).not.toBeInTheDocument()
  await act(async () => { release?.(fresh) })
  await ready(); expect(field(text.fields.notes)).toHaveValue(fresh.notes)
})
it('shows guarded disposed Unit state without offering movement to another location', async () => {
  units.set(unit.id, { ...unit, status: 'disposed' }); mount('/u/unit')
  expect(await screen.findByRole('button', { name: text.moveUnit })).toBeDisabled()
  expect(screen.getByText(text.disposedHelp)).toBeVisible()
  expect(api.moveUnit).not.toHaveBeenCalled()
})
it('does not add an Item-counter precondition to an unchanged independent Unit edit', async () => {
  const work = mount('/units/unit/edit'); await ready()
  fill(text.fields.notes, 'Independent local text')
  touchOwner(item.id)
  await refresh(work)
  expect(screen.queryByText(text.changed, { exact: true })).not.toBeInTheDocument()
  await userEvent.setup().click(screen.getByRole('button', { name: text.review }))
  await confirm(text.confirmSave)
  expect(api.patchUnit).toHaveBeenCalledWith(unit.id, { notes: 'Independent local text' }, expect.objectContaining({ expectedRevision: 2, itemID: item.id }))
})
it.each([true, false])('adopts a changed Unit-creation Item only explicitly, keep=%s', async (keep) => {
  const work = mount('/items/item/units/new'); await ready()
  fill(text.fields.notes, 'Local creation text')
  items.set(item.id, { ...item, revision: 8, name: 'Changed owner' })
  await refresh(work)
  await userEvent.setup().click(await screen.findByRole('button', { name: keep ? text.keepEdits : text.discardEdits }))
  expect(field(text.fields.notes)).toHaveValue(keep ? 'Local creation text' : '')
  await userEvent.setup().click(screen.getByRole('button', { name: text.review }))
  await confirm(text.confirmCreate)
  expect(api.createUnit).toHaveBeenCalledWith(item.id, expect.objectContaining({ notes: keep ? 'Local creation text' : '' }), expect.objectContaining({ expectedRevision: 8 }))
})
it('adopts an untouched move target from current placement rather than silently moving a Unit back', async () => {
  const work = mount('/units/unit/move'); await ready()
  units.set(unit.id, { ...unit, revision: 3, location_id: target.id }); touchOwner(item.id)
  await refresh(work)
  await userEvent.setup().click(await screen.findByRole('button', { name: text.keepEdits }))
  expect(screen.getByRole('combobox', { name: text.fields.target_location_id })).toHaveValue(target.id)
  await userEvent.setup().click(screen.getByRole('button', { name: text.review }))
  expect(await screen.findByText(text.noChanges)).toBeVisible()
  expect(api.moveUnit).not.toHaveBeenCalled()
})
it('retains the whole edited lifecycle interpretation when adopting a new Unit proof', async () => {
  const work = mount('/units/unit/status'); await ready()
  select(text.fields.status, 'disposed'); select(text.fields.disposal_kind, 'donated'); fill(text.fields.note, 'Local lifecycle note')
  units.set(unit.id, { ...unit, revision: 3, status: 'lent' }); touchOwner(item.id)
  await refresh(work)
  await userEvent.setup().click(await screen.findByRole('button', { name: text.keepEdits }))
  expect(screen.getByRole('combobox', { name: text.fields.status })).toHaveValue('disposed')
  expect(screen.getByRole('combobox', { name: text.fields.disposal_kind })).toHaveValue('donated')
  await userEvent.setup().click(screen.getByRole('button', { name: text.review }))
  await confirm(text.confirmStatus)
  expect(api.setUnitStatus).toHaveBeenCalledWith(unit.id, { status: 'disposed', disposal_kind: 'donated', note: 'Local lifecycle note' }, expect.objectContaining({ expectedRevision: 3 }))
})
it('adopts an untouched operational status and keeps only the actual local note', async () => {
  const work = mount('/units/unit/status'); await ready()
  fill(text.fields.note, 'Retained note')
  units.set(unit.id, { ...unit, revision: 3, status: 'retired' }); touchOwner(item.id)
  await refresh(work)
  await userEvent.setup().click(await screen.findByRole('button', { name: text.keepEdits }))
  expect(screen.getByRole('combobox', { name: text.fields.status })).toHaveValue('retired')
  expect(field(text.fields.note)).toHaveValue('Retained note')
})
it('keeps invalid serial/notes and initial states at their exact field boundaries', async () => {
  mount('/items/item/units/new'); await ready()
  await userEvent.setup().click(screen.getByRole('checkbox', { name: text.providedSerial }))
  fill(text.fields.serial_number, '\uD800')
  await userEvent.setup().click(screen.getByRole('button', { name: text.review }))
  expect(field(text.fields.serial_number)).toHaveFocus()
  fill(text.fields.serial_number, ''); fill(text.fields.notes, '😀'.repeat(10001))
  await userEvent.setup().click(screen.getByRole('button', { name: text.review }))
  expect(field(text.fields.notes)).toHaveFocus()
  expect(api.createUnit).not.toHaveBeenCalled()
})
it('withholds a Unit write when its retained physical path is missing', async () => {
  places.delete(source.id); mount('/units/unit/edit')
  await screen.findByText(text.editHelp)
  expect(screen.getByRole('button', { name: text.review })).toBeDisabled()
  expect(screen.getByRole('button', { name: text.recheckPaths })).toBeDisabled()
  expect(api.patchUnit).not.toHaveBeenCalled()
})
it('rejects insufficient Stock locally without adjusting the reviewed amount', async () => {
  mount('/items/quantity/stock/consume?location=source'); await ready()
  fill(text.fields.quantity_base, '1501')
  await userEvent.setup().click(screen.getByRole('button', { name: text.review }))
  expect(await screen.findByText(text.insufficient)).toBeVisible()
  expect(field(text.fields.quantity_base)).toHaveValue('1501')
  expect(api.consumeStock).not.toHaveBeenCalled()
})
it('rejects target overflow and archived increases without inventing a converted quantity', async () => {
  stock.set(key(quantity.id, target.id), { ...balance, location_id: target.id, quantity_base: Number.MAX_SAFE_INTEGER })
  mount('/items/quantity/stock/receive?location=target'); await ready()
  fill(text.fields.quantity_base, '1')
  await userEvent.setup().click(screen.getByRole('button', { name: text.review }))
  expect(await screen.findByText(text.overflow)).toBeVisible()
  expect(api.receiveStock).not.toHaveBeenCalled()
})
it('allows an archived retained count to decrease but rejects increasing it before review', async () => {
  places.set(source.id, { ...source, status: 'archived' }); mount('/items/quantity/stock/adjust?location=source'); await ready()
  fill(text.fields.quantity_base, '1501'); fill(text.fields.reason, 'Explicit count')
  await userEvent.setup().click(screen.getByRole('button', { name: text.review }))
  expect(await screen.findByText(text.archivedError)).toBeVisible()
  expect(api.adjustStock).not.toHaveBeenCalled()
  fill(text.fields.quantity_base, '0')
  await userEvent.setup().click(screen.getByRole('button', { name: text.review }))
  await confirm(text.confirms.adjust)
  expect(api.adjustStock).toHaveBeenCalledWith(expect.objectContaining({ quantity_base: 0 }), expect.anything())
})
it.each(['/units/missing', '/items/missing/holdings'])('shows missing source %s rather than a synthetic empty collection', async (path) => {
  mount(path)
  expect(await screen.findByRole('heading', { name: text.missing })).toBeVisible()
  expect(screen.queryByRole('heading', { name: text.emptyUnits })).not.toBeInTheDocument()
})
it.each(['unit', 'locations', 'auth'] as const)('exposes a safe fatal %s read failure without a write', async (kind) => {
  if (kind === 'unit') vi.mocked(api.fetchUnit).mockRejectedValue(new ApiError('SERVICE_UNAVAILABLE', { status: 503 }))
  else if (kind === 'locations') vi.mocked(locations.fetchLocationsPage).mockRejectedValue(new ApiError('INVALID_RESPONSE'))
  else vi.mocked(auth.fetchAdmin).mockRejectedValue(new ApiError('NETWORK_ERROR'))
  mount('/units/unit')
  expect(await screen.findByRole('alert')).toBeVisible()
  expect(api.deleteUnit).not.toHaveBeenCalled()
})
it.each([false, true])('clears volatile proposal and returns safely after mutation session loss, mandatory=%s', async (mandatory) => {
  vi.mocked(api.receiveStock).mockRejectedValueOnce(new ApiError(mandatory ? 'PASSWORD_CHANGE_REQUIRED' : 'AUTHENTICATION_REQUIRED', { status: mandatory ? 403 : 401 }))
  const work = mount('/items/quantity/stock/receive?location=target'); await ready()
  fill(text.fields.quantity_base, '1000')
  await userEvent.setup().click(screen.getByRole('button', { name: text.review }))
  await userEvent.setup().click(within(screen.getByRole('alertdialog')).getByRole('button', { name: text.confirms.receive }))
  await waitFor(() => expect(work.router.state.location.pathname).toBe(mandatory ? '/settings/account' : '/login'))
  expect(work.client.getQueryData(['holdings', 'item', quantity.id])).toBeUndefined()
  expect(work.router.state.location.state.returnTo).toBe('/items/quantity/stock/receive?location=target')
})
it('redirects on transactional read revocation even when the account read initially succeeded', async () => {
  vi.mocked(api.fetchUnit).mockRejectedValue(new ApiError('AUTHENTICATION_REQUIRED', { status: 401 }))
  const work = mount('/units/unit/edit')
  await waitFor(() => expect(work.router.state.location.pathname).toBe('/login'))
  expect(work.router.state.location.state.returnTo).toBe('/units/unit/edit')
})
it('does not initialize a new offline editor from a stale cache or publish a draft queue', async () => {
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: false })
  mount('/items/quantity/stock/receive', undefined, (client) => { client.setQueryData(['auth', 'me'], administrator); client.setQueryData(['holdings', 'item', quantity.id], { kind: 'stock', item: quantity, stock: [balance] }); client.setQueryData(['locations', 'tree'], buildLocationTree([...places.values()])) })
  expect(await screen.findByText(text.offline, { exact: true })).toBeVisible()
  expect(screen.queryByRole('button', { name: text.review })).not.toBeInTheDocument()
  expect(api.receiveStock).not.toHaveBeenCalled()
})
it.each([true, false])('preserves only a validated catalog return across holding login, valid scope=%s', async (validScope) => {
  administrator = null
  const catalogList = validScope ? '/items?status=archived&limit=1&cursor=opaque' : 'https://evil.example/private'
  const work = mount('/units/unit/edit', { catalogList, catalogCursors: validScope ? [null, 'visited'] : ['\uD800'], quantity_base: 'private draft must not travel' })
  await waitFor(() => expect(work.router.state.location.pathname).toBe('/login'))
  expect(work.router.state.location.state).not.toHaveProperty('quantity_base')
  await screen.findByRole('heading', { name: messages.auth.loginTitle })
  fireEvent.change(field(messages.auth.username), { target: { value: 'owner' } })
  fireEvent.change(field(messages.auth.password), { target: { value: 'explicit login password' } })
  await userEvent.setup().click(screen.getByRole('button', { name: messages.auth.login }))
  await ready()
  expect(work.router.state.location.pathname).toBe('/units/unit/edit')
  expect(screen.getByRole('link', { name: messages.catalog.back })).toHaveAttribute('href', validScope ? catalogList : '/items')
  expect(work.router.state.location.state.catalogCursors).toEqual(validScope ? [null, 'visited'] : [])
})
it('carries validated holding return context through an already-authenticated login redirect', async () => {
  const work = mount('/login', { returnTo: '/items/quantity/holdings', catalogList: '/items?status=active', catalogCursors: ['visited'] })
  await waitFor(() => expect(work.router.state.location.pathname).toBe('/items/quantity/holdings'))
  expect(await screen.findByRole('link', { name: messages.catalog.back })).toHaveAttribute('href', '/items?status=active')
  expect(work.router.state.location.state.catalogCursors).toEqual(['visited'])
})
it.each(['/items/item/holdings?unknown=1', '/units/unit?quantity=1'])('rejects invalid holding URL %s without reading or writing private source', async (path) => {
  mount(path)
  expect(await screen.findByRole('alert')).toBeVisible()
  expect(api.fetchUnit).not.toHaveBeenCalled()
  expect(api.fetchItemUnitsPage).not.toHaveBeenCalled()
})
