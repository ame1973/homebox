import { z } from 'zod'
import { ApiError } from '@/lib/api/client'
import { itemSchema } from '@/lib/api/catalog'
import { input, parseSafeCount } from '@/lib/api/domain'
import { unitSchema, type Unit } from '@/lib/api/holdings'
import * as api from '@/lib/api/purchases'
import type { components } from '@/lib/api/schema'
import type { PurchaseAllocation, PurchaseAllocationContext } from './model'

const headerValues = z.strictObject({ merchant_name: z.string(), purchased_at: z.string(), currency: z.string(), receipt_note: z.string() })
const lineValues = z.strictObject({ allocation: z.string(), item_unit_id: z.string(), quantity_base: z.string(), paid_minor: z.string(), note: z.string() })
export type PurchaseValues = z.infer<typeof headerValues>
export type PurchaseLineValues = z.infer<typeof lineValues>

export function purchaseDefaults(purchase?: api.Purchase): PurchaseValues {
  return { merchant_name: purchase?.merchant_name ?? '', purchased_at: purchase?.purchased_at ?? '', currency: purchase?.currency ?? '', receipt_note: purchase?.receipt_note ?? '' }
}
export function purchaseCreateBody(values: PurchaseValues): components['schemas']['PurchaseWrite'] {
  return input(api.purchaseCreateSchema, input(headerValues, values))
}
export function purchasePatch(values: PurchaseValues, original: api.Purchase): api.PurchasePatchInput | undefined {
  const desired = purchaseCreateBody(values)
  const patch: components['schemas']['PurchasePatch'] = {}
  for (const key of Object.keys(desired) as (keyof typeof desired)[]) if (desired[key] !== original[key]) Object.assign(patch, { [key]: desired[key] })
  return Object.keys(patch).length ? input(api.purchasePatchSchema, patch) : undefined
}

// Only an explicit adoption replaces source context. Invalid/in-progress text
// stays verbatim, and equivalent instants are not normalized through Date.
export function retainPurchaseEdits(values: PurchaseValues, original: api.Purchase, current: api.Purchase): PurchaseValues {
  if (original.id !== current.id) throw new ApiError('VALIDATION_ERROR')
  const before = purchaseDefaults(original), next = purchaseDefaults(current)
  for (const key of Object.keys(next) as (keyof PurchaseValues)[]) if (values[key] !== before[key]) next[key] = values[key]
  return next
}

export function purchaseLineDefaults(): PurchaseLineValues {
  // Null allocation, quantity and even zero paid money are explicit decisions.
  // Do not infer them from an Item, existing Stock or an earlier allocation.
  return { allocation: '', item_unit_id: '', quantity_base: '', paid_minor: '', note: '' }
}
export function purchaseLineProposal(values: PurchaseLineValues, source: PurchaseAllocationContext): PurchaseAllocation {
  const checked = input(lineValues, values)
  const purchase = input(api.purchaseSchema, source.purchase), item = input(itemSchema, source.item)
  if ((source.kind !== 'individual' && source.kind !== 'quantity') || source.kind !== item.tracking_mode || !['none', 'unit'].includes(checked.allocation)) throw new ApiError('VALIDATION_ERROR')
  let unit: Unit | null = null
  if (checked.allocation === 'unit') {
    if (source.kind !== 'individual') throw new ApiError('TRACKING_MODE_CONFLICT')
    if (!Array.isArray(source.units)) throw new ApiError('VALIDATION_ERROR')
    unit = input(unitSchema, source.units.find((candidate) => candidate.id === checked.item_unit_id))
    if (unit.item_id !== item.id) throw new ApiError('VALIDATION_ERROR')
  }
  const body = input(api.purchaseLineCreateSchema, { item_id: item.id, item_revision: item.revision, item_unit_id: unit?.id ?? null,
    quantity_base: parseSafeCount(checked.quantity_base, 1) ?? NaN, paid_minor: parseSafeCount(checked.paid_minor) ?? NaN, note: checked.note })
  return { purchase, item, unit, body }
}

// Currency plus the entire Item interpretation changes as one reviewed context.
// Retain raw amount/count/Unit-choice text, including unavailable/invalid edits;
// explicit adoption never converts money, measurement or a missing Unit to null.
export function adoptPurchaseAllocation(values: PurchaseLineValues, original: PurchaseAllocationContext, current: PurchaseAllocationContext) {
  if (original.purchase.id !== current.purchase.id || original.item.id !== current.item.id) throw new ApiError('VALIDATION_ERROR')
  return { values: { ...values }, source: structuredClone(current) }
}
