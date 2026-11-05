import { beforeEach, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/client'
import { fetchItem } from '@/lib/api/catalog'
import * as api from '@/lib/api/catalog-links'
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
beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(fetchItem).mockReset().mockResolvedValue(item)
  vi.mocked(api.fetchItemBarcodesPage).mockReset().mockResolvedValue({ data: [barcode], next_cursor: null })
  vi.mocked(api.fetchItemAliasesPage).mockReset().mockResolvedValue({ data: [alias], next_cursor: null })
  vi.mocked(api.fetchItemAttributesPage).mockReset().mockResolvedValue({ data: [attribute], next_cursor: null })
  vi.mocked(api.fetchItemTagsPage).mockReset().mockResolvedValue({ data: [tag], next_cursor: null })
  vi.mocked(readTags).mockReset().mockResolvedValue(buildTags([tag]))
})

it('brackets every complete owned collection with the real Item and preserves exact source data', async () => {
  const signal = new AbortController().signal
  const result = await readOwnedMetadata(item.id, signal)
  expect(result).toMatchObject({ item, barcodes: [barcode], aliases: [alias], attributes: [attribute], tags: [tag] })
  expect(result.availableTags.byID.get(tag.id)).toEqual(tag)
  expect(fetchItem).toHaveBeenNthCalledWith(1, item.id, signal)
  expect(fetchItem).toHaveBeenNthCalledWith(2, item.id, signal)
  for (const read of [api.fetchItemBarcodesPage, api.fetchItemAliasesPage, api.fetchItemAttributesPage, api.fetchItemTagsPage]) {
    expect(read).toHaveBeenCalledWith(item.id, { limit: 100 }, signal)
  }
  expect(readTags).toHaveBeenCalledWith(signal)
})

it('completes every page including a larger 205-member legacy set without installing a first-page cap', async () => {
  const barcodes = Array.from({ length: 101 }, (_, index) => ({ ...barcode, id: `barcode-${index}` }))
  const aliases = Array.from({ length: 101 }, (_, index) => ({ ...alias, id: `alias-${index}`, alias: `Alias${index}` }))
  const attributes = Array.from({ length: 101 }, (_, index) => ({ ...attribute, id: `attribute-${index}` }))
  const tags = Array.from({ length: 205 }, (_, index) => ({ ...tag, id: `tag-${index}`, name: `Tag${index}` }))
  vi.mocked(api.fetchItemBarcodesPage).mockResolvedValueOnce({ data: barcodes.slice(0, 100), next_cursor: 'barcodes-next' }).mockResolvedValueOnce({ data: barcodes.slice(100), next_cursor: null })
  vi.mocked(api.fetchItemAliasesPage).mockResolvedValueOnce({ data: aliases.slice(0, 100), next_cursor: 'aliases-next' }).mockResolvedValueOnce({ data: aliases.slice(100), next_cursor: null })
  vi.mocked(api.fetchItemAttributesPage).mockResolvedValueOnce({ data: attributes.slice(0, 100), next_cursor: 'attributes-next' }).mockResolvedValueOnce({ data: attributes.slice(100), next_cursor: null })
  vi.mocked(api.fetchItemTagsPage).mockResolvedValueOnce({ data: tags.slice(0, 100), next_cursor: 'tags-next' })
    .mockResolvedValueOnce({ data: tags.slice(100, 200), next_cursor: 'tags-last' }).mockResolvedValueOnce({ data: tags.slice(200), next_cursor: null })
  vi.mocked(readTags).mockResolvedValueOnce(buildTags(tags))
  const signal = new AbortController().signal
  const result = await readOwnedMetadata(item.id, signal)
  expect(result.barcodes).toEqual(barcodes)
  expect(result.aliases).toEqual(aliases)
  expect(result.attributes).toEqual(attributes)
  expect(result.tags).toEqual(tags)
  expect(api.fetchItemTagsPage).toHaveBeenLastCalledWith(item.id, { limit: 100, cursor: 'tags-last' }, signal)
})

it.each([{ ...item, revision: 8 }, { ...item, name: 'Impossible same-revision change' }, { ...item, model: null }])('rejects a mixed live owner interpretation without returning a prefix', async (current) => {
  vi.mocked(fetchItem).mockResolvedValueOnce(item).mockResolvedValueOnce(current)
  await expect(readOwnedMetadata(item.id, new AbortController().signal)).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
})

it('compares validated Item fields rather than JSON property insertion order', async () => {
  vi.mocked(fetchItem).mockResolvedValueOnce(item).mockResolvedValueOnce(Object.fromEntries(Object.entries(item).reverse()) as typeof item)
  expect((await readOwnedMetadata(item.id, new AbortController().signal)).item).toEqual(item)
})

it.each([{ available: [] }, { available: [{ ...tag, revision: 4 }] }, { available: [{ ...tag, name: 'ChangedLabel' }] }])('rejects unavailable or inconsistent assigned/available Tag metadata $available', async ({ available }) => {
  vi.mocked(readTags).mockResolvedValueOnce(buildTags(available))
  await expect(readOwnedMetadata(item.id, new AbortController().signal)).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
})

it('rejects duplicate page identities and cyclic cursors instead of exposing apparently complete lists', async () => {
  vi.mocked(api.fetchItemAliasesPage).mockResolvedValueOnce({ data: [alias], next_cursor: 'next' }).mockResolvedValueOnce({ data: [alias], next_cursor: null })
  await expect(readOwnedMetadata(item.id, new AbortController().signal)).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  vi.mocked(api.fetchItemAliasesPage).mockResolvedValueOnce({ data: [], next_cursor: 'cycle' }).mockResolvedValueOnce({ data: [], next_cursor: 'cycle' })
  await expect(readOwnedMetadata(item.id, new AbortController().signal)).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
})

it('propagates actual read failures without a partial result or an automatic retry', async () => {
  vi.mocked(api.fetchItemAttributesPage).mockRejectedValueOnce(new ApiError('NETWORK_ERROR'))
  await expect(readOwnedMetadata(item.id, new AbortController().signal)).rejects.toMatchObject({ code: 'NETWORK_ERROR' })
  expect(api.fetchItemAttributesPage).toHaveBeenCalledTimes(1)
  expect(fetchItem).toHaveBeenCalledTimes(1)
})

it('withholds the completed result if cancellation arrives at the final authoritative read', async () => {
  const controller = new AbortController()
  vi.mocked(fetchItem).mockResolvedValueOnce(item).mockImplementationOnce(async () => { controller.abort(); return item })
  await expect(readOwnedMetadata(item.id, controller.signal)).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
})

it('reports only added/removed membership, never counts unchanged IDs as a mutation', () => {
  expect(tagChanges(['a', 'b'], ['b', 'c'])).toEqual({ added: ['c'], removed: ['a'] })
  expect(tagChanges(['a', 'b'], ['b', 'a'])).toEqual({ added: [], removed: [] })
})
