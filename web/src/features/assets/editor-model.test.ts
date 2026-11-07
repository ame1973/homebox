import { expect, it } from 'vitest'
import type { ResolverOptions } from 'react-hook-form'
import { messages } from '@/lib/messages'
import { assetEditorDefaults, assetEditorProposal, assetEditorResolver, assetFieldVisible, editorFields, retainAssetEditorEdits, type AssetEditorData, type AssetEditorValues } from './editor-model'
import { assetCommandCurrent, assetInterpretation, assetValue, sameAssetSource } from './display'
import { freezeAssetCommand } from './commands'

const item = { id: 'item', revision: 11, name: 'Owner', brand: null, model: '', category_id: null, description: '', notes: '', tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1, status: 'active' } as const
const unit = { id: 'unit', item_id: item.id, revision: 13, asset_code: 'AST', serial_number: null, notes: '', status: 'stored', location_id: null } as const
const asset = { item_unit_id: unit.id, revision: 7, cost_basis: 'manual', currency: 'JPY', manual_acquisition_minor: 100, in_service_at: null, retired_at: null, estimated_residual_minor: null, usage_unit: 'times' } as const
const instant = '2026-11-07T10:00:00.123456789+08:00'
const usage = { id: 'use', revision: 1, item_unit_id: unit.id, delta: 2, event_type: 'use', usage_unit: 'times', occurred_at: instant, note: '', correction_of: null } as const
const cost = { id: 'cost', revision: 1, item_unit_id: unit.id, amount_minor: 5, currency: 'JPY', kind: 'repair', direction: 'expense', occurred_at: instant, note: '', correction_of: null } as const
const owner = { item, unit, asset }
const scopes: AssetEditorData[] = [{ kind: 'put-profile', source: owner }, { kind: 'record-usage', source: { ...owner, kind: 'usage', usage: [usage] } }, { kind: 'record-cost', source: { ...owner, kind: 'costs', costs: [cost] } }, { kind: 'retire-asset', source: { ...owner, disposal: null } }, { kind: 'dispose-asset', source: { ...owner, disposal: null } }]
function valid(data: AssetEditorData): AssetEditorValues {
  const defaults = assetEditorDefaults(data)
  if (data.kind === 'record-usage') return { ...defaults, event_type: 'correction', delta: '-1', occurred_at: instant, correction_of: usage.id, note: '\0Exact correction é\n' }
  if (data.kind === 'record-cost') return { ...defaults, kind: 'adjustment', direction: 'credit', amount_minor: '2', occurred_at: instant, correction_of: cost.id }
  if (data.kind === 'retire-asset') return { ...defaults, retired_at: instant }
  if (data.kind === 'dispose-asset') return { ...defaults, kind: 'other', realized_proceeds_minor: '0', disposed_at: '1999-01-01T00:00:00Z' }
  return defaults
}
async function errors(data: AssetEditorData, values: AssetEditorValues) {
  const options: ResolverOptions<AssetEditorValues> = { fields: {}, shouldUseNativeValidation: false }
  return (await assetEditorResolver(data)(values, undefined, options)).errors
}
it.each(scopes)('uses the actual $kind raw defaults, pure proposal and independent proof scopes', async (data) => {
  const values = valid(data)
  expect(Object.keys(assetEditorDefaults(data)).sort()).toEqual([...editorFields[data.kind]].sort())
  expect(await errors(data, values)).toEqual({})
  const proposal = assetEditorProposal(data, values)
  expect(proposal.kind).toBe(data.kind); expect(proposal.source).toBe(data.source)
  expect(freezeAssetCommand(proposal)).toMatchObject(proposal)
  expect(retainAssetEditorEdits(values, data, structuredClone(data))).toEqual(values)
})
it.each(scopes)('requires explicit blank new $kind choices before creating any proposal', async (data) => {
  if (data.kind === 'put-profile') {
    const absent = { ...data, source: { ...owner, asset: null } }
    expect(await errors(absent, assetEditorDefaults(absent))).toMatchObject({ cost_basis: {}, currency: {}, usage_unit: {}, service_decision: {}, retirement_decision: {}, residual_decision: {} })
  } else expect(Object.keys(await errors(data, assetEditorDefaults(data))).length).toBeGreaterThan(0)
})
it.each(scopes)('rejects a different operation during $kind adoption instead of reinterpreting its draft', (current) => {
  const before = scopes.find((scope) => scope.kind !== current.kind)!
  expect(() => retainAssetEditorEdits(valid(before), before, current)).toThrow()
})
it('preserves invalid financial/date groups and raw event text through explicit complete interpretation adoption', () => {
  const profile = scopes[0]!, before = { ...owner, asset: { ...asset } }, current = { ...before, asset: { ...asset, revision: 8, currency: 'USD' } }
  const values = { ...assetEditorDefaults(profile), manual_acquisition_minor: '1.5', in_service_at: 'invalid', service_decision: 'value' }
  expect(retainAssetEditorEdits(values, { kind: 'put-profile', source: before }, { kind: 'put-profile', source: current })).toEqual(values)
  const usageBefore = { ...before, kind: 'usage' as const, usage: [usage] }, usageCurrent = { ...usageBefore, asset: { ...current.asset, usage_unit: 'minutes' as const }, usage: [] }
  const rawUse = { ...valid(scopes[1]!), delta: 'bad', correction_of: usage.id }
  expect(retainAssetEditorEdits(rawUse, { kind: 'record-usage', source: usageBefore }, { kind: 'record-usage', source: usageCurrent })).toEqual({ ...rawUse, usage_unit: 'minutes' })
  const rawCost = { ...valid(scopes[2]!), amount_minor: 'bad', correction_of: cost.id }
  expect(retainAssetEditorEdits(rawCost, scopes[2]!, { kind: 'record-cost', source: { ...current, kind: 'costs', costs: [] } })).toEqual({ ...rawCost, currency: 'USD' })
})
it.each(['', '01', '1.5', '1e2', '9007199254740992', '-0', '+1', ' 1'])('rejects noncanonical raw usage/cost/proceeds %j without repair', async (value) => {
  expect(await errors(scopes[1]!, { ...valid(scopes[1]!), delta: value })).toHaveProperty('delta')
  expect(await errors(scopes[2]!, { ...valid(scopes[2]!), amount_minor: value })).toHaveProperty('amount_minor')
  expect(await errors(scopes[4]!, { ...valid(scopes[4]!), realized_proceeds_minor: value })).toHaveProperty('realized_proceeds_minor')
})
it('validates exact nullable/value date pairs and safe zero amounts without installing defaults', async () => {
  const data = scopes[0]!, values = valid(data)
  expect(await errors(data, { ...values, cost_basis: 'purchase', manual_acquisition_minor: 'inactive invalid draft', residual_decision: 'value', estimated_residual_minor: '0', service_decision: 'value', in_service_at: instant })).toEqual({})
  expect(await errors(data, { ...values, cost_basis: 'unknown', currency: 'jpy', usage_unit: 'hours', service_decision: 'value', in_service_at: 'bad', residual_decision: 'value', estimated_residual_minor: '' })).toMatchObject({ cost_basis: {}, currency: {}, usage_unit: {}, in_service_at: {}, estimated_residual_minor: {} })
  expect(await errors(data, { ...values, service_decision: 'value', in_service_at: instant, retirement_decision: 'value', retired_at: '2026-01-01T00:00:00Z' })).toHaveProperty('in_service_at')
  expect(await errors(data, { ...values, retirement_decision: 'value', retired_at: instant })).toHaveProperty('retirement_decision')
  expect(await errors(scopes[3]!, { ...valid(scopes[3]!), retired_at: 'bad' })).toHaveProperty('retired_at')
  const retired: AssetEditorData = { kind: 'retire-asset', source: { ...owner, asset: { ...asset, retired_at: instant }, disposal: null } }
  expect(await errors(retired, { ...valid(retired), retired_at: '2026-11-08T00:00:00Z' })).toHaveProperty('retired_at')
})
it('requires actual same-interpretation targets, explicit direction and non-sale zero without deriving totals', async () => {
  expect(await errors(scopes[1]!, { ...valid(scopes[1]!), event_type: 'use', delta: '-1' })).toHaveProperty('delta')
  expect(await errors(scopes[1]!, { ...valid(scopes[1]!), usage_unit: 'minutes', correction_of: 'missing' })).toMatchObject({ usage_unit: {}, correction_of: {} })
  expect(await errors(scopes[2]!, { ...valid(scopes[2]!), currency: 'USD', direction: 'inferred', correction_of: 'missing', kind: 'adjustment' })).toMatchObject({ currency: {}, direction: {}, correction_of: {} })
  expect(await errors(scopes[2]!, { ...valid(scopes[2]!), kind: 'unknown', note: '\ud800' })).toMatchObject({ kind: {}, note: {} })
  expect(await errors(scopes[4]!, { ...valid(scopes[4]!), kind: 'unknown', disposed_at: 'bad' })).toMatchObject({ kind: {}, disposed_at: {} })
  expect(await errors(scopes[4]!, { ...valid(scopes[4]!), realized_proceeds_minor: '1' })).toHaveProperty('realized_proceeds_minor')
  expect(await errors(scopes[2]!, { ...valid(scopes[2]!), amount_minor: '0' })).toHaveProperty('amount_minor')
})
it('keeps inactive controllers registered while only rendering fields required by the explicit decision', () => {
  for (const [name, flag] of [['manual_acquisition_minor', 'cost_basis'], ['in_service_at', 'service_decision'], ['retired_at', 'retirement_decision'], ['estimated_residual_minor', 'residual_decision']] as const) {
    expect(assetFieldVisible('put-profile', name, {})).toBe(false)
    expect(assetFieldVisible('put-profile', name, { [flag]: name === 'manual_acquisition_minor' ? 'manual' : 'value' })).toBe(true)
  }
  expect(assetFieldVisible('record-usage', 'correction_of', { event_type: 'use' })).toBe(false)
  expect(assetFieldVisible('record-usage', 'correction_of', { event_type: 'correction' })).toBe(true)
  expect(assetFieldVisible('record-cost', 'correction_of', { kind: 'adjustment' })).toBe(true)
  expect(assetFieldVisible('record-cost', 'note', {})).toBe(true)
})
it('compares full selected review context while excluding display-only calculation metadata', () => {
  expect(sameAssetSource(undefined, undefined)).toBe(true); expect(sameAssetSource(owner, null)).toBe(false)
  expect(sameAssetSource({ ...owner, asset: null }, { ...owner, asset: null })).toBe(true)
  for (const key of ['item', 'unit', 'asset'] as const) expect(sameAssetSource(owner, { ...owner, [key]: { ...owner[key], revision: 99 } })).toBe(false)
  const uses = { ...owner, kind: 'usage' as const, usage: [usage] }, costs = { ...owner, kind: 'costs' as const, costs: [cost] }
  expect(sameAssetSource(uses, { ...uses, usage: [] })).toBe(false); expect(sameAssetSource(uses, { ...uses, usage: [{ ...usage, note: 'changed' }] })).toBe(false)
  expect(sameAssetSource(costs, { ...costs, costs: [{ ...cost, amount_minor: 9 }] })).toBe(false); expect(sameAssetSource(uses, costs)).toBe(false)
  expect(sameAssetSource(owner, { ...owner, disposal: null })).toBe(false)
  const disposal = { item_unit_id: unit.id, revision: 3, kind: 'other' as const, currency: 'JPY', realized_proceeds_minor: 0, disposed_at: instant, note: '' }
  expect(sameAssetSource({ ...owner, disposal }, { ...owner, disposal: { ...disposal, note: 'changed' } })).toBe(false)
  expect(sameAssetSource({ ...owner, disposal: null }, { ...owner, disposal })).toBe(false)
  expect(sameAssetSource({ ...owner, disposal }, { ...owner, disposal: { ...disposal } })).toBe(true)
  expect(assetCommandCurrent(freezeAssetCommand(assetEditorProposal(scopes[1]!, valid(scopes[1]!))), uses)).toBe(true)
  expect(assetInterpretation(owner)).not.toBe(assetInterpretation({ ...owner, asset: null }))
})
it('formats actual nullable fields, enum meaning and exact control characters without money conversion', () => {
  expect(assetValue('manual_acquisition_minor', 0)).toBe('0'); expect(assetValue('manual_acquisition_minor', -97)).toBe('-97')
  expect(assetValue('currency', null)).toBe(messages.assets.unknownValue); expect(assetValue('currency', undefined)).toBe(messages.assets.unknownValue)
  expect(assetValue('note', '\0Exact é\n')).toBe(JSON.stringify('\0Exact é\n'))
  expect(assetValue('usage_unit', 'times')).toBe(`${messages.assets.usageUnits.times} (times)`)
})
