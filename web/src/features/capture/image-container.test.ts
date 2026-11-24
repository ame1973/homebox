import { describe, expect, it } from 'vitest'
import { maxFrameBytes } from '@/lib/capture-frame'
import { inspectImageContainer } from './image-container'

// Independently encoded by Pillow 10.2.0; WebP samples use Pillow/libwebp 1.3.2.
const png = 'iVBORw0KGgoAAAANSUhEUgAAAAIAAAADCAYAAAC56t6BAAAAHElEQVR4nAXBAQEAMAjAIHwyo9l8B5EcqnhhtB+Pawm7X97uPgAAAABJRU5ErkJggg=='
const jpeg = '/9j/4AAQSkZJRgABAQAAAQABAAD/2wBDAAgGBgcGBQgHBwcJCQgKDBQNDAsLDBkSEw8UHRofHh0aHBwgJC4nICIsIxwcKDcpLDAxNDQ0Hyc5PTgyPC4zNDL/2wBDAQkJCQwLDBgNDRgyIRwhMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjL/wAARCAADAAIDASIAAhEBAxEB/8QAHwAAAQUBAQEBAQEAAAAAAAAAAAECAwQFBgcICQoL/8QAtRAAAgEDAwIEAwUFBAQAAAF9AQIDAAQRBRIhMUEGE1FhByJxFDKBkaEII0KxwRVS0fAkM2JyggkKFhcYGRolJicoKSo0NTY3ODk6Q0RFRkdISUpTVFVWV1hZWmNkZWZnaGlqc3R1dnd4eXqDhIWGh4iJipKTlJWWl5iZmqKjpKWmp6ipqrKztLW2t7i5usLDxMXGx8jJytLT1NXW19jZ2uHi4+Tl5ufo6erx8vP09fb3+Pn6/8QAHwEAAwEBAQEBAQEBAQAAAAAAAAECAwQFBgcICQoL/8QAtREAAgECBAQDBAcFBAQAAQJ3AAECAxEEBSExBhJBUQdhcRMiMoEIFEKRobHBCSMzUvAVYnLRChYkNOEl8RcYGRomJygpKjU2Nzg5OkNERUZHSElKU1RVVldYWVpjZGVmZ2hpanN0dXZ3eHl6goOEhYaHiImKkpOUlZaXmJmaoqOkpaanqKmqsrO0tba3uLm6wsPExcbHyMnK0tPU1dbX2Nna4uPk5ebn6Onq8vP09fb3+Pn6/9oADAMBAAIRAxEAPwDqtKCR6RZIsMBCwRgFoVY/dHUkZP1NFFFezUynAObboQ3/AJY/5H5HmOMxEcZVSqStzS6vuz//2Q=='
const progressiveJPEG = '/9j/4AAQSkZJRgABAQAAAQABAAD/2wBDAAgGBgcGBQgHBwcJCQgKDBQNDAsLDBkSEw8UHRofHh0aHBwgJC4nICIsIxwcKDcpLDAxNDQ0Hyc5PTgyPC4zNDL/2wBDAQkJCQwLDBgNDRgyIRwhMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjL/wgARCAADAAIDASIAAhEBAxEB/8QAFQABAQAAAAAAAAAAAAAAAAAAAAX/xAAVAQEBAAAAAAAAAAAAAAAAAAAEBv/aAAwDAQACEAMQAAABqhkj/8QAFRABAQAAAAAAAAAAAAAAAAAAAyH/2gAIAQEAAQUCKF//xAAaEQACAgMAAAAAAAAAAAAAAAABAwACBCIx/9oACAEDAQE/AchzA62x6Z//xAAbEQABBAMAAAAAAAAAAAAAAAABAAIDBBEykf/aAAgBAgEBPwF1SDOg4F//xAAZEAABBQAAAAAAAAAAAAAAAAABAAIhMUH/2gAIAQEABj8CZAoYv//EABkQAQACAwAAAAAAAAAAAAAAAAEAESExQf/aAAgBAQABPyGgjHuLyf/aAAwDAQACAAMAAAAQC//EABcRAQEBAQAAAAAAAAAAAAAAAAFRABH/2gAIAQMBAT8QIThRrv/EABgRAQADAQAAAAAAAAAAAAAAAAEAITHw/9oACAECAQE/EEtd+Kn/xAAYEAEAAwEAAAAAAAAAAAAAAAABABExcf/aAAgBAQABPxAtyCFDGqW9Z//Z'
const webpLossless = 'UklGRjYAAABXRUJQVlA4TCkAAAAvAYAAEC8w/wKCIv9HExAU+T+aQIDgzP+U4A0i6igIBNKQUgorHNH/WAA='
const webpLossy = 'UklGRmoAAABXRUJQVlA4IF4AAACwAgCdASoCAAMAAMASJagCdLoBJgMEA9f8EOzgmQAA/vKT/2r9Av+Vf/9WOey7nHOt+30f6UAJ7/37sfyK++N3+L//70dfyK++N3+L//9eZf9eZfFp6ynzkoWh8AAA'
const webpAlpha = 'UklGRogAAABXRUJQVlA4WAoAAAAQAAAAAQAAAgAAQUxQSAcAAAAA/4D/AP9AAFZQOCBaAAAAsAMAnQEqAgADAADAEiWoAnS6AfgB+gEwA/gGEAev/dgAMDSy0AAA/QPp3b8Z/+/9lv/2xEaL/+l4PvTutm0CAx6d1s21V//ceX/4VWu0YoCd+0YpiN/zcAAA'
const webpAnimated = 'UklGRqIAAABXRUJQVlA4WAoAAAASAAAAAQAAAgAAQU5JTQYAAAAAAAAAAABBTk1GRAAAAAAAAAAAAAEAAAIAAHgAAAJWUDhMKwAAAC8BgAAQLyAQSNofeo35FxAU/B+dQCAJbbPVg0xc7AAEApSxYoMHIvof7wAAQU5NRioAAAAAAAAAAAABAAACAAB4AAAAVlA4TBEAAAAvAYAAAAdQlFKUp/+BiOh/AAA='

const bytes = (base64: string) => Uint8Array.from(atob(base64), (c) => c.charCodeAt(0))
const ascii = (text: string) => Uint8Array.from(text, (c) => c.charCodeAt(0))
const concat = (...parts: Uint8Array[]) => {
  const result = new Uint8Array(parts.reduce((total, part) => total + part.length, 0))
  let offset = 0
  for (const part of parts) { result.set(part, offset); offset += part.length }
  return result
}
function crc32(data: Uint8Array): number {
  let value = 0xffffffff
  for (const byte of data) {
    value ^= byte
    for (let bit = 0; bit < 8; bit++) value = value >>> 1 ^ (value & 1 ? 0xedb88320 : 0)
  }
  return (value ^ 0xffffffff) >>> 0
}
function pngChunk(name: string, data: Uint8Array): Uint8Array {
  const result = new Uint8Array(data.length + 12), view = new DataView(result.buffer)
  view.setUint32(0, data.length); result.set(ascii(name), 4); result.set(data, 8)
  view.setUint32(data.length + 8, crc32(result.subarray(4, data.length + 8)))
  return result
}
function webpChunk(name: string, data: Uint8Array): Uint8Array {
  const result = new Uint8Array(8 + data.length + (data.length & 1))
  result.set(ascii(name)); new DataView(result.buffer).setUint32(4, data.length, true); result.set(data, 8)
  return result
}
function webpContainer(...chunks: Uint8Array[]): Uint8Array {
  const raw = concat(ascii('RIFF'), new Uint8Array(4), ascii('WEBP'), ...chunks)
  new DataView(raw.buffer).setUint32(4, raw.length - 8, true)
  return raw
}
function exif(orientation: number, little: boolean): Uint8Array {
  const data = new Uint8Array(26), view = new DataView(data.buffer)
  data.set(ascii(little ? 'II' : 'MM')); view.setUint16(2, 42, little); view.setUint32(4, 8, little)
  view.setUint16(8, 1, little); view.setUint16(10, 0x112, little); view.setUint16(12, 3, little)
  view.setUint32(14, 1, little); view.setUint16(18, orientation, little)
  return data
}
function jpegEXIF(raw: Uint8Array, data: Uint8Array): Uint8Array {
  const body = concat(ascii('Exif\0\0'), data), segment = new Uint8Array(body.length + 4)
  segment.set([0xff, 0xe1]); new DataView(segment.buffer).setUint16(2, body.length + 2); segment.set(body, 4)
  return concat(raw.subarray(0, 2), segment, raw.subarray(2))
}
function pngEXIF(raw: Uint8Array, data: Uint8Array): Uint8Array {
  return concat(raw.subarray(0, 33), pngChunk('eXIf', data), raw.subarray(33))
}
function webpEXIF(raw: Uint8Array, data: Uint8Array): Uint8Array {
  return webpContainer(webpChunk('VP8X', new Uint8Array([0x18, 0, 0, 0, 1, 0, 0, 2, 0, 0])), raw.subarray(12), webpChunk('EXIF', data))
}
const expected = (mime: string, orientation = 1) => ({ kind: 'inspected', mime, width: 2, height: 3, orientation })

describe('bounded selected-image container preflight', () => {
  it.each([
    ['transparent PNG', png, 'image/png'], ['baseline JPEG', jpeg, 'image/jpeg'], ['progressive JPEG', progressiveJPEG, 'image/jpeg'],
    ['lossless WebP', webpLossless, 'image/webp'], ['lossy WebP', webpLossy, 'image/webp'], ['lossy alpha WebP', webpAlpha, 'image/webp'],
  ])('reads actual %s dimensions without changing source bytes', (_name, encoded, mime) => {
    const raw = bytes(encoded), original = raw.slice()
    expect(inspectImageContainer(raw)).toEqual(expected(mime))
    expect(raw).toEqual(original)
  })

  it.each([false, true])('reads all TIFF orientations in both byte orders (little=%s)', (little) => {
    for (let orientation = 1; orientation <= 8; orientation++) {
      const data = exif(orientation, little)
      expect(inspectImageContainer(jpegEXIF(bytes(jpeg), data))).toEqual(expected('image/jpeg', orientation))
      expect(inspectImageContainer(pngEXIF(bytes(png), data))).toEqual(expected('image/png', orientation))
      expect(inspectImageContainer(webpEXIF(bytes(webpLossless), data))).toEqual(expected('image/webp', orientation))
      expect(inspectImageContainer(webpEXIF(bytes(webpLossless), concat(ascii('Exif\0\0'), data)))).toEqual(expected('image/webp', orientation))
    }
  })

  it('distinguishes unrecognized, oversized and malformed input', () => {
    expect(inspectImageContainer(new Uint8Array())).toEqual({ kind: 'invalid' })
    expect(inspectImageContainer(ascii('<svg/>'))).toEqual({ kind: 'unsupported' })
    expect(inspectImageContainer(new Uint8Array(maxFrameBytes + 1))).toEqual({ kind: 'too-large' })
    for (const encoded of [jpeg, progressiveJPEG, png, webpLossless, webpLossy, webpAlpha]) {
      const raw = bytes(encoded)
      for (let end = 1; end < raw.length; end++) expect(inspectImageContainer(raw.subarray(0, end)).kind).not.toBe('inspected')
      expect(inspectImageContainer(concat(raw, ascii('private trailing file'))).kind).not.toBe('inspected')
    }
  })

  it('rejects animated, checksum-damaged and ambiguous PNG containers', () => {
    const raw = bytes(png), broken = raw.slice(); broken[45] = broken[45]! ^ 1
    const ihdr = raw.subarray(8, 33), idatAndEnd = raw.subarray(33)
    for (const invalid of [broken, concat(raw.subarray(0, 33), pngChunk('acTL', new Uint8Array(8)), idatAndEnd),
      concat(raw.subarray(0, 33), pngChunk('fcTL', new Uint8Array(26)), idatAndEnd),
      concat(raw.subarray(0, 33), pngChunk('fdAT', new Uint8Array(4)), idatAndEnd),
      concat(raw.subarray(0, 33), ihdr, idatAndEnd), concat(raw.subarray(0, 8), idatAndEnd),
      concat(raw.subarray(0, 33), pngChunk('ABCD', new Uint8Array()), idatAndEnd)]) {
      expect(inspectImageContainer(invalid)).toEqual({ kind: 'invalid' })
    }
  })

  it('checks PNG header, palette, chunk order and chunk names rather than just its dimensions', () => {
    const raw = bytes(png), header = raw.subarray(16, 29).slice(), prefix = raw.subarray(0, 8), rest = raw.subarray(33)
    const source = (data: Uint8Array, ...chunks: Uint8Array[]) => concat(prefix, pngChunk('IHDR', data), ...chunks, rest)
    const palette = pngChunk('PLTE', new Uint8Array([255, 0, 0, 0, 255, 0]))
    const indexed = header.slice(); indexed[8] = 8; indexed[9] = 3
    expect(inspectImageContainer(source(indexed, palette))).toEqual(expected('image/png'))
    expect(inspectImageContainer(source(indexed))).toEqual({ kind: 'invalid' })
    expect(inspectImageContainer(source(indexed, palette, palette))).toEqual({ kind: 'invalid' })
    const gray = header.slice(); gray[9] = 0
    expect(inspectImageContainer(source(gray, palette))).toEqual({ kind: 'invalid' })
    for (const [index, value] of [[8, 7], [9, 1], [10, 1], [11, 1], [12, 2]] as const) {
      const invalid = header.slice(); invalid[index] = value
      expect(inspectImageContainer(source(invalid))).toEqual({ kind: 'invalid' })
    }
    const image = raw.subarray(41, raw.length - 16)
    const separated = concat(prefix, pngChunk('IHDR', header), pngChunk('IDAT', image.subarray(0, 1)),
      pngChunk('tEXt', ascii('note\0value')), pngChunk('IDAT', image.subarray(1)), pngChunk('IEND', new Uint8Array()))
    expect(inspectImageContainer(separated)).toEqual({ kind: 'invalid' })
    expect(inspectImageContainer(source(header, pngChunk('bad1', new Uint8Array())))).toEqual({ kind: 'invalid' })
    expect(inspectImageContainer(source(header, pngChunk('badc', new Uint8Array())))).toEqual({ kind: 'invalid' })
  })

  it('rejects animated, mismatched, duplicated and malformed WebP chunks', () => {
    const plain = bytes(webpLossless), body = plain.subarray(12), wrongCanvas = webpChunk('VP8X', new Uint8Array([0x10, 0, 0, 0, 0, 0, 0, 0, 0, 0]))
    const oversizedLength = plain.slice(); new DataView(oversizedLength.buffer).setUint32(16, 0xffffffff, true)
    const badPadding = plain.slice(); badPadding[badPadding.length - 1] = 1
    const missingEXIF = webpContainer(webpChunk('VP8X', new Uint8Array([0x18, 0, 0, 0, 1, 0, 0, 2, 0, 0])), body)
    for (const invalid of [bytes(webpAnimated), oversizedLength, badPadding, missingEXIF,
      webpContainer(body, body), webpContainer(wrongCanvas, body), webpContainer(body, webpChunk('ANMF', new Uint8Array(2)))]) {
      expect(inspectImageContainer(invalid)).toEqual({ kind: 'invalid' })
    }
  })

  it('handles extended WebP metadata while rejecting inconsistent flags and alpha chunks', () => {
    const body = bytes(webpLossless).subarray(12), tiff = exif(6, true)
    const header = (flags: number) => webpChunk('VP8X', new Uint8Array([flags, 0, 0, 0, 1, 0, 0, 2, 0, 0]))
    const icc = webpChunk('ICCP', ascii('private profile')), xmp = webpChunk('XMP ', ascii('private metadata'))
    const photo = webpContainer(header(0x3c), icc, body, webpChunk('EXIF', tiff), xmp, webpChunk('JUNK', ascii('private')))
    expect(inspectImageContainer(photo)).toEqual(expected('image/webp', 6))
    for (const invalid of [webpContainer(body, icc), webpContainer(header(0x20), body),
      webpContainer(header(0x20), icc, icc, body), webpContainer(header(4), body),
      webpContainer(header(4), body, xmp, xmp), webpContainer(header(0), body, xmp),
      webpContainer(header(0x10), webpChunk('ALPH', new Uint8Array([0, 1])), bytes(webpLossy).subarray(12)),
      webpContainer(header(0x10), webpChunk('ALPH', new Uint8Array([3, 1])), bytes(webpLossy).subarray(12)),
      webpContainer(header(0), webpChunk('ALPH', new Uint8Array([0, 1])), body),
      webpContainer(webpChunk('JUNK', new Uint8Array(2)), body)]) {
      expect(inspectImageContainer(invalid)).toEqual({ kind: 'invalid' })
    }
  })

  it('rejects changing JPEG frame dimensions and malformed scan markers', () => {
    const raw = bytes(jpeg), marker = raw.findIndex((byte, index) => byte === 0xff && raw[index + 1] === 0xc0)
    const frame = raw.subarray(marker, marker + 19)
    const dnl = new Uint8Array([0xff, 0xdc, 0, 4, 0, 1])
    const embedded = (segment: Uint8Array) => concat(raw.subarray(0, marker), segment, raw.subarray(marker))
    const unsupported = raw.slice(); unsupported[marker + 1] = 0xc3
    const noHeight = raw.slice(); noHeight[marker + 5] = 0; noHeight[marker + 6] = 0
    for (const invalid of [embedded(frame), embedded(dnl), unsupported, noHeight,
      concat(raw.subarray(0, 2), new Uint8Array([0xff, 0xda, 0, 2]), raw.subarray(2)),
      concat(raw.subarray(0, raw.length - 2), new Uint8Array([0xff, 0xff, 0, 0xff, 0xd9]))]) {
      expect(inspectImageContainer(invalid)).toEqual({ kind: 'invalid' })
    }
  })

  it('rejects malformed, repeated and out-of-range EXIF orientation instead of guessing', () => {
    const good = exif(1, true), truncated = good.subarray(0, 20), wrongType = good.slice(), duplicate = new Uint8Array(38)
    new DataView(wrongType.buffer).setUint16(12, 4, true)
    duplicate.set(good.subarray(0, 22)); duplicate.set(good.subarray(10, 22), 22); new DataView(duplicate.buffer).setUint16(8, 2, true)
    for (const bad of [truncated, wrongType, duplicate, exif(0, true), exif(9, false)]) {
      for (const raw of [jpegEXIF(bytes(jpeg), bad), pngEXIF(bytes(png), bad), webpEXIF(bytes(webpLossless), bad)]) {
        expect(inspectImageContainer(raw)).toEqual({ kind: 'invalid' })
      }
    }
    expect(inspectImageContainer(jpegEXIF(jpegEXIF(bytes(jpeg), good), good))).toEqual({ kind: 'invalid' })
    expect(inspectImageContainer(pngEXIF(pngEXIF(bytes(png), good), good))).toEqual({ kind: 'invalid' })
    const twice = webpEXIF(bytes(webpLossless), good)
    expect(inspectImageContainer(webpContainer(twice.subarray(12), webpChunk('EXIF', good)))).toEqual({ kind: 'invalid' })
  })

  it('enforces dimension and pixel ceilings before any image decoder runs', () => {
    const raw = bytes(png)
    function sized(width: number, height: number) {
      const header = raw.subarray(16, 29).slice(), view = new DataView(header.buffer)
      view.setUint32(0, width); view.setUint32(4, height)
      return concat(raw.subarray(0, 8), pngChunk('IHDR', header), raw.subarray(33))
    }
    expect(inspectImageContainer(sized(8_000, 8_000))).toEqual({ kind: 'inspected', mime: 'image/png', width: 8_000, height: 8_000, orientation: 1 })
    expect(inspectImageContainer(sized(8_001, 8_000))).toEqual({ kind: 'too-large' })
    expect(inspectImageContainer(sized(16_385, 1))).toEqual({ kind: 'too-large' })
    expect(inspectImageContainer(sized(0, 2))).toEqual({ kind: 'invalid' })
    const largeJPEG = bytes(jpeg), marker = largeJPEG.findIndex((byte, index) => byte === 0xff && largeJPEG[index + 1] === 0xc0)
    expect(marker).toBeGreaterThan(0)
    new DataView(largeJPEG.buffer).setUint16(marker + 7, 16_385)
    expect(inspectImageContainer(largeJPEG)).toEqual({ kind: 'too-large' })
    const largeWebP = bytes(webpLossless)
    largeWebP[21] = 0xff; largeWebP[22] = 0xff; largeWebP[23] = 0xff; largeWebP[24] = 0x0f
    expect(inspectImageContainer(largeWebP)).toEqual({ kind: 'too-large' })
  })

  it('handles bounded deterministic byte mutations and nonzero buffer offsets without throwing', () => {
    let seed = 0x91cf73a5
    const random = () => { seed = (Math.imul(seed, 1664525) + 1013904223) >>> 0; return seed }
    for (const encoded of [jpeg, progressiveJPEG, png, webpLossless, webpLossy, webpAlpha]) {
      const source = bytes(encoded), padded = concat(new Uint8Array(11), source, new Uint8Array(7))
      expect(inspectImageContainer(padded.subarray(11, 11 + source.length))).toEqual(inspectImageContainer(source))
      for (let iteration = 0; iteration < 2_000; iteration++) {
        const candidate = source.slice()
        for (let changed = 0, count = 1 + random() % 4; changed < count; changed++) {
          const index = random() % candidate.length
          candidate[index] = candidate[index]! ^ (random() & 255)
        }
        const before = candidate.slice(), result = inspectImageContainer(candidate)
        expect(candidate).toEqual(before)
        if (result.kind === 'inspected') {
          expect(result.width).toBeGreaterThan(0); expect(result.width).toBeLessThanOrEqual(16_384)
          expect(result.height).toBeGreaterThan(0); expect(result.height).toBeLessThanOrEqual(16_384)
          expect(result.width * result.height).toBeLessThanOrEqual(64_000_000)
          expect(result.orientation).toBeGreaterThanOrEqual(1); expect(result.orientation).toBeLessThanOrEqual(8)
        }
      }
    }
  })
})
