import { beforeEach, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/client'
import { fetchItem, type Item } from '@/lib/api/catalog'
import * as api from '@/lib/api/holdings'
import { readItemHoldings, readUnitHolding } from './model'

vi.mock('@/lib/api/catalog', () => ({ fetchItem: vi.fn() }))
vi.mock('@/lib/api/holdings', () => ({ fetchItemUnitsPage: vi.fn(), fetchStockPage: vi.fn(), fetchUnit: vi.fn() }))
const item: Item = { id: 'item', revision: 7, name: 'Owner', brand: null, model: '', category_id: null, description: '', notes: '',
  tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1, status: 'active' }
const quantity: Item = { ...item, tracking_mode: 'quantity', unit_code: 'kg', base_unit: 'g', scale_factor: 1000 }
const unit: api.Unit = { id: 'unit', item_id: item.id, revision: 2, location_id: null, asset_code: '  AST é  ', serial_number: null, notes: '', status: 'stored' }
const stock: api.Stock = { item_id: item.id, location_id: 'place', revision: 2, quantity_base: 0, unit_code: 'kg', base_unit: 'g', scale_factor: 1000 }
const signal = () => new AbortController().signal
beforeEach(() => {
  vi.mocked(fetchItem).mockReset().mockImplementation(async () => ({ ...item }))
  vi.mocked(api.fetchUnit).mockReset().mockImplementation(async () => ({ ...unit }))
  vi.mocked(api.fetchItemUnitsPage).mockReset().mockResolvedValue({ data: [unit], next_cursor: null })
  vi.mocked(api.fetchStockPage).mockReset().mockResolvedValue({ data: [stock], next_cursor: null })
})

it('brackets only the actual Unit collection with matching Item reads and does not invent an unqueried Stock set', async () => {
  const abort = signal()
  expect(await readItemHoldings(item.id, abort)).toEqual({ kind: 'units', item, units: [unit] })
  expect(fetchItem).toHaveBeenNthCalledWith(1, item.id, abort)
  expect(fetchItem).toHaveBeenNthCalledWith(2, item.id, abort)
  expect(api.fetchItemUnitsPage).toHaveBeenCalledExactlyOnceWith(item.id, { limit: 100 }, abort)
  expect(api.fetchStockPage).not.toHaveBeenCalled()
  expect(api.fetchUnit).not.toHaveBeenCalled()
})
it('brackets only exact Item-scoped Stock with matching measurement and preserves retained zero rows', async () => {
  vi.mocked(fetchItem).mockResolvedValue(quantity)
  const abort = signal()
  expect(await readItemHoldings(item.id, abort)).toEqual({ kind: 'stock', item: quantity, stock: [stock] })
  expect(api.fetchStockPage).toHaveBeenCalledExactlyOnceWith({ item_id: item.id, limit: 100 }, abort)
  expect(api.fetchItemUnitsPage).not.toHaveBeenCalled()
})
it.each(['units', 'stock'] as const)('completes all 205 %s rows and signed continuations before exposing a result', async (kind) => {
  const rows = Array.from({ length: 205 }, (_, index) => kind === 'units' ? { ...unit, id: `unit-${index}` } : { ...stock, location_id: `place-${index}` })
  if (kind === 'units') vi.mocked(api.fetchItemUnitsPage)
    .mockResolvedValueOnce({ data: rows.slice(0, 100) as api.Unit[], next_cursor: 'second' })
    .mockResolvedValueOnce({ data: rows.slice(100, 200) as api.Unit[], next_cursor: 'last' })
    .mockResolvedValueOnce({ data: rows.slice(200) as api.Unit[], next_cursor: null })
  else {
    vi.mocked(fetchItem).mockResolvedValue(quantity)
    vi.mocked(api.fetchStockPage)
      .mockResolvedValueOnce({ data: rows.slice(0, 100) as api.Stock[], next_cursor: 'second' })
      .mockResolvedValueOnce({ data: rows.slice(100, 200) as api.Stock[], next_cursor: 'last' })
      .mockResolvedValueOnce({ data: rows.slice(200) as api.Stock[], next_cursor: null })
  }
  const abort = signal(), result = await readItemHoldings(item.id, abort)
  expect(result.kind === 'units' ? result.units : result.stock).toEqual(rows)
  if (kind === 'units') expect(api.fetchItemUnitsPage).toHaveBeenLastCalledWith(item.id, { limit: 100, cursor: 'last' }, abort)
  else expect(api.fetchStockPage).toHaveBeenLastCalledWith({ item_id: item.id, limit: 100, cursor: 'last' }, abort)
  expect(fetchItem).toHaveBeenCalledTimes(2)
})
it.each([{ revision: 8 }, { name: 'Unversioned inconsistent name' }, { model: null }, { tracking_mode: 'quantity' as const }, { scale_factor: 2 }, { id: 'another' }])('rejects mixed Item interpretation %j without publishing a prefix', async (changed) => {
  vi.mocked(fetchItem).mockResolvedValueOnce(item).mockResolvedValueOnce({ ...item, ...changed })
  await expect(readItemHoldings(item.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
})
it('does not depend on JSON property insertion order and returns actual empty collections', async () => {
  vi.mocked(fetchItem).mockResolvedValueOnce(item).mockResolvedValueOnce(Object.fromEntries(Object.entries(item).reverse()) as Item)
  vi.mocked(api.fetchItemUnitsPage).mockResolvedValueOnce({ data: [], next_cursor: null })
  expect(await readItemHoldings(item.id, signal())).toEqual({ kind: 'units', item, units: [] })
  vi.mocked(fetchItem).mockResolvedValue(quantity)
  vi.mocked(api.fetchStockPage).mockResolvedValueOnce({ data: [], next_cursor: null })
  expect(await readItemHoldings(item.id, signal())).toEqual({ kind: 'stock', item: quantity, stock: [] })
})
it.each([{ item_id: 'another' }, { unit_code: 'L' }, { base_unit: 'ml' }, { scale_factor: 1 }])('rejects an inconsistent Stock owner/measurement %j', async (changed) => {
  vi.mocked(fetchItem).mockResolvedValue(quantity)
  vi.mocked(api.fetchStockPage).mockResolvedValueOnce({ data: [{ ...stock, ...changed }], next_cursor: null })
  await expect(readItemHoldings(item.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
})
it('rejects wrong Unit ownership and duplicate identities or cursor cycles across pages', async () => {
  vi.mocked(api.fetchItemUnitsPage).mockResolvedValueOnce({ data: [{ ...unit, item_id: 'other' }], next_cursor: null })
  await expect(readItemHoldings(item.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  vi.mocked(api.fetchItemUnitsPage).mockResolvedValueOnce({ data: [unit], next_cursor: 'next' }).mockResolvedValueOnce({ data: [unit], next_cursor: null })
  await expect(readItemHoldings(item.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  vi.mocked(fetchItem).mockResolvedValue(quantity)
  vi.mocked(api.fetchStockPage).mockResolvedValueOnce({ data: [stock], next_cursor: 'next' }).mockResolvedValueOnce({ data: [stock], next_cursor: null })
  await expect(readItemHoldings(item.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  vi.mocked(api.fetchStockPage).mockResolvedValueOnce({ data: [], next_cursor: 'loop' }).mockResolvedValueOnce({ data: [], next_cursor: 'loop' })
  await expect(readItemHoldings(item.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
})
it('propagates missing source/read failures without retrying or treating them as empty', async () => {
  vi.mocked(fetchItem).mockRejectedValueOnce(new ApiError('NOT_FOUND'))
  await expect(readItemHoldings(item.id, signal())).rejects.toMatchObject({ code: 'NOT_FOUND' })
  expect(api.fetchItemUnitsPage).not.toHaveBeenCalled()
  vi.mocked(api.fetchItemUnitsPage).mockRejectedValueOnce(new ApiError('NETWORK_ERROR'))
  await expect(readItemHoldings(item.id, signal())).rejects.toMatchObject({ code: 'NETWORK_ERROR' })
  expect(api.fetchItemUnitsPage).toHaveBeenCalledTimes(1)
  vi.mocked(api.fetchUnit).mockRejectedValueOnce(new ApiError('NOT_FOUND'))
  await expect(readUnitHolding(unit.id, signal())).rejects.toMatchObject({ code: 'NOT_FOUND' })
  expect(api.fetchUnit).toHaveBeenCalledTimes(1)
})
it('reads one independent Unit around owner metadata without loading siblings or Stock', async () => {
  const abort = signal()
  expect(await readUnitHolding(unit.id, abort)).toEqual({ item, unit })
  expect(api.fetchUnit).toHaveBeenNthCalledWith(1, unit.id, abort)
  expect(api.fetchUnit).toHaveBeenNthCalledWith(2, unit.id, abort)
  expect(fetchItem).toHaveBeenCalledExactlyOnceWith(unit.item_id, abort)
  expect(api.fetchItemUnitsPage).not.toHaveBeenCalled()
  expect(api.fetchStockPage).not.toHaveBeenCalled()
})
it.each([{ id: 'another' }, { item_id: 'another' }, { revision: 3 }, { serial_number: '' }, { notes: 'Inconsistent' }, { location_id: 'place' }, { status: 'disposed' as const }])('rejects mixed Unit state %j', async (changed) => {
  vi.mocked(api.fetchUnit).mockResolvedValueOnce(unit).mockResolvedValueOnce({ ...unit, ...changed })
  await expect(readUnitHolding(unit.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
})
it.each([{ ...item, id: 'another' }, quantity])('rejects an unrelated or invalid tracking owner for a Unit', async (owner) => {
  vi.mocked(fetchItem).mockResolvedValue(owner)
  await expect(readUnitHolding(unit.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
})
it.each(['item', 'unit'] as const)('withholds a final completed %s read after cancellation', async (kind) => {
  const controller = new AbortController()
  if (kind === 'item') vi.mocked(fetchItem).mockResolvedValueOnce(item).mockImplementationOnce(async () => { controller.abort(); return item })
  else vi.mocked(api.fetchUnit).mockResolvedValueOnce(unit).mockImplementationOnce(async () => { controller.abort(); return unit })
  await expect(kind === 'item' ? readItemHoldings(item.id, controller.signal) : readUnitHolding(unit.id, controller.signal)).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
})
