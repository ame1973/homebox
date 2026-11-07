import { useReviewedMutation } from '@/lib/use-reviewed-mutation'
import { freezeTaxonomyCommand, sendTaxonomyCommand, type TaxonomyCommand, type TaxonomyResult } from './commands'

export function useTaxonomyMutation(onConfirmed: (command: TaxonomyCommand, result: TaxonomyResult) => void, onSessionLost: (mandatory: boolean) => void) {
  return useReviewedMutation(freezeTaxonomyCommand, sendTaxonomyCommand, [['taxonomy'], ['catalog'], ['holdings'], ['purchases'], ['assets']], onConfirmed, onSessionLost)
}

export type TaxonomyMutation = ReturnType<typeof useTaxonomyMutation>
