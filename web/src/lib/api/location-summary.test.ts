import { beforeEach, describe, expect, it, vi } from 'vitest'

const fetchMock = vi.fn<typeof fetch>()
const signal = () => new AbortController().signal
const summary = { location_id: 'root', direct_units: 1, subtree_units: 3, distinct_item_count: 3,
  direct_quantities: [{ base_unit: 'g', quantity_base: 400 }],
  subtree_quantities: [{ base_unit: 'g', quantity_base: 1500 }, { base_unit: 'ml', quantity_base: 1200 }] }

function reply(value: unknown, etag: string | null = '"7"', status = 200) {
  return Response.json(value, { status, headers: etag === null ? {} : { ETag: etag } })
}

beforeEach(() => { vi.resetModules(); fetchMock.mockReset(); vi.stubGlobal('fetch', fetchMock) })

describe('location summary live read', () => {
  it('performs one exact protected read and retains its independent aggregate ETag', async () => {
    fetchMock.mockResolvedValue(reply(summary))
    const { fetchLocationSummary } = await import('./locations')
    expect(await fetchLocationSummary('root', signal())).toEqual({ value: summary, revision: 7 })
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const request = fetchMock.mock.calls[0]![0] as Request
    expect(request.url).toBe('http://localhost/api/v1/locations/root/summary')
    expect(request.method).toBe('GET'); expect(request.cache).toBe('no-store')
    expect(request.credentials).toBe('same-origin'); expect(request.redirect).toBe('error')
    expect(await request.text()).toBe(''); expect(request.headers.has('If-Match')).toBe(false)
    expect(request.headers.has('Idempotency-Key')).toBe(false); expect(request.headers.has('X-CSRF-Token')).toBe(false)
  })

  it('accepts actual empty arrays and the maximum exact counts and ETag', async () => {
    const { fetchLocationSummary } = await import('./locations')
    fetchMock.mockResolvedValueOnce(reply({ location_id: 'root', direct_units: 0, subtree_units: 0, distinct_item_count: 0,
      direct_quantities: [], subtree_quantities: [] }, '"1"'))
    expect(await fetchLocationSummary('root', signal())).toMatchObject({ revision: 1, value: { direct_quantities: [], subtree_quantities: [] } })
    const maximum = Number.MAX_SAFE_INTEGER
    const boundary = { location_id: 'root', direct_units: maximum, subtree_units: maximum, distinct_item_count: maximum,
      direct_quantities: [{ base_unit: 'g', quantity_base: maximum }], subtree_quantities: [{ base_unit: 'g', quantity_base: maximum }] }
    fetchMock.mockResolvedValueOnce(reply(boundary, `"${maximum}"`))
    expect(await fetchLocationSummary('root', signal())).toEqual({ value: boundary, revision: maximum })
  })

  it.each(['', 'tree', '.', '..', 'a/b', 'a\\b', '\0', '\ud800', 'x'.repeat(129)])('rejects an unsafe Location path identity %j before fetching', async (id) => {
    const { fetchLocationSummary } = await import('./locations')
    await expect(fetchLocationSummary(id, signal())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it.each([null, '', '1', 'W/"7"', '"0"', '"01"', '"-1"', '"7", "8"', '"9007199254740992"'])('rejects a missing, weak or noncanonical ETag %j', async (etag) => {
    fetchMock.mockResolvedValue(reply(summary, etag))
    const { fetchLocationSummary } = await import('./locations')
    await expect(fetchLocationSummary('root', signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE', status: 200 })
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it.each([
    null, {}, { ...summary, location_id: 'other' }, { ...summary, location_id: '\ud800' },
    { ...summary, direct_units: -1 }, { ...summary, direct_units: 1.5 }, { ...summary, direct_units: Number.MAX_SAFE_INTEGER + 1 },
    { ...summary, direct_units: 4 }, { ...summary, subtree_units: 0, direct_units: 0, distinct_item_count: 0 },
    { ...summary, distinct_item_count: 0 }, { ...summary, distinct_item_count: 3000 },
    { ...summary, direct_quantities: null }, { ...summary, direct_quantities: undefined },
    { ...summary, subtree_quantities: [{ base_unit: 'g\n', quantity_base: 1 }] },
    { ...summary, subtree_quantities: [{ base_unit: 'g', quantity_base: 0 }] },
    { ...summary, subtree_quantities: [{ base_unit: 'g', quantity_base: Number.MAX_SAFE_INTEGER + 1 }] },
    { ...summary, subtree_quantities: [{ base_unit: 'g', quantity_base: 1500 }, { base_unit: 'g', quantity_base: 1 }] },
    { ...summary, subtree_quantities: [{ base_unit: 'ml', quantity_base: 1200 }, { base_unit: 'g', quantity_base: 1500 }] },
    { ...summary, direct_quantities: [{ base_unit: 'g', quantity_base: 1501 }] },
    { ...summary, direct_quantities: [{ base_unit: 'pcs', quantity_base: 1 }] },
    { ...summary, subtree_quantities: [{ base_unit: 'g', quantity_base: 1500, extra: 'private' }] },
    { ...summary, extra: 'private' },
    JSON.parse('{"location_id":"root","direct_units":0,"subtree_units":0,"distinct_item_count":0,"direct_quantities":[],"subtree_quantities":[],"__proto__":{"private":true}}'),
    { ...summary, subtree_quantities: [JSON.parse('{"base_unit":"g","quantity_base":1500,"__proto__":{"private":true}}')] },
  ])('rejects malformed or inconsistent summary %#', async (value) => {
    fetchMock.mockResolvedValue(reply(value))
    const { fetchLocationSummary } = await import('./locations')
    await expect(fetchLocationSummary('root', signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE', status: 200 })
  })

  it('never retries after abort and retains no raw server error details', async () => {
    const controller = new AbortController()
    fetchMock.mockImplementation((input) => new Promise((_, reject) => {
      const requestSignal = (input as Request).signal
      requestSignal.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')), { once: true })
    }))
    const { fetchLocationSummary } = await import('./locations')
    const pending = fetchLocationSummary('root', controller.signal)
    controller.abort()
    await expect(pending).rejects.toMatchObject({ code: 'REQUEST_ABORTED' })
    expect(fetchMock).toHaveBeenCalledTimes(1)
    fetchMock.mockResolvedValueOnce(reply({ error: { code: 'SERVICE_UNAVAILABLE', request_id: 'safe-id', message: 'private details' } }, null, 503))
    const error = await fetchLocationSummary('root', signal()).catch((cause: unknown) => cause)
    expect(error).toMatchObject({ code: 'SERVICE_UNAVAILABLE', status: 503, requestId: 'safe-id' })
    expect(JSON.stringify(error)).not.toContain('private details')
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })
})
