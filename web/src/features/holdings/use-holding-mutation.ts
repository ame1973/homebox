import { useReviewedMutation } from '@/lib/use-reviewed-mutation'
import { freezeHoldingCommand, sendHoldingCommand, type HoldingCommand, type HoldingResult } from './commands'

export function useHoldingMutation(onConfirmed: (command: HoldingCommand, result: HoldingResult) => void, onSessionLost: (mandatory: boolean) => void) {
  return useReviewedMutation(freezeHoldingCommand, sendHoldingCommand, [['holdings'], ['catalog'], ['locations']], onConfirmed, onSessionLost)
}

export type HoldingMutation = ReturnType<typeof useHoldingMutation>
