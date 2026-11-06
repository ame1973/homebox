import { validText } from '@/lib/api/client'
import { locationReturnPath } from './location-navigation'
import { catalogReturnPath } from './catalog-navigation'
import { holdingReturnPath } from './holding-navigation'

export type TaxonomyKind = 'categories' | 'tags'
export const taxonomyListRoute = (kind: TaxonomyKind) => `/settings/${kind}`
export const taxonomyEditRoute = (kind: TaxonomyKind, id: string) => `${taxonomyListRoute(kind)}/${encodeURIComponent(id)}/edit`
export const taxonomyCreateRoute = (kind: TaxonomyKind, parentID?: string) => `${taxonomyListRoute(kind)}/new${kind === 'categories' && parentID ? `?parent=${encodeURIComponent(parentID)}` : ''}`

function validID(id: string) {
  return validText(id, 1, 128) && !['.', '..'].includes(id) && !['/', '\\', '\0'].some((character) => id.includes(character))
}

// Authentication returns only to implemented application routes. IDs matching
// new/tree remain independent edit targets, never collection creation aliases.
export function settingsReturnPath(value: unknown): string | undefined {
  if (typeof value !== 'string' || value.length > 2048 || !value.startsWith('/settings') || value.includes('\\') || value.includes('#')) return undefined
  try {
    const url = new URL(value, 'https://homebox.invalid')
    if (url.origin !== 'https://homebox.invalid') return undefined
    if ((url.pathname === '/settings' || url.pathname === '/settings/account') && !url.search) return url.pathname
    const match = /^\/settings\/(categories|tags)(?:\/([^/]+)(\/edit)?)?$/.exec(url.pathname)
    if (!match) return undefined
    const id = match[2] ? decodeURIComponent(match[2]) : undefined
    if (id !== undefined && (!validID(id) || !match[3] && id.toLowerCase() !== 'new')) return undefined
    if (url.search) {
      const entries = Array.from(url.searchParams.entries())
      if (match[1] !== 'categories' || id?.toLowerCase() !== 'new' || match[3] || entries.length !== 1 || entries[0]?.[0] !== 'parent' || !validID(entries[0][1])) return undefined
    }
    return url.pathname + url.search
  } catch { return undefined }
}

export function applicationReturnPath(value: unknown): string | undefined {
  return locationReturnPath(value) ?? settingsReturnPath(value) ?? catalogReturnPath(value) ?? holdingReturnPath(value)
}
