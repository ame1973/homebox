import { z } from 'zod'
import { apiClient, requestData, type RequestSender } from './client'
import { itemSchema, type Item, type ItemWriteContext } from './catalog'
import { tagSchema } from './taxonomy'
import { cursorSchema, deleteAckSchema, idSchema, input, pageQuerySchema, pathIDSchema, resourceRequest, revisionHeaders, revisionSchema, text,
  type RevisionWriteContext } from './domain'
import type { components, operations } from './schema'

export type Barcode = components['schemas']['Barcode']
export type Alias = components['schemas']['Alias']
export type Attribute = components['schemas']['Attribute']
export type BarcodesPage = components['schemas']['BarcodeList']
export type AliasesPage = components['schemas']['AliasList']
export type AttributesPage = components['schemas']['AttributeList']
export type ItemTagsPage = components['schemas']['ItemTagList']
export type OwnedPageQuery = NonNullable<operations['get_items_id_barcodes']['parameters']['query']>
// Creation/tag replacement use the original Item proof. Row edits/deletions keep
// their independent proof and original owner, never an invented second header.
export type OwnedCreateContext = ItemWriteContext
export type OwnedRowWriteContext = RevisionWriteContext & Readonly<{ itemID: string }>
export type AttributePatchInput = components['schemas']['AttributePatch'] & (
  Readonly<{ attribute_key: string; attribute_value?: string }> | Readonly<{ attribute_key?: string; attribute_value: string }>
)

const symbologySchema = z.enum(['EAN_13', 'EAN_8', 'UPC_A', 'UPC_E', 'CODE_128', 'QR_CODE', 'OTHER'])
const barcodeFields = { symbology: symbologySchema, raw_value: text(1, 512) }
const aliasFields = { alias: text(1, 300) }
const attributeFields = { attribute_key: text(1, 100), attribute_value: text(0, 10000) }
const identityFields = { id: idSchema, item_id: idSchema, revision: revisionSchema }
const barcodeSchema: z.ZodType<Barcode> = z.strictObject({ ...identityFields, ...barcodeFields, normalized_value: text(1, 512) })
const aliasSchema: z.ZodType<Alias> = z.strictObject({ ...identityFields, ...aliasFields })
const attributeSchema: z.ZodType<Attribute> = z.strictObject({ ...identityFields, ...attributeFields })
const barcodeShapes: Partial<Record<Barcode['symbology'], RegExp>> = {
  EAN_13: /^[0-9]{13}(?![\s\S])/, EAN_8: /^[0-9]{8}(?![\s\S])/, UPC_A: /^[0-9]{12}(?![\s\S])/, UPC_E: /^[01][0-9]{7}(?![\s\S])/,
}
// Match the executable request shape without repairing text. The authoritative
// Go service still validates actual GTIN arithmetic; retained replies may predate
// that policy and must not be reinterpreted by this new-input schema.
export const barcodeWriteSchema: z.ZodType<components['schemas']['BarcodeWrite']> = z.strictObject(barcodeFields)
  .refine((value) => value.symbology === 'CODE_128' ? [...value.raw_value].every((character) => character.charCodeAt(0) <= 0x7f) : barcodeShapes[value.symbology]?.test(value.raw_value) ?? true)
export const aliasWriteSchema: z.ZodType<components['schemas']['AliasWrite']> = z.strictObject(aliasFields)
export const attributeWriteSchema: z.ZodType<components['schemas']['AttributeWrite']> = z.strictObject(attributeFields)
const partialAttribute = z.strictObject(attributeFields).partial()
export const attributePatchSchema: z.ZodType<AttributePatchInput> = z.union([
  partialAttribute.required({ attribute_key: true }), partialAttribute.required({ attribute_value: true }),
])
export const itemTagsSchema: z.ZodType<components['schemas']['ItemTagsInput']> = z.strictObject({
  tag_ids: z.array(idSchema).max(100).refine((ids) => new Set(ids).size === ids.length),
})

function ownedPage<T extends { id: string }>(row: z.ZodType<T>, query: OwnedPageQuery, signal: AbortSignal, send: RequestSender): Promise<{ data: T[]; next_cursor: string | null }> {
  const schema = z.strictObject({ data: z.array(row).max(100), next_cursor: cursorSchema().nullable() })
    .refine((page) => new Set(page.data.map((value) => value.id)).size === page.data.length)
  const limit = query.limit ?? 50
  return requestData(signal, send, schema, 8_000, (page, response) =>
    !response.headers.has('ETag') && page.data.length <= limit &&
    (page.next_cursor === null || page.data.length === limit && page.next_cursor !== query.cursor))
}

export async function fetchItemBarcodesPage(itemID: string, query: OwnedPageQuery, signal: AbortSignal): Promise<BarcodesPage> {
  const owner = input(pathIDSchema, itemID)
  const values = input(pageQuerySchema, query)
  return ownedPage(barcodeSchema.refine((row) => row.item_id === owner), values, signal,
    (signal) => apiClient.GET('/items/{id}/barcodes', { signal, params: { path: { id: owner }, query: values } }))
}

export async function fetchItemAliasesPage(itemID: string, query: OwnedPageQuery, signal: AbortSignal): Promise<AliasesPage> {
  const owner = input(pathIDSchema, itemID)
  const values = input(pageQuerySchema, query)
  return ownedPage(aliasSchema.refine((row) => row.item_id === owner), values, signal,
    (signal) => apiClient.GET('/items/{id}/aliases', { signal, params: { path: { id: owner }, query: values } }))
}

export async function fetchItemAttributesPage(itemID: string, query: OwnedPageQuery, signal: AbortSignal): Promise<AttributesPage> {
  const owner = input(pathIDSchema, itemID)
  const values = input(pageQuerySchema, query)
  return ownedPage(attributeSchema.refine((row) => row.item_id === owner), values, signal,
    (signal) => apiClient.GET('/items/{id}/attributes', { signal, params: { path: { id: owner }, query: values } }))
}

export async function fetchItemTagsPage(itemID: string, query: OwnedPageQuery, signal: AbortSignal): Promise<ItemTagsPage> {
  const owner = input(pathIDSchema, itemID)
  const values = input(pageQuerySchema, query)
  return ownedPage(tagSchema, values, signal,
    (signal) => apiClient.GET('/items/{id}/tags', { signal, params: { path: { id: owner }, query: values } }))
}

export async function createBarcode(itemID: string, body: components['schemas']['BarcodeWrite'], context: OwnedCreateContext): Promise<Barcode> {
  const owner = input(pathIDSchema, itemID)
  const values = input(barcodeWriteSchema, body)
  const headers = revisionHeaders(context)
  return resourceRequest(barcodeSchema.refine((row) => row.item_id === owner), context.signal,
    (signal) => apiClient.POST('/items/{id}/barcodes', { signal, body: values, params: { path: { id: owner }, header: headers } }), { creating: true })
}

export async function patchBarcode(id: string, body: components['schemas']['BarcodeWrite'], context: OwnedRowWriteContext): Promise<Barcode> {
  const resourceID = input(pathIDSchema, id)
  const owner = input(pathIDSchema, context.itemID)
  const values = input(barcodeWriteSchema, body)
  const headers = revisionHeaders(context)
  return resourceRequest(barcodeSchema.refine((row) => row.item_id === owner), context.signal,
    (signal) => apiClient.PATCH('/barcodes/{id}', { signal, body: values, params: { path: { id: resourceID }, header: headers } }), { id: resourceID, expectedRevision: context.expectedRevision })
}

export async function createAlias(itemID: string, body: components['schemas']['AliasWrite'], context: OwnedCreateContext): Promise<Alias> {
  const owner = input(pathIDSchema, itemID)
  const values = input(aliasWriteSchema, body)
  const headers = revisionHeaders(context)
  return resourceRequest(aliasSchema.refine((row) => row.item_id === owner), context.signal,
    (signal) => apiClient.POST('/items/{id}/aliases', { signal, body: values, params: { path: { id: owner }, header: headers } }), { creating: true })
}

export async function patchAlias(id: string, body: components['schemas']['AliasWrite'], context: OwnedRowWriteContext): Promise<Alias> {
  const resourceID = input(pathIDSchema, id)
  const owner = input(pathIDSchema, context.itemID)
  const values = input(aliasWriteSchema, body)
  const headers = revisionHeaders(context)
  return resourceRequest(aliasSchema.refine((row) => row.item_id === owner), context.signal,
    (signal) => apiClient.PATCH('/aliases/{id}', { signal, body: values, params: { path: { id: resourceID }, header: headers } }), { id: resourceID, expectedRevision: context.expectedRevision })
}

export async function createAttribute(itemID: string, body: components['schemas']['AttributeWrite'], context: OwnedCreateContext): Promise<Attribute> {
  const owner = input(pathIDSchema, itemID)
  const values = input(attributeWriteSchema, body)
  const headers = revisionHeaders(context)
  return resourceRequest(attributeSchema.refine((row) => row.item_id === owner), context.signal,
    (signal) => apiClient.POST('/items/{id}/attributes', { signal, body: values, params: { path: { id: owner }, header: headers } }), { creating: true })
}

export async function patchAttribute(id: string, body: AttributePatchInput, context: OwnedRowWriteContext): Promise<Attribute> {
  const resourceID = input(pathIDSchema, id)
  const owner = input(pathIDSchema, context.itemID)
  const values = input(attributePatchSchema, body)
  const headers = revisionHeaders(context)
  return resourceRequest(attributeSchema.refine((row) => row.item_id === owner), context.signal,
    (signal) => apiClient.PATCH('/attributes/{id}', { signal, body: values, params: { path: { id: resourceID }, header: headers } }), { id: resourceID, expectedRevision: context.expectedRevision })
}

export async function replaceItemTags(itemID: string, body: components['schemas']['ItemTagsInput'], context: ItemWriteContext): Promise<Item> {
  const owner = input(pathIDSchema, itemID)
  const values = input(itemTagsSchema, body)
  const headers = revisionHeaders(context)
  return resourceRequest(itemSchema, context.signal,
    (signal) => apiClient.PUT('/items/{id}/tags', { signal, body: values, params: { path: { id: owner }, header: headers } }), { id: owner, expectedRevision: context.expectedRevision })
}

async function deleteOwned(id: string, context: OwnedRowWriteContext, send: (resourceID: string, headers: ReturnType<typeof revisionHeaders>, signal: AbortSignal) => ReturnType<RequestSender>): Promise<components['schemas']['ActionAck']> {
  const resourceID = input(pathIDSchema, id)
  input(pathIDSchema, context.itemID)
  const expectedRevision = context.expectedRevision
  const headers = revisionHeaders(context)
  return requestData(context.signal, (signal) => send(resourceID, headers, signal), deleteAckSchema, 30_000,
    (ack, response) => !response.headers.has('ETag') && ack.revision === expectedRevision + 1)
}

export async function deleteBarcode(id: string, context: OwnedRowWriteContext): Promise<components['schemas']['ActionAck']> {
  return deleteOwned(id, context, (id, headers, signal) => apiClient.DELETE('/barcodes/{id}', { signal, params: { path: { id }, header: headers } }))
}

export async function deleteAlias(id: string, context: OwnedRowWriteContext): Promise<components['schemas']['ActionAck']> {
  return deleteOwned(id, context, (id, headers, signal) => apiClient.DELETE('/aliases/{id}', { signal, params: { path: { id }, header: headers } }))
}

export async function deleteAttribute(id: string, context: OwnedRowWriteContext): Promise<components['schemas']['ActionAck']> {
  return deleteOwned(id, context, (id, headers, signal) => apiClient.DELETE('/attributes/{id}', { signal, params: { path: { id }, header: headers } }))
}
