import { useReviewedMutation } from '@/lib/use-reviewed-mutation'
import { freezeLocationCommand, sendLocationCommand, type LocationCommand, type LocationResult } from './commands'

export function useLocationMutation(onConfirmed: (command: LocationCommand, result: LocationResult) => void, onSessionLost: (mandatory: boolean) => void) {
  return useReviewedMutation(freezeLocationCommand, sendLocationCommand, [['locations']], onConfirmed, onSessionLost)
}

export type LocationMutation = ReturnType<typeof useLocationMutation>
