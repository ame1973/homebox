import { useReviewedMutation } from '@/lib/use-reviewed-mutation'
import { freezePurchaseCommand, sendPurchaseCommand, type PurchaseCommand, type PurchaseResult } from './commands'

export function usePurchaseMutation(onConfirmed: (command: PurchaseCommand, result: PurchaseResult) => void, onSessionLost: (mandatory: boolean) => void) {
  return useReviewedMutation(freezePurchaseCommand, sendPurchaseCommand, [['purchases'], ['catalog'], ['holdings']], onConfirmed, onSessionLost)
}

export type PurchaseMutation = ReturnType<typeof usePurchaseMutation>
