import { z } from 'zod'
import { ApiError } from '@/lib/api/client'
import { fetchItem, fetchItemsPage } from '@/lib/api/catalog'
import { idSchema, input, pathIDSchema } from '@/lib/api/domain'
import { fetchItemUnitsPage, fetchUnit, type Unit } from '@/lib/api/holdings'
import { fetchLocation, fetchLocationsPage } from '@/lib/api/locations'
import { fetchMedia, fetchMediaLinksPage, fetchOwnerMediaLinksPage, type Media, type MediaLinkView } from '@/lib/api/media'
import type { MediaOwner } from '@/lib/api/media-writes'
import { readAllMetadata } from '@/lib/metadata'

const scopeSchema = z.discriminatedUnion('kind', [
  z.strictObject({ kind: z.literal('item'), id: idSchema }),
  z.strictObject({ kind: z.literal('unit'), id: idSchema, itemID: idSchema.optional() }),
  z.strictObject({ kind: z.literal('location'), id: idSchema }),
])
export type MediaOwnerScope = z.infer<typeof scopeSchema>
export type OwnerMedia = Readonly<{ owner: MediaOwner; links: readonly MediaLinkView[] }>
export type MediaMembership = Readonly<{ media: Media; links: readonly MediaLinkView[] }>

// These validated source DTOs contain only scalar/null fields; compare values,
// not insertion order, and never substitute a cached projection for a read.
function sameSource(first: object, second: object): boolean {
  const keys = Object.keys(first)
  return keys.length === Object.keys(second).length && keys.every((key) => Reflect.get(first, key) === Reflect.get(second, key))
}
function requireActive(signal: AbortSignal) {
  if (signal.aborted) throw new ApiError('REQUEST_ABORTED')
}
function selected<T extends { id: string }>(rows: readonly T[], id: string): T {
  const value = rows.find((row) => row.id === id)
  if (!value) throw new ApiError('NOT_FOUND')
  return value
}

// Retained owner IDs may not be representable as paths. Resolve such Items and
// Locations from complete live lists, and Units from their known Item's complete
// list. A Unit whose Item path is also unaddressable cannot be resolved here.
async function readOwner(scope: MediaOwnerScope, signal: AbortSignal): Promise<MediaOwner> {
  const addressable = pathIDSchema.safeParse(scope.id).success
  let owner: MediaOwner
  if (scope.kind === 'item') {
    const value = addressable ? await fetchItem(scope.id, signal) : selected(await readAllMetadata(fetchItemsPage, signal), scope.id)
    owner = { kind: 'item', value }
  } else if (scope.kind === 'unit') {
    let value: Unit
    if (addressable) value = await fetchUnit(scope.id, signal)
    else {
      const itemID = scope.itemID
      if (itemID === undefined || !pathIDSchema.safeParse(itemID).success) throw new ApiError('VALIDATION_ERROR')
      value = selected(await readAllMetadata((query, signal) => fetchItemUnitsPage(itemID, query, signal), signal), scope.id)
    }
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
  if (!sameSource(before.value, owner.value)) throw new ApiError('INVALID_RESPONSE')
  const snapshots = new Map<string, Media>()
  for (const link of links) {
    const previous = snapshots.get(link.media_id)
    if (link.owner_type !== scope.kind || link.owner_id !== scope.id || link.media.id !== link.media_id || previous && !sameSource(previous, link.media)) {
      throw new ApiError('INVALID_RESPONSE')
    }
    snapshots.set(link.media_id, link.media)
  }
  return { owner, links }
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
  if (!sameSource(before, media) || links.some((link) => link.media_id !== mediaID || !sameSource(link.media, media))) throw new ApiError('INVALID_RESPONSE')
  return { media, links }
}
