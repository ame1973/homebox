import { describe, expect, it } from 'vitest'
import { decodeZXing } from './scan-zxing-core'
import { parseHomeboxQR } from './scan-qr'
import { encodeQRLabel } from './qr-label-encode'
import { makeQRLabelLink } from './qr-label-link'

function raster(size: number, modules: Uint8Array) {
  const scale = 4, width = size * scale, pixels = new Uint8ClampedArray(width * width)
  for (let y = 0; y < width; y++) for (let x = 0; x < width; x++) pixels[y * width + x] = modules[Math.floor(y / scale) * size + Math.floor(x / scale)] ? 0 : 255
  return { pixels, width }
}

describe('local QR label encoding', () => {
  it.each([
    ['location', '4300a96e-49bc-4190-8cb3-47d881a3651e', 'https://homebox.example'],
    ['location', 'new', 'http://127.0.0.1:8080'],
    ['unit', 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~', 'https://homebox.example:8443'],
    ['unit', 'A'.repeat(128), 'https://homebox.example'],
  ] as const)('creates a bounded QR matrix whose actual pixels decode to the exact %s link', (kind, id, origin) => {
    const result = encodeQRLabel(kind, id, origin)
    expect(result.size).toBeGreaterThanOrEqual(29); expect(result.size).toBeLessThanOrEqual(185)
    expect((result.size - 25) % 4).toBe(0)
    expect(result.modules).toHaveLength(result.size * result.size)
    for (let y = 0; y < result.size; y++) for (let x = 0; x < result.size; x++) {
      const value = result.modules[y * result.size + x]
      expect(value === 0 || value === 1).toBe(true)
      if (x < 4 || y < 4 || x >= result.size - 4 || y >= result.size - 4) expect(value).toBe(0)
    }
    const image = raster(result.size, result.modules)
    const decoded = decodeZXing(image.pixels, image.width, image.width, ['QR_CODE'])
    expect(decoded).toEqual([{ format: 'QR_CODE', rawValue: result.target.url }])
    expect(parseHomeboxQR(decoded[0]?.rawValue, origin)).toEqual({ kind, id, path: makeQRLabelLink(kind, id, origin).path })
  })

  it('creates independent deterministic matrices rather than retaining mutable caller data', () => {
    const first = encodeQRLabel('unit', 'unit-1', 'https://homebox.example')
    const second = encodeQRLabel('unit', 'unit-1', 'https://homebox.example')
    expect(first).not.toBe(second); expect(first.modules).not.toBe(second.modules)
    expect(first.modules).toEqual(second.modules); expect(first.target).toEqual(second.target)
    first.modules.fill(0)
    expect(second.modules.some((value) => value === 1)).toBe(true)
  })

  it.each([
    ['location', 'tree', 'https://homebox.example'], ['location', 'legacy/owner', 'https://homebox.example'],
    ['unit', 'literal%2F', 'https://homebox.example'], ['unit', 'name+plus', 'https://homebox.example'],
    ['unit', 'unit', 'https://foreign.example/path'],
  ] as const)('refuses unsafe or ambiguous %s inputs without exposing their values', (kind, id, origin) => {
    const error = (() => { try { encodeQRLabel(kind, id, origin); return undefined } catch (cause) { return cause } })()
    expect(error).toBeInstanceOf(Error); expect(String(error)).not.toContain(id); expect(String(error)).not.toContain(origin)
  })
})
