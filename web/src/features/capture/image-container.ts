import { maxFrameBytes, maxFrameDimension, maxFramePixels } from '@/lib/capture-frame'

type ImageMIME = 'image/jpeg' | 'image/png' | 'image/webp'
type ImageInfo = Readonly<{ mime: ImageMIME; width: number; height: number; orientation: number }>
export type ImageContainerResult = Readonly<{ kind: 'inspected' } & ImageInfo> | Readonly<{ kind: 'invalid' | 'unsupported' | 'too-large' }>

const pngSignature = new Uint8Array([137, 80, 78, 71, 13, 10, 26, 10])
const crcTable = Uint32Array.from({ length: 256 }, (_, byte) => {
  let value = byte
  for (let bit = 0; bit < 8; bit++) value = value >>> 1 ^ (value & 1 ? 0xedb88320 : 0)
  return value >>> 0
})

function textAt(raw: Uint8Array, offset: number, text: string): boolean {
  if (offset < 0 || text.length > raw.length - offset) return false
  for (let i = 0; i < text.length; i++) if (raw[offset + i] !== text.charCodeAt(i)) return false
  return true
}
function viewOf(raw: Uint8Array): DataView { return new DataView(raw.buffer, raw.byteOffset, raw.byteLength) }
function checksum(raw: Uint8Array): number {
  let value = 0xffffffff
  for (const byte of raw) value = value >>> 8 ^ crcTable[(value ^ byte) & 255]!
  return (value ^ 0xffffffff) >>> 0
}

// Read only the inline IFD0 Orientation. The remaining EXIF, including GPS,
// stays in the untrusted source and is not an image-safety or privacy proof.
function exifOrientation(raw: Uint8Array): number | undefined {
  if (raw.length < 8) return undefined
  const little = textAt(raw, 0, 'II')
  if (!little && !textAt(raw, 0, 'MM')) return undefined
  const view = viewOf(raw)
  if (view.getUint16(2, little) !== 42) return undefined
  const offset = view.getUint32(4, little)
  if (offset < 8 || offset + 2 > raw.length) return undefined
  const count = view.getUint16(offset, little), start = offset + 2
  if (start + count * 12 + 4 > raw.length) return undefined
  let result = 1, found = false
  for (let index = 0; index < count; index++) {
    const pos = start + index * 12
    if (view.getUint16(pos, little) !== 0x112) continue
    if (found || view.getUint16(pos + 2, little) !== 3 || view.getUint32(pos + 4, little) !== 1) return undefined
    result = view.getUint16(pos + 8, little); found = true
    if (result < 1 || result > 8) return undefined
  }
  return result
}

function jpegInfo(raw: Uint8Array): ImageInfo | undefined {
  let pos = 2, orientation = 1, exif = false, scan = false, scanning = false, width = 0, height = 0
  while (pos < raw.length) {
    if (scanning) {
      while (true) {
        const start = raw.indexOf(0xff, pos)
        if (start < 0) return undefined
        pos = start
        while (pos < raw.length && raw[pos] === 0xff) pos++
        if (pos === raw.length) return undefined
        const marker = raw[pos++]!
        if (marker === 0) { if (pos - start !== 2) return undefined; continue }
        if (marker >= 0xd0 && marker <= 0xd7) continue
        pos = start; scanning = false; break
      }
    }
    if (raw[pos] !== 0xff) return undefined
    while (pos < raw.length && raw[pos] === 0xff) pos++
    if (pos === raw.length) return undefined
    const marker = raw[pos++]!
    if (marker === 0xd9) return scan && width && height && pos === raw.length ? { mime: 'image/jpeg', width, height, orientation } : undefined
    if (marker === 0 || marker === 1 || marker === 0xd8 || marker === 0xdc || marker >= 0xd0 && marker <= 0xd7 || pos + 2 > raw.length) return undefined
    const length = viewOf(raw).getUint16(pos)
    if (length < 2 || length > raw.length - pos) return undefined
    const segment = raw.subarray(pos + 2, pos + length)
    pos += length
    const sof = marker >= 0xc0 && marker <= 0xcf && marker !== 0xc4 && marker !== 0xc8 && marker !== 0xcc
    if (sof) {
      if (width || scan || marker > 0xc2 || segment.length < 6 || segment[0] !== 8) return undefined
      const components = segment[5]!
      if (components !== 1 && components !== 3 && components !== 4 || segment.length !== 6 + 3 * components) return undefined
      const view = viewOf(segment)
      height = view.getUint16(1); width = view.getUint16(3)
      if (!width || !height) return undefined
    }
    if (marker === 0xe1 && textAt(segment, 0, 'Exif\0\0')) {
      if (exif) return undefined
      const value = exifOrientation(segment.subarray(6))
      if (value === undefined) return undefined
      exif = true; orientation = value
    }
    if (marker === 0xda) {
      if (!width || !height) return undefined
      scan = true; scanning = true
    }
  }
  return undefined
}

function pngInfo(raw: Uint8Array): ImageInfo | undefined {
  const view = viewOf(raw)
  let pos = 8, width = 0, height = 0, orientation = 1, color = -1
  let exif = false, palette = false, image = false, imageEnded = false
  while (pos < raw.length) {
    if (raw.length - pos < 12) return undefined
    const length = view.getUint32(pos), start = pos + 8
    if (length > raw.length - start - 4) return undefined
    const end = start + length, name = raw.subarray(pos + 4, start)
    if (name.some((c) => !(c >= 65 && c <= 90 || c >= 97 && c <= 122)) || name[2]! < 65 || name[2]! > 90 ||
      checksum(raw.subarray(pos + 4, end)) !== view.getUint32(end)) return undefined
    const label = String.fromCharCode(...name), data = raw.subarray(start, end)
    if (pos === 8 && label !== 'IHDR' || pos !== 8 && label === 'IHDR') return undefined
    if (image && label !== 'IDAT') imageEnded = true
    switch (label) {
      case 'IHDR': {
        if (length !== 13) return undefined
        width = view.getUint32(start); height = view.getUint32(start + 4); color = data[9]!
        const depth = data[8]!, validDepth = color === 0 ? [1, 2, 4, 8, 16] : color === 3 ? [1, 2, 4, 8] : color === 2 || color === 4 || color === 6 ? [8, 16] : []
        if (!width || !height || !validDepth.includes(depth) || data[10] !== 0 || data[11] !== 0 || data[12]! > 1) return undefined
        break
      }
      case 'PLTE':
        if (palette || image || length < 3 || length > 768 || length % 3 !== 0 || color === 0 || color === 4) return undefined
        palette = true; break
      case 'IDAT':
        if (imageEnded || color === 3 && !palette) return undefined
        image = true; break
      case 'IEND':
        return length === 0 && image && end + 4 === raw.length ? { mime: 'image/png', width, height, orientation } : undefined
      case 'acTL': case 'fcTL': case 'fdAT': return undefined
      case 'eXIf': {
        if (exif) return undefined
        const value = exifOrientation(data)
        if (value === undefined) return undefined
        exif = true; orientation = value; break
      }
      default:
        if (name[0]! >= 65 && name[0]! <= 90) return undefined
    }
    pos = end + 4
  }
  return undefined
}

function webpInfo(raw: Uint8Array): ImageInfo | undefined {
  if (raw.length < 12 || !textAt(raw, 8, 'WEBP')) return undefined
  const view = viewOf(raw)
  if (view.getUint32(4, true) !== raw.length - 8) return undefined
  let pos = 12, flags = 0, canvasWidth = 0, canvasHeight = 0, width = 0, height = 0, orientation = 1
  let extended = false, image = false, alpha = false, exif = false, icc = false, xmp = false, imageKind = ''
  while (pos < raw.length) {
    if (raw.length - pos < 8) return undefined
    const name = String.fromCharCode(...raw.subarray(pos, pos + 4)), length = view.getUint32(pos + 4, true), start = pos + 8
    if (length > raw.length - start) return undefined
    const end = start + length, next = end + (length & 1), data = raw.subarray(start, end)
    if (next > raw.length || next !== end && raw[end] !== 0) return undefined
    switch (name) {
      case 'VP8X':
        if (extended || pos !== 12 || length !== 10 || data[0]! & 0xc3 || data[1] !== 0 || data[2] !== 0 || data[3] !== 0) return undefined
        extended = true; flags = data[0]!
        canvasWidth = 1 + data[4]! + data[5]! * 256 + data[6]! * 65536
        canvasHeight = 1 + data[7]! + data[8]! * 256 + data[9]! * 65536
        if (canvasWidth * canvasHeight > 0xffffffff) return undefined
        break
      case 'ANIM': case 'ANMF': return undefined
      case 'VP8 ': case 'VP8L': {
        if (image || alpha && name !== 'VP8 ' || extended && (flags & 0x10) !== 0 && name === 'VP8 ' && !alpha) return undefined
        if (name === 'VP8 ') {
          if (length < 10 || data[0]! & 1 || data[3] !== 0x9d || data[4] !== 1 || data[5] !== 0x2a) return undefined
          const frame = viewOf(data)
          width = frame.getUint16(6, true) & 0x3fff; height = frame.getUint16(8, true) & 0x3fff
        } else {
          if (length < 5 || data[0] !== 0x2f || data[4]! & 0xe0) return undefined
          width = 1 + data[1]! + (data[2]! & 0x3f) * 256
          height = 1 + (data[2]! >>> 6) + data[3]! * 4 + (data[4]! & 15) * 1024
        }
        if (!width || !height || extended && (width !== canvasWidth || height !== canvasHeight)) return undefined
        image = true; imageKind = name; break
      }
      case 'ALPH': {
        if (!extended || !(flags & 0x10) || image || alpha || length < 2) return undefined
        const compression = data[0]! & 3, preprocessing = data[0]! >>> 4 & 3
        if (data[0]! & 0xc0 || compression > 1 || preprocessing > 1 || compression === 0 && length - 1 !== canvasWidth * canvasHeight) return undefined
        alpha = true; break
      }
      case 'ICCP':
        if (!extended || !(flags & 0x20) || icc || image || alpha) return undefined
        icc = true; break
      case 'EXIF': {
        if (!extended || !(flags & 8) || exif) return undefined
        const value = exifOrientation(textAt(data, 0, 'Exif\0\0') ? data.subarray(6) : data)
        if (value === undefined) return undefined
        exif = true; orientation = value; break
      }
      case 'XMP ':
        if (!extended || !(flags & 4) || xmp) return undefined
        xmp = true; break
      default:
        if (!image && !extended) return undefined
    }
    pos = next
  }
  if (!image || alpha && imageKind !== 'VP8 ' || extended &&
    (!!(flags & 0x20) !== icc || !!(flags & 8) !== exif || !!(flags & 4) !== xmp)) return undefined
  return { mime: 'image/webp', width, height, orientation }
}

// This is a synchronous, bounded container preflight, not a complete decoder,
// sanitizer, trusted photo or upload. Run it in a terminable worker over an
// exclusively owned immutable byte snapshot, after bounding the file size and
// before invoking a native decoder. Dimensions alone do not bound the cost of
// decoding compressed metadata or pixels. The caller must remove unneeded source
// metadata before decoding, validate the decoded dimensions, apply orientation,
// preserve alpha, and discard results on session loss or page suspension.
export function inspectImageContainer(raw: Uint8Array): ImageContainerResult {
  if (!(raw instanceof Uint8Array) || !raw.length) return { kind: 'invalid' }
  if (raw.length > maxFrameBytes) return { kind: 'too-large' }
  let info: ImageInfo | undefined
  if (raw[0] === 0xff && raw[1] === 0xd8) info = jpegInfo(raw)
  else if (pngSignature.every((value, index) => raw[index] === value)) info = pngInfo(raw)
  else if (textAt(raw, 0, 'RIFF')) info = webpInfo(raw)
  else return { kind: 'unsupported' }
  if (!info) return { kind: 'invalid' }
  if (info.width > maxFrameDimension || info.height > maxFrameDimension || info.width * info.height > maxFramePixels) return { kind: 'too-large' }
  return { kind: 'inspected', ...info }
}
