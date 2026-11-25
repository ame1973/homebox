import BitMatrix from '@zxing/library/esm/core/common/BitMatrix'
import Decoder from '@zxing/library/esm/core/qrcode/decoder/Decoder'
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
    ['unit', '62aca93f-e021-426f-86d9-a48e41bf1b6b', 'http://127.0.0.1:35917'],
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

  it('reads a deterministic set of generated Unit labels through the scanner decoder', () => {
    const failures: string[] = []
    for (let index = 0; index < 64; index++) {
      const id = `62aca93f-e021-426f-86d9-${index.toString(16).padStart(12, '0')}`
      const result = encodeQRLabel('unit', id, 'http://127.0.0.1:35917'), image = raster(result.size, result.modules)
      let pixels = image.pixels
      for (let turn = 0; turn < 4; turn++) {
        const decoded = decodeZXing(pixels, image.width, image.width, ['QR_CODE'])
        if (decoded.length !== 1 || decoded[0]?.rawValue !== result.target.url) failures.push(`${id}:${turn}`)
        const rotated = new Uint8ClampedArray(pixels.length)
        for (let y = 0; y < image.width; y++) for (let x = 0; x < image.width; x++) rotated[(image.width - 1 - x) * image.width + y] = pixels[y * image.width + x]!
        pixels = rotated
      }
    }
    expect(failures).toEqual([])
  })

  it.each(['000000000013', '000000000037', 'a48e41bf1b6b'])('reads a generated label in all quarter-turn orientations: %s', (suffix) => {
    const id = `62aca93f-e021-426f-86d9-${suffix}`
    const result = encodeQRLabel('unit', id, 'http://127.0.0.1:35917')
    const image = raster(result.size, result.modules)
    let pixels = image.pixels
    for (let turn = 0; turn < 4; turn++) {
      expect(decodeZXing(pixels, image.width, image.width, ['QR_CODE'])).toEqual([{ format: 'QR_CODE', rawValue: result.target.url }])
      const rotated = new Uint8ClampedArray(pixels.length)
      for (let y = 0; y < image.width; y++) for (let x = 0; x < image.width; x++) rotated[(image.width - 1 - x) * image.width + y] = pixels[y * image.width + x]!
      pixels = rotated
    }
  })

  it('decodes the exact failing browser sample directly from its unpadded modules', () => {
    const result = encodeQRLabel('unit', '62aca93f-e021-426f-86d9-a48e41bf1b6b', 'http://127.0.0.1:35917')
    const size = result.size - 8, matrix = new BitMatrix(size, size)
    for (let y = 0; y < size; y++) for (let x = 0; x < size; x++) if (result.modules[(y + 4) * result.size + x + 4]) matrix.set(x, y)
    expect(new Decoder().decodeBitMatrix(matrix).getText()).toBe(result.target.url)
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
