import { itemPageQuerySchema, type ItemPageQuery } from './api/catalog'
import { validText } from './api/client'
import { input, pathIDSchema } from './api/domain'

export const catalogRowKinds = ['barcodes', 'aliases', 'attributes'] as const
export const catalogOwnedKinds = [...catalogRowKinds, 'tags'] as const
export type CatalogRowKind = typeof catalogRowKinds[number]
export type CatalogOwnedKind = typeof catalogOwnedKinds[number]

export function catalogOwnedRoute(itemID: string, kind: CatalogOwnedKind): string {
  return `/items/${encodeURIComponent(input(pathIDSchema, itemID))}/${kind}`
}
export function catalogOwnedCreateRoute(itemID: string, kind: CatalogRowKind): string {
  return `${catalogOwnedRoute(itemID, kind)}/new`
}
export function catalogOwnedEditRoute(itemID: string, kind: CatalogRowKind, rowID: string): string {
  return `${catalogOwnedRoute(itemID, kind)}/${encodeURIComponent(input(pathIDSchema, rowID))}/edit`
}

export function catalogNavigationState(candidate: unknown, cursors: unknown) {
  const catalogList = typeof candidate === 'string' && candidate.split('?')[0] === '/items' && catalogReturnPath(candidate) ? candidate : '/items'
  // ponytail: visited cursor history grows per page; use bidirectional server
  // continuations if unusually deep navigation reaches browser history limits.
  const catalogCursors: (string | null)[] = Array.isArray(cursors) && cursors.every((cursor) => cursor === null || typeof cursor === 'string' && validText(cursor, 1, 2048)) ? cursors : []
  return { catalogList, catalogCursors }
}

export function catalogRoute(id: string, mode: 'detail' | 'edit' = 'detail'): string {
  const identity = input(pathIDSchema, id)
  return `/items/${encodeURIComponent(identity)}${mode === 'edit' ? '/edit' : identity.toLowerCase() === 'new' ? '/view' : ''}`
}

export function catalogQuery(search: string): ItemPageQuery | undefined {
  if (search.length > 16384) return undefined
  const values: Record<string, string | number> = Object.create(null)
  for (const [key, value] of new URLSearchParams(search)) {
    if (Object.hasOwn(values, key)) return undefined
    if (key === 'limit') {
      if (!/^[1-9][0-9]{0,2}$/.test(value)) return undefined
      values[key] = Number(value)
    } else values[key] = value
  }
  const parsed = itemPageQuerySchema.safeParse(values)
  return parsed.success ? parsed.data : undefined
}

export function catalogListRoute(query: ItemPageQuery): string {
  const params = new URLSearchParams()
  for (const [key, value] of Object.entries(input(itemPageQuerySchema, query))) if (value !== undefined) params.set(key, String(value))
  return `/items${params.size ? `?${params}` : ''}`
}

// Return only to an implemented same-application route. Reject normalization,
// duplicate/unknown criteria and unsafe IDs, never repair an external redirect.
export function catalogReturnPath(value: unknown): string | undefined {
  if (typeof value !== 'string' || value.length > 18000 || !value.startsWith('/items') || value.includes('\\') || value.includes('#')) return undefined
  try {
    const url = new URL(value, 'https://homebox.invalid')
    if (url.origin !== 'https://homebox.invalid' || url.pathname + url.search !== value) return undefined
    if (url.pathname === '/items') return catalogQuery(url.search) === undefined ? undefined : value
    const match = [
      /^\/items\/([^/]+)(?:\/(?:edit|view))?$/,
      /^\/items\/([^/]+)\/(?:barcodes|aliases|attributes|tags)$/,
      /^\/items\/([^/]+)\/(?:barcodes|aliases|attributes)\/new$/,
      /^\/items\/([^/]+)\/(?:barcodes|aliases|attributes)\/([^/]+)\/edit$/,
    ].map((pattern) => pattern.exec(url.pathname)).find((result) => result !== null)
    if (!match || url.search || !match.slice(1).every((id) => pathIDSchema.safeParse(decodeURIComponent(id)).success)) return undefined
    return value
  } catch { return undefined }
}
