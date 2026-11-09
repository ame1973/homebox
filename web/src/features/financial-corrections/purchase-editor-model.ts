import type { FieldErrors, Resolver } from 'react-hook-form'
import { validText } from '@/lib/api/client'
import { samePurchaseLine, type PurchaseLineCorrection } from '@/lib/api/financial-corrections'
import { messages } from '@/lib/messages'
import { purchaseCorrectionProposal, type PurchaseCorrectionValues } from '@/features/purchases/correction-form'
import type { OriginalCorrectionUnit, PurchaseCorrectionSource, ReplacementCorrectionSource } from '@/features/purchases/correction-model'
import { sameRecord } from '@/features/purchases/display'
import { purchaseLineResolver } from '@/features/purchases/editor-validation'

const text = messages.purchaseCorrections

function sameOriginalUnit(before: OriginalCorrectionUnit, current: OriginalCorrectionUnit) {
  if (before.kind !== current.kind) return false
  if (before.kind === 'unallocated') return true
  if (before.kind === 'unaddressable') return current.kind === 'unaddressable' && before.id === current.id
  return current.kind === 'resolved' && sameRecord(before.unit, current.unit)
}
function sameReplacement(before: ReplacementCorrectionSource, current: ReplacementCorrectionSource) {
  if (before.kind !== current.kind) return false
  if (before.kind === 'none') return true
  if (current.kind === 'none' || !sameRecord(before.item, current.item)) return false
  if (before.kind === 'quantity') return current.kind === 'quantity'
  return current.kind === 'individual' && before.units.length === current.units.length && before.units.every((unit, index) => sameRecord(unit, current.units[index]))
}
function sameCorrection(before: PurchaseLineCorrection, current: PurchaseLineCorrection | undefined) {
  return !!current && before.id === current.id && before.revision === current.revision && before.purchase_id === current.purchase_id &&
    before.action === current.action && before.reason === current.reason && before.created_at === current.created_at && samePurchaseLine(before.before, current.before) &&
    (before.after === null ? current.after === null : current.after !== null && samePurchaseLine(before.after, current.after))
}
export function samePurchaseCorrectionSource(before: PurchaseCorrectionSource | null | undefined, current: PurchaseCorrectionSource | null | undefined) {
  if (!before || !current) return before === current
  return sameRecord(before.purchase, current.purchase) && sameRecord(before.item, current.item) && samePurchaseLine(before.line, current.line) &&
    sameOriginalUnit(before.originalUnit, current.originalUnit) && sameReplacement(before.replacement, current.replacement) &&
    before.lines.length === current.lines.length && before.lines.every((line, index) => !!current.lines[index] && samePurchaseLine(line, current.lines[index]!)) &&
    before.corrections.length === current.corrections.length && before.corrections.every((entry, index) => sameCorrection(entry, current.corrections[index]))
}

// Currency and each complete Item measurement are one reviewed interpretation.
export function correctionInterpretation(source: PurchaseCorrectionSource) {
  const tuple = (item: PurchaseCorrectionSource['item']) => [item.id, item.tracking_mode, item.unit_code, item.base_unit, item.scale_factor]
  return JSON.stringify([source.purchase.currency, tuple(source.item), source.replacement.kind === 'none' ? null : tuple(source.replacement.item)])
}

export function purchaseCorrectionResolver(source: PurchaseCorrectionSource): Resolver<PurchaseCorrectionValues> {
  return async (values) => {
    const errors: FieldErrors<PurchaseCorrectionValues> = {}
    if (values.action !== 'void' && values.action !== 'replace') errors.action = { type: 'validate', message: text.actionError }
    if (!validText(values.reason, 1, 10000)) errors.reason = { type: 'validate', message: text.reasonError }
    if (values.action === 'replace') {
      if (source.replacement.kind === 'none' || values.replacement_item_id !== source.replacement.item.id) errors.replacement_item_id = { type: 'validate', message: text.targetError }
      else {
        const result = await purchaseLineResolver({ ...source.replacement, purchase: source.purchase })({ allocation: values.allocation, item_unit_id: values.item_unit_id,
          quantity_base: values.quantity_base, paid_minor: values.paid_minor, note: values.note }, undefined, { fields: {}, shouldUseNativeValidation: false })
        Object.assign(errors, result.errors)
      }
    }
    if (Object.keys(errors).length) return { values: {}, errors }
    try { purchaseCorrectionProposal(values, source) } catch { return { values: {}, errors: { action: { type: 'validate', message: text.actionError } } } }
    return { values, errors: {} }
  }
}
