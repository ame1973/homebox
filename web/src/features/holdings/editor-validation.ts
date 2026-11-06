import type { FieldErrors, Resolver } from 'react-hook-form'
import { parseSafeCount } from '@/lib/api/domain'
import * as api from '@/lib/api/holdings'
import { messages } from '@/lib/messages'
import type { StockAction, StockValues, UnitCreateValues, UnitStatusValues } from './form'

export type UnitEditorMode = 'create' | 'edit' | 'move' | 'status'
export type UnitEditorValues = UnitCreateValues & UnitStatusValues & { target_location_id: string }
const text = messages.holdings
const unitErrors = { serial_number: text.serialError, notes: text.textError, location_id: text.locationError, target_location_id: text.locationError, status: text.statusError, disposal_kind: text.kindError, note: text.textError }

// Compose localized field placement around the SDK's actual limits and unions;
// the tested form helpers still construct the final outgoing command bodies.
export function unitFormResolver(mode: UnitEditorMode): Resolver<UnitEditorValues> {
  return (values) => {
    const metadata = { serial_number: values.serial_provided ? values.serial_number : null, notes: values.notes }
    const result = mode === 'create' ? api.unitCreateSchema.safeParse({ ...metadata, location_id: values.location_id || null, status: values.status })
      : mode === 'edit' ? api.unitPatchSchema.safeParse(metadata) : mode === 'move' ? api.unitMoveSchema.safeParse({ target_location_id: values.target_location_id })
        : api.unitStatusSchema.safeParse({ status: values.status, note: values.note, ...(values.status === 'disposed' ? { disposal_kind: values.disposal_kind } : {}) })
    if (result.success) return { values, errors: {} }
    const errors: FieldErrors<UnitEditorValues> = {}
    for (const issue of result.error.issues) {
      const fallback = mode === 'move' ? 'target_location_id' : mode === 'status' ? values.status === 'disposed' ? 'disposal_kind' : 'status' : mode === 'create' ? 'location_id' : 'serial_number'
      const key = typeof issue.path[0] === 'string' && Object.hasOwn(unitErrors, issue.path[0]) ? issue.path[0] as keyof typeof unitErrors : fallback
      errors[key] = { type: 'validate', message: unitErrors[key] }
    }
    return { values: {}, errors }
  }
}
export function stockFormResolver(action: StockAction, itemID: string): Resolver<StockValues> {
  return (values) => {
    const common = { item_id: itemID, quantity_base: parseSafeCount(values.quantity_base, action === 'adjust' ? 0 : 1) ?? NaN, note: values.note }
    const result = action === 'receive' ? api.stockReceiveSchema.safeParse({ ...common, target_location_id: values.target_location_id })
      : action === 'consume' ? api.stockConsumeSchema.safeParse({ ...common, source_location_id: values.source_location_id })
        : action === 'transfer' ? api.stockTransferSchema.safeParse({ ...common, source_location_id: values.source_location_id, target_location_id: values.target_location_id })
          : api.stockAdjustSchema.safeParse({ ...common, target_location_id: values.target_location_id, reason: values.reason })
    if (result.success) return { values, errors: {} }
    const labels = { quantity_base: action === 'adjust' ? text.absoluteError : text.quantityError, source_location_id: text.locationError, target_location_id: text.locationError, note: text.textError, reason: text.reasonError }
    const errors: FieldErrors<StockValues> = {}
    for (const issue of result.error.issues) {
      const key = typeof issue.path[0] === 'string' && Object.hasOwn(labels, issue.path[0]) ? issue.path[0] as keyof typeof labels : 'target_location_id'
      errors[key] = { type: 'validate', message: labels[key] }
    }
    return { values: {}, errors }
  }
}
