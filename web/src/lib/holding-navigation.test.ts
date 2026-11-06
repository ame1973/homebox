import { expect, it } from 'vitest'
import { applicationReturnPath } from './settings-navigation'
import { holdingReturnPath, itemHoldingsRoute, stockActions, stockPrefill, stockRoute, unitCreateRoute, unitRoute } from './holding-navigation'

it('builds real shared routes with exact imported identities and only a location prefill', () => {
  for (const id of ['new', 'tree', '  物品 +?# é  ']) {
    const encoded = encodeURIComponent(id)
    expect(itemHoldingsRoute(id)).toBe(`/items/${encoded}/holdings`)
    expect(unitCreateRoute(id)).toBe(`/items/${encoded}/units/new`)
    expect(unitRoute(id)).toBe(`/units/${encoded}`)
    for (const mode of ['edit', 'move', 'status'] as const) expect(unitRoute(id, mode)).toBe(`/units/${encoded}/${mode}`)
    for (const action of stockActions) {
      expect(stockRoute(id, action)).toBe(`/items/${encoded}/stock/${action}`)
      const url = stockRoute(id, action, '  位置 +?# é  ')
      expect(new URL(url, 'https://homebox.invalid').searchParams.get('location')).toBe('  位置 +?# é  ')
      expect(holdingReturnPath(url)).toBe(url)
    }
  }
  expect(stockRoute('item', 'receive', '\0'.repeat(128))).toContain('location=')
})
it.each(['/items/item/holdings', '/items/item/units/new', '/units/new', '/units/tree/edit', '/units/unit/move', '/units/unit/status', '/u/unit', ...stockActions.map((action) => `/items/item/stock/${action}`)])('permits only the implemented safe holding return %s', (path) => {
  expect(holdingReturnPath(path)).toBe(path)
  expect(applicationReturnPath(path)).toBe(path)
})
it.each([undefined, null, {}, '', 'https://evil.example/units/id', '//evil.example/units/id', '/units/id#fragment', '/units/id?x=1', '/items/item/holdings?location=loc', '/items/item/units/new?location=loc', '/units/id/delete', '/units/id/qr', '/assets/unit', '/items/item/stock/unknown', '/items/item/stock/receive?location=', '/items/item/stock/receive?location=x&location=y', '/items/item/stock/receive?quantity_base=1', '/items/item/stock/receive?location=%ff', '/items/item/stock/receive?location=%', '/items/item/stock/receive?location=%ED%A0%80', '/items/item/stock/receive?location=' + 'x'.repeat(129), '/units/%2e%2e', '/units/%2F', '/units/%5c', '/units/%00', '/units/%ff', '/units/../id', '/units\\id', '/units/id/', '/units/' + 'x'.repeat(129), '/units/\uD800'])('rejects unsafe or unimplemented holding return %j without repair', (value) => {
  expect(holdingReturnPath(value)).toBeUndefined()
})
it('rejects invalid route identities/actions before creating a navigable target', () => {
  for (const id of ['', '.', '..', 'a/b', 'a\\b', '\0', '😀'.repeat(129), '\uD800']) {
    for (const build of [itemHoldingsRoute, unitCreateRoute, unitRoute]) expect(() => build(id)).toThrow(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
    expect(() => stockRoute(id, 'receive')).toThrow(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
  }
  expect(() => unitRoute('unit', 'delete' as never)).toThrow(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
  expect(() => stockRoute('item', 'unknown' as never)).toThrow(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
  expect(() => stockRoute('item', 'receive', '')).toThrow(expect.objectContaining({ code: 'VALIDATION_ERROR' }))
})
it('decodes exactly one bounded location criterion without a hidden quantity or malformed UTF repair', () => {
  expect(stockPrefill('?location=%E4%BD%8D%E7%BD%AE+%2B')).toBe('位置 +')
  for (const search of ['', '?', 'location=x', '?location=', '?location=x&location=y', '?location=x&quantity_base=1', '?unknown=x', '?location=%ff', '?location=%', '?location=' + 'x'.repeat(129), '?location=\uD800', '?' + 'x'.repeat(4096)]) expect(stockPrefill(search)).toBeUndefined()
})
