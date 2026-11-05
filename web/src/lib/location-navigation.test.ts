import { expect, it } from 'vitest'
import { locationReturnPath, locationRoute } from './location-navigation'

it('keeps supported imported IDs out of the static create route, including case-insensitive matches', () => {
  expect(locationRoute('room')).toBe('/locations/room')
  expect(locationRoute('房間', 'edit')).toBe('/locations/%E6%88%BF%E9%96%93/edit')
  for (const id of ['new', 'NEW', 'New']) {
    expect(locationRoute(id)).toBe(`/locations/${id}/view`)
    expect(locationRoute(id, 'edit')).toBe(`/locations/${id}/edit`)
  }
})

it.each(['/locations', '/locations/new', '/locations/room', '/locations/room/edit', '/locations/new/edit', '/locations/new/view', '/locations/NEW/view', '/locations/%E6%88%BF%E9%96%93/edit', '/locations/new?parent=room', '/locations/new?parent=%E6%88%BF%E9%96%93'])('allows only the explicit relative application location destination %s', (path) => {
  expect(locationReturnPath(path)).toBe(path)
})

it.each([null, undefined, {}, 12, '', '/login', 'https://evil.example/locations', '//evil.example/locations', '/locations-extra', '/locations/room#secret', '/locations/room?secret=value', '/locations/new?parent=room&secret=value', '/locations/new?parent=a&parent=b', '/locations/new?parent=', '/locations/new?parent=%2Felsewhere', '/locations/new?parent=%00', '/locations/tree', '/locations/new/view?parent=room', '/locations/new/edit?parent=room', '/locations/%2f%2fevil.example', '/locations/%5croom', '/locations/%00', '/locations/room\\elsewhere', '/locations/room/other', '/locations/%EF', `/locations/${'a'.repeat(129)}`, `/locations/${'😀'.repeat(129)}`, `/locations/${'a'.repeat(2050)}`, '/locations/new?parent=..'])('does not carry an unsafe or unrelated auth return target %j', (path) => {
  expect(locationReturnPath(path)).toBeUndefined()
})
