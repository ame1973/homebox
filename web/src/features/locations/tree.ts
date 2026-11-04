import { ApiError } from '@/lib/api/client'
import { fetchLocationsPage, type Location } from '@/lib/api/locations'

export type LocationTree = Readonly<{
  byID: ReadonlyMap<string, Location>
  children: ReadonlyMap<string | null, readonly string[]>
  order: readonly string[]
  depth: ReadonlyMap<string, number>
  canReceive: ReadonlyMap<string, boolean>
  descendantCount: ReadonlyMap<string, number>
}>

const collator = new Intl.Collator('zh-Hant', { sensitivity: 'variant' })

// Publish a complete validated graph, not an arbitrary first page or a partial
// prefix. Normal HTTP lists remain live; inconsistent cross-page reads require
// a fresh explicit read instead of inventing missing parents or accepting cycles.
export async function readLocationTree(signal: AbortSignal): Promise<LocationTree> {
  const records: Location[] = []
  const identities = new Set<string>()
  const cursors = new Set<string>()
  let cursor: string | undefined
  do {
    const page = await fetchLocationsPage({ limit: 100, ...(cursor ? { cursor } : {}) }, signal)
    if (signal.aborted) throw new ApiError('REQUEST_ABORTED')
    for (const record of page.data) {
      if (identities.has(record.id)) throw new ApiError('INVALID_RESPONSE')
      identities.add(record.id)
      records.push(record)
    }
    cursor = page.next_cursor ?? undefined
    if (cursor && cursors.has(cursor)) throw new ApiError('INVALID_RESPONSE')
    if (cursor) cursors.add(cursor)
  } while (cursor)
  return buildLocationTree(records)
}

export function buildLocationTree(records: readonly Location[]): LocationTree {
  const byID = new Map<string, Location>()
  const children = new Map<string | null, string[]>([[null, []]])
  for (const record of records) {
    if (byID.has(record.id) || record.parent_id === record.id) throw new ApiError('INVALID_RESPONSE')
    byID.set(record.id, record)
    children.set(record.id, [])
  }
  for (const record of records) {
    const siblings = children.get(record.parent_id)
    if (!siblings) throw new ApiError('INVALID_RESPONSE')
    siblings.push(record.id)
  }
  for (const siblings of children.values()) siblings.sort((left, right) => {
    const a = byID.get(left)!
    const b = byID.get(right)!
    if (a.sort_order !== b.sort_order) return a.sort_order < b.sort_order ? -1 : 1
    return collator.compare(a.name, b.name) || collator.compare(a.code, b.code) || collator.compare(a.id, b.id)
  })
  const order: string[] = []
  const depth = new Map<string, number>()
  const canReceive = new Map<string, boolean>()
  const stack = (children.get(null) ?? []).slice().reverse().map((id) => ({ id, level: 0, activePath: true }))
  while (stack.length) {
    const entry = stack.pop()!
    if (depth.has(entry.id)) throw new ApiError('INVALID_RESPONSE')
    const record = byID.get(entry.id)!
    const active = entry.activePath && record.status === 'active'
    order.push(entry.id)
    depth.set(entry.id, entry.level)
    canReceive.set(entry.id, active)
    for (const child of (children.get(entry.id) ?? []).slice().reverse()) stack.push({ id: child, level: entry.level + 1, activePath: active })
  }
  if (order.length !== records.length) throw new ApiError('INVALID_RESPONSE')
  const descendantCount = new Map<string, number>()
  for (const id of order.slice().reverse()) {
    let total = 0
    for (const child of children.get(id) ?? []) total += 1 + descendantCount.get(child)!
    descendantCount.set(id, total)
  }
  return { byID, children, order, depth, canReceive, descendantCount }
}

export function locationPath(tree: LocationTree, id: string | null): Location[] {
  const path: Location[] = []
  let current = id
  while (current !== null) {
    const record = tree.byID.get(current)
    if (!record) throw new ApiError('INVALID_RESPONSE')
    path.push(record)
    current = record.parent_id
  }
  return path.reverse()
}

export function subtreeIDs(tree: LocationTree, id: string): ReadonlySet<string> {
  const result = new Set<string>()
  const stack = [id]
  while (stack.length) {
    const current = stack.pop()!
    result.add(current)
    for (const child of tree.children.get(current) ?? []) stack.push(child)
  }
  return result
}

export function visibleLocations(tree: LocationTree, expanded: ReadonlySet<string>, search: string, archived: boolean): Location[] {
  const term = search.trim().toLocaleLowerCase('zh-Hant')
  const wanted = new Set<string>()
  for (const id of tree.order.slice().reverse()) {
    const record = tree.byID.get(id)!
    const matches = !term || `${record.name}\n${record.code}`.toLocaleLowerCase('zh-Hant').includes(term)
    if ((archived || record.status === 'active') && matches || (tree.children.get(id) ?? []).some((child) => wanted.has(child))) wanted.add(id)
  }
  const visible = new Set<string>()
  const result: Location[] = []
  for (const id of tree.order) {
    const record = tree.byID.get(id)!
    if (!wanted.has(id) || record.parent_id !== null && (!visible.has(record.parent_id) || !term && !expanded.has(record.parent_id))) continue
    visible.add(id)
    result.push(record)
  }
  return result
}
