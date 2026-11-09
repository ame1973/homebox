import { ApiError } from '@/lib/api/client'
import { fetchItem, type Item } from '@/lib/api/catalog'
import { idSchema, input, pathIDSchema } from '@/lib/api/domain'
import { fetchPurchaseCorrectionsPage, samePurchaseLine, type PurchaseLineCorrection } from '@/lib/api/financial-corrections'
import { fetchItemUnitsPage, fetchUnit, type Unit } from '@/lib/api/holdings'
import { fetchPurchase, fetchPurchaseLinesPage, type Purchase, type PurchaseLine } from '@/lib/api/purchases'
import { readAllMetadata } from '@/lib/metadata'

export type PurchaseCorrectionHistory = Readonly<{ purchase: Purchase; lines: readonly PurchaseLine[]; corrections: readonly PurchaseLineCorrection[] }>
export type OriginalCorrectionUnit = Readonly<{ kind: 'unallocated' }> | Readonly<{ kind: 'unaddressable'; id: string }> | Readonly<{ kind: 'resolved'; unit: Unit }>
export type ReplacementCorrectionSource = Readonly<{ kind: 'none' }> | Readonly<{ kind: 'quantity'; item: Item }> | Readonly<{ kind: 'individual'; item: Item; units: readonly Unit[] }>
export type PurchaseCorrectionSource = PurchaseCorrectionHistory & Readonly<{ line: PurchaseLine; item: Item; originalUnit: OriginalCorrectionUnit; replacement: ReplacementCorrectionSource }>

function unchanged<T extends object>(before: T, after: T): boolean {
  return (Object.keys(before) as (keyof T)[]).every((key) => before[key] === after[key])
}

// Validate relationships between the complete current and immutable collections;
// entry IDs are not chronological and retained original revisions are not guessed.
export function coherentPurchaseCorrectionHistory(owner: string, lines: readonly PurchaseLine[], entries: readonly PurchaseLineCorrection[]) {
  const live = new Map(lines.map((line) => [line.id, line]))
  const before = new Map<string, PurchaseLine>(), afterIDs = new Set<string>(), next = new Map<string, string | null>()
  if (live.size !== lines.length || new Set(entries.map((entry) => entry.id)).size !== entries.length || lines.some((line) => line.purchase_id !== owner)) return false
  for (const entry of entries) {
    if (entry.purchase_id !== owner || entry.before.purchase_id !== owner || before.has(entry.before.id) || live.has(entry.before.id)) return false
    before.set(entry.before.id, entry.before)
    next.set(entry.before.id, entry.after?.id ?? null)
  }
  for (const entry of entries) {
    if (entry.after === null) continue
    const after = entry.after, actual = live.get(after.id) ?? before.get(after.id)
    if (after.purchase_id !== owner || afterIDs.has(after.id) || actual === undefined || !samePurchaseLine(after, actual)) return false
    afterIDs.add(after.id)
  }
  const complete = new Set<string>()
  for (const id of before.keys()) {
    const path = new Set<string>()
    let current: string | null = id
    while (current !== null && next.has(current) && !complete.has(current)) {
      if (path.has(current)) return false
      path.add(current)
      current = next.get(current) ?? null
    }
    for (const visited of path) complete.add(visited)
  }
  return true
}
async function collections(purchaseID: string, signal: AbortSignal) {
  const lines = await readAllMetadata((query, signal) => fetchPurchaseLinesPage(purchaseID, query, signal), signal)
  const corrections = await readAllMetadata((query, signal) => fetchPurchaseCorrectionsPage(purchaseID, query, signal), signal)
  if (!coherentPurchaseCorrectionHistory(purchaseID, lines, corrections)) throw new ApiError('INVALID_RESPONSE')
  return { lines, corrections }
}
async function finishPurchase(before: Purchase, purchaseID: string, signal: AbortSignal) {
  const purchase = await fetchPurchase(purchaseID, signal)
  if (signal.aborted) throw new ApiError('REQUEST_ABORTED')
  if (purchase.id !== purchaseID || before.id !== purchaseID || !unchanged(before, purchase)) throw new ApiError('INVALID_RESPONSE')
  return purchase
}

// Complete only this Purchase's current Lines and correction history between
// matching Purchase reads, including when no active Line remains.
export async function readPurchaseCorrectionHistory(purchaseID: string, signal: AbortSignal): Promise<PurchaseCorrectionHistory> {
  const id = input(pathIDSchema, purchaseID), before = await fetchPurchase(id, signal)
  const data = await collections(id, signal)
  const purchase = await finishPurchase(before, id, signal)
  return { purchase, ...data }
}

// An explicit null target selects no replacement scope. The original Unit is
// review context only; an unaddressable retained ID is preserved, never null.
export async function readPurchaseCorrection(purchaseID: string, lineID: string, replacementItemID: string | null, signal: AbortSignal): Promise<PurchaseCorrectionSource> {
  const id = input(pathIDSchema, purchaseID), selected = input(idSchema, lineID)
  const targetID = replacementItemID === null ? null : input(pathIDSchema, replacementItemID)
  const before = await fetchPurchase(id, signal), data = await collections(id, signal)
  const line = data.lines.find((value) => value.id === selected)
  if (!line) {
    await finishPurchase(before, id, signal)
    throw new ApiError('NOT_FOUND')
  }
  const oldItem = await fetchItem(line.item_id, signal)
  if (oldItem.id !== line.item_id) throw new ApiError('INVALID_RESPONSE')
  let originalUnit: OriginalCorrectionUnit = { kind: 'unallocated' }
  if (line.item_unit_id !== null) {
    if (pathIDSchema.safeParse(line.item_unit_id).success) {
      const unit = await fetchUnit(line.item_unit_id, signal)
      if (unit.id !== line.item_unit_id || unit.item_id !== oldItem.id) throw new ApiError('INVALID_RESPONSE')
      originalUnit = { kind: 'resolved', unit }
    } else originalUnit = { kind: 'unaddressable', id: line.item_unit_id }
  }
  let replacement: ReplacementCorrectionSource = { kind: 'none' }
  if (targetID !== null) {
    const item = targetID === oldItem.id ? oldItem : await fetchItem(targetID, signal)
    if (item.id !== targetID) throw new ApiError('INVALID_RESPONSE')
    replacement = item.tracking_mode === 'individual'
      ? { kind: 'individual', item, units: await readAllMetadata((query, signal) => fetchItemUnitsPage(item.id, query, signal), signal) }
      : { kind: 'quantity', item }
    if (replacement.kind === 'individual' && replacement.units.some((unit) => unit.item_id !== item.id)) throw new ApiError('INVALID_RESPONSE')
    if (targetID !== oldItem.id && !unchanged(item, await fetchItem(targetID, signal))) throw new ApiError('INVALID_RESPONSE')
  }
  if (originalUnit.kind === 'resolved') {
    const after = await fetchUnit(originalUnit.unit.id, signal)
    if (!unchanged(originalUnit.unit, after)) throw new ApiError('INVALID_RESPONSE')
    if (replacement.kind === 'individual' && replacement.item.id === oldItem.id) {
      const actual = replacement.units.find((unit) => unit.id === originalUnit.unit.id)
      if (!actual || !unchanged(originalUnit.unit, actual)) throw new ApiError('INVALID_RESPONSE')
    }
  }
  const item = await fetchItem(oldItem.id, signal)
  if (!unchanged(oldItem, item)) throw new ApiError('INVALID_RESPONSE')
  const purchase = await finishPurchase(before, id, signal)
  return { purchase, ...data, line, item, originalUnit, replacement }
}
