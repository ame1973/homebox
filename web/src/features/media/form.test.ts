import { expect, it } from 'vitest'
import { mediaLinkCreateBody, mediaLinkDefaults, mediaLinkPatch, retainMediaLinkEdits } from './form'

const media = { id: 'unsafe/media\0😀', revision: 7, sha256: 'a'.repeat(64), mime_type: 'image/heic', verification: 'unverified', width: 10, height: 20, bytes: 30, thumb_url: null, thumb_sha256: null } as const
const original = { id: 'link', revision: 2, media_id: media.id, owner_type: 'item', owner_id: 'owner/\0😀', is_cover: true, sort_order: -3, description: '\0Original é\n' } as const
const view = { ...original, media } as const
const current = { ...original, revision: 3, is_cover: false, sort_order: 6, description: 'Concurrent caption' } as const

it('keeps visible attachment defaults and complete retained link values exactly', () => {
  expect(mediaLinkDefaults()).toEqual({ is_cover: false, sort_order: '0', description: '' })
  expect(mediaLinkDefaults(original)).toEqual({ is_cover: true, sort_order: '-3', description: '\0Original é\n' })
  expect(mediaLinkDefaults(view)).toEqual(mediaLinkDefaults(original))
  expect(mediaLinkCreateBody(mediaLinkDefaults())).toEqual({ is_cover: false, sort_order: 0, description: '' })
  expect(mediaLinkCreateBody({ is_cover: true, sort_order: '9007199254740991', description: '\0Exact 😀 é\n' })).toEqual({ is_cover: true, sort_order: Number.MAX_SAFE_INTEGER, description: '\0Exact 😀 é\n' })
})

it.each(['', '-1', '+1', '01', '1.0', '1e2', ' 1', '1 ', '1\n', '9007199254740992', '😀'])('rejects noncanonical or unsafe new sort position %j', (sort_order) => {
  expect(() => mediaLinkCreateBody({ is_cover: false, sort_order, description: '' })).toThrowError(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
  expect(() => mediaLinkPatch({ ...mediaLinkDefaults(original), sort_order }, original)).toThrowError(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
})

it('does not trim, repair malformed Unicode, coerce null or accept unknown fields', () => {
  for (const values of [
    { is_cover: false, sort_order: '0', description: '\uD800' },
    { is_cover: false, sort_order: '0', description: '😀'.repeat(10001) },
    { is_cover: null, sort_order: '0', description: '' },
    { is_cover: false, sort_order: 0, description: '' },
    { is_cover: false, sort_order: '0', description: '', owner_id: 'other' },
  ]) expect(() => mediaLinkCreateBody(values as never)).toThrowError(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
  expect(mediaLinkCreateBody({ is_cover: false, sort_order: '0', description: '  Exact é  \n' }).description).toBe('  Exact é  \n')
})

it('emits only explicitly changed fields and leaves omitted negative legacy order untouched', () => {
  const values = mediaLinkDefaults(view)
  expect(mediaLinkPatch(values, view)).toBeUndefined()
  expect(mediaLinkPatch({ ...values, description: '' }, original)).toEqual({ description: '' })
  expect(mediaLinkPatch({ ...values, is_cover: false }, original)).toEqual({ is_cover: false })
  expect(mediaLinkPatch({ ...values, sort_order: '0' }, original)).toEqual({ sort_order: 0 })
  expect(mediaLinkPatch({ is_cover: false, sort_order: '2', description: '  New  ' }, view)).toEqual({ is_cover: false, sort_order: 2, description: '  New  ' })
  expect(mediaLinkPatch({ ...mediaLinkDefaults(current), sort_order: '9007199254740991' }, current)).toEqual({ sort_order: Number.MAX_SAFE_INTEGER })
})

it('adopts concurrent untouched values only by explicit choice while retaining invalid raw edits', () => {
  const unchanged = mediaLinkDefaults(original)
  expect(retainMediaLinkEdits(unchanged, original, current)).toEqual(mediaLinkDefaults(current))
  expect(retainMediaLinkEdits({ ...unchanged, description: '' }, view, current)).toEqual({ is_cover: false, sort_order: '6', description: '' })
  expect(retainMediaLinkEdits({ ...unchanged, is_cover: false, sort_order: '01', description: '\uD800' }, original, current)).toEqual({ is_cover: false, sort_order: '01', description: '\uD800' })
  expect(retainMediaLinkEdits({ ...unchanged, sort_order: '-1' }, original, { ...current, sort_order: -5 })).toEqual({ is_cover: false, sort_order: '-1', description: current.description })
  expect(() => mediaLinkPatch(retainMediaLinkEdits({ ...unchanged, sort_order: '01' }, original, current), current)).toThrowError(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
})

it.each([
  { id: 'other-link' }, { media_id: 'other-media' }, { owner_type: 'unit' as const }, { owner_id: 'other-owner' },
])('never adopts metadata from a differently identified independent link %#', (change) => {
  expect(() => retainMediaLinkEdits(mediaLinkDefaults(original), original, { ...current, ...change })).toThrowError(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
})

it.each([{ revision: 0 }, { description: '\uD800' }])('rejects malformed retained source %#', (change) => {
  const changed = { ...current, ...change }
  expect(() => retainMediaLinkEdits(mediaLinkDefaults(original), original, changed)).toThrowError(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
  expect(() => mediaLinkDefaults(changed)).toThrowError(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
})

it('rejects malformed raw values during adoption without silently erasing local fields', () => {
  for (const values of [{ is_cover: false, sort_order: '0', description: '', extra: true }, { is_cover: false, sort_order: null, description: '' }]) {
    expect(() => retainMediaLinkEdits(values as never, original, current)).toThrowError(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
  }
})
