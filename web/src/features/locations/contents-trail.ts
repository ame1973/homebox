import { ApiError } from '@/lib/api/client'
import type { LocationContentsRead } from '@/lib/api/locations'

type Scope = 'direct' | 'subtree'
type VisitedPage = Readonly<{ cursor: string | undefined; read: LocationContentsRead }>
export type ContentsTrail = readonly VisitedPage[]

// Pages are fetched only on explicit actions. Previously visited pages remain
// volatile so navigation and cross-page identity checks use the same evidence.
export function appendContentsPage(
  trail: ContentsTrail, read: LocationContentsRead, cursor: string | undefined, id: string, scope: Scope,
): ContentsTrail {
  const previous = trail.at(-1)
  if ((!previous && cursor !== undefined) || (previous && previous.read.value.next_cursor !== cursor) ||
      read.value.location_id !== id || read.value.scope !== scope) throw new ApiError('INVALID_RESPONSE')
  if (previous && previous.read.revision !== read.revision) throw new ApiError('INVALID_CURSOR')

  // ponytail: visited pages are rescanned when appending; use server-provided
  // bidirectional continuations if unusually deep manual browsing needs less memory.
  const units = new Set(trail.flatMap(({ read }) => read.value.units.map((unit) => unit.id)))
  const stock = new Set(trail.flatMap(({ read }) => read.value.stock.map((row) => JSON.stringify([row.item_id, row.location_id]))))
  const locations = new Set(trail.flatMap(({ read }) => read.value.sub_locations.map((location) => location.id)))
  for (const [values, seen] of [
    [read.value.units.map((unit) => unit.id), units],
    [read.value.stock.map((row) => JSON.stringify([row.item_id, row.location_id])), stock],
    [read.value.sub_locations.map((location) => location.id), locations],
  ] as const) {
    for (const value of values) {
      if (seen.has(value)) throw new ApiError('INVALID_RESPONSE')
      seen.add(value)
    }
  }
  const next = read.value.next_cursor
  if (next !== null && (next === cursor || trail.some((page) => page.cursor === next))) throw new ApiError('INVALID_RESPONSE')
  return [...trail, { cursor, read }]
}
