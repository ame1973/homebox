import { createQuerySerializer } from 'openapi-fetch'
import { z } from 'zod'
import { apiClient, requestData, validText } from './client'
import { cursorSchema, idSchema, input, pageQuerySchema, pathIDSchema, resourceRequest, revisionSchema, text } from './domain'
import { ownedQuerySchema, validLivePage } from './purchases'
import type { components, operations } from './schema'

export type Media = components['schemas']['Media']
export type MediaLink = components['schemas']['MediaLink']
export type MediaLinkView = components['schemas']['MediaLinkView']
export type MediaLinkPage = components['schemas']['MediaLinkList']
export type MediaOwnerType = MediaLink['owner_type']
export type MediaPageQuery = NonNullable<operations['get_media_id_links']['parameters']['query']>
type OwnerQuery = NonNullable<operations['get_media_links']['parameters']['query']>

const digestSchema = z.string().regex(/^[a-f0-9]{64}(?![\s\S])/)
const thumbnailPath = /^\/api\/v1\/media\/((?:[A-Za-z0-9._~!$&'()*+,;=:@-]|%[0-9A-Fa-f]{2})+)\/thumbnail(?![\s\S])/
const thumbnailSchema = z.string().max(2048).regex(thumbnailPath)

function matchesThumbnail(id: string, url: string): boolean {
  const match = thumbnailPath.exec(url)
  if (!match || !pathIDSchema.safeParse(id).success) return false
  try { return decodeURIComponent(match[1] ?? '') === id } catch { return false }
}

// Retained claims stay readable without asserting that any image bytes exist.
// A thumbnail URL is meaningful only when paired, verified and scoped to this ID.
export const mediaSchema: z.ZodType<Media> = z.strictObject({
  id: idSchema, revision: revisionSchema, sha256: digestSchema, mime_type: text(1, 100), verification: z.enum(['verified', 'unverified']),
  width: revisionSchema, height: revisionSchema, bytes: revisionSchema, thumb_url: thumbnailSchema.nullable(), thumb_sha256: digestSchema.nullable(),
}).refine((media) => {
  if (media.verification === 'unverified') return media.thumb_url === null && media.thumb_sha256 === null
  if (!['image/jpeg', 'image/png', 'image/webp'].includes(media.mime_type)) return false
  return media.thumb_url === null ? media.thumb_sha256 === null : media.thumb_sha256 !== null && matchesThumbnail(media.id, media.thumb_url)
})

const ownerTypeSchema = z.enum(['item', 'unit', 'location'])
const linkFields = { id: idSchema, revision: revisionSchema, media_id: idSchema, owner_type: ownerTypeSchema, owner_id: idSchema,
  is_cover: z.boolean(), sort_order: z.int(), description: text(0, 10000) }
export const mediaLinkSchema: z.ZodType<MediaLink> = z.strictObject(linkFields)
export const mediaLinkViewSchema: z.ZodType<MediaLinkView> = z.strictObject({ ...linkFields, media: mediaSchema })
  .refine((link) => link.media_id === link.media.id)
const mediaPageSchema: z.ZodType<MediaLinkPage> = z.strictObject({ data: z.array(mediaLinkViewSchema).max(100), next_cursor: cursorSchema().nullable() })
  .refine((page) => {
    const links = new Set<string>(), media = new Map<string, string>()
    for (const link of page.data) {
      const snapshot = JSON.stringify(link.media)
      if (links.has(link.id) || media.has(link.media_id) && media.get(link.media_id) !== snapshot) return false
      links.add(link.id)
      media.set(link.media_id, snapshot)
    }
    return true
  })

const serializeOwnerQuery = createQuerySerializer<OwnerQuery>()
const ownerQuerySchema = pageQuerySchema.extend({ owner_type: ownerTypeSchema, owner_id: idSchema })
  .refine((query) => Object.values(query).every((value) => typeof value !== 'string' || validText(value, 0, value.length)) && serializeOwnerQuery(query).length <= 16_384)

export async function fetchMedia(id: string, signal: AbortSignal): Promise<Media> {
  const resourceID = input(pathIDSchema, id)
  return resourceRequest(mediaSchema, signal, (signal) => resourceID === 'links'
    ? apiClient.GET('/media/{id}/view', { signal, params: { path: { id: resourceID } } })
    : apiClient.GET('/media/{id}', { signal, params: { path: { id: resourceID } } }), { id: resourceID }, 8_000)
}

export async function fetchMediaLinksPage(mediaID: string, query: MediaPageQuery, signal: AbortSignal): Promise<MediaLinkPage> {
  const selected = input(pathIDSchema, mediaID)
  const values = input(ownedQuerySchema, query)
  return requestData(signal, (signal) => apiClient.GET('/media/{id}/links', { signal, params: { path: { id: selected }, query: values } }), mediaPageSchema, 8_000,
    (page, response) => validLivePage(page, response, values) && page.data.every((link) => link.media_id === selected))
}

export async function fetchOwnerMediaLinksPage(ownerType: MediaOwnerType, ownerID: string, query: MediaPageQuery, signal: AbortSignal): Promise<MediaLinkPage> {
  const paging = input(pageQuerySchema, query)
  const values = input(ownerQuerySchema, { ...paging, owner_type: ownerType, owner_id: ownerID })
  return requestData(signal, (signal) => apiClient.GET('/media/links', { signal, params: { query: values } }), mediaPageSchema, 8_000,
    (page, response) => validLivePage(page, response, values) && page.data.every((link) => link.owner_type === values.owner_type && link.owner_id === values.owner_id))
}
