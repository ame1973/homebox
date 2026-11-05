import { expect, it } from 'vitest'
import { aliasBody, aliasDefaults, aliasPatch, attributeBody, attributeDefaults, attributePatch, barcodeBody, barcodeDefaults, barcodePatch,
  retainAliasEdits, retainAttributeEdits, retainBarcodeEdits, retainTagEdits } from './owned-form'

const barcode = { id: 'barcode', item_id: 'item', revision: 2, symbology: 'UPC_A', raw_value: '036000291452', normalized_value: '036000291452' } as const
const alias = { id: 'alias', item_id: 'item', revision: 2, alias: '  Exact é  ' } as const
const attribute = { id: 'attribute', item_id: 'item', revision: 2, attribute_key: 'OriginalKey', attribute_value: 'OriginalValue' } as const

it('keeps exact source defaults and requires an explicit new barcode namespace', () => {
  expect(barcodeDefaults()).toEqual({ symbology: '', raw_value: '' })
  expect(aliasDefaults()).toEqual({ alias: '' })
  expect(attributeDefaults()).toEqual({ attribute_key: '', attribute_value: '' })
  expect(barcodeDefaults(barcode)).toEqual({ symbology: barcode.symbology, raw_value: barcode.raw_value })
  expect(aliasDefaults(alias)).toEqual({ alias: alias.alias })
  expect(attributeDefaults(attribute)).toEqual({ attribute_key: attribute.attribute_key, attribute_value: attribute.attribute_value })
  expect(() => barcodeBody(barcodeDefaults())).toThrowError(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
})

it('uses the actual SDK schemas without implicit defaults, trimming, owner fields or nullable empty repair', () => {
  expect(barcodeBody({ symbology: 'CODE_128', raw_value: '\x00GS1\x1d00123' })).toEqual({ symbology: 'CODE_128', raw_value: '\x00GS1\x1d00123' })
  expect(aliasBody({ alias: '  Original é  ' })).toEqual({ alias: '  Original é  ' })
  expect(attributeBody({ attribute_key: 'Key', attribute_value: '' })).toEqual({ attribute_key: 'Key', attribute_value: '' })
  for (const work of [
    () => barcodeBody({ symbology: 'UPC_A', raw_value: 'missing width' }),
    () => aliasBody({ alias: '' }), () => attributeBody({ attribute_key: '', attribute_value: '' }),
    () => attributeBody({ attribute_key: 'Key', attribute_value: null } as never),
  ]) expect(work).toThrowError(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
})

it('emits only changed metadata and treats explicit normalization adoption as a real barcode edit', () => {
  expect(barcodePatch(barcodeDefaults(barcode), barcode)).toBeUndefined()
  expect(aliasPatch(aliasDefaults(alias), alias)).toBeUndefined()
  expect(attributePatch(attributeDefaults(attribute), attribute)).toBeUndefined()
  expect(barcodePatch({ symbology: 'UPC_E', raw_value: '04252614' }, barcode)).toEqual({ symbology: 'UPC_E', raw_value: '04252614' })
  expect(barcodePatch(barcodeDefaults(barcode), { ...barcode, normalized_value: 'older normalization' })).toEqual({ symbology: barcode.symbology, raw_value: barcode.raw_value })
  expect(aliasPatch({ alias: '' + alias.alias + ' ' }, alias)).toEqual({ alias: alias.alias + ' ' })
  expect(attributePatch({ attribute_key: 'NewKey', attribute_value: attribute.attribute_value }, attribute)).toEqual({ attribute_key: 'NewKey' })
  expect(attributePatch({ attribute_key: attribute.attribute_key, attribute_value: '' }, attribute)).toEqual({ attribute_value: '' })
  expect(attributePatch({ attribute_key: 'NewKey', attribute_value: '' }, attribute)).toEqual({ attribute_key: 'NewKey', attribute_value: '' })
})

it('explicitly adopts current values while retaining local barcode interpretation and invalid text as a whole', () => {
  const current = { ...barcode, symbology: 'UPC_E', raw_value: '04252614', normalized_value: '04252614', revision: 3 } as const
  expect(retainBarcodeEdits(barcodeDefaults(barcode), barcode, current)).toEqual(barcodeDefaults(current))
  const local = { symbology: barcode.symbology, raw_value: 'still invalid text' }
  expect(retainBarcodeEdits(local, barcode, current)).toEqual(local)
  const changedNamespace = { symbology: 'QR_CODE', raw_value: barcode.raw_value }
  expect(retainBarcodeEdits(changedNamespace, barcode, current)).toEqual(changedNamespace)
})

it('retains only explicitly edited alias and attribute logical fields during current-state adoption', () => {
  const currentAlias = { ...alias, alias: 'CurrentAlias', revision: 3 }
  expect(retainAliasEdits(aliasDefaults(alias), alias, currentAlias)).toEqual({ alias: 'CurrentAlias' })
  expect(retainAliasEdits({ alias: '' }, alias, currentAlias)).toEqual({ alias: '' })
  const current = { ...attribute, attribute_key: 'CurrentKey', attribute_value: 'CurrentValue', revision: 3 }
  expect(retainAttributeEdits(attributeDefaults(attribute), attribute, current)).toEqual(attributeDefaults(current))
  expect(retainAttributeEdits({ attribute_key: 'LocalKey', attribute_value: attribute.attribute_value }, attribute, current)).toEqual({ attribute_key: 'LocalKey', attribute_value: 'CurrentValue' })
  expect(retainAttributeEdits({ attribute_key: attribute.attribute_key, attribute_value: '' }, attribute, current)).toEqual({ attribute_key: 'CurrentKey', attribute_value: '' })
})

it.each(['identity', 'owner'])('never adopts %s from a different independent row', (field) => {
  const patch = field === 'identity' ? { id: 'another-row' } : { item_id: 'another-owner' }
  for (const work of [
    () => retainBarcodeEdits(barcodeDefaults(barcode), barcode, { ...barcode, ...patch }),
    () => retainAliasEdits(aliasDefaults(alias), alias, { ...alias, ...patch }),
    () => retainAttributeEdits(attributeDefaults(attribute), attribute, { ...attribute, ...patch }),
  ]) expect(work).toThrowError(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
})

it('retains local membership deltas only after explicit adoption, preserving concurrent additions and order', () => {
  expect(retainTagEdits(['b'], ['a', 'b'], ['a', 'b', 'c'])).toEqual(['b', 'c'])
  expect(retainTagEdits(['a', 'b'], ['a', 'b'], ['a', 'b', 'c'])).toEqual(['a', 'b', 'c'])
  expect(retainTagEdits(['a', 'b', 'd'], ['a', 'b'], ['b', 'c'])).toEqual(['b', 'c', 'd'])
  expect(retainTagEdits(['a', 'b', 'c'], ['a', 'b'], ['a', 'b', 'c'])).toEqual(['a', 'b', 'c'])
  expect(retainTagEdits([], ['a', 'b'], ['a', 'b', 'c'])).toEqual(['c'])
})

it('preserves invalid membership drafts and never silently caps a larger legacy set', () => {
  for (const invalid of [['same', 'same'], [''], ['\uD800']]) expect(retainTagEdits(invalid, ['a'], ['b'])).toEqual(invalid)
  const legacy = Array.from({ length: 205 }, (_, index) => `legacy-${index}`)
  expect(retainTagEdits(legacy, legacy, [...legacy, 'new'])).toHaveLength(206)
})
