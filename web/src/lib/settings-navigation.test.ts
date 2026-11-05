import { expect, it } from 'vitest'
import { applicationReturnPath, settingsReturnPath, taxonomyCreateRoute, taxonomyEditRoute, taxonomyListRoute } from './settings-navigation'

it('keeps imported new/tree IDs distinct from the static create routes without rewriting identity', () => {
  expect(taxonomyListRoute('categories')).toBe('/settings/categories')
  expect(taxonomyListRoute('tags')).toBe('/settings/tags')
  expect(taxonomyCreateRoute('categories')).toBe('/settings/categories/new')
  expect(taxonomyCreateRoute('categories', '房間')).toBe('/settings/categories/new?parent=%E6%88%BF%E9%96%93')
  expect(taxonomyCreateRoute('tags', 'ignored')).toBe('/settings/tags/new')
  for (const id of ['new', 'NEW', 'tree', '房間']) {
    expect(taxonomyEditRoute('categories', id)).toBe(`/settings/categories/${encodeURIComponent(id)}/edit`)
    expect(taxonomyEditRoute('tags', id)).toBe(`/settings/tags/${encodeURIComponent(id)}/edit`)
  }
})

it.each(['/settings', '/settings/account', '/settings/categories', '/settings/tags', '/settings/categories/new', '/settings/tags/new',
  '/settings/categories/new/edit', '/settings/tags/tree/edit', '/settings/categories/NEW/edit', '/settings/categories/%E6%88%BF%E9%96%93/edit',
  '/settings/categories/new?parent=parent', '/settings/categories/NEW?parent=parent'])('preserves only implemented settings return path %s', (path) => {
  expect(settingsReturnPath(path)).toBe(path)
  expect(applicationReturnPath(path)).toBe(path)
})

it.each([null, undefined, {}, 7, '', '/login', '/settings-extra', '/settings/system', '/settings/backup', '/settings/account/new',
  'https://evil.example/settings', '//evil.example/settings', '/settings/categories/room', '/settings/tags/room/unknown',
  '/settings/categories/room/edit?secret=value', '/settings/categories/new?parent=room&extra=value', '/settings/categories/new?parent=a&parent=b',
  '/settings/categories/new?parent=', '/settings/categories/new?parent=..', '/settings/categories/new?parent=%2Felsewhere',
  '/settings/tags/new?parent=parent', '/settings?unknown=value', '/settings/account?unknown=value', '/settings/categories/room/edit#secret',
  '/settings/categories/%2Felsewhere/edit', '/settings/categories/%5celsewhere/edit', '/settings/categories/%00/edit',
  '/settings/categories/%EF/edit', '/settings/categories/room\\elsewhere/edit', `/settings/categories/${'😀'.repeat(129)}/edit`, 'x'.repeat(2049)])('rejects unsafe or unimplemented settings return path %j', (path) => {
  expect(settingsReturnPath(path)).toBeUndefined()
  expect(applicationReturnPath(path)).toBeUndefined()
})

it('retains established safe location returns without admitting unrelated routes', () => {
  expect(applicationReturnPath('/locations/room/edit')).toBe('/locations/room/edit')
  expect(settingsReturnPath('/locations/room/edit')).toBeUndefined()
})
