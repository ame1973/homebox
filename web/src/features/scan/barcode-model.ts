import { fetchBarcodeMatches, type BarcodeMatch, type BarcodeSearchQuery } from '@/lib/api/barcode-search'
import { fetchItemByIdentity, type Item } from '@/lib/api/catalog'
import { ApiError } from '@/lib/api/client'

export const barcodeChoicePageSize = 5
export type BarcodeChoice = Readonly<{ barcode: BarcodeMatch; item: Item }>
export type BarcodeChoices = Readonly<{ matches: readonly BarcodeChoice[]; next_cursor: string | null }>

// Each page is live: the Item details are separate reads, not a frozen proof of
// barcode membership or a unique physical Unit. Bound the extra requests and
// deduplicate shared owners instead of hydrating the entire catalog.
export async function readBarcodeChoices(query: Omit<BarcodeSearchQuery, 'limit'>, signal: AbortSignal): Promise<BarcodeChoices> {
  if (signal.aborted) throw new ApiError('REQUEST_ABORTED')
  const controller = new AbortController()
  const abort = () => controller.abort()
  signal.addEventListener('abort', abort, { once: true })
  try {
    const page = await fetchBarcodeMatches({ ...query, limit: barcodeChoicePageSize }, controller.signal)
    if (signal.aborted) throw new ApiError('REQUEST_ABORTED')
    const ids = [...new Set(page.matches.map((match) => match.item_id))]
    const items = await Promise.all(ids.map(async (id) => {
      try { return await fetchItemByIdentity(id, controller.signal) }
      catch (error) {
        if (error instanceof ApiError && error.code === 'NOT_FOUND' && error.status === 404) throw new ApiError('INVALID_RESPONSE')
        throw error
      }
    }))
    if (signal.aborted) throw new ApiError('REQUEST_ABORTED')
    const byID = new Map(items.map((item) => [item.id, item]))
    const matches = page.matches.map((barcode) => {
      const item = byID.get(barcode.item_id)
      if (!item) throw new ApiError('INVALID_RESPONSE')
      return { barcode, item }
    })
    return { matches, next_cursor: page.next_cursor }
  } catch (error) {
    controller.abort()
    if (signal.aborted) throw new ApiError('REQUEST_ABORTED')
    throw error
  } finally { signal.removeEventListener('abort', abort) }
}
