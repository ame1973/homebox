import { z } from 'zod'
import { apiClient, requestData } from './client'
import { createHeaders, cursorSchema, deleteAckSchema, idSchema, input, pageQuerySchema, pathIDSchema, resourceRequest, revisionHeaders, revisionSchema, text,
  type RevisionWriteContext, type RootWriteContext } from './domain'
import type { components, operations } from './schema'

export type Item = components['schemas']['Item']
export type ItemsPage = components['schemas']['ItemList']
export type ItemPageQuery = NonNullable<operations['get_items']['parameters']['query']>
export type ItemWriteContext = RevisionWriteContext
export type ItemCreateInput = Omit<components['schemas']['ItemCreate'], 'unit_code' | 'base_unit' | 'scale_factor'> & (
  Readonly<{ unit_code?: 'pcs'; base_unit?: 'pcs'; scale_factor?: 1 }> |
  Readonly<{ unit_code: string; base_unit: string; scale_factor: number }>
)

export const itemMeasurementFields = {
  unit_code: text(1, 32), base_unit: text(1, 32).regex(/^[A-Za-z][A-Za-z0-9_/-]{0,31}(?![\s\S])/),
  scale_factor: revisionSchema,
}

const fields = {
  name: text(1, 300), brand: text(0, 300).nullable(), model: text(0, 300).nullable(), category_id: idSchema.nullable(),
  description: text(0, 10000), notes: text(0, 10000), tracking_mode: z.enum(['individual', 'quantity']),
  ...itemMeasurementFields, status: z.enum(['active', 'archived']),
}
export const itemSchema: z.ZodType<Item> = z.strictObject({ id: idSchema, revision: revisionSchema, ...fields })
const optionalFields = z.strictObject(fields).partial()

// Preserve optional server defaults; a custom tuple must be explicit, never
// inferred from a display label. Generated types do not enforce JSON if/then.
export const itemCreateSchema: z.ZodType<components['schemas']['ItemCreate']> = optionalFields.required({ name: true }).refine((value) =>
  (value.unit_code ?? 'pcs') === 'pcs' && (value.base_unit ?? 'pcs') === 'pcs' && (value.scale_factor ?? 1) === 1 ||
  value.unit_code !== undefined && value.base_unit !== undefined && value.scale_factor !== undefined)
export const itemPatchSchema: z.ZodType<components['schemas']['ItemWrite']> = optionalFields
  .refine((patch) => Object.values(patch).some((value) => value !== undefined))
export const itemArchiveSchema: z.ZodType<components['schemas']['ItemArchiveInput']> = z.strictObject({ reason: fields.notes.optional() })
const pageSchema: z.ZodType<ItemsPage> = z.strictObject({ data: z.array(itemSchema).max(100), next_cursor: cursorSchema().nullable() })
  .refine((page) => new Set(page.data.map((item) => item.id)).size === page.data.length)
export const civilDateSchema = z.iso.date().refine((value) => !value.startsWith('0000-'))
export const itemPageQuerySchema: z.ZodType<ItemPageQuery> = z.strictObject({
  ...pageQuerySchema.shape, from: civilDateSchema.optional(), to: civilDateSchema.optional(),
  currency: z.string().regex(/^[A-Z]{3}(?![\s\S])/).optional(), category_id: idSchema.optional(), location_id: idSchema.optional(),
  scope: z.enum(['direct', 'subtree']).optional(), status: fields.status.optional(), merchant: text(1, 300).optional(), q: text(0, 500).optional(),
}).refine((query) => query.from === undefined || query.to === undefined || query.from <= query.to)

export async function fetchItem(id: string, signal: AbortSignal): Promise<Item> {
  const resourceID = input(pathIDSchema, id)
  return resourceRequest(itemSchema, signal, (signal) => apiClient.GET('/items/{id}', { signal, params: { path: { id: resourceID } } }), { id: resourceID }, 8_000)
}

// One explicit live page, never hidden pagination, persistence or a snapshot
// claim. q/filter text remains caller-owned; the server binds its effective scope.
export async function fetchItemsPage(query: ItemPageQuery, signal: AbortSignal): Promise<ItemsPage> {
  const values = input(itemPageQuerySchema, query)
  const limit = values.limit ?? 50
  return requestData(signal, (signal) => apiClient.GET('/items', { signal, params: { query: values } }), pageSchema, 8_000,
    (page) => page.data.length <= limit && (page.next_cursor === null || page.data.length === limit && page.next_cursor !== values.cursor) &&
      page.data.every((item) => (values.status === undefined || item.status === values.status) &&
        (values.category_id === undefined || values.scope !== 'direct' || item.category_id === values.category_id)))
}

export async function createItem(body: ItemCreateInput, context: RootWriteContext): Promise<Item> {
  const values = input(itemCreateSchema, body)
  const headers = createHeaders(undefined, context)
  return resourceRequest(itemSchema, context.signal, (signal) => apiClient.POST('/items', { signal, body: values, params: { header: headers } }), { creating: true })
}

export async function patchItem(id: string, body: components['schemas']['ItemWrite'], context: ItemWriteContext): Promise<Item> {
  const resourceID = input(pathIDSchema, id)
  const values = input(itemPatchSchema, body)
  const headers = revisionHeaders(context)
  return resourceRequest(itemSchema, context.signal, (signal) => apiClient.PATCH('/items/{id}', { signal, body: values, params: { path: { id: resourceID }, header: headers } }),
    { id: resourceID, expectedRevision: context.expectedRevision })
}

export async function archiveItem(id: string, body: components['schemas']['ItemArchiveInput'], context: ItemWriteContext): Promise<Item> {
  const resourceID = input(pathIDSchema, id)
  const values = input(itemArchiveSchema, body)
  const headers = revisionHeaders(context)
  return resourceRequest(itemSchema, context.signal, (signal) => apiClient.POST('/items/{id}/archive', { signal, body: values, params: { path: { id: resourceID }, header: headers } }),
    { id: resourceID, expectedRevision: context.expectedRevision })
}

export async function deleteItem(id: string, context: ItemWriteContext): Promise<components['schemas']['ActionAck']> {
  const resourceID = input(pathIDSchema, id)
  const expectedRevision = context.expectedRevision
  const headers = revisionHeaders(context)
  return requestData(context.signal, (signal) => apiClient.DELETE('/items/{id}', { signal, params: { path: { id: resourceID }, header: headers } }), deleteAckSchema, 30_000,
    (ack) => ack.revision === expectedRevision + 1)
}
