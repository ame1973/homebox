import { useReviewedMutation } from '@/lib/use-reviewed-mutation'
import { freezeOwnedCommand, sendOwnedCommand, type OwnedCommand, type OwnedResult } from './owned-commands'

export function useOwnedMutation(onConfirmed: (command: OwnedCommand, result: OwnedResult) => void, onSessionLost: (mandatory: boolean) => void) {
  return useReviewedMutation(freezeOwnedCommand, sendOwnedCommand, [['catalog'], ['taxonomy']], onConfirmed, onSessionLost)
}

export type OwnedMutation = ReturnType<typeof useOwnedMutation>
