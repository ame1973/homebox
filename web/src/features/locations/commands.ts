import { ApiError, type WriteContext } from '@/lib/api/client'
import { freezeReviewedCommand } from '@/lib/reviewed-command'
import * as api from '@/lib/api/locations'
import type { components } from '@/lib/api/schema'

export { uncertainMutationFailure as uncertainLocationFailure } from '@/lib/reviewed-command'

type Schemas = components['schemas']
type Subject = Readonly<{ subject: api.Location }>
export type LocationCommandInput =
  | Readonly<{ kind: 'create'; body: Schemas['LocationCreate']; parentRevision?: number }>
  | (Subject & Readonly<{ kind: 'patch'; body: Schemas['LocationWrite'] }>)
  | (Subject & Readonly<{ kind: 'move'; body: Schemas['LocationMove'] }>)
  | (Subject & Readonly<{ kind: 'archive'; body: Schemas['LocationArchiveInput'] }>)
  | (Subject & Readonly<{ kind: 'restore' | 'delete' }>)
export type LocationCommand = LocationCommandInput & Readonly<{ key: string }>
export type LocationResult = api.Location | Schemas['ActionAck']

// A review is an immutable volatile proposal, not a persisted/offline write.
// New keys are created only when a person starts a new explicit review.
export function freezeLocationCommand(input: LocationCommandInput): LocationCommand {
  return freezeReviewedCommand(input)
}

export function commandName(command: LocationCommand): string {
  return command.kind === 'create' ? command.body.name : command.subject.name
}

export function sendLocationCommand(command: LocationCommand, csrfToken: string, signal: AbortSignal): Promise<LocationResult> {
  const context: WriteContext = { csrfToken, signal, idempotencyKey: command.key }
  if (command.kind === 'create') {
    if (command.body.parent_id != null) {
      if (command.parentRevision === undefined) return Promise.reject(new ApiError('VALIDATION_ERROR'))
      return api.createLocation({ ...command.body, parent_id: command.body.parent_id }, { ...context, expectedRevision: command.parentRevision })
    }
    return api.createLocation({ ...command.body, parent_id: null }, context)
  }
  const proof = { ...context, expectedRevision: command.subject.revision }
  switch (command.kind) {
    case 'patch': return api.patchLocation(command.subject.id, command.body, proof)
    case 'move': return api.moveLocation(command.subject.id, command.body, proof)
    case 'archive': return api.archiveLocation(command.subject.id, command.body, proof)
    case 'restore': return api.patchLocation(command.subject.id, { status: 'active' }, proof)
    case 'delete': return api.deleteLocation(command.subject.id, proof)
  }
}
