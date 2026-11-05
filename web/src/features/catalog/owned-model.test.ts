import { beforeEach, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/client'
import { fetchItem } from '@/lib/api/catalog'
import * as api from '@/lib/api/catalog-links'
import type { Tag } from '@/lib/api/taxonomy'
import { buildTags, readTags } from '@/features/taxonomy/model'
import { readOwnedMetadata, tagChanges } from './owned-model'

vi.mock('@/lib/api/catalog', () => ({ fetchItem: vi.fn() }))
vi.mock('@/lib/api/catalog-links', () => ({ fetchItemBarcodesPage: vi.fn(), fetchItemAliasesPage: vi.fn(), fetchItemAttributesPage: vi.fn(), fetchItemTagsPage: vi.fn() }))
vi.mock('@/features/taxonomy/model', async (original) => ({ ...await original<typeof import('@/features/taxonomy/model')>(), readTags: vi.fn() }))
const item = { id: 'item', revision: 7, name: 'Owner', brand: null, model: '', category_id: null, description: '', notes: '',
  tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1, status: 'active' } as const
const barcode = { id: 'barcode', item_id: item.id, revision: 2, symbology: 'UPC_A', raw_value: '036000291452', normalized_value: '036000291452' } as const
const alias = { id: 'alias', item_id: item.id, revision: 2, alias: '  Exact é  ' } as const
const attribute = { id: 'attribute', item_id: item.id, revision: 2, attribute_key: 'Key', attribute_value: '' } as const
const tag = { id: 'tag', revision: 3, name: 'Tag' } as const
const cases = [
  { kind: 'barcodes', read: 'fetchItemBarcodesPage', row: barcode },
  { kind: 'aliases', read: 'fetchItemAliasesPage', row: alias },
  { kind: 'attributes', read: 'fetchItemAttributesPage', row: attribute },
  { kind: 'tags', read: 'fetchItemTagsPage', row: tag },
] as const
beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(fetchItem).mockReset().mockResolvedValue(item)
  vi.mocked(api.fetchItemBarcodesPage).mockReset().mockResolvedValue({ data: [barcode], next_cursor: null })
  vi.mocked(api.fetchItemAliasesPage).mockReset().mockResolvedValue({ data: [alias], next_cursor: null })
  vi.mocked(api.fetchItemAttributesPage).mockReset().mockResolvedValue({ data: [attribute], next_cursor: null })
  vi.mocked(api.fetchItemTagsPage).mockReset().mockResolvedValue({ data: [tag], next_cursor: null })
  vi.mocked(readTags).mockReset().mockResolvedValue(buildTags([tag]))
})
it.each(cases)('brackets the complete $kind collection without reading or inventing unrelated collections', async ({ kind, read, row }) => {
  const signal = new AbortController().signal
  const result = await readOwnedMetadata(item.id, kind, signal)
  expect(result).toMatchObject({ kind, item, ...(kind === 'tags' ? { tags: [row] } : { rows: [row] }) })
  expect(fetchItem).toHaveBeenNthCalledWith(1, item.id, signal)
  expect(fetchItem).toHaveBeenNthCalledWith(2, item.id, signal)
  expect(api[read]).toHaveBeenCalledWith(item.id, { limit: 100 }, signal)
  for (const entry of cases) if (entry.read !== read) expect(api[entry.read]).not.toHaveBeenCalled()
  if (result.kind === 'tags') {
    expect(result.availableTags.byID.get(tag.id)).toEqual(tag)
    expect(readTags).toHaveBeenCalledWith(signal)
  } else { expect('availableTags' in result).toBe(false); expect(readTags).not.toHaveBeenCalled() }
})
it.each(cases)('completes every $kind page, including all 205 legacy members, without a first-page cap', async ({ kind, read, row }) => {
  const rows = Array.from({ length: kind === 'tags' ? 205 : 101 }, (_, index) => ({ ...row, id: `row-${index}`, ...('name' in row ? { name: `Tag${index}` } : {}) }))
  // Each selected reader has its own exact DTO type; configure it with the
  // corresponding original row shape rather than installing a partial response.
  if (kind === 'barcodes') vi.mocked(api.fetchItemBarcodesPage).mockResolvedValueOnce({ data: rows.slice(0, 100) as api.Barcode[], next_cursor: 'next' }).mockResolvedValueOnce({ data: rows.slice(100) as api.Barcode[], next_cursor: null })
  else if (kind === 'aliases') vi.mocked(api.fetchItemAliasesPage).mockResolvedValueOnce({ data: rows.slice(0, 100) as api.Alias[], next_cursor: 'next' }).mockResolvedValueOnce({ data: rows.slice(100) as api.Alias[], next_cursor: null })
  else if (kind === 'attributes') vi.mocked(api.fetchItemAttributesPage).mockResolvedValueOnce({ data: rows.slice(0, 100) as api.Attribute[], next_cursor: 'next' }).mockResolvedValueOnce({ data: rows.slice(100) as api.Attribute[], next_cursor: null })
  else {
    const tags = rows as Tag[]
    vi.mocked(api.fetchItemTagsPage).mockResolvedValueOnce({ data: tags.slice(0, 100), next_cursor: 'next' }).mockResolvedValueOnce({ data: tags.slice(100, 200), next_cursor: 'last' }).mockResolvedValueOnce({ data: tags.slice(200), next_cursor: null })
    vi.mocked(readTags).mockResolvedValueOnce(buildTags(tags))
  }
  const signal = new AbortController().signal
  const result = await readOwnedMetadata(item.id, kind, signal)
  expect(result.kind === 'tags' ? result.tags : result.rows).toEqual(rows)
  expect(api[read]).toHaveBeenLastCalledWith(item.id, { limit: 100, cursor: kind === 'tags' ? 'last' : 'next' }, signal)
})
it.each([{ ...item, revision: 8 }, { ...item, name: 'Impossible same-revision change' }, { ...item, model: null }])('rejects a mixed live owner interpretation without returning a prefix', async (current) => {
  vi.mocked(fetchItem).mockResolvedValueOnce(item).mockResolvedValueOnce(current)
  await expect(readOwnedMetadata(item.id, 'aliases', new AbortController().signal)).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
})
it('compares validated Item fields rather than JSON property insertion order', async () => {
  vi.mocked(fetchItem).mockResolvedValueOnce(item).mockResolvedValueOnce(Object.fromEntries(Object.entries(item).reverse()) as typeof item)
  expect((await readOwnedMetadata(item.id, 'aliases', new AbortController().signal)).item).toEqual(item)
})
it.each([{ available: [] }, { available: [{ ...tag, revision: 4 }] }, { available: [{ ...tag, name: 'ChangedLabel' }] }])('rejects unavailable or inconsistent assigned/available Tag metadata $available', async ({ available }) => {
  vi.mocked(readTags).mockResolvedValueOnce(buildTags(available))
  await expect(readOwnedMetadata(item.id, 'tags', new AbortController().signal)).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
})
it('rejects duplicate page identities and cyclic cursors instead of exposing apparently complete lists', async () => {
  vi.mocked(api.fetchItemAliasesPage).mockResolvedValueOnce({ data: [alias], next_cursor: 'next' }).mockResolvedValueOnce({ data: [alias], next_cursor: null })
  await expect(readOwnedMetadata(item.id, 'aliases', new AbortController().signal)).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  vi.mocked(api.fetchItemAliasesPage).mockResolvedValueOnce({ data: [], next_cursor: 'cycle' }).mockResolvedValueOnce({ data: [], next_cursor: 'cycle' })
  await expect(readOwnedMetadata(item.id, 'aliases', new AbortController().signal)).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
})
it('propagates actual selected-collection read failures without a partial result or an automatic retry', async () => {
  vi.mocked(api.fetchItemAttributesPage).mockRejectedValueOnce(new ApiError('NETWORK_ERROR'))
  await expect(readOwnedMetadata(item.id, 'attributes', new AbortController().signal)).rejects.toMatchObject({ code: 'NETWORK_ERROR' })
  expect(api.fetchItemAttributesPage).toHaveBeenCalledTimes(1)
  expect(fetchItem).toHaveBeenCalledTimes(1)
})
it('withholds the completed result if cancellation arrives at the final authoritative read', async () => {
  const controller = new AbortController()
  vi.mocked(fetchItem).mockResolvedValueOnce(item).mockImplementationOnce(async () => { controller.abort(); return item })
  await expect(readOwnedMetadata(item.id, 'aliases', controller.signal)).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
})
it('reports only added/removed membership, never counts unchanged IDs as a mutation', () => {
  expect(tagChanges(['a', 'b'], ['b', 'c'])).toEqual({ added: ['c'], removed: ['a'] })
  expect(tagChanges(['a', 'b'], ['b', 'a'])).toEqual({ added: [], removed: [] })
})
