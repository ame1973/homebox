import { z } from 'zod'
import { apiClient, requestData } from './client'
import { itemMeasurementFields } from './catalog'
import { stockSchema, unitSchema } from './holdings'
import { createHeaders, cursorSchema, deleteAckSchema, idSchema, input, pageQuerySchema, parseSafeCount, pathIDSchema, resourceRequest, revisionHeaders, revisionSchema, text,
  type CreateContext, type RevisionWriteContext, type RootWriteContext } from './domain'
import type { components, operations } from './schema'

export type Location = components['schemas']['Location']
export type LocationContents = components['schemas']['LocationContents']
export type LocationContentsRead = Readonly<{ value: LocationContents; revision: number }>
export type LocationContentsQuery = NonNullable<operations['get_locations_id_contents']['parameters']['query']>
export type LocationSummary = components['schemas']['LocationSummary']
export type LocationSummaryRead = Readonly<{ value: LocationSummary; revision: number }>
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
const contentsQuerySchema: z.ZodType<LocationContentsQuery> = z.strictObject({
  ...pageQuerySchema.shape, cursor: cursorSchema(4096).optional(), scope: z.enum(['direct', 'subtree']).optional(),
})

function onlyRawKeys(value: unknown, allowed: readonly string[]): value is Record<string, unknown> {
  return !!value && typeof value === 'object' && !Array.isArray(value) && Object.keys(value).every((key) => allowed.includes(key))
}
function rawContentsKeys(value: unknown): boolean {
  if (!onlyRawKeys(value, ['location_id', 'scope', 'units', 'stock', 'sub_locations', 'next_cursor'])) return false
  const groups = [
    ['units', ['id', 'item_id', 'location_id', 'asset_code', 'status', 'serial_number', 'notes', 'revision']],
    ['stock', ['item_id', 'location_id', 'quantity_base', 'unit_code', 'base_unit', 'scale_factor', 'revision']],
    ['sub_locations', ['id', 'parent_id', 'name', 'code', 'kind', 'status', 'sort_order', 'description', 'revision']],
  ] as const
  return groups.every(([key, allowed]) => {
    const rows = value[key]
    return Array.isArray(rows) && rows.every((row: unknown) => onlyRawKeys(row, allowed))
  })
}
const contentsSchema: z.ZodType<LocationContents> = z.unknown().refine(rawContentsKeys).pipe(z.strictObject({
  location_id: idSchema, scope: z.enum(['direct', 'subtree']), units: z.array(unitSchema).max(100), stock: z.array(stockSchema).max(100),
  sub_locations: z.array(locationSchema).max(100), next_cursor: cursorSchema(4096).nullable(),
}).refine((page) => page.units.every((unit) => unit.location_id !== null) && page.sub_locations.every((location) => location.id !== page.location_id) &&
  new Set(page.units.map((unit) => unit.id)).size === page.units.length &&
  new Set(page.stock.map((stock) => JSON.stringify([stock.item_id, stock.location_id]))).size === page.stock.length &&
  new Set(page.sub_locations.map((location) => location.id)).size === page.sub_locations.length))
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
const pageSchema: z.ZodType<LocationsPage> = z.strictObject({ data: z.array(locationSchema).max(100), next_cursor: cursorSchema().nullable().optional() })
  .refine((page) => new Set(page.data.map((location) => location.id)).size === page.data.length)

// Check the unparsed keys too: Zod can drop an own __proto__ key before strict
// object validation. No arbitrary server field belongs in this derived read.
function rawSummaryKeys(value: unknown): boolean {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false
  if (Object.keys(value).some((key) => !['location_id', 'direct_units', 'subtree_units', 'distinct_item_count', 'direct_quantities', 'subtree_quantities'].includes(key))) return false
  const record = value as Record<string, unknown>
  for (const key of ['direct_quantities', 'subtree_quantities'] as const) {
    const groups = record[key]
    if (!Array.isArray(groups)) return false
    if (groups.some((group: unknown) => !group || typeof group !== 'object' || Array.isArray(group) ||
      Object.keys(group).some((field) => field !== 'base_unit' && field !== 'quantity_base'))) return false
  }
  return true
}
const quantityGroupSchema = z.strictObject({ base_unit: itemMeasurementFields.base_unit, quantity_base: z.int().min(1) })
const summarySchema: z.ZodType<LocationSummary> = z.unknown().refine(rawSummaryKeys).pipe(z.strictObject({
  location_id: idSchema, direct_units: z.int().min(0), subtree_units: z.int().min(0), distinct_item_count: z.int().min(0),
  direct_quantities: z.array(quantityGroupSchema).max(500), subtree_quantities: z.array(quantityGroupSchema).max(500),
}).refine((value) => {
  if (value.direct_units > value.subtree_units) return false
  const ordered = (groups: readonly { base_unit: string }[]) => groups.every((group, index) => index === 0 || groups[index - 1]!.base_unit < group.base_unit)
  if (!ordered(value.direct_quantities) || !ordered(value.subtree_quantities)) return false
  const subtree = new Map(value.subtree_quantities.map((group) => [group.base_unit, group.quantity_base]))
  if (value.direct_quantities.some((group) => group.quantity_base > (subtree.get(group.base_unit) ?? 0))) return false
  const present = value.subtree_units > 0 || value.subtree_quantities.length > 0
  if (present !== (value.distinct_item_count > 0)) return false
  const maximumItems = value.subtree_quantities.reduce((total, group) => total + BigInt(group.quantity_base), BigInt(value.subtree_units))
  return BigInt(value.distinct_item_count) <= maximumItems
}))

export async function fetchLocation(id: string, signal: AbortSignal): Promise<Location> {
  const resourceID = input(locationPathIDSchema, id)
  return resourceRequest(locationSchema, signal, (signal) => apiClient.GET('/locations/{id}', { signal, params: { path: { id: resourceID } } }), { id: resourceID }, 8_000)
}

function locationReadRevision(response: Response): number {
  const etag = response.headers.get('ETag')
  return etag?.startsWith('"') && etag.endsWith('"') ? parseSafeCount(etag.slice(1, -1), 1) ?? 0 : 0
}

// The aggregate revision identifies the live Location read, not an individual
// Unit/Stock proof or a frozen snapshot. The caller owns refresh and comparison.
export async function fetchLocationSummary(id: string, signal: AbortSignal): Promise<LocationSummaryRead> {
  const resourceID = input(locationPathIDSchema, id)
  let revision = 0
  const value = await requestData(signal, (signal) => apiClient.GET('/locations/{id}/summary', { signal, params: { path: { id: resourceID } } }), summarySchema, 8_000,
    (result, response) => {
      revision = locationReadRevision(response)
      return result.location_id === resourceID && revision > 0
    })
  return { value, revision }
}

// One bounded live page only. A Location aggregate revision and cursor never
// prove physical presence, authorize a write or establish a frozen collection.
export async function fetchLocationContents(id: string, query: LocationContentsQuery, signal: AbortSignal): Promise<LocationContentsRead> {
  const resourceID = input(locationPathIDSchema, id)
  const values = input(contentsQuerySchema, query)
  const scope = values.scope ?? 'subtree', limit = values.limit ?? 50
  let revision = 0
  const value = await requestData(signal, (signal) => apiClient.GET('/locations/{id}/contents', { signal, params: { path: { id: resourceID }, query: values } }), contentsSchema, 8_000,
    (result, response) => {
      revision = locationReadRevision(response)
      const count = result.units.length + result.stock.length + result.sub_locations.length
      return revision > 0 && result.location_id === resourceID && result.scope === scope && count <= limit &&
        (result.next_cursor === null || count === limit && result.next_cursor !== values.cursor) &&
        (scope !== 'direct' || result.units.every((unit) => unit.location_id === resourceID) &&
          result.stock.every((stock) => stock.location_id === resourceID) && result.sub_locations.every((location) => location.parent_id === resourceID))
    })
  return { value, revision }
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
