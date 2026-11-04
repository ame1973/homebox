import { expect, it } from 'vitest'
import { locationReturnPath } from './location-navigation'

it.each(['/locations', '/locations/new', '/locations/room', '/locations/room/edit', '/locations/%E6%88%BF%E9%96%93/edit', '/locations/new?parent=room', '/locations/new?parent=%E6%88%BF%E9%96%93'])('allows only the explicit relative application location destination %s', (path) => {
  expect(locationReturnPath(path)).toBe(path)
})

it.each([null, undefined, {}, 12, '', '/login', 'https://evil.example/locations', '//evil.example/locations', '/locations-extra', '/locations/room#secret', '/locations/room?secret=value', '/locations/new?parent=room&secret=value', '/locations/new?parent=a&parent=b', '/locations/new?parent=', '/locations/new?parent=%2Felsewhere', '/locations/new?parent=%00', '/locations/tree', '/locations/new/edit', '/locations/%2f%2fevil.example', '/locations/%5croom', '/locations/%00', '/locations/room\\elsewhere', '/locations/room/other', '/locations/%EF', `/locations/${'a'.repeat(129)}`, `/locations/${'😀'.repeat(129)}`, `/locations/${'a'.repeat(2050)}`, '/locations/new?parent=..'])('does not carry an unsafe or unrelated auth return target %j', (path) => {
  expect(locationReturnPath(path)).toBeUndefined()
})
