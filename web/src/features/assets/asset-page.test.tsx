import { QueryClientProvider } from '@tanstack/react-query'
import { createMemoryRouter } from 'react-router'
import { RouterProvider } from 'react-router/dom'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeAll, beforeEach, expect, it, vi } from 'vitest'
import App from '@/App'
import * as auth from '@/lib/api/auth'
import * as api from '@/lib/api/assets'
import * as models from './model'
import * as locations from '@/lib/api/locations'
import * as taxonomy from '@/lib/api/taxonomy'
import { ApiError } from '@/lib/api/client'
import { canonicalInstant } from '@/lib/api/purchases'
import { messages } from '@/lib/messages'
import { createQueryClient } from '@/lib/query'

vi.mock('@/lib/api/auth', async (original) => ({ ...await original<typeof import('@/lib/api/auth')>(), fetchAdmin: vi.fn(), fetchCSRF: vi.fn(), login: vi.fn() }))
vi.mock('@/lib/api/assets', async (original) => ({ ...await original<typeof import('@/lib/api/assets')>(), fetchAssetsPage: vi.fn(), putAssetProfile: vi.fn(), recordAssetUsage: vi.fn(), recordAssetCost: vi.fn(), retireAsset: vi.fn(), disposeAsset: vi.fn() }))
vi.mock('./model', async (original) => ({ ...await original<typeof import('./model')>(), readAssetProfile: vi.fn(), readAssetUsage: vi.fn(), readAssetCosts: vi.fn(), readAssetLifecycle: vi.fn(), readAssetDetail: vi.fn() }))
vi.mock('@/lib/api/locations', async (original) => ({ ...await original<typeof import('@/lib/api/locations')>(), fetchLocationsPage: vi.fn() }))
vi.mock('@/lib/api/taxonomy', async (original) => ({ ...await original<typeof import('@/lib/api/taxonomy')>(), fetchCategoriesPage: vi.fn() }))
const text = messages.assets, cat = messages.catalog
const owner: auth.Admin = { id: 'owner', revision: 2, username: 'owner', must_change_password: false }
const original: models.AssetSource = { item: { id: 'item', revision: 11, name: 'Original Item', brand: null, model: '', category_id: null, description: '', notes: '', status: 'active', tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1 },
  unit: { id: 'unit', revision: 13, item_id: 'item', asset_code: 'AST-Original', serial_number: null, notes: 'Original physical notes', status: 'stored', location_id: null },
  asset: { item_unit_id: 'unit', revision: 7, cost_basis: 'manual', currency: 'JPY', manual_acquisition_minor: 100, usage_unit: 'times', in_service_at: null, retired_at: null, estimated_residual_minor: 30 } }
const instant = '2026-11-07T10:00:00.123456789+08:00'
const use: api.Usage = { id: 'use', revision: 1, item_unit_id: 'unit', delta: 2, usage_unit: 'times', event_type: 'use', occurred_at: instant, correction_of: null, note: 'Original use' }
const cost: api.Cost = { id: 'cost', revision: 1, item_unit_id: 'unit', amount_minor: 5, currency: 'JPY', kind: 'repair', direction: 'expense', occurred_at: instant, correction_of: null, note: 'Original cost' }
let administrator: auth.Admin | null, source: models.AssetProfileSource, usage: api.Usage[], costs: api.Cost[], disposal: api.Disposal | null, missing: boolean
let profiles: Map<string, api.Asset>, uses: Map<string, api.Usage>, charges: Map<string, api.Cost>, retirements: Map<string, models.AssetSource['unit']>, disposals: Map<string, api.Disposal>
const disposers: (() => void)[] = []
function profile() { if (missing) throw new ApiError('NOT_FOUND', { status: 404 }); return structuredClone(source) }
function financial(): models.AssetSource { const current = profile(); if (current.asset === null) throw new ApiError('NOT_FOUND', { status: 404 }); return { ...current, asset: current.asset } }
function touchAsset(changes: Partial<api.Asset> = {}) { const current = financial(); source = { ...current, item: { ...current.item, revision: current.item.revision + 1 }, asset: { ...current.asset, ...changes, revision: current.asset.revision + 1 } } }
function proof(expected: number, actual: number) { if (expected !== actual) throw new ApiError('REVISION_CONFLICT', { status: 409 }) }
beforeAll(async () => { await Promise.all([import('./asset-page'), import('@/features/auth/account-page'), import('@/features/holdings/holding-page'), import('@/features/purchases/purchase-page')]) })
beforeEach(() => {
  vi.clearAllMocks(); administrator = { ...owner }; source = structuredClone(original); usage = [structuredClone(use)]; costs = [structuredClone(cost)]; disposal = null; missing = false
  profiles = new Map(); uses = new Map(); charges = new Map(); retirements = new Map(); disposals = new Map()
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: true }); vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
  vi.mocked(auth.fetchAdmin).mockReset().mockImplementation(async () => { if (!administrator) throw new ApiError('AUTHENTICATION_REQUIRED', { status: 401 }); return administrator })
  vi.mocked(auth.fetchCSRF).mockReset().mockResolvedValue('C'.repeat(43))
  vi.mocked(auth.login).mockReset().mockImplementation(async () => { administrator = { ...owner }; return administrator })
  vi.mocked(locations.fetchLocationsPage).mockReset().mockResolvedValue({ data: [], next_cursor: null }); vi.mocked(taxonomy.fetchCategoriesPage).mockReset().mockResolvedValue({ data: [], next_cursor: null })
  vi.mocked(models.readAssetProfile).mockReset().mockImplementation(async () => profile())
  vi.mocked(models.readAssetUsage).mockReset().mockImplementation(async () => ({ ...financial(), kind: 'usage', usage: structuredClone(usage) }))
  vi.mocked(models.readAssetCosts).mockReset().mockImplementation(async () => ({ ...financial(), kind: 'costs', costs: structuredClone(costs) }))
  vi.mocked(models.readAssetLifecycle).mockReset().mockImplementation(async () => ({ ...financial(), disposal: structuredClone(disposal) }))
  vi.mocked(models.readAssetDetail).mockReset().mockImplementation(async (_id, query) => ({ ...financial(), unit: { ...source.unit, status: 'disposed' }, asset: { ...financial().asset, in_service_at: '2024-11-07T00:00:00Z' }, disposal: { item_unit_id: 'unit', revision: 1, kind: 'sold', disposed_at: instant, currency: 'JPY', realized_proceeds_minor: 200, note: '' }, summary: { unit_id: 'unit', currency: 'JPY', cost_basis: 'manual', acquisition_minor: 100, extra_minor: 3, realized_proceeds_minor: 200, net_cost_minor: -97, estimated_residual_minor: 30, projected_net_cost_minor: 73, elapsed_days: 731, total_usage: 2, usage_unit: 'times', per_day_minor: -97 / 731, per_use_minor: -48.5, projected_per_day_minor: 73 / 731, projected_per_use_minor: 36.5, as_of: canonicalInstant(query.as_of ?? instant)!, timezone: 'UTC', day_policy: 'inclusive_civil_days', source_revision: 99 } }))
  vi.mocked(api.fetchAssetsPage).mockReset().mockImplementation(async (query) => ({ data: source.asset && (!query.currency || query.currency === source.asset.currency) ? [structuredClone(source.asset)] : [], next_cursor: null }))
  vi.mocked(api.putAssetProfile).mockReset().mockImplementation(async (id, body, context) => {
    const frozen = profiles.get(context.idempotencyKey); if (frozen) return frozen
    const current = profile(); proof(current.unit.revision, context.unit.revision); proof(current.item.revision, body.item_revision)
    if (body.asset_revision !== (current.asset?.revision ?? null)) throw new ApiError('REVISION_CONFLICT', { status: 409 })
    const { item_revision: _item, asset_revision: _asset, ...fields } = body
    const asset = { ...fields, item_unit_id: id, revision: (current.asset?.revision ?? 0) + 1 }
    source = { ...current, item: { ...current.item, revision: current.item.revision + 1 }, asset }; profiles.set(context.idempotencyKey, structuredClone(asset)); return asset
  })
  vi.mocked(api.recordAssetUsage).mockReset().mockImplementation(async (id, body, context) => {
    const frozen = uses.get(context.idempotencyKey); if (frozen) return frozen
    proof(financial().asset.revision, context.asset.revision)
    const event = { ...body, id: `new-use-${usage.length}`, item_unit_id: id, revision: 1, note: body.note ?? '', correction_of: body.correction_of ?? null, occurred_at: canonicalInstant(body.occurred_at)! }
    usage = [...usage, event]; touchAsset(); uses.set(context.idempotencyKey, structuredClone(event)); return event
  })
  vi.mocked(api.recordAssetCost).mockReset().mockImplementation(async (id, body, context) => {
    const frozen = charges.get(context.idempotencyKey); if (frozen) return frozen
    proof(financial().asset.revision, context.asset.revision)
    const event = { ...body, id: `new-cost-${costs.length}`, item_unit_id: id, revision: 1, note: body.note ?? '', correction_of: body.correction_of ?? null, occurred_at: canonicalInstant(body.occurred_at)! }
    costs = [...costs, event]; touchAsset(); charges.set(context.idempotencyKey, structuredClone(event)); return event
  })
  vi.mocked(api.retireAsset).mockReset().mockImplementation(async (_id, body, context) => {
    const frozen = retirements.get(context.idempotencyKey); if (frozen) return frozen
    const current = financial(); proof(current.asset.revision, context.asset.revision); proof(current.unit.revision, body.unit_revision)
    touchAsset({ retired_at: canonicalInstant(body.retired_at)! }); source = { ...source, unit: { ...source.unit, revision: source.unit.revision + 1, status: 'retired' } }; retirements.set(context.idempotencyKey, structuredClone(source.unit)); return source.unit
  })
  vi.mocked(api.disposeAsset).mockReset().mockImplementation(async (id, body, context) => {
    const frozen = disposals.get(context.idempotencyKey); if (frozen) return frozen
    const current = financial(); proof(current.asset.revision, context.asset.revision); proof(current.unit.revision, body.unit_revision)
    const { unit_revision: _unit, ...fields } = body
    disposal = { ...fields, item_unit_id: id, revision: 1, note: body.note ?? '', disposed_at: canonicalInstant(body.disposed_at)! }
    touchAsset(); source = { ...source, unit: { ...source.unit, revision: source.unit.revision + 1, status: 'disposed' } }; disposals.set(context.idempotencyKey, structuredClone(disposal)); return disposal
  })
})
afterEach(() => { for (const dispose of disposers.splice(0)) dispose() })
function mount(path = '/assets', state?: unknown, prepare?: (client: ReturnType<typeof createQueryClient>) => void) {
  const client = createQueryClient(); prepare?.(client)
  const [pathname, search = ''] = path.split('?'), router = createMemoryRouter([{ path: '*', element: <App /> }], { initialEntries: [{ pathname, search: search ? `?${search}` : '', state }] })
  const view = render(<QueryClientProvider client={client}><RouterProvider router={router} /></QueryClientProvider>)
  disposers.push(() => { view.unmount(); router.dispose(); client.clear() }); return { ...view, client, router }
}
const label = (value: string) => new RegExp(`^${value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}`)
function fill(name: string, value: string) { fireEvent.change(screen.getByLabelText(label(name)), { target: { value } }) }
function select(name: string, value: string) { fireEvent.change(screen.getByRole('combobox', { name: label(name) }), { target: { value } }) }
const click = (name: string) => userEvent.setup().click(screen.getByRole('button', { name }))
async function ready() { await waitFor(() => expect(screen.getByRole('button', { name: text.review })).toBeEnabled()) }
async function confirm(work: ReturnType<typeof mount>, name: string, path: string, heading: string) {
  const dialog = await screen.findByRole('alertdialog'); await userEvent.setup().click(within(dialog).getByRole('button', { name }))
  await waitFor(() => expect(work.router.state.location.pathname).toBe(path)); await screen.findByRole('heading', { level: 1, name: heading }); await screen.findByText(text.done, { exact: true })
}
function eventValues(kind: 'usage' | 'costs' | 'retire' | 'dispose') {
  if (kind === 'usage') { select(text.fields.event_type, 'correction'); fill(text.fields.delta, '-1'); fill(text.fields.occurred_at, instant); select(text.fields.correction_of, use.id) }
  else if (kind === 'costs') { select(text.fields.kind, 'adjustment'); select(text.fields.direction, 'credit'); fill(text.fields.amount_minor, '2'); fill(text.fields.occurred_at, instant); select(text.fields.correction_of, cost.id) }
  else if (kind === 'retire') fill(text.fields.retired_at, instant)
  else { select(text.dispose, 'other'); fill(text.fields.realized_proceeds_minor, '0'); fill(text.fields.disposed_at, '1999-01-01T00:00:00Z') }
  fill(text.fields.note, '\0Exact raw é\n')
}

it('shows a real filtered page and explicit stable identities without hidden row hydration or unsupported search', async () => {
  mount('/assets?currency=JPY&limit=1', { assetCursors: [null] })
  expect(await screen.findByRole('link', { name: `${text.identifier}：unit` })).toHaveAttribute('href', '/assets/unit')
  expect(api.fetchAssetsPage).toHaveBeenCalledWith({ currency: 'JPY', limit: 1 }, expect.any(AbortSignal)); expect(screen.queryByLabelText(cat.search)).not.toBeInTheDocument()
  await userEvent.setup().click(screen.getByText(cat.advanced, { exact: true }))
  for (const name of [cat.from, cat.to, cat.currency, cat.merchant, cat.pageSize]) expect(screen.getByLabelText(label(name))).toBeVisible()
  expect(screen.getByRole('button', { name: cat.previousPage })).toBeEnabled(); expect(models.readAssetProfile).not.toHaveBeenCalled(); expect(auth.fetchCSRF).not.toHaveBeenCalled()
})
it('keeps exact merchant/server criteria and explicit cursor controls, resetting only after apply', async () => {
  const work = mount('/assets?limit=1&cursor=original', { assetCursors: [null, 'older-scope'] }); await screen.findByRole('link', { name: `${text.identifier}：unit` }); expect(screen.getByRole('button', { name: cat.previousPage })).toBeEnabled(); await userEvent.setup().click(screen.getByText(cat.advanced, { exact: true }))
  fill(cat.merchant, 'Exact\nmerchant'); fill(cat.currency, 'JPY'); fill(cat.pageSize, '1'); await click(cat.apply)
  await waitFor(() => expect(new URLSearchParams(work.router.state.location.search).get('merchant')).toBe('Exact\nmerchant'))
  await waitFor(() => expect(api.fetchAssetsPage).toHaveBeenCalledWith(expect.objectContaining({ merchant: 'Exact\nmerchant', currency: 'JPY', limit: 1 }), expect.any(AbortSignal)))
  expect(work.router.state.location.state.assetCursors).toEqual([]); await waitFor(() => expect(screen.getByRole('button', { name: cat.previousPage })).toBeDisabled())
  await waitFor(() => expect(screen.getByRole('button', { name: cat.reset })).toBeEnabled()); await click(cat.reset); await waitFor(() => expect(work.router.state.location.search).toBe(''))
})
it('reads the profile alone and links actual physical/purchase evidence without requiring a calculation or sibling history', async () => {
  mount('/assets/unit', { assetList: '/assets?currency=JPY', assetCursors: [null] })
  expect(await screen.findByRole('link', { name: text.edit })).toHaveAttribute('href', '/assets/unit/edit')
  expect(screen.getByRole('link', { name: text.evidence })).toHaveAttribute('href', '/items/item/purchases')
  expect(screen.getByRole('link', { name: text.back })).toHaveAttribute('href', '/assets?currency=JPY')
  expect(models.readAssetDetail).not.toHaveBeenCalled(); expect(models.readAssetUsage).not.toHaveBeenCalled(); expect(models.readAssetCosts).not.toHaveBeenCalled()
})
it('requires explicit profile absence choices and zero without defaulting dates, money or financial interpretation', async () => {
  source = { ...source, asset: null }
  const work = mount('/assets/unit/edit'); await ready()
  expect(screen.getByRole('combobox', { name: label(text.fields.cost_basis) })).toHaveValue('')
  await click(text.review); await waitFor(() => expect(screen.getByRole('combobox', { name: label(text.fields.cost_basis) })).toHaveFocus())
  expect(api.putAssetProfile).not.toHaveBeenCalled(); select(text.fields.cost_basis, 'manual'); fill(text.fields.currency, 'JPY'); select(text.fields.usage_unit, 'times')
  for (const field of ['service_decision', 'retirement_decision', 'residual_decision'] as const) select(text.fields[field], 'unknown')
  expect(screen.getByLabelText(label(text.fields.manual_acquisition_minor))).toHaveValue(''); fill(text.fields.manual_acquisition_minor, '0'); await click(text.review)
  await confirm(work, text.confirms['put-profile'], '/assets/unit', text.detail)
  expect(api.putAssetProfile).toHaveBeenCalledWith('unit', expect.objectContaining({ manual_acquisition_minor: 0, asset_revision: null, item_revision: 11, in_service_at: null, retired_at: null, estimated_residual_minor: null }), expect.objectContaining({ unit: original.unit, item: original.item, asset: null }))
})
it.each(['usage', 'costs', 'retire', 'dispose'] as const)('reviews and explicitly confirms the actual %s proposal with independent original contexts', async (kind) => {
  const work = mount(`/assets/unit/${kind}${kind === 'usage' || kind === 'costs' ? '/new' : ''}`); await ready(); eventValues(kind)
  expect(auth.fetchCSRF).not.toHaveBeenCalled(); await click(text.review)
  const dialog = await screen.findByRole('alertdialog'); expect(within(dialog).getByRole('heading', { name: text.reviewTitle })).toHaveFocus()
  const commandKind = kind === 'usage' ? 'record-usage' : kind === 'costs' ? 'record-cost' : kind === 'retire' ? 'retire-asset' : 'dispose-asset'
  await confirm(work, text.confirms[commandKind], kind === 'usage' || kind === 'costs' ? `/assets/unit/${kind}` : '/assets/unit', kind === 'usage' ? text.usage : kind === 'costs' ? text.costs : text.detail)
  const writer = kind === 'usage' ? api.recordAssetUsage : kind === 'costs' ? api.recordAssetCost : kind === 'retire' ? api.retireAsset : api.disposeAsset
  expect(writer).toHaveBeenCalledTimes(1); expect(writer).toHaveBeenCalledWith('unit', expect.objectContaining({ note: '\0Exact raw é\n' }), expect.objectContaining({ asset: original.asset, ...(kind === 'retire' || kind === 'dispose' ? { unit: original.unit } : {}), ...(kind === 'dispose' ? { disposal: null } : {}) }))
  if (kind === 'usage') expect(api.recordAssetUsage).toHaveBeenCalledWith('unit', expect.objectContaining({ delta: -1, correction_of: use.id }), expect.anything())
  if (kind === 'costs') expect(api.recordAssetCost).toHaveBeenCalledWith('unit', expect.objectContaining({ amount_minor: 2, direction: 'credit', correction_of: cost.id }), expect.anything())
  if (kind === 'dispose') expect(source.asset?.retired_at).toBeNull()
})
it.each(['retire', 'dispose'] as const)('describes absent current disposal without claiming absent history in the %s editor and review', async (kind) => {
  source = { ...source, unit: { ...source.unit, status: 'disposed' } }
  mount(`/assets/unit/${kind}`); await ready()
  const explanation = '目前沒有生效的處置財務決定；這不代表沒有歷史紀錄。'
  expect(screen.getByText(explanation, { exact: true })).toBeVisible(); expect(screen.queryByText('原先已確認尚無處置紀錄', { exact: true })).not.toBeInTheDocument()
  eventValues(kind); await click(text.review)
  expect(within(await screen.findByRole('alertdialog')).getByText(explanation, { exact: true })).toBeVisible()
  expect(api.retireAsset).not.toHaveBeenCalled(); expect(api.disposeAsset).not.toHaveBeenCalled()
})
it('saves a complete changed profile from its original independent Unit/Item/Asset snapshots', async () => {
  const work = mount('/assets/unit/edit'); await ready(); fill(text.fields.manual_acquisition_minor, '123'); await click(text.review)
  await confirm(work, text.confirms['put-profile'], '/assets/unit', text.detail)
  expect(api.putAssetProfile).toHaveBeenCalledWith('unit', expect.objectContaining({ item_revision: 11, asset_revision: 7, manual_acquisition_minor: 123 }), expect.objectContaining({ item: original.item, unit: original.unit, asset: original.asset }))
})
it('fills +1 only on a gesture, still requires an explicit date and never dispatches the shortcut itself', async () => {
  mount('/assets/unit/usage/new'); await ready(); await click(text.quickUse)
  expect(screen.getByLabelText(label(text.fields.delta))).toHaveValue('1'); expect(screen.getByRole('combobox', { name: label(text.fields.event_type) })).toHaveValue('use')
  expect(screen.getByLabelText(label(text.fields.occurred_at))).toHaveValue(''); expect(api.recordAssetUsage).not.toHaveBeenCalled()
  await userEvent.setup().click(screen.getByRole('button', { name: `${text.now}：${text.fields.occurred_at}` }))
  expect(screen.getByLabelText(label(text.fields.occurred_at))).not.toHaveValue(''); expect(api.recordAssetUsage).not.toHaveBeenCalled()
})
it('renders negative authoritative net and separate residual ratios as accessible exact rows with as_of context', async () => {
  const work = mount('/assets/unit/summary?as_of=2026-11-07T10%3A00%3A00.123456789%2B08%3A00')
  const table = await screen.findByRole('table'); expect(within(table).getByRole('rowheader', { name: label(text.summaryFields.net_cost_minor) })).toBeVisible()
  const net = within(table).getByRole('row', { name: new RegExp(text.summaryFields.net_cost_minor) }); expect(within(net).getByRole('cell')).toHaveTextContent('-97')
  const projected = within(table).getByRole('row', { name: new RegExp(text.summaryFields.projected_net_cost_minor) }); expect(within(projected).getByRole('cell')).toHaveTextContent('73')
  expect(screen.getByText('2026-11-07T02:00:00.123456789Z')).toBeVisible(); expect(screen.getByText(text.sourceRevision)).toBeVisible()
  fill(text.asOf, 'bad'); await click(text.applyAsOf); expect(work.router.state.location.search).toContain('as_of=')
  fill(text.asOf, '2026-11-08T00:00:00.000000001Z'); await click(text.applyAsOf)
  await waitFor(() => expect(models.readAssetDetail).toHaveBeenCalledWith('unit', { as_of: '2026-11-08T00:00:00.000000001Z' }, expect.any(AbortSignal)))
})
it('retains raw invalid money and changes interpretation only after explicit acknowledgement and adoption', async () => {
  costs = []
  const work = mount('/assets/unit/costs/new'); await ready(); select(text.fields.kind, 'repair'); select(text.fields.direction, 'expense'); fill(text.fields.amount_minor, '1.5'); fill(text.fields.occurred_at, instant)
  touchAsset({ currency: 'USD' }); await act(async () => { await work.client.invalidateQueries({ queryKey: ['assets'] }) })
  expect(await screen.findByText(text.changed)).toBeVisible(); expect(screen.getByRole('button', { name: text.keepEdits })).toBeDisabled()
  await userEvent.setup().click(screen.getByRole('checkbox', { name: text.acknowledge })); await click(text.keepEdits)
  expect(screen.getByLabelText(label(text.fields.currency))).toHaveValue('USD'); expect(screen.getByLabelText(label(text.fields.amount_minor))).toHaveValue('1.5')
  await click(text.review); expect(api.recordAssetCost).not.toHaveBeenCalled()
  fill(text.fields.amount_minor, '2'); await click(text.review); await confirm(work, text.confirms['record-cost'], '/assets/unit/costs', text.costs)
  expect(api.recordAssetCost).toHaveBeenCalledWith('unit', expect.objectContaining({ currency: 'USD', amount_minor: 2 }), expect.objectContaining({ asset: expect.objectContaining({ revision: 8, currency: 'USD' }) }))
})
it('keeps a sent original command through failed current reads and explicitly retries without installing an old reply', async () => {
  const deliver = vi.mocked(api.recordAssetCost).getMockImplementation()!
  vi.mocked(api.recordAssetCost).mockImplementationOnce(async (...args) => { await deliver(...args); throw new ApiError('INVALID_RESPONSE') })
  const work = mount('/assets/unit/costs/new'); await ready(); eventValues('costs'); await click(text.review)
  const dialog = await screen.findByRole('alertdialog'); await userEvent.setup().click(within(dialog).getByRole('button', { name: text.confirms['record-cost'] }))
  await screen.findByRole('heading', { name: text.uncertain }); const originalCall = vi.mocked(api.recordAssetCost).mock.calls[0]!
  missing = true; await act(async () => { await work.client.invalidateQueries({ queryKey: ['assets'] }) })
  expect(await screen.findByText(text.missingHelp)).toBeVisible(); await act(async () => { window.dispatchEvent(new Event('online')) }); expect(api.recordAssetCost).toHaveBeenCalledTimes(1)
  vi.mocked(auth.fetchCSRF).mockResolvedValueOnce('fresh-csrf')
  await userEvent.setup().click(within(dialog).getByRole('button', { name: text.retryOriginal })); await waitFor(() => expect(work.router.state.location.pathname).toBe('/assets/unit/costs'))
  expect(api.recordAssetCost).toHaveBeenCalledTimes(2)
  expect(vi.mocked(api.recordAssetCost).mock.calls[1]?.slice(0, 2)).toEqual(originalCall.slice(0, 2))
  expect(vi.mocked(api.recordAssetCost).mock.calls[1]?.[2]).toMatchObject({ idempotencyKey: originalCall[2].idempotencyKey, asset: originalCall[2].asset, csrfToken: 'fresh-csrf' })
  expect(costs).toHaveLength(2)
})
it('retains an initialized offline draft but never replays a financial write on reconnect', async () => {
  mount('/assets/unit/usage/new'); await ready(); fill(text.fields.delta, '123')
  act(() => { Object.defineProperty(navigator, 'onLine', { configurable: true, value: false }); window.dispatchEvent(new Event('offline')) })
  expect(screen.getByText(text.offline)).toBeVisible(); expect(screen.getByLabelText(label(text.fields.delta))).toHaveValue('123'); expect(screen.getByRole('button', { name: text.review })).toBeDisabled()
  await act(async () => { Object.defineProperty(navigator, 'onLine', { configurable: true, value: true }); window.dispatchEvent(new Event('online')) })
  expect(api.recordAssetUsage).not.toHaveBeenCalled(); expect(screen.getByLabelText(label(text.fields.delta))).toHaveValue('123')
})
it('keeps a dirty editor on Back/cancel and first Escape without sending or losing the draft', async () => {
  const work = mount('/assets/unit/retire'); await ready(); fill(text.fields.retired_at, instant)
  await userEvent.setup().click(screen.getByRole('link', { name: text.backDetail })); const leave = await screen.findByRole('alertdialog')
  await userEvent.setup().click(within(leave).getByRole('button', { name: text.stay })); expect(work.router.state.location.pathname).toBe('/assets/unit/retire')
  await click(text.review); const review = await screen.findByRole('alertdialog'); expect(within(review).getByRole('heading', { name: text.reviewTitle })).toHaveFocus()
  await userEvent.setup().keyboard('{Escape}'); await waitFor(() => expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument())
  expect(screen.getByLabelText(label(text.fields.retired_at))).toHaveValue(instant); expect(api.retireAsset).not.toHaveBeenCalled()
})
it('admits only validated Asset read scope through the implemented login route and drops raw amounts/commands', async () => {
  administrator = null
  const work = mount('/assets/unit/costs/new', { assetList: '/assets?currency=JPY&limit=1', assetCursors: [null, 'signed'], catalogList: '/items?limit=1', command: { key: 'secret', amount_minor: 100 }, amount_minor: '100' })
  await screen.findByRole('heading', { level: 1, name: messages.auth.loginTitle }); expect(work.router.state.location.state).not.toHaveProperty('command'); expect(work.router.state.location.state).not.toHaveProperty('amount_minor')
  fill(messages.auth.username, 'owner'); fill(messages.auth.password, 'Known-test-password-123!'); await click(messages.auth.login)
  await waitFor(() => expect(work.router.state.location.pathname).toBe('/assets/unit/costs/new')); await ready()
  expect(work.router.state.location.state).toMatchObject({ assetList: '/assets?currency=JPY&limit=1', assetCursors: [null, 'signed'], catalogList: '/items?limit=1' })
  expect(screen.getByLabelText(label(text.fields.amount_minor))).toHaveValue(''); expect(api.recordAssetCost).not.toHaveBeenCalled()
})
it('initializes a new editor only after its first current read, never from a warm cached proof or money value', async () => {
  let release: ((value: models.AssetProfileSource) => void) | undefined
  vi.mocked(models.readAssetProfile).mockImplementationOnce(() => new Promise((resolve) => { release = resolve }))
  mount('/assets/unit/edit', undefined, (client) => client.setQueryData(['assets', 'profile', 'unit'], { scope: 'profile', source: { ...original, asset: { ...original.asset, revision: 99, manual_acquisition_minor: 999 } } }))
  await waitFor(() => expect(models.readAssetProfile).toHaveBeenCalledTimes(1)); expect(screen.queryByLabelText(label(text.fields.manual_acquisition_minor))).not.toBeInTheDocument()
  await act(async () => { release?.(structuredClone(original)) }); await ready(); expect(screen.getByLabelText(label(text.fields.manual_acquisition_minor))).toHaveValue('100')
})
it('withholds new writes after a failed current read while retaining initialized raw invalid input', async () => {
  const work = mount('/assets/unit/usage/new'); await ready(); fill(text.fields.delta, 'invalid raw count')
  vi.mocked(models.readAssetUsage).mockRejectedValueOnce(new ApiError('INVALID_RESPONSE'))
  await act(async () => { await work.client.invalidateQueries({ queryKey: ['assets'] }) })
  expect(await screen.findByText(text.invalidRead)).toBeVisible(); expect(screen.getByLabelText(label(text.fields.delta))).toHaveValue('invalid raw count'); expect(screen.getByRole('button', { name: text.review })).toBeDisabled(); expect(api.recordAssetUsage).not.toHaveBeenCalled()
})
it('renders all unknown residual/divisor results as unknown rather than zero or a fabricated projected total', async () => {
  source = { ...original, asset: { ...original.asset, estimated_residual_minor: null } }; usage = []; costs = []
  vi.mocked(models.readAssetDetail).mockResolvedValueOnce({ ...financial(), disposal: null, summary: { unit_id: 'unit', currency: 'JPY', cost_basis: 'manual', acquisition_minor: 100, extra_minor: 0, realized_proceeds_minor: 0, net_cost_minor: 100, estimated_residual_minor: null, projected_net_cost_minor: null, elapsed_days: null, total_usage: 0, usage_unit: 'times', per_day_minor: null, per_use_minor: null, projected_per_day_minor: null, projected_per_use_minor: null, as_of: canonicalInstant(instant)!, timezone: 'UTC', day_policy: 'inclusive_civil_days', source_revision: 0 } })
  mount('/assets/unit/summary'); const table = await screen.findByRole('table')
  expect(within(table).getAllByText(text.nullRatio)).toHaveLength(4); expect(within(table).getByRole('row', { name: new RegExp(text.summaryFields.projected_net_cost_minor) })).toHaveTextContent(text.unknownValue)
  expect(within(table).getByRole('row', { name: new RegExp(text.summaryFields.net_cost_minor) })).toHaveTextContent('100')
})
it('renders complete retained history and filters locally without inventing a partial financial total', async () => {
  usage = Array.from({ length: 205 }, (_, index) => ({ ...use, id: `legacy-${String(index).padStart(3, '0')}`, event_type: index === 204 ? 'correction' : 'use', correction_of: null }))
  mount('/assets/unit/usage'); expect(await screen.findByText(`${text.eventCount}205`)).toBeVisible()
  fill(text.eventSearch, 'legacy-204'); expect(screen.getByText(new RegExp(`${text.eventID}：legacy-204`))).toBeVisible(); expect(screen.getByText(text.legacyReference)).toBeVisible()
  fill(text.eventSearch, 'missing target'); expect(screen.getByText(text.noMatches)).toBeVisible(); expect(models.readAssetDetail).not.toHaveBeenCalled(); expect(models.readAssetCosts).not.toHaveBeenCalled()
})

it.each(['/assets/unit/usage/new?delta=1', '/assets/%2Fsecret/edit', '/assets/%00/summary', `/assets/${'x'.repeat(129)}/costs/new`])('does not build unsafe links or read/write invalid financial route %s', async (path) => {
  mount(path)
  await screen.findByText(messages.locations.validation)
  expect(models.readAssetProfile).not.toHaveBeenCalled(); expect(models.readAssetUsage).not.toHaveBeenCalled(); expect(api.recordAssetUsage).not.toHaveBeenCalled()
})
