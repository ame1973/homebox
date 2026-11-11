import { z } from 'zod'
import { validText } from './api/client'
import { idSchema, input } from './api/domain'

const scopeSchema = z.strictObject({ kind: z.enum(['item', 'unit', 'location']), id: idSchema })
const editScopeSchema = scopeSchema.extend({ linkID: idSchema })
export type MediaRouteScope = z.infer<typeof scopeSchema>
export type MediaEditScope = z.infer<typeof editScopeSchema>

// Owner and link identities are query values, not path segments; legacy slash,
// NUL and dot IDs must round-trip without changing the resource or route.
export function mediaOwnerRoute(scope: MediaRouteScope): string {
  const checked = input(scopeSchema, scope)
  return `/photos?${new URLSearchParams({ owner_type: checked.kind, owner_id: checked.id })}`
}

export function mediaAttachRoute(scope: MediaRouteScope): string {
  const checked = input(scopeSchema, scope)
  return `/photos/attach?${new URLSearchParams({ owner_type: checked.kind, owner_id: checked.id })}`
}

export function mediaEditRoute(scope: MediaEditScope): string {
  const checked = input(editScopeSchema, scope)
  return `/photos/edit?${new URLSearchParams({ owner_type: checked.kind, owner_id: checked.id, link_id: checked.linkID })}`
}

function exactParameters(search: string, names: readonly string[]): URLSearchParams | undefined {
  if (typeof search !== 'string' || !search.startsWith('?') || !validText(search, 1, 4096) || search.includes('#') || search.includes('\\')) return undefined
  try {
    decodeURIComponent(search.slice(1).replace(/\+/g, ' '))
    const values = new URLSearchParams(search), entries = Array.from(values)
    return entries.length === names.length && names.every((name) => values.getAll(name).length === 1) ? values : undefined
  } catch { return undefined }
}

export function mediaOwnerScope(search: string): MediaRouteScope | undefined {
  const values = exactParameters(search, ['owner_type', 'owner_id'])
  if (!values) return undefined
  const result = scopeSchema.safeParse({ kind: values.get('owner_type'), id: values.get('owner_id') })
  return result.success ? result.data : undefined
}

export function mediaEditScope(search: string): MediaEditScope | undefined {
  const values = exactParameters(search, ['owner_type', 'owner_id', 'link_id'])
  if (!values) return undefined
  const result = editScopeSchema.safeParse({ kind: values.get('owner_type'), id: values.get('owner_id'), linkID: values.get('link_id') })
  return result.success ? result.data : undefined
}

// Carry only validated identities through login, never draft data or keys.
export function mediaReturnPath(value: unknown): string | undefined {
  if (typeof value !== 'string' || !validText(value, 1, 4096) || value.includes('\\') || value.includes('#')) return undefined
  try {
    const url = new URL(value, 'https://homebox.invalid')
    if (url.origin !== 'https://homebox.invalid' || url.pathname + url.search !== value) return undefined
    if (url.pathname === '/photos' || url.pathname === '/photos/attach') {
      const scope = mediaOwnerScope(url.search)
      return scope ? url.pathname === '/photos' ? mediaOwnerRoute(scope) : mediaAttachRoute(scope) : undefined
    }
    if (url.pathname === '/photos/edit') {
      const scope = mediaEditScope(url.search)
      return scope ? mediaEditRoute(scope) : undefined
    }
    return undefined
  } catch { return undefined }
}
