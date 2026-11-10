import { z } from 'zod'
import { apiClient, requestData, type WriteContext } from './client'
import { itemSchema, type Item } from './catalog'
import { deleteAckSchema, input, pathIDSchema, resourceRequest, revisionHeaders, text } from './domain'
import { unitSchema, type Unit } from './holdings'
import { locationSchema, type Location } from './locations'
import { mediaLinkSchema, mediaLinkViewSchema, mediaSchema, type Media, type MediaLink, type MediaLinkView } from './media'
import type { components } from './schema'

export type MediaOwner = Readonly<{ kind: 'item'; value: Item }> | Readonly<{ kind: 'unit'; value: Unit }> | Readonly<{ kind: 'location'; value: Location }>
export type MediaAttachContext = WriteContext & Readonly<{ media: Media; owner: MediaOwner; expectedRevision?: never }>
export type MediaLinkWriteContext = WriteContext & Readonly<{ link: MediaLink | MediaLinkView; expectedRevision?: never }>
export type MediaLinkMetadataInput = components['schemas']['MediaLinkWrite']
export type MediaLinkPatchInput = MediaLinkMetadataInput & (
  Readonly<{ is_cover: boolean }> | Readonly<{ sort_order: number }> | Readonly<{ description: string }>
)

const metadata = z.strictObject({ is_cover: z.boolean().optional(), sort_order: z.int().min(0).optional(), description: text(0, 10000).optional() })
export const mediaLinkMetadataSchema: z.ZodType<MediaLinkMetadataInput> = metadata
export const mediaLinkPatchSchema: z.ZodType<MediaLinkPatchInput> = z.union([
  metadata.required({ is_cover: true }), metadata.required({ sort_order: true }), metadata.required({ description: true }),
])
export const mediaOwnerSchema: z.ZodType<MediaOwner> = z.discriminatedUnion('kind', [
  z.strictObject({ kind: z.literal('item'), value: itemSchema }),
  z.strictObject({ kind: z.literal('unit'), value: unitSchema }),
  z.strictObject({ kind: z.literal('location'), value: locationSchema }),
])

// Owner identity and revision are derived from the same reviewed source. They
// are JSON values, so retained owner IDs need not be addressable as paths.
export async function attachMedia(id: string, body: MediaLinkMetadataInput, context: MediaAttachContext): Promise<MediaLink> {
  const mediaID = input(pathIDSchema, id), values = input(mediaLinkMetadataSchema, body)
  const media = input(mediaSchema, context.media), owner = input(mediaOwnerSchema, context.owner)
  input(z.undefined(), context.expectedRevision); input(z.literal(mediaID), media.id)
  const headers = revisionHeaders({ ...context, expectedRevision: media.revision })
  const selected = owner.kind === 'item' ? { item_id: owner.value.id } : owner.kind === 'unit' ? { item_unit_id: owner.value.id } : { location_id: owner.value.id }
  const request = { ...values, ...selected, owner_revision: owner.value.revision }
  const schema = mediaLinkSchema.refine((link) => link.media_id === mediaID && link.owner_type === owner.kind && link.owner_id === owner.value.id &&
    link.is_cover === (values.is_cover ?? false) && link.sort_order === (values.sort_order ?? 0) && link.description === (values.description ?? ''))
  return resourceRequest(schema, context.signal,
    (signal) => apiClient.POST('/media/{id}/links', { signal, body: request, params: { path: { id: mediaID }, header: headers } }), { creating: true })
}

// A joined list view is accepted as review context, not as a Media proof. Only
// the selected independent link revision is sent for editing or unlinking.
function reviewedLink(id: string, context: MediaLinkWriteContext) {
  const resourceID = input(pathIDSchema, id)
  const link = input(z.union([mediaLinkSchema, mediaLinkViewSchema]), context.link)
  input(z.undefined(), context.expectedRevision); input(z.literal(resourceID), link.id)
  return { id: resourceID, link, headers: revisionHeaders({ ...context, expectedRevision: link.revision }) }
}

export async function patchMediaLink(id: string, body: MediaLinkPatchInput, context: MediaLinkWriteContext): Promise<MediaLink> {
  const values = input(mediaLinkPatchSchema, body)
  const reviewed = reviewedLink(id, context), before = reviewed.link
  const cover = values.is_cover ?? before.is_cover, order = values.sort_order ?? before.sort_order, description = values.description ?? before.description
  const revision = before.revision + Number(cover !== before.is_cover || order !== before.sort_order || description !== before.description)
  const schema = mediaLinkSchema.refine((link) => link.id === reviewed.id && link.media_id === before.media_id && link.owner_type === before.owner_type &&
    link.owner_id === before.owner_id && link.is_cover === cover && link.sort_order === order && link.description === description)
  return requestData(context.signal,
    (signal) => apiClient.PATCH('/media/links/{id}', { signal, body: values, params: { path: { id: reviewed.id }, header: reviewed.headers } }), schema, 30_000,
    (link, response) => link.revision === revision && response.headers.get('ETag') === `"${link.revision}"`)
}

export async function unlinkMediaLink(id: string, context: MediaLinkWriteContext): Promise<components['schemas']['ActionAck']> {
  const reviewed = reviewedLink(id, context), revision = reviewed.link.revision + 1
  return requestData(context.signal,
    (signal) => apiClient.DELETE('/media/links/{id}', { signal, params: { path: { id: reviewed.id }, header: reviewed.headers } }), deleteAckSchema, 30_000,
    (ack, response) => !response.headers.has('ETag') && ack.revision === revision)
}
