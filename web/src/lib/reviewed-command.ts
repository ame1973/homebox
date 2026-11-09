import { ApiError } from '@/lib/api/client'

export type ReviewedCommand = Readonly<{ key: string }>

// Review proposals are volatile copies, never persistent/offline write queues.
// Freeze the nested domain objects/arrays too, so the reviewed body and proofs
// cannot change between an uncertain delivery and explicit reconciliation.
export function freezeReviewedCommand<T extends object>(input: T): Readonly<T> & ReviewedCommand {
  const command = { ...structuredClone(input), key: crypto.randomUUID() }
  const pending: object[] = [command]
  while (pending.length) {
    const current = pending.pop()!
    if (Object.isFrozen(current)) continue
    Object.freeze(current)
    for (const value of Object.values(current)) if (value !== null && typeof value === 'object') pending.push(value)
  }
  return command
}

export function uncertainMutationFailure(error: ApiError): boolean {
  return ['NETWORK_ERROR', 'REQUEST_TIMEOUT', 'REQUEST_ABORTED', 'INVALID_RESPONSE', 'UNKNOWN_ERROR', 'IDEMPOTENCY_CONFLICT', 'IDEMPOTENCY_EXPIRED'].includes(error.code) || (error.status !== undefined && error.status >= 500)
}
