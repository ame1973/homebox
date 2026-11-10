import { z } from 'zod'
import { ApiError } from '@/lib/api/client'
import { fetchItem, fetchItemsPage } from '@/lib/api/catalog'
import { idSchema, input, pathIDSchema } from '@/lib/api/domain'
import { fetchUnit, fetchUnitByIdentity } from '@/lib/api/holdings'
import { fetchLocation, fetchLocationsPage } from '@/lib/api/locations'
import { fetchMedia, fetchMediaLinksPage, fetchOwnerMediaLinksPage, mediaLinkViewSchema, mediaSchema, type Media, type MediaLinkView } from '@/lib/api/media'
import { mediaOwnerSchema, type MediaOwner } from '@/lib/api/media-writes'
import { readAllMetadata } from '@/lib/metadata'

const scopeSchema = z.discriminatedUnion('kind', [
  z.strictObject({ kind: z.literal('item'), id: idSchema }),
  z.strictObject({ kind: z.literal('unit'), id: idSchema, itemID: idSchema.optional() }),
  z.strictObject({ kind: z.literal('location'), id: idSchema }),
])
export type MediaOwnerScope = z.infer<typeof scopeSchema>
export type OwnerMedia = Readonly<{ owner: MediaOwner; links: readonly MediaLinkView[] }>
export type MediaMembership = Readonly<{ media: Media; links: readonly MediaLinkView[] }>

const ownerMediaSchema: z.ZodType<OwnerMedia> = z.strictObject({ owner: mediaOwnerSchema, links: z.array(mediaLinkViewSchema) })
const membershipSchema: z.ZodType<MediaMembership> = z.strictObject({ media: mediaSchema, links: z.array(mediaLinkViewSchema) })

// These validated source DTOs contain only scalar/null fields; compare values,
// not insertion order, and never substitute a cached projection for a read.
export function sameMediaSource(first: object, second: object): boolean {
  const keys = Object.keys(first)
  return keys.length === Object.keys(second).length && keys.every((key) => Reflect.get(first, key) === Reflect.get(second, key))
}

export function sameMediaLink(first: MediaLinkView, second: MediaLinkView): boolean {
  const { media: firstMedia, ...firstLink } = first, { media: secondMedia, ...secondLink } = second
  return sameMediaSource(firstLink, secondLink) && sameMediaSource(firstMedia, secondMedia)
}

// Revalidate whole selected scopes before freezing or delivering a command.
// An owner bracket does not establish independent Media freshness.
export function reviewedOwnerMedia(value: unknown): OwnerMedia {
  const source = input(ownerMediaSchema, value), ids = new Set<string>(), snapshots = new Map<string, Media>()
  for (const link of source.links) {
    const previous = snapshots.get(link.media_id)
    if (ids.has(link.id) || link.owner_type !== source.owner.kind || link.owner_id !== source.owner.value.id || previous && !sameMediaSource(previous, link.media)) {
      throw new ApiError('VALIDATION_ERROR')
    }
    ids.add(link.id)
    snapshots.set(link.media_id, link.media)
  }
  return source
}

export function reviewedMediaMembership(value: unknown): MediaMembership {
  const source = input(membershipSchema, value), ids = new Set<string>()
  for (const link of source.links) {
    if (ids.has(link.id) || link.media_id !== source.media.id || !sameMediaSource(link.media, source.media)) throw new ApiError('VALIDATION_ERROR')
    ids.add(link.id)
  }
  return source
}
function requireActive(signal: AbortSignal) {
  if (signal.aborted) throw new ApiError('REQUEST_ABORTED')
}
function selected<T extends { id: string }>(rows: readonly T[], id: string): T {
  const value = rows.find((row) => row.id === id)
  if (!value) throw new ApiError('NOT_FOUND')
  return value
}

// Retained Item/Location IDs may not be representable as paths; resolve them
// from complete live lists. Unit query lookup preserves its exact identity.
async function readOwner(scope: MediaOwnerScope, signal: AbortSignal): Promise<MediaOwner> {
  const addressable = pathIDSchema.safeParse(scope.id).success
  let owner: MediaOwner
  if (scope.kind === 'item') {
    const value = addressable ? await fetchItem(scope.id, signal) : selected(await readAllMetadata(fetchItemsPage, signal), scope.id)
    owner = { kind: 'item', value }
  } else if (scope.kind === 'unit') {
    const value = addressable ? await fetchUnit(scope.id, signal) : await fetchUnitByIdentity(scope.id, signal)
    owner = { kind: 'unit', value }
  } else {
    const value = addressable && scope.id !== 'tree' ? await fetchLocation(scope.id, signal)
      : selected(await readAllMetadata(fetchLocationsPage, signal), scope.id)
    owner = { kind: 'location', value }
  }
  requireActive(signal)
  if (owner.value.id !== scope.id || owner.kind === 'unit' && scope.kind === 'unit' && scope.itemID !== undefined && owner.value.item_id !== scope.itemID) {
    throw new ApiError('INVALID_RESPONSE')
  }
  return owner
}

// Owner revisions bracket link membership, not independently changing Media
// metadata. Repeated Media values must still agree across the collected pages.
export async function readOwnerMedia(requested: MediaOwnerScope, signal: AbortSignal): Promise<OwnerMedia> {
  const scope = input(scopeSchema, requested)
  requireActive(signal)
  const before = await readOwner(scope, signal)
  const links = await readAllMetadata((query, signal) => fetchOwnerMediaLinksPage(scope.kind, scope.id, query, signal), signal)
  const owner = await readOwner(scope, signal)
  if (!sameMediaSource(before.value, owner.value)) throw new ApiError('INVALID_RESPONSE')
  try { return reviewedOwnerMedia({ owner, links }) } catch { throw new ApiError('INVALID_RESPONSE') }
}

// A Media revision covers its complete membership. Bracket every live link page
// with matching actual Media reads and compare every joined Media snapshot.
export async function readMediaMembership(id: string, signal: AbortSignal): Promise<MediaMembership> {
  const mediaID = input(pathIDSchema, id)
  requireActive(signal)
  const before = await fetchMedia(mediaID, signal)
  requireActive(signal)
  if (before.id !== mediaID) throw new ApiError('INVALID_RESPONSE')
  const links = await readAllMetadata((query, signal) => fetchMediaLinksPage(mediaID, query, signal), signal)
  const media = await fetchMedia(mediaID, signal)
  requireActive(signal)
  if (!sameMediaSource(before, media)) throw new ApiError('INVALID_RESPONSE')
  try { return reviewedMediaMembership({ media, links }) } catch { throw new ApiError('INVALID_RESPONSE') }
}
