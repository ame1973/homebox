import { beforeEach, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/client'
import type { Asset } from '@/lib/api/assets'
import type { Item } from '@/lib/api/catalog'
import * as api from '@/lib/api/financial-corrections'
import type { Unit } from '@/lib/api/holdings'
import type { Purchase, PurchaseLine } from '@/lib/api/purchases'
import type { DisposalCorrectionSource } from '@/features/assets/correction-model'
import type { PurchaseCorrectionSource } from '@/features/purchases/correction-model'
import { freezeCorrectionCommand, sendCorrectionCommand, type CorrectionCommandInput } from './commands'

vi.mock('@/lib/api/financial-corrections', async (original) => ({ ...await original<typeof import('@/lib/api/financial-corrections')>(),
  correctPurchaseLine: vi.fn(), correctDisposal: vi.fn(), fetchPurchaseCorrectionsPage: vi.fn(), fetchDisposalRecord: vi.fn(), fetchDisposalCorrectionsPage: vi.fn() }))
const item: Item = { id: 'item', revision: 7, name: 'Original', tracking_mode: 'quantity', unit_code: 'kg', base_unit: 'g', scale_factor: 1000, brand: null, model: '', category_id: null, description: '', notes: '', status: 'active' }
const target: Item = { ...item, id: 'target', revision: 9, tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1 }
const unit: Unit = { id: 'unit', item_id: target.id, revision: 3, status: 'disposed', asset_code: 'Original', location_id: null, serial_number: null, notes: '' }
const purchase: Purchase = { id: 'purchase', revision: 12, merchant_name: 'Merchant', purchased_at: '2026-11-07T00:00:00.000000001Z', currency: 'JPY', receipt_note: '' }
const line: PurchaseLine = { id: 'line/legacy', revision: 4, purchase_id: purchase.id, item_id: item.id, item_unit_id: null, quantity_base: 1500, paid_minor: 0, note: 'Original' }
const purchaseSource: PurchaseCorrectionSource = { purchase, line, item, lines: [line], corrections: [], originalUnit: { kind: 'unallocated' }, replacement: { kind: 'individual', item: target, units: [unit] } }
const instant = '2026-11-07T12:00:00.123456789+08:00'
const asset: Asset = { item_unit_id: unit.id, revision: 8, cost_basis: 'manual', currency: 'JPY', manual_acquisition_minor: 100, in_service_at: null, retired_at: null, estimated_residual_minor: 20, usage_unit: 'times' }
const disposalData: api.DisposalData = { item_unit_id: unit.id, kind: 'sold', currency: 'JPY', realized_proceeds_minor: 200, disposed_at: instant, note: 'Original' }
const record: api.DisposalRecord = { item_unit_id: unit.id, revision: 9, original: disposalData, current: disposalData }
const voided = { ...record, revision: 10, current: null } as const satisfies api.DisposalRecord
const disposalEntry: api.DisposalCorrection = { id: 'entry', revision: 1, item_unit_id: unit.id, action: 'void', reason: 'Earlier void', created_at: instant, before: record, after: voided }
const disposalSource: DisposalCorrectionSource = { item: target, unit, asset, disposal: record, corrections: [] }
const voidSource: DisposalCorrectionSource = { ...disposalSource, disposal: voided, corrections: [disposalEntry] }
const purchaseVoid = { line_id: line.id, line_revision: line.revision, item_revision: item.revision, action: 'void', replacement: null, reason: '\0Void exact é' } as const
const purchaseReplace = { ...purchaseVoid, action: 'replace', replacement: { item_id: target.id, item_revision: target.revision, item_unit_id: unit.id, quantity_base: 1, paid_minor: 0 } } as const
const disposalVoid = { asset_revision: asset.revision, action: 'void', replacement: null, reason: 'Void finance' } as const
const disposalReplace = { ...disposalVoid, action: 'replace', replacement: { kind: 'sold', currency: 'JPY', realized_proceeds_minor: 0, disposed_at: instant } } as const
const proposals: CorrectionCommandInput[] = [
  { kind: 'correct-purchase', source: purchaseSource, body: purchaseVoid, unit: null },
  { kind: 'correct-purchase', source: purchaseSource, body: purchaseReplace, unit },
  { kind: 'correct-disposal', source: disposalSource, body: disposalVoid },
  { kind: 'correct-disposal', source: voidSource, body: disposalReplace },
]
const purchaseEntry: api.PurchaseLineCorrection = { id: 'purchase-entry', revision: 1, purchase_id: purchase.id, action: 'void', reason: purchaseVoid.reason, created_at: instant, before: line, after: null }
const reads = [api.fetchPurchaseCorrectionsPage, api.fetchDisposalRecord, api.fetchDisposalCorrectionsPage]
beforeEach(() => {
  vi.mocked(api.correctPurchaseLine).mockReset().mockResolvedValue(purchaseEntry)
  vi.mocked(api.correctDisposal).mockReset().mockResolvedValue(disposalEntry)
  for (const read of reads) vi.mocked(read).mockReset()
})

it.each(proposals)('freezes complete $kind review context, body and independent key without credentials', (proposal) => {
  const command = freezeCorrectionCommand(proposal), another = freezeCorrectionCommand(proposal)
  expect(command).toEqual({ ...proposal, key: command.key }); expect(command.key).toMatch(/^[0-9a-f-]{36}$/); expect(command.key).not.toBe(another.key)
  expect(Object.isFrozen(command)).toBe(true); expect(Object.isFrozen(command.source)).toBe(true); expect(Object.isFrozen(command.body)).toBe(true)
  expect(Object.isFrozen(command.source.corrections)).toBe(true); expect(Reflect.set(command.body, 'reason', 'changed')).toBe(false)
  expect(Reflect.set(command.source.item, 'revision', 99)).toBe(false); expect(command.source).not.toBe(proposal.source)
  expect(command).not.toHaveProperty('csrfToken'); expect(command).not.toHaveProperty('signal'); expect(command).not.toHaveProperty('expectedRevision')
})
it('keeps original source and complete Unit/history snapshots independent of draft mutation', () => {
  const proposal = structuredClone(proposals[1]!), command = freezeCorrectionCommand(proposal)
  Object.assign(proposal.source.item, { revision: 99 }); Object.assign(proposal.body, { reason: 'changed' })
  if (proposal.kind !== 'correct-purchase' || proposal.source.replacement.kind !== 'individual') throw new Error('Expected replacement fixture')
  Object.assign(proposal.source.replacement.units[0]!, { notes: 'Changed' })
  expect(command).toMatchObject({ body: purchaseReplace, source: { item, replacement: { item: target, units: [unit] } } })
})
it.each(proposals)('dispatches $kind exactly once with original proofs, caller CSRF/key/signal and no reads', async (proposal) => {
  const command = freezeCorrectionCommand(proposal), signal = new AbortController().signal
  await sendCorrectionCommand(command, 'csrf', signal)
  const context = { csrfToken: 'csrf', idempotencyKey: command.key, signal }
  if (command.kind === 'correct-purchase') {
    expect(api.correctPurchaseLine).toHaveBeenCalledExactlyOnceWith(purchase.id, command.body, { ...context, purchase, line, item, replacementItem: command.body.action === 'void' ? null : target })
    expect(api.correctDisposal).not.toHaveBeenCalled()
  } else {
    expect(api.correctDisposal).toHaveBeenCalledExactlyOnceWith(unit.id, command.body, { ...context, asset, disposal: command.source.disposal })
    expect(api.correctPurchaseLine).not.toHaveBeenCalled()
  }
  for (const read of reads) expect(read).not.toHaveBeenCalled()
})
it('permits explicit unallocated replacement while not inventing a Unit context', async () => {
  const body = { ...purchaseReplace, replacement: { ...purchaseReplace.replacement, item_unit_id: null, quantity_base: 5 } } as const
  const command = freezeCorrectionCommand({ kind: 'correct-purchase', source: purchaseSource, body, unit: null })
  await sendCorrectionCommand(command, 'csrf', new AbortController().signal)
  expect(api.correctPurchaseLine).toHaveBeenCalledWith(purchase.id, body, expect.objectContaining({ replacementItem: target }))
})
it.each([
  { ...proposals[0], kind: 'unknown' }, { ...proposals[0], csrfToken: 'must-not-be-retained' }, { ...proposals[0], expectedRevision: 12 },
  { ...proposals[0], unit }, { ...proposals[0], body: { ...purchaseVoid, line_id: 'other' } },
  { ...proposals[0], body: { ...purchaseVoid, line_revision: 5 } }, { ...proposals[0], body: { ...purchaseVoid, item_revision: 8 } },
  { ...proposals[0], source: { ...purchaseSource, lines: [] } }, { ...proposals[0], body: { ...purchaseVoid, reason: '' } },
  { ...proposals[1], source: { ...purchaseSource, replacement: { kind: 'none' } } },
  { ...proposals[1], body: { ...purchaseReplace, replacement: { ...purchaseReplace.replacement, item_id: 'other' } } },
  { ...proposals[1], body: { ...purchaseReplace, replacement: { ...purchaseReplace.replacement, item_revision: 10 } } },
  { ...proposals[1], body: { ...purchaseReplace, replacement: { ...purchaseReplace.replacement, item_unit_id: null, quantity_base: 1 } }, unit },
  { ...proposals[1], unit: { ...unit, id: 'missing' } }, { ...proposals[1], unit: { ...unit, notes: 'Changed' } },
  { ...proposals[1], source: { ...purchaseSource, replacement: { kind: 'individual', item: target, units: [] } } },
  { ...proposals[1], body: { ...purchaseReplace, replacement: { ...purchaseReplace.replacement, quantity_base: 2 } } },
  { ...proposals[2], unit }, { ...proposals[2], body: { ...disposalVoid, asset_revision: 9 } },
  { ...proposals[2], source: voidSource }, { ...proposals[2], source: { ...disposalSource, unit: { ...unit, item_id: 'other' } } },
  { ...proposals[3], body: { ...disposalReplace, replacement: { ...disposalReplace.replacement, currency: 'USD' } } },
  { ...proposals[3], body: { ...disposalReplace, replacement: { ...disposalReplace.replacement, kind: 'other', realized_proceeds_minor: 1 } } },
])('rejects malformed, cross-owner or unreviewed command %# before freeze and dispatch', async (proposal) => {
  expect(() => freezeCorrectionCommand(proposal as never)).toThrowError(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
  await expect(sendCorrectionCommand({ ...proposal, key: 'original-key' } as never, 'csrf', new AbortController().signal)).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
  expect(api.correctPurchaseLine).not.toHaveBeenCalled(); expect(api.correctDisposal).not.toHaveBeenCalled()
})
it('rejects a named Unit under a quantity interpretation without silently making it null', () => {
  const quantity = { ...target, tracking_mode: 'quantity' as const }
  const proposal = { kind: 'correct-purchase', source: { ...purchaseSource, replacement: { kind: 'quantity', item: quantity } }, body: purchaseReplace, unit }
  expect(() => freezeCorrectionCommand(proposal as never)).toThrowError(expect.objectContaining({ code: 'TRACKING_MODE_CONFLICT' }))
})
it('rejects an invalid retained key and reconciles only the explicit identical original command', async () => {
  const command = freezeCorrectionCommand(proposals[1]!)
  await expect(sendCorrectionCommand({ ...command, key: 'short' }, 'csrf', new AbortController().signal)).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
  vi.mocked(api.correctPurchaseLine).mockRejectedValueOnce(new ApiError('INVALID_RESPONSE')).mockResolvedValueOnce(purchaseEntry)
  const first = new AbortController().signal, second = new AbortController().signal
  await expect(sendCorrectionCommand(command, 'old-csrf', first)).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  window.dispatchEvent(new Event('online')); expect(api.correctPurchaseLine).toHaveBeenCalledTimes(1)
  expect(await sendCorrectionCommand(command, 'fresh-csrf', second)).toEqual(purchaseEntry)
  expect(api.correctPurchaseLine).toHaveBeenNthCalledWith(1, purchase.id, purchaseReplace, { csrfToken: 'old-csrf', idempotencyKey: command.key, signal: first, purchase, line, item, replacementItem: target })
  expect(api.correctPurchaseLine).toHaveBeenNthCalledWith(2, purchase.id, purchaseReplace, { csrfToken: 'fresh-csrf', idempotencyKey: command.key, signal: second, purchase, line, item, replacementItem: target })
  for (const read of reads) expect(read).not.toHaveBeenCalled()
})
