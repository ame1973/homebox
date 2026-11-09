import { beforeEach, describe, expect, it, vi } from 'vitest'
import { version } from '../../../package.json'
import type { Item } from './catalog'
import type { Unit } from './holdings'
import type { Asset, AssetCostSummary, Cost, Disposal, Usage } from './assets'

type AssetAPI = typeof import('./assets')
const fetchMock = vi.fn<typeof fetch>()
const signal = () => new AbortController().signal
const csrf = 'A'.repeat(43)
const item: Item = { id: 'item-1', revision: 11, name: 'Original owner', tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1,
  brand: null, model: '', category_id: null, description: '', notes: '', status: 'active' }
const unit: Unit = { id: 'unit-1', item_id: item.id, revision: 13, asset_code: 'Asset code', location_id: 'place', serial_number: null, notes: 'Physical source', status: 'stored' }
const asset: Asset = { item_unit_id: unit.id, revision: 7, cost_basis: 'manual', currency: 'JPY', manual_acquisition_minor: 100,
  in_service_at: '2024-11-07T12:00:00.123456789+08:00', retired_at: null, estimated_residual_minor: 20, usage_unit: 'times' }
const profileInput = { item_revision: item.revision, asset_revision: asset.revision, cost_basis: 'manual', currency: 'JPY', manual_acquisition_minor: 100,
  in_service_at: '2024-11-07T04:00:00.123456789Z', retired_at: null, estimated_residual_minor: 30, usage_unit: 'times' } as const
const useInput = { delta: 3, usage_unit: 'times', event_type: 'use', occurred_at: '2026-11-07T12:00:00.123456789+08:00' } as const
const costInput = { amount_minor: 5, currency: 'JPY', kind: 'repair', direction: 'expense', occurred_at: useInput.occurred_at } as const
const retireInput = { unit_revision: unit.revision, retired_at: useInput.occurred_at } as const
const disposalInput = { unit_revision: unit.revision, kind: 'sold', disposed_at: useInput.occurred_at, currency: 'JPY', realized_proceeds_minor: 200 } as const
const usage: Usage = { id: 'use-1', item_unit_id: unit.id, revision: 1, ...useInput, occurred_at: '2026-11-07T04:00:00.123456789Z', note: '', correction_of: null }
const cost: Cost = { id: 'cost-1', item_unit_id: unit.id, revision: 1, ...costInput, occurred_at: usage.occurred_at, note: '', correction_of: null }
const disposal: Disposal = { item_unit_id: unit.id, revision: 1, kind: 'sold', disposed_at: usage.occurred_at, realized_proceeds_minor: 200, currency: 'JPY', note: '' }
const summary: AssetCostSummary = { unit_id: unit.id, currency: 'JPY', cost_basis: 'manual', acquisition_minor: 100, extra_minor: 3,
  realized_proceeds_minor: 200, estimated_residual_minor: 20, net_cost_minor: -97, projected_net_cost_minor: 83,
  elapsed_days: 731, total_usage: 2, usage_unit: 'times', per_day_minor: -97 / 731, per_use_minor: -48.5,
  projected_per_day_minor: 83 / 731, projected_per_use_minor: 41.5, as_of: '2026-11-08T04:00:00.123456789Z', timezone: 'UTC', day_policy: 'inclusive_civil_days', source_revision: 0 }
const context = () => ({ csrfToken: csrf, idempotencyKey: 'original-asset-key', signal: signal() })
const assetContext = () => ({ ...context(), asset: { ...asset } })
const profileContext = () => ({ ...assetContext(), unit: { ...unit }, item: { ...item } })
const lifecycleContext = () => ({ ...assetContext(), unit: { ...unit } })
const disposeContext = () => ({ ...lifecycleContext(), disposal: null })
const reply = (value: unknown, etag?: string, status = 200) => Response.json(value, { status, headers: etag === undefined ? {} : { ETag: etag } })
function captured(index = fetchMock.mock.calls.length - 1): Request {
  const request = fetchMock.mock.calls[index]?.[0]
  if (!(request instanceof Request)) throw new Error('Expected a captured financial request')
  return request
}
const cases: { name: string; method: string; path: string; body?: unknown; result: unknown; etag?: string; proof?: number; invoke: (api: AssetAPI) => Promise<unknown> }[] = [
  { name: 'global Assets', method: 'GET', path: '/assets', result: { data: [asset], next_cursor: null }, invoke: (api) => api.fetchAssetsPage({}, signal()) },
  { name: 'Asset detail', method: 'GET', path: '/units/unit-1/asset', result: asset, etag: '"7"', invoke: (api) => api.fetchAsset(unit.id, signal()) },
  { name: 'owned usage', method: 'GET', path: '/units/unit-1/usage', result: { data: [usage], next_cursor: null }, invoke: (api) => api.fetchAssetUsagePage(unit.id, {}, signal()) },
  { name: 'owned costs', method: 'GET', path: '/units/unit-1/costs', result: { data: [cost], next_cursor: null }, invoke: (api) => api.fetchAssetCostsPage(unit.id, {}, signal()) },
  { name: 'recorded disposal', method: 'GET', path: '/units/unit-1/disposal', result: disposal, etag: '"1"', invoke: (api) => api.fetchAssetDisposal(unit.id, signal()) },
  { name: 'current summary', method: 'GET', path: '/units/unit-1/cost-summary', result: summary, invoke: (api) => api.fetchAssetCostSummary(unit.id, {}, signal()) },
  { name: 'complete profile', method: 'PUT', path: '/units/unit-1/asset', body: profileInput, result: { ...asset, revision: 8, estimated_residual_minor: 30 }, etag: '"8"', proof: 13,
    invoke: (api) => api.putAssetProfile(unit.id, profileInput, profileContext()) },
  { name: 'immutable use', method: 'POST', path: '/units/unit-1/usage', body: useInput, result: usage, etag: '"1"', proof: 7, invoke: (api) => api.recordAssetUsage(unit.id, useInput, assetContext()) },
  { name: 'immutable cost', method: 'POST', path: '/units/unit-1/costs', body: costInput, result: cost, etag: '"1"', proof: 7, invoke: (api) => api.recordAssetCost(unit.id, costInput, assetContext()) },
  { name: 'dual-proof retirement', method: 'POST', path: '/units/unit-1/retire', body: retireInput, result: { ...unit, revision: 14, status: 'retired' }, etag: '"14"', proof: 7,
    invoke: (api) => api.retireAsset(unit.id, retireInput, lifecycleContext()) },
  { name: 'dual-proof disposal', method: 'POST', path: '/units/unit-1/disposal', body: disposalInput, result: disposal, etag: '"1"', proof: 7,
    invoke: (api) => api.disposeAsset(unit.id, disposalInput, disposeContext()) },
]

beforeEach(() => { vi.resetModules(); fetchMock.mockReset(); vi.stubGlobal('fetch', fetchMock) })

describe('eleven stateless financial adapters', () => {
  it.each(cases)('sends exactly one $name using its complete original proof scope', async (test) => {
    fetchMock.mockResolvedValue(reply(test.result, test.etag))
    const api = await import('./assets')
    expect(await test.invoke(api)).toEqual(test.result)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const request = captured()
    expect(request.url).toBe(`http://localhost/api/v1${test.path}`); expect(request.method).toBe(test.method)
    expect(request.credentials).toBe('same-origin'); expect(request.cache).toBe('no-store'); expect(request.redirect).toBe('error')
    expect(request.headers.has('Cookie')).toBe(false); expect(request.headers.has('Authorization')).toBe(false)
    expect(request.headers.get('Origin')).toBe(test.method === 'GET' ? null : 'http://localhost')
    expect(request.headers.get('X-CSRF-Token')).toBe(test.method === 'GET' ? null : csrf)
    expect(request.headers.get('Idempotency-Key')).toBe(test.method === 'GET' ? null : 'original-asset-key')
    expect(request.headers.get('X-HomeBox-Client-Version')).toBe(test.method === 'GET' ? null : version)
    expect(request.headers.get('If-Match')).toBe(test.proof === undefined ? null : `"${test.proof}"`)
    expect(request.headers.has('X-Item-Revision')).toBe(false); expect(request.headers.has('X-Unit-Revision')).toBe(false)
    if (test.body === undefined) { expect(await request.text()).toBe(''); expect(request.headers.has('Content-Type')).toBe(false) }
    else expect(await request.json()).toEqual(test.body)
  })
  it.each(cases)('does not retry a failed $name on reconnect', async (test) => {
    fetchMock.mockRejectedValue(new TypeError('private transport'))
    await expect(test.invoke(await import('./assets'))).rejects.toMatchObject({ code: 'NETWORK_ERROR' })
    window.dispatchEvent(new Event('online'))
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })
  it.each(cases)('bounds the $name deadline and removes its timer', async (test) => {
    vi.useFakeTimers()
    try {
      fetchMock.mockImplementation((request) => new Promise((_resolve, reject) => {
        (request as Request).signal.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')), { once: true })
      }))
      const api = await import('./assets')
      const pending = expect(test.invoke(api)).rejects.toMatchObject({ code: 'REQUEST_TIMEOUT' })
      const timeout = test.method === 'GET' ? 8_000 : 30_000
      await vi.advanceTimersByTimeAsync(timeout - 1); expect(captured().signal.aborted).toBe(false)
      await vi.advanceTimersByTimeAsync(1); await pending
      expect(fetchMock).toHaveBeenCalledTimes(1); expect(vi.getTimerCount()).toBe(0)
    } finally { vi.useRealTimers() }
  })
  it.each(cases)('rejects an incomplete or extended $name without returning repaired state', async (test) => {
    fetchMock.mockResolvedValue(reply({ ...test.result as object, private_extra: true }, test.etag))
    await expect(test.invoke(await import('./assets'))).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })
  it.each(cases)('rejects a missing, weak or unexpected $name ETag', async (test) => {
    fetchMock.mockImplementation(async () => reply(test.result, test.etag === undefined ? '"1"' : `W/${test.etag}`))
    await expect(test.invoke(await import('./assets'))).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('keeps complete nullable opt-in, canonical new dates and equivalent retained spelling distinct', async () => {
    const api = await import('./assets')
    const create = { ...profileInput, asset_revision: null, estimated_residual_minor: null }
    const created = { ...asset, revision: 1, in_service_at: '2024-11-07T04:00:00.123456789Z', estimated_residual_minor: null }
    fetchMock.mockResolvedValueOnce(reply(created, '"1"'))
    expect(await api.putAssetProfile(unit.id, create, { ...profileContext(), asset: null })).toEqual(created)
    const original = { ...profileInput, estimated_residual_minor: 20 }
    fetchMock.mockResolvedValueOnce(reply(asset, '"7"'))
    expect(await api.putAssetProfile(unit.id, original, profileContext())).toEqual(asset)
    fetchMock.mockResolvedValueOnce(reply({ ...asset, revision: 8, in_service_at: '2024-11-07T04:00:00.123456788Z' }, '"8"'))
    expect(await api.putAssetProfile(unit.id, { ...original, in_service_at: '2024-11-07T04:00:00.123456788Z' }, profileContext())).toMatchObject({ revision: 8, in_service_at: '2024-11-07T04:00:00.123456788Z' })
    const purchase = { ...create, cost_basis: 'purchase', manual_acquisition_minor: null, in_service_at: null, retired_at: null } as const
    fetchMock.mockResolvedValueOnce(reply({ ...created, cost_basis: 'purchase', manual_acquisition_minor: null, in_service_at: null }, '"1"'))
    await expect(api.putAssetProfile(unit.id, purchase, { ...profileContext(), asset: null })).resolves.toMatchObject({ cost_basis: 'purchase', manual_acquisition_minor: null, in_service_at: null })
    expect(fetchMock).toHaveBeenCalledTimes(4)
  })

  it('preserves broad retained profiles, negative old use, absent correction provenance and non-sale proceeds', async () => {
    const api = await import('./assets')
    const legacyAsset = { ...asset, currency: 'ZZZ', manual_acquisition_minor: null }
    fetchMock.mockResolvedValueOnce(reply(legacyAsset, '"7"'))
    expect(await api.fetchAsset(unit.id, signal())).toEqual(legacyAsset)
    const oldUse = { ...usage, delta: -2, event_type: 'use', correction_of: null, occurred_at: useInput.occurred_at }
    fetchMock.mockResolvedValueOnce(reply({ data: [oldUse], next_cursor: null }))
    expect((await api.fetchAssetUsagePage(unit.id, {}, signal())).data).toEqual([oldUse])
    const oldCost = { ...cost, kind: 'adjustment', currency: 'ZZZ', correction_of: null }
    fetchMock.mockResolvedValueOnce(reply({ data: [oldCost], next_cursor: null }))
    expect((await api.fetchAssetCostsPage(unit.id, {}, signal())).data).toEqual([oldCost])
    const oldDisposal = { ...disposal, kind: 'donated', realized_proceeds_minor: 5, currency: 'ZZZ', disposed_at: useInput.occurred_at }
    fetchMock.mockResolvedValueOnce(reply(oldDisposal, '"1"'))
    expect(await api.fetchAssetDisposal(unit.id, signal())).toEqual(oldDisposal)
  })

  it('requires whole original contexts and matching owners/body proofs without hidden reads', async () => {
    const api = await import('./assets')
    const invalid: (() => Promise<unknown>)[] = [
      () => api.putAssetProfile(unit.id, profileInput, { ...profileContext(), expectedRevision: 13 } as never),
      () => api.putAssetProfile(unit.id, profileInput, { ...profileContext(), asset: null }),
      () => api.putAssetProfile(unit.id, { ...profileInput, item_revision: 12 }, profileContext()),
      () => api.putAssetProfile(unit.id, { ...profileInput, asset_revision: 8 }, profileContext()),
      () => api.putAssetProfile(unit.id, profileInput, { ...profileContext(), unit: { ...unit, item_id: 'wrong' } }),
      () => api.putAssetProfile(unit.id, profileInput, { ...profileContext(), unit: { ...unit, id: 'wrong' } }),
      () => api.putAssetProfile(unit.id, profileInput, { ...profileContext(), asset: { ...asset, item_unit_id: 'wrong' } }),
      () => api.putAssetProfile(unit.id, profileInput, { ...profileContext(), item: { ...item, tracking_mode: 'quantity' } }),
      () => api.recordAssetUsage(unit.id, useInput, { ...context(), expectedRevision: 7 } as never),
      () => api.recordAssetCost(unit.id, costInput, { ...assetContext(), expectedRevision: 7 } as never),
      () => api.recordAssetUsage(unit.id, useInput, { ...assetContext(), asset: { ...asset, item_unit_id: 'wrong' } }),
      () => api.recordAssetUsage(unit.id, { ...useInput, usage_unit: 'minutes' }, assetContext()),
      () => api.recordAssetCost(unit.id, { ...costInput, currency: 'USD' }, assetContext()),
      () => api.retireAsset(unit.id, { ...retireInput, unit_revision: 7 }, lifecycleContext()),
      () => api.retireAsset(unit.id, retireInput, { ...lifecycleContext(), unit: { ...unit, id: 'wrong' } }),
      () => api.disposeAsset(unit.id, disposalInput, { ...lifecycleContext() } as never),
      () => api.disposeAsset(unit.id, disposalInput, { ...disposeContext(), disposal: { ...disposal, item_unit_id: 'wrong' } }),
      () => api.disposeAsset(unit.id, { ...disposalInput, currency: 'USD' }, disposeContext()),
    ]
    for (const invoke of invalid) await expect(invoke()).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('snapshots profile body, all original scopes, key and proof before an in-flight reply', async () => {
    const api = await import('./assets')
    let finish: ((value: Response) => void) | undefined
    fetchMock.mockImplementation(() => new Promise((resolve) => { finish = resolve }))
    const original = profileContext(), body = { ...profileInput }
    const pending = api.putAssetProfile(unit.id, body, original)
    original.asset.revision = 99; original.unit.revision = 99; original.item.revision = 99
    original.idempotencyKey = 'changed-key'; Object.assign(body, { estimated_residual_minor: 999, item_revision: 99, asset_revision: 99 })
    finish?.(reply({ ...asset, revision: 8, estimated_residual_minor: 30 }, '"8"'))
    expect(await pending).toMatchObject({ revision: 8, estimated_residual_minor: 30 })
    expect(captured().headers.get('Idempotency-Key')).toBe('original-asset-key'); expect(captured().headers.get('If-Match')).toBe('"13"')
    expect(await captured().json()).toEqual(profileInput)
  })

  it('preserves exact correction/adjustment references, Unicode notes and explicit zero non-sale decisions', async () => {
    const api = await import('./assets')
    const note = '\0  Exact é\n😀  '
    const correction = { ...useInput, delta: -1, event_type: 'correction', correction_of: 'validation', note } as const
    fetchMock.mockResolvedValueOnce(reply({ ...usage, ...correction, occurred_at: usage.occurred_at }, '"1"'))
    expect(await api.recordAssetUsage(unit.id, correction, assetContext())).toMatchObject({ delta: -1, correction_of: 'validation', note })
    const adjustment = { ...costInput, kind: 'adjustment', direction: 'credit', correction_of: 'tree', note } as const
    fetchMock.mockResolvedValueOnce(reply({ ...cost, ...adjustment, occurred_at: usage.occurred_at }, '"1"'))
    expect(await api.recordAssetCost(unit.id, adjustment, assetContext())).toMatchObject({ direction: 'credit', correction_of: 'tree', note })
    for (const kind of ['sold', 'donated', 'recycled', 'discarded', 'other'] as const) {
      fetchMock.mockResolvedValueOnce(reply({ ...disposal, kind, realized_proceeds_minor: 0 }, '"1"'))
      expect(await api.disposeAsset(unit.id, { ...disposalInput, kind, realized_proceeds_minor: 0 }, disposeContext())).toMatchObject({ kind, realized_proceeds_minor: 0 })
    }
    const retained = { ...disposal, revision: 19, disposed_at: disposalInput.disposed_at }
    fetchMock.mockResolvedValueOnce(reply(retained, '"19"'))
    expect(await api.disposeAsset(unit.id, disposalInput, { ...disposeContext(), disposal: retained })).toEqual(retained)
    const retired = { ...unit, revision: 21, status: 'retired' } as const
    fetchMock.mockResolvedValueOnce(reply(retired, '"21"'))
    expect(await api.retireAsset(unit.id, { ...retireInput, unit_revision: 21 }, { ...lifecycleContext(), unit: retired })).toEqual(retired)
  })

  it('keeps signed filters, native multiline text, imported paths and lossless summary offsets exact', async () => {
    const api = await import('./assets')
    fetchMock.mockImplementation(async () => reply({ data: [], next_cursor: null }))
    const query = { from: '0001-01-01', to: '9999-12-31', currency: 'ZZZ', category_id: '分類 +?#', location_id: '位置 +?#', scope: 'direct', status: 'archived', merchant: '  é\n😀  ', limit: 100, cursor: 'opaque?&+#' } as const
    await api.fetchAssetsPage(query, signal())
    for (const [key, value] of Object.entries(query)) expect(new URL(captured().url).searchParams.getAll(key)).toEqual([String(value)])
    for (const id of ['new', 'tree', '單位 +?# é']) {
      await api.fetchAssetUsagePage(id, { limit: 1, cursor: query.cursor }, signal())
      expect(decodeURIComponent(new URL(captured().url).pathname)).toBe(`/api/v1/units/${id}/usage`)
      await api.fetchAssetCostsPage(id, {}, signal())
      expect(decodeURIComponent(new URL(captured().url).pathname)).toBe(`/api/v1/units/${id}/costs`)
    }
    const cursor = '😀'.repeat(512)
    for (const fetchPage of [(q: { cursor: string }) => api.fetchAssetsPage(q, signal()), (q: { cursor: string }) => api.fetchAssetUsagePage(unit.id, q, signal()), (q: { cursor: string }) => api.fetchAssetCostsPage(unit.id, q, signal())]) {
      await fetchPage({ cursor }); expect(new URL(captured().url).search.slice(1)).toHaveLength(6151)
      await expect(fetchPage({ cursor: cursor + 'x' })).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    }
    fetchMock.mockResolvedValueOnce(reply(summary))
    await api.fetchAssetCostSummary(unit.id, { as_of: '2026-11-08T12:00:00.123456789+08:00' }, signal())
    expect(new URL(captured().url).searchParams.get('as_of')).toBe('2026-11-08T12:00:00.123456789+08:00')
  })
})

it.each(cases.filter((test) => test.method !== 'GET'))('rejects an unrelated aggregate revision with a matching strong $name ETag', async (test) => {
  fetchMock.mockResolvedValue(reply({ ...test.result as object, revision: test.proof }, `"${test.proof}"`))
  await expect(test.invoke(await import('./assets'))).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  expect(fetchMock).toHaveBeenCalledTimes(1)
})

it.each(['asset', 'disposal'] as const)('rejects a foreign %s identity even with its actual strong ETag', async (kind) => {
  const api = await import('./assets')
  fetchMock.mockResolvedValue(reply({ ...(kind === 'asset' ? asset : disposal), item_unit_id: 'wrong' }, kind === 'asset' ? '"7"' : '"1"'))
  await expect(kind === 'asset' ? api.fetchAsset(unit.id, signal()) : api.fetchAssetDisposal(unit.id, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
})

it.each([
  { ...summary, net_cost_minor: 0 }, { ...summary, projected_net_cost_minor: 0 }, { ...summary, per_use_minor: 0 }, { ...summary, projected_per_use_minor: 0 },
  { ...summary, elapsed_days: null }, { ...summary, total_usage: 0 }, { ...summary, estimated_residual_minor: null },
  { ...summary, projected_net_cost_minor: null }, { ...summary, net_cost_minor: Number.MAX_SAFE_INTEGER + 1 },
  { ...summary, acquisition_minor: 0.5 }, { ...summary, source_revision: -1 }, { ...summary, day_policy: 'elapsed_24_hours' },
  { ...summary, unit_id: 'wrong' }, { ...summary, as_of: '2026-11-08T04:00:00.123456788Z' },
])('withholds a malformed or inconsistent complete cost summary %#', async (value) => {
  fetchMock.mockResolvedValue(reply(value))
  const api = await import('./assets')
  await expect(api.fetchAssetCostSummary(unit.id, { as_of: summary.as_of }, signal())).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  expect(fetchMock).toHaveBeenCalledTimes(1)
})

it('validates safe signed cancellation through BigInt and preserves every null divisor/projection', async () => {
  const api = await import('./assets')
  const empty = { ...summary, estimated_residual_minor: null, projected_net_cost_minor: null, elapsed_days: null, total_usage: 0,
    per_day_minor: null, per_use_minor: null, projected_per_day_minor: null, projected_per_use_minor: null }
  fetchMock.mockResolvedValueOnce(reply(empty))
  expect(await api.fetchAssetCostSummary(unit.id, {}, signal())).toEqual(empty)
  const max = Number.MAX_SAFE_INTEGER
  const large = { ...empty, acquisition_minor: max, extra_minor: 2, realized_proceeds_minor: 2, net_cost_minor: max, estimated_residual_minor: 2, projected_net_cost_minor: max }
  fetchMock.mockResolvedValueOnce(reply(large))
  expect(await api.fetchAssetCostSummary(unit.id, {}, signal())).toEqual(large)
})

it('rejects malformed nullable decisions, numbers, Unicode, instants and undocumented queries before sending', async () => {
  const api = await import('./assets')
  for (const field of Object.keys(profileInput)) {
    const body: Record<string, unknown> = { ...profileInput }; delete body[field]
    await expect(api.putAssetProfile(unit.id, body as never, profileContext())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
  }
  for (const bad of [NaN, Infinity, -1, 0.5, '1', true, Number.MAX_SAFE_INTEGER + 1]) {
    await expect(api.recordAssetCost(unit.id, { ...costInput, amount_minor: bad } as never, assetContext())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    await expect(api.disposeAsset(unit.id, { ...disposalInput, realized_proceeds_minor: bad } as never, disposeContext())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
  }
  for (const body of [{ ...useInput, delta: 0 }, { ...useInput, event_type: 'correction' }, { ...useInput, correction_of: 'target' }, { ...useInput, note: '\ud800' }, { ...useInput, note: '界'.repeat(10001) }, { ...useInput, unit_revision: 13 }]) {
    await expect(api.recordAssetUsage(unit.id, body as never, assetContext())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
  }
  for (const body of [{ ...costInput, kind: 'adjustment' }, { ...costInput, correction_of: 'target' }, { ...costInput, currency: 'JPY\n' }]) {
    await expect(api.recordAssetCost(unit.id, body as never, assetContext())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
  }
  for (const instant of ['2026-02-30T00:00:00Z', '2026-11-07T00:00:60Z', '2026-11-07T00:00:00.1234567890Z', '0000-01-01T00:00:00Z', '0001-01-01T00:00:00+00:01', '9999-12-31T23:59:59-00:01']) {
    await expect(api.recordAssetUsage(unit.id, { ...useInput, occurred_at: instant }, assetContext())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
    await expect(api.fetchAssetCostSummary(unit.id, { as_of: instant }, signal())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
  }
  for (const query of [{ q: 'x' }, { item_id: 'item' }, { scope: 'all' }, { status: 'disposed' }, { currency: 'JPY\n' }, { merchant: '\ud800' }, { cursor: '\udfff' }, { limit: 101 }, { from: '2026-11-08', to: '2026-11-07' }]) {
    await expect(api.fetchAssetsPage(query as never, signal())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
  }
  await expect(api.fetchAssetCostsPage(unit.id, { currency: 'JPY' } as never, signal())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
  await expect(api.fetchAssetCostSummary(unit.id, { limit: 1 } as never, signal())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
  for (const id of ['', '.', '..', 'bad/path', 'bad\\path', 'bad\0path', '\ud800', '界'.repeat(129)]) await expect(api.fetchAsset(id, signal())).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
  expect(fetchMock).not.toHaveBeenCalled()
})

it.each(['assets', 'usage', 'costs'] as const)('rejects partial, duplicate, foreign and inconsistent %s pages', async (kind) => {
  const api = await import('./assets')
  const row = kind === 'assets' ? asset : kind === 'usage' ? usage : cost
  const fetchPage = () => kind === 'assets' ? api.fetchAssetsPage({ limit: 1, cursor: 'original', currency: 'JPY' }, signal()) :
    kind === 'usage' ? api.fetchAssetUsagePage(unit.id, { limit: 1, cursor: 'original' }, signal()) : api.fetchAssetCostsPage(unit.id, { limit: 1, cursor: 'original' }, signal())
  for (const value of [ { data: [row] }, { data: [row, row], next_cursor: null }, { data: [], next_cursor: 'next' }, { data: [row], next_cursor: 'original' },
    { data: [{ ...row, ...(kind === 'assets' ? { currency: 'USD' } : { item_unit_id: 'wrong' }) }], next_cursor: null } ]) {
    fetchMock.mockResolvedValueOnce(reply(value))
    await expect(fetchPage()).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  }
  fetchMock.mockResolvedValueOnce(reply({ data: [row], next_cursor: 'next' }))
  await expect(fetchPage()).resolves.toEqual({ data: [row], next_cursor: 'next' })
})
