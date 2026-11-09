import { expect, it } from 'vitest'
import type { Item } from '@/lib/api/catalog'
import type { PurchaseLineCorrection } from '@/lib/api/financial-corrections'
import type { Unit } from '@/lib/api/holdings'
import type { Purchase, PurchaseLine } from '@/lib/api/purchases'
import type { PurchaseCorrectionSource } from './correction-model'
import { adoptPurchaseCorrection, purchaseCorrectionDefaults, purchaseCorrectionProposal } from './correction-form'

const item: Item = { id: 'old', revision: 7, name: 'Original', tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1,
  brand: null, model: '', category_id: null, description: '', notes: '', status: 'active' }
const target: Item = { ...item, id: 'target', revision: 9, name: 'Replacement' }
const quantity: Item = { ...target, tracking_mode: 'quantity', unit_code: 'kg', base_unit: 'g', scale_factor: 1000 }
const unit: Unit = { id: 'unit', item_id: item.id, revision: 3, status: 'disposed', asset_code: 'Original', location_id: null, serial_number: null, notes: '' }
const targetUnit: Unit = { ...unit, id: 'target-unit', item_id: target.id, status: 'stored' }
const purchase: Purchase = { id: 'purchase', revision: 12, merchant_name: 'Merchant', purchased_at: '2026-11-07T12:00:00.123456789+08:00', currency: 'JPY', receipt_note: '' }
// Retained legacy Lines can name a Unit with a count greater than one.
const line: PurchaseLine = { id: 'line/legacy', revision: 4, purchase_id: purchase.id, item_id: item.id, item_unit_id: unit.id, quantity_base: 3, paid_minor: 0, note: 'Original' }
const source: PurchaseCorrectionSource = { purchase, lines: [line], corrections: [], line, item, originalUnit: { kind: 'resolved', unit }, replacement: { kind: 'none' } }
const individual: PurchaseCorrectionSource = { ...source, replacement: { kind: 'individual', item: target, units: [targetUnit] } }
const measured: PurchaseCorrectionSource = { ...source, replacement: { kind: 'quantity', item: quantity } }
const voidValues = () => ({ ...purchaseCorrectionDefaults(), action: 'void', reason: '\0  Exact reason é\n' })
const replaceValues = () => ({ ...voidValues(), action: 'replace', replacement_item_id: target.id, allocation: 'none', quantity_base: '1500', paid_minor: '0', note: 'Exact replacement\n' })
const invalid = (work: () => unknown) => expect(work).toThrowError(expect.objectContaining({ code: 'VALIDATION_ERROR' }))

it('leaves the decision, target, nullable Unit intent and new money/count unreviewed', () => {
  expect(purchaseCorrectionDefaults()).toEqual({ action: '', reason: '', replacement_item_id: '', allocation: '', item_unit_id: '', quantity_base: '', paid_minor: '', note: '' })
  invalid(() => purchaseCorrectionProposal(purchaseCorrectionDefaults(), source))
  invalid(() => purchaseCorrectionProposal({ ...voidValues(), reason: '' }, source))
  invalid(() => purchaseCorrectionProposal({ ...replaceValues(), allocation: '' }, measured))
  invalid(() => purchaseCorrectionProposal({ ...replaceValues(), replacement_item_id: '' }, measured))
})
it('builds a void from real original proofs while preserving inactive raw drafts and legacy source', () => {
  const values = { ...voidValues(), replacement_item_id: 'missing', allocation: 'unit', item_unit_id: 'legacy/unit', quantity_base: '1e3', paid_minor: '-1', note: '\ud800' }
  const proposal = purchaseCorrectionProposal(values, source)
  expect(proposal).toEqual({ source, unit: null, body: { line_id: line.id, line_revision: 4, item_revision: 7, reason: values.reason, action: 'void', replacement: null } })
  expect(proposal.source).not.toBe(source); expect(proposal.source.line).not.toBe(line); expect(values.quantity_base).toBe('1e3')
  const unaddressableLine = { ...line, item_unit_id: 'legacy/unit' }
  const unaddressable: PurchaseCorrectionSource = { ...source, line: unaddressableLine, lines: [unaddressableLine], originalUnit: { kind: 'unaddressable', id: 'legacy/unit' } }
  expect(purchaseCorrectionProposal(values, unaddressable).source.originalUnit).toEqual({ kind: 'unaddressable', id: 'legacy/unit' })
  const unallocated = { ...line, item_unit_id: null }
  expect(purchaseCorrectionProposal(values, { ...source, line: unallocated, lines: [unallocated], originalUnit: { kind: 'unallocated' } }).source.originalUnit).toEqual({ kind: 'unallocated' })
})
it('creates a complete quantity replacement with independent Item proofs and explicit zero, never multiplying money', () => {
  const result = purchaseCorrectionProposal(replaceValues(), measured)
  expect(result).toEqual({ source: measured, unit: null, body: { line_id: line.id, line_revision: 4, item_revision: 7, action: 'replace', reason: voidValues().reason,
    replacement: { item_id: target.id, item_revision: 9, item_unit_id: null, quantity_base: 1500, paid_minor: 0, note: 'Exact replacement\n' } } })
  const maximum = String(Number.MAX_SAFE_INTEGER)
  expect(purchaseCorrectionProposal({ ...replaceValues(), quantity_base: maximum, paid_minor: maximum }, measured).body.replacement).toMatchObject({ quantity_base: Number.MAX_SAFE_INTEGER, paid_minor: Number.MAX_SAFE_INTEGER })
  expect(purchaseCorrectionProposal(replaceValues(), { ...measured, purchase: { ...purchase, currency: 'ZZZ' } }).source.purchase.currency).toBe('ZZZ')
  const mutable = structuredClone(measured), frozen = purchaseCorrectionProposal(replaceValues(), mutable)
  Object.assign(mutable.item, { revision: 99 }); Object.assign(mutable.lines[0]!, { note: 'Later change' })
  expect(frozen.source.item.revision).toBe(7); expect(frozen.source.lines[0]!.note).toBe('Original')
})
it('requires the actually selected Unit and exactly one base unit, or an explicit unallocated decision', () => {
  const values = { ...replaceValues(), allocation: 'unit', item_unit_id: targetUnit.id, quantity_base: '1' }
  expect(purchaseCorrectionProposal(values, individual)).toMatchObject({ unit: targetUnit, body: { replacement: { item_id: target.id, item_unit_id: targetUnit.id, quantity_base: 1 } } })
  for (const id of ['', 'missing', '\ud800']) invalid(() => purchaseCorrectionProposal({ ...values, item_unit_id: id }, individual))
  invalid(() => purchaseCorrectionProposal({ ...values, quantity_base: '2' }, individual))
  expect(() => purchaseCorrectionProposal(values, measured)).toThrowError(expect.objectContaining({ code: 'TRACKING_MODE_CONFLICT' }))
  expect(purchaseCorrectionProposal({ ...values, allocation: 'none', item_unit_id: 'preserved inactive choice' }, individual).body.replacement).toMatchObject({ item_unit_id: null })
  const sameItem: PurchaseCorrectionSource = { ...source, replacement: { kind: 'individual', item, units: [unit] } }
  expect(purchaseCorrectionProposal({ ...values, replacement_item_id: item.id, item_unit_id: unit.id }, sameItem)).toMatchObject({ unit, body: { replacement: { item_id: item.id, item_revision: item.revision, item_unit_id: unit.id } } })
})
it.each(['', ' 1', '1 ', '01', '+1', '-1', '1.0', '1e3', '1,000', 'NaN', '9007199254740992', '1\n'])('rejects malformed or unsafe new quantity/money %j without modifying raw input', (raw) => {
  const values = replaceValues()
  invalid(() => purchaseCorrectionProposal({ ...values, quantity_base: raw }, measured))
  invalid(() => purchaseCorrectionProposal({ ...values, paid_minor: raw }, measured))
})
it('requires a positive quantity, exact Unicode reason and bounded replacement note', () => {
  invalid(() => purchaseCorrectionProposal({ ...replaceValues(), quantity_base: '0' }, measured))
  for (const reason of ['', '\ud800', '界'.repeat(10001)]) invalid(() => purchaseCorrectionProposal({ ...voidValues(), reason }, source))
  for (const note of ['\ud800', '😀'.repeat(10001)]) invalid(() => purchaseCorrectionProposal({ ...replaceValues(), note }, measured))
  expect(purchaseCorrectionProposal({ ...voidValues(), reason: '😀'.repeat(10000) }, source).body.reason).toHaveLength(20000)
})
it('rejects mixed source owners, missing selected Lines and invented or inconsistent Unit context', () => {
  const sameOwner: PurchaseCorrectionSource = { ...source, replacement: { kind: 'individual', item, units: [unit] } }
  const cases: unknown[] = [
    { ...source, purchase: { ...purchase, id: 'other' } }, { ...source, item: { ...item, id: 'other' } }, { ...source, lines: [] },
    { ...source, lines: [line, line] }, { ...source, lines: [{ ...line, note: 'Changed' }] },
    { ...source, originalUnit: { kind: 'unallocated' } }, { ...source, originalUnit: { kind: 'unaddressable', id: unit.id } },
    { ...source, originalUnit: { kind: 'resolved', unit: { ...unit, item_id: 'other' } } },
    { ...source, line: { ...line, item_unit_id: 'legacy/unit' }, lines: [{ ...line, item_unit_id: 'legacy/unit' }], originalUnit: { kind: 'resolved', unit: { ...unit, id: 'legacy/unit' } } },
    { ...individual, replacement: { kind: 'individual', item: target, units: [targetUnit, targetUnit] } },
    { ...individual, replacement: { kind: 'individual', item: target, units: [{ ...targetUnit, item_id: 'other' }] } },
    { ...individual, replacement: { kind: 'quantity', item: target } },
    { ...sameOwner, replacement: { kind: 'individual', item: { ...item, notes: 'Different' }, units: [unit] } },
    { ...sameOwner, replacement: { kind: 'individual', item, units: [] } },
    { ...sameOwner, replacement: { kind: 'individual', item, units: [{ ...unit, notes: 'Different' }] } },
    { ...source, corrections: undefined }, { ...source, extra: 'unexpected' },
  ]
  for (const value of cases) invalid(() => purchaseCorrectionProposal(voidValues(), value as PurchaseCorrectionSource))
  invalid(() => purchaseCorrectionProposal(replaceValues(), source))
  invalid(() => purchaseCorrectionProposal({ ...replaceValues(), replacement_item_id: 'different' }, individual))
})
it('rejects malformed or duplicate correction history rather than presenting an invented complete source', () => {
  const old = { ...line, id: 'old-line' }
  const correction: PurchaseLineCorrection = { id: 'event', revision: 1, purchase_id: purchase.id, reason: 'Reason', created_at: '2026-11-07T00:00:00Z', action: 'void', before: old, after: null }
  expect(purchaseCorrectionProposal(voidValues(), { ...source, corrections: [correction] }).source.corrections).toEqual([correction])
  invalid(() => purchaseCorrectionProposal(voidValues(), { ...source, corrections: [correction, correction] }))
  invalid(() => purchaseCorrectionProposal(voidValues(), { ...source, corrections: [{ ...correction, before: line }] }))
})
it('explicitly adopts whole changed interpretations without conversion, erasing raw text or changing the original Line', () => {
  const values = { ...replaceValues(), allocation: 'unit', item_unit_id: targetUnit.id, quantity_base: '1e3 invalid', paid_minor: '9007199254740992', note: '\ud800' }
  const current: PurchaseCorrectionSource = { ...measured, purchase: { ...purchase, revision: 13, currency: 'USD' }, item: { ...item, revision: 8 },
    replacement: { kind: 'quantity', item: { ...quantity, revision: 10, unit_code: 'L', base_unit: 'ml', scale_factor: 100 } } }
  const adopted = adoptPurchaseCorrection(values, individual, current)
  expect(adopted).toEqual({ values, source: current }); expect(adopted.values).not.toBe(values); expect(adopted.source).not.toBe(current)
  expect(() => purchaseCorrectionProposal(adopted.values, adopted.source)).toThrowError(expect.objectContaining({ code: 'TRACKING_MODE_CONFLICT' }))
  const removed: PurchaseCorrectionSource = { ...individual, replacement: { kind: 'individual', item: target, units: [] } }
  const retained = adoptPurchaseCorrection({ ...replaceValues(), allocation: 'unit', item_unit_id: targetUnit.id, quantity_base: '1' }, individual, removed)
  expect(retained.values.item_unit_id).toBe(targetUnit.id); invalid(() => purchaseCorrectionProposal(retained.values, retained.source))
  invalid(() => adoptPurchaseCorrection(values, individual, { ...current, purchase: { ...current.purchase, id: 'other' } }))
  const changed = { ...line, note: 'Different original' }
  invalid(() => adoptPurchaseCorrection(values, individual, { ...current, line: changed, lines: [changed] }))
  expect(purchase.currency).toBe('JPY'); expect(item.revision).toBe(7)
})
