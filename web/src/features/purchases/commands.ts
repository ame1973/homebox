import { ApiError, type WriteContext } from '@/lib/api/client'
import { itemSchema } from '@/lib/api/catalog'
import { input, pathIDSchema } from '@/lib/api/domain'
import { unitSchema } from '@/lib/api/holdings'
import * as api from '@/lib/api/purchases'
import type { components } from '@/lib/api/schema'
import { freezeReviewedCommand } from '@/lib/reviewed-command'
import type { PurchaseAllocation, PurchaseDetail } from './model'

export type PurchaseCommandInput = Readonly<{ kind: 'create-purchase'; body: components['schemas']['PurchaseWrite'] }>
  | Readonly<{ kind: 'patch-purchase'; source: PurchaseDetail; body: api.PurchasePatchInput }>
  | (PurchaseAllocation & Readonly<{ kind: 'create-line' }>)
export type PurchaseCommand = PurchaseCommandInput & Readonly<{ key: string }>
export type PurchaseResult = api.Purchase | api.PurchaseLine

function reviewedSource(source: PurchaseDetail) {
  const purchase = input(api.purchaseSchema, source.purchase)
  input(pathIDSchema, purchase.id)
  if (!Array.isArray(source.lines)) throw new ApiError('VALIDATION_ERROR')
  const identities = new Set<string>()
  for (const record of source.lines) {
    const line = input(api.purchaseLineSchema, record)
    if (line.purchase_id !== purchase.id || identities.has(line.id)) throw new ApiError('VALIDATION_ERROR')
    identities.add(line.id)
  }
  return purchase
}
function validatePurchaseCommand(proposal: PurchaseCommandInput) {
  if (proposal.kind === 'create-purchase') {
    input(api.purchaseCreateSchema, proposal.body)
    return
  }
  if (proposal.kind === 'patch-purchase') {
    const purchase = reviewedSource(proposal.source)
    const body = input(api.purchasePatchSchema, proposal.body)
    if (proposal.source.lines.length && body.currency !== undefined && body.currency !== purchase.currency) throw new ApiError('PURCHASE_CURRENCY_LOCKED')
    return
  }
  if (proposal.kind !== 'create-line') throw new ApiError('VALIDATION_ERROR')
  const purchase = input(api.purchaseSchema, proposal.purchase), item = input(itemSchema, proposal.item)
  input(pathIDSchema, purchase.id)
  const body = input(api.purchaseLineCreateSchema, proposal.body)
  if (body.item_id !== item.id || body.item_revision !== item.revision) throw new ApiError('VALIDATION_ERROR')
  if (body.item_unit_id === null) {
    if (proposal.unit !== null) throw new ApiError('VALIDATION_ERROR')
  } else {
    if (item.tracking_mode !== 'individual') throw new ApiError('TRACKING_MODE_CONFLICT')
    const unit = input(unitSchema, proposal.unit)
    if (unit.id !== body.item_unit_id || unit.item_id !== item.id) throw new ApiError('VALIDATION_ERROR')
  }
  // Retained Unit status is not asset-basis/currency eligibility. The actual SQL
  // financial producer remains authoritative; never preview implicit receipt.
}

export function freezePurchaseCommand(proposal: PurchaseCommandInput): PurchaseCommand {
  validatePurchaseCommand(proposal)
  return freezeReviewedCommand(proposal)
}

// Use original snapshots only, including after source changes or uncertain
// results. No reads, adoption, token storage or alternate key are hidden here.
export async function sendPurchaseCommand(command: PurchaseCommand, csrfToken: string, signal: AbortSignal): Promise<PurchaseResult> {
  validatePurchaseCommand(command)
  const context: WriteContext = { csrfToken, signal, idempotencyKey: command.key }
  switch (command.kind) {
    case 'create-purchase': return api.createPurchase(command.body, context)
    case 'patch-purchase': return api.patchPurchase(command.source.purchase.id, command.body, { ...context, purchase: command.source.purchase })
    case 'create-line': return api.createPurchaseLine(command.purchase.id, command.body, { ...context, purchase: command.purchase, item: command.item })
  }
}
