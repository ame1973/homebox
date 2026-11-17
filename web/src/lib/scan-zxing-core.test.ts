import { describe, expect, it } from 'vitest'
import BarcodeFormat from '@zxing/library/esm/core/BarcodeFormat'
import EncodeHintType from '@zxing/library/esm/core/EncodeHintType'
import QRCodeWriter from '@zxing/library/esm/core/qrcode/QRCodeWriter'
import { decodeZXing } from './scan-zxing-core'

// Independent GS1 module patterns keep these fixtures separate from ZXing's
// readers. The digits/checks include the server's fixed UPC-E test vectors.
const left = ['0001101', '0011001', '0010011', '0111101', '0100011', '0110001', '0101111', '0111011', '0110111', '0001011']
const parity = ['LLLLLL', 'LLGLGG', 'LLGGLG', 'LLGGGL', 'LGLLGG', 'LGGLLG', 'LGGGLL', 'LGLGLG', 'LGLGGL', 'LGGLGL']
const upceParity = [0x38, 0x34, 0x32, 0x31, 0x2c, 0x26, 0x23, 0x2a, 0x29, 0x25]
const invert = (bits: string) => [...bits].map((bit) => bit === '0' ? '1' : '0').join('')
function digit(value: string, encoding: 'L' | 'G' | 'R') {
  const bits = left[Number(value)]!
  if (encoding === 'L') return bits
  return encoding === 'R' ? invert(bits) : invert([...bits].reverse().join(''))
}
function ean13(code: string) {
  const scheme = parity[Number(code[0])]!
  return '101' + [...code.slice(1, 7)].map((value, index) => digit(value, scheme[index] as 'L' | 'G')).join('') +
    '01010' + [...code.slice(7)].map((value) => digit(value, 'R')).join('') + '101'
}
function ean8(code: string) {
  return '101' + [...code.slice(0, 4)].map((value) => digit(value, 'L')).join('') +
    '01010' + [...code.slice(4)].map((value) => digit(value, 'R')).join('') + '101'
}
function upce(code: string) {
  const mask = upceParity[Number(code[7])]! ^ (code[0] === '1' ? 0x3f : 0)
  return '101' + [...code.slice(1, 7)].map((value, index) => digit(value, mask & (1 << (5 - index)) ? 'G' : 'L')).join('') + '010101'
}
function code128() {
  // Code set B: 000ABC, checksum 94, stop. Widths are standard module counts.
  const patterns = ['211214', '123122', '123122', '123122', '111323', '131123', '131321', '131141', '2331112']
  return patterns.map((widths) => [...widths].map((width, index) => (index % 2 ? '0' : '1').repeat(Number(width))).join('')).join('')
}
function frame(bits: string) {
  const padded = '0'.repeat(16) + bits + '0'.repeat(16), width = padded.length * 4, height = 100
  const pixels = new Uint8ClampedArray(width * height)
  for (let y = 0; y < height; y++) for (let x = 0; x < width; x++) pixels[y * width + x] = padded[Math.floor(x / 4)] === '1' ? 0 : 255
  return { pixels, width, height }
}
function rotated(input: ReturnType<typeof frame>) {
  const pixels = new Uint8ClampedArray(input.pixels.length)
  for (let y = 0; y < input.height; y++) for (let x = 0; x < input.width; x++) pixels[x * input.height + input.height - y - 1] = input.pixels[y * input.width + x]!
  return { pixels, width: input.height, height: input.width }
}

describe('ZXing pixel decoder', () => {
  it.each([
    ['EAN_13', '5901234123457', ean13('5901234123457')],
    ['EAN_13', '4901234567894', ean13('4901234567894')],
    ['EAN_8', '96385074', ean8('96385074')],
    ['UPC_A', '042100005264', ean13('0042100005264')],
    ['CODE_128', '000ABC', code128()],
  ] as const)('decodes an actual %s raster without changing %s', (format, value, bits) => {
    const image = frame(bits)
    expect(decodeZXing(image.pixels, image.width, image.height, [format])).toEqual([{ format, rawValue: value }])
  })

  it.each(['04252614', '01234565', '01234505', '01234514', '01234523', '01234531', '01234543', '01234558', '01234596', '11234538', '10000069'])('preserves the original UPC-E vector %s', (code) => {
    const image = frame(upce(code))
    expect(decodeZXing(image.pixels, image.width, image.height, ['UPC_E'])).toEqual([{ format: 'UPC_E', rawValue: code }])
    for (let digit = 0; digit < 10; digit++) {
      if (String(digit) === code[7]) continue
      const altered = frame(upce(code.slice(0, 7) + digit))
      expect(decodeZXing(altered.pixels, altered.width, altered.height, ['UPC_E'])).toEqual([])
    }
  })

  it('keeps EAN-13 and UPC-A alternatives distinct instead of choosing or converting one', () => {
    const image = frame(ean13('0042100005264'))
    expect(decodeZXing(image.pixels, image.width, image.height, ['EAN_13', 'UPC_A'])).toEqual([
      { format: 'EAN_13', rawValue: '0042100005264' }, { format: 'UPC_A', rawValue: '042100005264' },
    ])
    expect(decodeZXing(image.pixels, image.width, image.height, ['EAN_13'])).toEqual([{ format: 'EAN_13', rawValue: '0042100005264' }])
  })

  it('rejects an incorrect UPC-E checksum and handles a sideways barcode', () => {
    const bad = frame(upce('04252615'))
    expect(decodeZXing(bad.pixels, bad.width, bad.height, ['UPC_E'])).toEqual([])
    const vertical = rotated(frame(ean13('5901234123457')))
    expect(decodeZXing(vertical.pixels, vertical.width, vertical.height, ['EAN_13'])).toEqual([{ format: 'EAN_13', rawValue: '5901234123457' }])
  })

  it('decodes a QR raster, retaining exact Unicode and query text', () => {
    const text = 'https://example.test/l/家庭?x=+'
    const hints = new Map([[EncodeHintType.CHARACTER_SET, 'UTF-8']])
    const bitmap = new QRCodeWriter().encode(text, BarcodeFormat.QR_CODE, 240, 240, hints)
    const pixels = new Uint8ClampedArray(240 * 240)
    for (let y = 0; y < 240; y++) for (let x = 0; x < 240; x++) pixels[y * 240 + x] = bitmap.get(x, y) ? 0 : 255
    expect(decodeZXing(pixels, 240, 240, ['QR_CODE'])).toEqual([{ format: 'QR_CODE', rawValue: text }])
  })

  it('bounds dimensions, pixel buffers and requested formats', () => {
    expect(() => decodeZXing(new Uint8ClampedArray(1), 1, 1, ['EAN_13'])).toThrow()
    expect(() => decodeZXing(new Uint8ClampedArray(16), 4, 5, ['EAN_13'])).toThrow()
    expect(() => decodeZXing(new Uint8ClampedArray(16), Infinity, 4, ['EAN_13'])).toThrow()
    expect(() => decodeZXing(new Uint8ClampedArray(16), 4, 4, [])).toThrow()
    expect(() => decodeZXing(new Uint8ClampedArray(32 * 32), 32, 32, ['QR_CODE', 'QR_CODE'])).toThrow()
    expect(() => decodeZXing(new Uint8ClampedArray(32 * 32), 32, 32, ['OTHER' as never])).toThrow()
    expect(() => decodeZXing(new Uint8ClampedArray(1), 1921, 32, ['EAN_13'])).toThrow()
    expect(() => decodeZXing(new Uint8ClampedArray(1), 1920, 1081, ['EAN_13'])).toThrow()
    const blank = new Uint8ClampedArray(100 * 100).fill(255)
    expect(decodeZXing(blank, 100, 100, ['QR_CODE', 'CODE_128'])).toEqual([])
  })
})
