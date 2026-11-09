import type { FieldErrors, Resolver } from 'react-hook-form'
import { validText } from '@/lib/api/client'
import { parseSafeCount } from '@/lib/api/domain'
import { sameDisposalRecord, type DisposalCorrection } from '@/lib/api/financial-corrections'
import { purchaseInstantSchema } from '@/lib/api/purchases'
import { messages } from '@/lib/messages'
import { disposalCorrectionProposal, type DisposalCorrectionValues } from '@/features/assets/correction-form'
import type { DisposalCorrectionSource } from '@/features/assets/correction-model'
import { sameRecord } from '@/features/purchases/display'

const text = messages.disposalCorrections, asset = messages.assets

function sameCorrection(before: DisposalCorrection, current: DisposalCorrection | undefined) {
  return !!current && before.id === current.id && before.revision === current.revision && before.item_unit_id === current.item_unit_id &&
    before.action === current.action && before.reason === current.reason && before.created_at === current.created_at &&
    sameDisposalRecord(before.before, current.before) && sameDisposalRecord(before.after, current.after)
}
export function sameDisposalCorrectionSource(before: DisposalCorrectionSource | null | undefined, current: DisposalCorrectionSource | null | undefined) {
  if (!before || !current) return before === current
  return sameRecord(before.unit, current.unit) && sameRecord(before.item, current.item) && sameRecord(before.asset, current.asset) &&
    sameDisposalRecord(before.disposal, current.disposal) && before.corrections.length === current.corrections.length &&
    before.corrections.every((entry, index) => sameCorrection(entry, current.corrections[index]))
}

// Raw fields remain strings; complete financial eligibility stays server-owned.
export function disposalCorrectionResolver(source: DisposalCorrectionSource): Resolver<DisposalCorrectionValues> {
  return (values) => {
    const errors: FieldErrors<DisposalCorrectionValues> = {}
    const check = (name: keyof DisposalCorrectionValues, valid: boolean, message: string) => { if (!valid) errors[name] = { type: 'validate', message } }
    check('action', values.action === 'replace' || values.action === 'void' && source.disposal.current !== null, text.actionError)
    check('reason', validText(values.reason, 1, 10000), text.reasonError)
    if (values.action === 'replace') {
      check('kind', Object.hasOwn(asset.disposalKinds, values.kind), asset.choiceError)
      check('currency', values.currency === source.asset.currency, asset.currencyHelp)
      check('realized_proceeds_minor', parseSafeCount(values.realized_proceeds_minor) !== undefined && (values.kind === 'sold' || values.realized_proceeds_minor === '0'), values.kind === 'sold' ? asset.moneyError : asset.nonSaleError)
      check('disposed_at', purchaseInstantSchema.safeParse(values.disposed_at).success, asset.instantError)
      check('note', validText(values.note, 0, 10000), asset.textHelp)
    }
    if (Object.keys(errors).length) return { values: {}, errors }
    try { disposalCorrectionProposal(values, source) } catch { return { values: {}, errors: { action: { type: 'validate', message: text.actionError } } } }
    return { values, errors: {} }
  }
}
