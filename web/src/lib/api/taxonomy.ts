import { z } from 'zod'
import { apiClient, requestData } from './client'
import { createHeaders, cursorSchema, deleteAckSchema, idSchema, input, pageQuerySchema, pathIDSchema, resourceRequest, revisionHeaders, revisionSchema, text,
  type CreateContext, type RevisionWriteContext, type RootWriteContext } from './domain'
import type { components, operations } from './schema'

export type Category = components['schemas']['Category']
export type Tag = components['schemas']['Tag']
export type CategoriesPage = components['schemas']['CategoryList']
export type TagsPage = components['schemas']['TagList']
export type CategoryPageQuery = NonNullable<operations['get_categories']['parameters']['query']>
export type TagPageQuery = NonNullable<operations['get_tags']['parameters']['query']>
export type TaxonomyWriteContext = RevisionWriteContext
export type RootCategoryInput = Omit<components['schemas']['CategoryCreate'], 'parent_id'> & { readonly parent_id?: null }
export type ChildCategoryInput = Omit<components['schemas']['CategoryCreate'], 'parent_id'> & { readonly parent_id: string }

const nameSchema = text(1, 200)
const parentSchema = idSchema.nullable()
const categorySchema: z.ZodType<Category> = z.strictObject({ id: idSchema, revision: revisionSchema, name: nameSchema, parent_id: parentSchema })
  .refine((category) => category.parent_id !== category.id)
export const tagSchema: z.ZodType<Tag> = z.strictObject({ id: idSchema, revision: revisionSchema, name: nameSchema })
export const categoryCreateSchema: z.ZodType<components['schemas']['CategoryCreate']> = z.strictObject({ name: nameSchema, parent_id: parentSchema.optional() })
export const categoryPatchSchema: z.ZodType<components['schemas']['CategoryWrite']> = z.strictObject({ name: nameSchema.optional(), parent_id: parentSchema.optional() })
  .refine((patch) => Object.values(patch).some((value) => value !== undefined))
export const tagWriteSchema: z.ZodType<components['schemas']['TagWrite']> = z.strictObject({ name: nameSchema })
const categoriesPageSchema: z.ZodType<CategoriesPage> = z.strictObject({ data: z.array(categorySchema).max(100), next_cursor: cursorSchema().nullable().optional() })
  .refine((page) => new Set(page.data.map((category) => category.id)).size === page.data.length)
const tagsPageSchema: z.ZodType<TagsPage> = z.strictObject({ data: z.array(tagSchema).max(100), next_cursor: cursorSchema().nullable().optional() })
  .refine((page) => new Set(page.data.map((tag) => tag.id)).size === page.data.length)

// Live metadata is explicitly paged by the caller, never silently read as a
// frozen snapshot or followed by a background write/retry/token refresh.
export async function fetchCategoriesPage(query: CategoryPageQuery, signal: AbortSignal): Promise<CategoriesPage> {
  const values = input(pageQuerySchema, query)
  const limit = values.limit ?? 100
  return requestData(signal, (signal) => apiClient.GET('/categories', { signal, params: { query: values } }), categoriesPageSchema, 8_000,
    (page) => page.data.length <= limit && (page.next_cursor == null || page.data.length === limit))
}

export async function fetchTagsPage(query: TagPageQuery, signal: AbortSignal): Promise<TagsPage> {
  const values = input(pageQuerySchema, query)
  const limit = values.limit ?? 100
  return requestData(signal, (signal) => apiClient.GET('/tags', { signal, params: { query: values } }), tagsPageSchema, 8_000,
    (page) => page.data.length <= limit && (page.next_cursor == null || page.data.length === limit))
}

export function createCategory(body: RootCategoryInput, context: RootWriteContext): Promise<Category>
export function createCategory(body: ChildCategoryInput, context: TaxonomyWriteContext): Promise<Category>
export async function createCategory(body: components['schemas']['CategoryCreate'], context: CreateContext): Promise<Category> {
  const values = input(categoryCreateSchema, body)
  const headers = createHeaders(values.parent_id, context)
  return resourceRequest(categorySchema, context.signal, (signal) => apiClient.POST('/categories', { signal, body: values, params: { header: headers } }), { creating: true })
}

export async function patchCategory(id: string, body: components['schemas']['CategoryWrite'], context: TaxonomyWriteContext): Promise<Category> {
  const resourceID = input(pathIDSchema, id)
  const values = input(categoryPatchSchema, body)
  const headers = revisionHeaders(context)
  return resourceRequest(categorySchema, context.signal, (signal) => apiClient.PATCH('/categories/{id}', { signal, body: values, params: { path: { id: resourceID }, header: headers } }), { id: resourceID, expectedRevision: context.expectedRevision })
}

export async function deleteCategory(id: string, context: TaxonomyWriteContext): Promise<components['schemas']['ActionAck']> {
  const resourceID = input(pathIDSchema, id)
  const expectedRevision = context.expectedRevision
  const headers = revisionHeaders(context)
  return requestData(context.signal, (signal) => apiClient.DELETE('/categories/{id}', { signal, params: { path: { id: resourceID }, header: headers } }), deleteAckSchema, 30_000,
    (ack) => ack.revision === expectedRevision + 1)
}

export async function createTag(body: components['schemas']['TagWrite'], context: RootWriteContext): Promise<Tag> {
  const values = input(tagWriteSchema, body)
  const headers = createHeaders(undefined, context)
  return resourceRequest(tagSchema, context.signal, (signal) => apiClient.POST('/tags', { signal, body: values, params: { header: headers } }), { creating: true })
}

export async function patchTag(id: string, body: components['schemas']['TagWrite'], context: TaxonomyWriteContext): Promise<Tag> {
  const resourceID = input(pathIDSchema, id)
  const values = input(tagWriteSchema, body)
  const headers = revisionHeaders(context)
  return resourceRequest(tagSchema, context.signal, (signal) => apiClient.PATCH('/tags/{id}', { signal, body: values, params: { path: { id: resourceID }, header: headers } }), { id: resourceID, expectedRevision: context.expectedRevision })
}

export async function deleteTag(id: string, context: TaxonomyWriteContext): Promise<components['schemas']['ActionAck']> {
  const resourceID = input(pathIDSchema, id)
  const expectedRevision = context.expectedRevision
  const headers = revisionHeaders(context)
  return requestData(context.signal, (signal) => apiClient.DELETE('/tags/{id}', { signal, params: { path: { id: resourceID }, header: headers } }), deleteAckSchema, 30_000,
    (ack) => ack.revision === expectedRevision + 1)
}
