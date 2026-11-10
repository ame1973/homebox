import { beforeEach, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/client'
import * as catalog from '@/lib/api/catalog'
import * as holdings from '@/lib/api/holdings'
import * as locations from '@/lib/api/locations'
import * as mediaAPI from '@/lib/api/media'
import { readMediaMembership, readOwnerMedia } from './model'

vi.mock('@/lib/api/catalog', async (original) => ({ ...await original<typeof import('@/lib/api/catalog')>(), fetchItem: vi.fn(), fetchItemsPage: vi.fn() }))
vi.mock('@/lib/api/holdings', async (original) => ({ ...await original<typeof import('@/lib/api/holdings')>(), fetchUnit: vi.fn(), fetchUnitByIdentity: vi.fn(), fetchItemUnitsPage: vi.fn() }))
vi.mock('@/lib/api/locations', async (original) => ({ ...await original<typeof import('@/lib/api/locations')>(), fetchLocation: vi.fn(), fetchLocationsPage: vi.fn() }))
vi.mock('@/lib/api/media', async (original) => ({ ...await original<typeof import('@/lib/api/media')>(), fetchMedia: vi.fn(), fetchMediaLinksPage: vi.fn(), fetchOwnerMediaLinksPage: vi.fn() }))

const signal = () => new AbortController().signal
const item = { id: 'item', revision: 4, name: 'Owner', brand: null, model: '', category_id: null, description: '', notes: '',
  tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1, status: 'active' } as const
const unit = { id: 'unit', item_id: item.id, revision: 3, location_id: null, asset_code: 'Asset', status: 'stored', serial_number: null, notes: '' } as const
const location = { id: 'room', revision: 6, parent_id: null, name: 'Room', code: 'ROOM', kind: 'room', status: 'active', sort_order: 0, description: '' } as const
const image = { id: 'photo', revision: 7, sha256: 'a'.repeat(64), mime_type: 'image/heic', verification: 'unverified', width: 10, height: 20,
  bytes: 30, thumb_url: null, thumb_sha256: null } as const
const link = { id: 'link', revision: 2, media_id: image.id, owner_type: 'item', owner_id: item.id, is_cover: true, sort_order: -3,
  description: '\0Exact é\n', media: image } as const

beforeEach(() => {
  vi.resetAllMocks()
  vi.mocked(catalog.fetchItem).mockResolvedValue(item)
  vi.mocked(catalog.fetchItemsPage).mockResolvedValue({ data: [item], next_cursor: null })
  vi.mocked(holdings.fetchUnit).mockResolvedValue(unit)
  vi.mocked(holdings.fetchUnitByIdentity).mockResolvedValue(unit)
  vi.mocked(holdings.fetchItemUnitsPage).mockResolvedValue({ data: [unit], next_cursor: null })
  vi.mocked(locations.fetchLocation).mockResolvedValue(location)
  vi.mocked(locations.fetchLocationsPage).mockResolvedValue({ data: [location], next_cursor: null })
  vi.mocked(mediaAPI.fetchMedia).mockResolvedValue(image)
  vi.mocked(mediaAPI.fetchMediaLinksPage).mockResolvedValue({ data: [link], next_cursor: null })
  vi.mocked(mediaAPI.fetchOwnerMediaLinksPage).mockResolvedValue({ data: [link], next_cursor: null })
})

it.each([
  { scope: { kind: 'item', id: item.id } as const, source: item, reader: catalog.fetchItem, ownerType: 'item' },
  { scope: { kind: 'unit', id: unit.id } as const, source: unit, reader: holdings.fetchUnit, ownerType: 'unit' },
  { scope: { kind: 'location', id: location.id } as const, source: location, reader: locations.fetchLocation, ownerType: 'location' },
])('brackets the complete $ownerType collection with actual matching owner reads', async ({ scope, source, reader }) => {
  const selected = { ...link, owner_type: scope.kind, owner_id: source.id }
  vi.mocked(mediaAPI.fetchOwnerMediaLinksPage).mockResolvedValueOnce({ data: [selected], next_cursor: null })
  const abort = signal(), result = await readOwnerMedia(scope, abort)
  expect(result).toEqual({ owner: { kind: scope.kind, value: source }, links: [selected] })
  expect(reader).toHaveBeenCalledTimes(2)
  expect(reader).toHaveBeenNthCalledWith(1, source.id, abort); expect(reader).toHaveBeenNthCalledWith(2, source.id, abort)
  expect(mediaAPI.fetchOwnerMediaLinksPage).toHaveBeenCalledWith(scope.kind, source.id, { limit: 100 }, abort)
  expect(vi.mocked(reader).mock.invocationCallOrder[0]).toBeLessThan(vi.mocked(mediaAPI.fetchOwnerMediaLinksPage).mock.invocationCallOrder[0]!)
  expect(vi.mocked(mediaAPI.fetchOwnerMediaLinksPage).mock.invocationCallOrder[0]).toBeLessThan(vi.mocked(reader).mock.invocationCallOrder[1]!)
  expect(mediaAPI.fetchMedia).not.toHaveBeenCalled(); expect(mediaAPI.fetchMediaLinksPage).not.toHaveBeenCalled()
})

it('completes all owner pages without inventing sibling sets or truncating 205 legacy links', async () => {
  const rows = Array.from({ length: 205 }, (_, index) => ({ ...link, id: `link-${index}` }))
  vi.mocked(mediaAPI.fetchOwnerMediaLinksPage).mockResolvedValueOnce({ data: rows.slice(0, 100), next_cursor: 'first' })
    .mockResolvedValueOnce({ data: rows.slice(100, 200), next_cursor: 'second' }).mockResolvedValueOnce({ data: rows.slice(200), next_cursor: null })
  const abort = signal(), result = await readOwnerMedia({ kind: 'item', id: item.id }, abort)
  expect(result.links).toEqual(rows); expect(result.owner).toEqual({ kind: 'item', value: item })
  expect(mediaAPI.fetchOwnerMediaLinksPage).toHaveBeenNthCalledWith(1, 'item', item.id, { limit: 100 }, abort)
  expect(mediaAPI.fetchOwnerMediaLinksPage).toHaveBeenNthCalledWith(2, 'item', item.id, { limit: 100, cursor: 'first' }, abort)
  expect(mediaAPI.fetchOwnerMediaLinksPage).toHaveBeenNthCalledWith(3, 'item', item.id, { limit: 100, cursor: 'second' }, abort)
  expect(catalog.fetchItem).toHaveBeenCalledTimes(2); expect(holdings.fetchUnit).not.toHaveBeenCalled(); expect(locations.fetchLocation).not.toHaveBeenCalled()
})

it.each([{ ...item, revision: 5 }, { ...item, name: 'Changed without a revision' }, { ...item, model: null }])('withholds the owner collection when actual source changes', async (after) => {
  vi.mocked(catalog.fetchItem).mockResolvedValueOnce(item).mockResolvedValueOnce(after)
  await expect(readOwnerMedia({ kind: 'item', id: item.id }, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
})

it('compares flat owner and Media fields semantically, not by JSON key insertion order', async () => {
  const reversedItem = Object.fromEntries(Object.entries(item).reverse()) as typeof item
  const reversedImage = Object.fromEntries(Object.entries(image).reverse()) as typeof image
  vi.mocked(catalog.fetchItem).mockResolvedValueOnce(item).mockResolvedValueOnce(reversedItem)
  vi.mocked(mediaAPI.fetchOwnerMediaLinksPage).mockResolvedValueOnce({ data: [link], next_cursor: 'next' })
    .mockResolvedValueOnce({ data: [{ ...link, id: 'second', media: reversedImage }], next_cursor: null })
  expect((await readOwnerMedia({ kind: 'item', id: item.id }, signal())).links).toHaveLength(2)
  vi.mocked(mediaAPI.fetchMedia).mockResolvedValueOnce(image).mockResolvedValueOnce(reversedImage)
  vi.mocked(mediaAPI.fetchMediaLinksPage).mockResolvedValueOnce({ data: [{ ...link, media: reversedImage }], next_cursor: null })
  expect((await readMediaMembership(image.id, signal())).media).toEqual(image)
})

it.each([{ ...image, revision: 8 }, { ...image, width: 99 }])('rejects inconsistent repeated Media snapshots across owner pages', async (changed) => {
  vi.mocked(mediaAPI.fetchOwnerMediaLinksPage).mockResolvedValueOnce({ data: [link], next_cursor: 'next' })
    .mockResolvedValueOnce({ data: [{ ...link, id: 'second', media: changed }], next_cursor: null })
  await expect(readOwnerMedia({ kind: 'item', id: item.id }, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
})

it('does not treat an owner bracket as a proof that every embedded Media is globally current', async () => {
  const another = { ...image, id: 'other', revision: 11, width: 40 }
  vi.mocked(mediaAPI.fetchOwnerMediaLinksPage).mockResolvedValueOnce({ data: [link, { ...link, id: 'other-link', media_id: another.id, media: another }], next_cursor: null })
  const result = await readOwnerMedia({ kind: 'item', id: item.id }, signal())
  expect(result.links.map((value) => value.media)).toEqual([image, another])
  expect(mediaAPI.fetchMedia).not.toHaveBeenCalled()
})

it('withholds wrong-scope links, duplicate IDs and cyclic continuations rather than exposing a prefix', async () => {
  for (const invalid of [{ ...link, owner_type: 'unit' as const }, { ...link, owner_id: 'other' }, { ...link, media_id: 'other' }]) {
    vi.mocked(mediaAPI.fetchOwnerMediaLinksPage).mockResolvedValueOnce({ data: [invalid], next_cursor: null })
    await expect(readOwnerMedia({ kind: 'item', id: item.id }, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  }
  vi.mocked(mediaAPI.fetchOwnerMediaLinksPage).mockResolvedValueOnce({ data: [link], next_cursor: 'next' }).mockResolvedValueOnce({ data: [link], next_cursor: null })
  await expect(readOwnerMedia({ kind: 'item', id: item.id }, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  vi.mocked(mediaAPI.fetchOwnerMediaLinksPage).mockResolvedValueOnce({ data: [], next_cursor: 'loop' }).mockResolvedValueOnce({ data: [], next_cursor: 'loop' })
  await expect(readOwnerMedia({ kind: 'item', id: item.id }, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
})

it('snapshots the requested scope before any asynchronous source read settles', async () => {
  let release: ((value: typeof item) => void) | undefined
  vi.mocked(catalog.fetchItem).mockImplementationOnce(() => new Promise((resolve) => { release = resolve }))
  const scope = { kind: 'item' as const, id: String(item.id) }, abort = signal()
  const pending = readOwnerMedia(scope, abort)
  scope.id = 'changed-after-start'
  release?.(item)
  expect(await pending).toEqual({ owner: { kind: 'item', value: item }, links: [link] })
  expect(catalog.fetchItem).toHaveBeenNthCalledWith(1, item.id, abort); expect(catalog.fetchItem).toHaveBeenNthCalledWith(2, item.id, abort)
  expect(mediaAPI.fetchOwnerMediaLinksPage).toHaveBeenCalledWith('item', item.id, { limit: 100 }, abort)
})

it('resolves path-unaddressable retained Item and Location owners from their complete actual lists', async () => {
  const legacyItem = { ...item, id: 'item/\0😀' }, legacyLocation = { ...location, id: 'tree' }
  const itemLink = { ...link, owner_id: legacyItem.id }, locationLink = { ...link, owner_type: 'location' as const, owner_id: legacyLocation.id }
  vi.mocked(catalog.fetchItemsPage).mockResolvedValue({ data: [legacyItem], next_cursor: null })
  vi.mocked(locations.fetchLocationsPage).mockResolvedValue({ data: [legacyLocation], next_cursor: null })
  vi.mocked(mediaAPI.fetchOwnerMediaLinksPage).mockResolvedValueOnce({ data: [itemLink], next_cursor: null }).mockResolvedValueOnce({ data: [locationLink], next_cursor: null })
  const abort = signal()
  expect(await readOwnerMedia({ kind: 'item', id: legacyItem.id }, abort)).toEqual({ owner: { kind: 'item', value: legacyItem }, links: [itemLink] })
  expect(await readOwnerMedia({ kind: 'location', id: legacyLocation.id }, abort)).toEqual({ owner: { kind: 'location', value: legacyLocation }, links: [locationLink] })
  expect(catalog.fetchItem).not.toHaveBeenCalled(); expect(catalog.fetchItemsPage).toHaveBeenCalledTimes(2)
  expect(locations.fetchLocation).not.toHaveBeenCalled(); expect(locations.fetchLocationsPage).toHaveBeenCalledTimes(2)
  expect(mediaAPI.fetchOwnerMediaLinksPage).toHaveBeenNthCalledWith(1, 'item', legacyItem.id, { limit: 100 }, abort)
})

it('searches every actual Item list page when a retained owner ID cannot be addressed as a path', async () => {
  const legacy = { ...item, id: 'unsafe/item' }, rows = Array.from({ length: 100 }, (_, index) => ({ ...item, id: `item-${index}` }))
  vi.mocked(catalog.fetchItemsPage).mockResolvedValueOnce({ data: rows, next_cursor: 'next' }).mockResolvedValueOnce({ data: [legacy], next_cursor: null })
    .mockResolvedValueOnce({ data: rows, next_cursor: 'again' }).mockResolvedValueOnce({ data: [legacy], next_cursor: null })
  vi.mocked(mediaAPI.fetchOwnerMediaLinksPage).mockResolvedValueOnce({ data: [], next_cursor: null })
  const abort = signal()
  expect(await readOwnerMedia({ kind: 'item', id: legacy.id }, abort)).toEqual({ owner: { kind: 'item', value: legacy }, links: [] })
  expect(catalog.fetchItemsPage).toHaveBeenNthCalledWith(2, { limit: 100, cursor: 'next' }, abort)
  expect(catalog.fetchItemsPage).toHaveBeenNthCalledWith(4, { limit: 100, cursor: 'again' }, abort)
})

it('resolves a path-unaddressable Unit and parent directly from exact actual Unit source', async () => {
  const legacyUnit = { ...unit, id: 'unit/\0😀', item_id: 'item/\0😀' }, unitLink = { ...link, owner_type: 'unit' as const, owner_id: legacyUnit.id }
  vi.mocked(holdings.fetchUnitByIdentity).mockResolvedValue(legacyUnit)
  vi.mocked(mediaAPI.fetchOwnerMediaLinksPage).mockResolvedValueOnce({ data: [unitLink], next_cursor: null })
  const abort = signal()
  expect(await readOwnerMedia({ kind: 'unit', id: legacyUnit.id }, abort)).toEqual({ owner: { kind: 'unit', value: legacyUnit }, links: [unitLink] })
  expect(holdings.fetchUnit).not.toHaveBeenCalled(); expect(holdings.fetchItemUnitsPage).not.toHaveBeenCalled()
  expect(holdings.fetchUnitByIdentity).toHaveBeenCalledTimes(2)
  expect(holdings.fetchUnitByIdentity).toHaveBeenNthCalledWith(1, legacyUnit.id, abort)
})

it('rejects a conflicting parent scope and propagates actual missing or failed authoritative source', async () => {
  await expect(readOwnerMedia({ kind: 'unit', id: unit.id, itemID: 'other-item' }, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  vi.mocked(holdings.fetchUnitByIdentity).mockRejectedValueOnce(new ApiError('NOT_FOUND', { status: 404 }))
  await expect(readOwnerMedia({ kind: 'unit', id: 'unsafe/unit', itemID: 'unsafe/item' }, signal())).rejects.toMatchObject({ code: 'NOT_FOUND', status: 404 })
  vi.mocked(catalog.fetchItemsPage).mockResolvedValueOnce({ data: [], next_cursor: null })
  const missing = await readOwnerMedia({ kind: 'item', id: 'unsafe/item' }, signal()).catch((failure: unknown) => failure)
  expect(missing).toMatchObject({ code: 'NOT_FOUND' }); expect((missing as ApiError).status).toBeUndefined()
  vi.mocked(locations.fetchLocationsPage).mockRejectedValueOnce(new ApiError('NETWORK_ERROR'))
  await expect(readOwnerMedia({ kind: 'location', id: 'tree' }, signal())).rejects.toMatchObject({ code: 'NETWORK_ERROR' })
  expect(mediaAPI.fetchOwnerMediaLinksPage).not.toHaveBeenCalled()
})

it('completes all Media-owned pages between matching Media source reads', async () => {
  const rows = Array.from({ length: 205 }, (_, index) => ({ ...link, id: `link-${index}` }))
  vi.mocked(mediaAPI.fetchMediaLinksPage).mockResolvedValueOnce({ data: rows.slice(0, 100), next_cursor: 'first' })
    .mockResolvedValueOnce({ data: rows.slice(100, 200), next_cursor: 'second' }).mockResolvedValueOnce({ data: rows.slice(200), next_cursor: null })
  const abort = signal(), result = await readMediaMembership(image.id, abort)
  expect(result).toEqual({ media: image, links: rows })
  expect(mediaAPI.fetchMedia).toHaveBeenCalledTimes(2)
  expect(mediaAPI.fetchMediaLinksPage).toHaveBeenNthCalledWith(2, image.id, { limit: 100, cursor: 'first' }, abort)
  expect(mediaAPI.fetchMediaLinksPage).toHaveBeenNthCalledWith(3, image.id, { limit: 100, cursor: 'second' }, abort)
  expect(mediaAPI.fetchOwnerMediaLinksPage).not.toHaveBeenCalled(); expect(catalog.fetchItem).not.toHaveBeenCalled()
})

it.each([{ ...image, revision: 8 }, { ...image, mime_type: 'image/changed' }])('withholds the Media collection if source changes during the read', async (after) => {
  vi.mocked(mediaAPI.fetchMedia).mockResolvedValueOnce(image).mockResolvedValueOnce(after)
  await expect(readMediaMembership(image.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
})

it.each([{ ...link, media_id: 'other' }, { ...link, media: { ...image, revision: 8 } }, { ...link, media: { ...image, height: 21 } }])(
  'rejects Media page links inconsistent with the selected authority', async (value) => {
    vi.mocked(mediaAPI.fetchMediaLinksPage).mockResolvedValueOnce({ data: [value], next_cursor: null })
    await expect(readMediaMembership(image.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  },
)

it('preserves actual empty Media and owner collections, not missing or failed source', async () => {
  vi.mocked(mediaAPI.fetchMediaLinksPage).mockResolvedValueOnce({ data: [], next_cursor: null })
  vi.mocked(mediaAPI.fetchOwnerMediaLinksPage).mockResolvedValueOnce({ data: [], next_cursor: null })
  expect(await readMediaMembership(image.id, signal())).toEqual({ media: image, links: [] })
  expect(await readOwnerMedia({ kind: 'item', id: item.id }, signal())).toEqual({ owner: { kind: 'item', value: item }, links: [] })
  vi.mocked(mediaAPI.fetchMedia).mockRejectedValueOnce(new ApiError('NOT_FOUND', { status: 404 }))
  await expect(readMediaMembership('missing', signal())).rejects.toMatchObject({ code: 'NOT_FOUND', status: 404 })
})

it('rejects an actual read returning the wrong selected owner or Media identity', async () => {
  vi.mocked(catalog.fetchItem).mockResolvedValueOnce({ ...item, id: 'different' })
  await expect(readOwnerMedia({ kind: 'item', id: item.id }, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  expect(mediaAPI.fetchOwnerMediaLinksPage).not.toHaveBeenCalled()
  vi.mocked(mediaAPI.fetchMedia).mockResolvedValueOnce({ ...image, id: 'different' })
  await expect(readMediaMembership(image.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  expect(mediaAPI.fetchMediaLinksPage).not.toHaveBeenCalled()
})

it('propagates page failures and caller cancellation without automatic retry or a partial result', async () => {
  vi.mocked(mediaAPI.fetchMediaLinksPage).mockRejectedValueOnce(new ApiError('NETWORK_ERROR'))
  await expect(readMediaMembership(image.id, signal())).rejects.toMatchObject({ code: 'NETWORK_ERROR' })
  expect(mediaAPI.fetchMedia).toHaveBeenCalledTimes(1); expect(mediaAPI.fetchMediaLinksPage).toHaveBeenCalledTimes(1)
  vi.mocked(mediaAPI.fetchOwnerMediaLinksPage).mockResolvedValueOnce({ data: [link], next_cursor: 'next' }).mockRejectedValueOnce(new ApiError('NETWORK_ERROR'))
  await expect(readOwnerMedia({ kind: 'item', id: item.id }, signal())).rejects.toMatchObject({ code: 'NETWORK_ERROR' })
  expect(catalog.fetchItem).toHaveBeenCalledTimes(1); expect(mediaAPI.fetchOwnerMediaLinksPage).toHaveBeenCalledTimes(2)
  vi.mocked(catalog.fetchItem).mockClear()
  const controller = new AbortController()
  vi.mocked(catalog.fetchItem).mockResolvedValueOnce(item).mockImplementationOnce(async () => { controller.abort(); return item })
  await expect(readOwnerMedia({ kind: 'item', id: item.id }, controller.signal)).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
})

it('rejects malformed scope identities and unsupported kinds before reading any source', async () => {
  for (const scope of [{ kind: 'purchase', id: 'p' }, { kind: 'item', id: '' }, { kind: 'unit', id: '\uD800' },
    { kind: 'location', id: '😀'.repeat(129) }, { kind: 'item', id: item.id, extra: true }, { kind: 'unit', id: unit.id, itemID: '' }]) {
    await expect(readOwnerMedia(scope as never, signal())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
  }
  expect(catalog.fetchItem).not.toHaveBeenCalled(); expect(catalog.fetchItemsPage).not.toHaveBeenCalled()
  expect(holdings.fetchUnit).not.toHaveBeenCalled(); expect(locations.fetchLocation).not.toHaveBeenCalled(); expect(mediaAPI.fetchOwnerMediaLinksPage).not.toHaveBeenCalled()
})
