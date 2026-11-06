import { expect, it } from 'vitest'
import { applicationReturnPath } from './settings-navigation'
import { itemPurchasesRoute, purchaseAllocationRoute, purchaseCursorTrail, purchaseListRoute, purchaseNavigationState, purchaseQuery, purchaseReturnPath, purchaseRoute, purchaseSelectionRoute } from './purchase-navigation'

it('escapes imported new identities without rewriting them and builds the actual operating routes', () => {
  for (const id of ['new', 'NEW', 'tree', '物品 😀']) {
    expect(purchaseRoute(id)).toBe(`/purchases/${encodeURIComponent(id)}${id.toLowerCase() === 'new' ? '/view' : ''}`)
    expect(purchaseRoute(id, 'edit')).toBe(`/purchases/${encodeURIComponent(id)}/edit`)
    expect(itemPurchasesRoute(id)).toBe(`/items/${encodeURIComponent(id)}/purchases`)
    expect(purchaseAllocationRoute(id, id)).toBe(`/purchases/${encodeURIComponent(id)}/allocate/${encodeURIComponent(id)}`)
    expect(purchaseSelectionRoute(id)).toBe(`/purchases/${encodeURIComponent(id)}/allocate`)
  }
  const selection = new URL(purchaseSelectionRoute('purchase', { q: 'approved', limit: 1 }), 'https://homebox.invalid')
  expect(selection.pathname).toBe('/purchases/purchase/allocate')
  expect(Object.fromEntries(selection.searchParams)).toEqual({ q: 'approved', limit: '1' })
})
it('preserves every global filter and independent explicit continuation without allowing catalog text queries', () => {
  const query = { from: '2026-01-01', to: '2026-11-07', currency: 'JPY', merchant: '  Exact é 😀 ', category_id: 'category', location_id: 'location', scope: 'direct' as const, status: 'active' as const, limit: 1, cursor: 'opaque' }
  const path = purchaseListRoute(query)
  expect(purchaseQuery(path.slice(path.indexOf('?')))).toEqual(query)
  expect(purchaseReturnPath(path)).toBe(path)
  expect(purchaseQuery('')).toEqual({})
  expect(() => purchaseListRoute({ ...query, q: 'hidden' } as typeof query)).toThrow()
})
it.each(['/purchases', '/purchases/new', '/purchases/new/view', '/purchases/NEW/edit', '/purchases/tree', '/purchases/purchase/edit', '/purchases/purchase/allocate', '/purchases/purchase/allocate?limit=1&q=approved', '/purchases/purchase/allocate/new', '/purchases/%E6%8E%A1%E8%B3%BC/view', '/items/new/purchases', '/purchases?merchant=Exact&limit=1&cursor=opaque'])('admits only an implemented purchase return %s', (path) => {
  expect(purchaseReturnPath(path)).toBe(path)
  expect(applicationReturnPath(path)).toBe(path)
})
it.each([undefined, null, {}, 7, '', '/purchases/../items', '/purchases\\other', 'https://evil.example/purchases', '//evil.example/purchases', '/purchases?currency=JPY#secret', '/purchases?amount=100', '/purchases?q=hidden', '/purchases?currency=JPY&currency=USD', '/purchases?limit=01', '/purchases?limit=101', '/purchases?from=2026-02-30', '/purchases?from=2026-11-07&to=2026-01-01', '/purchases?merchant=%EF', '/purchases?merchant=%', '/purchases/%EF', '/purchases/%2Fsecret/edit', '/purchases/%00/edit', '/purchases/id/edit/extra', '/purchases/id/delete', '/purchases/id?paid_minor=100', '/purchases/id/allocate/item?quantity_base=1', '/purchases/id/allocate?key=secret', '/purchases/id/allocate?q=%EF', '/purchases/id/allocate?q=one&q=two', '/purchases/id/allocate/%2Fsecret', '/items/item/purchases?cursor=secret', '/items/%5Csecret/purchases', '/purchases/./view', `/purchases/${'😀'.repeat(129)}`, `/purchases?merchant=${'x'.repeat(18000)}`, '/purchases?merchant=\ud800'])('rejects unsafe, repaired, duplicate or financial return data %j', (path) => {
  expect(purchaseReturnPath(path)).toBeUndefined()
})
it.each(['?q=text', '?merchant=%', '?merchant=%EF', '?merchant=\ud800', '?unknown=value', '?cursor=', '?limit=0', '?currency=jpy', '?merchant=one&merchant=two', `?merchant=${'x'.repeat(16385)}`])('rejects invalid raw global query %j without throwing or repairing it', (search) => {
  expect(purchaseQuery(search)).toBeUndefined()
})
it('reconstructs navigation state from safe paths, cursors and one Item identity only', () => {
  expect(purchaseNavigationState('/purchases?currency=JPY', [null, 'opaque'], 'new')).toEqual({ purchaseList: '/purchases?currency=JPY', purchaseCursors: [null, 'opaque'], purchaseItem: 'new' })
  expect(purchaseNavigationState('/items/new/purchases', [], 'new').purchaseList).toBe('/items/new/purchases')
  expect(purchaseNavigationState('/purchases/id/edit', ['x'.repeat(2049)], '../secret')).toEqual({ purchaseList: '/purchases', purchaseCursors: [] })
  expect(purchaseNavigationState('/items?paid_minor=100', { key: 'private' }, { amount: 100 })).toEqual({ purchaseList: '/purchases', purchaseCursors: [] })
  const original = [null, 'cursor']; const copy = purchaseCursorTrail(original); original[1] = 'changed'; expect(copy).toEqual([null, 'cursor'])
  for (const value of [null, {}, [7], ['\ud800'], [''], ['x'.repeat(2049)]]) expect(purchaseCursorTrail(value)).toEqual([])
})
it.each(['.', '..', '/', '\\', '\0', '\ud800', '😀'.repeat(129)])('rejects unsafe IDs before building any operating URL %j', (id) => {
  for (const build of [purchaseRoute, itemPurchasesRoute, purchaseSelectionRoute, (value: string) => purchaseAllocationRoute('purchase', value)]) expect(() => build(id)).toThrow()
})
