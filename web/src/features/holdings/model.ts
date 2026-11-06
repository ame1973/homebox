import { ApiError } from '@/lib/api/client'
import { fetchItem, type Item } from '@/lib/api/catalog'
import { fetchItemUnitsPage, fetchStockPage, fetchUnit, type Stock, type Unit } from '@/lib/api/holdings'
import { readAllMetadata } from '@/lib/metadata'

export type ItemHoldings = Readonly<{ kind: 'units'; item: Item; units: readonly Unit[] }>
  | Readonly<{ kind: 'stock'; item: Item; stock: readonly Stock[] }>
export type UnitHolding = Readonly<{ item: Item; unit: Unit }>

// Complete only this Item's actual collection. Its aggregate brackets the live
// pages; this is not a frozen sync snapshot or an invented empty other domain.
export async function readItemHoldings(itemID: string, signal: AbortSignal): Promise<ItemHoldings> {
  const before = await fetchItem(itemID, signal)
  const collection = before.tracking_mode === 'individual'
    ? { kind: 'units' as const, units: await readAllMetadata((query, signal) => fetchItemUnitsPage(itemID, query, signal), signal) }
    : { kind: 'stock' as const, stock: (await readAllMetadata(async (query, signal) => {
      const page = await fetchStockPage({ ...query, item_id: itemID }, signal)
      return { ...page, data: page.data.map((stock) => ({ id: JSON.stringify([stock.item_id, stock.location_id]), stock })) }
    }, signal)).map(({ stock }) => stock) }
  const item = await fetchItem(itemID, signal)
  if (signal.aborted) throw new ApiError('REQUEST_ABORTED')
  if ((Object.keys(before) as (keyof Item)[]).some((key) => before[key] !== item[key]) || item.id !== itemID) throw new ApiError('INVALID_RESPONSE')
  if (collection.kind === 'units' ? collection.units.some((unit) => unit.item_id !== itemID)
    : collection.stock.some((stock) => stock.item_id !== itemID || stock.unit_code !== item.unit_code || stock.base_unit !== item.base_unit || stock.scale_factor !== item.scale_factor)) {
    throw new ApiError('INVALID_RESPONSE')
  }
  return { ...collection, item }
}

// Independent Unit authority brackets the owner label read. Item metadata does
// not become an extra Unit precondition or require loading every sibling Unit.
export async function readUnitHolding(unitID: string, signal: AbortSignal): Promise<UnitHolding> {
  const before = await fetchUnit(unitID, signal)
  const item = await fetchItem(before.item_id, signal)
  const unit = await fetchUnit(unitID, signal)
  if (signal.aborted) throw new ApiError('REQUEST_ABORTED')
  if (unit.id !== unitID || item.id !== unit.item_id || item.tracking_mode !== 'individual' ||
    (Object.keys(before) as (keyof Unit)[]).some((key) => before[key] !== unit[key])) throw new ApiError('INVALID_RESPONSE')
  return { item, unit }
}
