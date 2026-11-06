import type { FieldErrors, Resolver } from 'react-hook-form'
import { purchaseCreateSchema } from '@/lib/api/purchases'
import { parseSafeCount } from '@/lib/api/domain'
import { validText } from '@/lib/api/client'
import { messages } from '@/lib/messages'
import type { PurchaseValues, PurchaseLineValues } from './form'
import type { PurchaseAllocationContext } from './model'

const text = messages.purchases
export const headerErrors = { merchant_name: text.merchantError, purchased_at: text.instantError, currency: text.currencyError, receipt_note: text.textError }
export const lineErrors = { allocation: text.allocationError, item_unit_id: text.unitError, quantity_base: text.quantityError, paid_minor: text.moneyError, note: text.textError }
export const purchaseHeaderResolver: Resolver<PurchaseValues> = (values) => {
  const parsed = purchaseCreateSchema.safeParse(values)
  if (parsed.success) return { values, errors: {} }
  const errors: FieldErrors<PurchaseValues> = {}
  for (const issue of parsed.error.issues) {
    const key = issue.path[0] as keyof PurchaseValues
    if (Object.hasOwn(headerErrors, key)) errors[key] = { type: 'validate', message: headerErrors[key] }
  }
  return { values: {}, errors }
}
export function purchaseLineResolver(source: PurchaseAllocationContext): Resolver<PurchaseLineValues> {
  return (values) => {
    const quantity = parseSafeCount(values.quantity_base, 1)
    const checks: Record<keyof PurchaseLineValues, boolean> = {
      allocation: values.allocation === 'none' || values.allocation === 'unit' && source.kind === 'individual',
      item_unit_id: values.allocation !== 'unit' || source.kind === 'individual' && source.units.some((unit) => unit.id === values.item_unit_id && unit.item_id === source.item.id),
      quantity_base: quantity !== undefined && (values.allocation !== 'unit' || quantity === 1),
      paid_minor: parseSafeCount(values.paid_minor) !== undefined,
      note: validText(values.note, 0, 10000),
    }
    const errors: FieldErrors<PurchaseLineValues> = {}
    for (const key of Object.keys(checks) as (keyof PurchaseLineValues)[]) if (!checks[key]) errors[key] = { type: 'validate', message: lineErrors[key] }
    return Object.keys(errors).length ? { values: {}, errors } : { values, errors: {} }
  }
}
