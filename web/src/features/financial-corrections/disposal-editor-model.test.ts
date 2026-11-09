import { expect, it } from 'vitest'
import { disposalCorrectionDefaults } from '@/features/assets/correction-form'
import type { DisposalCorrectionSource } from '@/features/assets/correction-model'
import type { DisposalCorrection, DisposalRecord } from '@/lib/api/financial-corrections'
import { disposalCorrectionResolver, sameDisposalCorrectionSource } from './disposal-editor-model'

const instant = '2026-11-07T12:00:00.123456789+08:00'
const data = { item_unit_id: 'unit', kind: 'sold', currency: 'JPY', realized_proceeds_minor: 200, disposed_at: instant, note: 'Original' } as const
const record: DisposalRecord = { item_unit_id: 'unit', revision: 9, original: data, current: data }
const voided = { ...record, revision: 10, current: null } as const satisfies DisposalRecord
const entry: DisposalCorrection = { id: 'entry', revision: 1, item_unit_id: 'unit', reason: 'Reason', created_at: instant, before: record, action: 'void', after: voided }
const source: DisposalCorrectionSource = {
  item: { id: 'item', revision: 11, name: 'Owner', tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1, brand: null, model: '', category_id: null, description: '', notes: '', status: 'active' },
  unit: { id: 'unit', item_id: 'item', revision: 13, status: 'disposed', asset_code: 'AST', location_id: null, serial_number: null, notes: '' },
  asset: { item_unit_id: 'unit', revision: 7, cost_basis: 'manual', currency: 'JPY', manual_acquisition_minor: 100, in_service_at: null, retired_at: null, estimated_residual_minor: null, usage_unit: 'times' },
  disposal: record, corrections: [],
}
const voidSource: DisposalCorrectionSource = { ...source, disposal: voided, corrections: [entry] }
const values = () => ({ ...disposalCorrectionDefaults(source), action: 'replace', reason: 'Exact reason', kind: 'sold', realized_proceeds_minor: '0', disposed_at: instant })
const resolve = (data: DisposalCorrectionSource, input = values()) => disposalCorrectionResolver(data)(input, undefined, { fields: {}, shouldUseNativeValidation: false })

it('compares complete reviewed scopes and history by field rather than object property order', () => {
  expect(sameDisposalCorrectionSource(undefined, undefined)).toBe(true); expect(sameDisposalCorrectionSource(null, undefined)).toBe(false)
  expect(sameDisposalCorrectionSource(source, structuredClone(source))).toBe(true)
  const reordered = { action: entry.action, after: entry.after, before: entry.before, created_at: entry.created_at, reason: entry.reason, item_unit_id: entry.item_unit_id, revision: entry.revision, id: entry.id } as const
  expect(sameDisposalCorrectionSource(voidSource, { ...voidSource, corrections: [reordered] })).toBe(true)
  for (const current of [{ ...voidSource, unit: { ...source.unit, notes: 'Changed' } }, { ...voidSource, item: { ...source.item, revision: 12 } },
    { ...voidSource, asset: { ...source.asset, currency: 'USD' } }, { ...voidSource, disposal: { ...voided, revision: 11 } },
    { ...voidSource, corrections: [] }, { ...voidSource, corrections: [{ ...entry, reason: 'Changed' }] }, { ...voidSource, corrections: [{ ...entry, after: { ...voided, revision: 11 } }] }]) {
    expect(sameDisposalCorrectionSource(voidSource, current)).toBe(false)
  }
})
it('maps explicit action and reason, and ignores hidden replacement text for a valid void', async () => {
  expect((await resolve(source, disposalCorrectionDefaults(source))).errors).toHaveProperty('action')
  expect((await resolve(source, { ...values(), action: 'void', reason: '' })).errors).toHaveProperty('reason')
  expect((await resolve(voidSource, { ...values(), action: 'void' })).errors).toHaveProperty('action')
  expect((await resolve(source, { ...values(), action: 'void', kind: 'invalid', realized_proceeds_minor: 'bad', disposed_at: 'bad', note: '\ud800' })).errors).toEqual({})
})
it('validates kind, exact currency, integer proceeds, date and note before building a replacement', async () => {
  expect((await resolve(source)).errors).toEqual({})
  const bad = await resolve(source, { ...values(), kind: '', currency: 'USD', realized_proceeds_minor: '', disposed_at: 'invalid', note: '\ud800' })
  expect(Object.keys(bad.errors).sort()).toEqual(['currency', 'disposed_at', 'kind', 'note', 'realized_proceeds_minor'])
  expect((await resolve(source, { ...values(), kind: 'donated', realized_proceeds_minor: '1' })).errors).toHaveProperty('realized_proceeds_minor')
  expect((await resolve(source, { ...values(), kind: 'donated', realized_proceeds_minor: '0' })).errors).toEqual({})
  expect((await resolve({ ...source, item: { ...source.item, id: 'other' } })).errors).toHaveProperty('action')
})
