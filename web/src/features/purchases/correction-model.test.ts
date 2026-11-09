import { beforeEach, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/client'
import { fetchItem, type Item } from '@/lib/api/catalog'
import * as finance from '@/lib/api/financial-corrections'
import { fetchItemUnitsPage, fetchUnit, type Unit } from '@/lib/api/holdings'
import * as purchases from '@/lib/api/purchases'
import { readPurchaseCorrection, readPurchaseCorrectionHistory } from './correction-model'

vi.mock('@/lib/api/catalog', async (original) => ({ ...await original<typeof import('@/lib/api/catalog')>(), fetchItem: vi.fn() }))
vi.mock('@/lib/api/holdings', async (original) => ({ ...await original<typeof import('@/lib/api/holdings')>(), fetchItemUnitsPage: vi.fn(), fetchUnit: vi.fn() }))
vi.mock('@/lib/api/purchases', async (original) => ({ ...await original<typeof import('@/lib/api/purchases')>(), fetchPurchase: vi.fn(), fetchPurchaseLinesPage: vi.fn() }))
vi.mock('@/lib/api/financial-corrections', async (original) => ({ ...await original<typeof import('@/lib/api/financial-corrections')>(), fetchPurchaseCorrectionsPage: vi.fn() }))
const item: Item = { id: 'item', revision: 7, name: 'Original Item', brand: null, model: '', category_id: null, description: '', notes: '', tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1, status: 'active' }
const target: Item = { ...item, id: 'target', revision: 9, name: 'Replacement Item' }
const unit: Unit = { id: 'unit', revision: 3, item_id: item.id, location_id: null, asset_code: 'Original Unit', serial_number: null, notes: '', status: 'disposed' }
const targetUnit: Unit = { ...unit, id: 'target-unit', item_id: target.id, revision: 5, asset_code: 'Replacement Unit', status: 'stored' }
const purchase: purchases.Purchase = { id: 'purchase', revision: 12, merchant_name: 'Exact merchant', purchased_at: '2026-11-07T12:00:00.123456789+08:00', currency: 'JPY', receipt_note: '' }
const line: purchases.PurchaseLine = { id: 'line', revision: 4, purchase_id: purchase.id, item_id: item.id, item_unit_id: unit.id, quantity_base: 3, paid_minor: 0, note: 'Retained legacy allocation' }
const instant = '2026-11-07T04:00:00.123456789Z', signal = () => new AbortController().signal
function correction(id: string, before: purchases.PurchaseLine, after: purchases.PurchaseLine | null): finance.PurchaseLineCorrection {
  const base = { id, revision: 1 as const, purchase_id: purchase.id, reason: 'Retained reason', created_at: instant, before }
  return after === null ? { ...base, action: 'void', after: null } : { ...base, action: 'replace', after: { ...after, revision: 1 } }
}
beforeEach(() => {
  vi.mocked(fetchItem).mockReset().mockImplementation(async (id) => structuredClone(id === target.id ? target : item))
  vi.mocked(fetchUnit).mockReset().mockImplementation(async (id) => structuredClone(id === targetUnit.id ? targetUnit : unit))
  vi.mocked(fetchItemUnitsPage).mockReset().mockImplementation(async (id) => ({ data: [structuredClone(id === target.id ? targetUnit : unit)], next_cursor: null }))
  vi.mocked(purchases.fetchPurchase).mockReset().mockImplementation(async () => structuredClone(purchase))
  vi.mocked(purchases.fetchPurchaseLinesPage).mockReset().mockResolvedValue({ data: [line], next_cursor: null })
  vi.mocked(finance.fetchPurchaseCorrectionsPage).mockReset().mockResolvedValue({ data: [], next_cursor: null })
})

it('completes only actual Purchase Lines and correction history between matching parent reads', async () => {
  const abort = signal()
  expect(await readPurchaseCorrectionHistory(purchase.id, abort)).toEqual({ purchase, lines: [line], corrections: [] })
  expect(purchases.fetchPurchase).toHaveBeenCalledTimes(2)
  expect(purchases.fetchPurchaseLinesPage).toHaveBeenCalledExactlyOnceWith(purchase.id, { limit: 100 }, abort)
  expect(finance.fetchPurchaseCorrectionsPage).toHaveBeenCalledExactlyOnceWith(purchase.id, { limit: 100 }, abort)
  expect(fetchItem).not.toHaveBeenCalled(); expect(fetchUnit).not.toHaveBeenCalled(); expect(fetchItemUnitsPage).not.toHaveBeenCalled()
})

it('reviews the selected original and explicit absence of a replacement without inventing a Unit proof', async () => {
  const abort = signal(), source = await readPurchaseCorrection(purchase.id, line.id, null, abort)
  expect(source).toEqual({ purchase, lines: [line], corrections: [], line, item, originalUnit: { kind: 'resolved', unit }, replacement: { kind: 'none' } })
  expect(fetchItem).toHaveBeenCalledTimes(2); expect(fetchUnit).toHaveBeenCalledTimes(2); expect(fetchItemUnitsPage).not.toHaveBeenCalled()
  expect(vi.mocked(purchases.fetchPurchase).mock.invocationCallOrder[0]).toBeLessThan(vi.mocked(fetchItem).mock.invocationCallOrder[0]!)
  expect(vi.mocked(fetchItem).mock.invocationCallOrder[1]).toBeLessThan(vi.mocked(purchases.fetchPurchase).mock.invocationCallOrder[1]!)
})

it('brackets a selected individual replacement separately and retains its complete actual Unit collection', async () => {
  const abort = signal(), source = await readPurchaseCorrection(purchase.id, line.id, target.id, abort)
  expect(source.replacement).toEqual({ kind: 'individual', item: target, units: [targetUnit] })
  expect(fetchItem).toHaveBeenNthCalledWith(1, item.id, abort); expect(fetchItem).toHaveBeenNthCalledWith(2, target.id, abort)
  expect(fetchItem).toHaveBeenNthCalledWith(3, target.id, abort); expect(fetchItem).toHaveBeenNthCalledWith(4, item.id, abort)
  expect(fetchItemUnitsPage).toHaveBeenCalledExactlyOnceWith(target.id, { limit: 100 }, abort)
})

it('does not invent a Unit collection for a quantity replacement', async () => {
  const quantity = { ...target, tracking_mode: 'quantity', unit_code: 'kg', base_unit: 'g', scale_factor: 1000 } as const
  vi.mocked(fetchItem).mockImplementation(async (id) => structuredClone(id === target.id ? quantity : item))
  expect((await readPurchaseCorrection(purchase.id, line.id, target.id, signal())).replacement).toEqual({ kind: 'quantity', item: quantity })
  expect(fetchItemUnitsPage).not.toHaveBeenCalled()
})

it('reuses the same complete Item snapshot and checks its original Unit against the actual selected collection', async () => {
  const source = await readPurchaseCorrection(purchase.id, line.id, item.id, signal())
  expect(source.replacement).toEqual({ kind: 'individual', item, units: [unit] }); expect(fetchItem).toHaveBeenCalledTimes(2)
  vi.mocked(fetchItemUnitsPage).mockResolvedValueOnce({ data: [], next_cursor: null })
  await expect(readPurchaseCorrection(purchase.id, line.id, item.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  vi.mocked(fetchItemUnitsPage).mockResolvedValueOnce({ data: [{ ...unit, notes: 'Different snapshot' }], next_cursor: null })
  await expect(readPurchaseCorrection(purchase.id, line.id, item.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
})

it('keeps explicit unallocated and path-unaddressable legacy Unit identities distinct', async () => {
  vi.mocked(purchases.fetchPurchaseLinesPage).mockResolvedValueOnce({ data: [{ ...line, item_unit_id: null }], next_cursor: null })
  expect((await readPurchaseCorrection(purchase.id, line.id, null, signal())).originalUnit).toEqual({ kind: 'unallocated' })
  vi.mocked(purchases.fetchPurchaseLinesPage).mockResolvedValueOnce({ data: [{ ...line, id: 'line/legacy', item_unit_id: 'legacy/unit' }], next_cursor: null })
  expect((await readPurchaseCorrection(purchase.id, 'line/legacy', null, signal())).originalUnit).toEqual({ kind: 'unaddressable', id: 'legacy/unit' })
  expect(fetchUnit).not.toHaveBeenCalled()
})

it('withholds a missing selected Line after the parent interval instead of manufacturing source', async () => {
  await expect(readPurchaseCorrection(purchase.id, 'missing', null, signal())).rejects.toMatchObject({ code: 'NOT_FOUND' })
  expect(purchases.fetchPurchase).toHaveBeenCalledTimes(2); expect(fetchItem).not.toHaveBeenCalled()
  vi.mocked(purchases.fetchPurchase).mockResolvedValueOnce(purchase).mockResolvedValueOnce({ ...purchase, revision: 13 })
  await expect(readPurchaseCorrection(purchase.id, 'missing', null, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
})

it('reconstructs only actual replacement links regardless of correction-entry order', async () => {
  const prior = { ...line, id: 'prior', revision: 11 }, middle = { ...line, id: 'middle', revision: 1 }, active = { ...line, id: 'active', revision: 1 }
  const first = correction('z-entry', prior, middle), second = correction('a-entry', middle, active)
  vi.mocked(purchases.fetchPurchaseLinesPage).mockResolvedValueOnce({ data: [active], next_cursor: null })
  vi.mocked(finance.fetchPurchaseCorrectionsPage).mockResolvedValueOnce({ data: [second, first], next_cursor: null })
  expect(await readPurchaseCorrectionHistory(purchase.id, signal())).toEqual({ purchase, lines: [active], corrections: [second, first] })
})

it('completes 205 current Lines, immutable decisions and selected replacement Units without truncation', async () => {
  const lines = Array.from({ length: 205 }, (_, index) => ({ ...line, id: `active-${index}`, item_unit_id: null }))
  const decisions = Array.from({ length: 205 }, (_, index) => correction(`event-${index}`, { ...line, id: `voided-${index}` }, null))
  const units = Array.from({ length: 205 }, (_, index) => ({ ...targetUnit, id: `target-${index}` }))
  for (const [index, data] of [lines.slice(0, 100), lines.slice(100, 200), lines.slice(200)].entries()) vi.mocked(purchases.fetchPurchaseLinesPage).mockResolvedValueOnce({ data, next_cursor: index < 2 ? `lines-${index}` : null })
  for (const [index, data] of [decisions.slice(0, 100), decisions.slice(100, 200), decisions.slice(200)].entries()) vi.mocked(finance.fetchPurchaseCorrectionsPage).mockResolvedValueOnce({ data, next_cursor: index < 2 ? `events-${index}` : null })
  for (const [index, data] of [units.slice(0, 100), units.slice(100, 200), units.slice(200)].entries()) vi.mocked(fetchItemUnitsPage).mockResolvedValueOnce({ data, next_cursor: index < 2 ? `units-${index}` : null })
  const abort = signal(), source = await readPurchaseCorrection(purchase.id, lines[0]!.id, target.id, abort)
  expect(source.lines).toEqual(lines); expect(source.corrections).toEqual(decisions); expect(source.replacement).toEqual({ kind: 'individual', item: target, units })
  expect(finance.fetchPurchaseCorrectionsPage).toHaveBeenLastCalledWith(purchase.id, { limit: 100, cursor: 'events-1' }, abort)
  expect(fetchItemUnitsPage).toHaveBeenLastCalledWith(target.id, { limit: 100, cursor: 'units-1' }, abort)
})

it('rejects incomplete, duplicated, foreign and cyclic financial histories', async () => {
  const prior = { ...line, id: 'prior', revision: 1 }, middle = { ...line, id: 'middle', revision: 1 }, active = { ...line, id: 'active', revision: 1 }
  const first = correction('first', prior, middle), second = correction('second', middle, active)
  const cases: { lines: purchases.PurchaseLine[]; entries: finance.PurchaseLineCorrection[] }[] = [
    { lines: [{ ...active, purchase_id: 'other' }], entries: [] }, { lines: [prior], entries: [first] }, { lines: [active], entries: [first, { ...first, id: 'another' }] },
    { lines: [active], entries: [first] }, { lines: [active], entries: [first, { ...second, before: { ...middle, paid_minor: 9 } }] },
    { lines: [active], entries: [first, { ...second, purchase_id: 'other' }] },
    { lines: [active], entries: [first, correction('other', { ...line, id: 'different' }, middle), second] },
    { lines: [], entries: [correction('one', prior, middle), correction('two', middle, prior)] },
  ]
  for (const value of cases) {
    vi.mocked(purchases.fetchPurchaseLinesPage).mockResolvedValueOnce({ data: value.lines, next_cursor: null })
    vi.mocked(finance.fetchPurchaseCorrectionsPage).mockResolvedValueOnce({ data: value.entries, next_cursor: null })
    await expect(readPurchaseCorrectionHistory(purchase.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  }
})

it.each([{ revision: 13 }, { currency: 'USD' }, { receipt_note: 'Unversioned change' }, { id: 'other' }])('withholds a mixed Purchase parent %j', async (changed) => {
  vi.mocked(purchases.fetchPurchase).mockResolvedValueOnce(purchase).mockResolvedValueOnce({ ...purchase, ...changed })
  await expect(readPurchaseCorrection(purchase.id, line.id, null, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
})
it.each([{ revision: 8 }, { name: 'Unversioned change' }, { id: 'other' }, { tracking_mode: 'quantity' as const }])('withholds a changed original Item %j', async (changed) => {
  vi.mocked(fetchItem).mockResolvedValueOnce(item).mockResolvedValueOnce({ ...item, ...changed })
  await expect(readPurchaseCorrection(purchase.id, line.id, null, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
})
it('rejects wrong initial owners and changed replacement or original Unit', async () => {
  vi.mocked(fetchItem).mockResolvedValueOnce({ ...item, id: 'other' })
  await expect(readPurchaseCorrection(purchase.id, line.id, null, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  vi.mocked(fetchUnit).mockResolvedValueOnce({ ...unit, item_id: 'other' })
  await expect(readPurchaseCorrection(purchase.id, line.id, null, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  vi.mocked(fetchUnit).mockResolvedValueOnce(unit).mockResolvedValueOnce({ ...unit, notes: 'Changed' })
  await expect(readPurchaseCorrection(purchase.id, line.id, null, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  vi.mocked(fetchItem).mockResolvedValueOnce(item).mockResolvedValueOnce({ ...target, id: 'other' })
  await expect(readPurchaseCorrection(purchase.id, line.id, target.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  vi.mocked(fetchItem).mockResolvedValueOnce(item).mockResolvedValueOnce(target).mockResolvedValueOnce({ ...target, revision: 10 })
  await expect(readPurchaseCorrection(purchase.id, line.id, target.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  vi.mocked(fetchItemUnitsPage).mockResolvedValueOnce({ data: [{ ...targetUnit, item_id: 'other' }], next_cursor: null })
  await expect(readPurchaseCorrection(purchase.id, line.id, target.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
})

it('propagates failed pages, duplicate records, cursor loops and cancellation without returning prefixes', async () => {
  vi.mocked(finance.fetchPurchaseCorrectionsPage).mockRejectedValueOnce(new ApiError('NETWORK_ERROR'))
  await expect(readPurchaseCorrectionHistory(purchase.id, signal())).rejects.toMatchObject({ code: 'NETWORK_ERROR' })
  vi.mocked(purchases.fetchPurchaseLinesPage).mockResolvedValueOnce({ data: [line], next_cursor: 'next' }).mockResolvedValueOnce({ data: [line], next_cursor: null })
  await expect(readPurchaseCorrectionHistory(purchase.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  vi.mocked(finance.fetchPurchaseCorrectionsPage).mockResolvedValueOnce({ data: [], next_cursor: 'loop' }).mockResolvedValueOnce({ data: [], next_cursor: 'loop' })
  await expect(readPurchaseCorrectionHistory(purchase.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  const controller = new AbortController()
  vi.mocked(purchases.fetchPurchase).mockResolvedValueOnce(purchase).mockImplementationOnce(async () => { controller.abort(); return purchase })
  await expect(readPurchaseCorrectionHistory(purchase.id, controller.signal)).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
})
