import { QueryClientProvider } from '@tanstack/react-query'
import { createMemoryRouter } from 'react-router'
import { RouterProvider } from 'react-router/dom'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeAll, beforeEach, expect, it, vi } from 'vitest'
import App from '@/App'
import * as auth from '@/lib/api/auth'
import * as catalog from '@/lib/api/catalog'
import * as api from '@/lib/api/purchases'
import * as holdings from '@/lib/api/holdings'
import * as locations from '@/lib/api/locations'
import * as taxonomy from '@/lib/api/taxonomy'
import { ApiError } from '@/lib/api/client'
import { messages } from '@/lib/messages'
import { createQueryClient } from '@/lib/query'

vi.mock('@/lib/api/auth', async (original) => ({ ...await original<typeof import('@/lib/api/auth')>(), fetchAdmin: vi.fn(), fetchCSRF: vi.fn(), login: vi.fn() }))
vi.mock('@/lib/api/catalog', async (original) => ({ ...await original<typeof import('@/lib/api/catalog')>(), fetchItem: vi.fn(), fetchItemsPage: vi.fn() }))
vi.mock('@/lib/api/purchases', async (original) => ({ ...await original<typeof import('@/lib/api/purchases')>(), fetchPurchase: vi.fn(), fetchPurchasesPage: vi.fn(), fetchItemPurchasesPage: vi.fn(), fetchPurchaseLinesPage: vi.fn(), createPurchase: vi.fn(), patchPurchase: vi.fn(), createPurchaseLine: vi.fn() }))
vi.mock('@/lib/api/holdings', async (original) => ({ ...await original<typeof import('@/lib/api/holdings')>(), fetchItemUnitsPage: vi.fn(), fetchStockPage: vi.fn() }))
vi.mock('@/lib/api/locations', async (original) => ({ ...await original<typeof import('@/lib/api/locations')>(), fetchLocationsPage: vi.fn() }))
vi.mock('@/lib/api/taxonomy', async (original) => ({ ...await original<typeof import('@/lib/api/taxonomy')>(), fetchCategoriesPage: vi.fn() }))
const text = messages.purchases, cat = messages.catalog
const owner: auth.Admin = { id: 'owner', username: 'owner', revision: 2, must_change_password: false }
const purchase: api.Purchase = { id: 'purchase', revision: 12, merchant_name: 'Original merchant', purchased_at: '2026-11-07T10:00:00.123456789+08:00', currency: 'JPY', receipt_note: 'Original receipt' }
const item: catalog.Item = { id: 'item', revision: 7, name: 'Individual owner', brand: null, model: '', category_id: null, description: '', notes: '', tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1, status: 'active' }
const quantity: catalog.Item = { ...item, id: 'quantity', name: 'Quantity owner', tracking_mode: 'quantity', unit_code: 'kg', base_unit: 'g', scale_factor: 1000 }
const unit: holdings.Unit = { id: 'unit', item_id: item.id, revision: 8, location_id: null, asset_code: 'AST-Unit', serial_number: null, notes: 'Original Unit notes', status: 'stored' }
const line: api.PurchaseLine = { id: 'line', revision: 9, purchase_id: purchase.id, item_id: item.id, item_unit_id: unit.id, quantity_base: 1, paid_minor: 0, note: 'Retained allocation' }
let administrator: auth.Admin | null, purchases: Map<string, api.Purchase>, items: Map<string, catalog.Item>, units: Map<string, holdings.Unit>, lines: Map<string, api.PurchaseLine>, receipts: Map<string, api.Purchase | api.PurchaseLine>
const disposers: (() => void)[] = []
const field = (label: string) => screen.getByLabelText(new RegExp(`^${label.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}`))
function fill(label: string, value: string) { fireEvent.change(field(label), { target: { value } }) }
function select(label: string, value: string) { fireEvent.change(screen.getByRole('combobox', { name: new RegExp(`^${label}`) }), { target: { value } }) }
function page<T>(rows: T[], query: { cursor?: string; limit?: number }) { const start = Number(query.cursor ?? 0), end = start + (query.limit ?? 50); return { data: rows.slice(start, end), next_cursor: rows.length > end ? String(end) : null } }
function proof(expected: number, actual: number) { if (expected !== actual) throw new ApiError('REVISION_CONFLICT', { status: 409 }) }
function touchPurchase(changes: Partial<api.Purchase> = {}) { const current = purchases.get(purchase.id)!; const next = { ...current, ...changes, revision: current.revision + 1 }; purchases.set(next.id, next); return next }
function touchItem(id: string, changes: Partial<catalog.Item> = {}) { const current = items.get(id)!; const next = { ...current, ...changes, revision: current.revision + 1 }; items.set(id, next); return next }
function remember<T extends api.Purchase | api.PurchaseLine>(key: string, next: T): T { receipts.set(key, structuredClone(next)); return next }

beforeAll(async () => { await Promise.all([import('./purchase-page'), import('@/features/catalog/catalog-page'), import('@/features/auth/account-page')]) })
beforeEach(() => {
  vi.clearAllMocks(); administrator = { ...owner }; purchases = new Map([[purchase.id, { ...purchase }]]); items = new Map([[item.id, { ...item }], [quantity.id, { ...quantity }]]); units = new Map([[unit.id, { ...unit }]]); lines = new Map(); receipts = new Map()
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: true }); vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
  vi.mocked(auth.fetchAdmin).mockReset().mockImplementation(async () => { if (!administrator) throw new ApiError('AUTHENTICATION_REQUIRED', { status: 401 }); return administrator })
  vi.mocked(auth.fetchCSRF).mockReset().mockResolvedValue('C'.repeat(43))
  vi.mocked(auth.login).mockReset().mockImplementation(async () => { administrator = { ...owner }; return administrator })
  vi.mocked(catalog.fetchItem).mockReset().mockImplementation(async (id) => { const current = items.get(id); if (!current) throw new ApiError('NOT_FOUND', { status: 404 }); return { ...current } })
  vi.mocked(catalog.fetchItemsPage).mockReset().mockImplementation(async (query) => page([...items.values()].filter((row) => !query.q || row.name.includes(query.q)), query))
  vi.mocked(taxonomy.fetchCategoriesPage).mockReset().mockResolvedValue({ data: [], next_cursor: null })
  vi.mocked(locations.fetchLocationsPage).mockReset().mockResolvedValue({ data: [], next_cursor: null })
  vi.mocked(holdings.fetchItemUnitsPage).mockReset().mockImplementation(async (id, query) => page([...units.values()].filter((unit) => unit.item_id === id), query))
  vi.mocked(holdings.fetchStockPage).mockReset().mockResolvedValue({ data: [], next_cursor: null })
  vi.mocked(api.fetchPurchase).mockReset().mockImplementation(async (id) => { const current = purchases.get(id); if (!current) throw new ApiError('NOT_FOUND', { status: 404 }); return { ...current } })
  vi.mocked(api.fetchPurchasesPage).mockReset().mockImplementation(async (query) => page([...purchases.values()].filter((row) => (!query.merchant || row.merchant_name === query.merchant) && (!query.currency || row.currency === query.currency)), query))
  vi.mocked(api.fetchItemPurchasesPage).mockReset().mockImplementation(async (id, query) => page([...purchases.values()].filter((purchase) => [...lines.values()].some((line) => line.item_id === id && line.purchase_id === purchase.id)), query))
  vi.mocked(api.fetchPurchaseLinesPage).mockReset().mockImplementation(async (id, query) => page([...lines.values()].filter((line) => line.purchase_id === id), query))
  vi.mocked(api.createPurchase).mockReset().mockImplementation(async (body, context) => {
    const frozen = receipts.get(context.idempotencyKey); if (frozen) return frozen as api.Purchase
    const next = { ...purchase, ...body, receipt_note: body.receipt_note ?? '', id: 'created', revision: 1 }; purchases.set(next.id, next); return remember(context.idempotencyKey, next)
  })
  vi.mocked(api.patchPurchase).mockReset().mockImplementation(async (id, body, context) => {
    const frozen = receipts.get(context.idempotencyKey); if (frozen) return frozen as api.Purchase
    const current = purchases.get(id)!; proof(current.revision, context.purchase.revision)
    if (body.currency !== undefined && body.currency !== current.currency && [...lines.values()].some((line) => line.purchase_id === id)) throw new ApiError('PURCHASE_CURRENCY_LOCKED', { status: 409 })
    const next = { ...current, ...body, revision: current.revision + 1 }; purchases.set(id, next); return remember(context.idempotencyKey, next)
  })
  vi.mocked(api.createPurchaseLine).mockReset().mockImplementation(async (id, body, context) => {
    const frozen = receipts.get(context.idempotencyKey); if (frozen) return frozen as api.PurchaseLine
    proof(purchases.get(id)!.revision, context.purchase.revision); proof(items.get(body.item_id)!.revision, body.item_revision)
    const next = { ...body, note: body.note ?? '', id: `created-line-${lines.size}`, revision: 1, purchase_id: id }; lines.set(next.id, next); touchPurchase(); touchItem(body.item_id); return remember(context.idempotencyKey, next)
  })
})
afterEach(() => { for (const dispose of disposers.splice(0)) dispose() })
function mount(path = '/purchases', state?: unknown, prepare?: (client: ReturnType<typeof createQueryClient>) => void) {
  const client = createQueryClient(); prepare?.(client)
  const [pathname, search = ''] = path.split('?'), router = createMemoryRouter([{ path: '*', element: <App /> }], { initialEntries: [{ pathname, search: search ? `?${search}` : '', state }] })
  const view = render(<QueryClientProvider client={client}><RouterProvider router={router} /></QueryClientProvider>)
  disposers.push(() => { view.unmount(); router.dispose(); client.clear() }); return { ...view, client, router }
}
const click = (name: string) => userEvent.setup().click(screen.getByRole('button', { name }))
async function ready() { await waitFor(() => expect(screen.getByRole('button', { name: text.review })).toBeEnabled()) }
async function confirm(label: string) { const dialog = await screen.findByRole('alertdialog'); await userEvent.setup().click(within(dialog).getByRole('button', { name: label })); await screen.findByText(text.done, { exact: true }); await waitFor(() => expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()) }
async function refresh(work: ReturnType<typeof mount>) { await act(async () => { await work.client.invalidateQueries({ queryKey: ['purchases'] }) }) }
function fillHeader() { fill(text.fields.merchant_name, '  Exact new merchant é 😀  '); fill(text.fields.purchased_at, purchase.purchased_at); fill(text.fields.currency, 'JPY'); fill(text.fields.receipt_note, '  Exact\nreceipt  ') }
function fillLine(named = false) { select(text.fields.allocation, named ? 'unit' : 'none'); if (named) select(text.fields.item_unit_id, unit.id); fill(text.fields.quantity_base, named ? '1' : '1500'); fill(text.fields.paid_minor, '123'); fill(text.fields.note, '  Exact allocation é\n  ') }

it('exposes the real global list and all filter criteria without uncontracted text search or duplicate totals', async () => {
  mount('/purchases?currency=JPY&limit=1', { purchaseCursors: [null] })
  expect(await screen.findByRole('link', { name: JSON.stringify(purchase.merchant_name) })).toHaveAttribute('href', '/purchases/purchase')
  expect(api.fetchPurchasesPage).toHaveBeenCalledWith({ currency: 'JPY', limit: 1 }, expect.any(AbortSignal))
  expect(screen.queryByLabelText(cat.search)).not.toBeInTheDocument()
  await userEvent.setup().click(screen.getByText(cat.advanced, { exact: true }))
  for (const label of [cat.from, cat.to, cat.currency, cat.merchant, cat.pageSize]) expect(field(label)).toBeVisible()
  expect(screen.getByRole('button', { name: cat.previousPage })).toBeEnabled()
  expect(auth.fetchCSRF).not.toHaveBeenCalled(); expect(api.fetchPurchaseLinesPage).not.toHaveBeenCalled()
})
it('applies exact server filters and resets page scope only after explicit apply', async () => {
  const work = mount(); await screen.findByText(JSON.stringify(purchase.merchant_name))
  await userEvent.setup().click(screen.getByText(cat.advanced, { exact: true }))
  fill(cat.from, '2026-01-01'); fill(cat.to, '2026-11-07'); fill(cat.merchant, purchase.merchant_name); fill(cat.currency, 'JPY'); fill(cat.pageSize, '1')
  await click(cat.apply)
  await waitFor(() => expect(work.router.state.location.search).toContain('from=2026-01-01'))
  expect(new URLSearchParams(work.router.state.location.search).get('merchant')).toBe(purchase.merchant_name)
  await waitFor(() => expect(api.fetchPurchasesPage).toHaveBeenCalledWith(expect.objectContaining({ from: '2026-01-01', to: '2026-11-07', limit: 1 }), expect.any(AbortSignal)))
  await waitFor(() => expect(screen.getByRole('button', { name: cat.reset })).toBeEnabled()); await click(cat.reset); await waitFor(() => expect(work.router.state.location.search).toBe(''))
})
it('sends the exact multiline merchant filter instead of native single-line normalization', async () => {
  const work = mount(); await screen.findByRole('link', { name: JSON.stringify(purchase.merchant_name) }); await userEvent.setup().click(screen.getByText(cat.advanced, { exact: true }))
  const merchant = '  Exact\nmerchant é 😀  '; fill(cat.merchant, merchant); expect(field(cat.merchant)).toHaveValue(merchant); await click(cat.apply)
  await waitFor(() => expect(api.fetchPurchasesPage).toHaveBeenCalledWith(expect.objectContaining({ merchant }), expect.any(AbortSignal)))
  expect(new URLSearchParams(work.router.state.location.search).get('merchant')).toBe(merchant)
})
it('rejects invalid actual filter text with localized field focus before navigation', async () => {
  const work = mount(); await screen.findByText(JSON.stringify(purchase.merchant_name)); await userEvent.setup().click(screen.getByText(cat.advanced, { exact: true }))
  fill(cat.pageSize, '01'); await click(cat.apply)
  expect(field(cat.pageSize)).toHaveFocus(); expect(work.router.state.location.search).toBe('')
})
it('retains global filters through explicit next/previous/first pages', async () => {
  purchases.set('second', { ...purchase, id: 'second' }); const work = mount('/purchases?currency=JPY&limit=1')
  await waitFor(() => expect(screen.getByRole('button', { name: cat.nextPage })).toBeEnabled()); await click(cat.nextPage)
  await screen.findByRole('link', { name: JSON.stringify(purchase.merchant_name) })
  expect(new URLSearchParams(work.router.state.location.search).get('cursor')).toBe('1')
  await click(cat.previousPage); await waitFor(() => expect(new URLSearchParams(work.router.state.location.search).has('cursor')).toBe(false))
  await waitFor(() => expect(screen.getByRole('button', { name: cat.nextPage })).toBeEnabled()); await click(cat.nextPage); await waitFor(() => expect(screen.getByRole('button', { name: cat.firstPage })).toBeEnabled()); await click(cat.firstPage)
  expect(new URLSearchParams(work.router.state.location.search).get('currency')).toBe('JPY')
})
it('withholds repeated continuations and offers recovery for an invalid purchase URL', async () => {
  vi.mocked(api.fetchPurchasesPage).mockResolvedValue({ data: [purchase], next_cursor: 'old' }); const work = mount('/purchases?limit=1', { purchaseCursors: ['old'] })
  expect(await screen.findByText(text.invalidRead)).toBeVisible(); expect(screen.queryByRole('link', { name: JSON.stringify(purchase.merchant_name) })).not.toBeInTheDocument()
  await act(async () => { await work.router.navigate('/purchases?paid_minor=100') })
  expect(await screen.findByText(messages.locations.validation)).toBeVisible(); expect(screen.getByRole('link', { name: cat.firstPage })).toHaveAttribute('href', '/purchases')
})
it('creates an exact header only after focused review without defaulting instant or currency', async () => {
  mount('/purchases/new'); await ready()
  expect(field(text.fields.purchased_at)).toHaveValue(''); expect(field(text.fields.currency)).toHaveValue('')
  await click(text.review); expect(field(text.fields.merchant_name)).toHaveFocus(); expect(api.createPurchase).not.toHaveBeenCalled()
  fillHeader(); await click(text.review)
  expect(within(screen.getByRole('alertdialog')).getByRole('heading', { name: text.reviewTitle })).toHaveFocus()
  expect(screen.getByRole('alertdialog')).toHaveTextContent(purchase.purchased_at); expect(api.createPurchase).not.toHaveBeenCalled()
  await confirm(text.confirmCreate)
  expect(api.createPurchase).toHaveBeenCalledWith({ merchant_name: '  Exact new merchant é 😀  ', purchased_at: purchase.purchased_at, currency: 'JPY', receipt_note: '  Exact\nreceipt  ' }, expect.objectContaining({ idempotencyKey: expect.any(String) }))
  expect(holdings.fetchStockPage).not.toHaveBeenCalled()
})
it('preserves multiline merchant names through actual creation controls and untouched descriptive correction', async () => {
  mount('/purchases/new'); await ready(); fillHeader(); const name = '  Merchant\nsecond line é 😀  '; fill(text.fields.merchant_name, name)
  expect(field(text.fields.merchant_name)).toHaveValue(name); await click(text.review); await confirm(text.confirmCreate)
  expect(vi.mocked(api.createPurchase).mock.calls[0]?.[0].merchant_name).toBe(name)
  await userEvent.setup().click(await screen.findByRole('link', { name: text.edit })); await ready()
  expect(field(text.fields.merchant_name)).toHaveValue(name); fill(text.fields.receipt_note, 'Only this note changes'); await click(text.review); await confirm(text.confirmSave)
  expect(api.patchPurchase).toHaveBeenCalledWith('created', { receipt_note: 'Only this note changes' }, expect.objectContaining({ purchase: expect.objectContaining({ merchant_name: name }) }))
})
it('reads all 205 independent Lines between matching Purchase reads without fetching their unrelated holdings', async () => {
  lines = new Map(Array.from({ length: 205 }, (_, index) => [`line-${index}`, { ...line, id: `line-${index}` }]))
  mount('/purchases/purchase'); expect(await screen.findByText(`${text.lineCount}205`)).toBeVisible()
  expect(api.fetchPurchaseLinesPage).toHaveBeenCalledTimes(3); expect(api.fetchPurchase).toHaveBeenCalledTimes(2)
  expect(catalog.fetchItem).not.toHaveBeenCalled(); expect(holdings.fetchItemUnitsPage).not.toHaveBeenCalled(); expect(holdings.fetchStockPage).not.toHaveBeenCalled()
})
it('completes Item-associated headers and preserves safe imported IDs and catalog scope', async () => {
  lines.set(line.id, line); purchases.set('new', { ...purchase, id: 'new' }); lines.set('second', { ...line, id: 'second', purchase_id: 'new' })
  mount('/items/item/purchases', { catalogList: '/items?status=active&limit=1', catalogCursors: [null, 'opaque'] })
  expect(await screen.findByText(`${text.associatedCount}2`)).toBeVisible()
  expect(screen.getAllByRole('link', { name: JSON.stringify(purchase.merchant_name) })[1]).toHaveAttribute('href', '/purchases/new/view')
  expect(screen.getByRole('link', { name: cat.back })).toHaveAttribute('href', '/items?status=active&limit=1')
  expect(api.fetchPurchaseLinesPage).not.toHaveBeenCalled(); expect(api.fetchPurchase).not.toHaveBeenCalled()
})
it('paginates real Item discovery with catalog filters, then reads only the explicitly selected complete allocation context', async () => {
  const work = mount('/purchases/purchase/allocate?limit=1')
  await screen.findByRole('link', { name: text.chooseItem }); expect(catalog.fetchItem).not.toHaveBeenCalled()
  await click(cat.nextPage); await waitFor(() => expect(work.router.state.location.search).toContain('cursor=1'))
  await userEvent.setup().click(await screen.findByRole('link', { name: text.chooseItem })); await ready()
  expect(work.router.state.location.pathname).toBe('/purchases/purchase/allocate/quantity')
  expect(catalog.fetchItem).toHaveBeenCalledWith(quantity.id, expect.any(AbortSignal)); expect(holdings.fetchItemUnitsPage).not.toHaveBeenCalled(); expect(holdings.fetchStockPage).not.toHaveBeenCalled()
})
it('reports inconsistent selection metadata instead of allowing an unchecked category interpretation', async () => {
  items.set(item.id, { ...item, category_id: 'missing' }); mount('/purchases/purchase/allocate')
  expect(await screen.findByText(text.invalidRead)).toBeVisible(); expect(screen.getAllByRole('button', { name: text.chooseItem }).every((button) => (button as HTMLButtonElement).disabled)).toBe(true)
})
it.each([false, true])('allocates exact whole money with explicit null or actual Unit intent and original dual proofs, named=%s', async (named) => {
  mount(`/purchases/purchase/allocate/${named ? item.id : quantity.id}`); await ready()
  expect(field(text.fields.quantity_base)).toHaveValue(''); expect(field(text.fields.paid_minor)).toHaveValue(''); expect(screen.getByRole('combobox', { name: new RegExp(`^${text.fields.allocation}`) })).toHaveValue('')
  fillLine(named); await click(text.review)
  expect(api.createPurchaseLine).not.toHaveBeenCalled(); expect(screen.getByRole('alertdialog')).toHaveTextContent(`${text.purchaseProof}12`); expect(screen.getByRole('alertdialog')).toHaveTextContent(`${text.itemProof}7`)
  await confirm(text.confirmLine)
  const [id, body, context] = vi.mocked(api.createPurchaseLine).mock.calls[0]!
  expect(id).toBe(purchase.id); expect(body).toMatchObject({ item_id: named ? item.id : quantity.id, item_revision: 7, item_unit_id: named ? unit.id : null, quantity_base: named ? 1 : 1500, paid_minor: 123 })
  expect(context).toMatchObject({ purchase, item: named ? item : quantity }); expect(context).not.toHaveProperty('expectedRevision'); expect(context).not.toHaveProperty('unitRevision')
  expect([...lines.values()][0]?.revision).toBe(1); expect(holdings.fetchStockPage).not.toHaveBeenCalled()
})
it('focuses explicit nullable choice, safe integer counts, whole money and named-Unit quantity-one errors without coercion', async () => {
  mount('/purchases/purchase/allocate/item'); await ready(); await click(text.review)
  expect(screen.getByRole('combobox', { name: new RegExp(`^${text.fields.allocation}`) })).toHaveFocus()
  select(text.fields.allocation, 'unit'); await click(text.review); expect(screen.getByRole('combobox', { name: new RegExp(`^${text.fields.item_unit_id}`) })).toHaveFocus()
  select(text.fields.item_unit_id, unit.id); fill(text.fields.quantity_base, '2'); fill(text.fields.paid_minor, '1e3'); await click(text.review); expect(field(text.fields.quantity_base)).toHaveFocus()
  fill(text.fields.quantity_base, '1'); await click(text.review); expect(field(text.fields.paid_minor)).toHaveFocus()
  expect(api.createPurchaseLine).not.toHaveBeenCalled()
})
it('keeps unchanged header correction local, and emits only changed exact fields under the original complete source', async () => {
  mount('/purchases/purchase/edit'); await ready(); await click(text.review); expect(await screen.findByText(text.noChanges)).toBeVisible()
  fill(text.fields.receipt_note, ''); await click(text.review); await confirm(text.confirmSave)
  expect(api.patchPurchase).toHaveBeenCalledWith(purchase.id, { receipt_note: '' }, expect.objectContaining({ purchase }))
  expect(api.createPurchaseLine).not.toHaveBeenCalled()
})
it('retains equivalent instant spelling as explicit correction intent rather than truncating through Date', async () => {
  mount('/purchases/purchase/edit'); await ready(); fill(text.fields.purchased_at, '2026-11-07T02:00:00.123456789Z'); await click(text.review); await confirm(text.confirmSave)
  expect(api.patchPurchase).toHaveBeenCalledWith(purchase.id, { purchased_at: '2026-11-07T02:00:00.123456789Z' }, expect.objectContaining({ purchase }))
})
it('locks changed currency after even a zero-paid allocation but permits a descriptive correction', async () => {
  lines.set(line.id, line); mount('/purchases/purchase/edit'); await ready(); fill(text.fields.currency, 'USD'); await click(text.review)
  expect(field(text.fields.currency)).toHaveFocus(); expect(field(text.fields.currency)).toHaveAttribute('aria-invalid', 'true'); expect(api.patchPurchase).not.toHaveBeenCalled()
  fill(text.fields.currency, 'JPY'); fill(text.fields.receipt_note, 'changed description'); await click(text.review); await confirm(text.confirmSave)
  expect(api.patchPurchase).toHaveBeenCalledWith(purchase.id, { receipt_note: 'changed description' }, expect.objectContaining({ purchase }))
})
it.each([true, false])('adopts current header source only explicitly and retains invalid local text only when chosen, keep=%s', async (keep) => {
  const work = mount('/purchases/purchase/edit'); await ready(); fill(text.fields.purchased_at, 'invalid raw instant'); touchPurchase({ merchant_name: 'New current merchant' }); await refresh(work)
  expect(await screen.findByText(text.changed, { exact: true })).toBeVisible(); expect(field(text.fields.purchased_at)).toHaveValue('invalid raw instant'); expect(screen.getByRole('button', { name: text.review })).toBeDisabled()
  await click(keep ? text.keepEdits : text.discardEdits)
  expect(field(text.fields.merchant_name)).toHaveValue('New current merchant'); expect(field(text.fields.purchased_at)).toHaveValue(keep ? 'invalid raw instant' : purchase.purchased_at); expect(api.patchPurchase).not.toHaveBeenCalled()
})
it.each([true, false])('requires whole currency/tracking/measurement acknowledgement and never converts raw count/money through adoption, keep=%s', async (keep) => {
  const work = mount('/purchases/purchase/allocate/quantity'); await ready(); fillLine(); fill(text.fields.quantity_base, 'invalid count'); fill(text.fields.paid_minor, '9007199254740992')
  touchPurchase({ currency: 'USD' }); touchItem(quantity.id, { unit_code: 'L', base_unit: 'ml', scale_factor: 100 }); await refresh(work)
  expect(await screen.findByText(text.interpretationChanged)).toBeVisible(); expect(screen.getByRole('button', { name: text.keepEdits })).toBeDisabled(); expect(field(text.fields.quantity_base)).toHaveValue('invalid count')
  await userEvent.setup().click(screen.getByRole('checkbox', { name: text.acknowledge })); await click(keep ? text.keepEdits : text.discardEdits)
  expect(field(text.fields.quantity_base)).toHaveValue(keep ? 'invalid count' : ''); expect(field(text.fields.paid_minor)).toHaveValue(keep ? '9007199254740992' : ''); expect(field(text.fields.paid_minor)).toHaveAccessibleName(new RegExp('USD')); expect(field(text.fields.quantity_base)).toHaveAccessibleName(/ml/)
  expect(api.createPurchaseLine).not.toHaveBeenCalled()
})
it('retains a disappeared Unit choice and requires an explicit repair instead of changing it to null', async () => {
  const work = mount('/purchases/purchase/allocate/item'); await ready(); fillLine(true); units.clear(); touchItem(item.id); await refresh(work)
  expect(await screen.findByText(text.changed, { exact: true })).toBeVisible(); await click(text.keepEdits)
  expect(screen.getByRole('combobox', { name: new RegExp(`^${text.fields.item_unit_id}`) })).toHaveValue(unit.id); await click(text.review)
  expect(screen.getByRole('combobox', { name: new RegExp(`^${text.fields.item_unit_id}`) })).toHaveFocus(); expect(api.createPurchaseLine).not.toHaveBeenCalled()
  select(text.fields.allocation, 'none'); await click(text.review); await confirm(text.confirmLine)
  expect(vi.mocked(api.createPurchaseLine).mock.calls[0]?.[1].item_unit_id).toBeNull()
})
it('retains a named Unit decision when the whole Item changes to quantity tracking until the user explicitly changes it', async () => {
  const work = mount('/purchases/purchase/allocate/item'); await ready(); fillLine(true); units.clear(); touchItem(item.id, { tracking_mode: 'quantity', unit_code: 'kg', base_unit: 'g', scale_factor: 1000 }); await refresh(work)
  await userEvent.setup().click(await screen.findByRole('checkbox', { name: text.acknowledge })); await click(text.keepEdits); await click(text.review)
  expect(screen.getByRole('combobox', { name: new RegExp(`^${text.fields.allocation}`) })).toHaveValue('unit'); expect(api.createPurchaseLine).not.toHaveBeenCalled()
})
it('rejects stale original Item proof and keeps the draft for explicit comparison rather than rebasing in the writer', async () => {
  const work = mount('/purchases/purchase/allocate/quantity'); await ready(); fillLine(); touchItem(quantity.id); await click(text.review)
  await userEvent.setup().click(within(screen.getByRole('alertdialog')).getByRole('button', { name: text.confirmLine }))
  expect(await screen.findByText(messages.locations.conflict)).toBeVisible(); expect(vi.mocked(api.createPurchaseLine).mock.calls[0]?.[1].item_revision).toBe(7)
  await click(text.cancel); expect(field(text.fields.quantity_base)).toHaveValue('1500'); await refresh(work); expect(await screen.findByText(text.changed, { exact: true })).toBeVisible()
})
it('disables an unsent review after current sources change, but never substitutes its original proof', async () => {
  const work = mount('/purchases/purchase/allocate/quantity'); await ready(); fillLine(); await click(text.review); touchPurchase(); await refresh(work)
  expect(await screen.findByText(text.reviewStale)).toBeVisible(); expect(within(screen.getByRole('alertdialog')).getByRole('button', { name: text.confirmLine })).toBeDisabled(); expect(api.createPurchaseLine).not.toHaveBeenCalled()
})
it('retains a committed uncertain original command through current-read errors and cache eviction, and only explicitly reconciles the identical key/body/proofs', async () => {
  const original = vi.mocked(api.createPurchaseLine).getMockImplementation()!; let first = true
  vi.mocked(api.createPurchaseLine).mockImplementation(async (...args) => { const result = await original(...args); if (first) { first = false; throw new ApiError('INVALID_RESPONSE') } return result })
  const work = mount('/purchases/purchase/allocate/quantity'); await ready(); fillLine(); await click(text.review)
  await userEvent.setup().click(within(screen.getByRole('alertdialog')).getByRole('button', { name: text.confirmLine })); await screen.findAllByText(text.uncertain); await click(text.cancel)
  const [, body, context] = vi.mocked(api.createPurchaseLine).mock.calls[0]!
  vi.mocked(api.fetchPurchase).mockRejectedValue(new ApiError('NETWORK_ERROR')); await refresh(work); act(() => work.client.removeQueries({ queryKey: ['purchases'] }))
  expect(field(text.fields.quantity_base)).toHaveValue('1500'); expect(field(text.fields.quantity_base)).toBeDisabled(); act(() => window.dispatchEvent(new Event('online'))); expect(api.createPurchaseLine).toHaveBeenCalledTimes(1)
  vi.mocked(api.fetchPurchase).mockImplementation(async (id) => ({ ...purchases.get(id)! })); await click(text.openOriginal); await confirm(text.retryOriginal)
  const retry = vi.mocked(api.createPurchaseLine).mock.calls[1]!
  expect(retry[1]).toEqual(body); expect(retry[2]).toMatchObject({ idempotencyKey: context.idempotencyKey, purchase: context.purchase, item: context.item }); expect(lines.size).toBe(1)
})
it('keeps original-key reconciliation available even after current source reads report missing', async () => {
  vi.mocked(api.createPurchaseLine).mockRejectedValue(new ApiError('NETWORK_ERROR')); const work = mount('/purchases/purchase/allocate/item'); await ready(); fillLine(true); await click(text.review)
  await userEvent.setup().click(within(screen.getByRole('alertdialog')).getByRole('button', { name: text.confirmLine })); await screen.findAllByText(text.uncertain); await click(text.cancel)
  items.delete(item.id); await refresh(work); expect(await screen.findByText(text.missing, { exact: true })).toBeVisible(); await click(text.openOriginal)
  expect(within(screen.getByRole('alertdialog')).getByRole('button', { name: text.retryOriginal })).toBeEnabled()
})
it('waits for the first current complete read rather than initializing an editor from old cached data', async () => {
  let resolve!: (value: api.Purchase) => void; vi.mocked(api.fetchPurchase).mockReturnValueOnce(new Promise((done) => { resolve = done }))
  mount('/purchases/purchase/edit', undefined, (client) => client.setQueryData(['purchases', 'detail', purchase.id], { kind: 'detail', purchase: { ...purchase, receipt_note: 'OLD CACHE' }, lines: [] }))
  await screen.findByText(text.loading); expect(screen.queryByLabelText(text.fields.receipt_note)).not.toBeInTheDocument()
  await act(async () => resolve(purchase)); await ready(); expect(field(text.fields.receipt_note)).toHaveValue(purchase.receipt_note)
})
it('keeps initialized volatile financial input editable offline and never replays it on reconnect', async () => {
  mount('/purchases/purchase/allocate/quantity'); await ready(); fillLine()
  act(() => { Object.defineProperty(navigator, 'onLine', { configurable: true, value: false }); window.dispatchEvent(new Event('offline')) })
  expect(await screen.findByText(text.offline)).toBeVisible(); expect(screen.getByRole('button', { name: text.review })).toBeDisabled(); fill(text.fields.paid_minor, '0')
  act(() => { Object.defineProperty(navigator, 'onLine', { configurable: true, value: true }); window.dispatchEvent(new Event('online')) })
  await ready(); expect(field(text.fields.paid_minor)).toHaveValue('0'); expect(api.createPurchaseLine).not.toHaveBeenCalled()
})
it('cancels dirty navigation, restores the original review and handles the first Escape without sending', async () => {
  const work = mount('/purchases/purchase/edit'); await ready(); fill(text.fields.receipt_note, 'Dirty receipt'); await click(text.review)
  await act(async () => { void work.router.navigate('/purchases') }); expect(await screen.findByRole('heading', { name: text.leaveTitle })).toBeVisible(); await click(text.stay)
  expect(await screen.findByRole('heading', { name: text.reviewTitle })).toHaveFocus(); fireEvent.keyDown(screen.getByRole('alertdialog'), { key: 'Escape' }); await waitFor(() => expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument())
  expect(field(text.fields.receipt_note)).toHaveValue('Dirty receipt'); expect(api.patchPurchase).not.toHaveBeenCalled()
  await act(async () => { void work.router.navigate('/purchases') }); await click(text.leave); await waitFor(() => expect(work.router.state.location.pathname).toBe('/purchases'))
})
it.each([false, true])('purges financial draft context on session loss and retains only validated implemented navigation, mandatory=%s', async (mandatory) => {
  const work = mount('/purchases/purchase/allocate/quantity', { purchaseList: '/purchases?currency=JPY&limit=1', purchaseCursors: [null, 'opaque'], purchaseItem: quantity.id, catalogList: '/items?status=active', catalogCursors: [null], paid_minor: 'private financial draft', key: 'private key' }); await ready(); fillLine()
  administrator = mandatory ? { ...owner, must_change_password: true } : null; await click(text.refresh)
  await waitFor(() => expect(work.router.state.location.pathname).toBe(mandatory ? '/settings/account' : '/login'))
  expect(work.router.state.location.state).toMatchObject({ returnTo: '/purchases/purchase/allocate/quantity', purchaseList: '/purchases?currency=JPY&limit=1', purchaseItem: quantity.id, catalogList: '/items?status=active' })
  expect(work.router.state.location.state).not.toHaveProperty('paid_minor'); expect(work.router.state.location.state).not.toHaveProperty('key'); expect(work.client.getQueryData(['purchases', 'allocation', purchase.id, quantity.id])).toBeUndefined()
})
it('returns through login to a safe allocation route with empty financial fields and preserved catalog/purchase scopes', async () => {
  administrator = null; const work = mount('/purchases/purchase/allocate/quantity', { purchaseList: '/purchases?currency=JPY', catalogList: '/items?status=active', purchaseItem: quantity.id, paid_minor: 'private' })
  await screen.findByRole('heading', { name: messages.auth.loginTitle }); fill(messages.auth.username, 'owner'); fill(messages.auth.password, 'a sufficiently long password'); await click(messages.auth.login)
  await ready(); expect(work.router.state.location.pathname).toBe('/purchases/purchase/allocate/quantity'); expect(field(text.fields.quantity_base)).toHaveValue(''); expect(field(text.fields.paid_minor)).toHaveValue('')
  expect(work.router.state.location.state).toMatchObject({ purchaseList: '/purchases?currency=JPY', catalogList: '/items?status=active' }); expect(work.router.state.location.state).not.toHaveProperty('paid_minor')
})
it.each(['PURCHASE_CURRENCY_CONFLICT', 'ASSET_COST_BASIS_CONFLICT', 'ASSET_LIFECYCLE_REQUIRED'] as const)('shows an authoritative safe financial guard without turning it into physical receipt: %s', async (code) => {
  vi.mocked(api.createPurchaseLine).mockRejectedValue(new ApiError(code, { status: 409 })); mount('/purchases/purchase/allocate/item'); await ready(); fillLine(true); await click(text.review)
  await userEvent.setup().click(within(screen.getByRole('alertdialog')).getByRole('button', { name: text.confirmLine })); await waitFor(() => expect(within(screen.getByRole('alertdialog')).getByRole('alert')).toBeVisible())
  expect(within(screen.getByRole('alertdialog')).getByRole('button', { name: text.confirmLine })).toBeDisabled(); expect(holdings.fetchStockPage).not.toHaveBeenCalled()
})
it('requires explicit zero payment and retains an independent zero-paid allocation after confirmation', async () => {
  mount('/purchases/purchase/allocate/quantity'); await ready(); fillLine(); fill(text.fields.paid_minor, '0'); await click(text.review); await confirm(text.confirmLine)
  expect(vi.mocked(api.createPurchaseLine).mock.calls[0]?.[1].paid_minor).toBe(0)
  expect([...lines.values()][0]).toMatchObject({ paid_minor: 0, quantity_base: 1500, item_unit_id: null, revision: 1 })
})
it.each([false, true])('clears a sent financial command on authoritative session rejection and forwards no command through login, mandatory=%s', async (mandatory) => {
  vi.mocked(api.createPurchaseLine).mockRejectedValue(new ApiError(mandatory ? 'PASSWORD_CHANGE_REQUIRED' : 'AUTHENTICATION_REQUIRED', { status: mandatory ? 403 : 401 }))
  const work = mount('/purchases/purchase/allocate/quantity', { purchaseList: '/purchases?currency=JPY', catalogList: '/items?status=active' }); await ready(); fillLine(); await click(text.review)
  administrator = mandatory ? { ...owner, must_change_password: true } : null
  await userEvent.setup().click(within(screen.getByRole('alertdialog')).getByRole('button', { name: text.confirmLine }))
  await waitFor(() => expect(work.router.state.location.pathname).toBe(mandatory ? '/settings/account' : '/login'))
  expect(work.router.state.location.state).toMatchObject({ returnTo: '/purchases/purchase/allocate/quantity', purchaseList: '/purchases?currency=JPY' })
  expect(JSON.stringify(work.router.state.location.state)).not.toContain('paid_minor'); expect(JSON.stringify(work.router.state.location.state)).not.toContain('Exact allocation')
  expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument(); expect(lines.size).toBe(0)
})
it('shows a safe missing/error state with explicit refresh rather than inventing an empty Purchase', async () => {
  const work = mount('/purchases/missing'); expect(await screen.findByRole('heading', { name: text.missing })).toBeVisible(); expect(screen.queryByRole('link', { name: text.allocate })).not.toBeInTheDocument()
  await act(async () => { await work.router.navigate('/purchases') }); vi.mocked(api.fetchPurchasesPage).mockRejectedValue(new ApiError('NETWORK_ERROR')); await refresh(work)
  expect(await screen.findByText(messages.locations.network)).toBeVisible()
})
