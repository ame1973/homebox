import { beforeEach, expect, it, vi } from 'vitest'
import { fetchCategoriesPage, fetchTagsPage, type Category, type Tag } from '@/lib/api/taxonomy'
import { ApiError } from '@/lib/api/client'
import { hierarchyPath, subtreeIDs } from '@/lib/metadata'
import { buildCategoryTree, buildTags, readCategoryTree, readTags, visibleCategories, visibleTags } from './model'

vi.mock('@/lib/api/taxonomy', () => ({ fetchCategoriesPage: vi.fn(), fetchTagsPage: vi.fn() }))
const category = (id: string, parent_id: string | null = null, name = id): Category => ({ id, parent_id, name, revision: 1 })
const tag = (id: string, name = id): Tag => ({ id, name, revision: 1 })
beforeEach(() => { vi.mocked(fetchCategoriesPage).mockReset(); vi.mocked(fetchTagsPage).mockReset() })

it('sorts stable category identities, preserves duplicate names, and computes complete paths/membership', () => {
  const records = [category('later', null, 'Z'), category('child', 'first', 'Child'), category('first', null, 'A'), category('same', null, 'A')]
  const tree = buildCategoryTree(records)
  expect(tree.order).toEqual(['first', 'child', 'same', 'later'])
  expect(tree.depth.get('child')).toBe(1)
  expect(tree.descendantCount.get('first')).toBe(1)
  expect(hierarchyPath(tree, 'child').map((record) => record.id)).toEqual(['first', 'child'])
  expect(hierarchyPath(tree, null)).toEqual([])
  expect(Array.from(subtreeIDs(tree, 'first'))).toEqual(['first', 'child'])
  expect(() => hierarchyPath(tree, 'missing')).toThrow(ApiError)
  expect(records[0]?.id).toBe('later')
})

it.each([
  [category('a'), category('a')], [category('self', 'self')], [category('orphan', 'missing')],
  [category('a', 'b'), category('b', 'a')], [category('root'), category('a', 'b'), category('b', 'a')],
])('rejects inconsistent live category graphs rather than publishing a partial prefix', (...records) => {
  expect(() => buildCategoryTree(records)).toThrow(ApiError)
})

it('supports deep and wide category graphs without recursion or argument spreading', () => {
  const records = Array.from({ length: 1500 }, (_, index) => category(`node-${index}`, index === 0 ? null : `node-${index - 1}`))
  const tree = buildCategoryTree(records)
  expect(tree.order).toHaveLength(1500)
  expect(tree.depth.get('node-1499')).toBe(1499)
  expect(tree.descendantCount.get('node-0')).toBe(1499)
  expect(hierarchyPath(tree, 'node-1499')).toHaveLength(1500)
  expect(subtreeIDs(tree, 'node-0').size).toBe(1500)
  const wide = buildCategoryTree([category('root'), ...Array.from({ length: 3000 }, (_, index) => category(`wide-${index}`, 'root'))])
  expect(wide.order).toHaveLength(3001)
  expect(wide.descendantCount.get('root')).toBe(3000)
})

it('retains matching ancestors for search and respects explicit expansion without changing source labels', () => {
  const name = '  工具 😀 é  '
  const tree = buildCategoryTree([category('root'), category('child', 'root', name), category('other')])
  expect(visibleCategories(tree, new Set(), '').map((record) => record.id)).toEqual(['other', 'root'])
  expect(visibleCategories(tree, new Set(['root']), '').map((record) => record.id)).toEqual(['other', 'root', 'child'])
  expect(visibleCategories(tree, new Set(), '工具').map((record) => record.id)).toEqual(['root', 'child'])
  expect(visibleCategories(tree, new Set(), ' CHILD ').map((record) => record.id)).toEqual(['root', 'child'])
  expect(visibleCategories(tree, new Set(tree.order), 'not found')).toEqual([])
  expect(tree.byID.get('child')?.name).toBe(name)
})

it('preserves exact tag uniqueness rather than trimming, case-folding or normalizing names', () => {
  const records = [tag('upper', 'Tag'), tag('lower', 'tag'), tag('spaced', ' Tag '), tag('nfc', 'é'), tag('nfd', 'é')]
  const index = buildTags(records)
  expect(index.byID.size).toBe(5)
  expect(new Set(index.order.map((record) => record.name))).toEqual(new Set(records.map((record) => record.name)))
  expect(visibleTags(index, 'tag')).toHaveLength(3)
  expect(visibleTags(index, ' NFC ').map((record) => record.id)).toEqual(['nfc'])
  expect(visibleTags(index, '')).toHaveLength(5)
  expect(visibleTags(index, 'not found')).toEqual([])
  expect(() => buildTags([tag('same'), tag('same', 'Other')])).toThrow(ApiError)
  expect(() => buildTags([tag('a', 'Same'), tag('b', 'Same')])).toThrow(ApiError)
})

it('uses deterministic raw identity ties when locale comparison regards Unicode IDs as equivalent', () => {
  const first = category('é', null, 'Same')
  const second = category('é', null, 'Same')
  expect(buildCategoryTree([first, second]).order).toEqual(buildCategoryTree([second, first]).order)
  expect(buildTags([tag(first.id, 'A'), tag(second.id, 'B')]).order.map((record) => record.id)).toEqual([first.id, second.id])
})

it('completes both metadata lists through signed pages before publishing lookup state', async () => {
  const signal = new AbortController().signal
  vi.mocked(fetchCategoriesPage).mockResolvedValueOnce({ data: [category('root')], next_cursor: 'next-category' }).mockResolvedValueOnce({ data: [category('child', 'root')], next_cursor: null })
  vi.mocked(fetchTagsPage).mockResolvedValueOnce({ data: [tag('first')], next_cursor: 'next-tag' }).mockResolvedValueOnce({ data: [tag('last')], next_cursor: null })
  expect((await readCategoryTree(signal)).byID.size).toBe(2)
  expect((await readTags(signal)).byID.size).toBe(2)
  expect(fetchCategoriesPage).toHaveBeenNthCalledWith(2, { limit: 100, cursor: 'next-category' }, signal)
  expect(fetchTagsPage).toHaveBeenNthCalledWith(2, { limit: 100, cursor: 'next-tag' }, signal)
})

it('rejects repeated cursors, cross-page identities/exact tag names, and late canceled results', async () => {
  const signal = new AbortController().signal
  vi.mocked(fetchCategoriesPage).mockResolvedValueOnce({ data: [category('a')], next_cursor: 'next' }).mockResolvedValueOnce({ data: [category('b')], next_cursor: 'next' })
  await expect(readCategoryTree(signal)).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  vi.mocked(fetchTagsPage).mockResolvedValueOnce({ data: [tag('a')], next_cursor: 'next' }).mockResolvedValueOnce({ data: [tag('a')] })
  await expect(readTags(signal)).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  vi.mocked(fetchTagsPage).mockResolvedValueOnce({ data: [tag('a', 'Same')], next_cursor: 'next' }).mockResolvedValueOnce({ data: [tag('b', 'Same')] })
  await expect(readTags(signal)).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  const controller = new AbortController()
  vi.mocked(fetchTagsPage).mockImplementationOnce(async () => { controller.abort(); return { data: [] } })
  await expect(readTags(controller.signal)).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
})

it('never publishes a truncated category/tag prefix when a later page fails', async () => {
  vi.mocked(fetchCategoriesPage).mockResolvedValueOnce({ data: [category('a')], next_cursor: 'next' }).mockRejectedValueOnce(new ApiError('NETWORK_ERROR'))
  vi.mocked(fetchTagsPage).mockResolvedValueOnce({ data: [tag('a')], next_cursor: 'next' }).mockRejectedValueOnce(new ApiError('AUTHENTICATION_REQUIRED', { status: 401 }))
  await expect(readCategoryTree(new AbortController().signal)).rejects.toMatchObject({ code: 'NETWORK_ERROR' })
  await expect(readTags(new AbortController().signal)).rejects.toMatchObject({ code: 'AUTHENTICATION_REQUIRED', status: 401 })
})
