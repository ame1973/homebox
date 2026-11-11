import { matchRoutes } from 'react-router'
import { describe, expect, it } from 'vitest'
import { parseHomeboxQR } from './scan-qr'

const origin = 'https://homebox.example'
const routes = [{ path: '/locations/:id/view' }, { path: '/locations/:id' }, { path: '/units/:id' }]

describe('HomeBox QR destinations', () => {
  it.each([
    ['https://homebox.example/l/4300a96e-49bc-4190-8cb3-47d881a3651e', 'location', '4300a96e-49bc-4190-8cb3-47d881a3651e', '/locations/4300a96e-49bc-4190-8cb3-47d881a3651e'],
    ['HTTPS://HOMEBOX.EXAMPLE:443/l/new', 'location', 'new', '/locations/new/view'],
    ['https://homebox.example/l/NEW', 'location', 'NEW', '/locations/NEW/view'],
    ['https://homebox.example/u/%E5%AE%B6%E5%85%B7', 'unit', '家具', '/units/%E5%AE%B6%E5%85%B7'],
    ['https://homebox.example/u/%E5%AE%B6%E5%85%B7%2B1', 'unit', '家具+1', '/units/%E5%AE%B6%E5%85%B7%2B1'],
    ['https://homebox.example/u/%0D%0Aunit', 'unit', '\r\nunit', '/units/%0D%0Aunit'],
    ['https://homebox.example/u/a%3Fb%23c', 'unit', 'a?b#c', '/units/a%3Fb%23c'],
    ['https://homebox.example/u/%25', 'unit', '%', '/units/%25'],
    ['https://homebox.example/u/%252f', 'unit', '%2f', '/units/%252f'],
    ['https://homebox.example/l/%E6%88%BF%E9%96%93', 'location', '房間', '/locations/%E6%88%BF%E9%96%93'],
    ['https://homebox.example/l/房間', 'location', '房間', '/locations/%E6%88%BF%E9%96%93'],
  ] as const)('maps only a validated same-origin identity to the canonical route: %s', (value, kind, id, path) => {
    expect(parseHomeboxQR(value, origin)).toEqual({ kind, id, path })
    expect(matchRoutes(routes, path)?.at(-1)?.params.id).toBe(id)
  })

  it('keeps the complete bounded Unicode identity and supports an actual loopback origin', () => {
    const id = '😀'.repeat(128)
    const path = `/units/${encodeURIComponent(id)}`
    expect(parseHomeboxQR(`http://[::1]:8080/u/${encodeURIComponent(id)}`, 'http://[::1]:8080')).toEqual({ kind: 'unit', id, path })
    expect(matchRoutes(routes, path)?.at(-1)?.params.id).toBe(id)
  })

  it('preserves the exact router identity for deterministic mixed-character inputs', () => {
    const alphabet = Array.from('abZ01%2Ff./\\?# +\r\n\0家具😀')
    let seed = 0x70686f74
    const next = () => { seed = (Math.imul(seed, 1664525) + 1013904223) >>> 0; return seed }
    let accepted = 0
    for (let index = 0; index < 4000; index++) {
      const size = 1 + next() % 24
      let id = ''
      for (let character = 0; character < size; character++) id += alphabet[next() % alphabet.length] ?? ''
      for (const alias of ['l', 'u']) {
        const parsed = parseHomeboxQR(`${origin}/${alias}/${encodeURIComponent(id)}`, origin)
        if (!parsed) continue
        accepted++
        expect(parsed.id).toBe(id)
        expect(matchRoutes(routes, parsed.path)?.at(-1)?.params.id).toBe(id)
      }
    }
    expect(accepted).toBeGreaterThan(100)
  })

  it('withholds a retained encoded-slash spelling that React Router would decode as a different identity', () => {
    expect(matchRoutes(routes, '/units/%252F')?.at(-1)?.params.id).toBe('/')
    expect(parseHomeboxQR('https://homebox.example/u/%252F', origin)).toBeUndefined()
  })

  it.each([
    null, undefined, {}, 1, '', ' ', '/l/room', '//homebox.example/l/room',
    'http://homebox.example/l/room', 'https://other.example/l/room', 'https://homebox.example.evil/l/room',
    'https://homebox.example./l/room', 'https://homebox.example:444/l/room',
    'https://@homebox.example/l/room', 'https://user@homebox.example/l/room', 'https://user:pass@homebox.example/l/room',
    'https://homebox.example@other.example/l/room', 'https://homebox.example\\@other.example/l/room',
    'javascript:alert(1)', 'data:text/html,hello', 'ftp://homebox.example/l/room',
    'https://homebox.example/l', 'https://homebox.example/l/', 'https://homebox.example/L/room',
    'https://homebox.example/locations/room', 'https://homebox.example/l/room/extra',
    'https://homebox.example/l/room?', 'https://homebox.example/l/room#',
    'https://homebox.example/l/room?next=https://other.example', 'https://homebox.example/l/room#section',
    ' https://homebox.example/l/room', 'https://homebox.example/l/room ', 'https://homebox.example/l/ro\nom',
    'https://homebox.example/l/room\\other', 'https://homebox.example/l//room',
    'https://homebox.example/l/.', 'https://homebox.example/l/..',
    'https://homebox.example/l/%2e', 'https://homebox.example/u/%2e%2e',
    'https://homebox.example/l/tree', 'https://homebox.example/u/%2F', 'https://homebox.example/u/%5C',
    'https://homebox.example/u/%00', 'https://homebox.example/u/%EF', 'https://homebox.example/u/%',
    'https://homebox.example/u/%252F',
    `https://homebox.example/u/${'a'.repeat(129)}`, `https://homebox.example/u/${'😀'.repeat(129)}`,
    `https://homebox.example/u/${'a'.repeat(4096)}`, 'https://homebox.example/u/\ud800',
  ])('never turns untrusted or unaddressable QR text into navigation: %j', (value) => {
    expect(parseHomeboxQR(value, origin)).toBeUndefined()
  })

  it.each([
    null, undefined, {}, '', 'null', 'javascript:alert(1)', 'https://homebox.example/path',
    'https://homebox.example?secret=1', 'https://homebox.example#fragment',
    'https://@homebox.example', 'https://user@homebox.example', 'https://homebox.example\\@evil.example',
  ])('requires a valid trusted application origin: %j', (candidate) => {
    expect(parseHomeboxQR('https://homebox.example/l/room', candidate)).toBeUndefined()
  })
})
