import { ApiError, validText } from './api/client'
import { input, pathIDSchema } from './api/domain'
import { assetPageQuerySchema, assetSummaryQuerySchema, type AssetPageQuery, type AssetSummaryQuery } from './api/assets'
import { purchaseCursorTrail, purchaseQuery } from './purchase-navigation'

export const assetModes = ['detail', 'edit', 'usage', 'usage/new', 'costs', 'costs/new', 'retire', 'dispose', 'summary'] as const
export type AssetRouteMode = typeof assetModes[number]
export function assetRoute(id: string, mode: AssetRouteMode = 'detail') {
  if (!assetModes.includes(mode)) throw new ApiError('VALIDATION_ERROR')
  return `/assets/${encodeURIComponent(input(pathIDSchema, id))}${mode === 'detail' ? '' : `/${mode}`}`
}
export function assetQuery(search: string): AssetPageQuery | undefined {
  const result = assetPageQuerySchema.safeParse(purchaseQuery(search))
  return result.success ? result.data : undefined
}
export function assetListRoute(query: AssetPageQuery) {
  const params = new URLSearchParams()
  for (const [key, value] of Object.entries(input(assetPageQuerySchema, query))) if (value !== undefined) params.set(key, String(value))
  return `/assets${params.size ? `?${params}` : ''}`
}
export function assetSummaryQuery(search: string): AssetSummaryQuery | undefined {
  try {
    if (!validText(search, 0, 16385)) return undefined
    decodeURIComponent(search.replace(/\+/g, ' '))
    const entries = [...new URLSearchParams(search)]
    if (entries.length > 1 || entries.some(([key]) => key !== 'as_of')) return undefined
    const parsed = assetSummaryQuerySchema.safeParse(Object.fromEntries(entries))
    return parsed.success ? parsed.data : undefined
  } catch { return undefined }
}
export function assetSummaryRoute(id: string, query: AssetSummaryQuery) {
  const values = input(assetSummaryQuerySchema, query)
  return `${assetRoute(id, 'summary')}${values.as_of === undefined ? '' : `?${new URLSearchParams({ as_of: values.as_of })}`}`
}

// Only implemented read/editor paths and normalized read scope survive login.
// Amounts, drafts, arbitrary route state and original commands never do.
export function assetReturnPath(value: unknown): string | undefined {
  if (typeof value !== 'string' || !validText(value, 1, 18000) || value.includes('\\') || value.includes('#')) return undefined
  try {
    const url = new URL(value, 'https://homebox.invalid')
    if (url.origin !== 'https://homebox.invalid' || url.pathname + url.search !== value) return undefined
    if (url.pathname === '/assets') return assetQuery(url.search) === undefined ? undefined : value
    const match = /^\/assets\/([^/]+)(?:\/(edit|usage|costs|retire|dispose|summary)(\/new)?)?$/.exec(url.pathname)
    if (!match || !pathIDSchema.safeParse(decodeURIComponent(match[1]!)).success || match[3] && match[2] !== 'usage' && match[2] !== 'costs') return undefined
    if (url.search && (match[2] !== 'summary' || assetSummaryQuery(url.search) === undefined)) return undefined
    return value
  } catch { return undefined }
}
export function assetNavigationState(candidate: unknown, cursors: unknown) {
  const safe = assetReturnPath(candidate)
  return { assetList: safe && safe.split('?')[0] === '/assets' ? safe : '/assets', assetCursors: purchaseCursorTrail(cursors) }
}
