import { ApiError } from '@/lib/api/client'
import { fetchAsset, type Asset } from '@/lib/api/assets'
import { fetchItem, type Item } from '@/lib/api/catalog'
import { input, pathIDSchema } from '@/lib/api/domain'
import { fetchDisposalCorrectionsPage, fetchDisposalRecord, sameDisposalData, sameDisposalRecord,
  type DisposalCorrection, type DisposalRecord } from '@/lib/api/financial-corrections'
import { fetchUnit, type Unit } from '@/lib/api/holdings'
import { readAllMetadata } from '@/lib/metadata'

export type DisposalCorrectionSource = Readonly<{ unit: Unit; item: Item; asset: Asset; disposal: DisposalRecord; corrections: readonly DisposalCorrection[] }>

function unchanged<T extends object>(before: T, after: T): boolean {
  return (Object.keys(before) as (keyof T)[]).every((key) => before[key] === after[key])
}

// History pages are ordered by entry ID, not subject revision. Reconstruct only
// links actually present in the returned decisions; never invent an old proof.
function coherentHistory(record: DisposalRecord, entries: readonly DisposalCorrection[]) {
  if (!entries.length) return record.current !== null && sameDisposalData(record.current, record.original)
  const ordered = [...entries].sort((a, b) => a.before.revision - b.before.revision)
  const first = ordered[0]!
  if (first.item_unit_id !== record.item_unit_id || first.before.current === null ||
    !sameDisposalData(first.before.original, record.original) || !sameDisposalData(first.before.current, record.original)) return false
  let previous = first.before
  for (const entry of ordered) {
    if (entry.item_unit_id !== record.item_unit_id || !sameDisposalRecord(previous, entry.before)) return false
    previous = entry.after
  }
  return sameDisposalRecord(previous, record)
}

// Bracket only the actual Unit/Item/Asset/retained-record and complete selected
// correction history. A 404 remains an error, not a fabricated null decision.
export async function readDisposalCorrection(unitID: string, signal: AbortSignal): Promise<DisposalCorrectionSource> {
  const id = input(pathIDSchema, unitID)
  const unit = await fetchUnit(id, signal), item = await fetchItem(unit.item_id, signal), asset = await fetchAsset(id, signal)
  const disposal = await fetchDisposalRecord(id, signal)
  if (unit.id !== id || unit.item_id !== item.id || item.tracking_mode !== 'individual' || asset.item_unit_id !== id || disposal.item_unit_id !== id) throw new ApiError('INVALID_RESPONSE')
  const corrections = await readAllMetadata((query, signal) => fetchDisposalCorrectionsPage(id, query, signal), signal)
  const afterDisposal = await fetchDisposalRecord(id, signal), afterAsset = await fetchAsset(id, signal)
  const afterItem = await fetchItem(item.id, signal), afterUnit = await fetchUnit(id, signal)
  if (signal.aborted) throw new ApiError('REQUEST_ABORTED')
  if (!sameDisposalRecord(disposal, afterDisposal) || !unchanged(asset, afterAsset) || !unchanged(item, afterItem) || !unchanged(unit, afterUnit) || !coherentHistory(disposal, corrections)) throw new ApiError('INVALID_RESPONSE')
  return { unit: afterUnit, item: afterItem, asset: afterAsset, disposal: afterDisposal, corrections }
}
