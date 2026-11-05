import { itemPageQuerySchema, type ItemPageQuery } from './api/catalog'
import { input, pathIDSchema } from './api/domain'

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
    const match = /^\/items\/([^/]+)(?:\/(edit|view))?$/.exec(url.pathname)
    if (!match || url.search || !pathIDSchema.safeParse(decodeURIComponent(match[1]!)).success) return undefined
    return value
  } catch { return undefined }
}
