import { ApiError, type WriteContext } from '@/lib/api/client'
import * as api from '@/lib/api/catalog'
import type { Category } from '@/lib/api/taxonomy'
import type { components } from '@/lib/api/schema'
import { freezeReviewedCommand } from '@/lib/reviewed-command'

type Schemas = components['schemas']
export type CatalogCommandInput =
  | Readonly<{ kind: 'create'; body: api.ItemCreateInput; category?: Category | null }>
  | Readonly<{ kind: 'patch'; subject: api.Item; body: Schemas['ItemWrite']; category?: Category | null }>
  | Readonly<{ kind: 'archive'; subject: api.Item; body: Schemas['ItemArchiveInput'] }>
  | Readonly<{ kind: 'restore' | 'delete'; subject: api.Item }>
export type CatalogCommand = CatalogCommandInput & Readonly<{ key: string }>
export type CatalogResult = api.Item | Schemas['ActionAck']

export function freezeCatalogCommand(input: CatalogCommandInput): CatalogCommand {
  return freezeReviewedCommand(input)
}

export function commandName(command: CatalogCommand): string {
  return command.kind === 'create' ? command.body.name : command.subject.name
}

export function sendCatalogCommand(command: CatalogCommand, csrfToken: string, signal: AbortSignal): Promise<CatalogResult> {
  const context: WriteContext = { csrfToken, signal, idempotencyKey: command.key }
  if (command.kind === 'create' || command.kind === 'patch') {
    const categoryID = command.body.category_id
    // The reviewed label must identify the selected category. It is context, not
    // a parent If-Match: item creation has no category-revision precondition.
    if (categoryID != null ? !command.category || command.category.id !== categoryID : command.category != null) return Promise.reject(new ApiError('VALIDATION_ERROR'))
    if (command.kind === 'create') return api.createItem(command.body, context)
    return api.patchItem(command.subject.id, command.body, { ...context, expectedRevision: command.subject.revision })
  }
  const proof = { ...context, expectedRevision: command.subject.revision }
  switch (command.kind) {
    case 'archive': return api.archiveItem(command.subject.id, command.body, proof)
    case 'restore': return api.patchItem(command.subject.id, { status: 'active' }, proof)
    case 'delete': return api.deleteItem(command.subject.id, proof)
  }
}
