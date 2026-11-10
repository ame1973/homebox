import { expect, it } from 'vitest'
import { applicationReturnPath } from './settings-navigation'
import { mediaOwnerRoute, mediaOwnerScope, mediaReturnPath } from './media-navigation'

const examples = [
  { kind: 'item', id: 'new' }, { kind: 'unit', id: 'unit/\0😀' }, { kind: 'location', id: 'tree' },
  { kind: 'item', id: '../x\\name +?#%&=' }, { kind: 'unit', id: '\0' }, { kind: 'location', id: '界'.repeat(128) },
] as const

it.each(examples)('round-trips an exact $kind owner identity through the Media route and login return', (scope) => {
  const path = mediaOwnerRoute(scope)
  expect(path.startsWith('/photos?')).toBe(true)
  expect(mediaOwnerScope(path.slice('/photos'.length))).toEqual(scope)
  expect(mediaReturnPath(path)).toBe(path)
  expect(applicationReturnPath(path)).toBe(path)
})

it('normalizes only the two validated owner parameters without losing their exact decoded values', () => {
  const path = '/photos?owner_id=room%2Fzero%00%F0%9F%98%80&owner_type=location'
  const expected = mediaOwnerRoute({ kind: 'location', id: 'room/zero\0😀' })
  expect(mediaReturnPath(path)).toBe(expected)
  expect(applicationReturnPath(path)).toBe(expected)
  expect(mediaOwnerScope('?owner_type=item&owner_id=a%26b%3Dc')).toEqual({ kind: 'item', id: 'a&b=c' })
})

it.each([
  '/media?owner_type=item&owner_id=x', '/photos', '/photos?', '/photos?owner_type=item', '/photos?owner_id=x', '/photos?owner_type=item&owner_id=',
  '/photos?owner_type=purchase&owner_id=x', '/photos?owner_type=item&owner_type=unit&owner_id=x',
  '/photos?owner_type=item&owner_id=x&owner_id=y', '/photos?owner_type=item&owner_id=x&extra=y',
  '/photos?owner_type=item&owner_id=%', '/photos?owner_type=item&owner_id=%FF', '/photos?owner_type=item&owner_id=%ED%A0%80',
  '/photos?owner_type=item&owner_id=x#fragment', '/photos?owner_type=item&owner_id=x\\y',
  '/photos/../photos?owner_type=item&owner_id=x', '//evil.example/photos?owner_type=item&owner_id=x',
  'https://evil.example/photos?owner_type=item&owner_id=x', '/photos/link?owner_type=item&owner_id=x',
  `/photos?owner_type=item&owner_id=${'x'.repeat(129)}`, `/photos?owner_type=item&owner_id=${'a'.repeat(4097)}`,
])('rejects unsafe, incomplete or unimplemented Media return %j', (path) => {
  expect(mediaReturnPath(path)).toBeUndefined()
  expect(applicationReturnPath(path)).toBeUndefined()
})

it('rejects invalid scope identities or extra fields before constructing a route', () => {
  for (const scope of [{ kind: 'purchase', id: 'id' }, { kind: 'item', id: '' }, { kind: 'unit', id: '\uD800' },
    { kind: 'location', id: '😀'.repeat(129) }, { kind: 'item', id: 'id', body: 'private' }]) {
    expect(() => mediaOwnerRoute(scope as never)).toThrowError(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
  }
  for (const search of ['', 'owner_type=item&owner_id=x', '?owner_type=item&owner_id=%', '?owner_type=item&owner_id=x&body=private']) {
    expect(mediaOwnerScope(search)).toBeUndefined()
  }
})
