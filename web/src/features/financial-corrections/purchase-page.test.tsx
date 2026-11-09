import { QueryClientProvider } from '@tanstack/react-query'
import { createMemoryRouter } from 'react-router'
import { RouterProvider } from 'react-router/dom'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeAll, beforeEach, expect, it, vi } from 'vitest'
import App from '@/App'
import * as auth from '@/lib/api/auth'
import * as catalog from '@/lib/api/catalog'
import { ApiError } from '@/lib/api/client'
import * as finance from '@/lib/api/financial-corrections'
import type { Unit } from '@/lib/api/holdings'
import * as locations from '@/lib/api/locations'
import type { Purchase, PurchaseLine } from '@/lib/api/purchases'
import * as taxonomy from '@/lib/api/taxonomy'
import { messages } from '@/lib/messages'
import { createQueryClient } from '@/lib/query'
import * as models from '@/features/purchases/correction-model'

vi.mock('@/lib/api/auth', async (original) => ({ ...await original<typeof import('@/lib/api/auth')>(), fetchAdmin: vi.fn(), fetchCSRF: vi.fn(), login: vi.fn() }))
vi.mock('@/lib/api/catalog', async (original) => ({ ...await original<typeof import('@/lib/api/catalog')>(), fetchItemsPage: vi.fn() }))
vi.mock('@/lib/api/locations', async (original) => ({ ...await original<typeof import('@/lib/api/locations')>(), fetchLocationsPage: vi.fn() }))
vi.mock('@/lib/api/taxonomy', async (original) => ({ ...await original<typeof import('@/lib/api/taxonomy')>(), fetchCategoriesPage: vi.fn() }))
vi.mock('@/features/purchases/correction-model', async (original) => ({ ...await original<typeof import('@/features/purchases/correction-model')>(), readPurchaseCorrection: vi.fn(), readPurchaseCorrectionHistory: vi.fn() }))
vi.mock('@/lib/api/financial-corrections', async (original) => ({ ...await original<typeof import('@/lib/api/financial-corrections')>(), correctPurchaseLine: vi.fn() }))

const text = messages.purchaseCorrections, purchaseText = messages.purchases, cat = messages.catalog, historyText = messages.purchaseCorrectionHistory
const owner: auth.Admin = { id: 'owner', revision: 2, username: 'owner', must_change_password: false }
const originalPurchase: Purchase = { id: 'purchase', revision: 12, merchant_name: 'Original merchant', purchased_at: '2026-11-07T10:00:00.123456789+08:00', currency: 'JPY', receipt_note: 'Original receipt' }
const oldItem: catalog.Item = { id: 'old', revision: 7, name: 'Original individual', brand: null, model: '', category_id: null, description: '', notes: '', status: 'active', tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1 }
const quantity: catalog.Item = { ...oldItem, id: 'quantity', revision: 9, name: 'Quantity replacement', tracking_mode: 'quantity', unit_code: 'kg', base_unit: 'g', scale_factor: 1000 }
const individual: catalog.Item = { ...oldItem, id: 'individual', revision: 11, name: 'Individual replacement' }
const originalUnit: Unit = { id: 'old-unit', item_id: oldItem.id, revision: 3, status: 'disposed', location_id: null, asset_code: 'Old physical', serial_number: null, notes: 'Original Unit' }
const targetUnit: Unit = { ...originalUnit, id: 'target-unit', item_id: individual.id, status: 'stored', asset_code: 'New physical' }
const originalLine: PurchaseLine = { id: 'line/legacy', revision: 4, purchase_id: originalPurchase.id, item_id: oldItem.id, item_unit_id: originalUnit.id, quantity_base: 3, paid_minor: 0, note: 'Legacy original' }
let administrator: auth.Admin | null, purchase: Purchase, items: Map<string, catalog.Item>, units: Map<string, Unit>, lines: Map<string, PurchaseLine>, corrections: finance.PurchaseLineCorrection[], receipts: Map<string, finance.PurchaseLineCorrection>, missing: boolean
const disposers: (() => void)[] = []
const label = (value: string) => new RegExp(`^${value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}`)
const field = (name: string) => screen.getByLabelText(label(name))
function fill(name: string, value: string) { fireEvent.change(field(name), { target: { value } }) }
function select(name: string, value: string) { fireEvent.change(screen.getByRole('combobox', { name: label(name) }), { target: { value } }) }
const click = (name: string) => userEvent.setup().click(screen.getByRole('button', { name }))
function page<T>(rows: T[], query: { cursor?: string; limit?: number }) { const start = Number(query.cursor ?? 0), end = start + (query.limit ?? 50); return { data: rows.slice(start, end), next_cursor: rows.length > end ? String(end) : null } }
function currentSource(lineID: string, targetID: string | null): models.PurchaseCorrectionSource {
  const line = lines.get(lineID), item = line && items.get(line.item_id)
  if (missing || !line || !item) throw new ApiError('NOT_FOUND', { status: 404 })
  const original = line.item_unit_id === null ? { kind: 'unallocated' as const } : line.item_unit_id.includes('/') ? { kind: 'unaddressable' as const, id: line.item_unit_id } : { kind: 'resolved' as const, unit: units.get(line.item_unit_id)! }
  const selected = targetID === null ? undefined : items.get(targetID)
  if (targetID !== null && !selected) throw new ApiError('NOT_FOUND', { status: 404 })
  const replacement: models.ReplacementCorrectionSource = !selected ? { kind: 'none' } : selected.tracking_mode === 'quantity' ? { kind: 'quantity', item: selected } : { kind: 'individual', item: selected, units: [...units.values()].filter((unit) => unit.item_id === selected.id) }
  return structuredClone({ purchase, lines: [...lines.values()], corrections, line, item, originalUnit: original, replacement })
}
function bumpItem(id: string, changes: Partial<catalog.Item> = {}) { const value = items.get(id)!; items.set(id, { ...value, ...changes, revision: value.revision + 1 }) }

beforeAll(async () => { await Promise.all([import('./purchase-page'), import('./purchase-history-page'), import('@/features/auth/account-page')]) })
beforeEach(() => {
  vi.clearAllMocks(); administrator = { ...owner }; purchase = { ...originalPurchase }; items = new Map([[oldItem.id, { ...oldItem }], [quantity.id, { ...quantity }], [individual.id, { ...individual }]]); units = new Map([[originalUnit.id, { ...originalUnit }], [targetUnit.id, { ...targetUnit }]]); lines = new Map([[originalLine.id, { ...originalLine }]]); corrections = []; receipts = new Map(); missing = false
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: true }); vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
  vi.mocked(auth.fetchAdmin).mockReset().mockImplementation(async () => { if (!administrator) throw new ApiError('AUTHENTICATION_REQUIRED', { status: 401 }); return administrator })
  vi.mocked(auth.fetchCSRF).mockReset().mockResolvedValue('C'.repeat(43)); vi.mocked(auth.login).mockReset().mockImplementation(async () => { administrator = { ...owner }; return administrator })
  vi.mocked(taxonomy.fetchCategoriesPage).mockReset().mockResolvedValue({ data: [], next_cursor: null }); vi.mocked(locations.fetchLocationsPage).mockReset().mockResolvedValue({ data: [], next_cursor: null })
  vi.mocked(catalog.fetchItemsPage).mockReset().mockImplementation(async (query) => page([...items.values()].filter((item) => !query.q || item.name.includes(query.q)), query))
  vi.mocked(models.readPurchaseCorrection).mockReset().mockImplementation(async (_id, lineID, targetID) => currentSource(lineID, targetID))
  vi.mocked(models.readPurchaseCorrectionHistory).mockReset().mockImplementation(async () => structuredClone({ purchase, lines: [...lines.values()], corrections }))
  vi.mocked(finance.correctPurchaseLine).mockReset().mockImplementation(async (id, body, context) => {
    const receipt = receipts.get(context.idempotencyKey); if (receipt) return structuredClone(receipt)
    const line = lines.get(body.line_id), item = line && items.get(line.item_id)
    if (!line) throw new ApiError('PURCHASE_LINE_VOIDED', { status: 409 })
    if (!item || context.purchase.revision !== purchase.revision || body.line_revision !== line.revision || body.item_revision !== item.revision || body.replacement && items.get(body.replacement.item_id)?.revision !== body.replacement.item_revision) throw new ApiError('REVISION_CONFLICT', { status: 409 })
    const after = body.replacement === null ? null : { ...body.replacement, id: `replacement-${corrections.length}`, revision: 1 as const, purchase_id: id, note: body.replacement.note ?? '' }
    const base = { id: `correction-${corrections.length}`, revision: 1 as const, purchase_id: id, reason: body.reason, created_at: originalPurchase.purchased_at, before: line }
    const entry: finance.PurchaseLineCorrection = after === null ? { ...base, action: 'void', after: null } : { ...base, action: 'replace', after }
    lines.delete(line.id); if (after) lines.set(after.id, after); corrections = [...corrections, entry]; purchase = { ...purchase, revision: purchase.revision + 1 }
    bumpItem(item.id); if (after && after.item_id !== item.id) bumpItem(after.item_id)
    receipts.set(context.idempotencyKey, structuredClone(entry)); return structuredClone(entry)
  })
})
afterEach(() => { for (const dispose of disposers.splice(0)) dispose() })
function mount(path = '/purchases/purchase/line-corrections/edit?line=line%2Flegacy', state?: unknown, prepare?: (client: ReturnType<typeof createQueryClient>) => void) {
  const client = createQueryClient(); prepare?.(client)
  const [pathname, search = ''] = path.split('?'), router = createMemoryRouter([{ path: '*', element: <App /> }], { initialEntries: [{ pathname, search: search ? `?${search}` : '', state }] })
  const view = render(<QueryClientProvider client={client}><RouterProvider router={router} /></QueryClientProvider>)
  disposers.push(() => { view.unmount(); router.dispose(); client.clear() }); return { ...view, client, router }
}
async function ready() { await waitFor(() => expect(screen.getByRole('button', { name: text.review })).toBeEnabled()) }
async function chooseTarget(item: catalog.Item) {
  await userEvent.setup().click(screen.getByText(text.picker, { exact: true }))
  const button = await screen.findByRole('button', { name: `${text.chooseItem}：${JSON.stringify(item.id)}` }); await waitFor(() => expect(button).toBeEnabled()); await userEvent.setup().click(button)
  await screen.findByText(text.interpretationChanged); expect(screen.getByRole('button', { name: text.keepEdits })).toBeDisabled()
  await userEvent.setup().click(screen.getByRole('checkbox', { name: text.acknowledge })); await click(text.keepEdits); await ready()
}
async function confirm(work: ReturnType<typeof mount>, name: string) {
  const dialog = await screen.findByRole('alertdialog'); await userEvent.setup().click(within(dialog).getByRole('button', { name }))
  await waitFor(() => expect(work.router.state.location.pathname).toBe('/purchases/purchase/line-corrections')); await screen.findByRole('heading', { level: 1, name: historyText.title }); await waitFor(() => expect(screen.getByText(purchaseText.done, { exact: true })).toBeVisible())
}
function fillReplacement(named = false) {
  select(purchaseText.fields.allocation, named ? 'unit' : 'none'); if (named) select(purchaseText.fields.item_unit_id, targetUnit.id)
  fill(purchaseText.fields.quantity_base, named ? '1' : '1500'); fill(purchaseText.fields.paid_minor, '0'); fill(purchaseText.fields.note, 'Exact replacement\n')
}

it('requires explicit review and voids the exact legacy Line without a physical proof', async () => {
  const work = mount(); await ready(); expect(screen.getByRole('combobox', { name: label(text.action) })).toHaveValue('')
  await click(text.review); expect(screen.getByRole('combobox', { name: label(text.action) })).toHaveFocus(); expect(finance.correctPurchaseLine).not.toHaveBeenCalled()
  select(text.action, 'void'); fill(text.reason, '  Exact void reason é\n'); await click(text.review)
  const dialog = await screen.findByRole('alertdialog'); expect(within(dialog).getByRole('heading', { name: text.reviewTitle })).toHaveFocus(); expect(dialog).toHaveTextContent(originalLine.id); expect(dialog).toHaveTextContent(text.voidHelp)
  await confirm(work, text.confirmVoid)
  expect(finance.correctPurchaseLine).toHaveBeenCalledExactlyOnceWith('purchase', { line_id: originalLine.id, line_revision: 4, item_revision: 7, action: 'void', replacement: null, reason: '  Exact void reason é\n' }, expect.objectContaining({ purchase: originalPurchase, line: originalLine, item: oldItem, replacementItem: null }))
  expect(vi.mocked(finance.correctPurchaseLine).mock.calls[0]![2]).not.toHaveProperty('unit'); expect(units.get(originalUnit.id)).toEqual(originalUnit); expect(lines.size).toBe(0); expect(corrections).toHaveLength(1); expect(await screen.findByText(`${historyText.historyCount}1`)).toBeVisible()
})
it('selects and explicitly adopts a complete quantity target while retaining a draft', async () => {
  const work = mount(); await ready(); select(text.action, 'replace'); fill(text.reason, 'Preserved reason'); await click(text.review); expect(field(text.targetID)).toHaveFocus()
  await chooseTarget(quantity); expect(field(text.reason)).toHaveValue('Preserved reason'); expect(field(text.targetID)).toHaveValue(quantity.id)
  fillReplacement(); await click(text.review); expect(finance.correctPurchaseLine).not.toHaveBeenCalled(); const dialog = await screen.findByRole('alertdialog'); expect(dialog).toHaveTextContent('1500'); expect(dialog).toHaveTextContent('0 JPY')
  await confirm(work, text.confirmReplace); expect(finance.correctPurchaseLine).toHaveBeenCalledWith('purchase', expect.objectContaining({ line_id: originalLine.id, line_revision: 4, item_revision: 7, action: 'replace', replacement: { item_id: quantity.id, item_revision: 9, item_unit_id: null, quantity_base: 1500, paid_minor: 0, note: 'Exact replacement\n' } }), expect.objectContaining({ replacementItem: quantity }))
  expect(units.get(originalUnit.id)).toEqual(originalUnit); expect([...lines.values()][0]).toMatchObject({ item_id: quantity.id, paid_minor: 0 })
})
it('pages and resets actual replacement discovery without changing the separate Purchase return scope', async () => {
  const work = mount(undefined, { purchaseList: '/purchases?currency=JPY&limit=1', purchaseCursors: [null] }); await ready()
  await userEvent.setup().click(screen.getByText(text.picker, { exact: true })); await screen.findByRole('button', { name: `${text.chooseItem}：${JSON.stringify(oldItem.id)}` })
  await userEvent.setup().click(screen.getByText(cat.advanced, { exact: true })); fill(cat.pageSize, '1'); await click(cat.apply)
  await waitFor(() => expect(screen.getByRole('button', { name: cat.nextPage })).toBeEnabled()); await click(cat.nextPage)
  expect(await screen.findByRole('button', { name: `${text.chooseItem}：${JSON.stringify(quantity.id)}` })).toBeVisible(); expect(screen.getByRole('button', { name: cat.previousPage })).toBeEnabled()
  fill(cat.search, 'Individual'); await click(cat.apply); await waitFor(() => expect(screen.getByRole('button', { name: cat.previousPage })).toBeDisabled())
  await click(cat.reset); await waitFor(() => expect(field(cat.search)).toHaveValue('')); expect(screen.getByRole('button', { name: cat.previousPage })).toBeDisabled()
  expect(work.router.state.location.state.purchaseList).toBe('/purchases?currency=JPY&limit=1'); expect(work.router.state.location.search).toBe('?line=line%2Flegacy'); expect(finance.correctPurchaseLine).not.toHaveBeenCalled()
})
it('withholds a replacement page when its continuation returns to an earlier page', async () => {
  vi.mocked(catalog.fetchItemsPage).mockImplementation(async (query) => {
    if (!query.limit) return { data: [oldItem], next_cursor: null }
    if (!query.cursor) return { data: [oldItem], next_cursor: 'first' }
    if (query.cursor === 'first') return { data: [quantity], next_cursor: 'second' }
    return { data: [individual], next_cursor: 'first' }
  })
  mount(); await ready(); select(text.action, 'replace'); fill(text.reason, 'Preserve draft on a cyclic page')
  await userEvent.setup().click(screen.getByText(text.picker, { exact: true })); await screen.findByRole('button', { name: `${text.chooseItem}：${JSON.stringify(oldItem.id)}` })
  await userEvent.setup().click(screen.getByText(cat.advanced, { exact: true })); fill(cat.pageSize, '1'); await click(cat.apply); await waitFor(() => expect(screen.getByRole('button', { name: cat.nextPage })).toBeEnabled()); await click(cat.nextPage)
  await screen.findByRole('button', { name: `${text.chooseItem}：${JSON.stringify(quantity.id)}` }); await click(cat.nextPage); expect(await screen.findByText(purchaseText.invalidRead)).toBeVisible(); expect(screen.queryByRole('button', { name: `${text.chooseItem}：${JSON.stringify(individual.id)}` })).not.toBeInTheDocument(); expect(field(text.reason)).toHaveValue('Preserve draft on a cyclic page'); expect(finance.correctPurchaseLine).not.toHaveBeenCalled()
  await click(cat.reset); expect(await screen.findByRole('button', { name: `${text.chooseItem}：${JSON.stringify(oldItem.id)}` })).toBeVisible()
})

it('keeps inactive invalid replacement drafts out of an explicit void and its wire proofs', async () => {
  const work = mount(); await ready(); select(text.action, 'replace'); fill(text.reason, 'Void instead'); await chooseTarget(quantity)
  select(purchaseText.fields.allocation, 'none'); fill(purchaseText.fields.quantity_base, '1.5'); fill(purchaseText.fields.paid_minor, '-1'); select(text.action, 'void'); await click(text.review)
  const dialog = await screen.findByRole('alertdialog'); expect(dialog).toHaveTextContent(text.inactiveTarget); expect(dialog).not.toHaveTextContent(text.targetProof)
  await confirm(work, text.confirmVoid); expect(vi.mocked(finance.correctPurchaseLine).mock.calls[0]![1]).toEqual({ line_id: originalLine.id, line_revision: 4, item_revision: 7, action: 'void', replacement: null, reason: 'Void instead' })
  expect(vi.mocked(finance.correctPurchaseLine).mock.calls[0]![2].replacementItem).toBeNull()
})
it('requires the actual selected individual Unit and explicit quantity one', async () => {
  const work = mount(); await ready(); select(text.action, 'replace'); fill(text.reason, 'Named Unit'); await chooseTarget(individual)
  await click(text.review); expect(screen.getByRole('combobox', { name: label(purchaseText.fields.allocation) })).toHaveFocus()
  select(purchaseText.fields.allocation, 'unit'); await click(text.review); expect(screen.getByRole('combobox', { name: label(purchaseText.fields.item_unit_id) })).toHaveFocus(); fillReplacement(true)
  fill(purchaseText.fields.quantity_base, '2'); await click(text.review); expect(field(purchaseText.fields.quantity_base)).toHaveFocus(); expect(finance.correctPurchaseLine).not.toHaveBeenCalled()
  fill(purchaseText.fields.quantity_base, '1'); await click(text.review); expect(screen.getByRole('alertdialog')).toHaveTextContent(targetUnit.asset_code); await confirm(work, text.confirmReplace)
  expect(vi.mocked(finance.correctPurchaseLine).mock.calls[0]![1].replacement).toMatchObject({ item_unit_id: targetUnit.id, quantity_base: 1 }); expect(units.get(targetUnit.id)).toEqual(targetUnit)
})
it('uses one identical reviewed Item snapshot when replacing within the original Item', async () => {
  const work = mount(); await ready(); select(text.action, 'replace'); fill(text.reason, 'Same owner'); await chooseTarget(oldItem)
  select(purchaseText.fields.allocation, 'unit'); select(purchaseText.fields.item_unit_id, originalUnit.id); fill(purchaseText.fields.quantity_base, '1'); fill(purchaseText.fields.paid_minor, '0'); await click(text.review); await confirm(work, text.confirmReplace)
  const [_, body, context] = vi.mocked(finance.correctPurchaseLine).mock.calls[0]!; expect(body.replacement).toMatchObject({ item_id: oldItem.id, item_revision: oldItem.revision, item_unit_id: originalUnit.id, quantity_base: 1 }); expect(context.replacementItem).toEqual(context.item)
})
it('keeps invalid count, money and Unit choice while explicitly changing the whole target interpretation', async () => {
  mount(); await ready(); select(text.action, 'replace'); fill(text.reason, 'Keep raw text'); await chooseTarget(individual); fillReplacement(true)
  fill(purchaseText.fields.quantity_base, '1.5'); fill(purchaseText.fields.paid_minor, '9007199254740992'); await chooseTarget(quantity)
  expect(field(purchaseText.fields.quantity_base)).toHaveValue('1.5'); expect(field(purchaseText.fields.paid_minor)).toHaveValue('9007199254740992'); expect(field(text.reason)).toHaveValue('Keep raw text')
  expect(screen.getByRole('combobox', { name: label(purchaseText.fields.allocation) })).toHaveValue('unit'); await click(text.review); expect(finance.correctPurchaseLine).not.toHaveBeenCalled()
})
it('keeps drafts when a selected target becomes unavailable and permits explicit clearing', async () => {
  const work = mount(); await ready(); select(text.action, 'replace'); fill(text.reason, 'Keep draft')
  vi.mocked(models.readPurchaseCorrection).mockImplementation(async (_id, lineID, targetID) => { if (targetID === quantity.id) throw new ApiError('NOT_FOUND', { status: 404 }); return currentSource(lineID, targetID) })
  await userEvent.setup().click(screen.getByText(text.picker, { exact: true })); await userEvent.setup().click(await screen.findByRole('button', { name: `${text.chooseItem}：${JSON.stringify(quantity.id)}` }))
  expect(await screen.findByText(text.targetUnavailable)).toBeVisible(); expect(field(text.reason)).toHaveValue('Keep draft'); expect(screen.getByRole('button', { name: text.review })).toBeDisabled()
  await userEvent.setup().click(screen.getByText(text.picker, { exact: true })); await click(text.clearItem); await ready(); expect(field(text.targetID)).toHaveValue('')
  select(text.action, 'void'); await click(text.review); await confirm(work, text.confirmVoid); expect(corrections).toHaveLength(1)
})
it('retains a removed selected Unit until explicitly resolved', async () => {
  const work = mount(); await ready(); select(text.action, 'replace'); fill(text.reason, 'Keep Unit choice'); await chooseTarget(individual); fillReplacement(true)
  units.delete(targetUnit.id); bumpItem(individual.id)
  await act(async () => { await work.client.invalidateQueries({ queryKey: ['purchases'] }) }); expect(await screen.findByText(text.changed)).toBeVisible(); await click(text.keepEdits)
  const selected = screen.getByRole('combobox', { name: label(purchaseText.fields.item_unit_id) }); expect(selected).toHaveValue(targetUnit.id); expect(screen.getByRole('option', { name: new RegExp(purchaseText.unavailableUnit) })).toBeVisible()
  await click(text.review); expect(selected).toHaveFocus(); expect(finance.correctPurchaseLine).not.toHaveBeenCalled()
  select(purchaseText.fields.allocation, 'none'); await click(text.review); await confirm(work, text.confirmReplace); expect(vi.mocked(finance.correctPurchaseLine).mock.calls[0]![1].replacement).toMatchObject({ item_unit_id: null, quantity_base: 1 })
})
it('requires manual comparison and acknowledgement for a genuinely changed original interpretation', async () => {
  const work = mount(); await ready(); select(text.action, 'void'); fill(text.reason, 'Keep reason'); bumpItem(oldItem.id, { unit_code: 'box', base_unit: 'pcs', scale_factor: 2 })
  await act(async () => { await work.client.invalidateQueries({ queryKey: ['purchases'] }) }); expect(await screen.findByText(text.interpretationChanged)).toBeVisible(); expect(field(text.reason)).toHaveValue('Keep reason')
  expect(screen.getByRole('button', { name: text.keepEdits })).toBeDisabled(); await userEvent.setup().click(screen.getByRole('checkbox', { name: text.acknowledge })); await click(text.keepEdits); await click(text.review); await confirm(work, text.confirmVoid)
  expect(vi.mocked(finance.correctPurchaseLine).mock.calls[0]![1].item_revision).toBe(8)
})
it('disables an open new-key review when its full original source changes', async () => {
  const work = mount(); await ready(); select(text.action, 'void'); fill(text.reason, 'Original review'); await click(text.review)
  bumpItem(oldItem.id, { notes: 'Concurrent metadata' }); await act(async () => { await work.client.invalidateQueries({ queryKey: ['purchases'] }) })
  const dialog = screen.getByRole('alertdialog'); expect(await within(dialog).findByText(text.reviewStale)).toBeVisible(); expect(within(dialog).getByRole('button', { name: text.confirmVoid })).toBeDisabled(); expect(finance.correctPurchaseLine).not.toHaveBeenCalled()
  await userEvent.setup().click(within(dialog).getByRole('button', { name: text.cancel })); expect(await screen.findByText(text.changed)).toBeVisible(); expect(field(text.reason)).toHaveValue('Original review')
})
it('retains an uncertain original command after the selected Line disappears and retries only by original key', async () => {
  const send = vi.mocked(finance.correctPurchaseLine).getMockImplementation()!
  vi.mocked(finance.correctPurchaseLine).mockImplementationOnce(async (...args) => { await send(...args); throw new ApiError('INVALID_RESPONSE') })
  const work = mount(); await ready(); select(text.action, 'void'); fill(text.reason, 'Reconcile original'); await click(text.review)
  await userEvent.setup().click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: text.confirmVoid }))
  await screen.findByRole('heading', { name: text.uncertain }); await waitFor(() => expect(screen.getByText(text.missing)).toBeVisible())
  await userEvent.setup().click(within(screen.getByRole('alertdialog')).getByRole('button', { name: text.cancel })); await click(text.openOriginal)
  await act(async () => { window.dispatchEvent(new Event('online')) }); expect(finance.correctPurchaseLine).toHaveBeenCalledTimes(1)
  await userEvent.setup().click(within(screen.getByRole('alertdialog')).getByRole('button', { name: text.retryOriginal })); await waitFor(() => expect(work.router.state.location.pathname).toBe('/purchases/purchase/line-corrections'))
  const calls = vi.mocked(finance.correctPurchaseLine).mock.calls; expect(calls[1]![1]).toEqual(calls[0]![1]); expect(calls[1]![2]).toMatchObject({ idempotencyKey: calls[0]![2].idempotencyKey, purchase: calls[0]![2].purchase, line: calls[0]![2].line, item: calls[0]![2].item }); expect(corrections).toHaveLength(1)
})
it('preserves volatile offline drafts and requires consent before dirty navigation', async () => {
  mount(); await ready(); select(text.action, 'void'); fill(text.reason, 'Offline draft')
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: false }); act(() => { window.dispatchEvent(new Event('offline')) })
  expect(await screen.findByText(text.offline)).toBeVisible(); expect(field(text.reason)).toHaveValue('Offline draft'); expect(screen.getByRole('button', { name: text.review })).toBeDisabled()
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: true }); act(() => { window.dispatchEvent(new Event('online')) }); await ready(); expect(finance.correctPurchaseLine).not.toHaveBeenCalled()
  await userEvent.setup().click(screen.getByRole('link', { name: text.backHistory })); const dialog = await screen.findByRole('alertdialog'); expect(within(dialog).getByText(text.leaveHelp)).toBeVisible(); await userEvent.setup().click(within(dialog).getByRole('button', { name: text.stay })); expect(field(text.reason)).toHaveValue('Offline draft')
})
it('purges the volatile draft if a replacement discovery read loses its session', async () => {
  const work = mount(); await ready(); select(text.action, 'void'); fill(text.reason, 'Private draft')
  vi.mocked(catalog.fetchItemsPage).mockRejectedValueOnce(new ApiError('AUTHENTICATION_REQUIRED', { status: 401 }))
  await userEvent.setup().click(screen.getByText(text.picker, { exact: true })); await waitFor(() => expect(work.router.state.location.pathname).toBe('/login'))
  expect(work.router.state.location.state).not.toHaveProperty('reason'); expect(work.client.getQueryData(['purchases', 'line-correction', 'purchase', originalLine.id, null])).toBeUndefined(); expect(finance.correctPurchaseLine).not.toHaveBeenCalled()
})
it('keeps only the exact safe Line identity and read scope through session loss', async () => {
  administrator = null; const work = mount(undefined, { purchaseList: '/purchases?currency=JPY', purchaseCursors: [null], reason: 'private', key: 'private' })
  await waitFor(() => expect(work.router.state.location.pathname).toBe('/login')); expect(work.router.state.location.state).toMatchObject({ returnTo: '/purchases/purchase/line-corrections/edit?line=line%2Flegacy', purchaseList: '/purchases?currency=JPY', purchaseCursors: [null] })
  expect(work.router.state.location.state).not.toHaveProperty('reason'); expect(work.router.state.location.state).not.toHaveProperty('key')
})
it('rejects duplicate or financial query fields without reading a fabricated subject', async () => {
  mount('/purchases/purchase/line-corrections/edit?line=line%2Flegacy&reason=private')
  expect(await screen.findByText(messages.locations.validation)).toBeVisible(); expect(models.readPurchaseCorrection).not.toHaveBeenCalled(); expect(finance.correctPurchaseLine).not.toHaveBeenCalled()
})
it('waits for a fresh selected source rather than initializing from cached proofs', async () => {
  let release: ((value: models.PurchaseCorrectionSource) => void) | undefined
  vi.mocked(models.readPurchaseCorrection).mockImplementationOnce(() => new Promise((resolve) => { release = resolve }))
  mount(undefined, undefined, (client) => client.setQueryData(['purchases', 'line-correction', 'purchase', originalLine.id, null], { ...currentSource(originalLine.id, null), purchase: { ...purchase, revision: 11 } }))
  await waitFor(() => expect(models.readPurchaseCorrection).toHaveBeenCalledTimes(1)); expect(screen.queryByRole('button', { name: text.review })).not.toBeInTheDocument()
  await act(async () => { release?.(currentSource(originalLine.id, null)) }); await ready(); expect(screen.getAllByText(new RegExp(`${purchaseText.purchaseProof}12`)).length).toBeGreaterThan(0)
})
