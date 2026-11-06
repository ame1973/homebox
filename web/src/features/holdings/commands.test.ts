import { beforeEach, expect, it, vi } from 'vitest'
import { fetchItem, type Item } from '@/lib/api/catalog'
import * as api from '@/lib/api/holdings'
import type { Location } from '@/lib/api/locations'
import { freezeHoldingCommand, sendHoldingCommand, stockPreview, type HoldingCommandInput, type HoldingResult, type StockProposal } from './commands'

vi.mock('@/lib/api/catalog', async (original) => ({ ...await original<typeof import('@/lib/api/catalog')>(), fetchItem: vi.fn() }))
vi.mock('@/lib/api/holdings', async (original) => ({ ...await original<typeof import('@/lib/api/holdings')>(), createUnit: vi.fn(), patchUnit: vi.fn(), moveUnit: vi.fn(), setUnitStatus: vi.fn(), deleteUnit: vi.fn(), receiveStock: vi.fn(), consumeStock: vi.fn(), transferStock: vi.fn(), adjustStock: vi.fn(), fetchUnit: vi.fn(), fetchItemUnitsPage: vi.fn(), fetchStockPage: vi.fn() }))
const owner: Item = { id: 'item', revision: 7, name: '  Owner é  ', brand: null, model: '', category_id: null, description: '', notes: '',
  tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1, status: 'active' }
const quantity: Item = { ...owner, tracking_mode: 'quantity', unit_code: 'kg', base_unit: 'g', scale_factor: 1000 }
const place = (id: string, status: 'active' | 'archived' = 'active', parent_id: string | null = null): Location => ({ id, revision: 3, name: `  ${id} é  `, code: `C-${id}`, kind: 'other', parent_id, sort_order: 0, status, description: '' })
const source = place('source'), target = place('target')
const unit: api.Unit = { id: 'unit', item_id: owner.id, revision: 2, location_id: source.id, asset_code: 'Retained AST', serial_number: null, notes: 'Original', status: 'stored' }
const balance: api.Stock = { item_id: owner.id, location_id: source.id, revision: 2, quantity_base: 1500, unit_code: 'kg', base_unit: 'g', scale_factor: 1000 }
const unitContext = { owner, subject: unit, location: [source] }
const stockContext = { owner: quantity, balances: [balance], source: [], target: [] }
const methods = ['createUnit', 'patchUnit', 'moveUnit', 'setUnitStatus', 'deleteUnit', 'receiveStock', 'consumeStock', 'transferStock', 'adjustStock'] as const
beforeEach(() => { vi.clearAllMocks(); for (const method of methods) vi.mocked(api[method]).mockReset() })

it('freezes original Unit owner/row/location labels, body and one volatile key before review', () => {
  const proposal = { ...unitContext, kind: 'move-unit' as const, owner: { ...owner }, subject: { ...unit }, location: [{ ...source }], destination: [{ ...target }], body: { target_location_id: target.id } }
  const command = freezeHoldingCommand(proposal)
  proposal.owner.revision = 99; proposal.subject.revision = 99; proposal.location[0]!.name = 'Later source'; proposal.destination[0]!.name = 'Later destination'; proposal.body.target_location_id = 'another'
  expect(command).toMatchObject({ owner, subject: unit, location: [source], destination: [target], body: { target_location_id: target.id } })
  expect(Object.isFrozen(command)).toBe(true)
  expect(command.key).toMatch(/^[a-f0-9-]{36}$/)
  expect(freezeHoldingCommand({ kind: 'delete-unit', ...unitContext }).key).not.toBe(command.key)
  expect('csrfToken' in command).toBe(false)
  expect('session_token' in command).toBe(false)
})

const cases: { input: HoldingCommandInput; method: typeof methods[number]; body?: unknown; reply: HoldingResult; unitProof?: number }[] = [
  { input: { kind: 'create-unit', owner, body: { location_id: null }, destination: [] }, method: 'createUnit', body: { location_id: null }, reply: { ...unit, revision: 1, location_id: null, notes: '' }, unitProof: 7 },
  { input: { kind: 'patch-unit', ...unitContext, body: { serial_number: '', notes: '  Exact é  ' } }, method: 'patchUnit', body: { serial_number: '', notes: '  Exact é  ' }, reply: { ...unit, revision: 3, serial_number: '', notes: '  Exact é  ' } },
  { input: { kind: 'move-unit', ...unitContext, destination: [target], body: { target_location_id: target.id } }, method: 'moveUnit', body: { target_location_id: target.id }, reply: { ...unit, revision: 3, location_id: target.id } },
  { input: { kind: 'status-unit', ...unitContext, body: { status: 'disposed', disposal_kind: 'recycled', note: 'Reviewed' } }, method: 'setUnitStatus', body: { status: 'disposed', disposal_kind: 'recycled', note: 'Reviewed' }, reply: { ...unit, revision: 3, status: 'disposed' } },
  { input: { kind: 'delete-unit', ...unitContext }, method: 'deleteUnit', reply: { ok: true, operation_id: 'event', revision: 3 } },
  { input: { kind: 'receive', ...stockContext, target: [target], body: { item_id: owner.id, target_location_id: target.id, quantity_base: 1000 } }, method: 'receiveStock', body: { item_id: owner.id, target_location_id: target.id, quantity_base: 1000 }, reply: { ...balance, location_id: target.id, revision: 1, quantity_base: 1000 } },
  { input: { kind: 'consume', ...stockContext, source: [source], body: { item_id: owner.id, source_location_id: source.id, quantity_base: 1000 } }, method: 'consumeStock', body: { item_id: owner.id, source_location_id: source.id, quantity_base: 1000 }, reply: { ...balance, revision: 3, quantity_base: 500 } },
  { input: { kind: 'transfer', ...stockContext, source: [source], target: [target], body: { item_id: owner.id, source_location_id: source.id, target_location_id: target.id, quantity_base: 1500 } }, method: 'transferStock', body: { item_id: owner.id, source_location_id: source.id, target_location_id: target.id, quantity_base: 1500 }, reply: { ...balance, location_id: target.id, revision: 1 } },
  { input: { kind: 'adjust', ...stockContext, target: [target], body: { item_id: owner.id, target_location_id: target.id, quantity_base: 0, reason: '  Count  ', note: '' } }, method: 'adjustStock', body: { item_id: owner.id, target_location_id: target.id, quantity_base: 0, reason: '  Count  ', note: '' }, reply: { ...balance, location_id: target.id, revision: 1, quantity_base: 0 } },
]
it.each(cases)('dispatches $input.kind with only original proofs/identity and explicitly reuses them on reconciliation', async ({ input, method, body, reply, unitProof }) => {
  vi.mocked(api[method]).mockResolvedValue(reply as never)
  const command = freezeHoldingCommand(input), signal = new AbortController().signal
  expect(await sendHoldingCommand(command, 'csrf', signal)).toEqual(reply)
  const root = { csrfToken: 'csrf', signal, idempotencyKey: command.key }
  const context = method.endsWith('Stock') ? { ...root, item: quantity } : method === 'createUnit' ? { ...root, expectedRevision: unitProof } : { ...root, itemID: owner.id, expectedRevision: unit.revision }
  const id = method === 'createUnit' ? owner.id : unit.id
  const args = method.endsWith('Stock') ? [body, context] : body === undefined ? [id, context] : [id, body, context]
  expect(api[method]).toHaveBeenCalledWith(...args)
  await sendHoldingCommand(command, 'fresh-csrf', signal)
  expect(vi.mocked(api[method]).mock.lastCall?.at(-1)).toEqual({ ...context, csrfToken: 'fresh-csrf' })
  expect(api[method]).toHaveBeenCalledTimes(2)
  for (const other of methods) if (other !== method) expect(api[other]).not.toHaveBeenCalled()
  expect(fetchItem).not.toHaveBeenCalled()
  expect(api.fetchStockPage).not.toHaveBeenCalled()
  expect(api.fetchItemUnitsPage).not.toHaveBeenCalled()
  expect(api.fetchUnit).not.toHaveBeenCalled()
})
it('preserves the complete original Stock interpretation/balances through later draft/source mutations', async () => {
  const proposal: StockProposal = { kind: 'transfer', ...stockContext, owner: { ...quantity }, balances: [{ ...balance }], source: [{ ...source }], target: [{ ...target }], body: { item_id: owner.id, source_location_id: source.id, target_location_id: target.id, quantity_base: 1500 } }
  const command = freezeHoldingCommand(proposal)
  Object.assign(proposal.owner, { revision: 99, base_unit: 'ml', scale_factor: 1 })
  Object.assign(proposal.balances[0]!, { quantity_base: 0 })
  Object.assign(proposal.body, { quantity_base: 1 })
  expect(command).toMatchObject({ owner: quantity, balances: [balance], body: { quantity_base: 1500 } })
  await sendHoldingCommand(command, 'csrf', new AbortController().signal)
  expect(api.transferStock).toHaveBeenCalledWith(expect.objectContaining({ quantity_base: 1500 }), expect.objectContaining({ item: quantity, idempotencyKey: command.key }))
})
it('previews each actual base-count leg without mixed totals, guessed Stock revisions or invented rows', () => {
  const receive = stockPreview(cases[5]!.input as StockProposal)
  expect(receive).toEqual({ source: null, target: { location_id: target.id, before: null, quantity_base: 1000 } })
  expect(stockPreview(cases[6]!.input as StockProposal)).toEqual({ source: { location_id: source.id, before: balance, quantity_base: 500 }, target: null })
  expect(stockPreview(cases[7]!.input as StockProposal)).toEqual({ source: { location_id: source.id, before: balance, quantity_base: 0 }, target: { location_id: target.id, before: null, quantity_base: 1500 } })
  expect(stockPreview(cases[8]!.input as StockProposal)).toEqual({ source: null, target: { location_id: target.id, before: null, quantity_base: 0 } })
  const targetBalance = { ...balance, location_id: target.id, quantity_base: Number.MAX_SAFE_INTEGER - 1000 }
  expect(stockPreview({ ...cases[5]!.input as StockProposal, balances: [balance, targetBalance] }).target?.quantity_base).toBe(Number.MAX_SAFE_INTEGER)
})
it('allows existing equal/lower counts and outflow on valid archived paths, but requires active new/increased placement', () => {
  const archived = place(source.id, 'archived')
  const equal: StockProposal = { kind: 'adjust', ...stockContext, target: [archived], body: { item_id: owner.id, target_location_id: source.id, quantity_base: 1500, reason: 'Equal' } }
  expect(stockPreview(equal).target).toEqual({ location_id: source.id, before: balance, quantity_base: 1500 })
  expect(stockPreview({ ...equal, body: { ...equal.body, quantity_base: 0 } }).target?.quantity_base).toBe(0)
  expect(stockPreview({ ...cases[6]!.input as StockProposal, source: [archived] }).source?.quantity_base).toBe(500)
  for (const proposal of [{ ...equal, body: { ...equal.body, quantity_base: 1501 } }, { ...equal, balances: [], body: { ...equal.body, quantity_base: 0 } }, { ...cases[5]!.input as StockProposal, target: [place(target.id, 'archived')] }, { ...cases[5]!.input as StockProposal, target: [place('root', 'archived'), place(target.id, 'active', 'root')] }]) {
    expect(() => freezeHoldingCommand(proposal)).toThrow(expect.objectContaining({ code: 'LOCATION_ARCHIVED' }))
  }
})
it('withholds unsafe/insufficient preview legs before any dispatch', async () => {
  for (const proposal of [{ ...cases[6]!.input as StockProposal, balances: [] }, { ...cases[7]!.input as StockProposal, balances: [{ ...balance, quantity_base: 1499 }] }]) {
    expect(() => freezeHoldingCommand(proposal)).toThrow(expect.objectContaining({ code: 'INSUFFICIENT_STOCK' }))
  }
  for (const proposal of [{ ...cases[5]!.input as StockProposal, balances: [{ ...balance, location_id: target.id, quantity_base: Number.MAX_SAFE_INTEGER }] }, { ...cases[7]!.input as StockProposal, balances: [balance, { ...balance, location_id: target.id, quantity_base: Number.MAX_SAFE_INTEGER }] }]) {
    expect(() => freezeHoldingCommand(proposal)).toThrow(expect.objectContaining({ code: 'QUANTITY_OVERFLOW' }))
    await expect(sendHoldingCommand({ ...proposal, key: 'original-key' }, 'csrf', new AbortController().signal)).rejects.toMatchObject({ code: 'QUANTITY_OVERFLOW' })
  }
  for (const method of methods) expect(api[method]).not.toHaveBeenCalled()
})
it('rejects mismatched owners/interpretations/duplicate balances and unknown actions without rebuilding context', () => {
  const receive = cases[5]!.input as StockProposal
  for (const proposal of [{ ...receive, owner }, { ...receive, body: { ...receive.body, item_id: 'other' } }, { ...receive, balances: [balance, balance] }, { ...receive, balances: [{ ...balance, item_id: 'other' }] }, { ...receive, balances: [{ ...balance, scale_factor: 1 }] }, { ...receive, balances: [{ ...balance, base_unit: 'ml' }] }, { ...receive, balances: [{ ...balance, unit_code: 'L' }] }, { ...receive, balances: undefined }, { ...receive, body: { ...receive.body, quantity_base: 1.5 } }]) {
    expect(() => freezeHoldingCommand(proposal as never)).toThrow(expect.objectContaining({ code: proposal.owner.tracking_mode === 'individual' ? 'TRACKING_MODE_CONFLICT' : 'VALIDATION_ERROR' }))
  }
  expect(() => stockPreview({ ...receive, kind: 'unknown' } as never)).toThrow(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
  expect(() => freezeHoldingCommand({ ...unitContext, kind: 'unknown' } as never)).toThrow(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
})
it('checks complete selected paths and identities, including null placement, roots, cycles, order and bounded authority', () => {
  const create = cases[0]!.input
  for (const proposal of [
    { ...create, destination: [target] }, { ...create, body: { location_id: target.id }, destination: [] },
    { ...create, body: { location_id: target.id }, destination: [place(target.id, 'active', 'missing')] },
    { ...create, body: { location_id: target.id }, destination: [place('root'), place(target.id, 'active', 'root'), place('unrelated')] },
    { ...create, body: { location_id: target.id }, destination: [place(target.id, 'active', 'root'), place('root')] },
    { ...create, body: { location_id: target.id }, destination: [place('root', 'active', target.id), place(target.id, 'active', 'root')] },
    { ...create, body: { location_id: target.id }, destination: [{ ...target, revision: 0 }] }, { ...create, destination: undefined },
    { ...unitContext, kind: 'delete-unit', location: [target] }, { ...unitContext, kind: 'patch-unit', body: {} },
  ]) expect(() => freezeHoldingCommand(proposal as never)).toThrow(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
  expect(() => freezeHoldingCommand({ kind: 'create-unit', owner, body: { location_id: target.id }, destination: [place('root'), place(target.id, 'active', 'root')] })).not.toThrow()
  expect(() => freezeHoldingCommand({ ...unitContext, kind: 'move-unit', location: [place(source.id, 'archived')], destination: [place(source.id, 'archived')], body: { target_location_id: source.id } })).not.toThrow()
})
it('retains independent Unit ownership/lifecycle without guessing financial eligibility', () => {
  expect(() => freezeHoldingCommand({ kind: 'create-unit', owner: quantity, body: { location_id: null }, destination: [] })).toThrow(expect.objectContaining({ code: 'TRACKING_MODE_CONFLICT' }))
  expect(() => freezeHoldingCommand({ kind: 'delete-unit', ...unitContext, subject: { ...unit, item_id: 'other' } })).toThrow(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
  const disposed = { ...unitContext, subject: { ...unit, status: 'disposed' as const } }
  expect(() => freezeHoldingCommand({ ...disposed, kind: 'move-unit', destination: [target], body: { target_location_id: target.id } })).toThrow(expect.objectContaining({ code: 'UNIT_DISPOSED' }))
  expect(() => freezeHoldingCommand({ ...disposed, kind: 'status-unit', body: { status: 'stored' } })).toThrow(expect.objectContaining({ code: 'UNIT_DISPOSED' }))
  expect(() => freezeHoldingCommand({ ...disposed, kind: 'patch-unit', body: { notes: '' } })).not.toThrow()
  expect(() => freezeHoldingCommand({ ...disposed, kind: 'status-unit', body: { status: 'disposed', disposal_kind: 'donated' } })).not.toThrow()
  expect(() => freezeHoldingCommand({ ...disposed, kind: 'delete-unit' })).not.toThrow()
})

// @ts-expect-error Unit deletion retains its original physical location context.
const locationlessDelete: HoldingCommandInput = { kind: 'delete-unit', owner, subject: unit }
// @ts-expect-error A stock review needs actual selected source/target context.
const unreviewedStock: HoldingCommandInput = { kind: 'receive', owner: quantity, body: { item_id: owner.id, target_location_id: target.id, quantity_base: 1 } }
// @ts-expect-error Physical disposal cannot invent financial proceeds.
const financialDisposal: HoldingCommandInput = { kind: 'status-unit', ...unitContext, body: { status: 'disposed', disposal_kind: 'sold', realized_proceeds_minor: 1 } }
void [locationlessDelete, unreviewedStock, financialDisposal]
