import { z } from 'zod'
import { apiClient, requestData } from './client'
import { createHeaders, deleteAckSchema, idSchema, input, pageQuerySchema, pathIDSchema, resourceRequest, revisionHeaders, revisionSchema, text,
  type CreateContext, type RevisionWriteContext, type RootWriteContext } from './domain'
import type { components, operations } from './schema'

export type Location = components['schemas']['Location']
export type LocationsPage = components['schemas']['LocationList']
export type LocationPageQuery = NonNullable<operations['get_locations_tree']['parameters']['query']>
export type LocationWriteContext = RevisionWriteContext
export type RootLocationInput = Omit<components['schemas']['LocationCreate'], 'parent_id'> & { readonly parent_id?: null }
export type ChildLocationInput = Omit<components['schemas']['LocationCreate'], 'parent_id'> & { readonly parent_id: string }

const locationPathIDSchema = pathIDSchema.refine((id) => id !== 'tree')
const fields = {
  parent_id: idSchema.nullable(), name: text(1, 200), code: text(1, 100), kind: text(1, 32),
  status: z.enum(['active', 'archived']), sort_order: z.int(), description: text(0, 10000),
}
export const locationSchema: z.ZodType<Location> = z.strictObject({ id: idSchema, revision: revisionSchema, ...fields })
  .refine((location) => location.parent_id !== location.id)
export const locationCreateSchema: z.ZodType<components['schemas']['LocationCreate']> = z.strictObject({
  parent_id: fields.parent_id.optional(), name: fields.name, code: fields.code, kind: fields.kind.optional(),
  status: fields.status.optional(), sort_order: fields.sort_order.optional(), description: fields.description.optional(),
})
const patchSchema: z.ZodType<components['schemas']['LocationWrite']> = z.strictObject({
  parent_id: fields.parent_id.optional(), name: fields.name.optional(), code: fields.code.optional(), kind: fields.kind.optional(),
  status: fields.status.optional(), sort_order: fields.sort_order.optional(), description: fields.description.optional(),
}).refine((patch) => Object.values(patch).some((value) => value !== undefined))
const moveSchema: z.ZodType<components['schemas']['LocationMove']> = z.strictObject({ target_parent_id: fields.parent_id })
const archiveSchema: z.ZodType<components['schemas']['LocationArchiveInput']> = z.strictObject({ archive_children: z.boolean().optional(), reason: fields.description.optional() })
const pageSchema: z.ZodType<LocationsPage> = z.strictObject({ data: z.array(locationSchema).max(100), next_cursor: text(1, 2048).nullable().optional() })
  .refine((page) => new Set(page.data.map((location) => location.id)).size === page.data.length)

export async function fetchLocation(id: string, signal: AbortSignal): Promise<Location> {
  const resourceID = input(locationPathIDSchema, id)
  return resourceRequest(locationSchema, signal, (signal) => apiClient.GET('/locations/{id}', { signal, params: { path: { id: resourceID } } }), { id: resourceID }, 8_000)
}

// The caller explicitly requests each page. No hidden pagination, token refresh,
// automatic retry, or cached claim of a frozen/offline tree is made here.
export async function fetchLocationsPage(query: LocationPageQuery, signal: AbortSignal): Promise<LocationsPage> {
  const values = input(pageQuerySchema, query)
  const limit = values.limit ?? 100
  return requestData(signal, (signal) => apiClient.GET('/locations/tree', { signal, params: { query: values } }), pageSchema, 8_000,
    (page) => page.data.length <= limit && (page.next_cursor == null || page.data.length === limit))
}

// Overloads keep root creation free of an unrelated revision and require a
// reviewed parent proof for children. Runtime checks also protect JS callers.
export function createLocation(body: RootLocationInput, context: RootWriteContext): Promise<Location>
export function createLocation(body: ChildLocationInput, context: LocationWriteContext): Promise<Location>
export async function createLocation(body: components['schemas']['LocationCreate'], context: CreateContext): Promise<Location> {
  const values = input(locationCreateSchema, body)
  const headers = createHeaders(values.parent_id, context)
  return resourceRequest(locationSchema, context.signal, (signal) => apiClient.POST('/locations', { signal, body: values, params: { header: headers } }), { creating: true })
}

export async function patchLocation(id: string, body: components['schemas']['LocationWrite'], context: LocationWriteContext): Promise<Location> {
  const resourceID = input(locationPathIDSchema, id)
  const values = input(patchSchema, body)
  const headers = revisionHeaders(context)
  return resourceRequest(locationSchema, context.signal, (signal) => apiClient.PATCH('/locations/{id}', { signal, body: values, params: { path: { id: resourceID }, header: headers } }), { id: resourceID, expectedRevision: context.expectedRevision })
}

export async function moveLocation(id: string, body: components['schemas']['LocationMove'], context: LocationWriteContext): Promise<Location> {
  const resourceID = input(locationPathIDSchema, id)
  const values = input(moveSchema, body)
  const headers = revisionHeaders(context)
  return resourceRequest(locationSchema, context.signal, (signal) => apiClient.POST('/locations/{id}/move', { signal, body: values, params: { path: { id: resourceID }, header: headers } }), { id: resourceID, expectedRevision: context.expectedRevision })
}

export async function archiveLocation(id: string, body: components['schemas']['LocationArchiveInput'], context: LocationWriteContext): Promise<Location> {
  const resourceID = input(locationPathIDSchema, id)
  const values = input(archiveSchema, body)
  const headers = revisionHeaders(context)
  return resourceRequest(locationSchema, context.signal, (signal) => apiClient.POST('/locations/{id}/archive', { signal, body: values, params: { path: { id: resourceID }, header: headers } }), { id: resourceID, expectedRevision: context.expectedRevision })
}

export async function deleteLocation(id: string, context: LocationWriteContext): Promise<components['schemas']['ActionAck']> {
  const resourceID = input(locationPathIDSchema, id)
  const expectedRevision = context.expectedRevision
  const headers = revisionHeaders(context)
  return requestData(context.signal, (signal) => apiClient.DELETE('/locations/{id}', { signal, params: { path: { id: resourceID }, header: headers } }), deleteAckSchema, 30_000,
    (ack) => ack.revision === expectedRevision + 1)
}
