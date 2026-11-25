import { matchRoutes } from 'react-router'
import { describe, expect, it } from 'vitest'
import { parseHomeboxQR } from './scan-qr'
import { makeQRLabelLink } from './qr-label-link'

const routes = [{ path: '/locations/:id/view' }, { path: '/locations/:id' }, { path: '/units/:id' }]

describe('HomeBox QR label identity', () => {
  it.each([
    ['location', '4300a96e-49bc-4190-8cb3-47d881a3651e', 'https://homebox.example', '/locations/4300a96e-49bc-4190-8cb3-47d881a3651e'],
    ['location', 'new', 'http://127.0.0.1:8080', '/locations/new/view'],
    ['unit', 'new', 'http://[::1]:8080', '/units/new'],
    ['unit', 'ABC-123._~', 'https://homebox.example:8443', '/units/ABC-123._~'],
  ] as const)('constructs a same-origin %s link for exact ID %s', (kind, id, origin, path) => {
    const link = makeQRLabelLink(kind, id, origin)
    expect(link).toEqual({ kind, id, url: `${origin}/${kind === 'location' ? 'l' : 'u'}/${id}`, path })
    expect(parseHomeboxQR(link.url, origin)).toEqual({ kind, id, path })
    expect(matchRoutes(routes, path)?.at(-1)?.params.id).toBe(id)
    const url = new URL(link.url)
    expect(url.origin).toBe(origin); expect(url.search).toBe(''); expect(url.hash).toBe(''); expect(url.username).toBe(''); expect(url.password).toBe('')
  })

  it('retains every supported unreserved ASCII byte without changing its identity', () => {
    const id = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~'
    for (const kind of ['unit', 'location'] as const) {
      const link = makeQRLabelLink(kind, id, 'https://homebox.example')
      expect(parseHomeboxQR(link.url, 'https://homebox.example')?.id).toBe(id)
      expect(matchRoutes(routes, link.path)?.at(-1)?.params.id).toBe(id)
    }
  })

  it.each(['', '.', '..', 'tree', 'legacy/id', 'legacy\\id', '\0', 'literal%2F', 'name+plus', 'has space', 'unité', 'line\n', 'x'.repeat(129), '\ud800'])
  ('withholds an unsafe or ambiguous Location ID %j rather than changing it', (id) => {
    expect(() => makeQRLabelLink('location', id, 'https://homebox.example')).toThrow()
  })

  it.each(['', '.', '..', 'legacy/id', 'legacy\\id', '\0', 'literal%2F', 'name+plus', 'has space', 'unité', 'line\n', 'x'.repeat(129), '\ud800'])
  ('withholds an unsafe or ambiguous Unit ID %j rather than changing it', (id) => {
    expect(() => makeQRLabelLink('unit', id, 'https://homebox.example')).toThrow()
  })

  it.each([null, undefined, {}, 1, '', 'null', 'https://homebox.example/', 'HTTPS://HOMEBOX.EXAMPLE', 'https://user@homebox.example',
    'https://homebox.example/path', 'https://homebox.example?secret=1', 'https://homebox.example#fragment', 'javascript:alert(1)'])
  ('requires a canonical application origin, never a repaired URL: %j', (origin) => {
    expect(() => makeQRLabelLink('unit', 'unit', origin as never)).toThrow()
  })

  it('rejects unknown kinds without putting identity values in errors', () => {
    expect(() => makeQRLabelLink('other' as never, 'private-id', 'https://homebox.example')).toThrow('Invalid QR label target')
    try { makeQRLabelLink('other' as never, 'private-id', 'https://homebox.example') } catch (error) { expect(String(error)).not.toContain('private-id') }
  })
})
