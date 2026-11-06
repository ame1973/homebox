import { beforeEach, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/client'
import { fetchItem, type Item } from '@/lib/api/catalog'
import { fetchItemUnitsPage, type Unit } from '@/lib/api/holdings'
import * as api from '@/lib/api/purchases'
import { readItemPurchases, readPurchaseAllocation, readPurchaseDetail } from './model'

vi.mock('@/lib/api/catalog', () => ({ fetchItem: vi.fn() }))
vi.mock('@/lib/api/holdings', () => ({ fetchItemUnitsPage: vi.fn() }))
vi.mock('@/lib/api/purchases', () => ({ fetchPurchase: vi.fn(), fetchItemPurchasesPage: vi.fn(), fetchPurchaseLinesPage: vi.fn() }))
const item: Item = { id: 'item', revision: 7, name: 'Original Item', brand: null, model: '', category_id: null, description: '', notes: '',
  tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1, status: 'active' }
const quantity: Item = { ...item, tracking_mode: 'quantity', unit_code: 'kg', base_unit: 'g', scale_factor: 1000 }
const purchase: api.Purchase = { id: 'purchase', revision: 12, merchant_name: '  Exact é  ', purchased_at: '2026-11-06T12:00:00.123456789+08:00', currency: 'JPY', receipt_note: 'Original' }
const line: api.PurchaseLine = { id: 'line', revision: 1, purchase_id: purchase.id, item_id: item.id, item_unit_id: 'unit', quantity_base: 2, paid_minor: 0, note: 'Retained bulk allocation' }
const unit: Unit = { id: 'unit', revision: 3, item_id: item.id, location_id: null, asset_code: '  AST é  ', serial_number: null, notes: '', status: 'disposed' }
const signal = () => new AbortController().signal
beforeEach(() => {
  vi.mocked(fetchItem).mockReset().mockImplementation(async () => ({ ...item }))
  vi.mocked(fetchItemUnitsPage).mockReset().mockResolvedValue({ data: [unit], next_cursor: null })
  vi.mocked(api.fetchPurchase).mockReset().mockImplementation(async () => ({ ...purchase }))
  vi.mocked(api.fetchItemPurchasesPage).mockReset().mockResolvedValue({ data: [purchase], next_cursor: null })
  vi.mocked(api.fetchPurchaseLinesPage).mockReset().mockResolvedValue({ data: [line], next_cursor: null })
})

it('completes only the owned Lines between matching full Purchase reads and preserves retained zero/bulk history', async () => {
  const abort = signal()
  expect(await readPurchaseDetail(purchase.id, abort)).toEqual({ purchase, lines: [line] })
  expect(api.fetchPurchase).toHaveBeenNthCalledWith(1, purchase.id, abort)
  expect(api.fetchPurchase).toHaveBeenNthCalledWith(2, purchase.id, abort)
  expect(api.fetchPurchaseLinesPage).toHaveBeenCalledExactlyOnceWith(purchase.id, { limit: 100 }, abort)
  expect(fetchItem).not.toHaveBeenCalled(); expect(fetchItemUnitsPage).not.toHaveBeenCalled(); expect(api.fetchItemPurchasesPage).not.toHaveBeenCalled()
})
it('brackets Item-associated headers without reading their Lines, Units or unrelated Items', async () => {
  const abort = signal()
  expect(await readItemPurchases(item.id, abort)).toEqual({ item, purchases: [purchase] })
  expect(fetchItem).toHaveBeenNthCalledWith(1, item.id, abort); expect(fetchItem).toHaveBeenNthCalledWith(2, item.id, abort)
  expect(api.fetchItemPurchasesPage).toHaveBeenCalledExactlyOnceWith(item.id, { limit: 100 }, abort)
  expect(api.fetchPurchase).not.toHaveBeenCalled(); expect(api.fetchPurchaseLinesPage).not.toHaveBeenCalled(); expect(fetchItemUnitsPage).not.toHaveBeenCalled()
})
it.each(['lines', 'headers', 'units'] as const)('completes all 205 %s rows before returning any review', async (kind) => {
  const rows = Array.from({ length: 205 }, (_, index) => kind === 'lines' ? { ...line, id: `line-${index}` } : kind === 'headers' ? { ...purchase, id: `purchase-${index}` } : { ...unit, id: `unit-${index}` })
  const pages = [rows.slice(0, 100), rows.slice(100, 200), rows.slice(200)]
  if (kind === 'lines') for (const [index, data] of pages.entries()) vi.mocked(api.fetchPurchaseLinesPage).mockResolvedValueOnce({ data: data as api.PurchaseLine[], next_cursor: index < 2 ? `next-${index}` : null })
  if (kind === 'headers') for (const [index, data] of pages.entries()) vi.mocked(api.fetchItemPurchasesPage).mockResolvedValueOnce({ data: data as api.Purchase[], next_cursor: index < 2 ? `next-${index}` : null })
  if (kind === 'units') for (const [index, data] of pages.entries()) vi.mocked(fetchItemUnitsPage).mockResolvedValueOnce({ data: data as Unit[], next_cursor: index < 2 ? `next-${index}` : null })
  const abort = signal()
  const result = kind === 'lines' ? (await readPurchaseDetail(purchase.id, abort)).lines : kind === 'headers' ? (await readItemPurchases(item.id, abort)).purchases : await readPurchaseAllocation(purchase.id, item.id, abort)
  expect(Array.isArray(result) ? result : 'units' in result ? result.units : undefined).toEqual(rows)
  if (kind === 'lines') expect(api.fetchPurchaseLinesPage).toHaveBeenLastCalledWith(purchase.id, { limit: 100, cursor: 'next-1' }, abort)
  if (kind === 'headers') expect(api.fetchItemPurchasesPage).toHaveBeenLastCalledWith(item.id, { limit: 100, cursor: 'next-1' }, abort)
  if (kind === 'units') expect(fetchItemUnitsPage).toHaveBeenLastCalledWith(item.id, { limit: 100, cursor: 'next-1' }, abort)
})
it('contains the Item/Unit interval inside matching Purchase reads without a physical/financial eligibility guess', async () => {
  const abort = signal()
  expect(await readPurchaseAllocation(purchase.id, item.id, abort)).toEqual({ kind: 'individual', purchase, item, units: [unit] })
  expect(vi.mocked(api.fetchPurchase).mock.invocationCallOrder[0]).toBeLessThan(vi.mocked(fetchItem).mock.invocationCallOrder[0]!)
  expect(vi.mocked(fetchItem).mock.invocationCallOrder[0]).toBeLessThan(vi.mocked(fetchItemUnitsPage).mock.invocationCallOrder[0]!)
  expect(vi.mocked(fetchItemUnitsPage).mock.invocationCallOrder[0]).toBeLessThan(vi.mocked(fetchItem).mock.invocationCallOrder[1]!)
  expect(vi.mocked(fetchItem).mock.invocationCallOrder[1]).toBeLessThan(vi.mocked(api.fetchPurchase).mock.invocationCallOrder[1]!)
  expect(api.fetchPurchaseLinesPage).not.toHaveBeenCalled(); expect(api.fetchItemPurchasesPage).not.toHaveBeenCalled()
})
it('reads no holding collection for quantity allocation and invents no empty Unit/Stock authority', async () => {
  vi.mocked(fetchItem).mockResolvedValue(quantity)
  expect(await readPurchaseAllocation(purchase.id, item.id, signal())).toEqual({ kind: 'quantity', purchase, item: quantity })
  expect(fetchItemUnitsPage).not.toHaveBeenCalled(); expect(api.fetchPurchaseLinesPage).not.toHaveBeenCalled()
})
it.each([{ revision: 13 }, { merchant_name: 'Unversioned change' }, { currency: 'USD' }, { purchased_at: '2026-11-06T04:00:00.123456789Z' }, { receipt_note: '' }, { id: 'other' }])('rejects mixed Purchase source %j in both review scopes', async (changed) => {
  vi.mocked(api.fetchPurchase).mockResolvedValueOnce(purchase).mockResolvedValueOnce({ ...purchase, ...changed })
  await expect(readPurchaseDetail(purchase.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  vi.mocked(api.fetchPurchase).mockResolvedValueOnce(purchase).mockResolvedValueOnce({ ...purchase, ...changed })
  await expect(readPurchaseAllocation(purchase.id, item.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
})
it.each([{ revision: 8 }, { name: 'Unversioned change' }, { tracking_mode: 'individual' as const }, { base_unit: 'ml' }, { scale_factor: 1 }, { id: 'other' }])('rejects mixed Item source %j without an associated/allocation prefix', async (changed) => {
  const initial = quantity
  vi.mocked(fetchItem).mockResolvedValueOnce(initial).mockResolvedValueOnce({ ...initial, ...changed })
  await expect(readItemPurchases(item.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  vi.mocked(fetchItem).mockResolvedValueOnce(initial).mockResolvedValueOnce({ ...initial, ...changed })
  await expect(readPurchaseAllocation(purchase.id, item.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
})
it('rejects consistent but unrelated identities and foreign Line/Unit owners', async () => {
  vi.mocked(api.fetchPurchase).mockResolvedValue({ ...purchase, id: 'other' })
  await expect(readPurchaseDetail(purchase.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  await expect(readPurchaseAllocation(purchase.id, item.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  vi.mocked(api.fetchPurchase).mockResolvedValue(purchase)
  vi.mocked(api.fetchPurchaseLinesPage).mockResolvedValueOnce({ data: [{ ...line, purchase_id: 'other' }], next_cursor: null })
  await expect(readPurchaseDetail(purchase.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  vi.mocked(fetchItemUnitsPage).mockResolvedValueOnce({ data: [{ ...unit, item_id: 'other' }], next_cursor: null })
  await expect(readPurchaseAllocation(purchase.id, item.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  vi.mocked(fetchItem).mockResolvedValue({ ...item, id: 'other' })
  await expect(readItemPurchases(item.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  await expect(readPurchaseAllocation(purchase.id, item.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
})
it('accepts actual empty collections, retained unknown currency and reordered flat source fields', async () => {
  const retained = { ...purchase, currency: 'ZZZ' }
  vi.mocked(api.fetchPurchase).mockResolvedValueOnce(retained).mockResolvedValueOnce(Object.fromEntries(Object.entries(retained).reverse()) as api.Purchase)
  vi.mocked(api.fetchPurchaseLinesPage).mockResolvedValueOnce({ data: [], next_cursor: null })
  expect(await readPurchaseDetail(purchase.id, signal())).toEqual({ purchase: retained, lines: [] })
  vi.mocked(api.fetchItemPurchasesPage).mockResolvedValueOnce({ data: [], next_cursor: null })
  expect(await readItemPurchases(item.id, signal())).toEqual({ item, purchases: [] })
  vi.mocked(fetchItemUnitsPage).mockResolvedValueOnce({ data: [], next_cursor: null })
  expect(await readPurchaseAllocation(purchase.id, item.id, signal())).toEqual({ kind: 'individual', purchase, item, units: [] })
})
it.each(['lines', 'headers', 'units'] as const)('rejects duplicate identities and cursor cycles across %s pages', async (kind) => {
  const read = () => kind === 'lines' ? readPurchaseDetail(purchase.id, signal()) : kind === 'headers' ? readItemPurchases(item.id, signal()) : readPurchaseAllocation(purchase.id, item.id, signal())
  if (kind === 'lines') vi.mocked(api.fetchPurchaseLinesPage).mockResolvedValueOnce({ data: [line], next_cursor: 'next' }).mockResolvedValueOnce({ data: [line], next_cursor: null })
  if (kind === 'headers') vi.mocked(api.fetchItemPurchasesPage).mockResolvedValueOnce({ data: [purchase], next_cursor: 'next' }).mockResolvedValueOnce({ data: [purchase], next_cursor: null })
  if (kind === 'units') vi.mocked(fetchItemUnitsPage).mockResolvedValueOnce({ data: [unit], next_cursor: 'next' }).mockResolvedValueOnce({ data: [unit], next_cursor: null })
  await expect(read()).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  if (kind === 'lines') vi.mocked(api.fetchPurchaseLinesPage).mockResolvedValueOnce({ data: [], next_cursor: 'loop' }).mockResolvedValueOnce({ data: [], next_cursor: 'loop' })
  if (kind === 'headers') vi.mocked(api.fetchItemPurchasesPage).mockResolvedValueOnce({ data: [], next_cursor: 'loop' }).mockResolvedValueOnce({ data: [], next_cursor: 'loop' })
  if (kind === 'units') vi.mocked(fetchItemUnitsPage).mockResolvedValueOnce({ data: [], next_cursor: 'loop' }).mockResolvedValueOnce({ data: [], next_cursor: 'loop' })
  await expect(read()).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
})
it('propagates real missing/read errors without retry or an invented empty collection', async () => {
  vi.mocked(api.fetchPurchase).mockRejectedValueOnce(new ApiError('NOT_FOUND'))
  await expect(readPurchaseDetail(purchase.id, signal())).rejects.toMatchObject({ code: 'NOT_FOUND' })
  expect(api.fetchPurchaseLinesPage).not.toHaveBeenCalled()
  vi.mocked(api.fetchPurchaseLinesPage).mockRejectedValueOnce(new ApiError('NETWORK_ERROR'))
  await expect(readPurchaseDetail(purchase.id, signal())).rejects.toMatchObject({ code: 'NETWORK_ERROR' })
  expect(api.fetchPurchaseLinesPage).toHaveBeenCalledTimes(1)
  vi.mocked(api.fetchItemPurchasesPage).mockRejectedValueOnce(new ApiError('INVALID_CURSOR'))
  await expect(readItemPurchases(item.id, signal())).rejects.toMatchObject({ code: 'INVALID_CURSOR' })
  vi.mocked(fetchItemUnitsPage).mockRejectedValueOnce(new ApiError('SERVICE_UNAVAILABLE'))
  await expect(readPurchaseAllocation(purchase.id, item.id, signal())).rejects.toMatchObject({ code: 'SERVICE_UNAVAILABLE' })
})
it.each(['detail', 'associated', 'allocation'] as const)('withholds completed %s review after final cancellation', async (kind) => {
  const controller = new AbortController()
  if (kind === 'associated') vi.mocked(fetchItem).mockResolvedValueOnce(item).mockImplementationOnce(async () => { controller.abort(); return item })
  else vi.mocked(api.fetchPurchase).mockResolvedValueOnce(purchase).mockImplementationOnce(async () => { controller.abort(); return purchase })
  await expect(kind === 'detail' ? readPurchaseDetail(purchase.id, controller.signal) : kind === 'associated' ? readItemPurchases(item.id, controller.signal) : readPurchaseAllocation(purchase.id, item.id, controller.signal)).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
})
