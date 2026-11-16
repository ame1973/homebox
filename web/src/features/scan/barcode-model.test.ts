import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/client'
import { fetchBarcodeMatches } from '@/lib/api/barcode-search'
import { fetchItemByIdentity, type Item } from '@/lib/api/catalog'
import { readBarcodeChoices } from './barcode-model'

vi.mock('@/lib/api/barcode-search', () => ({ fetchBarcodeMatches: vi.fn() }))
vi.mock('@/lib/api/catalog', () => ({ fetchItemByIdentity: vi.fn() }))

const item = (id: string): Item => ({ id, name: `Item ${id}`, tracking_mode: 'individual', unit_code: 'pcs', category_id: null,
  brand: null, model: '', description: '', notes: '', base_unit: 'pcs', scale_factor: 1, status: 'active', revision: 7 })
const match = (barcode_id: string, item_id: string) => ({ barcode_id, item_id, normalized_value: '\0é\r\n001', symbology: 'EAN_13' as const })

beforeEach(() => { vi.resetAllMocks() })

describe('barcode choices', () => {
  it('reads one bounded exact page and each distinct actual Item once', async () => {
    const rows = [match('barcode/1', 'legacy/item\0😀'), match('barcode/2', 'legacy/item\0😀'), match('barcode/3', 'other')]
    vi.mocked(fetchBarcodeMatches).mockResolvedValueOnce({ matches: rows, next_cursor: 'next' })
    vi.mocked(fetchItemByIdentity).mockImplementation(async (id) => item(id))
    const signal = new AbortController().signal
    expect(await readBarcodeChoices({ value: '\0é\r\n001', symbology: 'EAN_13', cursor: 'current' }, signal)).toEqual({
      matches: rows.map((row) => ({ barcode: row, item: item(row.item_id) })), next_cursor: 'next',
    })
    expect(fetchBarcodeMatches).toHaveBeenCalledOnce()
    expect(vi.mocked(fetchBarcodeMatches).mock.calls[0]?.[0]).toEqual({ value: '\0é\r\n001', symbology: 'EAN_13', cursor: 'current', limit: 5 })
    expect(vi.mocked(fetchItemByIdentity).mock.calls.map(([id]) => id)).toEqual(['legacy/item\0😀', 'other'])
    expect(fetchItemByIdentity).toHaveBeenCalledTimes(2)
  })

  it('keeps an empty page distinct from a failed or changed Item source', async () => {
    vi.mocked(fetchBarcodeMatches).mockResolvedValueOnce({ matches: [], next_cursor: null })
    expect(await readBarcodeChoices({ value: '001' }, new AbortController().signal)).toEqual({ matches: [], next_cursor: null })
    expect(fetchItemByIdentity).not.toHaveBeenCalled()
    vi.mocked(fetchBarcodeMatches).mockResolvedValueOnce({ matches: [match('b', 'gone')], next_cursor: null })
    vi.mocked(fetchItemByIdentity).mockRejectedValueOnce(new ApiError('NOT_FOUND', { status: 404 }))
    await expect(readBarcodeChoices({ value: '\0é\r\n001' }, new AbortController().signal)).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  })

  it('rejects a mismatched hydrated Item instead of joining it to a barcode', async () => {
    vi.mocked(fetchBarcodeMatches).mockResolvedValueOnce({ matches: [match('b', 'expected')], next_cursor: null })
    vi.mocked(fetchItemByIdentity).mockResolvedValueOnce(item('different'))
    await expect(readBarcodeChoices({ value: '\0é\r\n001' }, new AbortController().signal)).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  })

  it('withholds a whole page on another error and cancels outstanding Item reads', async () => {
    vi.mocked(fetchBarcodeMatches).mockResolvedValueOnce({ matches: [match('a', 'bad'), match('b', 'slow')], next_cursor: null })
    let slowSignal: AbortSignal | undefined
    vi.mocked(fetchItemByIdentity).mockImplementation((id, signal) => {
      if (id === 'bad') return Promise.reject(new ApiError('RATE_LIMITED', { status: 429 }))
      slowSignal = signal
      return new Promise((_resolve, reject) => signal.addEventListener('abort', () => reject(new ApiError('REQUEST_ABORTED')), { once: true }))
    })
    await expect(readBarcodeChoices({ value: '\0é\r\n001' }, new AbortController().signal)).rejects.toMatchObject({ code: 'RATE_LIMITED', status: 429 })
    expect(slowSignal?.aborted).toBe(true)
  })

  it('honors cancellation before and during the separate Item reads', async () => {
    const early = new AbortController(); early.abort()
    await expect(readBarcodeChoices({ value: '001' }, early.signal)).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
    expect(fetchBarcodeMatches).not.toHaveBeenCalled()
    vi.mocked(fetchBarcodeMatches).mockResolvedValueOnce({ matches: [match('a', 'owner')], next_cursor: null })
    const controller = new AbortController()
    vi.mocked(fetchItemByIdentity).mockImplementation((_id, signal) => new Promise((_resolve, reject) => {
      signal.addEventListener('abort', () => reject(new ApiError('REQUEST_ABORTED')), { once: true })
      controller.abort()
    }))
    await expect(readBarcodeChoices({ value: '\0é\r\n001' }, controller.signal)).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
  })
})
