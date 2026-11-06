import { useReviewedMutation } from '@/lib/use-reviewed-mutation'
import { freezeCatalogCommand, sendCatalogCommand, type CatalogCommand, type CatalogResult } from './commands'

export function useCatalogMutation(onConfirmed: (command: CatalogCommand, result: CatalogResult) => void, onSessionLost: (mandatory: boolean) => void) {
  return useReviewedMutation(freezeCatalogCommand, sendCatalogCommand, [['catalog'], ['taxonomy'], ['holdings'], ['purchases']], onConfirmed, onSessionLost)
}

export type CatalogMutation = ReturnType<typeof useCatalogMutation>
