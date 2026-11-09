import { z } from 'zod'
import { apiClient, requestData, type WriteContext } from './client'
import { assetSchema, disposalDataSchema, type Asset } from './assets'
import { itemSchema, type Item } from './catalog'
import { cursorSchema, idSchema, input, pathIDSchema, resourceRequest, revisionHeaders, revisionSchema, text } from './domain'
import { canonicalInstant, ownedQuerySchema, purchaseInstantSchema, purchaseLineCreateSchema, purchaseLineSchema, purchaseSchema, validLivePage,
  type Purchase, type PurchaseLine, type PurchaseOwnedPageQuery } from './purchases'
import type { components } from './schema'

type Schemas = components['schemas']
export type PurchaseLineCorrection = Schemas['PurchaseLineCorrection']
export type PurchaseLineCorrectionWrite = Schemas['PurchaseLineCorrectionWrite']
export type PurchaseLineCorrectionPage = Schemas['PurchaseLineCorrectionList']
export type DisposalData = Schemas['DisposalData']
export type DisposalRecord = Schemas['DisposalRecord']
export type DisposalCorrection = Schemas['DisposalCorrection']
export type DisposalCorrectionWrite = Schemas['DisposalCorrectionWrite']
export type DisposalCorrectionPage = Schemas['DisposalCorrectionList']

// These contexts retain the original reviewed sources, not refreshed counters.
// The nullable replacement Item owns its body proof, not another HTTP header.
export type PurchaseCorrectionContext = WriteContext & Readonly<{
  purchase: Purchase; line: PurchaseLine; item: Item; replacementItem: Item | null; expectedRevision?: never
}>
export type DisposalCorrectionContext = WriteContext & Readonly<{
  disposal: DisposalRecord; asset: Asset; expectedRevision?: never
}>

const reason = text(1, 10000)
const purchaseWriteFields = { line_id: idSchema, line_revision: revisionSchema, item_revision: revisionSchema, reason }
export const purchaseCorrectionWriteSchema: z.ZodType<PurchaseLineCorrectionWrite> = z.discriminatedUnion('action', [
  z.strictObject({ ...purchaseWriteFields, action: z.literal('void'), replacement: z.null() }),
  z.strictObject({ ...purchaseWriteFields, action: z.literal('replace'), replacement: purchaseLineCreateSchema }),
])

const purchaseEntryFields = { id: idSchema, revision: z.literal(1), purchase_id: idSchema, reason, created_at: purchaseInstantSchema, before: purchaseLineSchema }
export const purchaseCorrectionSchema: z.ZodType<PurchaseLineCorrection> = z.discriminatedUnion('action', [
  z.strictObject({ ...purchaseEntryFields, action: z.literal('void'), after: z.null() }),
  z.strictObject({ ...purchaseEntryFields, action: z.literal('replace'), after: purchaseLineSchema.extend({ revision: z.literal(1) }) }),
]).refine((entry) => entry.before.purchase_id === entry.purchase_id && entry.before.revision < Number.MAX_SAFE_INTEGER &&
  (entry.after === null || entry.after.purchase_id === entry.purchase_id && entry.after.id !== entry.before.id))

const purchasePageSchema: z.ZodType<PurchaseLineCorrectionPage> = z.strictObject({ data: z.array(purchaseCorrectionSchema).max(100), next_cursor: cursorSchema().nullable() })
  .refine((page) => new Set(page.data.map((entry) => entry.id)).size === page.data.length)

const disposalRecordObject = z.strictObject({ item_unit_id: idSchema, revision: revisionSchema, original: disposalDataSchema, current: disposalDataSchema.nullable() })
function ownedDisposal(record: DisposalRecord) {
  return record.original.item_unit_id === record.item_unit_id && (record.current === null || record.current.item_unit_id === record.item_unit_id)
}
export const disposalRecordSchema: z.ZodType<DisposalRecord> = disposalRecordObject.refine(ownedDisposal)
const financialFields = disposalDataSchema.omit({ item_unit_id: true }).extend({ note: text(0, 10000).optional() })
export const disposalFinancialWriteSchema: z.ZodType<Schemas['DisposalFinancialWrite']> = z.discriminatedUnion('kind', [
  financialFields.extend({ kind: z.literal('sold') }),
  financialFields.extend({ kind: z.literal('donated'), realized_proceeds_minor: z.literal(0) }),
  financialFields.extend({ kind: z.literal('recycled'), realized_proceeds_minor: z.literal(0) }),
  financialFields.extend({ kind: z.literal('discarded'), realized_proceeds_minor: z.literal(0) }),
  financialFields.extend({ kind: z.literal('other'), realized_proceeds_minor: z.literal(0) }),
])
const disposalWriteFields = { asset_revision: revisionSchema, reason }
export const disposalCorrectionWriteSchema: z.ZodType<DisposalCorrectionWrite> = z.discriminatedUnion('action', [
  z.strictObject({ ...disposalWriteFields, action: z.literal('void'), replacement: z.null() }),
  z.strictObject({ ...disposalWriteFields, action: z.literal('replace'), replacement: disposalFinancialWriteSchema }),
])
function sameDisposalData(a: DisposalData, b: DisposalData) {
  return a.item_unit_id === b.item_unit_id && a.kind === b.kind && a.disposed_at === b.disposed_at &&
    a.realized_proceeds_minor === b.realized_proceeds_minor && a.currency === b.currency && a.note === b.note
}
function sameDisposalRecord(a: DisposalRecord, b: DisposalRecord) {
  return a.item_unit_id === b.item_unit_id && a.revision === b.revision && sameDisposalData(a.original, b.original) &&
    (a.current === null ? b.current === null : b.current !== null && sameDisposalData(a.current, b.current))
}
const disposalEntryFields = { id: idSchema, revision: z.literal(1), item_unit_id: idSchema, reason, created_at: purchaseInstantSchema, before: disposalRecordSchema }
export const disposalCorrectionSchema: z.ZodType<DisposalCorrection> = z.discriminatedUnion('action', [
  z.strictObject({ ...disposalEntryFields, action: z.literal('void'), after: disposalRecordObject.extend({ current: z.null() }).refine(ownedDisposal) }),
  z.strictObject({ ...disposalEntryFields, action: z.literal('replace'), after: disposalRecordObject.extend({ current: disposalDataSchema }).refine(ownedDisposal) }),
]).refine((entry) => entry.item_unit_id === entry.before.item_unit_id && entry.item_unit_id === entry.after.item_unit_id &&
  entry.before.revision < Number.MAX_SAFE_INTEGER && entry.after.revision === entry.before.revision + 1 &&
  sameDisposalData(entry.before.original, entry.after.original) && (entry.action !== 'void' || entry.before.current !== null))
const disposalPageSchema: z.ZodType<DisposalCorrectionPage> = z.strictObject({ data: z.array(disposalCorrectionSchema).max(100), next_cursor: cursorSchema().nullable() })
  .refine((page) => new Set(page.data.map((entry) => entry.id)).size === page.data.length)

function sameLine(a: PurchaseLine, b: PurchaseLine) {
  return a.id === b.id && a.revision === b.revision && a.purchase_id === b.purchase_id && a.item_id === b.item_id &&
    a.item_unit_id === b.item_unit_id && a.quantity_base === b.quantity_base && a.paid_minor === b.paid_minor && a.note === b.note
}

export async function fetchPurchaseCorrectionsPage(purchaseID: string, query: PurchaseOwnedPageQuery, signal: AbortSignal): Promise<PurchaseLineCorrectionPage> {
  const id = input(pathIDSchema, purchaseID), values = input(ownedQuerySchema, query)
  return requestData(signal, (signal) => apiClient.GET('/purchases/{id}/line-corrections', { signal, params: { path: { id }, query: values } }), purchasePageSchema, 8_000,
    (page, response) => validLivePage(page, response, values) && page.data.every((entry) => entry.purchase_id === id))
}
export async function correctPurchaseLine(purchaseID: string, body: PurchaseLineCorrectionWrite, context: PurchaseCorrectionContext): Promise<PurchaseLineCorrection> {
  const id = input(pathIDSchema, purchaseID), values = input(purchaseCorrectionWriteSchema, body)
  const purchase = input(purchaseSchema, context.purchase), line = input(purchaseLineSchema, context.line), item = input(itemSchema, context.item)
  const replacement = input(itemSchema.nullable(), context.replacementItem)
  input(z.undefined(), context.expectedRevision); input(z.literal(id), purchase.id); input(z.literal(id), line.purchase_id)
  input(z.literal(item.id), line.item_id); input(z.literal(line.id), values.line_id); input(z.literal(line.revision), values.line_revision)
  input(z.literal(item.revision), values.item_revision)
  if (values.replacement === null) input(z.null(), replacement)
  else {
    const target = input(itemSchema, replacement)
    input(z.literal(target.id), values.replacement.item_id); input(z.literal(target.revision), values.replacement.item_revision)
    if (target.tracking_mode === 'quantity') input(z.null(), values.replacement.item_unit_id)
    if (target.id === item.id) input(z.literal(JSON.stringify(item)), JSON.stringify(target))
  }
  const headers = revisionHeaders({ ...context, expectedRevision: purchase.revision })
  const schema = purchaseCorrectionSchema.refine((entry) => entry.purchase_id === id && entry.action === values.action && entry.reason === values.reason &&
    sameLine(entry.before, line) && (values.replacement === null ? entry.after === null : entry.after !== null &&
      entry.after.item_id === values.replacement.item_id && entry.after.item_unit_id === values.replacement.item_unit_id &&
      entry.after.quantity_base === values.replacement.quantity_base && entry.after.paid_minor === values.replacement.paid_minor && entry.after.note === (values.replacement.note ?? '')))
  return resourceRequest(schema, context.signal,
    (signal) => apiClient.POST('/purchases/{id}/line-corrections', { signal, body: values, params: { path: { id }, header: headers } }), { creating: true })
}

export async function fetchDisposalRecord(unitID: string, signal: AbortSignal): Promise<DisposalRecord> {
  const id = input(pathIDSchema, unitID)
  return requestData(signal, (signal) => apiClient.GET('/units/{id}/disposal-record', { signal, params: { path: { id } } }), disposalRecordSchema, 8_000,
    (record, response) => record.item_unit_id === id && response.headers.get('ETag') === `"${record.revision}"`)
}
export async function fetchDisposalCorrectionsPage(unitID: string, query: PurchaseOwnedPageQuery, signal: AbortSignal): Promise<DisposalCorrectionPage> {
  const id = input(pathIDSchema, unitID), values = input(ownedQuerySchema, query)
  return requestData(signal, (signal) => apiClient.GET('/units/{id}/disposal-corrections', { signal, params: { path: { id }, query: values } }), disposalPageSchema, 8_000,
    (page, response) => validLivePage(page, response, values) && page.data.every((entry) => entry.item_unit_id === id))
}
export async function correctDisposal(unitID: string, body: DisposalCorrectionWrite, context: DisposalCorrectionContext): Promise<DisposalCorrection> {
  const id = input(pathIDSchema, unitID), values = input(disposalCorrectionWriteSchema, body)
  const disposal = input(disposalRecordSchema, context.disposal), asset = input(assetSchema, context.asset)
  input(z.undefined(), context.expectedRevision); input(z.literal(id), disposal.item_unit_id); input(z.literal(id), asset.item_unit_id)
  input(z.literal(asset.revision), values.asset_revision)
  if (values.replacement === null) input(disposalDataSchema, disposal.current)
  else input(z.literal(asset.currency), values.replacement.currency)
  const headers = revisionHeaders({ ...context, expectedRevision: disposal.revision })
  const retained = disposal.current?.disposed_at ?? disposal.original.disposed_at
  const proposed = values.replacement === null ? null : {
    item_unit_id: id, kind: values.replacement.kind, currency: values.replacement.currency, realized_proceeds_minor: values.replacement.realized_proceeds_minor,
    note: values.replacement.note ?? '', disposed_at: canonicalInstant(values.replacement.disposed_at) === canonicalInstant(retained) ? retained : canonicalInstant(values.replacement.disposed_at)!,
  }
  const schema = disposalCorrectionSchema.refine((entry) => entry.item_unit_id === id && entry.action === values.action && entry.reason === values.reason &&
    sameDisposalRecord(entry.before, disposal) && (proposed === null ? entry.after.current === null : entry.after.current !== null && sameDisposalData(entry.after.current, proposed)))
  return resourceRequest(schema, context.signal,
    (signal) => apiClient.POST('/units/{id}/disposal-corrections', { signal, body: values, params: { path: { id }, header: headers } }), { creating: true })
}
