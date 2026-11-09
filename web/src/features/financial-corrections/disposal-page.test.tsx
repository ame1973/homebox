import { QueryClientProvider } from '@tanstack/react-query'
import { createMemoryRouter } from 'react-router'
import { RouterProvider } from 'react-router/dom'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeAll, beforeEach, expect, it, vi } from 'vitest'
import App from '@/App'
import * as auth from '@/lib/api/auth'
import { ApiError } from '@/lib/api/client'
import * as finance from '@/lib/api/financial-corrections'
import { canonicalInstant } from '@/lib/api/purchases'
import { messages } from '@/lib/messages'
import { createQueryClient } from '@/lib/query'
import * as models from '@/features/assets/correction-model'

vi.mock('@/lib/api/auth', async (original) => ({ ...await original<typeof import('@/lib/api/auth')>(), fetchAdmin: vi.fn(), fetchCSRF: vi.fn(), login: vi.fn() }))
vi.mock('@/features/assets/correction-model', async (original) => ({ ...await original<typeof import('@/features/assets/correction-model')>(), readDisposalCorrection: vi.fn() }))
vi.mock('@/lib/api/financial-corrections', async (original) => ({ ...await original<typeof import('@/lib/api/financial-corrections')>(), correctDisposal: vi.fn() }))
const text = messages.disposalCorrections, asset = messages.assets
const owner: auth.Admin = { id: 'owner', revision: 2, username: 'owner', must_change_password: false }
const instant = '2026-11-07T10:00:00.123456789+08:00'
const original: models.DisposalCorrectionSource = {
  item: { id: 'item', revision: 11, name: 'Original Item', brand: null, model: '', category_id: null, description: '', notes: '', status: 'active', tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1 },
  unit: { id: 'unit', revision: 13, item_id: 'item', asset_code: 'AST-Original', serial_number: null, notes: 'Original physical notes', status: 'disposed', location_id: null },
  asset: { item_unit_id: 'unit', revision: 7, cost_basis: 'manual', currency: 'JPY', manual_acquisition_minor: 100, usage_unit: 'times', in_service_at: null, retired_at: null, estimated_residual_minor: 30 },
  disposal: { item_unit_id: 'unit', revision: 9, original: { item_unit_id: 'unit', kind: 'sold', currency: 'JPY', realized_proceeds_minor: 200, disposed_at: instant, note: 'Original' },
    current: { item_unit_id: 'unit', kind: 'sold', currency: 'JPY', realized_proceeds_minor: 200, disposed_at: instant, note: 'Original' } }, corrections: [],
}
let administrator: auth.Admin | null, source: models.DisposalCorrectionSource, missing: boolean, receipts: Map<string, finance.DisposalCorrection>
const disposers: (() => void)[] = []
beforeAll(async () => { await Promise.all([import('./disposal-page'), import('@/features/assets/asset-page'), import('@/features/auth/account-page')]) })
beforeEach(() => {
  vi.clearAllMocks(); administrator = { ...owner }; source = structuredClone(original); missing = false; receipts = new Map()
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: true }); vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
  vi.mocked(auth.fetchAdmin).mockReset().mockImplementation(async () => { if (!administrator) throw new ApiError('AUTHENTICATION_REQUIRED', { status: 401 }); return administrator })
  vi.mocked(auth.fetchCSRF).mockReset().mockResolvedValue('C'.repeat(43)); vi.mocked(auth.login).mockReset().mockImplementation(async () => { administrator = { ...owner }; return administrator })
  vi.mocked(models.readDisposalCorrection).mockReset().mockImplementation(async () => { if (missing) throw new ApiError('NOT_FOUND', { status: 404 }); return structuredClone(source) })
  vi.mocked(finance.correctDisposal).mockReset().mockImplementation(async (id, body, context) => {
    const receipt = receipts.get(context.idempotencyKey); if (receipt) return receipt
    if (missing) throw new ApiError('NOT_FOUND', { status: 404 })
    if (context.disposal.revision !== source.disposal.revision || body.asset_revision !== source.asset.revision) throw new ApiError('REVISION_CONFLICT', { status: 409 })
    const before = structuredClone(source.disposal), replacement = body.replacement === null ? null : { item_unit_id: id, ...body.replacement, note: body.replacement.note ?? '',
      disposed_at: canonicalInstant(body.replacement.disposed_at) === canonicalInstant(before.current?.disposed_at ?? before.original.disposed_at) ? before.current?.disposed_at ?? before.original.disposed_at : canonicalInstant(body.replacement.disposed_at)! }
    const after = { ...before, revision: before.revision + 1, current: replacement }
    const base = { id: `correction-${source.corrections.length}`, revision: 1 as const, item_unit_id: id, reason: body.reason, created_at: instant, before }
    const event: finance.DisposalCorrection = replacement === null ? { ...base, action: 'void', after: { ...after, current: null } } : { ...base, action: 'replace', after: { ...after, current: replacement } }
    source = { ...source, item: { ...source.item, revision: source.item.revision + 1 }, asset: { ...source.asset, revision: source.asset.revision + 1 }, disposal: after, corrections: [...source.corrections, event] }
    receipts.set(context.idempotencyKey, structuredClone(event)); return event
  })
})
afterEach(() => { for (const dispose of disposers.splice(0)) dispose() })
function mount(path = '/assets/unit/disposal-corrections', state?: unknown, prepare?: (client: ReturnType<typeof createQueryClient>) => void) {
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
async function reviewVoid() { select(text.action, 'void'); fill(text.reason, '  Exact reason é\n'); await click(text.review) }
async function confirm(work: ReturnType<typeof mount>, label: string) {
  const dialog = await screen.findByRole('alertdialog'); await userEvent.setup().click(within(dialog).getByRole('button', { name: label }))
  await waitFor(() => expect(work.router.state.location.pathname).toBe('/assets/unit/disposal-corrections'))
  await screen.findByRole('heading', { level: 1, name: text.title }); await screen.findByText(text.done, { exact: true })
}

it('shows the retained original, actual current decision, proof scopes and empty history without inventing a financial write', async () => {
  mount('/assets/unit/disposal-corrections', { assetList: '/assets?currency=JPY', assetCursors: [null] }); await ready()
  expect(screen.getByRole('heading', { name: text.original })).toBeVisible(); expect(screen.getByRole('heading', { name: text.current })).toBeVisible()
  expect(screen.getByText(text.noHistory)).toBeVisible(); expect(screen.getAllByText(/200 JPY/)).toHaveLength(2)
  expect(screen.getAllByText(new RegExp(`${text.disposalProof}9`)).length).toBeGreaterThan(0); expect(screen.getAllByText(text.contextHelp).length).toBeGreaterThan(0)
  expect(screen.getByRole('link', { name: asset.back })).toHaveAttribute('href', '/assets?currency=JPY')
  expect(screen.getByRole('combobox', { name: label(text.action) })).toHaveValue(''); await click(text.review)
  expect(screen.getByRole('combobox', { name: label(text.action) })).toHaveFocus(); expect(auth.fetchCSRF).not.toHaveBeenCalled(); expect(finance.correctDisposal).not.toHaveBeenCalled()
})
it('voids only finance after explicit review and displays the actual retained decision and history', async () => {
  const work = mount(); await ready(); await reviewVoid()
  const dialog = await screen.findByRole('alertdialog'); expect(within(dialog).getByRole('heading', { name: text.reviewTitle })).toHaveFocus()
  expect(within(dialog).getByText(text.voidHelp)).toBeVisible(); expect(within(dialog).getByText(text.voided)).toBeVisible(); expect(finance.correctDisposal).not.toHaveBeenCalled()
  await confirm(work, text.confirmVoid); await waitFor(() => expect(within(screen.getByRole('heading', { name: text.current }).closest('section')!).getByText(text.voided)).toBeVisible()); expect(source.disposal.current).toBeNull(); expect(source.disposal.original).toEqual(original.disposal.original)
  expect(source.unit).toEqual(original.unit); expect(source.corrections).toHaveLength(1)
  expect(finance.correctDisposal).toHaveBeenCalledExactlyOnceWith('unit', { asset_revision: 7, action: 'void', replacement: null, reason: '  Exact reason é\n' }, expect.objectContaining({ asset: original.asset, disposal: original.disposal }))
  expect(vi.mocked(finance.correctDisposal).mock.calls[0]![2]).not.toHaveProperty('unit')
  expect(screen.getByRole('combobox', { name: label(text.action) }).querySelector('option[value="void"]')).toBeDisabled()
})
it('requires deliberate non-sale zero and exact date, then replaces without a physical proof', async () => {
  const work = mount(); await ready(); select(text.action, 'replace'); fill(text.reason, 'Correct proceeds'); select(text.kind, 'donated')
  expect(screen.getByLabelText(label(text.proceeds))).toHaveValue(''); expect(screen.getByLabelText(label(text.disposedAt))).toHaveValue('')
  await click(text.review); await waitFor(() => expect(screen.getByLabelText(label(text.proceeds))).toHaveFocus()); expect(finance.correctDisposal).not.toHaveBeenCalled()
  fill(text.proceeds, '1'); fill(text.disposedAt, instant); await click(text.review); await waitFor(() => expect(screen.getByLabelText(label(text.proceeds))).toHaveFocus())
  fill(text.proceeds, '0'); fill(text.note, 'Exact replacement\n'); await click(text.review); await confirm(work, text.confirmReplace)
  expect(source.disposal.current).toMatchObject({ kind: 'donated', realized_proceeds_minor: 0, disposed_at: instant, note: 'Exact replacement\n' }); expect(source.unit).toEqual(original.unit)
  expect(finance.correctDisposal).toHaveBeenCalledWith('unit', expect.objectContaining({ asset_revision: 7, action: 'replace', replacement: { kind: 'donated', currency: 'JPY', realized_proceeds_minor: 0, disposed_at: instant, note: 'Exact replacement\n' } }), expect.anything())
})
it('explicitly reinstates a previously voided record without mistaking null for zero or no history', async () => {
  const before = source.disposal, after = { ...before, revision: 10, current: null } as const
  source = { ...source, disposal: after, corrections: [{ id: 'prior', revision: 1, item_unit_id: 'unit', action: 'void', reason: 'Earlier void', created_at: instant, before, after }] }
  const work = mount(); await ready(); expect(within(screen.getByRole('heading', { name: text.current }).closest('section')!).getByText(text.voided)).toBeVisible(); expect(screen.getByText(`${text.historyCount}1`)).toBeVisible()
  select(text.action, 'replace'); fill(text.reason, 'Reinstate explicitly'); select(text.kind, 'sold'); fill(text.proceeds, '0'); fill(text.disposedAt, '2026-11-07T02:00:00.123456789Z')
  await click(text.review); await confirm(work, text.confirmReinstate)
  expect(source.disposal.current).toMatchObject({ realized_proceeds_minor: 0, disposed_at: instant }); expect(source.corrections).toHaveLength(2); expect(source.unit).toEqual(original.unit)
})
it('requires explicit whole-interpretation adoption while retaining invalid draft money and reason', async () => {
  mount(); await ready(); select(text.action, 'replace'); fill(text.reason, 'Preserve reason'); select(text.kind, 'sold'); fill(text.proceeds, '1.5'); fill(text.disposedAt, instant)
  source = { ...source, item: { ...source.item, revision: 12 }, unit: { ...source.unit, revision: 14, notes: 'Changed physical notes' }, asset: { ...source.asset, revision: 8, manual_acquisition_minor: 200 } }
  await click(asset.refresh); await screen.findByText(text.interpretationChanged); expect(screen.getByText(JSON.stringify('Original physical notes'))).toBeVisible(); expect(screen.getAllByText(JSON.stringify('Changed physical notes')).length).toBeGreaterThan(0); expect(screen.getByRole('button', { name: text.keepEdits })).toBeDisabled()
  await userEvent.setup().click(screen.getByRole('checkbox', { name: text.acknowledge })); await click(text.keepEdits)
  expect(screen.getByLabelText(label(text.proceeds))).toHaveValue('1.5'); expect(screen.getByLabelText(label(text.reason))).toHaveValue('Preserve reason'); expect(screen.queryByText(text.changed)).not.toBeInTheDocument()
  await click(text.review); await waitFor(() => expect(screen.getByLabelText(label(text.proceeds))).toHaveFocus()); expect(finance.correctDisposal).not.toHaveBeenCalled()
})
it('retains an uncertain original request after its current source disappears and retries only by explicit original key', async () => {
  const send = vi.mocked(finance.correctDisposal).getMockImplementation()!
  vi.mocked(finance.correctDisposal).mockImplementationOnce(async (...args) => { await send(...args); missing = true; throw new ApiError('INVALID_RESPONSE') })
  const work = mount(); await ready(); await reviewVoid()
  await userEvent.setup().click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: text.confirmVoid }))
  await screen.findByText(messages.mutations.uncertain, { selector: '[role="alert"] *' }); await waitFor(() => expect(models.readDisposalCorrection).toHaveBeenCalledTimes(2))
  expect(finance.correctDisposal).toHaveBeenCalledTimes(1); expect(source.disposal.current).toBeNull()
  await userEvent.setup().click(within(screen.getByRole('alertdialog')).getByRole('button', { name: text.cancel })); await click(text.openOriginal)
  await act(async () => { window.dispatchEvent(new Event('online')) }); expect(finance.correctDisposal).toHaveBeenCalledTimes(1)
  await userEvent.setup().click(within(screen.getByRole('alertdialog')).getByRole('button', { name: text.retryOriginal }))
  await waitFor(() => expect(finance.correctDisposal).toHaveBeenCalledTimes(2)); await waitFor(() => expect(work.router.state.location.pathname).toBe('/assets/unit/disposal-corrections'))
  const calls = vi.mocked(finance.correctDisposal).mock.calls
  expect(calls[1]![0]).toBe(calls[0]![0]); expect(calls[1]![1]).toEqual(calls[0]![1]); expect(calls[1]![2]).toMatchObject({ idempotencyKey: calls[0]![2].idempotencyKey, asset: calls[0]![2].asset, disposal: calls[0]![2].disposal })
  expect(source.corrections).toHaveLength(1); expect(source.unit).toEqual(original.unit)
})
it('renders every selected history decision in actual subject-revision order rather than entry-ID order', async () => {
  let before = source.disposal
  const entries: finance.DisposalCorrection[] = Array.from({ length: 205 }, (_, index) => {
    const current = { ...before.original, note: `Decision ${index}` }, after = { ...before, revision: before.revision + 1, current }
    const entry: finance.DisposalCorrection = { id: `event-${String(204 - index).padStart(3, '0')}`, revision: 1, item_unit_id: 'unit', reason: `Reason ${index}`, created_at: instant, before, action: 'replace', after }
    before = after; return entry
  })
  source = { ...source, disposal: before, corrections: entries.reverse() }; mount(); await ready()
  expect(screen.getByText(`${text.historyCount}205`)).toBeVisible()
  const history = screen.getByRole('heading', { name: text.history }).closest('section')!, summaries = history.querySelectorAll('summary')
  expect(summaries).toHaveLength(205); expect(summaries[0]).toHaveTextContent(`${text.afterVersion}：10`); expect(summaries[204]).toHaveTextContent(`${text.afterVersion}：214`)
  expect(finance.correctDisposal).not.toHaveBeenCalled()
})
it('waits for the first current read instead of initializing from cached financial proofs', async () => {
  let release: ((value: models.DisposalCorrectionSource) => void) | undefined
  vi.mocked(models.readDisposalCorrection).mockImplementationOnce(() => new Promise((resolve) => { release = resolve }))
  mount('/assets/unit/disposal-corrections', undefined, (client) => client.setQueryData(['assets', 'disposal-corrections', 'unit'], { ...original, asset: { ...original.asset, revision: 6 } }))
  await waitFor(() => expect(models.readDisposalCorrection).toHaveBeenCalledTimes(1))
  expect(screen.queryByRole('button', { name: text.review })).not.toBeInTheDocument()
  await act(async () => { release?.(structuredClone(original)) }); await ready()
  expect(screen.getAllByText(new RegExp(`${text.assetProof}7`)).length).toBeGreaterThan(0); expect(screen.queryByText(new RegExp(`${text.assetProof}6`))).not.toBeInTheDocument()
})
it('preserves raw input through a failed current read and requires explicit dirty-navigation consent', async () => {
  mount(); await ready(); select(text.action, 'replace'); fill(text.reason, 'Draft reason'); select(text.kind, 'sold'); fill(text.proceeds, '1.5')
  vi.mocked(models.readDisposalCorrection).mockRejectedValueOnce(new ApiError('NETWORK_ERROR'))
  await click(asset.refresh); await screen.findByText(asset.failed)
  expect(screen.getByLabelText(label(text.proceeds))).toHaveValue('1.5'); expect(screen.getByLabelText(label(text.reason))).toHaveValue('Draft reason')
  expect(screen.getByRole('button', { name: text.review })).toBeDisabled(); expect(finance.correctDisposal).not.toHaveBeenCalled()
  await userEvent.setup().click(screen.getByRole('link', { name: asset.backDetail }))
  const dialog = await screen.findByRole('alertdialog'); expect(within(dialog).getByText(text.leaveHelp)).toBeVisible()
  await userEvent.setup().click(within(dialog).getByRole('button', { name: text.stay })); expect(screen.getByLabelText(label(text.proceeds))).toHaveValue('1.5')
})
it('does not claim history absence for a real 404 and keeps safe return scope through login', async () => {
  missing = true; const first = mount(); expect(await screen.findByRole('heading', { name: text.missing })).toBeVisible(); expect(screen.getByText(text.missingHelp)).toBeVisible(); expect(finance.correctDisposal).not.toHaveBeenCalled()
  first.unmount(); first.router.dispose(); first.client.clear(); disposers.pop()
  administrator = null; const second = mount('/assets/unit/disposal-corrections', { assetList: '/assets?currency=JPY', assetCursors: [null], reason: 'private', key: 'private' })
  await waitFor(() => expect(second.router.state.location.pathname).toBe('/login'))
  expect(second.router.state.location.state).toMatchObject({ returnTo: '/assets/unit/disposal-corrections', assetList: '/assets?currency=JPY', assetCursors: [null] })
  expect(second.router.state.location.state).not.toHaveProperty('reason'); expect(second.router.state.location.state).not.toHaveProperty('key')
})
