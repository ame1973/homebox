import { ApiError } from '@/lib/api/client'

export type ReviewedCommand = Readonly<{ key: string }>

// Review proposals are volatile copies, never persistent/offline write queues.
export function freezeReviewedCommand<T extends object>(input: T): Readonly<T> & ReviewedCommand {
  return Object.freeze({ ...structuredClone(input), key: crypto.randomUUID() })
}

export function uncertainMutationFailure(error: ApiError): boolean {
  return ['NETWORK_ERROR', 'REQUEST_TIMEOUT', 'REQUEST_ABORTED', 'INVALID_RESPONSE', 'UNKNOWN_ERROR', 'IDEMPOTENCY_CONFLICT', 'IDEMPOTENCY_EXPIRED'].includes(error.code) || (error.status !== undefined && error.status >= 500)
}
