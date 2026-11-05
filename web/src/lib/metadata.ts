import { ApiError } from '@/lib/api/client'

type Identified = Readonly<{ id: string }>
type Node = Identified & Readonly<{ parent_id: string | null }>
type Page<T> = Readonly<{ data: readonly T[]; next_cursor?: string | null }>

export type Hierarchy<T extends Node> = Readonly<{
  byID: ReadonlyMap<string, T>
  children: ReadonlyMap<string | null, readonly string[]>
  order: readonly string[]
  depth: ReadonlyMap<string, number>
  descendantCount: ReadonlyMap<string, number>
}>

// Ordinary metadata remains live. Complete every explicit signed page, rejecting
// inconsistent prefixes instead of pretending a first page is a frozen replica.
export async function readAllMetadata<T extends Identified>(
  readPage: (query: { limit: number; cursor?: string }, signal: AbortSignal) => Promise<Page<T>>, signal: AbortSignal,
): Promise<T[]> {
  const records: T[] = []
  const identities = new Set<string>()
  const cursors = new Set<string>()
  let cursor: string | undefined
  do {
    const page = await readPage({ limit: 100, ...(cursor ? { cursor } : {}) }, signal)
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
  return records
}

export function buildHierarchy<T extends Node>(records: readonly T[], compare: (a: T, b: T) => number): Hierarchy<T> {
  const byID = new Map<string, T>()
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
  for (const siblings of children.values()) siblings.sort((a, b) => compare(byID.get(a)!, byID.get(b)!))
  const order: string[] = []
  const depth = new Map<string, number>()
  const stack = (children.get(null) ?? []).slice().reverse().map((id) => ({ id, level: 0 }))
  while (stack.length) {
    const entry = stack.pop()!
    if (depth.has(entry.id)) throw new ApiError('INVALID_RESPONSE')
    order.push(entry.id)
    depth.set(entry.id, entry.level)
    for (const child of (children.get(entry.id) ?? []).slice().reverse()) stack.push({ id: child, level: entry.level + 1 })
  }
  if (order.length !== records.length) throw new ApiError('INVALID_RESPONSE')
  const descendantCount = new Map<string, number>()
  for (const id of order.slice().reverse()) {
    let total = 0
    for (const child of children.get(id) ?? []) total += 1 + descendantCount.get(child)!
    descendantCount.set(id, total)
  }
  return { byID, children, order, depth, descendantCount }
}

export function hierarchyPath<T extends Node>(tree: Hierarchy<T>, id: string | null): T[] {
  const path: T[] = []
  let current = id
  while (current !== null) {
    const record = tree.byID.get(current)
    if (!record) throw new ApiError('INVALID_RESPONSE')
    path.push(record)
    current = record.parent_id
  }
  return path.reverse()
}

export function subtreeIDs(tree: Pick<Hierarchy<Node>, 'children'>, id: string): ReadonlySet<string> {
  const result = new Set<string>()
  const stack = [id]
  while (stack.length) {
    const current = stack.pop()!
    result.add(current)
    for (const child of tree.children.get(current) ?? []) stack.push(child)
  }
  return result
}

export function visibleHierarchy<T extends Node>(tree: Hierarchy<T>, expanded: ReadonlySet<string>, matches: (record: T) => boolean, searching: boolean): T[] {
  const wanted = new Set<string>()
  for (const id of tree.order.slice().reverse()) {
    if (matches(tree.byID.get(id)!) || (tree.children.get(id) ?? []).some((child) => wanted.has(child))) wanted.add(id)
  }
  const visible = new Set<string>()
  const result: T[] = []
  for (const id of tree.order) {
    const record = tree.byID.get(id)!
    if (!wanted.has(id) || record.parent_id !== null && (!visible.has(record.parent_id) || !searching && !expanded.has(record.parent_id))) continue
    visible.add(id)
    result.push(record)
  }
  return result
}
