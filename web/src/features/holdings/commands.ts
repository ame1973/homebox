import { ApiError, type WriteContext } from '@/lib/api/client'
import { itemSchema, type Item } from '@/lib/api/catalog'
import { input } from '@/lib/api/domain'
import * as api from '@/lib/api/holdings'
import { locationSchema, type Location } from '@/lib/api/locations'
import type { components } from '@/lib/api/schema'
import { freezeReviewedCommand } from '@/lib/reviewed-command'
import { buildLocationTree, locationPath } from '@/features/locations/tree'
import type { StockChange } from './form'

type Schemas = components['schemas']
type UnitContext = Readonly<{ owner: Item; subject: api.Unit; location: readonly Location[] }>
export type StockProposal = StockChange & Readonly<{ owner: Item; balances: readonly api.Stock[]; source: readonly Location[]; target: readonly Location[] }>
export type HoldingCommandInput = StockProposal
  | Readonly<{ kind: 'create-unit'; owner: Item; body: Schemas['UnitCreate']; destination: readonly Location[] }>
  | (UnitContext & Readonly<{ kind: 'patch-unit'; body: api.UnitPatchInput }>)
  | (UnitContext & Readonly<{ kind: 'move-unit'; body: Schemas['UnitMove']; destination: readonly Location[] }>)
  | (UnitContext & Readonly<{ kind: 'status-unit'; body: Schemas['UnitStatusInput'] }>)
  | (UnitContext & Readonly<{ kind: 'delete-unit' }>)
export type HoldingCommand = HoldingCommandInput & Readonly<{ key: string }>
export type HoldingResult = api.Unit | api.Stock | Schemas['ActionAck']
export type BalanceChange = Readonly<{ location_id: string; before: api.Stock | null; quantity_base: number }>
export type StockPreview = Readonly<{ source: BalanceChange | null; target: BalanceChange | null }>

function reviewedPath(records: readonly Location[], id: string | null, active = false) {
  if (!Array.isArray(records)) throw new ApiError('VALIDATION_ERROR')
  if (id === null) {
    if (records.length) throw new ApiError('VALIDATION_ERROR')
    return
  }
  const checked = records.map((record) => input(locationSchema, record))
  let tree: ReturnType<typeof buildLocationTree>
  try {
    tree = buildLocationTree(checked)
    const path = locationPath(tree, id)
    if (path.length !== checked.length || path.some((location, index) => location.id !== checked[index]!.id)) throw new ApiError('VALIDATION_ERROR')
  } catch { throw new ApiError('VALIDATION_ERROR') }
  if (active && !tree.canReceive.get(id)) throw new ApiError('LOCATION_ARCHIVED')
}

export function stockPreview(proposal: StockProposal): StockPreview {
  if (!['receive', 'consume', 'transfer', 'adjust'].includes(proposal.kind)) throw new ApiError('VALIDATION_ERROR')
  const owner = input(itemSchema, proposal.owner)
  if (owner.tracking_mode !== 'quantity') throw new ApiError('TRACKING_MODE_CONFLICT')
  const body = proposal.kind === 'receive' ? input(api.stockReceiveSchema, proposal.body)
    : proposal.kind === 'consume' ? input(api.stockConsumeSchema, proposal.body)
      : proposal.kind === 'transfer' ? input(api.stockTransferSchema, proposal.body) : input(api.stockAdjustSchema, proposal.body)
  if (body.item_id !== owner.id || !Array.isArray(proposal.balances)) throw new ApiError('VALIDATION_ERROR')
  const balances = new Map<string, api.Stock>()
  for (const row of proposal.balances) {
    const stock = input(api.stockSchema, row)
    if (stock.item_id !== owner.id || stock.unit_code !== owner.unit_code || stock.base_unit !== owner.base_unit || stock.scale_factor !== owner.scale_factor || balances.has(stock.location_id)) throw new ApiError('VALIDATION_ERROR')
    balances.set(stock.location_id, stock)
  }
  const sourceID = 'source_location_id' in body ? body.source_location_id : null
  const targetID = 'target_location_id' in body ? body.target_location_id : null
  const source = sourceID === null ? null : balances.get(sourceID) ?? null
  const target = targetID === null ? null : balances.get(targetID) ?? null
  reviewedPath(proposal.source, sourceID)
  reviewedPath(proposal.target, targetID, proposal.kind === 'receive' || proposal.kind === 'transfer' || proposal.kind === 'adjust' && (!target || body.quantity_base > target.quantity_base))
  if (sourceID !== null && (!source || source.quantity_base < body.quantity_base)) throw new ApiError('INSUFFICIENT_STOCK')
  if ((proposal.kind === 'receive' || proposal.kind === 'transfer') && (target?.quantity_base ?? 0) > Number.MAX_SAFE_INTEGER - body.quantity_base) throw new ApiError('QUANTITY_OVERFLOW')
  return {
    source: sourceID === null ? null : { location_id: sourceID, before: source, quantity_base: source!.quantity_base - body.quantity_base },
    target: targetID === null ? null : { location_id: targetID, before: target, quantity_base: proposal.kind === 'adjust' ? body.quantity_base : (target?.quantity_base ?? 0) + body.quantity_base },
  }
}

function validateHoldingCommand(proposal: HoldingCommandInput) {
  if (proposal.kind === 'receive' || proposal.kind === 'consume' || proposal.kind === 'transfer' || proposal.kind === 'adjust') {
    stockPreview(proposal)
    return
  }
  if (!['create-unit', 'patch-unit', 'move-unit', 'status-unit', 'delete-unit'].includes(proposal.kind)) throw new ApiError('VALIDATION_ERROR')
  const owner = input(itemSchema, proposal.owner)
  if (owner.tracking_mode !== 'individual') throw new ApiError('TRACKING_MODE_CONFLICT')
  if (proposal.kind === 'create-unit') {
    const body = input(api.unitCreateSchema, proposal.body)
    reviewedPath(proposal.destination, body.location_id, body.location_id !== null)
    return
  }
  const subject = input(api.unitSchema, proposal.subject)
  if (subject.item_id !== owner.id) throw new ApiError('VALIDATION_ERROR')
  reviewedPath(proposal.location, subject.location_id)
  if (proposal.kind === 'patch-unit') input(api.unitPatchSchema, proposal.body)
  if (proposal.kind === 'move-unit') {
    const body = input(api.unitMoveSchema, proposal.body)
    if (subject.status === 'disposed' && subject.location_id !== body.target_location_id) throw new ApiError('UNIT_DISPOSED')
    reviewedPath(proposal.destination, body.target_location_id, subject.location_id !== body.target_location_id)
  }
  if (proposal.kind === 'status-unit') {
    const body = input(api.unitStatusSchema, proposal.body)
    if (subject.status === 'disposed' && body.status !== 'disposed') throw new ApiError('UNIT_DISPOSED')
  }
}

export function freezeHoldingCommand(proposal: HoldingCommandInput): HoldingCommand {
  validateHoldingCommand(proposal)
  return freezeReviewedCommand(proposal)
}

// Reconciliation sends only original snapshots and proofs. Never re-read or
// adopt a new Item, balance, Unit or selected path inside this dispatch.
export async function sendHoldingCommand(command: HoldingCommand, csrfToken: string, signal: AbortSignal): Promise<HoldingResult> {
  validateHoldingCommand(command)
  const context: WriteContext = { csrfToken, signal, idempotencyKey: command.key }
  switch (command.kind) {
    case 'receive': return api.receiveStock(command.body, { ...context, item: command.owner })
    case 'consume': return api.consumeStock(command.body, { ...context, item: command.owner })
    case 'transfer': return api.transferStock(command.body, { ...context, item: command.owner })
    case 'adjust': return api.adjustStock(command.body, { ...context, item: command.owner })
    case 'create-unit': return api.createUnit(command.owner.id, command.body, { ...context, expectedRevision: command.owner.revision })
    case 'patch-unit': return api.patchUnit(command.subject.id, command.body, { ...context, itemID: command.owner.id, expectedRevision: command.subject.revision })
    case 'move-unit': return api.moveUnit(command.subject.id, command.body, { ...context, itemID: command.owner.id, expectedRevision: command.subject.revision })
    case 'status-unit': return api.setUnitStatus(command.subject.id, command.body, { ...context, itemID: command.owner.id, expectedRevision: command.subject.revision })
    case 'delete-unit': return api.deleteUnit(command.subject.id, { ...context, itemID: command.owner.id, expectedRevision: command.subject.revision })
  }
}
