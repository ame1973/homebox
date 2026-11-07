import { expect, it } from 'vitest'
import type { Item } from '@/lib/api/catalog'
import type { Unit } from '@/lib/api/holdings'
import type { Asset, Cost, Disposal, Usage } from '@/lib/api/assets'
import type { AssetCostsSource, AssetLifecycleSource, AssetProfileSource, AssetUsageSource } from './model'
import * as form from './form'

const item: Item = { id: 'item', revision: 11, name: 'Owner', tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1, brand: null, model: '', category_id: null, description: '', notes: '', status: 'active' }
const unit: Unit = { id: 'unit', item_id: item.id, revision: 13, status: 'stored', asset_code: 'AST', location_id: null, serial_number: null, notes: '' }
const asset: Asset = { item_unit_id: unit.id, revision: 7, cost_basis: 'manual', currency: 'JPY', manual_acquisition_minor: 100, in_service_at: '2026-01-01T12:00:00.123456789+08:00', retired_at: null, estimated_residual_minor: 20, usage_unit: 'times' }
const profile: AssetProfileSource = { item, unit, asset }
const unprofiled: AssetProfileSource = { item, unit, asset: null }
const instant = '2026-11-07T12:00:00.123456789+08:00'
const usage: Usage = { id: 'validation', item_unit_id: unit.id, revision: 1, delta: -2, usage_unit: 'times', event_type: 'use', occurred_at: instant, note: 'Retained legacy source', correction_of: null }
const cost: Cost = { id: 'tree', item_unit_id: unit.id, revision: 1, amount_minor: 5, currency: 'JPY', kind: 'adjustment', direction: 'expense', occurred_at: instant, note: '', correction_of: null }
const uses: AssetUsageSource = { item, unit, asset, kind: 'usage', usage: [usage] }
const costs: AssetCostsSource = { item, unit, asset, kind: 'costs', costs: [cost] }
const lifecycle: AssetLifecycleSource = { item, unit, asset, disposal: null }
const disposal: Disposal = { item_unit_id: unit.id, revision: 3, kind: 'sold', currency: 'JPY', disposed_at: instant, realized_proceeds_minor: 0, note: '\0Exact disposal é\n' }
const newProfile = () => ({ ...form.assetProfileDefaults(unprofiled), cost_basis: 'manual', currency: 'JPY', manual_acquisition_minor: '0', usage_unit: 'times', service_decision: 'unknown', retirement_decision: 'unknown', residual_decision: 'unknown' })
const useValues = () => ({ ...form.assetUsageDefaults(uses), delta: '1', event_type: 'use', occurred_at: instant })
const costValues = () => ({ ...form.assetCostDefaults(costs), amount_minor: '5', kind: 'repair', direction: 'expense', occurred_at: instant })
const disposalValues = () => ({ ...form.assetDisposalDefaults(lifecycle), kind: 'sold', realized_proceeds_minor: '0', disposed_at: instant })

it('leaves new financial quantities, dates and nullable interpretation decisions unreviewed rather than guessing them', () => {
  expect(form.assetProfileDefaults(unprofiled)).toEqual({ cost_basis: '', currency: '', manual_acquisition_minor: '', usage_unit: '', service_decision: '', in_service_at: '', retirement_decision: '', retired_at: '', residual_decision: '', estimated_residual_minor: '' })
  expect(form.assetUsageDefaults(uses)).toEqual({ delta: '', usage_unit: 'times', event_type: '', occurred_at: '', correction_of: '', note: '' })
  expect(form.assetCostDefaults(costs)).toEqual({ amount_minor: '', currency: 'JPY', kind: '', direction: '', occurred_at: '', correction_of: '', note: '' })
  expect(form.assetRetireDefaults(lifecycle)).toEqual({ retired_at: '', note: '' })
  expect(form.assetDisposalDefaults(lifecycle)).toEqual({ kind: '', currency: 'JPY', realized_proceeds_minor: '', disposed_at: '', note: '' })
  for (const invoke of [() => form.assetProfileBody(form.assetProfileDefaults(unprofiled), unprofiled), () => form.assetUsageBody(form.assetUsageDefaults(uses), uses),
    () => form.assetCostBody(form.assetCostDefaults(costs), costs), () => form.assetRetireBody(form.assetRetireDefaults(lifecycle), lifecycle), () => form.assetDisposalBody(form.assetDisposalDefaults(lifecycle), lifecycle)]) expect(invoke).toThrowError(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
})
it('creates complete explicit nullable zero-money profile intent with original Item/Asset presence proofs', () => {
  const values = newProfile()
  expect(form.assetProfileBody(values, unprofiled)).toEqual({ item_revision: 11, asset_revision: null, cost_basis: 'manual', currency: 'JPY', manual_acquisition_minor: 0, in_service_at: null, retired_at: null, estimated_residual_minor: null, usage_unit: 'times' })
  const purchase = { ...values, cost_basis: 'purchase', manual_acquisition_minor: 'unconverted inactive text', residual_decision: 'value', estimated_residual_minor: '0' }
  expect(form.assetProfileBody(purchase, unprofiled)).toMatchObject({ cost_basis: 'purchase', manual_acquisition_minor: null, estimated_residual_minor: 0 })
  expect(purchase.manual_acquisition_minor).toBe('unconverted inactive text')
  const existing = form.assetProfileDefaults({ ...profile, asset: { ...asset, manual_acquisition_minor: 0, estimated_residual_minor: 0 } })
  expect(existing).toMatchObject({ manual_acquisition_minor: '0', residual_decision: 'value', estimated_residual_minor: '0', service_decision: 'value', in_service_at: asset.in_service_at })
  expect(form.assetProfileBody(existing, profile)).toMatchObject({ item_revision: 11, asset_revision: 7, manual_acquisition_minor: 0, estimated_residual_minor: 0 })
})
it('preserves unknown retained currency and exact declared nullable dates without local money registry or clock inference', () => {
  const unknown = { ...profile, asset: { ...asset, currency: 'ZZZ' } }
  expect(form.assetProfileBody(form.assetProfileDefaults(unknown), unknown)).toMatchObject({ currency: 'ZZZ', in_service_at: asset.in_service_at })
  const explicit = { ...newProfile(), service_decision: 'value', in_service_at: instant, residual_decision: 'value', estimated_residual_minor: '5' }
  expect(form.assetProfileBody(explicit, unprofiled)).toMatchObject({ in_service_at: instant, estimated_residual_minor: 5 })
  const inactive = { ...newProfile(), in_service_at: 'raw invalid date', retired_at: 'raw invalid date', estimated_residual_minor: 'raw invalid amount' }
  expect(form.assetProfileBody(inactive, unprofiled)).toMatchObject({ in_service_at: null, retired_at: null, estimated_residual_minor: null })
  expect(inactive.in_service_at).toBe('raw invalid date')
})
it('keeps financial retirement in its separate reviewed lifecycle operation and compares real nanoseconds', () => {
  expect(() => form.assetProfileBody({ ...form.assetProfileDefaults(profile), retirement_decision: 'value', retired_at: instant }, profile)).toThrowError(expect.objectContaining({ code: 'ASSET_LIFECYCLE_REQUIRED' }))
  expect(() => form.assetProfileBody({ ...newProfile(), retirement_decision: 'value', retired_at: instant }, unprofiled)).toThrowError(expect.objectContaining({ code: 'ASSET_LIFECYCLE_REQUIRED' }))
  const retired = { ...profile, unit: { ...unit, status: 'retired' as const }, asset: null }
  expect(form.assetProfileBody({ ...newProfile(), retirement_decision: 'value', retired_at: instant }, retired)).toMatchObject({ retired_at: instant })
  const known = { ...profile, asset: { ...asset, retired_at: instant } }
  expect(form.assetProfileBody({ ...form.assetProfileDefaults(known), retired_at: '2026-11-07T04:00:00.123456789Z' }, known).retired_at).toBe('2026-11-07T04:00:00.123456789Z')
  expect(() => form.assetProfileBody({ ...newProfile(), service_decision: 'value', in_service_at: '2026-11-07T04:00:00.123456790Z', retirement_decision: 'value', retired_at: instant }, retired)).toThrowError(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
  expect(() => form.assetRetireBody({ retired_at: '2026-01-01T04:00:00.123456788Z', note: '' }, lifecycle)).toThrowError(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
  const oldRetirement = { ...lifecycle, asset: { ...asset, retired_at: instant } }
  expect(() => form.assetRetireBody({ retired_at: '2026-11-08T00:00:00Z', note: '' }, oldRetirement)).toThrowError(expect.objectContaining({ code: 'ASSET_HISTORY_LOCKED' }))
  expect(form.assetRetireBody({ retired_at: '2026-11-07T04:00:00.123456789Z', note: '' }, oldRetirement).unit_revision).toBe(13)
})

it.each(['', '01', '+1', '-0', '1.0', '1e2', '1,000', ' 1', '1\n', '9007199254740992'])('rejects noncanonical/unsafe raw money %j without conversion', (raw) => {
  for (const invoke of [() => form.assetProfileBody({ ...newProfile(), manual_acquisition_minor: raw }, unprofiled),
    () => form.assetProfileBody({ ...newProfile(), residual_decision: 'value', estimated_residual_minor: raw }, unprofiled),
    () => form.assetCostBody({ ...costValues(), amount_minor: raw }, costs), () => form.assetDisposalBody({ ...disposalValues(), realized_proceeds_minor: raw }, lifecycle)]) expect(invoke).toThrowError(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
})
it.each(['0', '-0', '-01', '+1', '1.5', '-1.5', '1e3', '--1', ' 1', '9007199254740992', '-9007199254740992'])('rejects noncanonical/zero/unsafe usage delta %j', (raw) => {
  expect(() => form.assetUsageBody({ ...useValues(), delta: raw, event_type: 'correction', correction_of: usage.id }, uses)).toThrowError(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
})
it('permits exact safe signed correction intent and selected legacy targets without pretending to validate ledger prefixes', () => {
  const note = '\0  Exact é\n😀  '
  expect(form.assetUsageBody({ ...useValues(), delta: '-9007199254740991', event_type: 'correction', correction_of: usage.id, note }, uses)).toEqual({ delta: -Number.MAX_SAFE_INTEGER, usage_unit: 'times', event_type: 'correction', occurred_at: instant, correction_of: usage.id, note })
  expect(form.assetUsageBody({ ...useValues(), delta: '9007199254740991', event_type: 'correction', correction_of: usage.id }, uses).delta).toBe(Number.MAX_SAFE_INTEGER)
  expect(form.assetUsageBody({ ...useValues(), correction_of: usage.id }, uses).correction_of).toBeNull()
  expect(() => form.assetUsageBody({ ...useValues(), delta: '-1' }, uses)).toThrowError(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
  expect(() => form.assetUsageBody({ ...useValues(), event_type: 'correction', correction_of: 'missing' }, uses)).toThrowError(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
  for (const changed of [{ item_unit_id: 'other' }, { usage_unit: 'minutes' as const }]) expect(() => form.assetUsageBody({ ...useValues(), event_type: 'correction', correction_of: usage.id }, { ...uses, usage: [{ ...usage, ...changed }] })).toThrowError(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
})
it('preserves explicit cost magnitude/direction and real adjustment references without inferring a refund sign', () => {
  expect(form.assetCostBody({ ...costValues(), amount_minor: '9007199254740991', kind: 'refund', direction: 'expense', correction_of: cost.id }, costs)).toMatchObject({ amount_minor: Number.MAX_SAFE_INTEGER, kind: 'refund', direction: 'expense', correction_of: null })
  expect(form.assetCostBody({ ...costValues(), amount_minor: '2', kind: 'adjustment', direction: 'credit', correction_of: cost.id }, costs)).toMatchObject({ amount_minor: 2, direction: 'credit', correction_of: cost.id })
  for (const changed of [{ correction_of: 'missing' }, { currency: 'USD' }, { amount_minor: '-1' }]) expect(() => form.assetCostBody({ ...costValues(), kind: 'adjustment', correction_of: cost.id, ...changed }, costs)).toThrowError(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
  for (const changed of [{ item_unit_id: 'other' }, { currency: 'USD' }]) expect(() => form.assetCostBody({ ...costValues(), kind: 'adjustment', correction_of: cost.id }, { ...costs, costs: [{ ...cost, ...changed }] })).toThrowError(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
})
it.each(['sold', 'donated', 'recycled', 'discarded', 'other'])('requires explicit zero proceeds and independent Unit proof for %s', (kind) => {
  expect(form.assetDisposalBody({ ...disposalValues(), kind }, lifecycle)).toMatchObject({ unit_revision: 13, kind, realized_proceeds_minor: 0, currency: 'JPY', disposed_at: instant })
  expect(() => form.assetDisposalBody({ ...disposalValues(), kind, realized_proceeds_minor: '' }, lifecycle)).toThrowError(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
  if (kind !== 'sold') expect(() => form.assetDisposalBody({ ...disposalValues(), kind, realized_proceeds_minor: '1' }, lifecycle)).toThrowError(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
})
it('retains exact recorded disposal defaults, unknown currency and independent date decisions without resynthesizing them', () => {
  expect(form.assetDisposalDefaults({ ...lifecycle, disposal })).toEqual({ kind: 'sold', currency: 'JPY', realized_proceeds_minor: '0', disposed_at: instant, note: disposal.note })
  const retained = { ...disposal, kind: 'donated' as const, realized_proceeds_minor: 5, currency: 'ZZZ' }
  expect(form.assetDisposalDefaults({ ...lifecycle, disposal: retained })).toMatchObject({ kind: 'donated', realized_proceeds_minor: '5', currency: 'ZZZ' })
  expect(form.assetDisposalBody({ ...disposalValues(), disposed_at: '1999-01-01T00:00:00Z' }, lifecycle).disposed_at).toBe('1999-01-01T00:00:00Z')
  expect(() => form.assetDisposalBody({ ...disposalValues(), currency: 'USD' }, lifecycle)).toThrowError(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
})
it('rejects wrong owners, tracking, presence, selected collections and missing nullable lifecycle source', () => {
  for (const source of [{ ...profile, unit: { ...unit, item_id: 'other' } }, { ...profile, item: { ...item, tracking_mode: 'quantity' as const } }, { ...profile, asset: { ...asset, item_unit_id: 'other' } }]) expect(() => form.assetProfileDefaults(source)).toThrowError(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
  expect(() => form.assetUsageDefaults({ ...uses, asset: null } as never)).toThrowError(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
  expect(() => form.assetUsageBody(useValues(), { ...uses, kind: 'costs' } as never)).toThrowError(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
  expect(() => form.assetCostBody(costValues(), { ...costs, costs: undefined } as never)).toThrowError(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
  expect(() => form.assetRetireDefaults({ ...lifecycle, disposal: undefined } as never)).toThrowError(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
  expect(() => form.assetDisposalDefaults({ ...lifecycle, disposal: { ...disposal, item_unit_id: 'other' } })).toThrowError(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
})
it.each(['0000-01-01T00:00:00Z', '2026-02-30T00:00:00Z', '2026-11-07T00:00:60Z', '2026-11-07T00:00:00.1234567890Z', '0001-01-01T00:00:00+00:01', '9999-12-31T23:59:59-00:01'])('rejects malformed/UTC-overflow financial instant %s without Date rounding', (value) => {
  for (const invoke of [() => form.assetProfileBody({ ...newProfile(), service_decision: 'value', in_service_at: value }, unprofiled), () => form.assetUsageBody({ ...useValues(), occurred_at: value }, uses),
    () => form.assetCostBody({ ...costValues(), occurred_at: value }, costs), () => form.assetRetireBody({ retired_at: value, note: '' }, lifecycle), () => form.assetDisposalBody({ ...disposalValues(), disposed_at: value }, lifecycle)]) expect(invoke).toThrowError(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
})
it('keeps maximum Unicode notes exact and rejects null, overlong and lone-surrogate submitted text', () => {
  const note = '😀'.repeat(10000)
  expect(form.assetCostBody({ ...costValues(), note }, costs).note).toBe(note)
  for (const invalid of [null, '界'.repeat(10001), '\ud800']) expect(() => form.assetUsageBody({ ...useValues(), note: invalid } as never, uses)).toThrowError(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
})

it('retains the whole edited financial tuple and nullable/date pairs on explicit profile adoption without conversion', () => {
  const current = { ...profile, item: { ...item, revision: 12 }, asset: { ...asset, revision: 8, currency: 'USD', cost_basis: 'purchase' as const, manual_acquisition_minor: null,
    usage_unit: 'minutes' as const, estimated_residual_minor: 99, in_service_at: instant } }
  const clean = form.assetProfileDefaults(profile)
  expect(form.retainAssetProfileEdits(clean, profile, current)).toEqual(form.assetProfileDefaults(current))
  const draft = { ...clean, manual_acquisition_minor: '1e3 invalid', service_decision: 'value', in_service_at: 'raw invalid date', residual_decision: 'value', estimated_residual_minor: 'bad residual' }
  const retained = form.retainAssetProfileEdits(draft, profile, current)
  expect(retained).toMatchObject({ cost_basis: 'manual', currency: 'JPY', manual_acquisition_minor: '1e3 invalid', usage_unit: 'times', estimated_residual_minor: 'bad residual', in_service_at: 'raw invalid date' })
  expect(() => form.assetProfileBody(retained, current)).toThrowError(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
  const invalidRetirement = { ...clean, retirement_decision: 'value', retired_at: 'bad retired date' }
  expect(form.retainAssetProfileEdits(invalidRetirement, profile, current)).toMatchObject({ retirement_decision: 'value', retired_at: 'bad retired date' })
})
it('explicitly adopts new event interpretation while retaining raw counts/money/targets/notes, never nulling missing references', () => {
  const desiredUse = { ...useValues(), delta: '-1e3', event_type: 'correction', correction_of: usage.id, note: '\0Draft\n' }
  const currentUse = { ...uses, asset: { ...asset, revision: 8, usage_unit: 'minutes' as const }, usage: [] }
  const adoptedUse = form.adoptAssetUsage(desiredUse, uses, currentUse)
  expect(adoptedUse.values).toEqual({ ...desiredUse, usage_unit: 'minutes' }); expect(adoptedUse.source).toEqual(currentUse); expect(adoptedUse.source).not.toBe(currentUse)
  expect(() => form.assetUsageBody({ ...adoptedUse.values, delta: '-1' }, adoptedUse.source)).toThrowError(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
  const desiredCost = { ...costValues(), amount_minor: '0.5', kind: 'adjustment', correction_of: cost.id }
  const currentCost = { ...costs, asset: { ...asset, revision: 8, currency: 'USD' }, costs: [] }
  const adoptedCost = form.adoptAssetCost(desiredCost, costs, currentCost)
  expect(adoptedCost.values).toEqual({ ...desiredCost, currency: 'USD' }); expect(adoptedCost.source).toEqual(currentCost)
  expect(() => form.assetCostBody({ ...adoptedCost.values, amount_minor: '1' }, adoptedCost.source)).toThrowError(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
})
it('adopts unedited lifecycle dates/decisions as whole source while retaining changed invalid raw drafts and notes', () => {
  const current = { ...lifecycle, unit: { ...unit, revision: 14, status: 'retired' as const }, asset: { ...asset, revision: 8, retired_at: instant }, disposal }
  expect(form.retainAssetRetireEdits(form.assetRetireDefaults(lifecycle), lifecycle, current)).toEqual({ retired_at: instant, note: '' })
  expect(form.retainAssetRetireEdits({ retired_at: 'bad date', note: 'Draft' }, lifecycle, current)).toEqual({ retired_at: 'bad date', note: 'Draft' })
  expect(form.retainAssetDisposalEdits(form.assetDisposalDefaults(lifecycle), lifecycle, current)).toEqual(form.assetDisposalDefaults(current))
  expect(form.retainAssetDisposalEdits({ ...disposalValues(), realized_proceeds_minor: '1e3', note: 'Draft' }, lifecycle, { ...current, asset: { ...current.asset, currency: 'USD' } })).toMatchObject({ currency: 'USD', realized_proceeds_minor: '1e3', note: 'Draft', disposed_at: instant })
})
it('rejects cross-Unit source adoption without mutating original drafts or source', () => {
  const other = { ...lifecycle, unit: { ...unit, id: 'other' }, asset: { ...asset, item_unit_id: 'other' } }
  for (const invoke of [() => form.retainAssetProfileEdits(form.assetProfileDefaults(profile), profile, other), () => form.adoptAssetUsage(useValues(), uses, { ...other, kind: 'usage', usage: [] }),
    () => form.adoptAssetCost(costValues(), costs, { ...other, kind: 'costs', costs: [] }), () => form.retainAssetRetireEdits({ retired_at: '', note: '' }, lifecycle, other),
    () => form.retainAssetDisposalEdits(disposalValues(), lifecycle, other)]) expect(invoke).toThrowError(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
  expect(asset.currency).toBe('JPY'); expect(asset.revision).toBe(7); expect(unit.revision).toBe(13)
})
