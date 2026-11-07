import { messages } from '@/lib/messages'
import type { AssetCommand } from './commands'
import type { AssetCostsSource, AssetLifecycleSource, AssetProfileSource, AssetUsageSource } from './model'
import { sameRecord } from '@/features/purchases/display'

export type AssetReadSource = AssetProfileSource | AssetUsageSource | AssetCostsSource | AssetLifecycleSource
export function sameAssetSource(before: AssetReadSource | null | undefined, current: AssetReadSource | null | undefined): boolean {
  if (!before || !current) return before === current
  if (!sameRecord(before.unit, current.unit) || !sameRecord(before.item, current.item) ||
    (before.asset === null || current.asset === null ? before.asset !== current.asset : !sameRecord(before.asset, current.asset))) return false
  if ('usage' in before || 'usage' in current) return 'usage' in before && 'usage' in current && before.usage.length === current.usage.length && before.usage.every((row, index) => sameRecord(row, current.usage[index]))
  if ('costs' in before || 'costs' in current) return 'costs' in before && 'costs' in current && before.costs.length === current.costs.length && before.costs.every((row, index) => sameRecord(row, current.costs[index]))
  if ('disposal' in before || 'disposal' in current) return 'disposal' in before && 'disposal' in current && (before.disposal === null || current.disposal === null ? before.disposal === current.disposal : sameRecord(before.disposal, current.disposal))
  return true
}
export const assetInterpretation = (source: AssetProfileSource) => JSON.stringify(source.asset === null ? null : [source.asset.cost_basis, source.asset.currency, source.asset.manual_acquisition_minor, source.asset.usage_unit, source.asset.estimated_residual_minor])
export const assetCommandCurrent = (command: AssetCommand, current: AssetReadSource | null | undefined) => sameAssetSource(command.source, current)
export function assetValue(name: string, value: unknown): string {
  const text = messages.assets
  if (value === null || value === undefined) return text.unknownValue
  const labels: Record<string, Readonly<Record<string, string>>> = { cost_basis: text.bases, usage_unit: text.usageUnits, event_type: text.eventTypes, direction: text.directions, kind: { ...text.costKinds, ...text.disposalKinds } }
  if (typeof value === 'string' && labels[name]?.[value]) return `${labels[name][value]} (${value})`
  return typeof value === 'string' ? JSON.stringify(value) : String(value)
}
