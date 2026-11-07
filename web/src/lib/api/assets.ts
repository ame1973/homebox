import { z } from 'zod'
import { apiClient, requestData, type WriteContext } from './client'
import { itemSchema, type Item } from './catalog'
import { idSchema, input, pathIDSchema, resourceRequest, revisionHeaders, revisionSchema, text } from './domain'
import { unitSchema, type Unit } from './holdings'
import { canonicalInstant, ownedQuerySchema, purchaseInstantSchema, purchasePageQuerySchema, validLivePage } from './purchases'
import type { components, operations } from './schema'

export type Asset = components['schemas']['Asset']
export type Usage = components['schemas']['Usage']
export type Cost = components['schemas']['Cost']
export type Disposal = components['schemas']['Disposal']
export type AssetCostSummary = components['schemas']['AssetCostSummary']
export type AssetsPage = components['schemas']['AssetList']
export type UsagePage = components['schemas']['UsageList']
export type CostsPage = components['schemas']['CostList']
export type AssetPageQuery = NonNullable<operations['get_assets']['parameters']['query']>
export type AssetOwnedPageQuery = NonNullable<operations['get_units_id_usage']['parameters']['query']>
export type AssetSummaryQuery = NonNullable<operations['get_units_id_cost_summary']['parameters']['query']>
export type AssetWriteContext = WriteContext & Readonly<{ asset: Asset; expectedRevision?: never }>
export type AssetProfileContext = WriteContext & Readonly<{ unit: Unit; item: Item; asset: Asset | null; expectedRevision?: never }>
export type AssetLifecycleContext = AssetWriteContext & Readonly<{ unit: Unit }>
// Disposal is retained review/reply context, never an additional wire proof.
export type AssetDisposalContext = AssetLifecycleContext & Readonly<{ disposal: Disposal | null }>

const currency = z.string().regex(/^[A-Z]{3}(?![\s\S])/)
const usageUnit = z.enum(['times', 'minutes', 'cycles'])
const amount = z.int().min(0)
const delta = z.int().refine((value) => value !== 0)
const note = text(0, 10000)
const profileFields = { cost_basis: z.enum(['purchase', 'manual']), currency, manual_acquisition_minor: amount.nullable(),
  in_service_at: purchaseInstantSchema.nullable(), retired_at: purchaseInstantSchema.nullable(), estimated_residual_minor: amount.nullable(), usage_unit: usageUnit }
// Retained representation is not today's new-money/basis/correction policy.
export const assetSchema: z.ZodType<Asset> = z.strictObject({ item_unit_id: idSchema, revision: revisionSchema, ...profileFields })
const usageFields = { delta, usage_unit: usageUnit, event_type: z.enum(['use', 'correction']), occurred_at: purchaseInstantSchema }
export const usageSchema: z.ZodType<Usage> = z.strictObject({ id: idSchema, item_unit_id: idSchema, revision: revisionSchema, ...usageFields, note, correction_of: idSchema.nullable() })
const costFields = { amount_minor: revisionSchema, currency, kind: z.enum(['maintenance', 'repair', 'accessory', 'fee', 'refund', 'adjustment']),
  direction: z.enum(['expense', 'credit']), occurred_at: purchaseInstantSchema }
export const costSchema: z.ZodType<Cost> = z.strictObject({ id: idSchema, item_unit_id: idSchema, revision: revisionSchema, ...costFields, note, correction_of: idSchema.nullable() })
const disposalFields = { kind: z.enum(['sold', 'donated', 'recycled', 'discarded', 'other']), disposed_at: purchaseInstantSchema, realized_proceeds_minor: amount, currency, note }
export const disposalSchema: z.ZodType<Disposal> = z.strictObject({ item_unit_id: idSchema, revision: revisionSchema, ...disposalFields })
export const assetProfileWriteSchema: z.ZodType<components['schemas']['AssetWrite']> = z.union([
  z.strictObject({ ...profileFields, item_revision: revisionSchema, asset_revision: revisionSchema.nullable(), cost_basis: z.literal('manual'), manual_acquisition_minor: amount }),
  z.strictObject({ ...profileFields, item_revision: revisionSchema, asset_revision: revisionSchema.nullable(), cost_basis: z.literal('purchase'), manual_acquisition_minor: z.null() }),
])
export const usageWriteSchema: z.ZodType<components['schemas']['UsageWrite']> = z.union([
  z.strictObject({ ...usageFields, event_type: z.literal('use'), delta: revisionSchema, note: note.optional(), correction_of: z.null().optional() }),
  z.strictObject({ ...usageFields, event_type: z.literal('correction'), note: note.optional(), correction_of: idSchema }),
])
export const costWriteSchema: z.ZodType<components['schemas']['CostWrite']> = z.union([
  z.strictObject({ ...costFields, kind: z.enum(['maintenance', 'repair', 'accessory', 'fee', 'refund']), note: note.optional(), correction_of: z.null().optional() }),
  z.strictObject({ ...costFields, kind: z.literal('adjustment'), note: note.optional(), correction_of: idSchema }),
])
export const assetRetireSchema: z.ZodType<components['schemas']['UnitRetireInput']> = z.strictObject({ unit_revision: revisionSchema, retired_at: purchaseInstantSchema, note: note.optional() })
export const assetDisposalSchema: z.ZodType<components['schemas']['DisposalWrite']> = z.union([
  z.strictObject({ ...disposalFields, unit_revision: revisionSchema, kind: z.literal('sold'), note: note.optional() }),
  z.strictObject({ ...disposalFields, unit_revision: revisionSchema, kind: z.enum(['donated', 'recycled', 'discarded', 'other']), realized_proceeds_minor: z.literal(0), note: note.optional() }),
])
export const assetPageQuerySchema: z.ZodType<AssetPageQuery> = purchasePageQuerySchema
export const assetSummaryQuerySchema: z.ZodType<AssetSummaryQuery> = z.strictObject({ as_of: purchaseInstantSchema.optional() })
const assetsPageSchema: z.ZodType<AssetsPage> = z.strictObject({ data: z.array(assetSchema).max(100), next_cursor: text(1, 2048).nullable() })
  .refine((page) => new Set(page.data.map((asset) => asset.item_unit_id)).size === page.data.length)
const usagePageSchema: z.ZodType<UsagePage> = z.strictObject({ data: z.array(usageSchema).max(100), next_cursor: text(1, 2048).nullable() })
  .refine((page) => new Set(page.data.map((event) => event.id)).size === page.data.length)
const costsPageSchema: z.ZodType<CostsPage> = z.strictObject({ data: z.array(costSchema).max(100), next_cursor: text(1, 2048).nullable() })
  .refine((page) => new Set(page.data.map((event) => event.id)).size === page.data.length)

function consistentSummary(value: AssetCostSummary) {
  const money = [value.acquisition_minor, value.extra_minor, value.realized_proceeds_minor, value.net_cost_minor, value.estimated_residual_minor, value.projected_net_cost_minor]
  if (!money.every((number) => number === null || Number.isSafeInteger(number))) return false
  // Validate only returned breakdown arithmetic, never reconstruct eligibility
  // from live pages. Safe operands can still exceed safe range when added.
  const gross = BigInt(value.acquisition_minor) + BigInt(value.extra_minor)
  if (BigInt(value.net_cost_minor) !== gross - BigInt(value.realized_proceeds_minor)) return false
  if (value.estimated_residual_minor === null ? value.projected_net_cost_minor !== null :
    value.projected_net_cost_minor === null || BigInt(value.projected_net_cost_minor) !== gross - BigInt(value.estimated_residual_minor)) return false
  const ratio = (total: number | null, divisor: number | null) => total !== null && divisor !== null && divisor > 0 ? total / divisor : null
  return value.per_day_minor === ratio(value.net_cost_minor, value.elapsed_days) && value.per_use_minor === ratio(value.net_cost_minor, value.total_usage) &&
    value.projected_per_day_minor === ratio(value.projected_net_cost_minor, value.elapsed_days) && value.projected_per_use_minor === ratio(value.projected_net_cost_minor, value.total_usage)
}
export const assetCostSummarySchema: z.ZodType<AssetCostSummary> = z.strictObject({
  unit_id: idSchema, currency, cost_basis: profileFields.cost_basis, acquisition_minor: amount, extra_minor: z.int(), realized_proceeds_minor: amount,
  estimated_residual_minor: amount.nullable(), net_cost_minor: z.int(), projected_net_cost_minor: z.int().nullable(), elapsed_days: revisionSchema.nullable(),
  total_usage: amount, usage_unit: usageUnit, per_day_minor: z.number().nullable(), per_use_minor: z.number().nullable(),
  projected_per_day_minor: z.number().nullable(), projected_per_use_minor: z.number().nullable(), as_of: purchaseInstantSchema,
  timezone: text(1, 100), day_policy: z.literal('inclusive_civil_days'), source_revision: amount,
}).refine(consistentSummary)

export async function fetchAsset(unitID: string, signal: AbortSignal): Promise<Asset> {
  const id = input(pathIDSchema, unitID)
  return requestData(signal, (signal) => apiClient.GET('/units/{id}/asset', { signal, params: { path: { id } } }), assetSchema, 8_000,
    (asset, response) => asset.item_unit_id === id && response.headers.get('ETag') === `"${asset.revision}"`)
}
export async function fetchAssetsPage(query: AssetPageQuery, signal: AbortSignal): Promise<AssetsPage> {
  const values = input(assetPageQuerySchema, query)
  return requestData(signal, (signal) => apiClient.GET('/assets', { signal, params: { query: values } }), assetsPageSchema, 8_000,
    (page, response) => validLivePage(page, response, values) && page.data.every((asset) => values.currency === undefined || asset.currency === values.currency))
}
export async function fetchAssetUsagePage(unitID: string, query: AssetOwnedPageQuery, signal: AbortSignal): Promise<UsagePage> {
  const id = input(pathIDSchema, unitID), values = input(ownedQuerySchema, query)
  return requestData(signal, (signal) => apiClient.GET('/units/{id}/usage', { signal, params: { path: { id }, query: values } }), usagePageSchema, 8_000,
    (page, response) => validLivePage(page, response, values) && page.data.every((event) => event.item_unit_id === id))
}
export async function fetchAssetCostsPage(unitID: string, query: AssetOwnedPageQuery, signal: AbortSignal): Promise<CostsPage> {
  const id = input(pathIDSchema, unitID), values = input(ownedQuerySchema, query)
  return requestData(signal, (signal) => apiClient.GET('/units/{id}/costs', { signal, params: { path: { id }, query: values } }), costsPageSchema, 8_000,
    (page, response) => validLivePage(page, response, values) && page.data.every((event) => event.item_unit_id === id))
}
export async function fetchAssetDisposal(unitID: string, signal: AbortSignal): Promise<Disposal> {
  const id = input(pathIDSchema, unitID)
  return requestData(signal, (signal) => apiClient.GET('/units/{id}/disposal', { signal, params: { path: { id } } }), disposalSchema, 8_000,
    (disposal, response) => disposal.item_unit_id === id && response.headers.get('ETag') === `"${disposal.revision}"`)
}
export async function fetchAssetCostSummary(unitID: string, query: AssetSummaryQuery, signal: AbortSignal): Promise<AssetCostSummary> {
  const id = input(pathIDSchema, unitID), values = input(assetSummaryQuerySchema, query)
  return requestData(signal, (signal) => apiClient.GET('/units/{id}/cost-summary', { signal, params: { path: { id }, query: values } }), assetCostSummarySchema, 8_000,
    (summary, response) => !response.headers.has('ETag') && summary.unit_id === id && (values.as_of === undefined || summary.as_of === canonicalInstant(values.as_of)))
}

function acceptedInstant(value: string | null, retained: string | null = null) {
  if (value === null) return null
  const canonical = canonicalInstant(value)
  return retained !== null && canonical === canonicalInstant(retained) ? retained : canonical
}
export async function putAssetProfile(unitID: string, body: components['schemas']['AssetWrite'], context: AssetProfileContext): Promise<Asset> {
  const id = input(pathIDSchema, unitID), values = input(assetProfileWriteSchema, body)
  const unit = input(unitSchema, context.unit), item = input(itemSchema, context.item), before = input(assetSchema.nullable(), context.asset)
  input(z.undefined(), context.expectedRevision)
  input(z.literal(id), unit.id); input(z.literal(item.id), unit.item_id); input(z.literal('individual'), item.tracking_mode)
  input(z.literal(item.revision), values.item_revision); input(z.literal(before?.revision ?? null), values.asset_revision)
  if (before !== null) input(z.literal(id), before.item_unit_id)
  const headers = revisionHeaders({ ...context, expectedRevision: unit.revision })
  const service = acceptedInstant(values.in_service_at, before?.in_service_at), retired = acceptedInstant(values.retired_at, before?.retired_at)
  const matches = (asset: Asset) => asset.item_unit_id === id && asset.cost_basis === values.cost_basis && asset.currency === values.currency &&
    asset.manual_acquisition_minor === values.manual_acquisition_minor && asset.in_service_at === service && asset.retired_at === retired &&
    asset.estimated_residual_minor === values.estimated_residual_minor && asset.usage_unit === values.usage_unit
  const revision = before === null ? 1 : before.revision + Number(!matches(before))
  return requestData(context.signal, (signal) => apiClient.PUT('/units/{id}/asset', { signal, body: values, params: { path: { id }, header: headers } }),
    assetSchema.refine(matches), 30_000, (asset, response) => asset.revision === revision && response.headers.get('ETag') === `"${asset.revision}"`)
}
function reviewedAsset(id: string, context: AssetWriteContext) {
  const asset = input(assetSchema, context.asset)
  input(z.undefined(), context.expectedRevision); input(z.literal(id), asset.item_unit_id)
  return { asset, headers: revisionHeaders({ ...context, expectedRevision: asset.revision }) }
}
export async function recordAssetUsage(unitID: string, body: components['schemas']['UsageWrite'], context: AssetWriteContext): Promise<Usage> {
  const id = input(pathIDSchema, unitID), values = input(usageWriteSchema, body)
  const { asset, headers } = reviewedAsset(id, context)
  input(z.literal(asset.usage_unit), values.usage_unit)
  const schema = usageSchema.refine((event) => event.item_unit_id === id && event.delta === values.delta && event.usage_unit === values.usage_unit &&
    event.event_type === values.event_type && event.occurred_at === canonicalInstant(values.occurred_at) && event.note === (values.note ?? '') && event.correction_of === (values.correction_of ?? null))
  return resourceRequest(schema, context.signal,
    (signal) => apiClient.POST('/units/{id}/usage', { signal, body: values, params: { path: { id }, header: headers } }), { creating: true })
}
export async function recordAssetCost(unitID: string, body: components['schemas']['CostWrite'], context: AssetWriteContext): Promise<Cost> {
  const id = input(pathIDSchema, unitID), values = input(costWriteSchema, body)
  const { asset, headers } = reviewedAsset(id, context)
  input(z.literal(asset.currency), values.currency)
  const schema = costSchema.refine((event) => event.item_unit_id === id && event.amount_minor === values.amount_minor && event.currency === values.currency &&
    event.kind === values.kind && event.direction === values.direction && event.occurred_at === canonicalInstant(values.occurred_at) && event.note === (values.note ?? '') && event.correction_of === (values.correction_of ?? null))
  return resourceRequest(schema, context.signal,
    (signal) => apiClient.POST('/units/{id}/costs', { signal, body: values, params: { path: { id }, header: headers } }), { creating: true })
}
function reviewedLifecycle(id: string, unitRevision: number, context: AssetLifecycleContext) {
  const reviewed = reviewedAsset(id, context), unit = input(unitSchema, context.unit)
  input(z.literal(id), unit.id); input(z.literal(unit.revision), unitRevision)
  return { ...reviewed, unit }
}
export async function retireAsset(unitID: string, body: components['schemas']['UnitRetireInput'], context: AssetLifecycleContext): Promise<Unit> {
  const id = input(pathIDSchema, unitID), values = input(assetRetireSchema, body)
  const { unit, headers } = reviewedLifecycle(id, values.unit_revision, context)
  const schema = unitSchema.refine((after) => after.item_id === unit.item_id && after.asset_code === unit.asset_code && after.location_id === unit.location_id &&
    after.serial_number === unit.serial_number && after.notes === unit.notes && after.status === 'retired' && after.revision === unit.revision + Number(unit.status !== 'retired'))
  return resourceRequest(schema, context.signal,
    (signal) => apiClient.POST('/units/{id}/retire', { signal, body: values, params: { path: { id }, header: headers } }), { id, expectedRevision: unit.revision })
}
export async function disposeAsset(unitID: string, body: components['schemas']['DisposalWrite'], context: AssetDisposalContext): Promise<Disposal> {
  const id = input(pathIDSchema, unitID), values = input(assetDisposalSchema, body)
  const { asset, headers } = reviewedLifecycle(id, values.unit_revision, context)
  const before = input(disposalSchema.nullable(), context.disposal)
  if (before !== null) input(z.literal(id), before.item_unit_id)
  input(z.literal(asset.currency), values.currency)
  const instant = acceptedInstant(values.disposed_at, before?.disposed_at)
  const schema = disposalSchema.refine((after) => after.item_unit_id === id && after.kind === values.kind && after.disposed_at === instant &&
    after.realized_proceeds_minor === values.realized_proceeds_minor && after.currency === values.currency && after.note === (values.note ?? ''))
  return requestData(context.signal, (signal) => apiClient.POST('/units/{id}/disposal', { signal, body: values, params: { path: { id }, header: headers } }), schema, 30_000,
    (disposal, response) => disposal.revision === (before?.revision ?? 1) && response.headers.get('ETag') === `"${disposal.revision}"`)
}
