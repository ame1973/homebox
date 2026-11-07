import { expect, it } from 'vitest'
import { applicationReturnPath } from './settings-navigation'
import { assetListRoute, assetModes, assetNavigationState, assetQuery, assetReturnPath, assetRoute, assetSummaryQuery, assetSummaryRoute } from './asset-navigation'

it('builds every actual Unit asset route without reserving imported new/tree identities', () => {
  for (const id of ['new', 'NEW', 'tree', '個體 😀']) for (const mode of assetModes) {
    const path = `/assets/${encodeURIComponent(id)}${mode === 'detail' ? '' : `/${mode}`}`
    expect(assetRoute(id, mode)).toBe(path); expect(assetReturnPath(path)).toBe(path); expect(applicationReturnPath(path)).toBe(path)
  }
  expect(() => assetRoute('unit', 'delete' as never)).toThrow()
})
it('round-trips all actual global filters and explicit signed continuation without adding text search or lifecycle filters', () => {
  const query = { from: '2026-01-01', to: '2026-11-07', currency: 'JPY', merchant: '  Exact\nmerchant é 😀 ', category_id: 'category', location_id: 'location', scope: 'direct' as const, status: 'active' as const, limit: 1, cursor: 'opaque' }
  const path = assetListRoute(query)
  expect(assetQuery(path.slice(path.indexOf('?')))).toEqual(query)
  expect(assetReturnPath(path)).toBe(path); expect(applicationReturnPath(path)).toBe(path); expect(assetQuery('')).toEqual({})
  expect(() => assetListRoute({ ...query, q: 'hidden' } as typeof query)).toThrow()
})
it('retains exact summary nanoseconds/offset and explicit absence without forwarding a money proof', () => {
  const query = { as_of: '2026-11-07T12:00:00.123456789+08:00' }
  const path = assetSummaryRoute('new', query)
  expect(assetSummaryQuery(path.slice(path.indexOf('?')))).toEqual(query)
  expect(assetReturnPath(path)).toBe(path); expect(applicationReturnPath(path)).toBe(path)
  expect(assetSummaryQuery('')).toEqual({}); expect(assetSummaryRoute('unit', {})).toBe('/assets/unit/summary')
  expect(() => assetSummaryRoute('unit', { as_of: 'bad' })).toThrow()
})
it.each([undefined, null, {}, 7, '', '/assets/../items', '/assets\\other', 'https://evil.example/assets', '//evil.example/assets', '/assets#secret', '/assets?', '/assets?amount=1', '/assets?q=hidden', '/assets?status=disposed', '/assets?currency=JPY&currency=USD', '/assets?limit=01', '/assets?limit=101', '/assets?from=2026-02-30', '/assets?from=2026-11-07&to=2026-01-01', '/assets?merchant=%EF', '/assets?merchant=%', '/assets?merchant=\ud800', '/assets/%EF', '/assets/%2Fsecret/edit', '/assets/%00/edit', '/assets/unit/edit/new', '/assets/unit/retire/new', '/assets/unit/dispose/new', '/assets/unit/summary/new', '/assets/unit/delete', '/assets/unit?manual_acquisition_minor=1', '/assets/unit/usage?delta=1', '/assets/unit/usage/new?key=secret', '/assets/unit/summary?asset_revision=7', '/assets/unit/summary?as_of=2026-11-07T00%3A00%3A00Z&as_of=2026-11-08T00%3A00%3A00Z', '/assets/unit/summary?as_of=%EF', '/assets/./usage', `/assets/${'😀'.repeat(129)}`, `/assets?merchant=${'x'.repeat(18000)}`])('rejects unsafe, repaired, duplicated or mutation-bearing returns %j', (path) => {
  expect(assetReturnPath(path)).toBeUndefined()
})
it.each(['?q=text', '?currency=jpy', '?cursor=', '?limit=0', '?merchant=%', '?merchant=%EF', '?merchant=\ud800', '?merchant=one&merchant=two', `?merchant=${'x'.repeat(16385)}`])('rejects invalid global query %j without throwing or repairing it', (search) => {
  expect(assetQuery(search)).toBeUndefined()
})
it.each(['?as_of=', '?as_of=%', '?as_of=%EF', '?as_of=\ud800', '?as_of=2026-02-30T00:00:00Z', '?as_of=0000-01-01T00:00:00Z', '?as_of=2026-11-07T00:00:00.1234567891Z', '?paid_minor=1', `?as_of=${'x'.repeat(16385)}`])('rejects invalid summary query %j', (search) => {
  expect(assetSummaryQuery(search)).toBeUndefined()
})
it('copies only a validated global list and bounded cursor trail, not arbitrary route state', () => {
  const trail = [null, 'opaque']
  const state = assetNavigationState('/assets?currency=JPY&limit=1', trail)
  trail[1] = 'changed'
  expect(state).toEqual({ assetList: '/assets?currency=JPY&limit=1', assetCursors: [null, 'opaque'] })
  for (const path of ['/assets/unit/edit', '/assets?paid_minor=1', 'https://evil.example/assets', { amount: 1 }]) expect(assetNavigationState(path, { key: 'private' })).toEqual({ assetList: '/assets', assetCursors: [] })
  expect(assetNavigationState('/assets', ['x'.repeat(2049)])).toEqual({ assetList: '/assets', assetCursors: [] })
})
it.each(['.', '..', '/', '\\', '\0', '\ud800', '😀'.repeat(129)])('rejects unsafe Unit IDs before building any asset URL %j', (id) => {
  expect(() => assetRoute(id)).toThrow(); expect(() => assetSummaryRoute(id, {})).toThrow()
})
