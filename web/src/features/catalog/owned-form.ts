import { ApiError } from '@/lib/api/client'
import { aliasWriteSchema, attributePatchSchema, attributeWriteSchema, barcodeWriteSchema, type Alias, type Attribute, type AttributePatchInput, type Barcode } from '@/lib/api/catalog-links'
import { idSchema, input } from '@/lib/api/domain'
import { tagChanges } from './owned-model'

export type BarcodeValues = { symbology: string; raw_value: string }
export type AliasValues = { alias: string }
export type AttributeValues = { attribute_key: string; attribute_value: string }

export function barcodeDefaults(subject?: Barcode): BarcodeValues {
  return { symbology: subject?.symbology ?? '', raw_value: subject?.raw_value ?? '' }
}
export function aliasDefaults(subject?: Alias): AliasValues {
  return { alias: subject?.alias ?? '' }
}
export function attributeDefaults(subject?: Attribute): AttributeValues {
  return { attribute_key: subject?.attribute_key ?? '', attribute_value: subject?.attribute_value ?? '' }
}

export function barcodeBody(values: BarcodeValues) { return input(barcodeWriteSchema, values) }
export function aliasBody(values: AliasValues) { return input(aliasWriteSchema, values) }
export function attributeBody(values: AttributeValues) { return input(attributeWriteSchema, values) }

export function barcodePatch(values: BarcodeValues, original: Barcode) {
  const body = barcodeBody(values)
  // An explicit valid replacement may adopt current normalization. A different
  // retained derived value is a real edit, not a hidden repair or false no-op.
  return body.symbology !== original.symbology || body.raw_value !== original.raw_value || original.normalized_value !== body.raw_value ? body : undefined
}
export function aliasPatch(values: AliasValues, original: Alias) {
  const body = aliasBody(values)
  return body.alias !== original.alias ? body : undefined
}
export function attributePatch(values: AttributeValues, original: Attribute): AttributePatchInput | undefined {
  const body = attributeBody(values)
  const patch = { ...(body.attribute_key !== original.attribute_key ? { attribute_key: body.attribute_key } : {}),
    ...(body.attribute_value !== original.attribute_value ? { attribute_value: body.attribute_value } : {}) }
  return Object.keys(patch).length ? input(attributePatchSchema, patch) : undefined
}

function requireSameRow(original: { id: string; item_id: string }, current: { id: string; item_id: string }) {
  if (original.id !== current.id || original.item_id !== current.item_id) throw new ApiError('VALIDATION_ERROR')
}

// Explicit adoption treats symbology/raw text as one interpretation; never mix a
// locally edited raw value with a concurrently changed decoding namespace.
export function retainBarcodeEdits(values: BarcodeValues, original: Barcode, current: Barcode): BarcodeValues {
  requireSameRow(original, current)
  return values.symbology !== original.symbology || values.raw_value !== original.raw_value ? { ...values } : barcodeDefaults(current)
}
export function retainAliasEdits(values: AliasValues, original: Alias, current: Alias): AliasValues {
  requireSameRow(original, current)
  return values.alias !== original.alias ? { ...values } : aliasDefaults(current)
}
export function retainAttributeEdits(values: AttributeValues, original: Attribute, current: Attribute): AttributeValues {
  requireSameRow(original, current)
  return { attribute_key: values.attribute_key !== original.attribute_key ? values.attribute_key : current.attribute_key,
    attribute_value: values.attribute_value !== original.attribute_value ? values.attribute_value : current.attribute_value }
}

// Apply only the explicitly retained local additions/removals to a reviewed new
// baseline. Keep invalid drafts intact and never truncate a larger legacy set.
export function retainTagEdits(selected: readonly string[], original: readonly string[], current: readonly string[]): string[] {
  if (new Set(selected).size !== selected.length || selected.some((id) => !idSchema.safeParse(id).success)) return [...selected]
  const changes = tagChanges(original, selected)
  const removed = new Set(changes.removed)
  const next = current.filter((id) => !removed.has(id))
  const present = new Set(next)
  for (const id of changes.added) if (!present.has(id)) { next.push(id); present.add(id) }
  return next
}
