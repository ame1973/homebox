import { z } from 'zod'
import { ApiError, type WriteContext } from '@/lib/api/client'
import type { Item } from '@/lib/api/catalog'
import * as api from '@/lib/api/catalog-links'
import { input } from '@/lib/api/domain'
import type { components } from '@/lib/api/schema'
import { tagSchema, type Tag } from '@/lib/api/taxonomy'
import { freezeReviewedCommand } from '@/lib/reviewed-command'

type Schemas = components['schemas']
export type OwnedCommandInput =
  | Readonly<{ kind: 'create-barcode'; owner: Item; body: Schemas['BarcodeWrite'] }>
  | Readonly<{ kind: 'patch-barcode'; owner: Item; subject: api.Barcode; body: Schemas['BarcodeWrite'] }>
  | Readonly<{ kind: 'delete-barcode'; owner: Item; subject: api.Barcode }>
  | Readonly<{ kind: 'create-alias'; owner: Item; body: Schemas['AliasWrite'] }>
  | Readonly<{ kind: 'patch-alias'; owner: Item; subject: api.Alias; body: Schemas['AliasWrite'] }>
  | Readonly<{ kind: 'delete-alias'; owner: Item; subject: api.Alias }>
  | Readonly<{ kind: 'create-attribute'; owner: Item; body: Schemas['AttributeWrite'] }>
  | Readonly<{ kind: 'patch-attribute'; owner: Item; subject: api.Attribute; body: api.AttributePatchInput }>
  | Readonly<{ kind: 'delete-attribute'; owner: Item; subject: api.Attribute }>
  | Readonly<{ kind: 'replace-tags'; owner: Item; before: readonly Tag[]; after: readonly Tag[] }>
export type OwnedCommand = OwnedCommandInput & Readonly<{ key: string }>
export type OwnedResult = api.Barcode | api.Alias | api.Attribute | Item | Schemas['ActionAck']

export function freezeOwnedCommand(proposal: OwnedCommandInput): OwnedCommand {
  return freezeReviewedCommand(proposal)
}

const reviewedTags = z.array(tagSchema).refine((tags) => new Set(tags.map((tag) => tag.id)).size === tags.length)

export function sendOwnedCommand(command: OwnedCommand, csrfToken: string, signal: AbortSignal): Promise<OwnedResult> {
  if ('subject' in command && command.subject.item_id !== command.owner.id) return Promise.reject(new ApiError('VALIDATION_ERROR'))
  const context: WriteContext = { csrfToken, signal, idempotencyKey: command.key }
  const itemProof = { ...context, expectedRevision: command.owner.revision }
  if (command.kind === 'replace-tags') {
    try {
      input(reviewedTags, command.before)
      const after = input(reviewedTags.max(100), command.after)
      return api.replaceItemTags(command.owner.id, { tag_ids: after.map((tag) => tag.id) }, itemProof)
    } catch (error) { return Promise.reject(error) }
  }
  if (command.kind === 'create-barcode') return api.createBarcode(command.owner.id, command.body, itemProof)
  if (command.kind === 'create-alias') return api.createAlias(command.owner.id, command.body, itemProof)
  if (command.kind === 'create-attribute') return api.createAttribute(command.owner.id, command.body, itemProof)
  const rowProof = { ...context, itemID: command.subject.item_id, expectedRevision: command.subject.revision }
  switch (command.kind) {
    case 'patch-barcode': return api.patchBarcode(command.subject.id, command.body, rowProof)
    case 'delete-barcode': return api.deleteBarcode(command.subject.id, rowProof)
    case 'patch-alias': return api.patchAlias(command.subject.id, command.body, rowProof)
    case 'delete-alias': return api.deleteAlias(command.subject.id, rowProof)
    case 'patch-attribute': return api.patchAttribute(command.subject.id, command.body, rowProof)
    case 'delete-attribute': return api.deleteAttribute(command.subject.id, rowProof)
  }
}
