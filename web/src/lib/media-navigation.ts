import { z } from 'zod'
import { validText } from './api/client'
import { idSchema, input } from './api/domain'

const scopeSchema = z.strictObject({ kind: z.enum(['item', 'unit', 'location']), id: idSchema })
export type MediaRouteScope = z.infer<typeof scopeSchema>

// Owner identities are query values, not path segments; legacy slash, NUL and
// dot IDs must round-trip without becoming a different resource or route.
export function mediaOwnerRoute(scope: MediaRouteScope): string {
  const checked = input(scopeSchema, scope)
  return `/photos?${new URLSearchParams({ owner_type: checked.kind, owner_id: checked.id })}`
}

export function mediaOwnerScope(search: string): MediaRouteScope | undefined {
  if (typeof search !== 'string' || !search.startsWith('?') || !validText(search, 1, 4096) || search.includes('#') || search.includes('\\')) return undefined
  try {
    decodeURIComponent(search.slice(1).replace(/\+/g, ' '))
    const entries = Array.from(new URLSearchParams(search))
    if (entries.length !== 2 || entries.filter(([key]) => key === 'owner_type').length !== 1 || entries.filter(([key]) => key === 'owner_id').length !== 1) return undefined
    const values = new URLSearchParams(search)
    const result = scopeSchema.safeParse({ kind: values.get('owner_type'), id: values.get('owner_id') })
    return result.success ? result.data : undefined
  } catch { return undefined }
}

// Carry only the validated owner scope through login, never draft data or keys.
export function mediaReturnPath(value: unknown): string | undefined {
  if (typeof value !== 'string' || !validText(value, 1, 4096) || value.includes('\\') || value.includes('#')) return undefined
  try {
    const url = new URL(value, 'https://homebox.invalid')
    if (url.origin !== 'https://homebox.invalid' || url.pathname !== '/photos' || url.pathname + url.search !== value) return undefined
    const scope = mediaOwnerScope(url.search)
    return scope ? mediaOwnerRoute(scope) : undefined
  } catch { return undefined }
}
