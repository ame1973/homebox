import { useReviewedMutation } from '@/lib/use-reviewed-mutation'
import { freezeMediaCommand, mediaCommandReadRoots, sendMediaCommand, type MediaCommand, type MediaResult } from './commands'

export function useMediaMutation(onConfirmed: (command: MediaCommand, result: MediaResult) => void, onSessionLost: (mandatory: boolean) => void) {
  return useReviewedMutation(freezeMediaCommand, sendMediaCommand, mediaCommandReadRoots, onConfirmed, onSessionLost)
}
