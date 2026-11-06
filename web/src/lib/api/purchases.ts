import { createQuerySerializer } from 'openapi-fetch'
import { z } from 'zod'
import { apiClient, requestData, validText, type WriteContext } from './client'
import { itemPageQuerySchema, itemSchema, type Item } from './catalog'
import { createHeaders, idSchema, input, pageQuerySchema, pathIDSchema, resourceRequest, revisionHeaders, revisionSchema, text,
  type RootWriteContext } from './domain'
import type { components, operations } from './schema'

export type Purchase = components['schemas']['Purchase']
export type PurchaseLine = components['schemas']['PurchaseLine']
export type PurchasesPage = components['schemas']['PurchaseList']
export type PurchaseLinesPage = components['schemas']['PurchaseLineList']
export type PurchasePageQuery = NonNullable<operations['get_purchases']['parameters']['query']>
export type PurchaseOwnedPageQuery = NonNullable<operations['get_purchases_id_lines']['parameters']['query']>
export type PurchasePatchInput = components['schemas']['PurchasePatch'] & (
  Readonly<{ merchant_name: string }> | Readonly<{ purchased_at: string }> | Readonly<{ currency: string }> | Readonly<{ receipt_note: string }>
)
// One original header owns its proof and currency/date interpretation. A Line
// additionally retains the whole reviewed Item, not a refreshed loose revision.
export type PurchaseWriteContext = WriteContext & Readonly<{ purchase: Purchase; expectedRevision?: never }>
export type PurchaseLineContext = PurchaseWriteContext & Readonly<{ item: Item }>

const instantPattern = /^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})(?![\s\S])/
function canonicalInstant(value: string): string | undefined {
  const parts = instantPattern.exec(value)
  if (!parts || value.startsWith('0000-')) return undefined
  // Date converts only whole seconds. Keep every fractional digit separate;
  // parsing nanoseconds through Date would silently erase financial precision.
  const seconds = new Date(`${parts[1]}${parts[3]}`)
  if (!(seconds.getUTCFullYear() >= 1 && seconds.getUTCFullYear() <= 9999)) return undefined
  return `${seconds.toISOString().slice(0, -5)}.${(parts[2] ?? '').padEnd(9, '0')}Z`
}
export const purchaseInstantSchema = text(20, 35).pipe(z.iso.datetime({ offset: true })).refine((value) => canonicalInstant(value) !== undefined)
const fields = { merchant_name: text(1, 300), purchased_at: purchaseInstantSchema,
  currency: z.string().regex(/^[A-Z]{3}(?![\s\S])/), receipt_note: text(0, 10000) }
export const purchaseSchema: z.ZodType<Purchase> = z.strictObject({ id: idSchema, revision: revisionSchema, ...fields })
export const purchaseCreateSchema: z.ZodType<components['schemas']['PurchaseWrite']> = z.strictObject({ ...fields, receipt_note: fields.receipt_note.optional() })
const partialHeader = z.strictObject(fields).partial()
export const purchasePatchSchema: z.ZodType<PurchasePatchInput> = z.union([
  partialHeader.required({ merchant_name: true }), partialHeader.required({ purchased_at: true }),
  partialHeader.required({ currency: true }), partialHeader.required({ receipt_note: true }),
])
export const purchaseLineSchema: z.ZodType<PurchaseLine> = z.strictObject({
  id: idSchema, revision: revisionSchema, purchase_id: idSchema, item_id: idSchema, item_unit_id: idSchema.nullable(),
  quantity_base: revisionSchema, paid_minor: z.int().min(0), note: text(0, 10000),
})
const lineFields = { item_id: idSchema, item_revision: revisionSchema, paid_minor: z.int().min(0), note: text(0, 10000).optional() }
export const purchaseLineCreateSchema: z.ZodType<components['schemas']['PurchaseLineWrite']> = z.union([
  z.strictObject({ ...lineFields, item_unit_id: z.null(), quantity_base: revisionSchema }),
  z.strictObject({ ...lineFields, item_unit_id: idSchema, quantity_base: z.literal(1) }),
])
const serializeQuery = createQuerySerializer<PurchasePageQuery>()
// Refinements can run after a Unicode issue; never let the encoder throw.
const withinQueryBudget = (query: PurchasePageQuery) => Object.values(query).every((value) => typeof value !== 'string' || validText(value, 0, value.length)) &&
  serializeQuery(query).length <= 16_384
// Reuse the catalog's complete filter/civil-date policy, without its q field.
export const purchasePageQuerySchema: z.ZodType<PurchasePageQuery> = itemPageQuerySchema
  .refine((query) => !Object.hasOwn(query, 'q') && withinQueryBudget(query))
const ownedQuerySchema = pageQuerySchema.refine(withinQueryBudget)
const purchasesPageSchema: z.ZodType<PurchasesPage> = z.strictObject({ data: z.array(purchaseSchema).max(100), next_cursor: text(1, 2048).nullable() })
  .refine((page) => new Set(page.data.map((purchase) => purchase.id)).size === page.data.length)
const linesPageSchema: z.ZodType<PurchaseLinesPage> = z.strictObject({ data: z.array(purchaseLineSchema).max(100), next_cursor: text(1, 2048).nullable() })
  .refine((page) => new Set(page.data.map((line) => line.id)).size === page.data.length)
function validLivePage(page: Readonly<{ data: readonly unknown[]; next_cursor: string | null }>, response: Response, query: PurchaseOwnedPageQuery) {
  const limit = query.limit ?? 50
  return !response.headers.has('ETag') && page.data.length <= limit &&
    (page.next_cursor === null || page.data.length === limit && page.next_cursor !== query.cursor)
}

export async function fetchPurchase(id: string, signal: AbortSignal): Promise<Purchase> {
  const resourceID = input(pathIDSchema, id)
  return resourceRequest(purchaseSchema, signal, (signal) => apiClient.GET('/purchases/{id}', { signal, params: { path: { id: resourceID } } }), { id: resourceID }, 8_000)
}
export async function fetchPurchasesPage(query: PurchasePageQuery, signal: AbortSignal): Promise<PurchasesPage> {
  const values = input(purchasePageQuerySchema, query)
  return requestData(signal, (signal) => apiClient.GET('/purchases', { signal, params: { query: values } }), purchasesPageSchema, 8_000,
    (page, response) => validLivePage(page, response, values) && page.data.every((purchase) =>
      (values.currency === undefined || purchase.currency === values.currency) && (values.merchant === undefined || purchase.merchant_name === values.merchant)))
}
export async function fetchItemPurchasesPage(itemID: string, query: PurchaseOwnedPageQuery, signal: AbortSignal): Promise<PurchasesPage> {
  const owner = input(pathIDSchema, itemID)
  const values = input(ownedQuerySchema, query)
  return requestData(signal, (signal) => apiClient.GET('/items/{id}/purchases', { signal, params: { path: { id: owner }, query: values } }), purchasesPageSchema, 8_000,
    (page, response) => validLivePage(page, response, values))
}
export async function fetchPurchaseLinesPage(purchaseID: string, query: PurchaseOwnedPageQuery, signal: AbortSignal): Promise<PurchaseLinesPage> {
  const owner = input(pathIDSchema, purchaseID)
  const values = input(ownedQuerySchema, query)
  return requestData(signal, (signal) => apiClient.GET('/purchases/{id}/lines', { signal, params: { path: { id: owner }, query: values } }), linesPageSchema, 8_000,
    (page, response) => validLivePage(page, response, values) && page.data.every((line) => line.purchase_id === owner))
}

export async function createPurchase(body: components['schemas']['PurchaseWrite'], context: RootWriteContext): Promise<Purchase> {
  const values = input(purchaseCreateSchema, body)
  const headers = createHeaders(undefined, context)
  const schema = purchaseSchema.refine((purchase) => purchase.merchant_name === values.merchant_name && purchase.currency === values.currency &&
    purchase.receipt_note === (values.receipt_note ?? '') && purchase.purchased_at === canonicalInstant(values.purchased_at))
  return resourceRequest(schema, context.signal,
    (signal) => apiClient.POST('/purchases', { signal, body: values, params: { header: headers } }), { creating: true })
}
function reviewedPurchase(id: string, context: PurchaseWriteContext) {
  const purchase = input(purchaseSchema, context.purchase)
  input(z.undefined(), context.expectedRevision)
  input(z.literal(id), purchase.id)
  return { purchase, headers: revisionHeaders({ ...context, expectedRevision: purchase.revision }) }
}
export async function patchPurchase(id: string, body: PurchasePatchInput, context: PurchaseWriteContext): Promise<Purchase> {
  const resourceID = input(pathIDSchema, id)
  const values = input(purchasePatchSchema, body)
  const { purchase, headers } = reviewedPurchase(resourceID, context)
  const expected = { merchant_name: values.merchant_name ?? purchase.merchant_name, currency: values.currency ?? purchase.currency,
    receipt_note: values.receipt_note ?? purchase.receipt_note, purchased_at: values.purchased_at ?? purchase.purchased_at }
  const instant = canonicalInstant(expected.purchased_at)
  // Equivalent/omitted instants retain the exact historical spelling. Only an
  // actual instant change uses canonical UTC nanoseconds in the frozen reply.
  const purchasedAt = instant === canonicalInstant(purchase.purchased_at) ? purchase.purchased_at : instant
  const schema = purchaseSchema.refine((value) => value.merchant_name === expected.merchant_name && value.currency === expected.currency &&
    value.receipt_note === expected.receipt_note && value.purchased_at === purchasedAt)
  return resourceRequest(schema, context.signal,
    (signal) => apiClient.PATCH('/purchases/{id}', { signal, body: values, params: { path: { id: resourceID }, header: headers } }),
    { id: resourceID, expectedRevision: purchase.revision })
}
export async function createPurchaseLine(purchaseID: string, body: components['schemas']['PurchaseLineWrite'], context: PurchaseLineContext): Promise<PurchaseLine> {
  const owner = input(pathIDSchema, purchaseID)
  const values = input(purchaseLineCreateSchema, body)
  const { headers } = reviewedPurchase(owner, context)
  const item = input(itemSchema, context.item)
  input(z.literal(item.id), values.item_id)
  input(z.literal(item.revision), values.item_revision)
  if (item.tracking_mode === 'quantity') input(z.null(), values.item_unit_id)
  const schema = purchaseLineSchema.refine((line) => line.purchase_id === owner && line.item_id === item.id &&
    line.item_unit_id === values.item_unit_id && line.quantity_base === values.quantity_base && line.paid_minor === values.paid_minor && line.note === (values.note ?? ''))
  return resourceRequest(schema, context.signal,
    (signal) => apiClient.POST('/purchases/{id}/lines', { signal, body: values, params: { path: { id: owner }, header: headers } }), { creating: true })
}
