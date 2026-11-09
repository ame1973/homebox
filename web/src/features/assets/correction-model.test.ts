import { beforeEach, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/client'
import * as assets from '@/lib/api/assets'
import { fetchItem, type Item } from '@/lib/api/catalog'
import * as finance from '@/lib/api/financial-corrections'
import { fetchUnit, type Unit } from '@/lib/api/holdings'
import { readDisposalCorrection } from './correction-model'

vi.mock('@/lib/api/catalog', async (original) => ({ ...await original<typeof import('@/lib/api/catalog')>(), fetchItem: vi.fn() }))
vi.mock('@/lib/api/holdings', async (original) => ({ ...await original<typeof import('@/lib/api/holdings')>(), fetchUnit: vi.fn() }))
vi.mock('@/lib/api/assets', async (original) => ({ ...await original<typeof import('@/lib/api/assets')>(), fetchAsset: vi.fn(), fetchAssetDisposal: vi.fn(), fetchAssetCostSummary: vi.fn() }))
vi.mock('@/lib/api/financial-corrections', async (original) => ({ ...await original<typeof import('@/lib/api/financial-corrections')>(), fetchDisposalRecord: vi.fn(), fetchDisposalCorrectionsPage: vi.fn() }))
const item: Item = { id: 'item', revision: 11, name: 'Original owner', brand: null, model: '', category_id: null, description: '', notes: '', tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1, status: 'active' }
const unit: Unit = { id: 'unit', revision: 13, item_id: item.id, asset_code: 'Retained physical source', serial_number: null, notes: '', status: 'disposed', location_id: 'last-place' }
const asset: assets.Asset = { item_unit_id: unit.id, revision: 7, cost_basis: 'manual', currency: 'JPY', manual_acquisition_minor: 100, in_service_at: null, retired_at: null, estimated_residual_minor: 30, usage_unit: 'times' }
const original: finance.DisposalData = { item_unit_id: unit.id, kind: 'donated', disposed_at: '2026-11-07T12:00:00.123456789+08:00', realized_proceeds_minor: 5, currency: 'ZZZ', note: 'Retained historical decision' }
const initial: finance.DisposalRecord = { item_unit_id: unit.id, revision: 7, original, current: original }
const zero: finance.DisposalRecord = { ...initial, revision: 8, current: { ...original, kind: 'sold', currency: 'JPY', realized_proceeds_minor: 0 } }
const voided = { ...initial, revision: 9, current: null } as const satisfies finance.DisposalRecord
const restored: finance.DisposalRecord = { ...initial, revision: 10, current: { ...original, kind: 'sold', currency: 'JPY', realized_proceeds_minor: 300 } }
const instant = '2026-11-07T04:00:00.123456789Z', signal = () => new AbortController().signal
function correction(id: string, before: finance.DisposalRecord, after: finance.DisposalRecord): finance.DisposalCorrection {
  const base = { id, revision: 1 as const, item_unit_id: unit.id, reason: 'Exact reason', created_at: instant, before }
  return after.current === null ? { ...base, action: 'void', after: { ...after, current: null } } : { ...base, action: 'replace', after: { ...after, current: after.current } }
}
const first = correction('z-event', initial, zero), second = correction('a-event', zero, voided), third = correction('m-event', voided, restored)
beforeEach(() => {
  vi.mocked(fetchUnit).mockReset().mockImplementation(async () => structuredClone(unit))
  vi.mocked(fetchItem).mockReset().mockImplementation(async () => structuredClone(item))
  vi.mocked(assets.fetchAsset).mockReset().mockImplementation(async () => structuredClone(asset))
  vi.mocked(assets.fetchAssetDisposal).mockReset(); vi.mocked(assets.fetchAssetCostSummary).mockReset()
  vi.mocked(finance.fetchDisposalRecord).mockReset().mockImplementation(async () => structuredClone(initial))
  vi.mocked(finance.fetchDisposalCorrectionsPage).mockReset().mockResolvedValue({ data: [], next_cursor: null })
})

it('brackets only the actual Unit, Item, Asset, retained record and selected history', async () => {
  const abort = signal()
  expect(await readDisposalCorrection(unit.id, abort)).toEqual({ unit, item, asset, disposal: initial, corrections: [] })
  for (const getter of [fetchUnit, fetchItem, assets.fetchAsset, finance.fetchDisposalRecord]) expect(getter).toHaveBeenCalledTimes(2)
  expect(finance.fetchDisposalCorrectionsPage).toHaveBeenCalledExactlyOnceWith(unit.id, { limit: 100 }, abort)
  expect(assets.fetchAssetDisposal).not.toHaveBeenCalled(); expect(assets.fetchAssetCostSummary).not.toHaveBeenCalled()
  expect(vi.mocked(fetchUnit).mock.invocationCallOrder[0]).toBeLessThan(vi.mocked(finance.fetchDisposalRecord).mock.invocationCallOrder[0]!)
  expect(vi.mocked(finance.fetchDisposalRecord).mock.invocationCallOrder[1]).toBeLessThan(vi.mocked(fetchUnit).mock.invocationCallOrder[1]!)
})

it('validates actual zero, void and reinstatement history without assuming the original revision or order', async () => {
  vi.mocked(finance.fetchDisposalRecord).mockResolvedValue(restored)
  vi.mocked(finance.fetchDisposalCorrectionsPage).mockResolvedValueOnce({ data: [second, third, first], next_cursor: null })
  const source = await readDisposalCorrection(unit.id, signal())
  expect(source.disposal).toEqual(restored); expect(source.corrections).toEqual([second, third, first]); expect(source.disposal.original).toEqual(original)
})

it('retains a real void and original legacy financial interpretation without reading the absent current endpoint', async () => {
  vi.mocked(finance.fetchDisposalRecord).mockResolvedValue(voided)
  vi.mocked(finance.fetchDisposalCorrectionsPage).mockResolvedValueOnce({ data: [second, first], next_cursor: null })
  const source = await readDisposalCorrection(unit.id, signal())
  expect(source.disposal.current).toBeNull(); expect(source.disposal.original).toEqual(original)
  expect(source.asset.currency).toBe('JPY'); expect(source.disposal.original.currency).toBe('ZZZ'); expect(assets.fetchAssetDisposal).not.toHaveBeenCalled()
})

it('completes all 205 actual decisions and validates their linked subject history', async () => {
  let before = initial
  const entries = Array.from({ length: 205 }, (_, index) => {
    const after = { ...before, revision: before.revision + 1, current: { ...original, note: `Decision ${index}` } }
    const value = correction(`event-${index}`, before, after); before = after; return value
  }).reverse()
  vi.mocked(finance.fetchDisposalRecord).mockResolvedValue(before)
  for (const [index, data] of [entries.slice(0, 100), entries.slice(100, 200), entries.slice(200)].entries()) vi.mocked(finance.fetchDisposalCorrectionsPage).mockResolvedValueOnce({ data, next_cursor: index < 2 ? `next-${index}` : null })
  const abort = signal(), source = await readDisposalCorrection(unit.id, abort)
  expect(source.corrections).toEqual(entries); expect(source.disposal).toEqual(before)
  expect(finance.fetchDisposalCorrectionsPage).toHaveBeenLastCalledWith(unit.id, { limit: 100, cursor: 'next-1' }, abort)
})

it('withholds incomplete, foreign, forked or inconsistent retained history', async () => {
  const wrongOriginal = { ...original, note: 'Altered original' }, wrongRecord = { ...initial, original: wrongOriginal, current: wrongOriginal }
  const cases: { record: finance.DisposalRecord; entries: finance.DisposalCorrection[] }[] = [
    { record: voided, entries: [] }, { record: { ...initial, current: { ...original, note: 'Changed' } }, entries: [] },
    { record: restored, entries: [second, third] }, { record: restored, entries: [first, third] },
    { record: restored, entries: [first, second] }, { record: restored, entries: [{ ...first, item_unit_id: 'other' }, second, third] },
    { record: restored, entries: [correction('wrong', wrongRecord, { ...zero, original: wrongOriginal }), second, third] },
    { record: restored, entries: [first, correction('fork', initial, zero), second, third] },
  ]
  for (const value of cases) {
    vi.mocked(finance.fetchDisposalRecord).mockResolvedValue(value.record)
    vi.mocked(finance.fetchDisposalCorrectionsPage).mockResolvedValueOnce({ data: value.entries, next_cursor: null })
    await expect(readDisposalCorrection(unit.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  }
})

it.each([{ revision: 14 }, { notes: 'Unversioned change' }, { item_id: 'other' }, { id: 'other' }])('rejects changed Unit source %j', async (change) => {
  vi.mocked(fetchUnit).mockResolvedValueOnce(unit).mockResolvedValueOnce({ ...unit, ...change })
  await expect(readDisposalCorrection(unit.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
})
it.each([{ revision: 12 }, { name: 'Unversioned change' }, { tracking_mode: 'quantity' as const }, { id: 'other' }])('rejects changed Item source %j', async (change) => {
  vi.mocked(fetchItem).mockResolvedValueOnce(item).mockResolvedValueOnce({ ...item, ...change })
  await expect(readDisposalCorrection(unit.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
})
it.each([{ revision: 8 }, { currency: 'USD' }, { manual_acquisition_minor: 101 }, { item_unit_id: 'other' }])('rejects changed Asset source %j', async (change) => {
  vi.mocked(assets.fetchAsset).mockResolvedValueOnce(asset).mockResolvedValueOnce({ ...asset, ...change })
  await expect(readDisposalCorrection(unit.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
})
it.each([{ revision: 8 }, { current: null }, { original: { ...original, note: 'Changed' } }, { current: { ...original, note: 'Changed' } }, { item_unit_id: 'other' }])('rejects changed retained Disposal source %j', async (change) => {
  vi.mocked(finance.fetchDisposalRecord).mockResolvedValueOnce(initial).mockResolvedValueOnce({ ...initial, ...change })
  await expect(readDisposalCorrection(unit.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
})

it('rejects wrong initial owners or tracking without manufacturing an empty history', async () => {
  vi.mocked(fetchUnit).mockResolvedValueOnce({ ...unit, id: 'other' })
  await expect(readDisposalCorrection(unit.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  vi.mocked(fetchItem).mockResolvedValueOnce({ ...item, id: 'other' })
  await expect(readDisposalCorrection(unit.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  vi.mocked(fetchItem).mockResolvedValueOnce({ ...item, tracking_mode: 'quantity' })
  await expect(readDisposalCorrection(unit.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  vi.mocked(assets.fetchAsset).mockResolvedValueOnce({ ...asset, item_unit_id: 'other' })
  await expect(readDisposalCorrection(unit.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  vi.mocked(finance.fetchDisposalRecord).mockResolvedValueOnce({ ...initial, item_unit_id: 'other' })
  await expect(readDisposalCorrection(unit.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  expect(finance.fetchDisposalCorrectionsPage).not.toHaveBeenCalled()
})

it('propagates real absence, failed pages, duplicate entries and final cancellation', async () => {
  const missing = new ApiError('NOT_FOUND', { status: 404 })
  vi.mocked(finance.fetchDisposalRecord).mockRejectedValueOnce(missing)
  await expect(readDisposalCorrection(unit.id, signal())).rejects.toBe(missing)
  expect(finance.fetchDisposalCorrectionsPage).not.toHaveBeenCalled()
  vi.mocked(finance.fetchDisposalCorrectionsPage).mockRejectedValueOnce(new ApiError('SERVICE_UNAVAILABLE'))
  await expect(readDisposalCorrection(unit.id, signal())).rejects.toMatchObject({ code: 'SERVICE_UNAVAILABLE' })
  vi.mocked(finance.fetchDisposalCorrectionsPage).mockResolvedValueOnce({ data: [first], next_cursor: 'next' }).mockResolvedValueOnce({ data: [first], next_cursor: null })
  await expect(readDisposalCorrection(unit.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  vi.mocked(finance.fetchDisposalCorrectionsPage).mockResolvedValueOnce({ data: [], next_cursor: 'loop' }).mockResolvedValueOnce({ data: [], next_cursor: 'loop' })
  await expect(readDisposalCorrection(unit.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  const controller = new AbortController()
  vi.mocked(fetchUnit).mockResolvedValueOnce(unit).mockImplementationOnce(async () => { controller.abort(); return unit })
  await expect(readDisposalCorrection(unit.id, controller.signal)).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
})
