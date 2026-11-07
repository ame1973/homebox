import { z } from 'zod'
import { ApiError } from '@/lib/api/client'
import { itemSchema } from '@/lib/api/catalog'
import { input, parseSafeCount } from '@/lib/api/domain'
import { unitSchema } from '@/lib/api/holdings'
import { canonicalInstant } from '@/lib/api/purchases'
import * as api from '@/lib/api/assets'
import type { components } from '@/lib/api/schema'
import type { AssetCostsSource, AssetLifecycleSource, AssetProfileSource, AssetSource, AssetUsageSource } from './model'

const profileValues = z.strictObject({ cost_basis: z.string(), currency: z.string(), manual_acquisition_minor: z.string(), usage_unit: z.string(),
  service_decision: z.string(), in_service_at: z.string(), retirement_decision: z.string(), retired_at: z.string(), residual_decision: z.string(), estimated_residual_minor: z.string() })
const usageValues = z.strictObject({ delta: z.string(), usage_unit: z.string(), event_type: z.string(), occurred_at: z.string(), correction_of: z.string(), note: z.string() })
const costValues = z.strictObject({ amount_minor: z.string(), currency: z.string(), kind: z.string(), direction: z.string(), occurred_at: z.string(), correction_of: z.string(), note: z.string() })
const retireValues = z.strictObject({ retired_at: z.string(), note: z.string() })
const disposalValues = z.strictObject({ kind: z.string(), currency: z.string(), realized_proceeds_minor: z.string(), disposed_at: z.string(), note: z.string() })
export type AssetProfileValues = z.infer<typeof profileValues>
export type AssetUsageValues = z.infer<typeof usageValues>
export type AssetCostValues = z.infer<typeof costValues>
export type AssetRetireValues = z.infer<typeof retireValues>
export type AssetDisposalValues = z.infer<typeof disposalValues>
export { profileValues, usageValues, costValues, retireValues, disposalValues }
type Schemas = components['schemas']

function original(source: AssetProfileSource) {
  const unit = input(unitSchema, source.unit), item = input(itemSchema, source.item), asset = input(api.assetSchema.nullable(), source.asset)
  if (unit.item_id !== item.id || item.tracking_mode !== 'individual' || asset !== null && asset.item_unit_id !== unit.id) throw new ApiError('VALIDATION_ERROR')
  return { unit, item, asset }
}
function financial(source: AssetSource) {
  const checked = original(source)
  if (checked.asset === null) throw new ApiError('VALIDATION_ERROR')
  return { ...checked, asset: checked.asset }
}
function lifecycle(source: AssetLifecycleSource) {
  const checked = financial(source), disposal = input(api.disposalSchema.nullable(), source.disposal)
  if (disposal !== null && disposal.item_unit_id !== checked.unit.id) throw new ApiError('VALIDATION_ERROR')
  return { ...checked, disposal }
}
function sameOwner(originalSource: AssetProfileSource, current: AssetProfileSource) {
  const before = original(originalSource), after = original(current)
  if (before.unit.id !== after.unit.id || before.item.id !== after.item.id) throw new ApiError('VALIDATION_ERROR')
}
function nullable(decision: string, value: string) {
  if (decision !== 'unknown' && decision !== 'value') throw new ApiError('VALIDATION_ERROR')
  return decision === 'unknown' ? null : value
}
function signedCount(value: string): number | undefined {
  const negative = value.startsWith('-'), count = parseSafeCount(negative ? value.slice(1) : value, 1)
  return count === undefined ? undefined : negative ? -count : count
}

export function assetProfileDefaults(source: AssetProfileSource): AssetProfileValues {
  const { asset } = original(source)
  return { cost_basis: asset?.cost_basis ?? '', currency: asset?.currency ?? '', manual_acquisition_minor: asset?.manual_acquisition_minor == null ? '' : String(asset.manual_acquisition_minor), usage_unit: asset?.usage_unit ?? '',
    service_decision: asset === null ? '' : asset.in_service_at === null ? 'unknown' : 'value', in_service_at: asset?.in_service_at ?? '',
    retirement_decision: asset === null ? '' : asset.retired_at === null ? 'unknown' : 'value', retired_at: asset?.retired_at ?? '',
    residual_decision: asset === null ? '' : asset.estimated_residual_minor === null ? 'unknown' : 'value', estimated_residual_minor: asset?.estimated_residual_minor == null ? '' : String(asset.estimated_residual_minor) }
}
export function assetProfileBody(values: AssetProfileValues, source: AssetProfileSource): Schemas['AssetWrite'] {
  const checked = input(profileValues, values), { unit, item, asset } = original(source)
  const residual = nullable(checked.residual_decision, checked.estimated_residual_minor)
  const body = input(api.assetProfileWriteSchema, { cost_basis: checked.cost_basis, currency: checked.currency, usage_unit: checked.usage_unit,
    manual_acquisition_minor: checked.cost_basis === 'purchase' ? null : parseSafeCount(checked.manual_acquisition_minor) ?? NaN,
    in_service_at: nullable(checked.service_decision, checked.in_service_at), retired_at: nullable(checked.retirement_decision, checked.retired_at),
    estimated_residual_minor: residual === null ? null : parseSafeCount(residual) ?? NaN, item_revision: item.revision, asset_revision: asset?.revision ?? null })
  const retired = body.retired_at === null ? null : canonicalInstant(body.retired_at)
  if (asset !== null ? retired !== (asset.retired_at === null ? null : canonicalInstant(asset.retired_at)) : retired !== null && unit.status !== 'retired' && unit.status !== 'disposed') throw new ApiError('ASSET_LIFECYCLE_REQUIRED')
  if (retired !== null && body.in_service_at !== null && canonicalInstant(body.in_service_at)! > retired!) throw new ApiError('VALIDATION_ERROR')
  return body
}

export function assetUsageDefaults(source: AssetUsageSource): AssetUsageValues {
  return { delta: '', usage_unit: financial(source).asset.usage_unit, event_type: '', occurred_at: '', correction_of: '', note: '' }
}
export function assetUsageBody(values: AssetUsageValues, source: AssetUsageSource): Schemas['UsageWrite'] {
  const checked = input(usageValues, values), { unit, asset } = financial(source)
  if (source.kind !== 'usage' || !Array.isArray(source.usage) || checked.usage_unit !== asset.usage_unit) throw new ApiError('VALIDATION_ERROR')
  const body = input(api.usageWriteSchema, { delta: signedCount(checked.delta) ?? NaN, usage_unit: checked.usage_unit, event_type: checked.event_type,
    occurred_at: checked.occurred_at, correction_of: checked.event_type === 'use' ? null : checked.correction_of, note: checked.note })
  if (body.correction_of != null) {
    const target = input(api.usageSchema, source.usage.find((event) => event.id === body.correction_of))
    if (target.item_unit_id !== unit.id || target.usage_unit !== asset.usage_unit) throw new ApiError('VALIDATION_ERROR')
  }
  return body
}
export function assetCostDefaults(source: AssetCostsSource): AssetCostValues {
  return { amount_minor: '', currency: financial(source).asset.currency, kind: '', direction: '', occurred_at: '', correction_of: '', note: '' }
}
export function assetCostBody(values: AssetCostValues, source: AssetCostsSource): Schemas['CostWrite'] {
  const checked = input(costValues, values), { unit, asset } = financial(source)
  if (source.kind !== 'costs' || !Array.isArray(source.costs) || checked.currency !== asset.currency) throw new ApiError('VALIDATION_ERROR')
  const body = input(api.costWriteSchema, { amount_minor: parseSafeCount(checked.amount_minor, 1) ?? NaN, currency: checked.currency, kind: checked.kind, direction: checked.direction,
    occurred_at: checked.occurred_at, correction_of: checked.kind === 'adjustment' ? checked.correction_of : null, note: checked.note })
  if (body.correction_of != null) {
    const target = input(api.costSchema, source.costs.find((event) => event.id === body.correction_of))
    if (target.item_unit_id !== unit.id || target.currency !== asset.currency) throw new ApiError('VALIDATION_ERROR')
  }
  return body
}
export function assetRetireDefaults(source: AssetLifecycleSource): AssetRetireValues {
  return { retired_at: lifecycle(source).asset.retired_at ?? '', note: '' }
}
export function assetRetireBody(values: AssetRetireValues, source: AssetLifecycleSource): Schemas['UnitRetireInput'] {
  const { unit, asset } = lifecycle(source)
  const body = input(api.assetRetireSchema, { ...input(retireValues, values), unit_revision: unit.revision })
  const instant = canonicalInstant(body.retired_at)
  if (asset.retired_at !== null && instant !== canonicalInstant(asset.retired_at)) throw new ApiError('ASSET_HISTORY_LOCKED')
  if (asset.in_service_at !== null && instant! < canonicalInstant(asset.in_service_at)!) throw new ApiError('VALIDATION_ERROR')
  return body
}
export function assetDisposalDefaults(source: AssetLifecycleSource): AssetDisposalValues {
  const { asset, disposal } = lifecycle(source)
  return { kind: disposal?.kind ?? '', currency: disposal?.currency ?? asset.currency, realized_proceeds_minor: disposal === null ? '' : String(disposal.realized_proceeds_minor),
    disposed_at: disposal?.disposed_at ?? '', note: disposal?.note ?? '' }
}
export function assetDisposalBody(values: AssetDisposalValues, source: AssetLifecycleSource): Schemas['DisposalWrite'] {
  const checked = input(disposalValues, values), { unit, asset } = lifecycle(source)
  if (checked.currency !== asset.currency) throw new ApiError('VALIDATION_ERROR')
  return input(api.assetDisposalSchema, { ...checked, unit_revision: unit.revision, realized_proceeds_minor: parseSafeCount(checked.realized_proceeds_minor) ?? NaN })
}

// Adopt only on an explicit review action. Keep financial interpretation and
// nullable/value pairs intact rather than mixing money or repairing invalid text.
export function retainAssetProfileEdits(values: AssetProfileValues, before: AssetProfileSource, current: AssetProfileSource): AssetProfileValues {
  sameOwner(before, current)
  const checked = input(profileValues, values), previous = assetProfileDefaults(before), next = assetProfileDefaults(current)
  const groups: readonly (readonly (keyof AssetProfileValues)[])[] = [
    ['cost_basis', 'currency', 'manual_acquisition_minor', 'usage_unit', 'residual_decision', 'estimated_residual_minor'],
    ['service_decision', 'in_service_at'], ['retirement_decision', 'retired_at'],
  ]
  for (const group of groups) if (group.some((key) => checked[key] !== previous[key])) for (const key of group) next[key] = checked[key]
  return next
}
export function adoptAssetUsage(values: AssetUsageValues, before: AssetUsageSource, current: AssetUsageSource) {
  sameOwner(before, current)
  return { values: { ...input(usageValues, values), usage_unit: financial(current).asset.usage_unit }, source: structuredClone(current) }
}
export function adoptAssetCost(values: AssetCostValues, before: AssetCostsSource, current: AssetCostsSource) {
  sameOwner(before, current)
  return { values: { ...input(costValues, values), currency: financial(current).asset.currency }, source: structuredClone(current) }
}
export function retainAssetRetireEdits(values: AssetRetireValues, before: AssetLifecycleSource, current: AssetLifecycleSource): AssetRetireValues {
  sameOwner(before, current)
  const checked = input(retireValues, values)
  return { ...checked, retired_at: checked.retired_at === assetRetireDefaults(before).retired_at ? assetRetireDefaults(current).retired_at : checked.retired_at }
}
export function retainAssetDisposalEdits(values: AssetDisposalValues, before: AssetLifecycleSource, current: AssetLifecycleSource): AssetDisposalValues {
  sameOwner(before, current)
  const checked = input(disposalValues, values), previous = assetDisposalDefaults(before)
  return (Object.keys(previous) as (keyof AssetDisposalValues)[]).every((key) => checked[key] === previous[key]) ? assetDisposalDefaults(current) : { ...checked, currency: financial(current).asset.currency }
}
