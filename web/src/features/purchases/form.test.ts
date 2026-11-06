import { expect, it } from 'vitest'
import type { Item } from '@/lib/api/catalog'
import type { Unit } from '@/lib/api/holdings'
import type { Purchase } from '@/lib/api/purchases'
import { adoptPurchaseAllocation, purchaseCreateBody, purchaseDefaults, purchaseLineDefaults, purchaseLineProposal, purchasePatch, retainPurchaseEdits } from './form'
import type { PurchaseAllocationContext } from './model'

const item: Item = { id: 'item', revision: 7, name: 'Reviewed quantity', brand: null, model: '', category_id: null, description: '', notes: '', tracking_mode: 'quantity',
  unit_code: 'kg', base_unit: 'g', scale_factor: 1000, status: 'active' }
const purchase: Purchase = { id: 'purchase', revision: 12, merchant_name: '  Merchant é  ', purchased_at: '2026-11-06T12:00:00.123456789+08:00', currency: 'JPY', receipt_note: 'Original' }
const unit: Unit = { id: 'unit', item_id: item.id, revision: 3, asset_code: 'Retained AST', location_id: null, status: 'disposed', serial_number: null, notes: '' }
const quantity: PurchaseAllocationContext = { kind: 'quantity', purchase, item }
const individual: PurchaseAllocationContext = { kind: 'individual', purchase, item: { ...item, tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1 }, units: [unit] }
const values = { ...purchaseLineDefaults(), allocation: 'none', quantity_base: '1500', paid_minor: '0', note: '  Exact é\n' }

it('defaults to no guessed date, currency, amount, count or nullable allocation and preserves exact retained header fields', () => {
  expect(purchaseDefaults()).toEqual({ merchant_name: '', purchased_at: '', currency: '', receipt_note: '' })
  expect(purchaseLineDefaults()).toEqual({ allocation: '', item_unit_id: '', quantity_base: '', paid_minor: '', note: '' })
  expect(purchaseDefaults(purchase)).toEqual({ merchant_name: purchase.merchant_name, purchased_at: purchase.purchased_at, currency: 'JPY', receipt_note: 'Original' })
  expect(() => purchaseCreateBody(purchaseDefaults())).toThrow(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
  expect(() => purchaseLineProposal(purchaseLineDefaults(), quantity)).toThrow(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
})
it('creates only exact header metadata and emits changed-only descriptive patches without normalizing equal-instant intent', () => {
  const original = purchaseDefaults(purchase)
  expect(purchaseCreateBody(original)).toEqual(original)
  expect(purchasePatch(original, purchase)).toBeUndefined()
  expect(purchasePatch({ ...original, receipt_note: '' }, purchase)).toEqual({ receipt_note: '' })
  expect(purchasePatch({ ...original, merchant_name: '  Edited é  ', currency: 'USD' }, purchase)).toEqual({ merchant_name: '  Edited é  ', currency: 'USD' })
  expect(purchasePatch({ ...original, purchased_at: '2026-11-06T04:00:00.123456789Z' }, purchase)).toEqual({ purchased_at: '2026-11-06T04:00:00.123456789Z' })
  const retained = { ...purchase, currency: 'ZZZ' }
  expect(purchasePatch({ ...purchaseDefaults(retained), receipt_note: 'Descriptive only' }, retained)).toEqual({ receipt_note: 'Descriptive only' })
})
it.each([{ merchant_name: '' }, { merchant_name: '\uD800' }, { purchased_at: '2026-11-06' }, { purchased_at: '2026-02-30T00:00:00Z' }, { purchased_at: '9999-12-31T23:59:59-00:01' },
  { currency: 'jpy' }, { receipt_note: '\0' + '界'.repeat(10000) }, { receipt_note: null }, { paid_minor: '1' }])('rejects invalid header fields %# without trimming or repair', (changed) => {
  const edited = { ...purchaseDefaults(purchase), ...changed }
  expect(() => purchaseCreateBody(edited as never)).toThrow(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
  expect(() => purchasePatch(edited as never, purchase)).toThrow(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
})
it('adopts only untouched header fields and retains larger/invalid in-progress local text, identity and spelling', () => {
  const current = { ...purchase, revision: 13, merchant_name: 'Concurrent merchant', purchased_at: '2026-11-07T00:00:00.000000001Z', currency: 'USD', receipt_note: 'Concurrent receipt' }
  const edited = { ...purchaseDefaults(purchase), merchant_name: '😀'.repeat(301), purchased_at: 'invalid raw instant', receipt_note: '' }
  expect(retainPurchaseEdits(edited, purchase, current)).toEqual({ ...edited, currency: 'USD' })
  const currencyEdited = { ...purchaseDefaults(purchase), currency: 'not a code' }
  expect(retainPurchaseEdits(currencyEdited, purchase, current)).toEqual({ ...purchaseDefaults(current), currency: 'not a code' })
  expect(() => retainPurchaseEdits(edited, purchase, { ...current, id: 'other' })).toThrow(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
  expect(edited.purchased_at).toBe('invalid raw instant'); expect(purchase.revision).toBe(12)
})
it('builds one explicit whole-allocation proposal from original currency/Item proof without receiving Stock or multiplying money', () => {
  const proposal = purchaseLineProposal(values, quantity)
  expect(proposal).toEqual({ purchase, item, unit: null, body: { item_id: item.id, item_revision: 7, item_unit_id: null, quantity_base: 1500, paid_minor: 0, note: values.note } })
  const maximum = String(Number.MAX_SAFE_INTEGER)
  expect(purchaseLineProposal({ ...values, quantity_base: maximum, paid_minor: maximum }, quantity).body).toMatchObject({ quantity_base: Number.MAX_SAFE_INTEGER, paid_minor: Number.MAX_SAFE_INTEGER })
  expect(purchaseLineProposal({ ...values, allocation: 'unit', item_unit_id: unit.id, quantity_base: '1', paid_minor: '120000' }, individual)).toEqual({ purchase, item: individual.item, unit,
    body: { item_id: item.id, item_revision: 7, item_unit_id: unit.id, quantity_base: 1, paid_minor: 120000, note: values.note } })
  expect(purchaseLineProposal(values, individual).body).toMatchObject({ item_unit_id: null, quantity_base: 1500, paid_minor: 0 })
})
it.each(['', ' 1', '1 ', '01', '+1', '-1', '1.0', '1e3', '1,000', '0x10', 'NaN', 'Infinity', '9007199254740992', '1\n'])('rejects noncanonical/unsafe raw count or money %j', (text) => {
  expect(() => purchaseLineProposal({ ...values, quantity_base: text }, quantity)).toThrow(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
  expect(() => purchaseLineProposal({ ...values, paid_minor: text }, quantity)).toThrow(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
})
it('requires a valid explicit nullable choice, one actually selected Unit and positive base count while retaining server eligibility', () => {
  for (const changed of [{ allocation: '' }, { allocation: 'unknown' }, { quantity_base: '0' }, { note: '\uD800' }, { note: '\0' + '界'.repeat(10000) }]) expect(() => purchaseLineProposal({ ...values, ...changed }, quantity)).toThrow(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
  expect(() => purchaseLineProposal({ ...values, allocation: 'unit', item_unit_id: unit.id, quantity_base: '1' }, quantity)).toThrow(expect.objectContaining({ code: 'TRACKING_MODE_CONFLICT' }))
  for (const id of ['', 'missing', '\uD800']) expect(() => purchaseLineProposal({ ...values, allocation: 'unit', item_unit_id: id, quantity_base: '1' }, individual)).toThrow(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
  expect(() => purchaseLineProposal({ ...values, allocation: 'unit', item_unit_id: unit.id, quantity_base: '2' }, individual)).toThrow(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
  expect(() => purchaseLineProposal(values, { ...quantity, kind: 'individual', units: [] })).toThrow(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
  expect(() => purchaseLineProposal({ ...values, allocation: 'unit', item_unit_id: unit.id, quantity_base: '1' }, { ...individual, units: [{ ...unit, item_id: 'other' }] })).toThrow(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
  expect(() => purchaseLineProposal({ ...values, allocation: 'unit', item_unit_id: unit.id, quantity_base: '1' }, { ...individual, units: null } as never)).toThrow(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
  expect(purchaseLineProposal({ ...values, allocation: 'none', item_unit_id: 'retained hidden choice' }, individual).body.item_unit_id).toBeNull()
  const retained = { ...quantity, purchase: { ...purchase, currency: 'ZZZ' } }
  expect(purchaseLineProposal(values, retained).purchase.currency).toBe('ZZZ')
})
it('adopts the whole currency and Item interpretation atomically without converting/dropping raw drafts or missing Unit decisions', () => {
  const edited = { ...values, allocation: 'unit', item_unit_id: unit.id, quantity_base: 'invalid quantity', paid_minor: '9007199254740992', note: '😀'.repeat(10001) }
  const current: PurchaseAllocationContext = { kind: 'quantity', purchase: { ...purchase, revision: 13, currency: 'USD', purchased_at: '2026-11-07T00:00:00Z' },
    item: { ...quantity.item, revision: 8, tracking_mode: 'quantity', unit_code: 'L', base_unit: 'ml', scale_factor: 100 } }
  const adopted = adoptPurchaseAllocation(edited, individual, current)
  expect(adopted).toEqual({ values: edited, source: current })
  expect(adopted.values).not.toBe(edited); expect(adopted.source).not.toBe(current); expect(adopted.source.item).not.toBe(current.item)
  expect(() => purchaseLineProposal(adopted.values, adopted.source)).toThrow()
  const removed = { ...individual, units: [] }
  const retained = adoptPurchaseAllocation({ ...values, allocation: 'unit', item_unit_id: unit.id, quantity_base: '1' }, individual, removed)
  expect(retained.values.item_unit_id).toBe(unit.id)
  expect(() => purchaseLineProposal(retained.values, retained.source)).toThrow(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
  expect(() => adoptPurchaseAllocation(values, quantity, { ...quantity, item: { ...item, id: 'other' } })).toThrow(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
  expect(() => adoptPurchaseAllocation(values, quantity, { ...quantity, purchase: { ...purchase, id: 'other' } })).toThrow(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
})
