import { z } from 'zod'
import { ApiError } from '@/lib/api/client'
import { input, parseSafeCount } from '@/lib/api/domain'
import * as api from '@/lib/api/holdings'
import type { components } from '@/lib/api/schema'

const metadataValues = z.strictObject({ serial_provided: z.boolean(), serial_number: z.string(), notes: z.string() })
const creationValues = metadataValues.extend({ location_id: z.string(), status: z.string() })
const lifecycleValues = z.strictObject({ status: z.string(), disposal_kind: z.string(), note: z.string() })
const stockValues = z.strictObject({ source_location_id: z.string(), target_location_id: z.string(), quantity_base: z.string(), reason: z.string(), note: z.string() })
const stockAction = z.enum(['receive', 'consume', 'transfer', 'adjust'])
export type UnitMetadataValues = z.infer<typeof metadataValues>
export type UnitCreateValues = z.infer<typeof creationValues>
export type UnitStatusValues = z.infer<typeof lifecycleValues>
export type StockValues = z.infer<typeof stockValues>
export type StockAction = z.infer<typeof stockAction>
type Schemas = components['schemas']
export type StockChange = Readonly<{ kind: 'receive'; body: Schemas['StockReceiveInput'] }>
  | Readonly<{ kind: 'consume'; body: Schemas['StockConsumeInput'] }>
  | Readonly<{ kind: 'transfer'; body: Schemas['StockTransferInput'] }>
  | Readonly<{ kind: 'adjust'; body: Schemas['StockAdjustInput'] }>

export function unitMetadataDefaults(unit?: api.Unit): UnitMetadataValues {
  return { serial_provided: unit?.serial_number != null, serial_number: unit?.serial_number ?? '', notes: unit?.notes ?? '' }
}
export function unitCreateDefaults(): UnitCreateValues {
  return { ...unitMetadataDefaults(), location_id: '', status: 'stored' }
}
function unitMetadata(values: UnitMetadataValues) {
  return { serial_number: values.serial_provided ? values.serial_number : null, notes: values.notes }
}
export function unitCreateBody(values: UnitCreateValues): Schemas['UnitCreate'] {
  const checked = input(creationValues, values)
  return input(api.unitCreateSchema, { ...unitMetadata(checked), location_id: checked.location_id || null, status: checked.status })
}
export function unitPatch(values: UnitMetadataValues, original: api.Unit): api.UnitPatchInput | undefined {
  const checked = input(api.unitPatchSchema, unitMetadata(input(metadataValues, values)))
  const patch = { ...(checked.serial_number !== original.serial_number ? { serial_number: checked.serial_number } : {}),
    ...(checked.notes !== original.notes ? { notes: checked.notes } : {}) }
  return Object.keys(patch).length ? input(api.unitPatchSchema, patch) : undefined
}
export function unitMoveBody(targetLocationID: string): Schemas['UnitMove'] {
  return input(api.unitMoveSchema, { target_location_id: targetLocationID })
}
export function unitStatusDefaults(unit: api.Unit): UnitStatusValues {
  // Disposal kind is history, not a field on Unit. Never guess a prior action.
  return { status: unit.status, disposal_kind: '', note: '' }
}
export function unitStatusBody(values: UnitStatusValues): Schemas['UnitStatusInput'] {
  const checked = input(lifecycleValues, values)
  return input(api.unitStatusSchema, { status: checked.status, note: checked.note, ...(checked.status === 'disposed' ? { disposal_kind: checked.disposal_kind } : {}) })
}

export function retainUnitEdits(values: UnitMetadataValues, original: api.Unit, current: api.Unit): UnitMetadataValues {
  if (original.id !== current.id || original.item_id !== current.item_id) throw new ApiError('VALIDATION_ERROR')
  const before = unitMetadataDefaults(original), next = unitMetadataDefaults(current)
  if (values.serial_provided !== before.serial_provided || values.serial_provided && values.serial_number !== before.serial_number) {
    next.serial_provided = values.serial_provided; next.serial_number = values.serial_number
  }
  if (values.notes !== before.notes) next.notes = values.notes
  return next
}

export function stockDefaults(kind: StockAction, balance?: api.Stock): StockValues {
  return { source_location_id: kind === 'consume' || kind === 'transfer' ? balance?.location_id ?? '' : '',
    target_location_id: kind === 'receive' || kind === 'adjust' ? balance?.location_id ?? '' : '',
    quantity_base: kind === 'adjust' && balance ? String(balance.quantity_base) : '', reason: '', note: '' }
}

// Amounts are exact base counts under the reviewed Item interpretation, never
// display-unit floats. Keep every raw field intact through conflict/refetch.
export function stockBody(kind: StockAction, values: StockValues, itemID: string): StockChange {
  const action = input(stockAction, kind), checked = input(stockValues, values)
  const common = { item_id: itemID, quantity_base: parseSafeCount(checked.quantity_base, action === 'adjust' ? 0 : 1) ?? NaN, note: checked.note }
  switch (action) {
    case 'receive': return { kind: action, body: input(api.stockReceiveSchema, { ...common, target_location_id: checked.target_location_id }) }
    case 'consume': return { kind: action, body: input(api.stockConsumeSchema, { ...common, source_location_id: checked.source_location_id }) }
    case 'transfer': return { kind: action, body: input(api.stockTransferSchema, { ...common, source_location_id: checked.source_location_id, target_location_id: checked.target_location_id }) }
    case 'adjust': return { kind: action, body: input(api.stockAdjustSchema, { ...common, target_location_id: checked.target_location_id, reason: checked.reason }) }
  }
}
