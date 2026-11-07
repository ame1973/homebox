import { useReviewedMutation } from '@/lib/use-reviewed-mutation'
import { assetCommandReadRoots, freezeAssetCommand, sendAssetCommand, type AssetCommand, type AssetResult } from './commands'

export function useAssetMutation(onConfirmed: (command: AssetCommand, result: AssetResult) => void, onSessionLost: (mandatory: boolean) => void) {
  return useReviewedMutation(freezeAssetCommand, sendAssetCommand, assetCommandReadRoots, onConfirmed, onSessionLost)
}

export type AssetMutation = ReturnType<typeof useAssetMutation>
