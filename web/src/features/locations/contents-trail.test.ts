import { expect, it } from 'vitest'
import { ApiError } from '@/lib/api/client'
import type { LocationContentsRead } from '@/lib/api/locations'
import { appendContentsPage } from './contents-trail'

const unit = (id: string) => ({ id, item_id: 'item', location_id: 'room', asset_code: id, status: 'stored' as const, serial_number: null, notes: '', revision: 1 })
const stock = (item_id: string, location_id: string) => ({ item_id, location_id, quantity_base: 0, unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1, revision: 1 })
const child = (id: string) => ({ id, parent_id: 'room', name: id, code: id, kind: 'box', status: 'active' as const, sort_order: 0, description: '', revision: 1 })
function page(changes: Partial<LocationContentsRead['value']> = {}, revision = 3): LocationContentsRead {
  return { revision, value: { location_id: 'room', scope: 'subtree', units: [], stock: [], sub_locations: [], next_cursor: null, ...changes } }
}

it('keeps explicit pages, exact identities and independent Stock tuples without mutating prior trails', () => {
  const first = appendContentsPage([], page({ units: [unit('unit\0😀')], stock: [stock('a', 'b/c')], next_cursor: 'first' }), undefined, 'room', 'subtree')
  const second = appendContentsPage(first, page({ units: [unit('unit/😀')], stock: [stock('a/b', 'c')], sub_locations: [child('loc\0/😀')], next_cursor: 'second' }), 'first', 'room', 'subtree')
  const last = appendContentsPage(second, page(), 'second', 'room', 'subtree')
  expect(first).toHaveLength(1); expect(second).toHaveLength(2); expect(last).toHaveLength(3)
  expect(last[1]?.read.value.sub_locations[0]?.id).toBe('loc\0/😀')
  expect(last[2]?.read.value.units).toEqual([])
})

it.each([
  ['initial cursor', () => appendContentsPage([], page(), 'unrequested', 'room', 'subtree')],
  ['wrong owner', () => appendContentsPage([], page({ location_id: 'other' }), undefined, 'room', 'subtree')],
  ['wrong scope', () => appendContentsPage([], page({ scope: 'direct' }), undefined, 'room', 'subtree')],
  ['duplicate Units within a page', () => appendContentsPage([], page({ units: [unit('same'), unit('same')] }), undefined, 'room', 'subtree')],
  ['duplicate Stock within a page', () => appendContentsPage([], page({ stock: [stock('a', 'b'), stock('a', 'b')] }), undefined, 'room', 'subtree')],
  ['duplicate Locations within a page', () => appendContentsPage([], page({ sub_locations: [child('same'), child('same')] }), undefined, 'room', 'subtree')],
])('rejects %s', (_label, operation) => {
  expect(operation).toThrow(expect.objectContaining({ code: 'INVALID_RESPONSE' }))
})

it.each(['unit', 'stock', 'location'] as const)('rejects a repeated %s across pages without exposing the new page', (kind) => {
  const rows = kind === 'unit' ? { units: [unit('same')] } : kind === 'stock' ? { stock: [stock('same', 'room')] } : { sub_locations: [child('same')] }
  const trail = appendContentsPage([], page({ ...rows, next_cursor: 'next' }), undefined, 'room', 'subtree')
  expect(() => appendContentsPage(trail, page(rows), 'next', 'room', 'subtree')).toThrow(expect.objectContaining({ code: 'INVALID_RESPONSE' }))
  expect(trail).toHaveLength(1)
})

it('rejects an unrequested, repeated or cyclic continuation and changed aggregate revisions', () => {
  const first = appendContentsPage([], page({ next_cursor: 'one' }), undefined, 'room', 'subtree')
  expect(() => appendContentsPage(first, page(), 'other', 'room', 'subtree')).toThrow(expect.objectContaining({ code: 'INVALID_RESPONSE' }))
  expect(() => appendContentsPage(first, page({ next_cursor: 'one' }), 'one', 'room', 'subtree')).toThrow(expect.objectContaining({ code: 'INVALID_RESPONSE' }))
  expect(() => appendContentsPage(first, page({}, 4), 'one', 'room', 'subtree')).toThrow(expect.objectContaining({ code: 'INVALID_CURSOR' }))
  const second = appendContentsPage(first, page({ next_cursor: 'two' }), 'one', 'room', 'subtree')
  expect(() => appendContentsPage(second, page({ next_cursor: 'one' }), 'two', 'room', 'subtree')).toThrow(expect.objectContaining({ code: 'INVALID_RESPONSE' }))
  expect(() => appendContentsPage(appendContentsPage(second, page(), 'two', 'room', 'subtree'), page(), undefined, 'room', 'subtree'))
    .toThrow(expect.objectContaining({ code: 'INVALID_RESPONSE' }))
})

it('uses only safe error codes and does not retain a malformed page in a prior trail', () => {
  const trail = appendContentsPage([], page({ units: [unit('secret')], next_cursor: 'next' }), undefined, 'room', 'subtree')
  try { appendContentsPage(trail, page({ units: [unit('secret')] }), 'next', 'room', 'subtree') } catch (error) {
    expect(error).toBeInstanceOf(ApiError)
    expect(String(error)).not.toContain('secret')
  }
  expect(trail).toHaveLength(1)
})
