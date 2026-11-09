import { useReviewedMutation } from '@/lib/use-reviewed-mutation'
import { freezeCorrectionCommand, sendCorrectionCommand, type CorrectionCommand, type CorrectionResult } from './commands'

export function useCorrectionMutation(onConfirmed: (command: CorrectionCommand, result: CorrectionResult) => void, onSessionLost: (mandatory: boolean) => void) {
  return useReviewedMutation(freezeCorrectionCommand, sendCorrectionCommand, [['purchases'], ['catalog'], ['holdings'], ['assets']], onConfirmed, onSessionLost)
}

export type CorrectionMutation = ReturnType<typeof useCorrectionMutation>
