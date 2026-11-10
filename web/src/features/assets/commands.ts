import { z } from 'zod'
import { ApiError, type WriteContext } from '@/lib/api/client'
import { itemSchema } from '@/lib/api/catalog'
import { input, pathIDSchema, text } from '@/lib/api/domain'
import { unitSchema, type Unit } from '@/lib/api/holdings'
import * as api from '@/lib/api/assets'
import type { components } from '@/lib/api/schema'
import { freezeReviewedCommand } from '@/lib/reviewed-command'
import type { AssetCostsSource, AssetLifecycleSource, AssetProfileSource, AssetUsageSource } from './model'

export type AssetCommandInput = Readonly<{ kind: 'put-profile'; source: AssetProfileSource; body: components['schemas']['AssetWrite'] }>
  | Readonly<{ kind: 'record-usage'; source: AssetUsageSource; body: components['schemas']['UsageWrite'] }>
  | Readonly<{ kind: 'record-cost'; source: AssetCostsSource; body: components['schemas']['CostWrite'] }>
  | Readonly<{ kind: 'retire-asset'; source: AssetLifecycleSource; body: components['schemas']['UnitRetireInput'] }>
  | Readonly<{ kind: 'dispose-asset'; source: AssetLifecycleSource; body: components['schemas']['DisposalWrite'] }>
export type AssetCommand = AssetCommandInput & Readonly<{ key: string }>
export type AssetResult = api.Asset | api.Usage | api.Cost | Unit | api.Disposal

const owner = { unit: unitSchema, item: itemSchema, asset: api.assetSchema }
const profileSource = z.strictObject({ ...owner, asset: api.assetSchema.nullable() })
const usageSource = z.strictObject({ ...owner, kind: z.literal('usage'), usage: z.array(api.usageSchema) })
const costsSource = z.strictObject({ ...owner, kind: z.literal('costs'), costs: z.array(api.costSchema) })
// An already reviewed detail may retain its calculation as display context. It
// never replaces the Asset/Unit proofs or establishes new financial eligibility.
const lifecycleSource = z.strictObject({ ...owner, disposal: api.disposalSchema.nullable(), summary: api.assetCostSummarySchema.optional() })
const proposalSchema: z.ZodType<AssetCommandInput> = z.discriminatedUnion('kind', [
  z.strictObject({ kind: z.literal('put-profile'), source: profileSource, body: api.assetProfileWriteSchema }),
  z.strictObject({ kind: z.literal('record-usage'), source: usageSource, body: api.usageWriteSchema }),
  z.strictObject({ kind: z.literal('record-cost'), source: costsSource, body: api.costWriteSchema }),
  z.strictObject({ kind: z.literal('retire-asset'), source: lifecycleSource, body: api.assetRetireSchema }),
  z.strictObject({ kind: z.literal('dispose-asset'), source: lifecycleSource, body: api.assetDisposalSchema }),
])
function checkedProposal(proposal: AssetCommandInput): AssetCommandInput {
  const checked = input(proposalSchema, proposal)
  const { unit, item } = checked.source
  input(pathIDSchema, unit.id)
  if (unit.item_id !== item.id || item.tracking_mode !== 'individual' || checked.source.asset !== null && checked.source.asset.item_unit_id !== unit.id) throw new ApiError('VALIDATION_ERROR')
  if (checked.kind === 'put-profile') {
    if (checked.body.item_revision !== item.revision || checked.body.asset_revision !== (checked.source.asset?.revision ?? null)) throw new ApiError('VALIDATION_ERROR')
    return checked
  }
  const asset = checked.source.asset
  if (checked.kind === 'record-usage') {
    const rows = checked.source.usage, body = checked.body
    if (new Set(rows.map((event) => event.id)).size !== rows.length || rows.some((event) => event.item_unit_id !== unit.id) || body.usage_unit !== asset.usage_unit) throw new ApiError('VALIDATION_ERROR')
    if (body.correction_of != null && !rows.some((event) => event.id === body.correction_of && event.usage_unit === body.usage_unit)) throw new ApiError('VALIDATION_ERROR')
  } else if (checked.kind === 'record-cost') {
    const rows = checked.source.costs, body = checked.body
    if (new Set(rows.map((event) => event.id)).size !== rows.length || rows.some((event) => event.item_unit_id !== unit.id) || body.currency !== asset.currency) throw new ApiError('VALIDATION_ERROR')
    if (body.correction_of != null && !rows.some((event) => event.id === body.correction_of && event.currency === body.currency)) throw new ApiError('VALIDATION_ERROR')
  } else if (checked.body.unit_revision !== unit.revision || checked.source.disposal !== null && checked.source.disposal.item_unit_id !== unit.id ||
    checked.kind === 'dispose-asset' && checked.body.currency !== asset.currency) throw new ApiError('VALIDATION_ERROR')
  return checked
}

export function freezeAssetCommand(proposal: AssetCommandInput): AssetCommand {
  return freezeReviewedCommand(checkedProposal(proposal))
}

// Only explicit original-command delivery. Never read/rebase a proof, adopt
// current source, persist credentials or install the frozen reply as current.
export async function sendAssetCommand(command: AssetCommand, csrfToken: string, signal: AbortSignal): Promise<AssetResult> {
  const { key, ...proposal } = command
  const checked = checkedProposal(proposal)
  const context: WriteContext = { csrfToken, signal, idempotencyKey: input(text(8, 128), key) }
  switch (checked.kind) {
    case 'put-profile': return api.putAssetProfile(checked.source.unit.id, checked.body, { ...context, ...checked.source })
    case 'record-usage': return api.recordAssetUsage(checked.source.unit.id, checked.body, { ...context, asset: checked.source.asset })
    case 'record-cost': return api.recordAssetCost(checked.source.unit.id, checked.body, { ...context, asset: checked.source.asset })
    case 'retire-asset': return api.retireAsset(checked.source.unit.id, checked.body, { ...context, asset: checked.source.asset, unit: checked.source.unit })
    case 'dispose-asset': return api.disposeAsset(checked.source.unit.id, checked.body, { ...context, asset: checked.source.asset, unit: checked.source.unit, disposal: checked.source.disposal })
  }
}

export function assetCommandReadRoots(command: AssetCommand): readonly (readonly string[])[] {
  const roots = [['assets'], ['catalog'], ['holdings'], ['purchases'], ['media']]
  if (command.kind === 'retire-asset' && !['retired', 'disposed'].includes(command.source.unit.status) || command.kind === 'dispose-asset' && command.source.unit.status !== 'disposed') roots.push(['locations'])
  return roots
}
