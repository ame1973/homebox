import { z } from 'zod'
import { ApiError } from '@/lib/api/client'
import { assetSchema } from '@/lib/api/assets'
import { itemSchema } from '@/lib/api/catalog'
import { input, parseSafeCount, pathIDSchema } from '@/lib/api/domain'
import { disposalCorrectionSchema, disposalCorrectionWriteSchema, disposalRecordSchema, sameDisposalData, type DisposalCorrectionWrite } from '@/lib/api/financial-corrections'
import { unitSchema } from '@/lib/api/holdings'
import { coherentDisposalCorrectionHistory, type DisposalCorrectionSource } from './correction-model'

export const disposalCorrectionValues = z.strictObject({ action: z.string(), reason: z.string(), kind: z.string(), currency: z.string(),
  realized_proceeds_minor: z.string(), disposed_at: z.string(), note: z.string() })
export type DisposalCorrectionValues = z.infer<typeof disposalCorrectionValues>
export type DisposalCorrectionProposal = Readonly<{ source: DisposalCorrectionSource; body: DisposalCorrectionWrite }>

const sourceSchema: z.ZodType<DisposalCorrectionSource> = z.strictObject({ unit: unitSchema, item: itemSchema, asset: assetSchema,
  disposal: disposalRecordSchema, corrections: z.array(disposalCorrectionSchema) })
function reviewedSource(source: DisposalCorrectionSource): DisposalCorrectionSource {
  const checked = input(sourceSchema, source), { unit, item, asset, disposal, corrections } = checked
  input(pathIDSchema, unit.id); input(pathIDSchema, item.id)
  if (unit.item_id !== item.id || item.tracking_mode !== 'individual' || asset.item_unit_id !== unit.id || disposal.item_unit_id !== unit.id ||
    !coherentDisposalCorrectionHistory(disposal, corrections)) throw new ApiError('VALIDATION_ERROR')
  return checked
}

export function disposalCorrectionDefaults(source: DisposalCorrectionSource): DisposalCorrectionValues {
  return { action: '', reason: '', kind: '', currency: reviewedSource(source).asset.currency, realized_proceeds_minor: '', disposed_at: '', note: '' }
}

// A retained null current decision is a financial void, not a missing history.
// Replacement money/date are new explicit inputs, including non-sale zero.
export function disposalCorrectionProposal(values: DisposalCorrectionValues, source: DisposalCorrectionSource): DisposalCorrectionProposal {
  const checked = input(disposalCorrectionValues, values), original = reviewedSource(source)
  const fields = { asset_revision: original.asset.revision, reason: checked.reason }
  if (checked.action === 'void') {
    if (original.disposal.current === null) throw new ApiError('ASSET_HISTORY_LOCKED')
    return { source: original, body: input(disposalCorrectionWriteSchema, { ...fields, action: 'void', replacement: null }) }
  }
  if (checked.action !== 'replace' || checked.currency !== original.asset.currency) throw new ApiError('VALIDATION_ERROR')
  return { source: original, body: input(disposalCorrectionWriteSchema, { ...fields, action: 'replace', replacement: {
    kind: checked.kind, currency: checked.currency, realized_proceeds_minor: parseSafeCount(checked.realized_proceeds_minor) ?? NaN,
    disposed_at: checked.disposed_at, note: checked.note,
  } }) }
}

// Explicit adoption updates the currency interpretation, not the raw amount,
// date, reason or kind. The first recorded disposal remains the same subject.
export function adoptDisposalCorrection(values: DisposalCorrectionValues, before: DisposalCorrectionSource, current: DisposalCorrectionSource) {
  const previous = reviewedSource(before), next = reviewedSource(current)
  if (previous.unit.id !== next.unit.id || previous.item.id !== next.item.id || !sameDisposalData(previous.disposal.original, next.disposal.original)) throw new ApiError('VALIDATION_ERROR')
  return { values: { ...input(disposalCorrectionValues, values), currency: next.asset.currency }, source: next }
}
