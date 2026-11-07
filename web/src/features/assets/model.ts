import { ApiError } from '@/lib/api/client'
import { fetchItem } from '@/lib/api/catalog'
import { fetchUnit } from '@/lib/api/holdings'
import { fetchAsset, fetchAssetCostSummary, fetchAssetCostsPage, fetchAssetDisposal, fetchAssetUsagePage,
  type Asset, type AssetCostSummary, type AssetSummaryQuery, type Cost, type Disposal, type Usage } from '@/lib/api/assets'
import { readAllMetadata } from '@/lib/metadata'
import type { UnitHolding } from '@/features/holdings/model'

export type AssetProfileSource = UnitHolding & Readonly<{ asset: Asset | null }>
export type AssetSource = UnitHolding & Readonly<{ asset: Asset }>
export type AssetUsageSource = AssetSource & Readonly<{ kind: 'usage'; usage: readonly Usage[] }>
export type AssetCostsSource = AssetSource & Readonly<{ kind: 'costs'; costs: readonly Cost[] }>
export type AssetLifecycleSource = AssetSource & Readonly<{ disposal: Disposal | null }>
export type AssetDetail = AssetLifecycleSource & Readonly<{ summary: AssetCostSummary }>

function unchanged<T extends object>(before: T, after: T): boolean {
  return (Object.keys(before) as (keyof T)[]).every((key) => before[key] === after[key])
}
async function optional<T>(read: () => Promise<T>): Promise<T | null> {
  try { return await read() } catch (error) {
    if (error instanceof ApiError && error.status === 404 && error.code === 'NOT_FOUND') return null
    throw error
  }
}
const optionalAsset = (unitID: string, signal: AbortSignal) => optional(() => fetchAsset(unitID, signal))

// The complete selected financial scope sits between matching actual owner,
// physical and financial reads. None of these reads installs a write proof.
async function context<A extends Asset | null>(unitID: string, signal: AbortSignal, read: (id: string, signal: AbortSignal) => Promise<A>) {
  const unit = await fetchUnit(unitID, signal)
  const item = await fetchItem(unit.item_id, signal)
  const asset = await read(unitID, signal)
  if (signal.aborted) throw new ApiError('REQUEST_ABORTED')
  if (unit.id !== unitID || item.id !== unit.item_id || item.tracking_mode !== 'individual' || asset !== null && asset.item_unit_id !== unitID) throw new ApiError('INVALID_RESPONSE')
  return { unit, item, asset }
}
async function finish<A extends Asset | null>(before: UnitHolding & Readonly<{ asset: A }>, unitID: string, signal: AbortSignal,
  read: (id: string, signal: AbortSignal) => Promise<A>) {
  const after = await context(unitID, signal, read)
  if (!unchanged(before.unit, after.unit) || !unchanged(before.item, after.item) ||
    (before.asset === null || after.asset === null ? before.asset !== after.asset : !unchanged(before.asset, after.asset))) throw new ApiError('INVALID_RESPONSE')
  return after
}

export async function readAssetProfile(unitID: string, signal: AbortSignal): Promise<AssetProfileSource> {
  const before = await context(unitID, signal, optionalAsset)
  return finish(before, unitID, signal, optionalAsset)
}
export async function readAssetUsage(unitID: string, signal: AbortSignal): Promise<AssetUsageSource> {
  const before = await context(unitID, signal, fetchAsset)
  const usage = await readAllMetadata((query, signal) => fetchAssetUsagePage(unitID, query, signal), signal)
  const source = await finish(before, unitID, signal, fetchAsset)
  if (usage.some((event) => event.item_unit_id !== unitID)) throw new ApiError('INVALID_RESPONSE')
  return { ...source, kind: 'usage', usage }
}
export async function readAssetCosts(unitID: string, signal: AbortSignal): Promise<AssetCostsSource> {
  const before = await context(unitID, signal, fetchAsset)
  const costs = await readAllMetadata((query, signal) => fetchAssetCostsPage(unitID, query, signal), signal)
  const source = await finish(before, unitID, signal, fetchAsset)
  if (costs.some((event) => event.item_unit_id !== unitID)) throw new ApiError('INVALID_RESPONSE')
  return { ...source, kind: 'costs', costs }
}
export async function readAssetLifecycle(unitID: string, signal: AbortSignal): Promise<AssetLifecycleSource> {
  const before = await context(unitID, signal, fetchAsset)
  const disposal = await optional(() => fetchAssetDisposal(unitID, signal))
  const source = await finish(before, unitID, signal, fetchAsset)
  if (disposal !== null && disposal.item_unit_id !== unitID) throw new ApiError('INVALID_RESPONSE')
  return { ...source, disposal }
}
export async function readAssetDetail(unitID: string, query: AssetSummaryQuery, signal: AbortSignal): Promise<AssetDetail> {
  const values = { ...query }
  const before = await context(unitID, signal, fetchAsset)
  const disposal = await optional(() => fetchAssetDisposal(unitID, signal))
  const summary = await fetchAssetCostSummary(unitID, values, signal)
  const source = await finish(before, unitID, signal, fetchAsset)
  const asset = source.asset
  if (disposal !== null && (disposal.item_unit_id !== unitID || disposal.currency !== asset.currency) || summary.unit_id !== unitID || summary.currency !== asset.currency ||
    summary.cost_basis !== asset.cost_basis || summary.usage_unit !== asset.usage_unit || summary.estimated_residual_minor !== asset.estimated_residual_minor ||
    summary.realized_proceeds_minor !== (disposal?.realized_proceeds_minor ?? 0) || asset.cost_basis === 'manual' && summary.acquisition_minor !== asset.manual_acquisition_minor) throw new ApiError('INVALID_RESPONSE')
  return { ...source, disposal, summary }
}
