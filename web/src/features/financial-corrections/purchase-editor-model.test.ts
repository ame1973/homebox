import { expect, it } from 'vitest'
import type { Item } from '@/lib/api/catalog'
import type { PurchaseLineCorrection } from '@/lib/api/financial-corrections'
import type { Unit } from '@/lib/api/holdings'
import type { PurchaseLine } from '@/lib/api/purchases'
import { purchaseCorrectionDefaults } from '@/features/purchases/correction-form'
import type { PurchaseCorrectionSource } from '@/features/purchases/correction-model'
import { correctionInterpretation, purchaseCorrectionResolver, samePurchaseCorrectionSource } from './purchase-editor-model'

const item: Item = { id: 'old', revision: 7, name: 'Original', tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1, brand: null, model: '', category_id: null, description: '', notes: '', status: 'active' }
const target: Item = { ...item, id: 'target', revision: 9, tracking_mode: 'quantity', unit_code: 'kg', base_unit: 'g', scale_factor: 1000 }
const unit: Unit = { id: 'unit', item_id: item.id, revision: 3, status: 'disposed', asset_code: 'Original', location_id: null, serial_number: null, notes: '' }
const purchase = { id: 'purchase', revision: 12, merchant_name: 'Merchant', purchased_at: '2026-11-07T00:00:00.000000001Z', currency: 'JPY', receipt_note: '' }
const line: PurchaseLine = { id: 'line/legacy', revision: 4, purchase_id: purchase.id, item_id: item.id, item_unit_id: unit.id, quantity_base: 3, paid_minor: 0, note: 'Original' }
const source: PurchaseCorrectionSource = { purchase, line, item, lines: [line], corrections: [], originalUnit: { kind: 'resolved', unit }, replacement: { kind: 'none' } }
const measured: PurchaseCorrectionSource = { ...source, replacement: { kind: 'quantity', item: target } }
const targetUnit: Unit = { ...unit, id: 'target-unit', item_id: target.id }
const individual: PurchaseCorrectionSource = { ...source, replacement: { kind: 'individual', item: { ...target, tracking_mode: 'individual' }, units: [targetUnit] } }
const resolve = (value: PurchaseCorrectionSource, values = purchaseCorrectionDefaults()) => purchaseCorrectionResolver(value)(values, undefined, { fields: {}, shouldUseNativeValidation: false })

it('compares complete original and selected context semantically, not object key order', () => {
  expect(samePurchaseCorrectionSource(undefined, undefined)).toBe(true); expect(samePurchaseCorrectionSource(source, null)).toBe(false)
  const reordered = { ...source, purchase: Object.fromEntries(Object.entries(purchase).reverse()) as typeof purchase }
  expect(samePurchaseCorrectionSource(source, reordered)).toBe(true); expect(samePurchaseCorrectionSource(measured, structuredClone(measured))).toBe(true)
  expect(samePurchaseCorrectionSource(source, { ...source, purchase: { ...purchase, revision: 13 } })).toBe(false)
  expect(samePurchaseCorrectionSource(source, { ...source, line: { ...line, note: 'different' } })).toBe(false)
  expect(samePurchaseCorrectionSource(source, { ...source, originalUnit: { kind: 'unallocated' } })).toBe(false)
  expect(samePurchaseCorrectionSource({ ...source, originalUnit: { kind: 'unaddressable', id: 'legacy/unit' } }, { ...source, originalUnit: { kind: 'unaddressable', id: 'legacy/unit' } })).toBe(true)
  expect(samePurchaseCorrectionSource(measured, { ...measured, replacement: { kind: 'quantity', item: { ...target, revision: 10 } } })).toBe(false)
  expect(samePurchaseCorrectionSource(individual, { ...individual, replacement: { kind: 'individual', item: { ...target, tracking_mode: 'individual' }, units: [] } })).toBe(false)
  expect(samePurchaseCorrectionSource(source, { ...source, lines: [] })).toBe(false)
})
it('compares the full immutable before/after chain without assuming property order', () => {
  const before = { ...line, id: 'prior' }, entry: PurchaseLineCorrection = { id: 'event', revision: 1, purchase_id: purchase.id, action: 'replace', reason: 'Reason', created_at: purchase.purchased_at, before, after: { ...line, revision: 1 } }
  const withHistory = { ...source, corrections: [entry] }
  expect(samePurchaseCorrectionSource(withHistory, structuredClone(withHistory))).toBe(true)
  expect(samePurchaseCorrectionSource(withHistory, { ...withHistory, corrections: [{ ...entry, reason: 'Changed' }] })).toBe(false)
  expect(samePurchaseCorrectionSource(withHistory, { ...withHistory, corrections: [{ ...entry, after: { ...entry.after!, note: 'Changed' } }] })).toBe(false)
})
it('treats both Item measurements and currency as one interpretation without treating notes as conversion', () => {
  expect(correctionInterpretation(measured)).toBe(correctionInterpretation({ ...measured, item: { ...item, notes: 'Changed' } }))
  expect(correctionInterpretation(measured)).not.toBe(correctionInterpretation({ ...measured, purchase: { ...purchase, currency: 'USD' } }))
  expect(correctionInterpretation(source)).not.toBe(correctionInterpretation(measured))
  expect(correctionInterpretation(measured)).not.toBe(correctionInterpretation({ ...measured, replacement: { kind: 'quantity', item: { ...target, scale_factor: 100 } } }))
})
it('requires explicit action and reason, but void ignores hidden invalid replacement drafts', async () => {
  const blank = await resolve(source); expect(blank.errors).toHaveProperty('action'); expect(blank.errors).toHaveProperty('reason')
  const values = { ...purchaseCorrectionDefaults(), action: 'void', reason: 'Exact', replacement_item_id: 'missing', allocation: 'unit', item_unit_id: 'bad', quantity_base: '1e3', paid_minor: '-1', note: '\ud800' }
  expect((await resolve(source, values)).errors).toEqual({})
})
it('validates the actual selected target, explicit Unit, count and whole minor money', async () => {
  const values = { ...purchaseCorrectionDefaults(), action: 'replace', reason: 'Exact', replacement_item_id: target.id, allocation: 'none', quantity_base: '1500', paid_minor: '0' }
  expect((await resolve(source, values)).errors).toHaveProperty('replacement_item_id'); expect((await resolve(measured, values)).errors).toEqual({})
  const invalid = await resolve(measured, { ...values, quantity_base: '1.5', paid_minor: '', note: '\ud800' })
  expect(invalid.errors).toHaveProperty('quantity_base'); expect(invalid.errors).toHaveProperty('paid_minor'); expect(invalid.errors).toHaveProperty('note')
  const named = { ...values, allocation: 'unit', item_unit_id: targetUnit.id, quantity_base: '1' }
  expect((await resolve(individual, named)).errors).toEqual({}); expect((await resolve(individual, { ...named, quantity_base: '2' })).errors).toHaveProperty('quantity_base')
  expect((await resolve(individual, { ...named, item_unit_id: 'missing' })).errors).toHaveProperty('item_unit_id')
})
