import { ApiError, type WriteContext } from '@/lib/api/client'
import * as api from '@/lib/api/taxonomy'
import type { components } from '@/lib/api/schema'
import { freezeReviewedCommand } from '@/lib/reviewed-command'

type Schemas = components['schemas']
export type TaxonomyCommandInput =
  | Readonly<{ kind: 'create-category'; body: Schemas['CategoryCreate']; parent?: api.Category }>
  | Readonly<{ kind: 'patch-category'; subject: api.Category; body: Schemas['CategoryWrite']; parent?: api.Category | null; previousParent?: api.Category | null }>
  | Readonly<{ kind: 'delete-category'; subject: api.Category }>
  | Readonly<{ kind: 'create-tag'; body: Schemas['TagWrite'] }>
  | Readonly<{ kind: 'patch-tag'; subject: api.Tag; body: Schemas['TagWrite'] }>
  | Readonly<{ kind: 'delete-tag'; subject: api.Tag }>
export type TaxonomyCommand = TaxonomyCommandInput & Readonly<{ key: string }>
export type TaxonomyResult = api.Category | api.Tag | Schemas['ActionAck']

export function freezeTaxonomyCommand(input: TaxonomyCommandInput): TaxonomyCommand {
  return freezeReviewedCommand(input)
}

export function commandName(command: TaxonomyCommand): string {
  return command.kind === 'create-category' || command.kind === 'create-tag' ? command.body.name : command.subject.name
}

export function sendTaxonomyCommand(command: TaxonomyCommand, csrfToken: string, signal: AbortSignal): Promise<TaxonomyResult> {
  const context: WriteContext = { csrfToken, signal, idempotencyKey: command.key }
  if (command.kind === 'create-category') {
    if (command.body.parent_id != null) {
      if (!command.parent || command.parent.id !== command.body.parent_id) return Promise.reject(new ApiError('VALIDATION_ERROR'))
      return api.createCategory({ ...command.body, parent_id: command.body.parent_id }, { ...context, expectedRevision: command.parent.revision })
    }
    if (command.parent) return Promise.reject(new ApiError('VALIDATION_ERROR'))
    return api.createCategory({ ...command.body, parent_id: null }, context)
  }
  if (command.kind === 'create-tag') return api.createTag(command.body, context)
  const proof = { ...context, expectedRevision: command.subject.revision }
  switch (command.kind) {
    case 'patch-category': return api.patchCategory(command.subject.id, command.body, proof)
    case 'delete-category': return api.deleteCategory(command.subject.id, proof)
    case 'patch-tag': return api.patchTag(command.subject.id, command.body, proof)
    case 'delete-tag': return api.deleteTag(command.subject.id, proof)
  }
}
