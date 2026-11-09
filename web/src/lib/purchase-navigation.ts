import { itemPageQuerySchema, type ItemPageQuery } from './api/catalog'
import { ApiError, validText } from './api/client'
import { input, pathIDSchema, validCursor } from './api/domain'
import { purchasePageQuerySchema, type PurchasePageQuery } from './api/purchases'
import { catalogQuery } from './catalog-navigation'

export const itemPurchasesRoute = (id: string) => `/items/${encodeURIComponent(input(pathIDSchema, id))}/purchases`
export function purchaseRoute(id: string, mode: 'detail' | 'edit' = 'detail') {
  if (!['detail', 'edit'].includes(mode)) throw new ApiError('VALIDATION_ERROR')
  const identity = input(pathIDSchema, id)
  return `/purchases/${encodeURIComponent(identity)}${mode === 'edit' ? '/edit' : identity.toLowerCase() === 'new' ? '/view' : ''}`
}
export const purchaseAllocationRoute = (id: string, itemID: string) => `/purchases/${encodeURIComponent(input(pathIDSchema, id))}/allocate/${encodeURIComponent(input(pathIDSchema, itemID))}`
export const purchaseCorrectionHistoryRoute = (id: string) => `/purchases/${encodeURIComponent(input(pathIDSchema, id))}/line-corrections`
export function purchaseSelectionRoute(id: string, query: ItemPageQuery = {}) {
  const params = new URLSearchParams()
  for (const [key, value] of Object.entries(input(itemPageQuerySchema, query))) if (value !== undefined) params.set(key, String(value))
  return `/purchases/${encodeURIComponent(input(pathIDSchema, id))}/allocate${params.size ? `?${params}` : ''}`
}
export function purchaseQuery(search: string): PurchasePageQuery | undefined {
  try {
    if (!validText(search, 0, 16385)) return undefined
    decodeURIComponent(search.replace(/\+/g, ' '))
    const values = catalogQuery(search)
    const result = purchasePageQuerySchema.safeParse(values)
    return result.success ? result.data : undefined
  } catch { return undefined }
}
export function purchaseListRoute(query: PurchasePageQuery) {
  const params = new URLSearchParams()
  for (const [key, value] of Object.entries(input(purchasePageQuerySchema, query))) if (value !== undefined) params.set(key, String(value))
  return `/purchases${params.size ? `?${params}` : ''}`
}

// Admit implemented paths and validated filter/cursor scope only. URL repair,
// duplicate criteria and financial command data are never login return context.
export function purchaseReturnPath(value: unknown): string | undefined {
  if (typeof value !== 'string' || !validText(value, 1, 18000) || value.includes('\\') || value.includes('#')) return undefined
  try {
    const url = new URL(value, 'https://homebox.invalid')
    if (url.origin !== 'https://homebox.invalid' || url.pathname + url.search !== value) return undefined
    if (url.pathname === '/purchases') return purchaseQuery(url.search) === undefined ? undefined : value
    const match = /^\/purchases\/([^/]+)(?:\/(edit|view|allocate|line-corrections)(?:\/([^/]+))?)?$/.exec(url.pathname)
    const item = /^\/items\/([^/]+)\/purchases$/.exec(url.pathname)
    if (item) return !url.search && pathIDSchema.safeParse(decodeURIComponent(item[1]!)).success ? value : undefined
    if (!match || !pathIDSchema.safeParse(decodeURIComponent(match[1]!)).success) return undefined
    if (match[3] && (match[2] !== 'allocate' || !pathIDSchema.safeParse(decodeURIComponent(match[3])).success)) return undefined
    if (url.search) {
      if (match[2] !== 'allocate' || match[3]) return undefined
      decodeURIComponent(url.search.replace(/\+/g, ' '))
      if (!catalogQuery(url.search)) return undefined
    }
    return value
  } catch { return undefined }
}
export function purchaseCursorTrail(value: unknown): (string | null)[] {
  return Array.isArray(value) && value.every((cursor) => cursor === null || typeof cursor === 'string' && validCursor(cursor)) ? [...value] : []
}
export function purchaseNavigationState(candidate: unknown, cursors: unknown, item: unknown) {
  const safe = purchaseReturnPath(candidate)
  const purchaseList = safe && (safe.split('?')[0] === '/purchases' || /^\/items\/[^/]+\/purchases$/.test(safe)) ? safe : '/purchases'
  return { purchaseList, purchaseCursors: purchaseCursorTrail(cursors), ...(pathIDSchema.safeParse(item).success ? { purchaseItem: item as string } : {}) }
}
