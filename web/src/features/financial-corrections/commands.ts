import { z } from 'zod'
import { ApiError, type WriteContext } from '@/lib/api/client'
import { input, text } from '@/lib/api/domain'
import * as api from '@/lib/api/financial-corrections'
import { unitSchema } from '@/lib/api/holdings'
import { freezeReviewedCommand } from '@/lib/reviewed-command'
import { reviewedDisposalCorrectionSource, type DisposalCorrectionProposal } from '@/features/assets/correction-form'
import { reviewedPurchaseCorrectionSource, type PurchaseCorrectionProposal } from '@/features/purchases/correction-form'

export type CorrectionCommandInput = (PurchaseCorrectionProposal & Readonly<{ kind: 'correct-purchase' }>)
  | (DisposalCorrectionProposal & Readonly<{ kind: 'correct-disposal' }>)
export type CorrectionCommand = CorrectionCommandInput & Readonly<{ key: string }>
export type CorrectionResult = api.PurchaseLineCorrection | api.DisposalCorrection

const envelope = z.discriminatedUnion('kind', [
  z.strictObject({ kind: z.literal('correct-purchase'), source: z.unknown(), body: z.unknown(), unit: z.unknown() }),
  z.strictObject({ kind: z.literal('correct-disposal'), source: z.unknown(), body: z.unknown() }),
])

function checkedProposal(proposal: unknown): CorrectionCommandInput {
  const checked = input(envelope, proposal)
  if (checked.kind === 'correct-purchase') {
    const source = reviewedPurchaseCorrectionSource(checked.source), body = input(api.purchaseCorrectionWriteSchema, checked.body)
    if (body.line_id !== source.line.id || body.line_revision !== source.line.revision || body.item_revision !== source.item.revision) throw new ApiError('VALIDATION_ERROR')
    if (body.action === 'void') {
      input(z.null(), checked.unit)
      return { kind: checked.kind, source, body, unit: null }
    }
    const target = source.replacement
    if (target.kind === 'none' || body.replacement.item_id !== target.item.id || body.replacement.item_revision !== target.item.revision) throw new ApiError('VALIDATION_ERROR')
    if (body.replacement.item_unit_id === null) {
      input(z.null(), checked.unit)
      return { kind: checked.kind, source, body, unit: null }
    }
    if (target.kind !== 'individual') throw new ApiError('TRACKING_MODE_CONFLICT')
    const unit = input(unitSchema, checked.unit), selected = target.units.find((candidate) => candidate.id === body.replacement.item_unit_id)
    if (!selected || unit.id !== selected.id || JSON.stringify(unit) !== JSON.stringify(selected)) throw new ApiError('VALIDATION_ERROR')
    return { kind: checked.kind, source, body, unit }
  }
  const source = reviewedDisposalCorrectionSource(checked.source), body = input(api.disposalCorrectionWriteSchema, checked.body)
  if (body.asset_revision !== source.asset.revision) throw new ApiError('VALIDATION_ERROR')
  if (body.action === 'void' ? source.disposal.current === null : body.replacement.currency !== source.asset.currency) throw new ApiError('VALIDATION_ERROR')
  return { kind: checked.kind, source, body }
}

export function freezeCorrectionCommand(proposal: CorrectionCommandInput): CorrectionCommand {
  return freezeReviewedCommand(checkedProposal(proposal))
}

// Send only the reviewed source/body/key. A frozen acknowledgement is historical
// and must never be installed as current state or automatically replayed.
export async function sendCorrectionCommand(command: CorrectionCommand, csrfToken: string, signal: AbortSignal): Promise<CorrectionResult> {
  const { key, ...proposal } = command, checked = checkedProposal(proposal)
  const context: WriteContext = { csrfToken, signal, idempotencyKey: input(text(8, 128), key) }
  if (checked.kind === 'correct-purchase') {
    const { source, body } = checked
    const replacementItem = body.action === 'void' || source.replacement.kind === 'none' ? null : source.replacement.item
    return api.correctPurchaseLine(source.purchase.id, body, { ...context, purchase: source.purchase, line: source.line, item: source.item, replacementItem })
  }
  return api.correctDisposal(checked.source.unit.id, checked.body, { ...context, asset: checked.source.asset, disposal: checked.source.disposal })
}
