import { expect, it } from 'vitest'
import type { Asset } from '@/lib/api/assets'
import type { Item } from '@/lib/api/catalog'
import type { DisposalCorrection, DisposalData, DisposalRecord } from '@/lib/api/financial-corrections'
import type { Unit } from '@/lib/api/holdings'
import type { DisposalCorrectionSource } from './correction-model'
import { adoptDisposalCorrection, disposalCorrectionDefaults, disposalCorrectionProposal } from './correction-form'

const item: Item = { id: 'item', revision: 11, name: 'Owner', tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1,
  brand: null, model: '', category_id: null, description: '', notes: '', status: 'active' }
const unit: Unit = { id: 'unit', item_id: item.id, revision: 13, status: 'disposed', asset_code: 'AST', location_id: null, serial_number: null, notes: '' }
const asset: Asset = { item_unit_id: unit.id, revision: 7, cost_basis: 'manual', currency: 'JPY', manual_acquisition_minor: 100, in_service_at: null, retired_at: null, estimated_residual_minor: 20, usage_unit: 'times' }
const instant = '2026-11-07T12:00:00.123456789+08:00'
const original: DisposalData = { item_unit_id: unit.id, kind: 'donated', disposed_at: instant, currency: 'ZZZ', realized_proceeds_minor: 5, note: 'Retained legacy' }
const record: DisposalRecord = { item_unit_id: unit.id, revision: 9, original, current: original }
const source: DisposalCorrectionSource = { unit, item, asset, disposal: record, corrections: [] }
const voided = { ...record, revision: 10, current: null } as const satisfies DisposalRecord
const voidEntry: DisposalCorrection = { id: 'event', revision: 1, item_unit_id: unit.id, action: 'void', reason: 'Earlier void', created_at: instant, before: record, after: voided }
const voidSource: DisposalCorrectionSource = { ...source, disposal: voided, corrections: [voidEntry] }
const voidValues = () => ({ ...disposalCorrectionDefaults(source), action: 'void', reason: '\0 Exact reason é\n' })
const replaceValues = () => ({ ...voidValues(), action: 'replace', kind: 'sold', realized_proceeds_minor: '0', disposed_at: instant, note: 'Replacement\n' })
const invalid = (work: () => unknown) => expect(work).toThrowError(expect.objectContaining({ code: 'VALIDATION_ERROR' }))

it('defaults to blank decision, date and new proceeds while displaying the actual Asset currency', () => {
  expect(disposalCorrectionDefaults(source)).toEqual({ action: '', reason: '', kind: '', currency: 'JPY', realized_proceeds_minor: '', disposed_at: '', note: '' })
  expect(disposalCorrectionDefaults(voidSource)).toEqual(disposalCorrectionDefaults(source))
  invalid(() => disposalCorrectionProposal(disposalCorrectionDefaults(source), source))
  invalid(() => disposalCorrectionProposal({ ...voidValues(), reason: '' }, source))
  invalid(() => disposalCorrectionProposal({ ...replaceValues(), realized_proceeds_minor: '' }, source))
  invalid(() => disposalCorrectionProposal({ ...replaceValues(), disposed_at: '' }, source))
})
it('retains a real void proof and legacy source without interpreting inactive raw replacement text', () => {
  const values = { ...voidValues(), kind: 'invalid', currency: 'not currency', realized_proceeds_minor: '1e3', disposed_at: 'invalid', note: '\ud800' }
  const result = disposalCorrectionProposal(values, source)
  expect(result).toEqual({ source, body: { asset_revision: 7, action: 'void', replacement: null, reason: values.reason } })
  expect(result.source).not.toBe(source); expect(result.source.disposal).not.toBe(record); expect(values.realized_proceeds_minor).toBe('1e3')
  expect(() => disposalCorrectionProposal(values, voidSource)).toThrowError(expect.objectContaining({ code: 'ASSET_HISTORY_LOCKED' }))
})
it('builds complete active replacement or explicit reinstatement without adding physical proofs', () => {
  const expected = { asset_revision: 7, action: 'replace', replacement: { kind: 'sold', currency: 'JPY', realized_proceeds_minor: 0, disposed_at: instant, note: 'Replacement\n' }, reason: voidValues().reason }
  const active = disposalCorrectionProposal(replaceValues(), source), reinstated = disposalCorrectionProposal(replaceValues(), voidSource)
  expect(active.body).toEqual(expected); expect(reinstated.body).toEqual(expected)
  expect(reinstated.source.disposal.current).toBeNull(); expect(reinstated.source.disposal.original).toEqual(original)
  expect(disposalCorrectionProposal({ ...replaceValues(), realized_proceeds_minor: String(Number.MAX_SAFE_INTEGER) }, source).body.replacement).toMatchObject({ realized_proceeds_minor: Number.MAX_SAFE_INTEGER })
  const otherSpelling = '2026-11-07T04:00:00.123456789Z'
  expect(disposalCorrectionProposal({ ...replaceValues(), disposed_at: otherSpelling }, voidSource).body.replacement).toMatchObject({ disposed_at: otherSpelling })
  const mutable = structuredClone(voidSource), frozen = disposalCorrectionProposal(replaceValues(), mutable)
  Object.assign(mutable.asset, { revision: 99 }); Object.assign(mutable.disposal.original, { note: 'Later change' })
  expect(frozen.source.asset.revision).toBe(7); expect(frozen.source.disposal.original.note).toBe('Retained legacy')
})
it.each(['sold', 'donated', 'recycled', 'discarded', 'other'])('requires explicit zero for %s and rejects positive non-sale proceeds', (kind) => {
  expect(disposalCorrectionProposal({ ...replaceValues(), kind }, source).body.replacement).toMatchObject({ kind, realized_proceeds_minor: 0 })
  invalid(() => disposalCorrectionProposal({ ...replaceValues(), kind, realized_proceeds_minor: '' }, source))
  if (kind !== 'sold') invalid(() => disposalCorrectionProposal({ ...replaceValues(), kind, realized_proceeds_minor: '1' }, source))
})
it.each(['', '01', '+1', '-1', '1.0', '1e3', '1,000', ' 1', '1\n', '9007199254740992'])('rejects malformed/unsafe raw proceeds %j without conversion', (raw) => {
  invalid(() => disposalCorrectionProposal({ ...replaceValues(), realized_proceeds_minor: raw }, source))
})
it.each(['0000-01-01T00:00:00Z', '2026-02-30T00:00:00Z', '2026-11-07T00:00:60Z', '2026-11-07T00:00:00.1234567890Z', '0001-01-01T00:00:00+00:01', '9999-12-31T23:59:59-00:01'])('rejects invalid or UTC-overflow instant %s without losing nanoseconds', (disposed_at) => {
  invalid(() => disposalCorrectionProposal({ ...replaceValues(), disposed_at }, source))
})
it('requires matching Asset currency, known action, exact Unicode reason and bounded note', () => {
  invalid(() => disposalCorrectionProposal({ ...replaceValues(), currency: 'USD' }, source))
  invalid(() => disposalCorrectionProposal({ ...replaceValues(), action: 'reinstate' }, voidSource))
  for (const reason of ['', '\ud800', '😀'.repeat(10001)]) invalid(() => disposalCorrectionProposal({ ...voidValues(), reason }, source))
  for (const note of ['\ud800', '😀'.repeat(10001)]) invalid(() => disposalCorrectionProposal({ ...replaceValues(), note }, source))
  expect(disposalCorrectionProposal({ ...voidValues(), reason: '界'.repeat(10000) }, source).body.reason).toHaveLength(10000)
})
it('withholds mismatched owners, missing history, duplicate entries and changed retained originals', () => {
  const cases: unknown[] = [
    { ...source, unit: { ...unit, item_id: 'other' } }, { ...source, item: { ...item, tracking_mode: 'quantity' } },
    { ...source, asset: { ...asset, item_unit_id: 'other' } }, { ...source, disposal: { ...record, item_unit_id: 'other' } },
    { ...source, disposal: voided }, { ...voidSource, corrections: [] }, { ...voidSource, corrections: [voidEntry, voidEntry] },
    { ...voidSource, disposal: { ...voided, original: { ...original, note: 'Changed' } } },
    { ...source, corrections: undefined }, { ...source, summary: {} },
  ]
  for (const value of cases) invalid(() => disposalCorrectionProposal(voidValues(), value as DisposalCorrectionSource))
})
it('adopts only after explicit review, preserving invalid raw money/date and the original disposal identity', () => {
  const values = { ...replaceValues(), realized_proceeds_minor: '1e3', disposed_at: 'invalid date', reason: '\ud800', note: 'draft' }
  const current: DisposalCorrectionSource = { ...voidSource, item: { ...item, revision: 12 }, unit: { ...unit, revision: 14 }, asset: { ...asset, revision: 8, currency: 'USD' } }
  const adopted = adoptDisposalCorrection(values, source, current)
  expect(adopted).toEqual({ values: { ...values, currency: 'USD' }, source: current })
  expect(adopted.values).not.toBe(values); expect(adopted.source).not.toBe(current); invalid(() => disposalCorrectionProposal(adopted.values, adopted.source))
  const changedOriginal = { ...original, note: 'Different original' }
  invalid(() => adoptDisposalCorrection(values, source, { ...source, disposal: { ...record, original: changedOriginal, current: changedOriginal } }))
  invalid(() => adoptDisposalCorrection(values, source, { ...current, item: { ...current.item, id: 'other' }, unit: { ...current.unit, item_id: 'other' } }))
  expect(asset.currency).toBe('JPY'); expect(record.current).toEqual(original)
})
