import { z } from 'zod'
import { ApiError } from '@/lib/api/client'
import { itemSchema } from '@/lib/api/catalog'
import { idSchema, input, pathIDSchema } from '@/lib/api/domain'
import { purchaseCorrectionSchema, purchaseCorrectionWriteSchema, samePurchaseLine, type PurchaseLineCorrectionWrite } from '@/lib/api/financial-corrections'
import { unitSchema, type Unit } from '@/lib/api/holdings'
import { purchaseLineSchema, purchaseSchema } from '@/lib/api/purchases'
import { coherentPurchaseCorrectionHistory, type PurchaseCorrectionSource } from './correction-model'
import { purchaseLineProposal } from './form'

export const purchaseCorrectionValues = z.strictObject({ action: z.string(), reason: z.string(), replacement_item_id: z.string(), allocation: z.string(),
  item_unit_id: z.string(), quantity_base: z.string(), paid_minor: z.string(), note: z.string() })
export type PurchaseCorrectionValues = z.infer<typeof purchaseCorrectionValues>
export type PurchaseCorrectionProposal = Readonly<{ source: PurchaseCorrectionSource; body: Extract<PurchaseLineCorrectionWrite, { action: 'void' }>; unit: null }>
  | Readonly<{ source: PurchaseCorrectionSource; body: Extract<PurchaseLineCorrectionWrite, { action: 'replace' }>; unit: Unit | null }>

const originalUnit = z.discriminatedUnion('kind', [z.strictObject({ kind: z.literal('unallocated') }),
  z.strictObject({ kind: z.literal('unaddressable'), id: idSchema }), z.strictObject({ kind: z.literal('resolved'), unit: unitSchema })])
const replacement = z.discriminatedUnion('kind', [z.strictObject({ kind: z.literal('none') }),
  z.strictObject({ kind: z.literal('quantity'), item: itemSchema }), z.strictObject({ kind: z.literal('individual'), item: itemSchema, units: z.array(unitSchema) })])
const sourceSchema: z.ZodType<PurchaseCorrectionSource> = z.strictObject({ purchase: purchaseSchema, lines: z.array(purchaseLineSchema),
  corrections: z.array(purchaseCorrectionSchema), line: purchaseLineSchema, item: itemSchema, originalUnit, replacement })

export function reviewedPurchaseCorrectionSource(source: unknown): PurchaseCorrectionSource {
  const checked = input(sourceSchema, source), { purchase, line, item, originalUnit, replacement } = checked
  input(pathIDSchema, purchase.id); input(pathIDSchema, item.id)
  const actual = checked.lines.find((candidate) => candidate.id === line.id)
  if (line.purchase_id !== purchase.id || line.item_id !== item.id || !actual || !samePurchaseLine(actual, line) ||
    !coherentPurchaseCorrectionHistory(purchase.id, checked.lines, checked.corrections)) throw new ApiError('VALIDATION_ERROR')
  if (originalUnit.kind === 'resolved') input(pathIDSchema, originalUnit.unit.id)
  if (originalUnit.kind === 'unallocated' ? line.item_unit_id !== null : originalUnit.kind === 'unaddressable'
    ? line.item_unit_id !== originalUnit.id || pathIDSchema.safeParse(originalUnit.id).success
    : line.item_unit_id !== originalUnit.unit.id || originalUnit.unit.item_id !== item.id) throw new ApiError('VALIDATION_ERROR')
  if (replacement.kind !== 'none') {
    input(pathIDSchema, replacement.item.id)
    if (replacement.kind !== replacement.item.tracking_mode || replacement.item.id === item.id && JSON.stringify(replacement.item) !== JSON.stringify(item)) throw new ApiError('VALIDATION_ERROR')
    if (replacement.kind === 'individual') {
      if (new Set(replacement.units.map((unit) => unit.id)).size !== replacement.units.length || replacement.units.some((unit) => unit.item_id !== replacement.item.id)) throw new ApiError('VALIDATION_ERROR')
      if (replacement.item.id === item.id && originalUnit.kind === 'resolved') {
        const unit = replacement.units.find((candidate) => candidate.id === originalUnit.unit.id)
        if (!unit || JSON.stringify(unit) !== JSON.stringify(originalUnit.unit)) throw new ApiError('VALIDATION_ERROR')
      }
    }
  }
  return checked
}

export function purchaseCorrectionDefaults(): PurchaseCorrectionValues {
  return { action: '', reason: '', replacement_item_id: '', allocation: '', item_unit_id: '', quantity_base: '', paid_minor: '', note: '' }
}

// A replacement is a whole new attribution, never a delta or physical receipt.
// Hidden raw fields survive a void choice but do not enter its wire body.
export function purchaseCorrectionProposal(values: PurchaseCorrectionValues, source: PurchaseCorrectionSource): PurchaseCorrectionProposal {
  const checked = input(purchaseCorrectionValues, values), original = reviewedPurchaseCorrectionSource(source)
  const fields = { line_id: original.line.id, line_revision: original.line.revision, item_revision: original.item.revision, reason: checked.reason }
  if (checked.action === 'void') {
    const body = { ...fields, action: 'void' as const, replacement: null }
    input(purchaseCorrectionWriteSchema, body)
    return { source: original, body, unit: null }
  }
  if (checked.action !== 'replace' || original.replacement.kind === 'none' || checked.replacement_item_id !== original.replacement.item.id) throw new ApiError('VALIDATION_ERROR')
  const allocation = purchaseLineProposal({ allocation: checked.allocation, item_unit_id: checked.item_unit_id, quantity_base: checked.quantity_base,
    paid_minor: checked.paid_minor, note: checked.note }, { ...original.replacement, purchase: original.purchase })
  const body = { ...fields, action: 'replace' as const, replacement: allocation.body }
  input(purchaseCorrectionWriteSchema, body)
  return { source: original, body, unit: allocation.unit }
}

// Only explicit review adopts a new Purchase/currency and replacement Item
// interpretation. Never convert counts/money or erase an unavailable Unit choice.
export function adoptPurchaseCorrection(values: PurchaseCorrectionValues, before: PurchaseCorrectionSource, current: PurchaseCorrectionSource) {
  const previous = reviewedPurchaseCorrectionSource(before), next = reviewedPurchaseCorrectionSource(current)
  if (previous.purchase.id !== next.purchase.id || previous.item.id !== next.item.id || !samePurchaseLine(previous.line, next.line)) throw new ApiError('VALIDATION_ERROR')
  return { values: input(purchaseCorrectionValues, values), source: next }
}
