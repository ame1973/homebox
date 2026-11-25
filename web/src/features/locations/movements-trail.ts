import { ApiError } from '@/lib/api/client'
import { orderedUTF8, type LocationMovementsRead } from '@/lib/api/locations'

type VisitedPage = Readonly<{ cursor: string | undefined; read: LocationMovementsRead }>
export type MovementsTrail = readonly VisitedPage[]

// Keep only explicitly read live pages; the cursor and Location revision are
// not proofs that the collection is frozen or that a historical path is current.
export function appendMovementsPage(trail: MovementsTrail, read: LocationMovementsRead, cursor: string | undefined, id: string): MovementsTrail {
  const previous = trail.at(-1)
  if ((!previous && cursor !== undefined) || (previous && previous.read.value.next_cursor !== cursor) || read.value.location_id !== id) throw new ApiError('INVALID_RESPONSE')
  if (previous && previous.read.revision !== read.revision) throw new ApiError('INVALID_CURSOR')
  if (previous && previous.read.value.deleted !== read.value.deleted) throw new ApiError('INVALID_RESPONSE')
  const first = read.value.data[0], prior = previous?.read.value.data.at(-1)
  if (first && prior && !orderedUTF8(prior.id, first.id)) throw new ApiError('INVALID_RESPONSE')
  if (!read.value.data.every((event, index) => event.location_id === id && (index === 0 || orderedUTF8(read.value.data[index - 1]!.id, event.id)))) throw new ApiError('INVALID_RESPONSE')
  // ponytail: retain and rescan explicit visited cursors; a server-supported
  // backward continuation would reduce memory for unusually deep browsing.
  const next = read.value.next_cursor
  if (next !== null && (next === cursor || trail.some((page) => page.cursor === next))) throw new ApiError('INVALID_RESPONSE')
  return [...trail, { cursor, read }]
}
