import { z } from 'zod'
import { apiClient, requestData, type WriteContext } from './client'
import { civilDateSchema, itemMeasurementFields, itemSchema, type Item, type ItemWriteContext } from './catalog'
import { cursorSchema, deleteAckSchema, idSchema, input, pageQuerySchema, pathIDSchema, resourceRequest, revisionHeaders, revisionSchema, text,
  type RevisionWriteContext } from './domain'
import type { components, operations } from './schema'

export type Unit = components['schemas']['Unit']
export type Stock = components['schemas']['Stock']
export type UnitsPage = components['schemas']['UnitList']
export type StockPage = components['schemas']['StockList']
export type UnitPageQuery = NonNullable<operations['get_items_id_units']['parameters']['query']>
export type StockPageQuery = NonNullable<operations['get_inventory']['parameters']['query']>
export type UnitCreateContext = ItemWriteContext
export type UnitWriteContext = RevisionWriteContext & Readonly<{ itemID: string }>
export type UnitPatchInput = components['schemas']['UnitWrite'] & (
  Readonly<{ serial_number: string | null; notes?: string }> | Readonly<{ serial_number?: string | null; notes: string }>
)
// One reviewed Item interpretation owns both the aggregate proof and measurement.
// Do not pass a Stock row revision or an independently refreshed loose proof.
export type StockWriteContext = WriteContext & Readonly<{ item: Item; expectedRevision?: never }>

const unitStatus = z.enum(['stored', 'in_use', 'lent', 'lost', 'retired', 'disposed'])
const unitText = text(0, 10000)
export const unitSchema: z.ZodType<Unit> = z.strictObject({
  id: idSchema, item_id: idSchema, revision: revisionSchema, location_id: idSchema.nullable(),
  asset_code: text(1, 10000), status: unitStatus, serial_number: unitText.nullable(), notes: unitText,
})
export const stockSchema: z.ZodType<Stock> = z.strictObject({
  item_id: idSchema, location_id: idSchema, revision: revisionSchema, quantity_base: z.int().min(0), ...itemMeasurementFields,
})
export const unitCreateSchema: z.ZodType<components['schemas']['UnitCreate']> = z.strictObject({
  location_id: idSchema.nullable(), serial_number: unitText.nullable().optional(), notes: unitText.optional(),
  status: z.enum(['stored', 'in_use', 'lent', 'lost']).optional(),
})
const partialUnit = z.strictObject({ serial_number: unitText.nullable(), notes: unitText }).partial()
export const unitPatchSchema: z.ZodType<UnitPatchInput> = z.union([
  partialUnit.required({ serial_number: true }), partialUnit.required({ notes: true }),
])
export const unitMoveSchema: z.ZodType<components['schemas']['UnitMove']> = z.strictObject({ target_location_id: idSchema })
export const unitStatusSchema: z.ZodType<components['schemas']['UnitStatusInput']> = z.union([
  z.strictObject({ status: z.enum(['stored', 'in_use', 'lent', 'lost', 'retired']), note: unitText.optional() }),
  z.strictObject({ status: z.literal('disposed'), disposal_kind: z.enum(['sold', 'donated', 'recycled', 'discarded']), note: unitText.optional() }),
])
const stockInput = { item_id: idSchema, quantity_base: revisionSchema, note: unitText.optional() }
export const stockReceiveSchema: z.ZodType<components['schemas']['StockReceiveInput']> = z.strictObject({ ...stockInput, target_location_id: idSchema })
export const stockConsumeSchema: z.ZodType<components['schemas']['StockConsumeInput']> = z.strictObject({ ...stockInput, source_location_id: idSchema })
export const stockTransferSchema: z.ZodType<components['schemas']['StockTransferInput']> = z.strictObject({ ...stockInput, source_location_id: idSchema, target_location_id: idSchema })
  .refine((value) => value.source_location_id !== value.target_location_id)
export const stockAdjustSchema: z.ZodType<components['schemas']['StockAdjustInput']> = z.strictObject({
  ...stockInput, quantity_base: z.int().min(0), target_location_id: idSchema, reason: text(1, 10000),
})
export const stockPageQuerySchema: z.ZodType<StockPageQuery> = z.strictObject({
  ...pageQuerySchema.shape, cursor: cursorSchema(4096).optional(), from: civilDateSchema.optional(), to: civilDateSchema.optional(),
  item_id: idSchema.optional(), currency: z.string().regex(/^[A-Z]{3}(?![\s\S])/).optional(), category_id: idSchema.optional(), location_id: idSchema.optional(),
  scope: z.enum(['direct', 'subtree']).optional(), status: z.enum(['active', 'archived']).optional(), merchant: text(1, 300).optional(),
}).refine((query) => query.from === undefined || query.to === undefined || query.from <= query.to)
const unitsPageSchema: z.ZodType<UnitsPage> = z.strictObject({ data: z.array(unitSchema).max(100), next_cursor: cursorSchema().nullable() })
  .refine((page) => new Set(page.data.map((value) => value.id)).size === page.data.length)
const stockPageSchema: z.ZodType<StockPage> = z.strictObject({ data: z.array(stockSchema).max(100), next_cursor: cursorSchema(4096).nullable() })
  .refine((page) => new Set(page.data.map((value) => JSON.stringify([value.item_id, value.location_id]))).size === page.data.length)

function validLivePage(page: Readonly<{ data: readonly unknown[]; next_cursor: string | null }>, response: Response, query: UnitPageQuery) {
  const limit = query.limit ?? 50
  return !response.headers.has('ETag') && page.data.length <= limit &&
    (page.next_cursor === null || page.data.length === limit && page.next_cursor !== query.cursor)
}

export async function fetchUnit(id: string, signal: AbortSignal): Promise<Unit> {
  const resourceID = input(pathIDSchema, id)
  return resourceRequest(unitSchema, signal, (signal) => apiClient.GET('/units/{id}', { signal, params: { path: { id: resourceID } } }), { id: resourceID }, 8_000)
}

export async function fetchItemUnitsPage(itemID: string, query: UnitPageQuery, signal: AbortSignal): Promise<UnitsPage> {
  const owner = input(pathIDSchema, itemID)
  const values = input(pageQuerySchema, query)
  return requestData(signal, (signal) => apiClient.GET('/items/{id}/units', { signal, params: { path: { id: owner }, query: values } }), unitsPageSchema, 8_000,
    (page, response) => validLivePage(page, response, values) && page.data.every((unit) => unit.item_id === owner))
}

export async function fetchStockPage(query: StockPageQuery, signal: AbortSignal): Promise<StockPage> {
  const values = input(stockPageQuerySchema, query)
  return requestData(signal, (signal) => apiClient.GET('/inventory', { signal, params: { query: values } }), stockPageSchema, 8_000,
    (page, response) => validLivePage(page, response, values) &&
      page.data.every((stock) => (values.item_id === undefined || stock.item_id === values.item_id) &&
        (values.location_id === undefined || values.scope !== 'direct' || stock.location_id === values.location_id)))
}

export async function createUnit(itemID: string, body: components['schemas']['UnitCreate'], context: UnitCreateContext): Promise<Unit> {
  const owner = input(pathIDSchema, itemID)
  const values = input(unitCreateSchema, body)
  const headers = revisionHeaders(context)
  const schema = unitSchema.refine((unit) => unit.item_id === owner && unit.location_id === values.location_id &&
    unit.serial_number === (values.serial_number ?? null) && unit.notes === (values.notes ?? '') && unit.status === (values.status ?? 'stored'))
  return resourceRequest(schema, context.signal,
    (signal) => apiClient.POST('/items/{id}/units', { signal, body: values, params: { path: { id: owner }, header: headers } }), { creating: true })
}

export async function patchUnit(id: string, body: UnitPatchInput, context: UnitWriteContext): Promise<Unit> {
  const resourceID = input(pathIDSchema, id)
  const owner = input(idSchema, context.itemID)
  const values = input(unitPatchSchema, body)
  const headers = revisionHeaders(context)
  const schema = unitSchema.refine((unit) => unit.item_id === owner &&
    (values.serial_number === undefined || unit.serial_number === values.serial_number) && (values.notes === undefined || unit.notes === values.notes))
  return resourceRequest(schema, context.signal,
    (signal) => apiClient.PATCH('/units/{id}', { signal, body: values, params: { path: { id: resourceID }, header: headers } }),
    { id: resourceID, expectedRevision: context.expectedRevision })
}

export async function moveUnit(id: string, body: components['schemas']['UnitMove'], context: UnitWriteContext): Promise<Unit> {
  const resourceID = input(pathIDSchema, id)
  const owner = input(idSchema, context.itemID)
  const values = input(unitMoveSchema, body)
  const headers = revisionHeaders(context)
  return resourceRequest(unitSchema.refine((unit) => unit.item_id === owner && unit.location_id === values.target_location_id), context.signal,
    (signal) => apiClient.POST('/units/{id}/move', { signal, body: values, params: { path: { id: resourceID }, header: headers } }),
    { id: resourceID, expectedRevision: context.expectedRevision })
}

export async function setUnitStatus(id: string, body: components['schemas']['UnitStatusInput'], context: UnitWriteContext): Promise<Unit> {
  const resourceID = input(pathIDSchema, id)
  const owner = input(idSchema, context.itemID)
  const values = input(unitStatusSchema, body)
  const headers = revisionHeaders(context)
  return resourceRequest(unitSchema.refine((unit) => unit.item_id === owner && unit.status === values.status), context.signal,
    (signal) => apiClient.POST('/units/{id}/status', { signal, body: values, params: { path: { id: resourceID }, header: headers } }),
    { id: resourceID, expectedRevision: context.expectedRevision })
}

export async function deleteUnit(id: string, context: UnitWriteContext): Promise<components['schemas']['ActionAck']> {
  const resourceID = input(pathIDSchema, id)
  input(idSchema, context.itemID)
  const expectedRevision = context.expectedRevision
  const headers = revisionHeaders(context)
  return requestData(context.signal, (signal) => apiClient.DELETE('/units/{id}', { signal, params: { path: { id: resourceID }, header: headers } }), deleteAckSchema, 30_000,
    (ack, response) => !response.headers.has('ETag') && ack.revision === expectedRevision + 1)
}

function reviewedStock(context: StockWriteContext, itemID: string) {
  const item = input(itemSchema, context.item)
  input(z.undefined(), context.expectedRevision)
  input(z.literal('quantity'), item.tracking_mode)
  input(z.literal(item.id), itemID)
  return { item, headers: revisionHeaders({ ...context, expectedRevision: item.revision }) }
}

function stockReply(item: Item, locationID: string, minimum = 0, maximum = Number.MAX_SAFE_INTEGER) {
  return stockSchema.refine((stock) => stock.item_id === item.id && stock.location_id === locationID &&
    stock.unit_code === item.unit_code && stock.base_unit === item.base_unit && stock.scale_factor === item.scale_factor &&
    stock.quantity_base >= minimum && stock.quantity_base <= maximum)
}

// Stock counters are independent from the reviewed Item proof. Never use the
// ordinary row-proof-plus-one invariant or fetch/rebase a proof before sending.
export async function receiveStock(body: components['schemas']['StockReceiveInput'], context: StockWriteContext): Promise<Stock> {
  const values = input(stockReceiveSchema, body)
  const { item, headers } = reviewedStock(context, values.item_id)
  return requestData(context.signal,
    (signal) => apiClient.POST('/stock/receive', { signal, body: values, params: { header: headers } }), stockReply(item, values.target_location_id, values.quantity_base), 30_000,
    (stock, response) => response.headers.get('ETag') === `"${stock.revision}"`)
}

export async function consumeStock(body: components['schemas']['StockConsumeInput'], context: StockWriteContext): Promise<Stock> {
  const values = input(stockConsumeSchema, body)
  const { item, headers } = reviewedStock(context, values.item_id)
  return requestData(context.signal,
    (signal) => apiClient.POST('/stock/consume', { signal, body: values, params: { header: headers } }), stockReply(item, values.source_location_id, 0, Number.MAX_SAFE_INTEGER - values.quantity_base), 30_000,
    (stock, response) => response.headers.get('ETag') === `"${stock.revision}"`)
}

export async function transferStock(body: components['schemas']['StockTransferInput'], context: StockWriteContext): Promise<Stock> {
  const values = input(stockTransferSchema, body)
  const { item, headers } = reviewedStock(context, values.item_id)
  return requestData(context.signal,
    (signal) => apiClient.POST('/stock/transfer', { signal, body: values, params: { header: headers } }), stockReply(item, values.target_location_id, values.quantity_base), 30_000,
    (stock, response) => response.headers.get('ETag') === `"${stock.revision}"`)
}

export async function adjustStock(body: components['schemas']['StockAdjustInput'], context: StockWriteContext): Promise<Stock> {
  const values = input(stockAdjustSchema, body)
  const { item, headers } = reviewedStock(context, values.item_id)
  return requestData(context.signal,
    (signal) => apiClient.POST('/stock/adjust', { signal, body: values, params: { header: headers } }), stockReply(item, values.target_location_id, values.quantity_base, values.quantity_base), 30_000,
    (stock, response) => response.headers.get('ETag') === `"${stock.revision}"`)
}
