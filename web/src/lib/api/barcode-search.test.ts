import { beforeEach, describe, expect, it, vi } from 'vitest'

const fetchMock = vi.fn<typeof fetch>()
const signal = () => new AbortController().signal
const reply = (data: unknown, etag?: string, status = 200) => Response.json(data, { status, headers: etag === undefined ? {} : { ETag: etag } })
const request = () => fetchMock.mock.lastCall?.[0] as Request
const first = { item_id: 'item-1', barcode_id: 'barcode-1', symbology: 'UPC_A', normalized_value: '036000291452' } as const
const second = { ...first, item_id: 'item-2', barcode_id: 'barcode-2' } as const

beforeEach(() => { vi.resetModules(); fetchMock.mockReset(); vi.stubGlobal('fetch', fetchMock) })

describe('exact product barcode discovery', () => {
  it('sends one exact same-origin read and preserves independent shared matches', async () => {
    const api = await import('./barcode-search')
    const result = { matches: [first, second, { ...first, barcode_id: 'barcode-3', symbology: 'OTHER' }], next_cursor: null }
    fetchMock.mockResolvedValueOnce(reply(result))
    expect(await api.fetchBarcodeMatches({ value: first.normalized_value }, signal())).toEqual(result)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const sent = request(), url = new URL(sent.url)
    expect(url.pathname).toBe('/api/v1/search/barcodes')
    expect([...url.searchParams]).toEqual([['value', '036000291452']])
    expect(sent.method).toBe('GET'); expect(sent.cache).toBe('no-store'); expect(sent.credentials).toBe('same-origin'); expect(sent.redirect).toBe('error')
    for (const header of ['Cookie', 'Authorization', 'Origin', 'X-CSRF-Token', 'Idempotency-Key', 'If-Match', 'Content-Type']) expect(sent.headers.has(header)).toBe(false)
    expect(await sent.text()).toBe('')
  })

  it('preserves retained Unicode, controls, exact namespace and decoded cursor bytes without repair', async () => {
    const api = await import('./barcode-search')
    const value = '\0legacy + é\r\n00123', cursor = '😀'.repeat(512)
    const retained = { item_id: 'legacy/item\0😀', barcode_id: 'legacy/\0barcode', symbology: 'EAN_13', normalized_value: value }
    fetchMock.mockResolvedValueOnce(reply({ matches: [retained], next_cursor: 'next' }))
    expect(await api.fetchBarcodeMatches({ value, symbology: 'EAN_13', limit: 1, cursor }, signal()))
      .toEqual({ matches: [retained], next_cursor: 'next' })
    const url = new URL(request().url)
    expect(url.searchParams.getAll('value')).toEqual([value])
    expect(url.searchParams.getAll('symbology')).toEqual(['EAN_13'])
    expect(url.searchParams.getAll('limit')).toEqual(['1'])
    expect(url.searchParams.getAll('cursor')).toEqual([cursor])
    expect(url.search.length - 1).toBeLessThanOrEqual(16_384)
    fetchMock.mockResolvedValueOnce(reply({ matches: [], next_cursor: null }))
    const maximum = '😀'.repeat(512)
    await api.fetchBarcodeMatches({ value: maximum, cursor }, signal())
    expect(new URL(request().url).searchParams.get('value')).toBe(maximum)
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it('rejects malformed query fields before sending without normalizing them', async () => {
    const api = await import('./barcode-search')
    for (const query of [
      {}, { value: '' }, { value: 123 }, { value: '😀'.repeat(513) }, { value: '\uD800' },
      { value: '001', symbology: '' }, { value: '001', symbology: 'ean_13' }, { value: '001', symbology: 'JAN' },
      { value: '001', limit: 0 }, { value: '001', limit: 101 }, { value: '001', limit: 1.5 },
      { value: '001', cursor: '' }, { value: '001', cursor: '😀'.repeat(512) + 'x' }, { value: '001', cursor: '\uD800' },
      { value: '001', unknown: 'private' },
    ]) {
      await expect(api.fetchBarcodeMatches(query as never, signal())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    }
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('withholds incomplete, wrong-scope, duplicate, overfull or falsely ETagged responses', async () => {
    const api = await import('./barcode-search')
    const invalid = [
      {}, { matches: [first] }, { matches: null, next_cursor: null }, { matches: [], next_cursor: '' },
      { matches: [], next_cursor: '😀'.repeat(512) + 'x' }, { matches: [first], next_cursor: 'unexpected' },
      { matches: [first, first], next_cursor: null }, { matches: [{ ...first, item_id: '' }], next_cursor: null },
      { matches: [{ ...first, barcode_id: '😀'.repeat(129) }], next_cursor: null },
      { matches: [{ ...first, barcode_id: '\uD800' }], next_cursor: null }, { matches: [{ ...first, symbology: 'JAN' }], next_cursor: null },
      { matches: [{ ...first, normalized_value: '36000291452' }], next_cursor: null },
      { matches: [{ ...first, raw_value: 'private' }], next_cursor: null }, { matches: [first], next_cursor: null, private: 'secret' },
      { matches: Array.from({ length: 101 }, (_, index) => ({ ...first, barcode_id: `id-${index}` })), next_cursor: null },
    ]
    for (const data of invalid) {
      fetchMock.mockResolvedValueOnce(reply(data))
      await expect(api.fetchBarcodeMatches({ value: first.normalized_value }, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    }
    fetchMock.mockResolvedValueOnce(reply({ matches: [first], next_cursor: null }, '"1"'))
    await expect(api.fetchBarcodeMatches({ value: first.normalized_value }, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    fetchMock.mockResolvedValueOnce(reply({ matches: [{ ...first, symbology: 'OTHER' }], next_cursor: null }))
    await expect(api.fetchBarcodeMatches({ value: first.normalized_value, symbology: 'UPC_A' }, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    fetchMock.mockResolvedValueOnce(reply({ matches: [first, second], next_cursor: null }))
    await expect(api.fetchBarcodeMatches({ value: first.normalized_value, limit: 1 }, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    fetchMock.mockResolvedValueOnce(reply({ matches: [first], next_cursor: 'same' }))
    await expect(api.fetchBarcodeMatches({ value: first.normalized_value, limit: 1, cursor: 'same' }, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    fetchMock.mockResolvedValueOnce(reply({ matches: [first], next_cursor: 'next' }))
    expect(await api.fetchBarcodeMatches({ value: first.normalized_value, limit: 1 }, signal())).toEqual({ matches: [first], next_cursor: 'next' })
  })

  it('returns safe errors without automatic paging, reconnect retry or private storage', async () => {
    const spies = [vi.spyOn(localStorage, 'getItem'), vi.spyOn(localStorage, 'setItem'), vi.spyOn(sessionStorage, 'getItem'),
      vi.spyOn(sessionStorage, 'setItem'), vi.spyOn(document, 'cookie', 'get'), vi.spyOn(document, 'cookie', 'set')]
    const database = vi.fn(), cache = vi.fn()
    vi.stubGlobal('indexedDB', { open: database }); vi.stubGlobal('caches', { open: cache })
    const api = await import('./barcode-search')
    fetchMock.mockResolvedValueOnce(reply({ error: { code: 'INVALID_CURSOR', message: 'secret', details: { file: '/private' } } }, undefined, 400))
    const failure = await api.fetchBarcodeMatches({ value: '001', cursor: 'stale' }, signal()).catch((error: unknown) => error)
    expect(failure).toMatchObject({ code: 'INVALID_CURSOR', status: 400 })
    expect(JSON.stringify(failure)).not.toMatch(/secret|private|details|cause/)
    fetchMock.mockRejectedValueOnce(new TypeError('private transport detail'))
    await expect(api.fetchBarcodeMatches({ value: '001' }, signal())).rejects.toMatchObject({ code: 'NETWORK_ERROR' })
    window.dispatchEvent(new Event('online'))
    expect(fetchMock).toHaveBeenCalledTimes(2)
    for (const spy of [...spies, database, cache]) expect(spy).not.toHaveBeenCalled()
  })

  it('honors caller cancellation and the eight-second deadline', async () => {
    vi.useFakeTimers()
    try {
      const api = await import('./barcode-search')
      fetchMock.mockImplementation((input) => new Promise((_resolve, reject) => {
        (input as Request).signal.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')), { once: true })
      }))
      const early = new AbortController(); early.abort()
      await expect(api.fetchBarcodeMatches({ value: '001' }, early.signal)).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
      const later = new AbortController()
      const aborted = expect(api.fetchBarcodeMatches({ value: '001' }, later.signal)).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
      later.abort(); await aborted
      const timed = expect(api.fetchBarcodeMatches({ value: '001' }, signal())).rejects.toMatchObject({ code: 'REQUEST_TIMEOUT' })
      await vi.advanceTimersByTimeAsync(7999); expect(request().signal.aborted).toBe(false)
      await vi.advanceTimersByTimeAsync(1); await timed
      expect(fetchMock).toHaveBeenCalledTimes(2); expect(vi.getTimerCount()).toBe(0)
    } finally { vi.useRealTimers() }
  })
})
