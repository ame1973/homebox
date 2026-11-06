import { beforeEach, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/client'
import type { Item } from '@/lib/api/catalog'
import type { Unit } from '@/lib/api/holdings'
import * as api from '@/lib/api/purchases'
import { freezePurchaseCommand, sendPurchaseCommand, type PurchaseCommandInput } from './commands'

vi.mock('@/lib/api/purchases', async (original) => ({ ...await original<typeof import('@/lib/api/purchases')>(),
  createPurchase: vi.fn(), patchPurchase: vi.fn(), createPurchaseLine: vi.fn(), fetchPurchase: vi.fn(), fetchPurchasesPage: vi.fn(), fetchItemPurchasesPage: vi.fn(), fetchPurchaseLinesPage: vi.fn() }))
const item: Item = { id: 'item', revision: 7, name: 'Original Item', brand: null, model: '', category_id: null, description: '', notes: '', tracking_mode: 'quantity', unit_code: 'kg', base_unit: 'g', scale_factor: 1000, status: 'active' }
const purchase: api.Purchase = { id: 'purchase', revision: 12, merchant_name: '  Original merchant é  ', purchased_at: '2026-11-06T12:00:00.123456789+08:00', currency: 'JPY', receipt_note: 'Original' }
const line: api.PurchaseLine = { id: 'line', revision: 1, purchase_id: purchase.id, item_id: item.id, item_unit_id: null, quantity_base: 1500, paid_minor: 0, note: '' }
const unit: Unit = { id: 'unit', item_id: item.id, revision: 3, asset_code: 'Original AST', location_id: null, status: 'disposed', serial_number: null, notes: '' }
const header = { merchant_name: '  Exact é  ', purchased_at: '2026-11-06T00:00:00.000000001Z', currency: 'JPY' }
const allocation = { item_id: item.id, item_revision: item.revision, item_unit_id: null, quantity_base: 1500, paid_minor: 0 } as const
const proposals: PurchaseCommandInput[] = [
  { kind: 'create-purchase', body: header },
  { kind: 'patch-purchase', source: { purchase, lines: [line] }, body: { receipt_note: '' } },
  { kind: 'create-line', purchase, item, unit: null, body: allocation },
]
beforeEach(() => {
  vi.mocked(api.createPurchase).mockReset().mockResolvedValue({ ...purchase, revision: 1 })
  vi.mocked(api.patchPurchase).mockReset().mockResolvedValue({ ...purchase, revision: 13, receipt_note: '' })
  vi.mocked(api.createPurchaseLine).mockReset().mockResolvedValue(line)
  vi.mocked(api.fetchPurchase).mockReset(); vi.mocked(api.fetchPurchasesPage).mockReset(); vi.mocked(api.fetchItemPurchasesPage).mockReset(); vi.mocked(api.fetchPurchaseLinesPage).mockReset()
})

it.each(proposals)('freezes one volatile $kind with an independent original key and no credential fields', (proposal) => {
  const first = freezePurchaseCommand(proposal), second = freezePurchaseCommand(proposal)
  expect(Object.isFrozen(first)).toBe(true); expect(first.key).toMatch(/^[0-9a-f-]{36}$/); expect(first.key).not.toBe(second.key)
  expect(first).toEqual({ ...proposal, key: first.key })
  expect(first.body).not.toBe(proposal.body)
  expect(first).not.toHaveProperty('csrfToken'); expect(first).not.toHaveProperty('signal'); expect(first).not.toHaveProperty('expectedRevision')
})
it('keeps complete original correction/zero-paid history and exact body independent from later source/draft mutation', () => {
  const original = { kind: 'patch-purchase' as const, source: { purchase: { ...purchase }, lines: [{ ...line }] }, body: { receipt_note: '' } }
  const frozen = freezePurchaseCommand(original)
  original.source.purchase.revision = 99; original.source.purchase.currency = 'USD'; original.source.lines[0]!.paid_minor = 999; original.body.receipt_note = 'Later draft'
  expect(frozen).toMatchObject({ source: { purchase, lines: [line] }, body: { receipt_note: '' } })
})
it.each(proposals)('dispatches $kind only once with original snapshots, explicit CSRF/key/signal and no reads', async (proposal) => {
  const command = freezePurchaseCommand(proposal), signal = new AbortController().signal
  const context = { csrfToken: 'current-csrf', idempotencyKey: command.key, signal }
  const result = await sendPurchaseCommand(command, context.csrfToken, signal)
  if (command.kind === 'create-purchase') { expect(api.createPurchase).toHaveBeenCalledExactlyOnceWith(command.body, context); expect(result.revision).toBe(1) }
  if (command.kind === 'patch-purchase') { expect(api.patchPurchase).toHaveBeenCalledExactlyOnceWith(purchase.id, command.body, { ...context, purchase }); expect(result.revision).toBe(13) }
  if (command.kind === 'create-line') { expect(api.createPurchaseLine).toHaveBeenCalledExactlyOnceWith(purchase.id, command.body, { ...context, purchase, item }); expect(result.revision).toBe(1) }
  expect(api.fetchPurchase).not.toHaveBeenCalled(); expect(api.fetchPurchasesPage).not.toHaveBeenCalled(); expect(api.fetchItemPurchasesPage).not.toHaveBeenCalled(); expect(api.fetchPurchaseLinesPage).not.toHaveBeenCalled()
})
it('permits retained descriptive and exact same-currency no-op context but never changing currency after even zero-paid history', () => {
  expect(() => freezePurchaseCommand({ kind: 'patch-purchase', source: { purchase, lines: [line] }, body: { currency: 'USD' } })).toThrow(expect.objectContaining({ code: 'PURCHASE_CURRENCY_LOCKED' }))
  expect(freezePurchaseCommand({ kind: 'patch-purchase', source: { purchase, lines: [line] }, body: { currency: 'JPY' } })).toMatchObject({ body: { currency: 'JPY' } })
  expect(freezePurchaseCommand({ kind: 'patch-purchase', source: { purchase, lines: [] }, body: { currency: 'USD' } })).toMatchObject({ body: { currency: 'USD' } })
  expect(freezePurchaseCommand({ kind: 'patch-purchase', source: { purchase: { ...purchase, currency: 'ZZZ' }, lines: [{ ...line, item_unit_id: unit.id, quantity_base: 2 }] }, body: { receipt_note: 'Descriptive only' } })).toMatchObject({ body: { receipt_note: 'Descriptive only' } })
})
it('retains the actually selected Unit without an invented Unit proof or treating status as financial eligibility', async () => {
  const owner = { ...item, tracking_mode: 'individual' as const, unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1 }
  const body = { ...allocation, item_unit_id: unit.id, quantity_base: 1 as const, paid_minor: 120000 }
  const command = freezePurchaseCommand({ kind: 'create-line', purchase, item: owner, unit, body })
  await sendPurchaseCommand(command, 'csrf', new AbortController().signal)
  expect(api.createPurchaseLine).toHaveBeenCalledWith(purchase.id, body, { csrfToken: 'csrf', signal: expect.any(AbortSignal), idempotencyKey: command.key, purchase, item: owner })
  expect(command).toMatchObject({ unit: { status: 'disposed', revision: 3 }, body: { quantity_base: 1, paid_minor: 120000 } })
})
it.each([
  { kind: 'unknown', body: header }, { kind: 'create-purchase', body: { ...header, paid_minor: 1 } },
  { kind: 'patch-purchase', source: { purchase, lines: [line] }, body: {} },
  { kind: 'patch-purchase', source: { purchase, lines: null }, body: { receipt_note: '' } },
  { kind: 'patch-purchase', source: { purchase, lines: [{ ...line, purchase_id: 'other' }] }, body: { receipt_note: '' } },
  { kind: 'patch-purchase', source: { purchase, lines: [line, line] }, body: { receipt_note: '' } },
  { kind: 'patch-purchase', source: { purchase: { ...purchase, id: '/' }, lines: [] }, body: { receipt_note: '' } },
  { kind: 'create-line', purchase, item, unit: null, body: { ...allocation, item_revision: 8 } },
  { kind: 'create-line', purchase, item, unit: null, body: { ...allocation, item_id: 'other' } },
  { kind: 'create-line', purchase, item, unit, body: allocation },
  { kind: 'create-line', purchase, item: { ...item, tracking_mode: 'individual' }, unit: null, body: { ...allocation, item_unit_id: unit.id, quantity_base: 1 } },
  { kind: 'create-line', purchase, item: { ...item, tracking_mode: 'individual' }, unit: { ...unit, item_id: 'other' }, body: { ...allocation, item_unit_id: unit.id, quantity_base: 1 } },
  { kind: 'create-line', purchase, item: { ...item, tracking_mode: 'individual' }, unit: { ...unit, id: 'other' }, body: { ...allocation, item_unit_id: unit.id, quantity_base: 1 } },
])('rejects invalid original proposal %# before review or dispatch', async (proposal) => {
  expect(() => freezePurchaseCommand(proposal as never)).toThrow(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
  await expect(sendPurchaseCommand({ ...proposal, key: 'original-command' } as never, 'csrf', new AbortController().signal)).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
  expect(api.createPurchase).not.toHaveBeenCalled(); expect(api.patchPurchase).not.toHaveBeenCalled(); expect(api.createPurchaseLine).not.toHaveBeenCalled()
})
it('rejects a named Unit on quantity tracking without converting it to an unallocated proposal', () => {
  expect(() => freezePurchaseCommand({ kind: 'create-line', purchase, item, unit, body: { ...allocation, item_unit_id: unit.id, quantity_base: 1 } })).toThrow(expect.objectContaining({ code: 'TRACKING_MODE_CONFLICT' }))
})
it('reconciles only an explicit identical original command after a failed response, with fresh CSRF but no source/proof adoption', async () => {
  const command = freezePurchaseCommand(proposals[2]!)
  vi.mocked(api.createPurchaseLine).mockRejectedValueOnce(new ApiError('INVALID_RESPONSE')).mockResolvedValueOnce(line)
  const first = new AbortController().signal, second = new AbortController().signal
  await expect(sendPurchaseCommand(command, 'old-csrf', first)).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  window.dispatchEvent(new Event('online'))
  expect(api.createPurchaseLine).toHaveBeenCalledTimes(1)
  expect(await sendPurchaseCommand(command, 'fresh-csrf', second)).toEqual(line)
  expect(api.createPurchaseLine).toHaveBeenNthCalledWith(1, purchase.id, allocation, { csrfToken: 'old-csrf', signal: first, idempotencyKey: command.key, purchase, item })
  expect(api.createPurchaseLine).toHaveBeenNthCalledWith(2, purchase.id, allocation, { csrfToken: 'fresh-csrf', signal: second, idempotencyKey: command.key, purchase, item })
  expect(api.fetchPurchase).not.toHaveBeenCalled()
})
