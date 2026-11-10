import { z } from 'zod'
import { ApiError, type WriteContext } from '@/lib/api/client'
import { input, pathIDSchema, text } from '@/lib/api/domain'
import type { MediaLink, MediaLinkView } from '@/lib/api/media'
import * as api from '@/lib/api/media-writes'
import type { components } from '@/lib/api/schema'
import { freezeReviewedCommand } from '@/lib/reviewed-command'
import { reviewedMediaMembership, reviewedOwnerMedia, sameMediaLink, type MediaMembership, type OwnerMedia } from './model'

export type MediaCommandInput = Readonly<{ kind: 'attach'; owner: OwnerMedia; membership: MediaMembership; body: api.MediaLinkMetadataInput }>
  | Readonly<{ kind: 'patch-link'; source: OwnerMedia; linkID: string; body: api.MediaLinkPatchInput }>
  | Readonly<{ kind: 'unlink'; source: OwnerMedia; linkID: string }>
export type MediaCommand = MediaCommandInput & Readonly<{ key: string }>
export type MediaResult = MediaLink | components['schemas']['ActionAck']

const envelope = z.discriminatedUnion('kind', [
  z.strictObject({ kind: z.literal('attach'), owner: z.unknown(), membership: z.unknown(), body: z.unknown() }),
  z.strictObject({ kind: z.literal('patch-link'), source: z.unknown(), linkID: z.string(), body: z.unknown() }),
  z.strictObject({ kind: z.literal('unlink'), source: z.unknown(), linkID: z.string() }),
])

// The two complete live scopes were read independently. Their overlap must
// agree, but this comparison does not turn either list into a server proof.
function requireConsistentScopes(owner: OwnerMedia, membership: MediaMembership) {
  const owners = new Map(owner.links.map((link) => [link.id, link]))
  const members = new Map(membership.links.map((link) => [link.id, link]))
  for (const link of owner.links) {
    const member = members.get(link.id)
    if ((member || link.media_id === membership.media.id) && (!member || !sameMediaLink(link, member))) throw new ApiError('VALIDATION_ERROR')
  }
  for (const member of membership.links) {
    if (member.owner_type === owner.owner.kind && member.owner_id === owner.owner.value.id) {
      const link = owners.get(member.id)
      if (!link || !sameMediaLink(link, member)) throw new ApiError('VALIDATION_ERROR')
    }
  }
}

function selectedLink(source: OwnerMedia, id: string): MediaLinkView {
  const linkID = input(pathIDSchema, id), link = source.links.find((value) => value.id === linkID)
  if (!link) throw new ApiError('VALIDATION_ERROR')
  return link
}

function checkedProposal(value: unknown): MediaCommandInput {
  const proposal = input(envelope, value)
  if (proposal.kind === 'attach') {
    const owner = reviewedOwnerMedia(proposal.owner), membership = reviewedMediaMembership(proposal.membership)
    const body = input(api.mediaLinkMetadataSchema, proposal.body)
    input(pathIDSchema, membership.media.id)
    requireConsistentScopes(owner, membership)
    if (body.is_cover && owner.links.some((link) => link.is_cover)) throw new ApiError('MEDIA_COVER_CONFLICT')
    return { kind: proposal.kind, owner, membership, body }
  }
  const source = reviewedOwnerMedia(proposal.source), link = selectedLink(source, proposal.linkID)
  if (proposal.kind === 'unlink') return { kind: proposal.kind, source, linkID: link.id }
  const body = input(api.mediaLinkPatchSchema, proposal.body)
  if (!link.is_cover && body.is_cover && source.links.some((other) => other.id !== link.id && other.is_cover)) throw new ApiError('MEDIA_COVER_CONFLICT')
  return { kind: proposal.kind, source, linkID: link.id, body }
}

export function freezeMediaCommand(proposal: MediaCommandInput): MediaCommand {
  return freezeReviewedCommand(checkedProposal(proposal))
}

// Dispatch only the original reviewed body and independent proofs. Joined Media
// and owner snapshots are review context, never additional link preconditions.
export async function sendMediaCommand(command: MediaCommand, csrfToken: string, signal: AbortSignal): Promise<MediaResult> {
  const { key, ...proposal } = command, checked = checkedProposal(proposal)
  const context: WriteContext = { csrfToken, signal, idempotencyKey: input(text(8, 128), key) }
  if (checked.kind === 'attach') return api.attachMedia(checked.membership.media.id, checked.body, { ...context, media: checked.membership.media, owner: checked.owner.owner })
  const link = selectedLink(checked.source, checked.linkID)
  if (checked.kind === 'patch-link') return api.patchMediaLink(link.id, checked.body, { ...context, link })
  return api.unlinkMediaLink(link.id, { ...context, link })
}

export function mediaCommandReadRoots(command: MediaCommand): readonly (readonly string[])[] {
  const owner = command.kind === 'attach' ? command.owner.owner : command.source.owner
  const roots = [['media'], ['catalog'], ['holdings'], ['purchases'], ['assets']]
  // A Unit can move after review without changing its link proof, so its actual
  // current location ancestry may differ even when the captured location is null.
  if (owner.kind !== 'item') roots.push(['locations'])
  return roots
}
