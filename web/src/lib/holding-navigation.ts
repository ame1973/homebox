import { ApiError, validText } from './api/client'
import { idSchema, input, pathIDSchema } from './api/domain'
import type { StockAction } from '@/features/holdings/form'

export const stockActions = ['receive', 'consume', 'transfer', 'adjust'] as const
export type UnitMode = 'detail' | 'edit' | 'move' | 'status'
export const itemHoldingsRoute = (itemID: string) => `/items/${encodeURIComponent(input(pathIDSchema, itemID))}/holdings`
export const unitCreateRoute = (itemID: string) => `/items/${encodeURIComponent(input(pathIDSchema, itemID))}/units/new`
export function unitRoute(unitID: string, mode: UnitMode = 'detail') {
  if (!['detail', 'edit', 'move', 'status'].includes(mode)) throw new ApiError('VALIDATION_ERROR')
  return `/units/${encodeURIComponent(input(pathIDSchema, unitID))}${mode === 'detail' ? '' : `/${mode}`}`
}
export function stockRoute(itemID: string, action: StockAction, locationID?: string) {
  if (!stockActions.includes(action)) throw new ApiError('VALIDATION_ERROR')
  const query = locationID === undefined ? '' : `?${new URLSearchParams({ location: input(idSchema, locationID) })}`
  return `/items/${encodeURIComponent(input(pathIDSchema, itemID))}/stock/${action}${query}`
}

// Only real same-application holding routes may survive a login round-trip.
// Reject URL repair, unknown/duplicate prefill and path-unsafe imported IDs.
export function holdingReturnPath(value: unknown): string | undefined {
  if (typeof value !== 'string' || !validText(value, 1, 4096) || value.includes('\\') || value.includes('#')) return undefined
  try {
    const url = new URL(value, 'https://homebox.invalid')
    if (url.origin !== 'https://homebox.invalid' || url.pathname + url.search !== value) return undefined
    const item = /^\/items\/([^/]+)\/(holdings|units\/new|stock\/(?:receive|consume|transfer|adjust))$/.exec(url.pathname)
    const unit = /^\/(?:units\/([^/]+)(?:\/(?:edit|move|status))?|u\/([^/]+))$/.exec(url.pathname)
    const identity = item?.[1] ?? unit?.[1] ?? unit?.[2]
    if (identity === undefined || !pathIDSchema.safeParse(decodeURIComponent(identity)).success) return undefined
    if (url.search) {
      if (!item?.[2]?.startsWith('stock/')) return undefined
      decodeURIComponent(url.search.slice(1).replace(/\+/g, ' '))
      const entries = Array.from(url.searchParams)
      if (entries.length !== 1 || entries[0]?.[0] !== 'location' || !idSchema.safeParse(entries[0][1]).success) return undefined
    }
    return value
  } catch { return undefined }
}

export function stockPrefill(search: string): string | undefined {
  if (!search.startsWith('?') || !validText(search, 1, 4096)) return undefined
  try {
    decodeURIComponent(search.slice(1).replace(/\+/g, ' '))
    const entries = Array.from(new URLSearchParams(search))
    return entries.length === 1 && entries[0]?.[0] === 'location' && idSchema.safeParse(entries[0][1]).success ? entries[0][1] : undefined
  } catch { return undefined }
}
