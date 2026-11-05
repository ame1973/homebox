import { expect, it } from 'vitest'
import type { Item } from '@/lib/api/catalog'
import { catalogDefaults, catalogFormSchema, catalogPatch, metadataFromValues, parseScaleFactor, retainCatalogEdits } from './form'

const item: Item = { id: 'item', revision: 7, name: '  Original é  ', brand: null, model: '', category_id: 'category', description: '', notes: '  Original note  ',
  tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1, status: 'active' }

it('defaults only absent fields and preserves null versus a supplied empty brand/model', () => {
  expect(catalogDefaults()).toEqual({ name: '', brand_provided: false, brand: '', model_provided: false, model: '', category_id: '', description: '', notes: '',
    tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: '1' })
  expect(catalogDefaults(item)).toEqual({ name: item.name, brand_provided: false, brand: '', model_provided: true, model: '', category_id: 'category', description: '', notes: item.notes,
    tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: '1' })
  expect(catalogDefaults({ ...item, brand: '  品牌 é  ', model: null, category_id: null, tracking_mode: 'quantity', unit_code: 'kg', base_unit: 'g', scale_factor: 1000 }))
    .toMatchObject({ brand_provided: true, brand: '  品牌 é  ', model_provided: false, model: '', category_id: '', tracking_mode: 'quantity', unit_code: 'kg', base_unit: 'g', scale_factor: '1000' })
})

it('maps valid exact original metadata without normalizing or adding status/holdings', () => {
  const values = { ...catalogDefaults(item), name: '  濾芯 é  ', brand_provided: true, brand: '', model_provided: false, category_id: '', notes: '  說明 😀  ' }
  expect(metadataFromValues(values)).toEqual({ name: values.name, brand: '', model: null, category_id: null, description: '', notes: values.notes,
    tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1 })
  const limits = { ...values, name: '😀'.repeat(300), brand: '😀'.repeat(300), model_provided: true, model: '😀'.repeat(300), category_id: '😀'.repeat(128),
    description: '😀'.repeat(10000), notes: '😀'.repeat(10000), unit_code: '😀'.repeat(32), base_unit: 'g' + '_'.repeat(31), scale_factor: String(Number.MAX_SAFE_INTEGER) }
  expect(catalogFormSchema.parse(limits)).toEqual(limits)
  expect(metadataFromValues(limits)).toMatchObject({ tracking_mode: 'individual', scale_factor: Number.MAX_SAFE_INTEGER, brand: limits.brand, model: limits.model })
})

it('never publishes inactive nullable-text drafts', () => {
  const values = { ...catalogDefaults(item), brand_provided: false, brand: '\uD800', model_provided: false, model: 'x'.repeat(301) }
  expect(catalogFormSchema.safeParse(values).success).toBe(true)
  expect(metadataFromValues(values)).toMatchObject({ brand: null, model: null })
})

it.each([
  ['name', ''], ['name', '\uD800'], ['name', '😀'.repeat(301)],
  ['brand', '\uD800'], ['brand', '😀'.repeat(301)], ['model', '\uD800'], ['model', 'x'.repeat(301)],
  ['category_id', '\uD800'], ['category_id', '😀'.repeat(129)], ['description', '\uD800'], ['description', 'x'.repeat(10001)], ['notes', 'x'.repeat(10001)],
  ['unit_code', ''], ['unit_code', '\uD800'], ['unit_code', '😀'.repeat(33)], ['base_unit', ''], ['base_unit', '1g'], ['base_unit', 'g\n'], ['base_unit', '克'], ['base_unit', 'g'.repeat(33)],
  ['scale_factor', ''], ['scale_factor', '0'], ['scale_factor', '-1'], ['scale_factor', '+1'], ['scale_factor', '01'], ['scale_factor', '1.0'], ['scale_factor', '1e3'],
  ['scale_factor', ' 1'], ['scale_factor', '1 '], ['scale_factor', '1\n'], ['scale_factor', '1,000'], ['scale_factor', '9007199254740992'], ['scale_factor', '99999999999999999'],
])('rejects invalid %s input %j at the corresponding field', (key, value) => {
  const result = catalogFormSchema.safeParse({ ...catalogDefaults(item), brand_provided: true, model_provided: true, [key]: value })
  expect(result.success).toBe(false)
  if (!result.success) {
    expect(result.error.issues[0]?.path).toEqual([key])
    expect(result.error.issues[0]?.message).toBeTruthy()
  }
})

it('rejects unknown/internal form fields, invalid mode and unvalidated scale conversion', () => {
  for (const extra of [{ quantity_base: 1 }, { status: 'archived' }, { brand: { set: true, value: null } }, { tracking_mode: 'mixed' }]) {
    expect(catalogFormSchema.safeParse({ ...catalogDefaults(item), ...extra }).success).toBe(false)
  }
  expect(() => metadataFromValues({ ...catalogDefaults(item), scale_factor: '1.5' })).toThrow(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
})

it('parses only canonical bounded positive decimal integer text', () => {
  for (const number of [1, 1000, Number.MAX_SAFE_INTEGER]) expect(parseScaleFactor(String(number))).toBe(number)
  for (const value of ['0', '-0', '-1', '+1', '01', '1.0', '1e3', '1\r\n', '1\n', '1 ', '1 ', ' 1', '1 ', '9007199254740992', '99999999999999999']) expect(parseScaleFactor(value)).toBeUndefined()
})

it('patches only actual changed metadata including explicit null/empty, never status or inventory', () => {
  const before = catalogDefaults(item)
  expect(catalogPatch(before, item)).toBeUndefined()
  expect(catalogPatch(catalogDefaults({ ...item, status: 'archived' }), { ...item, status: 'archived' })).toBeUndefined()
  expect(catalogPatch({ ...before, name: 'Renamed' }, item)).toEqual({ name: 'Renamed' })
  expect(catalogPatch({ ...before, brand_provided: true, brand: '' }, item)).toEqual({ brand: '' })
  expect(catalogPatch({ ...before, model_provided: false, model: 'Ignored' }, item)).toEqual({ model: null })
  expect(catalogPatch({ ...before, category_id: '' }, item)).toEqual({ category_id: null })
  expect(catalogPatch({ ...before, notes: '' }, item)).toEqual({ notes: '' })
  expect(catalogPatch({ ...before, tracking_mode: 'quantity', unit_code: 'kg', base_unit: 'g', scale_factor: '1000' }, item))
    .toEqual({ tracking_mode: 'quantity', unit_code: 'kg', base_unit: 'g', scale_factor: 1000 })
  expect(catalogPatch({ ...before, description: '😀', brand_provided: true, brand: ' Brand ', model: 'Model', category_id: 'next' }, item))
    .toEqual({ description: '😀', brand: ' Brand ', model: 'Model', category_id: 'next' })
  expect(catalogPatch({ ...before, name: item.name.normalize('NFC') }, item)).toEqual({ name: item.name.normalize('NFC') })
})

it('explicitly adopts untouched current values without overwriting changed or invalid text', () => {
  const current: Item = { ...item, revision: 8, name: 'Concurrent name', brand: 'Concurrent brand', model: 'Concurrent model', category_id: null, description: 'Concurrent description', notes: 'Concurrent notes',
    tracking_mode: 'quantity', unit_code: 'kg', base_unit: 'g', scale_factor: 1000 }
  expect(retainCatalogEdits(catalogDefaults(item), item, current)).toEqual(catalogDefaults(current))
  expect(retainCatalogEdits({ ...catalogDefaults(item), name: '', description: ' My description ', notes: '' }, item, current))
    .toEqual({ ...catalogDefaults(current), name: '', description: ' My description ', notes: '' })
  expect(retainCatalogEdits({ ...catalogDefaults(item), category_id: 'Selected category' }, item, current).category_id).toBe('Selected category')
})

it('adopts nullable text as logical value/provided pairs rather than merging null and empty', () => {
  const current = { ...item, revision: 8, brand: 'Concurrent brand', model: 'Concurrent model' }
  const values = { ...catalogDefaults(item), brand_provided: true, brand: '', model_provided: false, model: 'Hidden draft' }
  expect(retainCatalogEdits(values, item, current)).toEqual({ ...catalogDefaults(current), brand_provided: true, brand: '', model_provided: false, model: 'Hidden draft' })
  expect(retainCatalogEdits({ ...catalogDefaults(item), brand: 'Ignored hidden draft' }, item, current)).toEqual(catalogDefaults(current))
  expect(retainCatalogEdits({ ...catalogDefaults(item), model: '\uD800' }, item, current).model).toBe('\uD800')
})

it.each([{ unit_code: 'box' }, { scale_factor: '1e3' }, { tracking_mode: 'quantity' as const }, { base_unit: 'g' }])('keeps a changed or invalid interpretation together: %j', (changed) => {
  const current: Item = { ...item, revision: 8, tracking_mode: 'quantity', unit_code: 'kg', base_unit: 'g', scale_factor: 1000 }
  const values = { ...catalogDefaults(item), ...changed }
  expect(retainCatalogEdits(values, item, current)).toEqual({ ...catalogDefaults(current), tracking_mode: values.tracking_mode,
    unit_code: values.unit_code, base_unit: values.base_unit, scale_factor: values.scale_factor })
})
