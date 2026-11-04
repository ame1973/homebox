import { beforeEach, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/client'
import { fetchLocationsPage, type Location } from '@/lib/api/locations'
import { buildLocationTree, locationPath, readLocationTree, subtreeIDs, visibleLocations } from './tree'

vi.mock('@/lib/api/locations', () => ({ fetchLocationsPage: vi.fn() }))
const record = (id: string, parent_id: string | null = null, rest: Partial<Location> = {}): Location => ({ id, parent_id, revision: 1, name: id, code: id, status: 'active', kind: 'other', sort_order: 0, description: '', ...rest })
beforeEach(() => { vi.mocked(fetchLocationsPage).mockReset() })

it('sorts a complete parent graph without modifying source identity or metadata', () => {
  const rows = [record('later', null, { sort_order: 5 }), record('child', 'root'), record('root', null, { sort_order: -2 })]
  const tree = buildLocationTree(rows)
  expect(tree.order).toEqual(['root', 'child', 'later'])
  expect(tree.depth.get('child')).toBe(1)
  expect(tree.descendantCount.get('root')).toBe(1)
  expect(locationPath(tree, 'child').map((node) => node.id)).toEqual(['root', 'child'])
  expect(locationPath(tree, null)).toEqual([])
  expect(Array.from(subtreeIDs(tree, 'root'))).toEqual(['root', 'child'])
  expect(rows[0]?.id).toBe('later')
  expect(() => locationPath(tree, 'missing')).toThrow(ApiError)
})

it.each([
  [record('root'), record('root')], [record('self', 'self')], [record('child', 'missing')],
  [record('a', 'b'), record('b', 'a')], [record('root'), record('a', 'b'), record('b', 'a')],
])('rejects duplicate, orphan, self-parent and disconnected cyclic graphs', (...rows) => {
  expect(() => buildLocationTree(rows)).toThrow(ApiError)
})

it('handles depth over 1000 without recursive rendering preparation or truncation', () => {
  const rows: Location[] = []
  for (let index = 0; index < 1500; index++) rows.push(record(`node-${index}`, index === 0 ? null : `node-${index - 1}`))
  const tree = buildLocationTree(rows)
  expect(tree.order).toHaveLength(1500)
  expect(tree.depth.get('node-1499')).toBe(1499)
  expect(tree.descendantCount.get('node-0')).toBe(1499)
  expect(locationPath(tree, 'node-1499')).toHaveLength(1500)
  expect(subtreeIDs(tree, 'node-0').size).toBe(1500)
})

it('marks archived destination paths and retains ancestors when filtering', () => {
  const tree = buildLocationTree([record('root', null, { status: 'archived' }), record('child', 'root', { name: '書房', code: 'BOX-001' }), record('empty', null, { status: 'archived' })])
  expect(tree.canReceive.get('child')).toBe(false)
  expect(visibleLocations(tree, new Set(), '', false).map((value) => value.id)).toEqual(['root'])
  expect(visibleLocations(tree, new Set(['root']), '', false).map((value) => value.id)).toEqual(['root', 'child'])
  expect(visibleLocations(tree, new Set(), 'box-001', false).map((value) => value.id)).toEqual(['root', 'child'])
  expect(visibleLocations(tree, new Set(tree.order), '', true)).toHaveLength(3)
  expect(visibleLocations(tree, new Set(tree.order), 'not found', true)).toEqual([])
})

it('uses stable name/code/id ties and safe signed sort orders', () => {
  const tree = buildLocationTree([record('c', null, { name: 'Same', code: 'B' }), record('b', null, { name: 'Same', code: 'A' }), record('a', null, { name: 'Same', code: 'A' }), record('first', null, { sort_order: -Number.MAX_SAFE_INTEGER }), record('last', null, { sort_order: Number.MAX_SAFE_INTEGER })])
  expect(tree.order).toEqual(['first', 'a', 'b', 'c', 'last'])
})

it('reads every signed page before publishing a validated graph', async () => {
  const signal = new AbortController().signal
  vi.mocked(fetchLocationsPage).mockResolvedValueOnce({ data: [record('root')], next_cursor: 'next' }).mockResolvedValueOnce({ data: [record('child', 'root')], next_cursor: null })
  const tree = await readLocationTree(signal)
  expect(tree.order).toEqual(['root', 'child'])
  expect(fetchLocationsPage).toHaveBeenNthCalledWith(1, { limit: 100 }, signal)
  expect(fetchLocationsPage).toHaveBeenNthCalledWith(2, { limit: 100, cursor: 'next' }, signal)
})

it('rejects repeated cursors, cross-page IDs, partial parents and late aborted data', async () => {
  const controller = new AbortController()
  vi.mocked(fetchLocationsPage).mockResolvedValueOnce({ data: [record('a')], next_cursor: 'next' }).mockResolvedValueOnce({ data: [record('b')], next_cursor: 'next' })
  await expect(readLocationTree(controller.signal)).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  vi.mocked(fetchLocationsPage).mockResolvedValueOnce({ data: [record('a')], next_cursor: 'next' }).mockResolvedValueOnce({ data: [record('a')] })
  await expect(readLocationTree(controller.signal)).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  vi.mocked(fetchLocationsPage).mockResolvedValueOnce({ data: [record('child', 'missing')] })
  await expect(readLocationTree(controller.signal)).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  vi.mocked(fetchLocationsPage).mockImplementationOnce(async () => { controller.abort(); return { data: [] } })
  await expect(readLocationTree(controller.signal)).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
})

it('does not publish a truncated prefix when a later read fails', async () => {
  vi.mocked(fetchLocationsPage).mockResolvedValueOnce({ data: [record('root')], next_cursor: 'next' }).mockRejectedValueOnce(new ApiError('NETWORK_ERROR'))
  await expect(readLocationTree(new AbortController().signal)).rejects.toMatchObject({ code: 'NETWORK_ERROR' })
  expect(fetchLocationsPage).toHaveBeenCalledTimes(2)
})
