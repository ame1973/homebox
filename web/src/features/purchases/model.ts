import { ApiError } from '@/lib/api/client'
import { fetchItem, type Item } from '@/lib/api/catalog'
import { fetchItemUnitsPage, type Unit } from '@/lib/api/holdings'
import { fetchItemPurchasesPage, fetchPurchase, fetchPurchaseLinesPage, type Purchase, type PurchaseLine } from '@/lib/api/purchases'
import type { components } from '@/lib/api/schema'
import { readAllMetadata } from '@/lib/metadata'

export type PurchaseDetail = Readonly<{ purchase: Purchase; lines: readonly PurchaseLine[] }>
export type ItemPurchases = Readonly<{ item: Item; purchases: readonly Purchase[] }>
export type PurchaseAllocationContext = Readonly<{ kind: 'quantity'; purchase: Purchase; item: Item }>
  | Readonly<{ kind: 'individual'; purchase: Purchase; item: Item; units: readonly Unit[] }>
export type PurchaseAllocation = Readonly<{ purchase: Purchase; item: Item; unit: Unit | null; body: components['schemas']['PurchaseLineWrite'] }>

function unchanged<T extends object>(before: T, after: T): boolean {
  return (Object.keys(before) as (keyof T)[]).every((key) => before[key] === after[key])
}

// Complete this Purchase's actual Lines between matching header reads. Counts
// are allocation counts, never mixed-unit quantities or another monetary total.
export async function readPurchaseDetail(purchaseID: string, signal: AbortSignal): Promise<PurchaseDetail> {
  const before = await fetchPurchase(purchaseID, signal)
  const lines = await readAllMetadata((query, signal) => fetchPurchaseLinesPage(purchaseID, query, signal), signal)
  const purchase = await fetchPurchase(purchaseID, signal)
  if (signal.aborted) throw new ApiError('REQUEST_ABORTED')
  if (purchase.id !== purchaseID || !unchanged(before, purchase) || lines.some((line) => line.purchase_id !== purchaseID)) throw new ApiError('INVALID_RESPONSE')
  return { purchase, lines }
}

export async function readItemPurchases(itemID: string, signal: AbortSignal): Promise<ItemPurchases> {
  const before = await fetchItem(itemID, signal)
  const purchases = await readAllMetadata((query, signal) => fetchItemPurchasesPage(itemID, query, signal), signal)
  const item = await fetchItem(itemID, signal)
  if (signal.aborted) throw new ApiError('REQUEST_ABORTED')
  if (item.id !== itemID || !unchanged(before, item)) throw new ApiError('INVALID_RESPONSE')
  return { item, purchases }
}

// The Purchase interval contains the whole Item/Unit interval. Quantity review
// does not query Stock or invent an empty Unit set; no physical receipt follows.
export async function readPurchaseAllocation(purchaseID: string, itemID: string, signal: AbortSignal): Promise<PurchaseAllocationContext> {
  const parent = await fetchPurchase(purchaseID, signal)
  const before = await fetchItem(itemID, signal)
  const collection = before.tracking_mode === 'individual'
    ? { kind: 'individual' as const, units: await readAllMetadata((query, signal) => fetchItemUnitsPage(itemID, query, signal), signal) }
    : { kind: 'quantity' as const }
  const item = await fetchItem(itemID, signal)
  const purchase = await fetchPurchase(purchaseID, signal)
  if (signal.aborted) throw new ApiError('REQUEST_ABORTED')
  if (purchase.id !== purchaseID || item.id !== itemID || !unchanged(parent, purchase) || !unchanged(before, item) ||
    collection.kind === 'individual' && collection.units.some((unit) => unit.item_id !== itemID)) throw new ApiError('INVALID_RESPONSE')
  return { ...collection, purchase, item }
}
