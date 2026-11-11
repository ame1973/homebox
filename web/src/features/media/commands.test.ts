import { beforeEach, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/client'
import * as media from '@/lib/api/media'
import * as api from '@/lib/api/media-writes'
import { freezeMediaCommand, mediaCommandReadRoots, sendMediaCommand, type MediaCommandInput } from './commands'
import type { MediaMembership, OwnerMedia } from './model'

vi.mock('@/lib/api/media-writes', async (original) => ({ ...await original<typeof import('@/lib/api/media-writes')>(), attachMedia: vi.fn(), patchMediaLink: vi.fn(), unlinkMediaLink: vi.fn() }))
vi.mock('@/lib/api/media', async (original) => ({ ...await original<typeof import('@/lib/api/media')>(), fetchMedia: vi.fn(), fetchMediaLinksPage: vi.fn(), fetchOwnerMediaLinksPage: vi.fn() }))

const item = { id: 'item/\0😀', revision: 4, name: 'Reviewed owner', brand: null, model: '', category_id: null, description: '', notes: '', tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1, status: 'active' } as const
const image = { id: 'links', revision: 7, sha256: 'a'.repeat(64), mime_type: 'image/heic', verification: 'unverified', width: 10, height: 20, bytes: 30, thumb_url: null, thumb_sha256: null } as const
const link = { id: 'view', revision: 2, media_id: image.id, owner_type: 'item', owner_id: item.id, is_cover: true, sort_order: -3, description: '\0Exact é\n', media: image } as const
const sibling = { ...link, id: 'sibling', owner_id: 'other' } as const
const source: OwnerMedia = { owner: { kind: 'item', value: item }, links: [link] }
const membership: MediaMembership = { media: image, links: [link, sibling] }
const proposals: MediaCommandInput[] = [
  { kind: 'attach', owner: source, membership, body: { is_cover: false, sort_order: 0, description: '' } },
  { kind: 'patch-link', source, linkID: link.id, body: { description: 'Reviewed é\n' } },
  { kind: 'unlink', source, linkID: link.id },
]
const ack = { ok: true, operation_id: 'event', revision: 3 } as const
const writers = [api.attachMedia, api.patchMediaLink, api.unlinkMediaLink]
const readers = [media.fetchMedia, media.fetchMediaLinksPage, media.fetchOwnerMediaLinksPage]
const withoutMedia = (({ media: _media, ...value }) => value)(link)
beforeEach(() => {
  vi.mocked(api.attachMedia).mockReset().mockResolvedValue({ ...withoutMedia, id: 'new-link', revision: 1, is_cover: false, sort_order: 0, description: '' })
  vi.mocked(api.patchMediaLink).mockReset().mockResolvedValue({ ...withoutMedia, revision: 3, description: 'Reviewed é\n' })
  vi.mocked(api.unlinkMediaLink).mockReset().mockResolvedValue(ack)
  for (const read of readers) vi.mocked(read).mockReset()
})

it.each(proposals)('freezes a complete volatile $kind with a new key and no credentials', (proposal) => {
  const first = freezeMediaCommand(proposal), second = freezeMediaCommand(proposal)
  expect(first).toEqual({ ...proposal, key: first.key }); expect(first.key).toMatch(/^[0-9a-f-]{36}$/); expect(second.key).not.toBe(first.key)
  expect(Object.isFrozen(first)).toBe(true); expect(first).not.toHaveProperty('csrfToken'); expect(first).not.toHaveProperty('signal'); expect(first).not.toHaveProperty('expectedRevision')
  const captured = first.kind === 'attach' ? first.owner : first.source
  expect(Object.isFrozen(captured)).toBe(true); expect(Object.isFrozen(captured.links)).toBe(true); expect(Object.isFrozen(captured.links[0]?.media)).toBe(true)
})

it('snapshots complete original owner, membership and metadata despite later caller changes', () => {
  const owner = { owner: { kind: 'item' as const, value: { ...item, revision: Number(item.revision) } }, links: [{ ...link, description: String(link.description), media: { ...image } }] }
  const members = { media: { ...image, revision: Number(image.revision) }, links: [{ ...link, media: { ...image, revision: Number(image.revision) } }, { ...sibling, media: { ...image } }] }
  const body = { is_cover: false, sort_order: 0, description: 'Original' }
  const command = freezeMediaCommand({ kind: 'attach', owner, membership: members, body })
  owner.owner.value.revision = 99; owner.links[0]!.description = 'Changed'; members.media.revision = 99; members.links[0]!.media.revision = 99; body.description = 'Changed'
  expect(command).toEqual({ ...proposals[0], body: { is_cover: false, sort_order: 0, description: 'Original' }, key: command.key })
})

it.each(proposals)('sends only the original $kind, caller CSRF, key and signal without reads', async (proposal) => {
  const command = freezeMediaCommand(proposal), signal = new AbortController().signal
  const context = { csrfToken: 'fresh-csrf', idempotencyKey: command.key, signal }
  await sendMediaCommand(command, context.csrfToken, signal)
  if (command.kind === 'attach') expect(api.attachMedia).toHaveBeenCalledExactlyOnceWith(image.id, command.body, { ...context, media: image, owner: source.owner })
  if (command.kind === 'patch-link') expect(api.patchMediaLink).toHaveBeenCalledExactlyOnceWith(link.id, command.body, { ...context, link })
  if (command.kind === 'unlink') expect(api.unlinkMediaLink).toHaveBeenCalledExactlyOnceWith(link.id, { ...context, link })
  expect(writers.reduce((sum, write) => sum + vi.mocked(write).mock.calls.length, 0)).toBe(1)
  for (const read of readers) expect(read).not.toHaveBeenCalled()
})

it.each(['.', '..', 'retained/link', 'retained\\link', '\0', 'link/\0😀\r\n', '😀'.repeat(127) + '/'])('freezes and sends the exact retained link %j with its independent proof', async (id) => {
  const retained = { ...link, id }, selected = { ...source, links: [retained] }, signal = new AbortController().signal
  const patch = freezeMediaCommand({ kind: 'patch-link', source: selected, linkID: id, body: { description: 'Reviewed' } })
  const unlink = freezeMediaCommand({ kind: 'unlink', source: selected, linkID: id })
  await sendMediaCommand(patch, 'csrf', signal); await sendMediaCommand(unlink, 'csrf', signal)
  expect(api.patchMediaLink).toHaveBeenCalledExactlyOnceWith(id, { description: 'Reviewed' }, { csrfToken: 'csrf', idempotencyKey: patch.key, signal, link: retained })
  expect(api.unlinkMediaLink).toHaveBeenCalledExactlyOnceWith(id, { csrfToken: 'csrf', idempotencyKey: unlink.key, signal, link: retained })
  expect(patch.key).not.toBe(unlink.key)
  for (const read of readers) expect(read).not.toHaveBeenCalled()
})

it.each(['.', '..', 'retained/media', 'retained\\media', '\0', 'media/\0😀\r\n', '😀'.repeat(127) + '/'])(
  'freezes and sends exact retained Media %j with both original reviewed proofs', async (id) => {
    const selected = { ...image, id }, owned = { ...link, media_id: id, media: selected }, other = { ...sibling, media_id: id, media: selected }
    const owner = { ...source, links: [owned] }, complete = { media: selected, links: [owned, other] }, signal = new AbortController().signal
    const command = freezeMediaCommand({ kind: 'attach', owner, membership: complete, body: { is_cover: false, description: 'Exact' } })
    await sendMediaCommand(command, 'csrf', signal)
    expect(api.attachMedia).toHaveBeenCalledExactlyOnceWith(id, { is_cover: false, description: 'Exact' },
      { csrfToken: 'csrf', idempotencyKey: command.key, signal, media: selected, owner: owner.owner })
    expect(Object.isFrozen(command)).toBe(true); expect(command.kind === 'attach' && Object.isFrozen(command.membership)).toBe(true)
    for (const read of readers) expect(read).not.toHaveBeenCalled()
  },
)

it('accepts semantic equality independent of field order across actual joined scopes', () => {
  const reordered = Object.fromEntries(Object.entries(image).reverse()) as typeof image
  const owner = { ...source, links: [{ ...link, media: reordered }] }
  expect(freezeMediaCommand({ kind: 'attach', owner, membership, body: {} })).toMatchObject({ owner, membership })
})

it('supports complete large owner and shared-media memberships without truncation', () => {
  const rows = Array.from({ length: 205 }, (_, index) => ({ ...link, id: `link-${index}`, is_cover: false }))
  const command = freezeMediaCommand({ kind: 'attach', owner: { ...source, links: rows }, membership: { media: image, links: [...rows, sibling] }, body: { is_cover: true } })
  expect(command.kind === 'attach' && command.owner.links).toHaveLength(205)
  expect(command.kind === 'attach' && command.membership.links).toHaveLength(206)
})

it.each([
  { kind: 'wrong', source, linkID: link.id },
  { ...proposals[0], csrfToken: 'not-a-proof' }, { ...proposals[0], expectedRevision: 7 },
  { ...proposals[0], owner: { ...source, links: [link, link] } },
  { ...proposals[0], owner: { ...source, links: [{ ...link, owner_id: 'other' }] } },
  { ...proposals[0], owner: { ...source, links: [link, { ...link, id: 'duplicate-media', media: { ...image, revision: 8 } }] } },
  { ...proposals[0], membership: { media: image, links: [link, link] } },
  { ...proposals[0], membership: { media: image, links: [{ ...link, media_id: 'other' }] } },
  { ...proposals[0], membership: { media: image, links: [{ ...link, media: { ...image, revision: 8 } }] } },
  { ...proposals[0], membership: { media: image, links: [sibling] } },
  { ...proposals[0], membership: { media: image, links: [{ ...link, description: 'Different source' }, sibling] } },
  { ...proposals[0], membership: { media: { ...image, id: '\uD800' }, links: [] } },
  { ...proposals[0], body: { sort_order: -1 } }, { ...proposals[0], body: { description: null } },
  { ...proposals[1], linkID: 'absent' }, { ...proposals[1], linkID: 'unsafe/link' }, { ...proposals[1], linkID: '' },
  { ...proposals[1], linkID: '\uD800' }, { ...proposals[1], linkID: '😀'.repeat(129) }, { ...proposals[1], body: {} },
  { ...proposals[2], source: { ...source, owner: { kind: 'item', value: { ...item, revision: 0 } } } },
])('rejects malformed review context %# before freeze or dispatch', async (proposal) => {
  expect(() => freezeMediaCommand(proposal as never)).toThrowError(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
  await expect(sendMediaCommand({ ...proposal, key: 'original-key' } as never, 'csrf', new AbortController().signal)).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
  for (const write of writers) expect(write).not.toHaveBeenCalled()
})

it('rejects a caller-supplied key at review time', () => {
  expect(() => freezeMediaCommand({ ...proposals[0], key: 'caller-key' } as never)).toThrowError(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
})

it('rejects a new conflicting cover without silently clearing existing covers', () => {
  expect(() => freezeMediaCommand({ kind: 'attach', owner: source, membership, body: { is_cover: true } })).toThrowError(expect.objectContaining({ code: 'MEDIA_COVER_CONFLICT' }))
  const notCover = { ...link, is_cover: false }, another = { ...link, id: 'other-cover' }
  const owned = { ...source, links: [notCover, another] }
  expect(() => freezeMediaCommand({ kind: 'patch-link', source: owned, linkID: link.id, body: { is_cover: true } })).toThrowError(expect.objectContaining({ code: 'MEDIA_COVER_CONFLICT' }))
  expect(freezeMediaCommand({ kind: 'patch-link', source: { ...source, links: [link, another] }, linkID: link.id, body: { description: 'Edit legacy cover' } })).toBeTruthy()
  expect(freezeMediaCommand({ kind: 'patch-link', source: { ...source, links: [link, another] }, linkID: link.id, body: { is_cover: false } })).toBeTruthy()
})

it('permits a link with path-unaddressable Media while requiring only the selected independent link proof', async () => {
  const legacy = { ...image, id: 'legacy/media\0😀' }, selected = { ...link, media_id: legacy.id, media: legacy }
  const command = freezeMediaCommand({ kind: 'unlink', source: { ...source, links: [selected] }, linkID: selected.id })
  await sendMediaCommand(command, 'csrf', new AbortController().signal)
  expect(api.unlinkMediaLink).toHaveBeenCalledWith('view', expect.objectContaining({ link: selected }))
  for (const read of readers) expect(read).not.toHaveBeenCalled()
})

it('reconciles only an explicit identical-key retry and refuses malformed keys', async () => {
  const command = freezeMediaCommand(proposals[2]!)
  await expect(sendMediaCommand({ ...command, key: 'short' }, 'csrf', new AbortController().signal)).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
  vi.mocked(api.unlinkMediaLink).mockRejectedValueOnce(new ApiError('NETWORK_ERROR')).mockResolvedValueOnce(ack)
  const first = new AbortController().signal, second = new AbortController().signal
  await expect(sendMediaCommand(command, 'old-csrf', first)).rejects.toMatchObject({ code: 'NETWORK_ERROR' })
  window.dispatchEvent(new Event('online')); expect(api.unlinkMediaLink).toHaveBeenCalledTimes(1)
  expect(await sendMediaCommand(command, 'fresh-csrf', second)).toEqual(ack)
  expect(api.unlinkMediaLink).toHaveBeenNthCalledWith(1, link.id, { csrfToken: 'old-csrf', idempotencyKey: command.key, signal: first, link })
  expect(api.unlinkMediaLink).toHaveBeenNthCalledWith(2, link.id, { csrfToken: 'fresh-csrf', idempotencyKey: command.key, signal: second, link })
})

it('invalidates Media and owner-dependent reads, including paths only when affected', () => {
  const roots = [['media'], ['catalog'], ['holdings'], ['purchases'], ['assets']]
  expect(mediaCommandReadRoots(freezeMediaCommand(proposals[2]!))).toEqual(roots)
  const location = { id: 'place', revision: 4, parent_id: null, name: 'Room', code: 'R', kind: 'room', status: 'active', sort_order: 0, description: '' } as const
  const atLocation = { ...link, owner_type: 'location' as const, owner_id: location.id }
  expect(mediaCommandReadRoots(freezeMediaCommand({ kind: 'unlink', source: { owner: { kind: 'location', value: location }, links: [atLocation] }, linkID: atLocation.id }))).toEqual([...roots, ['locations']])
  const unit = { id: 'unit', item_id: item.id, revision: 3, location_id: null, asset_code: 'AST', status: 'stored', serial_number: null, notes: '' } as const
  const onUnit = { ...link, owner_type: 'unit' as const, owner_id: unit.id }
  const unitSource = { owner: { kind: 'unit' as const, value: unit }, links: [onUnit] }
  // Placement can change without the independent link revision changing.
  expect(mediaCommandReadRoots(freezeMediaCommand({ kind: 'unlink', source: unitSource, linkID: onUnit.id }))).toEqual([...roots, ['locations']])
  expect(mediaCommandReadRoots(freezeMediaCommand({ kind: 'unlink', source: { ...unitSource, owner: { ...unitSource.owner, value: { ...unit, location_id: location.id } } }, linkID: onUnit.id }))).toEqual([...roots, ['locations']])
})
