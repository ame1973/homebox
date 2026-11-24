import { inspectImageContainer, type ImageContainerResult } from './image-container'

type ImageInfo = Extract<ImageContainerResult, { kind: 'inspected' }>
export type ImageDecodeInput = Readonly<Omit<ImageInfo, 'kind'> & { kind: 'prepared'; bytes: Uint8Array<ArrayBuffer> }> |
  Extract<ImageContainerResult, { kind: 'invalid' | 'unsupported' | 'too-large' }>

function textAt(raw: Uint8Array, offset: number, text: string): boolean {
  if (text.length > raw.length - offset) return false
  for (let i = 0; i < text.length; i++) if (raw[offset + i] !== text.charCodeAt(i)) return false
  return true
}

// The container has already passed inspection over the same owned buffer.
// Keep frame/scan segments and only the minimal JFIF/Adobe color headers.
function jpegDecodeInput(raw: Uint8Array, out: Uint8Array<ArrayBuffer>): Uint8Array<ArrayBuffer> {
  const view = new DataView(raw.buffer, raw.byteOffset, raw.byteLength)
  let pos = 2, written = 2, scanning = false
  out.set(raw.subarray(0, 2))
  while (pos < raw.length) {
    if (scanning) {
      const scanStart = pos
      while (true) {
        const markerStart = raw.indexOf(0xff, pos)
        pos = markerStart
        while (raw[pos] === 0xff) pos++
        const marker = raw[pos++]!
        if (marker === 0 || marker >= 0xd0 && marker <= 0xd7) continue
        out.set(raw.subarray(scanStart, markerStart), written); written += markerStart - scanStart
        pos = markerStart; scanning = false; break
      }
    }
    const start = pos
    while (raw[pos] === 0xff) pos++
    const marker = raw[pos++]!
    if (marker === 0xd9) {
      out.set(raw.subarray(start, pos), written); written += pos - start
      break
    }
    const length = view.getUint16(pos), body = pos + 2, end = pos + length
    if (marker === 0xe0 && length >= 16 && textAt(raw, body, 'JFIF\0')) {
      out.set([0xff, 0xe0, 0, 16], written); written += 4
      out.set(raw.subarray(body, body + 12), written); written += 12
      out.set([0, 0], written); written += 2
    } else if (marker === 0xee && length >= 14 && textAt(raw, body, 'Adobe')) {
      out.set([0xff, 0xee, 0, 14], written); written += 4
      out.set(raw.subarray(body, body + 12), written); written += 12
    } else if ((marker < 0xe0 || marker > 0xef) && marker !== 0xfe) {
      out.set(raw.subarray(start, end), written); written += end - start
    }
    pos = end
    if (marker === 0xda) scanning = true
  }
  return out.subarray(0, written)
}

// Retain only critical pixel chunks and validated tRNS transparency. In
// particular, compressed color profiles never reach the native decoder.
function pngDecodeInput(raw: Uint8Array, out: Uint8Array<ArrayBuffer>): Uint8Array<ArrayBuffer> | undefined {
  const view = new DataView(raw.buffer, raw.byteOffset, raw.byteLength)
  const depth = raw[24]!, color = raw[25]!
  let pos = 8, written = 8, paletteEntries = 0, image = false, transparency = false
  out.set(raw.subarray(0, 8))
  while (pos < raw.length) {
    const length = view.getUint32(pos), end = pos + 12 + length
    const name = String.fromCharCode(...raw.subarray(pos + 4, pos + 8))
    if (name === 'PLTE') {
      paletteEntries = length / 3
      if (color === 3 && paletteEntries > 2 ** depth) return undefined
    }
    if (name === 'IDAT') image = true
    if (name === 'tRNS') {
      if (transparency || image || color === 0 && length !== 2 || color === 2 && length !== 6 ||
        color === 3 && (!paletteEntries || length === 0 || length > paletteEntries) || color === 4 || color === 6) return undefined
      transparency = true
    }
    if (name === 'IHDR' || name === 'PLTE' || name === 'IDAT' || name === 'IEND' || name === 'tRNS') {
      out.set(raw.subarray(pos, end), written); written += end - pos
    }
    pos = end
  }
  return out.subarray(0, written)
}

// VP8X keeps its alpha flag but no longer advertises removed metadata chunks.
function webpDecodeInput(raw: Uint8Array, out: Uint8Array<ArrayBuffer>): Uint8Array<ArrayBuffer> {
  const input = new DataView(raw.buffer, raw.byteOffset, raw.byteLength), output = new DataView(out.buffer)
  let pos = 12, written = 12
  out.set(raw.subarray(0, 12))
  while (pos < raw.length) {
    const length = input.getUint32(pos + 4, true), end = pos + 8 + length + (length & 1)
    const name = String.fromCharCode(...raw.subarray(pos, pos + 4))
    if (name === 'VP8X' || name === 'VP8 ' || name === 'VP8L' || name === 'ALPH') {
      out.set(raw.subarray(pos, end), written)
      if (name === 'VP8X') out[written + 8] = out[written + 8]! & 0x10
      written += end - pos
    }
    pos = end
  }
  output.setUint32(4, written - 8, true)
  return out.subarray(0, written)
}

// This minimizes native-decoder input; it is not a complete decoder, sanitizer,
// safe resource guarantee or trusted photo. Transfer an exclusively owned
// immutable snapshot to a terminable worker, after bounding the File before it
// is read. Do not decode or expose the original bytes. Decode this intermediate,
// check actual dimensions, apply all EXIF orientation transforms, preserve alpha
// and re-encode pixels before display or upload. Compressed pixels and the two
// minimal JPEG color headers remain untrusted. Discard results on cancellation,
// page suspension or session loss; JavaScript cleanup is not memory erasure.
export function prepareImageDecodeInput(raw: Uint8Array): ImageDecodeInput {
  if (!(raw instanceof Uint8Array) || !(raw.buffer instanceof ArrayBuffer)) return { kind: 'invalid' }
  const inspected = inspectImageContainer(raw)
  if (inspected.kind !== 'inspected') return inspected
  const out = new Uint8Array(raw.length)
  const bytes = inspected.mime === 'image/jpeg' ? jpegDecodeInput(raw, out)
    : inspected.mime === 'image/png' ? pngDecodeInput(raw, out) : webpDecodeInput(raw, out)
  if (!bytes) return { kind: 'invalid' }
  const { mime, width, height, orientation } = inspected
  return { kind: 'prepared', mime, width, height, orientation, bytes }
}
