import { expect, it } from 'vitest'
import { catalogListRoute, catalogQuery, catalogReturnPath, catalogRoute } from './catalog-navigation'
import { applicationReturnPath } from './settings-navigation'

it('keeps imported new/tree/Unicode identities independent of static creation and exact safe paths', () => {
  for (const id of ['new', 'NEW', 'tree', '  盒子 é #?%  ']) {
    const detail = `/items/${encodeURIComponent(id)}${id.toLowerCase() === 'new' ? '/view' : ''}`
    expect(catalogRoute(id)).toBe(detail)
    expect(catalogRoute(id, 'edit')).toBe(`/items/${encodeURIComponent(id)}/edit`)
    expect(catalogReturnPath(detail)).toBe(detail)
  }
  for (const id of ['', '.', '..', 'a/b', 'a\\b', 'a\0b', '\uD800', '😀'.repeat(129)]) expect(() => catalogRoute(id)).toThrow(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
})

it.each(['/items', '/items/new', '/items/NEW', '/items/tree', '/items/new/view', '/items/new/edit', '/items/item/view', '/items/item/edit',
  '/items?limit=1&cursor=opaque', '/items?q=%E6%BF%BE%E8%8A%AF&status=archived&scope=direct', '/items?from=0001-01-01&to=9999-12-31&currency=TWD&merchant=Exact&category_id=category&location_id=location'])('preserves implemented catalog return %s', (path) => {
  expect(catalogReturnPath(path)).toBe(path)
  expect(applicationReturnPath(path)).toBe(path)
})

it.each([null, undefined, 7, {}, '', '//evil.example/items', 'https://evil.example/items', '/items-extra', '/items/room/unknown',
  '/items/room/edit?unknown=value', '/items/new?category=one', '/items/room#secret', '/items/a\\b', '/items/%2Fsecret', '/items/%5csecret', '/items/%00', '/items/%EF',
  '/items/a/../new', '/items/%2e%2e', '/items/./new', '/items?q=a&q=b', '/items?limit=1&limit=2', '/items?unknown=x', '/items?__proto__=x', '/items?constructor=x',
  '/items?limit=0', '/items?limit=101', '/items?limit=01', '/items?limit=1e2', '/items?limit=1.0', '/items?from=0000-01-01', '/items?from=2026-02-29',
  '/items?from=2026-11-06&to=2026-11-05', '/items?merchant=', '/items?status=disposed', '/items?scope=all', '/items?currency=twd', '/items?', `/items?q=${'x'.repeat(18000)}`])('rejects unsafe catalog return %j', (path) => {
  expect(catalogReturnPath(path)).toBeUndefined()
  expect(applicationReturnPath(path)).toBeUndefined()
})

it('round-trips every filter and opaque cursor without interpreting literals, dropping criteria or installing defaults', () => {
  const values = { from: '2024-02-29', to: '2026-12-31', currency: 'TWD', merchant: ' Exact & + #? ', category_id: '分類', location_id: '位置',
    scope: 'direct' as const, status: 'archived' as const, q: '  濾芯 & + #?  ', limit: 2, cursor: 'opaque?limit=99&secret=1+#' }
  const path = catalogListRoute(values)
  expect(catalogReturnPath(path)).toBe(path)
  expect(catalogQuery(new URL(path, 'https://homebox.invalid').search)).toEqual(values)
  expect(catalogListRoute({})).toBe('/items')
  expect(catalogQuery('')).toEqual({})
  expect(catalogListRoute({ q: '' })).toBe('/items?q=')
  expect(() => catalogListRoute({ limit: 0 })).toThrow(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
  expect(catalogQuery(`?q=${'x'.repeat(16385)}`)).toBeUndefined()
})
