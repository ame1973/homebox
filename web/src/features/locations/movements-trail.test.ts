import { expect, it } from 'vitest'
import type { LocationMovementsRead } from '@/lib/api/locations'
import { appendMovementsPage } from './movements-trail'

const event = (id: string) => ({ id, location_id: 'room\0', occurred_at: 'legacy\ntext', from_parent_id: null, to_parent_id: 'old/parent' })
function page(changes: Partial<LocationMovementsRead['value']> = {}, revision = 3): LocationMovementsRead {
  return { revision, value: { location_id: 'room\0', deleted: false, data: [], next_cursor: null, ...changes } }
}

it('retains explicit exact pages without mutating the prior trail', () => {
  const first = appendMovementsPage([], page({ data: [event('\0first')], next_cursor: 'one' }), undefined, 'room\0')
  const second = appendMovementsPage(first, page({ data: [event('second')], next_cursor: 'two' }), 'one', 'room\0')
  const last = appendMovementsPage(second, page(), 'two', 'room\0')
  expect(first).toHaveLength(1); expect(second).toHaveLength(2); expect(last).toHaveLength(3)
  expect(last[0]?.read.value.data[0]?.occurred_at).toBe('legacy\ntext')
  expect(last[2]?.read.value.data).toEqual([])
})

it.each([
  ['initial cursor', () => appendMovementsPage([], page(), 'unrequested', 'room\0')],
  ['wrong owner', () => appendMovementsPage([], page({ location_id: 'other' }), undefined, 'room\0')],
  ['wrong event owner', () => appendMovementsPage([], page({ data: [{ ...event('event'), location_id: 'other' }] }), undefined, 'room\0')],
  ['duplicate within page', () => appendMovementsPage([], page({ data: [event('same'), event('same')] }), undefined, 'room\0')],
  ['reversed within page', () => appendMovementsPage([], page({ data: [event('z'), event('a')] }), undefined, 'room\0')],
])('rejects %s', (_label, operation) => {
  expect(operation).toThrow(expect.objectContaining({ code: 'INVALID_RESPONSE' }))
})

it('checks UTF-8 order across pages and rejects duplicate or reversed identities', () => {
  const first = appendMovementsPage([], page({ data: [event('')], next_cursor: 'one' }), undefined, 'room\0')
  expect(appendMovementsPage(first, page({ data: [event('😀')] }), 'one', 'room\0')).toHaveLength(2)
  expect(() => appendMovementsPage(first, page({ data: [event('')] }), 'one', 'room\0')).toThrow(expect.objectContaining({ code: 'INVALID_RESPONSE' }))
  expect(() => appendMovementsPage(first, page({ data: [event('a')] }), 'one', 'room\0')).toThrow(expect.objectContaining({ code: 'INVALID_RESPONSE' }))
  expect(first).toHaveLength(1)
})

it('rejects unrequested, repeated and cyclic continuations, changed revisions and inconsistent deletion state', () => {
  const first = appendMovementsPage([], page({ data: [event('a')], next_cursor: 'one' }), undefined, 'room\0')
  expect(() => appendMovementsPage(first, page(), 'other', 'room\0')).toThrow(expect.objectContaining({ code: 'INVALID_RESPONSE' }))
  expect(() => appendMovementsPage(first, page({ next_cursor: 'one' }), 'one', 'room\0')).toThrow(expect.objectContaining({ code: 'INVALID_RESPONSE' }))
  expect(() => appendMovementsPage(first, page({}, 4), 'one', 'room\0')).toThrow(expect.objectContaining({ code: 'INVALID_CURSOR' }))
  expect(() => appendMovementsPage(first, page({ deleted: true }), 'one', 'room\0')).toThrow(expect.objectContaining({ code: 'INVALID_RESPONSE' }))
  const second = appendMovementsPage(first, page({ data: [event('b')], next_cursor: 'two' }), 'one', 'room\0')
  expect(() => appendMovementsPage(second, page({ next_cursor: 'one' }), 'two', 'room\0')).toThrow(expect.objectContaining({ code: 'INVALID_RESPONSE' }))
  expect(() => appendMovementsPage(appendMovementsPage(second, page(), 'two', 'room\0'), page(), undefined, 'room\0')).toThrow(expect.objectContaining({ code: 'INVALID_RESPONSE' }))
})
