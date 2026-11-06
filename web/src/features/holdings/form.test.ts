import { expect, it } from 'vitest'
import { parseSafeCount } from '@/lib/api/domain'
import type { Stock, Unit } from '@/lib/api/holdings'
import { retainUnitEdits, stockBody, stockDefaults, unitCreateBody, unitCreateDefaults, unitMetadataDefaults, unitMoveBody, unitPatch, unitStatusBody, unitStatusDefaults } from './form'

const unit: Unit = { id: 'unit', item_id: 'item', revision: 2, location_id: 'source', asset_code: 'AST-retained', serial_number: null, notes: 'Original', status: 'stored' }
const stock: Stock = { item_id: 'item', location_id: 'source', revision: 2, quantity_base: 1500, unit_code: 'kg', base_unit: 'g', scale_factor: 1000 }

it('parses only original canonical safe base counts, including explicit absolute zero', () => {
  for (const quantity of [0, 1, 1000, Number.MAX_SAFE_INTEGER]) expect(parseSafeCount(String(quantity))).toBe(quantity)
  expect(parseSafeCount('0', 1)).toBeUndefined()
  expect(parseSafeCount('1', 1)).toBe(1)
})
it.each(['', '-0', '-1', '+1', '00', '01', '1.0', '.5', '1e3', '1,000', ' 1', '1 ', '1\n', '1\r', '1\r\n', '1 ', '1 ', '9007199254740992', '99999999999999999', '\uD800'])('never rounds, trims or converts invalid count text %j', (value) => {
  expect(parseSafeCount(value)).toBeUndefined()
})
it('does not coerce non-string count values', () => {
  for (const value of [null, undefined, 1, true, ['1'], {}, NaN]) expect(parseSafeCount(value as never)).toBeUndefined()
})
it('keeps creation location explicit and distinguishes absent, null and supplied-empty serial intent', () => {
  expect(unitCreateDefaults()).toEqual({ location_id: '', serial_provided: false, serial_number: '', notes: '', status: 'stored' })
  expect(unitCreateBody(unitCreateDefaults())).toEqual({ location_id: null, serial_number: null, notes: '', status: 'stored' })
  expect(unitCreateBody({ ...unitCreateDefaults(), location_id: 'target', serial_provided: true, serial_number: '', notes: '  Exact é 😀  ', status: 'lost' }))
    .toEqual({ location_id: 'target', serial_number: '', notes: '  Exact é 😀  ', status: 'lost' })
  expect(unitCreateBody({ ...unitCreateDefaults(), serial_number: '\uD800' }).serial_number).toBeNull()
  expect(unitMetadataDefaults({ ...unit, serial_number: '' })).toMatchObject({ serial_provided: true, serial_number: '' })
})
it.each([{ status: 'retired' }, { status: 'disposed' }, { location_id: '\uD800' }, { serial_provided: true, serial_number: '\uD800' }, { notes: '😀'.repeat(10001) }, { location_id: null }, { asset_code: 'caller' }, { item_id: 'other' }])('rejects invalid or implicit Unit creation %j', (changed) => {
  expect(() => unitCreateBody({ ...unitCreateDefaults(), ...changed } as never)).toThrow(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
})
it('builds changed-only metadata patches, preserving exact nullable/empty text and leaving identity/status/location alone', () => {
  const defaults = unitMetadataDefaults(unit)
  expect(unitPatch(defaults, unit)).toBeUndefined()
  expect(unitPatch({ ...defaults, serial_provided: true, serial_number: '' }, unit)).toEqual({ serial_number: '' })
  expect(unitPatch({ ...defaults, notes: '' }, unit)).toEqual({ notes: '' })
  expect(unitPatch({ ...defaults, serial_provided: true, serial_number: '  é 😀  ', notes: '\0Manual\n' }, unit)).toEqual({ serial_number: '  é 😀  ', notes: '\0Manual\n' })
  expect(unitPatch({ ...defaults, serial_provided: false }, { ...unit, serial_number: 'Earlier' })).toEqual({ serial_number: null })
  for (const changed of [{ notes: null }, { serial_number: 1 }, { notes: '\uD800' }, { status: 'disposed' }, { location_id: 'target' }, { quantity_base: 1 }]) {
    expect(() => unitPatch({ ...defaults, ...changed } as never, unit)).toThrow(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
  }
})
it('requires a real nonnull move target without silently clearing placement', () => {
  expect(unitMoveBody('target')).toEqual({ target_location_id: 'target' })
  for (const value of ['', null, '\uD800', '😀'.repeat(129)]) expect(() => unitMoveBody(value as never)).toThrow(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
})
it('requires explicit physical disposal without inferring a kind or publishing unused form fields', () => {
  expect(unitStatusDefaults(unit)).toEqual({ status: 'stored', disposal_kind: '', note: '' })
  expect(unitStatusDefaults({ ...unit, status: 'disposed' }).disposal_kind).toBe('')
  expect(unitStatusBody({ status: 'retired', disposal_kind: 'Old inactive draft', note: '  Exact retirement  ' })).toEqual({ status: 'retired', note: '  Exact retirement  ' })
  for (const disposal_kind of ['sold', 'donated', 'recycled', 'discarded']) expect(unitStatusBody({ status: 'disposed', disposal_kind, note: '' })).toEqual({ status: 'disposed', disposal_kind, note: '' })
  for (const values of [{ status: 'disposed', disposal_kind: '', note: '' }, { status: 'unknown', disposal_kind: '', note: '' }, { status: 'stored', disposal_kind: '', note: '\uD800' }, { status: 'disposed', disposal_kind: 'sold', note: '', realized_proceeds_minor: 1 }]) {
    expect(() => unitStatusBody(values)).toThrow(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
  }
})
it('adopts current untouched Unit fields only explicitly, retaining whole nullable serial edits and invalid active drafts', () => {
  const current = { ...unit, revision: 3, serial_number: 'Concurrent', notes: 'Concurrent note' }
  const defaults = unitMetadataDefaults(unit)
  expect(retainUnitEdits(defaults, unit, current)).toEqual(unitMetadataDefaults(current))
  const provided = { ...unit, serial_number: 'Original serial' }, concurrent = { ...provided, revision: 4, serial_number: 'Concurrent serial' }
  expect(retainUnitEdits({ ...unitMetadataDefaults(provided), serial_number: '' }, provided, concurrent)).toEqual({ serial_provided: true, serial_number: '', notes: unit.notes })
  expect(retainUnitEdits(unitMetadataDefaults(provided), provided, concurrent)).toEqual(unitMetadataDefaults(concurrent))
  expect(retainUnitEdits({ ...defaults, serial_provided: true, serial_number: '', notes: '\uD800' }, unit, current)).toEqual({ serial_provided: true, serial_number: '', notes: '\uD800' })
  expect(retainUnitEdits({ ...unitMetadataDefaults(current), serial_provided: false }, current, { ...current, revision: 4, notes: 'Later' }))
    .toEqual({ serial_provided: false, serial_number: 'Concurrent', notes: 'Later' })
  expect(retainUnitEdits({ ...defaults, serial_provided: true, serial_number: '😀'.repeat(10001) }, unit, current).serial_number).toHaveLength(20002)
  for (const changed of [{ id: 'other' }, { item_id: 'other' }]) expect(() => retainUnitEdits(defaults, unit, { ...current, ...changed })).toThrow(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
})
it('does not prefill a delta with the whole balance or invent a missing absolute zero placement', () => {
  expect(stockDefaults('receive', stock)).toEqual({ source_location_id: '', target_location_id: stock.location_id, quantity_base: '', reason: '', note: '' })
  expect(stockDefaults('consume', stock)).toMatchObject({ source_location_id: stock.location_id, target_location_id: '', quantity_base: '' })
  expect(stockDefaults('transfer', stock)).toMatchObject({ source_location_id: stock.location_id, target_location_id: '', quantity_base: '' })
  expect(stockDefaults('adjust', stock)).toMatchObject({ source_location_id: '', target_location_id: stock.location_id, quantity_base: '1500' })
  expect(stockDefaults('adjust', { ...stock, quantity_base: 0 }).quantity_base).toBe('0')
  expect(stockDefaults('adjust').quantity_base).toBe('')
})
it.each(['receive', 'consume', 'transfer', 'adjust'] as const)('builds exact %s base-count input without leftover fields, conversions or draft mutation', (kind) => {
  const values = { source_location_id: 'source', target_location_id: 'target', quantity_base: kind === 'adjust' ? '0' : '1000', reason: '  Counted é  ', note: '\0Exact note\n' }
  const before = { ...values }, result = stockBody(kind, values, stock.item_id)
  expect(result).toMatchObject({ kind, body: { item_id: stock.item_id, quantity_base: kind === 'adjust' ? 0 : 1000, note: values.note } })
  expect('source_location_id' in result.body).toBe(kind === 'consume' || kind === 'transfer')
  expect('target_location_id' in result.body).toBe(kind !== 'consume')
  expect('reason' in result.body).toBe(kind === 'adjust')
  expect(values).toEqual(before)
})
it('keeps invalid or unsafe Stock drafts intact and rejects incomplete/converted/lifecycle fields', () => {
  const base = { ...stockDefaults('transfer'), source_location_id: 'source', target_location_id: 'target', quantity_base: '1000' }
  for (const quantity_base of ['', '0', '-1', '1.5', '1e3', '1000\n', '9007199254740992']) {
    const values = { ...base, quantity_base }
    expect(() => stockBody('transfer', values, stock.item_id)).toThrow(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
    expect(values.quantity_base).toBe(quantity_base)
  }
  for (const changed of [{ source_location_id: '' }, { target_location_id: 'source' }, { note: '\uD800' }, { scale_factor: 1000 }, { quantity_base: 1000 }, { note: null }]) {
    expect(() => stockBody('transfer', { ...base, ...changed } as never, stock.item_id)).toThrow(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
  }
  expect(() => stockBody('adjust', { ...base, quantity_base: '0', reason: '' }, stock.item_id)).toThrow(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
  expect(() => stockBody('receive', base, '')).toThrow(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
  expect(() => stockBody('uncontracted' as never, base, stock.item_id)).toThrow(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
})
