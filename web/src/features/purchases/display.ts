import type { PurchaseCommand } from './commands'
import type { PurchaseAllocationContext, PurchaseDetail } from './model'

export function sameRecord<T extends object>(before: T | undefined, current: T | undefined): boolean {
  if (!before || !current) return before === current
  return Object.keys(before).length === Object.keys(current).length && (Object.keys(before) as (keyof T)[]).every((key) => before[key] === current[key])
}
export function samePurchaseDetail(before: PurchaseDetail, current: PurchaseDetail) {
  return sameRecord(before.purchase, current.purchase) && before.lines.length === current.lines.length && before.lines.every((line, index) => sameRecord(line, current.lines[index]))
}
export const purchaseInterpretation = (source: PurchaseAllocationContext) => JSON.stringify([source.purchase.currency, source.item.tracking_mode, source.item.unit_code, source.item.base_unit, source.item.scale_factor])
export function purchaseCommandCurrent(command: PurchaseCommand, detail?: PurchaseDetail, allocation?: PurchaseAllocationContext) {
  if (command.kind === 'create-purchase') return true
  if (command.kind === 'patch-purchase') return !!detail && samePurchaseDetail(command.source, detail)
  if (!allocation || !sameRecord(command.purchase, allocation.purchase) || !sameRecord(command.item, allocation.item)) return false
  return command.unit === null || allocation.kind === 'individual' && sameRecord(command.unit, allocation.units.find((unit) => unit.id === command.unit!.id))
}
