import { beforeEach, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/client'
import type { Item } from '@/lib/api/catalog'
import type { Unit } from '@/lib/api/holdings'
import * as api from '@/lib/api/assets'
import { assetCommandReadRoots, freezeAssetCommand, sendAssetCommand, type AssetCommandInput } from './commands'

vi.mock('@/lib/api/assets', async (original) => ({ ...await original<typeof import('@/lib/api/assets')>(), putAssetProfile: vi.fn(), recordAssetUsage: vi.fn(), recordAssetCost: vi.fn(), retireAsset: vi.fn(), disposeAsset: vi.fn(),
  fetchAsset: vi.fn(), fetchAssetsPage: vi.fn(), fetchAssetUsagePage: vi.fn(), fetchAssetCostsPage: vi.fn(), fetchAssetDisposal: vi.fn(), fetchAssetCostSummary: vi.fn() }))
const item: Item = { id: 'item', revision: 11, name: 'Original owner', tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1, brand: null, model: '', category_id: null, description: '', notes: '', status: 'active' }
const unit: Unit = { id: 'unit', item_id: item.id, revision: 13, asset_code: 'AST', status: 'stored', location_id: null, serial_number: null, notes: 'Original physical context' }
const asset: api.Asset = { item_unit_id: unit.id, revision: 7, cost_basis: 'manual', currency: 'JPY', manual_acquisition_minor: 100, estimated_residual_minor: null, in_service_at: null, retired_at: null, usage_unit: 'times' }
const instant = '2026-11-07T12:00:00.123456789+08:00'
const usage: api.Usage = { id: 'validation', item_unit_id: unit.id, revision: 1, delta: -2, usage_unit: 'times', event_type: 'use', occurred_at: instant, note: 'Legacy without provenance', correction_of: null }
const cost: api.Cost = { id: 'tree', item_unit_id: unit.id, revision: 1, amount_minor: 5, currency: 'JPY', kind: 'adjustment', direction: 'expense', occurred_at: instant, note: '', correction_of: null }
const disposal: api.Disposal = { item_unit_id: unit.id, revision: 3, kind: 'sold', disposed_at: instant, currency: 'JPY', realized_proceeds_minor: 0, note: '' }
const owner = { item, unit, asset }
const useBody = { delta: 1, usage_unit: 'times', event_type: 'use', occurred_at: instant, note: '\0Exact use é\n' } as const
const costBody = { amount_minor: 5, currency: 'JPY', kind: 'repair', direction: 'expense', occurred_at: instant } as const
const proposals: AssetCommandInput[] = [
  { kind: 'put-profile', source: owner, body: { item_revision: 11, asset_revision: 7, cost_basis: 'manual', currency: 'JPY', manual_acquisition_minor: 100, in_service_at: null, retired_at: null, estimated_residual_minor: null, usage_unit: 'times' } },
  { kind: 'record-usage', source: { ...owner, kind: 'usage', usage: [usage] }, body: useBody },
  { kind: 'record-cost', source: { ...owner, kind: 'costs', costs: [cost] }, body: costBody },
  { kind: 'retire-asset', source: { ...owner, disposal: null }, body: { unit_revision: 13, retired_at: instant } },
  { kind: 'dispose-asset', source: { ...owner, disposal: null }, body: { unit_revision: 13, kind: 'other', disposed_at: instant, realized_proceeds_minor: 0, currency: 'JPY' } },
]
const writers = [api.putAssetProfile, api.recordAssetUsage, api.recordAssetCost, api.retireAsset, api.disposeAsset]
const reads = [api.fetchAsset, api.fetchAssetsPage, api.fetchAssetUsagePage, api.fetchAssetCostsPage, api.fetchAssetDisposal, api.fetchAssetCostSummary]
beforeEach(() => {
  vi.mocked(api.putAssetProfile).mockReset().mockResolvedValue(asset)
  vi.mocked(api.recordAssetUsage).mockReset().mockResolvedValue({ ...usage, ...useBody, revision: 1 })
  vi.mocked(api.recordAssetCost).mockReset().mockResolvedValue({ ...cost, ...costBody, revision: 1 })
  vi.mocked(api.retireAsset).mockReset().mockResolvedValue({ ...unit, revision: 14, status: 'retired' })
  vi.mocked(api.disposeAsset).mockReset().mockResolvedValue({ ...disposal, revision: 1, kind: 'other' })
  for (const read of reads) vi.mocked(read).mockReset()
})

it.each(proposals)('freezes one complete volatile $kind without credentials or a supplied key', (proposal) => {
  const first = freezeAssetCommand(proposal), second = freezeAssetCommand(proposal)
  expect(Object.isFrozen(first)).toBe(true); expect(first.key).toMatch(/^[0-9a-f-]{36}$/); expect(second.key).not.toBe(first.key)
  expect(first).toEqual({ ...proposal, key: first.key }); expect(first.source).not.toBe(proposal.source); expect(first.body).not.toBe(proposal.body)
  expect(Object.keys(first).sort()).toEqual(['body', 'key', 'kind', 'source'])
  expect(first).not.toHaveProperty('csrfToken'); expect(first).not.toHaveProperty('signal'); expect(first).not.toHaveProperty('expectedRevision')
})
it('snapshots complete original history/proofs/body independently of later draft and source changes', () => {
  const proposal = { kind: 'record-usage' as const, source: { item: { ...item }, unit: { ...unit }, asset: { ...asset }, kind: 'usage' as const, usage: [{ ...usage }] }, body: { ...useBody, note: String(useBody.note) } }
  const frozen = freezeAssetCommand(proposal)
  proposal.source.item.revision = 99; proposal.source.unit.revision = 99; proposal.source.asset.currency = 'USD'; proposal.source.usage[0]!.delta = 999; proposal.body.note = 'Changed raw draft'
  expect(frozen).toEqual({ ...proposals[1], key: frozen.key })
})
it.each(proposals)('delivers $kind once with exact original scope, caller-owned CSRF/key/signal and no reads', async (proposal) => {
  const command = freezeAssetCommand(proposal), signal = new AbortController().signal
  const context = { csrfToken: 'fresh-csrf', idempotencyKey: command.key, signal }
  await sendAssetCommand(command, context.csrfToken, signal)
  if (command.kind === 'put-profile') expect(api.putAssetProfile).toHaveBeenCalledExactlyOnceWith(unit.id, command.body, { ...context, ...owner })
  if (command.kind === 'record-usage') expect(api.recordAssetUsage).toHaveBeenCalledExactlyOnceWith(unit.id, command.body, { ...context, asset })
  if (command.kind === 'record-cost') expect(api.recordAssetCost).toHaveBeenCalledExactlyOnceWith(unit.id, command.body, { ...context, asset })
  if (command.kind === 'retire-asset') expect(api.retireAsset).toHaveBeenCalledExactlyOnceWith(unit.id, command.body, { ...context, asset, unit })
  if (command.kind === 'dispose-asset') expect(api.disposeAsset).toHaveBeenCalledExactlyOnceWith(unit.id, command.body, { ...context, asset, unit, disposal: null })
  expect(writers.reduce((sum, write) => sum + vi.mocked(write).mock.calls.length, 0)).toBe(1)
  for (const read of reads) expect(read).not.toHaveBeenCalled()
})
it('retains explicit nullable opt-in and retained Disposal context without inventing another wire proof', async () => {
  const profile = proposals[0]!
  if (profile.kind !== 'put-profile') throw new Error('Expected profile fixture')
  const created = freezeAssetCommand({ ...profile, source: { ...owner, asset: null }, body: { ...profile.body, asset_revision: null } })
  await sendAssetCommand(created, 'csrf', new AbortController().signal)
  expect(api.putAssetProfile).toHaveBeenCalledWith(unit.id, created.body, expect.objectContaining({ unit, item, asset: null }))
  const dispose = proposals[4]!
  if (dispose.kind !== 'dispose-asset') throw new Error('Expected disposal fixture')
  const retained = freezeAssetCommand({ ...dispose, source: { ...owner, unit: { ...unit, status: 'disposed' }, disposal } })
  await sendAssetCommand(retained, 'csrf', new AbortController().signal)
  expect(api.disposeAsset).toHaveBeenCalledWith(unit.id, retained.body, expect.objectContaining({ disposal, unit: { ...unit, status: 'disposed' } }))
})
it('requires real selected correction targets while retaining all broad legacy rows and large complete collections', () => {
  const rows = Array.from({ length: 205 }, (_, index) => ({ ...usage, id: `legacy-${index}` }))
  const body = { ...useBody, event_type: 'correction' as const, delta: -1, correction_of: rows[204]!.id }
  expect(freezeAssetCommand({ kind: 'record-usage', source: { ...owner, kind: 'usage', usage: rows }, body }).source).toMatchObject({ usage: rows })
  expect(freezeAssetCommand({ kind: 'record-cost', source: { ...owner, kind: 'costs', costs: [cost] }, body: { ...costBody, kind: 'adjustment', direction: 'credit', correction_of: cost.id } }).body).toMatchObject({ correction_of: cost.id, direction: 'credit' })
})
it.each([
  { ...proposals[0], kind: 'unknown' }, { ...proposals[0], csrfToken: 'must-not-be-retained' }, { ...proposals[0], expectedRevision: 7 },
  { ...proposals[0], source: { ...owner, asset: null } }, { ...proposals[0], body: { ...(proposals[0]!.body as object), item_revision: 12 } },
  { ...proposals[0], source: { ...owner, unit: { ...unit, id: '/' } } }, { ...proposals[0], source: { ...owner, unit: { ...unit, item_id: 'other' } } },
  { ...proposals[0], source: { ...owner, item: { ...item, tracking_mode: 'quantity' } } }, { ...proposals[0], source: { ...owner, asset: { ...asset, item_unit_id: 'other' } } },
  { ...proposals[1], source: { ...owner, kind: 'usage', usage: null } }, { ...proposals[1], source: { ...owner, kind: 'usage', usage: [usage, usage] } },
  { ...proposals[1], source: { ...owner, kind: 'usage', usage: [{ ...usage, item_unit_id: 'other' }] } },
  { ...proposals[1], body: { ...useBody, usage_unit: 'minutes' } },
  { ...proposals[1], body: { ...useBody, event_type: 'correction', correction_of: 'missing' } },
  { ...proposals[1], source: { ...owner, kind: 'usage', usage: [{ ...usage, usage_unit: 'minutes' }] }, body: { ...useBody, event_type: 'correction', correction_of: usage.id } },
  { ...proposals[2], source: { ...owner, kind: 'costs', costs: [cost, cost] } }, { ...proposals[2], source: { ...owner, kind: 'costs', costs: [{ ...cost, item_unit_id: 'other' }] } },
  { ...proposals[2], body: { ...costBody, currency: 'USD' } }, { ...proposals[2], body: { ...costBody, kind: 'adjustment', correction_of: 'missing' } },
  { ...proposals[2], source: { ...owner, kind: 'costs', costs: [{ ...cost, currency: 'USD' }] }, body: { ...costBody, kind: 'adjustment', correction_of: cost.id } },
  { ...proposals[3], source: owner }, { ...proposals[3], body: { unit_revision: 7, retired_at: instant } },
  { ...proposals[4], source: { ...owner, disposal: { ...disposal, item_unit_id: 'other' } } },
  { ...proposals[4], body: { unit_revision: 13, kind: 'sold', disposed_at: instant, currency: 'USD', realized_proceeds_minor: 0 } },
])('rejects malformed original command %# before review or any dispatch', async (proposal) => {
  expect(() => freezeAssetCommand(proposal as never)).toThrowError(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
  await expect(sendAssetCommand({ ...proposal, key: 'original-command' } as never, 'csrf', new AbortController().signal)).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
  for (const write of writers) expect(write).not.toHaveBeenCalled()
})
it('rejects an invalid retained key and reconciles only an explicit identical original command with fresh CSRF', async () => {
  const command = freezeAssetCommand(proposals[1]!)
  await expect(sendAssetCommand({ ...command, key: 'short' }, 'csrf', new AbortController().signal)).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
  vi.mocked(api.recordAssetUsage).mockRejectedValueOnce(new ApiError('INVALID_RESPONSE')).mockResolvedValueOnce(usage)
  const first = new AbortController().signal, second = new AbortController().signal
  await expect(sendAssetCommand(command, 'old-csrf', first)).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  window.dispatchEvent(new Event('online')); expect(api.recordAssetUsage).toHaveBeenCalledTimes(1)
  expect(await sendAssetCommand(command, 'fresh-csrf', second)).toEqual(usage)
  expect(api.recordAssetUsage).toHaveBeenNthCalledWith(1, unit.id, command.body, { csrfToken: 'old-csrf', idempotencyKey: command.key, signal: first, asset })
  expect(api.recordAssetUsage).toHaveBeenNthCalledWith(2, unit.id, command.body, { csrfToken: 'fresh-csrf', idempotencyKey: command.key, signal: second, asset })
  for (const read of reads) expect(read).not.toHaveBeenCalled()
})
it.each(proposals)('invalidates financial/owner roots and only possible physical location scopes for $kind', (proposal) => {
  const roots = assetCommandReadRoots(freezeAssetCommand(proposal))
  expect(roots).toEqual([['assets'], ['catalog'], ['holdings'], ['purchases'], ['media'], ...(proposal.kind === 'retire-asset' || proposal.kind === 'dispose-asset' ? [['locations']] : [])])
})
it.each(['retired', 'disposed'] as const)('does not invalidate unrelated locations for nonphysical retained %s retirement', (status) => {
  const proposal = proposals[3]!
  if (proposal.kind !== 'retire-asset') throw new Error('Expected retirement fixture')
  expect(assetCommandReadRoots(freezeAssetCommand({ ...proposal, source: { ...owner, unit: { ...unit, status }, disposal: status === 'disposed' ? disposal : null } }))).not.toContainEqual(['locations'])
})
it('keeps financial adoption of an already disposed Unit separate from another physical removal', () => {
  const proposal = proposals[4]!
  if (proposal.kind !== 'dispose-asset') throw new Error('Expected disposal fixture')
  expect(assetCommandReadRoots(freezeAssetCommand({ ...proposal, source: { ...owner, unit: { ...unit, status: 'disposed' }, disposal: null } }))).not.toContainEqual(['locations'])
})
