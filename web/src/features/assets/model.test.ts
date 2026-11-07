import { beforeEach, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/client'
import { fetchItem, type Item } from '@/lib/api/catalog'
import { fetchUnit, type Unit } from '@/lib/api/holdings'
import * as api from '@/lib/api/assets'
import { readAssetCosts, readAssetDetail, readAssetLifecycle, readAssetProfile, readAssetUsage } from './model'

vi.mock('@/lib/api/catalog', () => ({ fetchItem: vi.fn() }))
vi.mock('@/lib/api/holdings', () => ({ fetchUnit: vi.fn() }))
vi.mock('@/lib/api/assets', () => ({ fetchAsset: vi.fn(), fetchAssetDisposal: vi.fn(), fetchAssetCostSummary: vi.fn(), fetchAssetUsagePage: vi.fn(), fetchAssetCostsPage: vi.fn() }))
const item: Item = { id: 'item', revision: 11, name: 'Original owner', brand: null, model: '', category_id: null, description: '', notes: '',
  tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1, status: 'active' }
const unit: Unit = { id: 'unit', item_id: item.id, revision: 13, asset_code: '  AST é  ', location_id: 'last-place', serial_number: null, notes: 'Original', status: 'stored' }
const asset: api.Asset = { item_unit_id: unit.id, revision: 7, currency: 'JPY', cost_basis: 'manual', manual_acquisition_minor: 100,
  in_service_at: null, retired_at: null, estimated_residual_minor: 20, usage_unit: 'times' }
const usage: api.Usage = { id: 'use', item_unit_id: unit.id, revision: 1, delta: -1, usage_unit: 'times', event_type: 'correction', occurred_at: '2026-11-07T12:00:00.123456789+08:00', note: '\0Exact é\n', correction_of: null }
const cost: api.Cost = { id: 'cost', item_unit_id: unit.id, revision: 1, amount_minor: 5, currency: 'JPY', kind: 'adjustment', direction: 'expense', occurred_at: usage.occurred_at, note: usage.note, correction_of: null }
const disposal: api.Disposal = { item_unit_id: unit.id, revision: 3, currency: 'JPY', kind: 'donated', realized_proceeds_minor: 5, disposed_at: usage.occurred_at, note: 'Retained decision' }
const summary: api.AssetCostSummary = { unit_id: unit.id, currency: 'JPY', cost_basis: 'manual', acquisition_minor: 100, extra_minor: 5,
  realized_proceeds_minor: 0, estimated_residual_minor: 20, net_cost_minor: 105, projected_net_cost_minor: 85, elapsed_days: null, total_usage: 0, usage_unit: 'times',
  per_day_minor: null, per_use_minor: null, projected_per_day_minor: null, projected_per_use_minor: null, as_of: '2026-11-07T04:00:00.123456789Z', timezone: 'UTC', day_policy: 'inclusive_civil_days', source_revision: 0 }
const signal = () => new AbortController().signal
const missing = () => new ApiError('NOT_FOUND', { status: 404 })
const readers = [
  { kind: 'profile', read: (abort: AbortSignal) => readAssetProfile(unit.id, abort) },
  { kind: 'usage', read: (abort: AbortSignal) => readAssetUsage(unit.id, abort) },
  { kind: 'costs', read: (abort: AbortSignal) => readAssetCosts(unit.id, abort) },
  { kind: 'lifecycle', read: (abort: AbortSignal) => readAssetLifecycle(unit.id, abort) },
  { kind: 'detail', read: (abort: AbortSignal) => readAssetDetail(unit.id, { as_of: summary.as_of }, abort) },
]
beforeEach(() => {
  vi.mocked(fetchUnit).mockReset().mockImplementation(async () => ({ ...unit }))
  vi.mocked(fetchItem).mockReset().mockImplementation(async () => ({ ...item }))
  vi.mocked(api.fetchAsset).mockReset().mockImplementation(async () => ({ ...asset }))
  vi.mocked(api.fetchAssetUsagePage).mockReset().mockResolvedValue({ data: [usage], next_cursor: null })
  vi.mocked(api.fetchAssetCostsPage).mockReset().mockResolvedValue({ data: [cost], next_cursor: null })
  vi.mocked(api.fetchAssetDisposal).mockReset().mockRejectedValue(missing())
  vi.mocked(api.fetchAssetCostSummary).mockReset().mockResolvedValue(summary)
})

it.each(readers)('brackets the actual $kind scope with complete independent Unit/Item/Asset sources', async ({ kind, read }) => {
  const abort = signal(), result = await read(abort)
  const base = { item, unit, asset }
  expect(result).toEqual(kind === 'usage' ? { ...base, kind, usage: [usage] } : kind === 'costs' ? { ...base, kind, costs: [cost] } :
    kind === 'lifecycle' ? { ...base, disposal: null } : kind === 'detail' ? { ...base, disposal: null, summary } : base)
  for (const getter of [fetchUnit, fetchItem, api.fetchAsset]) expect(getter).toHaveBeenCalledTimes(2)
  expect(fetchUnit).toHaveBeenNthCalledWith(1, unit.id, abort); expect(fetchUnit).toHaveBeenNthCalledWith(2, unit.id, abort)
  expect(fetchItem).toHaveBeenNthCalledWith(1, item.id, abort)
  expect(api.fetchAsset).toHaveBeenNthCalledWith(2, unit.id, abort)
  if (kind === 'usage') expect(api.fetchAssetUsagePage).toHaveBeenCalledExactlyOnceWith(unit.id, { limit: 100 }, abort)
  else expect(api.fetchAssetUsagePage).not.toHaveBeenCalled()
  if (kind === 'costs') expect(api.fetchAssetCostsPage).toHaveBeenCalledExactlyOnceWith(unit.id, { limit: 100 }, abort)
  else expect(api.fetchAssetCostsPage).not.toHaveBeenCalled()
  expect(api.fetchAssetDisposal).toHaveBeenCalledTimes(kind === 'lifecycle' || kind === 'detail' ? 1 : 0)
  expect(api.fetchAssetCostSummary).toHaveBeenCalledTimes(kind === 'detail' ? 1 : 0)
})

it('retains reviewed profile absence only between matching real owner reads, without an invented summary/history', async () => {
  vi.mocked(api.fetchAsset).mockRejectedValue(missing())
  expect(await readAssetProfile(unit.id, signal())).toEqual({ item, unit, asset: null })
  expect(api.fetchAssetUsagePage).not.toHaveBeenCalled(); expect(api.fetchAssetCostsPage).not.toHaveBeenCalled()
  expect(api.fetchAssetDisposal).not.toHaveBeenCalled(); expect(api.fetchAssetCostSummary).not.toHaveBeenCalled()
})
it.each([new ApiError('NOT_FOUND'), new ApiError('NOT_FOUND', { status: 503 }), new ApiError('NETWORK_ERROR'), new ApiError('SERVICE_UNAVAILABLE', { status: 503 }), new ApiError('AUTHENTICATION_REQUIRED', { status: 401 }), new Error('private failure')])('does not convert failed/ambiguous source into optional absence %#', async (error) => {
  vi.mocked(api.fetchAsset).mockRejectedValueOnce(error)
  await expect(readAssetProfile(unit.id, signal())).rejects.toBe(error)
  vi.mocked(api.fetchAssetDisposal).mockRejectedValueOnce(error)
  await expect(readAssetLifecycle(unit.id, signal())).rejects.toBe(error)
})
it('rejects profile creation/removal inside the reviewed absence interval', async () => {
  vi.mocked(api.fetchAsset).mockRejectedValueOnce(missing()).mockResolvedValueOnce(asset)
  await expect(readAssetProfile(unit.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  vi.mocked(api.fetchAsset).mockResolvedValueOnce(asset).mockRejectedValueOnce(missing())
  await expect(readAssetProfile(unit.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
})

for (const { kind, read } of readers) {
  it.each([{ revision: 14 }, { notes: 'Unversioned change' }, { location_id: null }, { item_id: 'other' }, { id: 'other' }])(`withholds ${kind} after a changed or inconsistent physical source %j`, async (changed) => {
    vi.mocked(fetchUnit).mockResolvedValueOnce(unit).mockResolvedValueOnce({ ...unit, ...changed })
    await expect(read(signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  })
  it.each([{ revision: 12 }, { name: 'Unversioned change' }, { model: null }, { tracking_mode: 'quantity' as const }, { id: 'other' }])(`withholds ${kind} after changed owner metadata/interpretation %j`, async (changed) => {
    vi.mocked(fetchItem).mockResolvedValueOnce(item).mockResolvedValueOnce({ ...item, ...changed })
    await expect(read(signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  })
  it.each([{ revision: 8 }, { currency: 'USD' }, { manual_acquisition_minor: 101 }, { in_service_at: '2026-11-07T00:00:00Z' }, { item_unit_id: 'other' }])(`withholds ${kind} after changed financial authority %j`, async (changed) => {
    vi.mocked(api.fetchAsset).mockResolvedValueOnce(asset).mockResolvedValueOnce({ ...asset, ...changed })
    await expect(read(signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  })
}

it.each(['usage', 'costs'] as const)('completes all 205 actual %s rows before exposing any prefix', async (kind) => {
  const abort = signal()
  if (kind === 'usage') {
    const rows = Array.from({ length: 205 }, (_, index) => ({ ...usage, id: `use-${index}` }))
    vi.mocked(api.fetchAssetUsagePage).mockResolvedValueOnce({ data: rows.slice(0, 100), next_cursor: 'second' })
      .mockResolvedValueOnce({ data: rows.slice(100, 200), next_cursor: 'last' }).mockResolvedValueOnce({ data: rows.slice(200), next_cursor: null })
    expect((await readAssetUsage(unit.id, abort)).usage).toEqual(rows)
    expect(api.fetchAssetUsagePage).toHaveBeenLastCalledWith(unit.id, { limit: 100, cursor: 'last' }, abort)
  } else {
    const rows = Array.from({ length: 205 }, (_, index) => ({ ...cost, id: `cost-${index}` }))
    vi.mocked(api.fetchAssetCostsPage).mockResolvedValueOnce({ data: rows.slice(0, 100), next_cursor: 'second' })
      .mockResolvedValueOnce({ data: rows.slice(100, 200), next_cursor: 'last' }).mockResolvedValueOnce({ data: rows.slice(200), next_cursor: null })
    expect((await readAssetCosts(unit.id, abort)).costs).toEqual(rows)
    expect(api.fetchAssetCostsPage).toHaveBeenLastCalledWith(unit.id, { limit: 100, cursor: 'last' }, abort)
  }
})
it.each(['usage', 'costs'] as const)('rejects duplicates, cursor cycles, foreign owners and mid-page failure in %s', async (kind) => {
  if (kind === 'usage') {
    const read = () => readAssetUsage(unit.id, signal())
    vi.mocked(api.fetchAssetUsagePage).mockResolvedValueOnce({ data: [usage], next_cursor: 'next' }).mockResolvedValueOnce({ data: [usage], next_cursor: null })
    await expect(read()).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    vi.mocked(api.fetchAssetUsagePage).mockResolvedValueOnce({ data: [], next_cursor: 'loop' }).mockResolvedValueOnce({ data: [], next_cursor: 'loop' })
    await expect(read()).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    vi.mocked(api.fetchAssetUsagePage).mockResolvedValueOnce({ data: [{ ...usage, item_unit_id: 'wrong' }], next_cursor: null })
    await expect(read()).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    vi.mocked(api.fetchAssetUsagePage).mockResolvedValueOnce({ data: [usage], next_cursor: 'next' }).mockRejectedValueOnce(new ApiError('INVALID_CURSOR'))
    await expect(read()).rejects.toMatchObject({ code: 'INVALID_CURSOR' })
  } else {
    const read = () => readAssetCosts(unit.id, signal())
    vi.mocked(api.fetchAssetCostsPage).mockResolvedValueOnce({ data: [cost], next_cursor: 'next' }).mockResolvedValueOnce({ data: [cost], next_cursor: null })
    await expect(read()).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    vi.mocked(api.fetchAssetCostsPage).mockResolvedValueOnce({ data: [], next_cursor: 'loop' }).mockResolvedValueOnce({ data: [], next_cursor: 'loop' })
    await expect(read()).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    vi.mocked(api.fetchAssetCostsPage).mockResolvedValueOnce({ data: [{ ...cost, item_unit_id: 'wrong' }], next_cursor: null })
    await expect(read()).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    vi.mocked(api.fetchAssetCostsPage).mockResolvedValueOnce({ data: [cost], next_cursor: 'next' }).mockRejectedValueOnce(new ApiError('SERVICE_UNAVAILABLE'))
    await expect(read()).rejects.toMatchObject({ code: 'SERVICE_UNAVAILABLE' })
  }
})

it('retains actual legacy lifecycle decisions and requires aligned detail calculation/source interpretation', async () => {
  const disposed = { ...unit, status: 'disposed' } as const
  vi.mocked(fetchUnit).mockResolvedValue(disposed)
  vi.mocked(api.fetchAssetDisposal).mockResolvedValue(disposal)
  const calculated = { ...summary, realized_proceeds_minor: 5, net_cost_minor: 100 }
  vi.mocked(api.fetchAssetCostSummary).mockResolvedValue(calculated)
  expect(await readAssetLifecycle(unit.id, signal())).toEqual({ item, unit: disposed, asset, disposal })
  expect(await readAssetDetail(unit.id, {}, signal())).toEqual({ item, unit: disposed, asset, disposal, summary: calculated })
  const legacy = { ...asset, currency: 'ZZZ', manual_acquisition_minor: null }
  vi.mocked(api.fetchAsset).mockResolvedValue(legacy)
  expect((await readAssetLifecycle(unit.id, signal())).asset).toEqual(legacy)
})
it.each([{ unit_id: 'other' }, { currency: 'USD' }, { cost_basis: 'purchase' as const }, { usage_unit: 'minutes' as const }, { estimated_residual_minor: null }, { realized_proceeds_minor: 1 }, { acquisition_minor: 101 }])('rejects mixed detail financial interpretation %j', async (changed) => {
  vi.mocked(api.fetchAssetCostSummary).mockResolvedValue({ ...summary, ...changed })
  await expect(readAssetDetail(unit.id, {}, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
})
it.each(['lifecycle', 'detail'] as const)('rejects a foreign Disposal in %s without synthetic zero proceeds', async (kind) => {
  vi.mocked(api.fetchAssetDisposal).mockResolvedValue({ ...disposal, item_unit_id: 'other' })
  await expect(kind === 'lifecycle' ? readAssetLifecycle(unit.id, signal()) : readAssetDetail(unit.id, {}, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
})
it('accepts complete actual empty owned histories, purchase basis and reordered flat source fields', async () => {
  vi.mocked(api.fetchAssetUsagePage).mockResolvedValue({ data: [], next_cursor: null })
  expect(await readAssetUsage(unit.id, signal())).toEqual({ item, unit, asset, kind: 'usage', usage: [] })
  vi.mocked(api.fetchAssetCostsPage).mockResolvedValue({ data: [], next_cursor: null })
  expect(await readAssetCosts(unit.id, signal())).toEqual({ item, unit, asset, kind: 'costs', costs: [] })
  const purchased = { ...asset, cost_basis: 'purchase', manual_acquisition_minor: null } as const
  vi.mocked(api.fetchAsset).mockResolvedValueOnce(purchased).mockResolvedValueOnce(Object.fromEntries(Object.entries(purchased).reverse()) as api.Asset)
  vi.mocked(api.fetchAssetCostSummary).mockResolvedValue({ ...summary, cost_basis: 'purchase', acquisition_minor: 500, net_cost_minor: 505, projected_net_cost_minor: 485 })
  expect((await readAssetDetail(unit.id, {}, signal())).asset).toEqual(purchased)
})
it('preserves a retained foreign-currency decision for lifecycle review but never mixes it into a calculated detail', async () => {
  const retained = { ...disposal, currency: 'ZZZ' }
  vi.mocked(api.fetchAssetDisposal).mockResolvedValue(retained)
  expect((await readAssetLifecycle(unit.id, signal())).disposal).toEqual(retained)
  vi.mocked(api.fetchAssetCostSummary).mockResolvedValue({ ...summary, realized_proceeds_minor: 5, net_cost_minor: 100 })
  await expect(readAssetDetail(unit.id, {}, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  vi.mocked(api.fetchAsset).mockResolvedValue({ ...asset, currency: 'ZZZ' })
  vi.mocked(api.fetchAssetCostSummary).mockResolvedValue({ ...summary, currency: 'ZZZ', realized_proceeds_minor: 5, net_cost_minor: 100 })
  expect((await readAssetDetail(unit.id, {}, signal())).summary.currency).toBe('ZZZ')
})
it('snapshots the requested as_of before asynchronous source loading without replacing its proof', async () => {
  const query = { as_of: summary.as_of }
  vi.mocked(fetchUnit).mockImplementationOnce(async () => { query.as_of = '2027-01-01T00:00:00Z'; return unit })
  const abort = signal()
  expect((await readAssetDetail(unit.id, query, abort)).summary.source_revision).toBe(0)
  expect(api.fetchAssetCostSummary).toHaveBeenCalledExactlyOnceWith(unit.id, { as_of: summary.as_of }, abort)
})
it.each(readers)('propagates actual required owner/missing failures and final cancellation in $kind', async ({ read }) => {
  vi.mocked(fetchUnit).mockRejectedValueOnce(missing())
  await expect(read(signal())).rejects.toMatchObject({ code: 'NOT_FOUND', status: 404 })
  const controller = new AbortController()
  vi.mocked(api.fetchAsset).mockResolvedValueOnce(asset).mockImplementationOnce(async () => { controller.abort(); return asset })
  await expect(read(controller.signal)).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
})
