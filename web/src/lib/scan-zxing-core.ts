import BarcodeFormat from '@zxing/library/esm/core/BarcodeFormat'
import BinaryBitmap from '@zxing/library/esm/core/BinaryBitmap'
import ChecksumException from '@zxing/library/esm/core/ChecksumException'
import FormatException from '@zxing/library/esm/core/FormatException'
import NotFoundException from '@zxing/library/esm/core/NotFoundException'
import ReaderException from '@zxing/library/esm/core/ReaderException'
import RGBLuminanceSource from '@zxing/library/esm/core/RGBLuminanceSource'
import HybridBinarizer from '@zxing/library/esm/core/common/HybridBinarizer'
import Code128Reader from '@zxing/library/esm/core/oned/Code128Reader'
import EAN13Reader from '@zxing/library/esm/core/oned/EAN13Reader'
import EAN8Reader from '@zxing/library/esm/core/oned/EAN8Reader'
import UPCAReader from '@zxing/library/esm/core/oned/UPCAReader'
import QRCodeReader from '@zxing/library/esm/core/qrcode/QRCodeReader'
import type Reader from '@zxing/library/esm/core/Reader'
import { maxScanDimension, maxScanPixels, type ScanFormat, type ScannedCode } from './scan-native'
import { validText } from './text-literal'
import { UPCEBarcodeReader } from './scan-upce-reader'

const formats: Record<ScanFormat, { value: BarcodeFormat; create: () => Reader }> = {
  QR_CODE: { value: BarcodeFormat.QR_CODE, create: () => new QRCodeReader() },
  EAN_13: { value: BarcodeFormat.EAN_13, create: () => new EAN13Reader() },
  EAN_8: { value: BarcodeFormat.EAN_8, create: () => new EAN8Reader() },
  UPC_A: { value: BarcodeFormat.UPC_A, create: () => new UPCAReader() },
  UPC_E: { value: BarcodeFormat.UPC_E, create: () => new UPCEBarcodeReader() },
  CODE_128: { value: BarcodeFormat.CODE_128, create: () => new Code128Reader() },
}

// This runs in a dedicated worker: ZXing decoding is synchronous and cannot
// otherwise be stopped if a malformed frame or decoder stalls the main thread.
export function decodeZXing(pixels: Uint8ClampedArray, width: number, height: number, selected: readonly ScanFormat[]): ScannedCode[] {
  if (!(pixels instanceof Uint8ClampedArray) || !Number.isSafeInteger(width) || !Number.isSafeInteger(height) ||
    width < 32 || height < 32 || width > maxScanDimension || height > maxScanDimension || width * height > maxScanPixels ||
    pixels.length !== width * height || !Array.isArray(selected) || selected.length < 1 || selected.length > 6 ||
    new Set(selected).size !== selected.length || selected.some((format) => !Object.hasOwn(formats, format))) throw new Error('Invalid scan frame')

  const readers = selected.map((format: ScanFormat) => ({ format, ...formats[format], reader: formats[format].create() }))
  function read(source: Uint8ClampedArray, w: number, h: number, requested: typeof readers): ScannedCode[] {
    const bitmap = new BinaryBitmap(new HybridBinarizer(new RGBLuminanceSource(source, w, h)))
    const result: ScannedCode[] = []
    for (const { format, value, reader } of requested) {
      try {
        const decoded = reader.decode(bitmap)
        const rawValue = decoded.getText()
        if (decoded.getBarcodeFormat() === value && typeof rawValue === 'string' && validText(rawValue, 1, format === 'QR_CODE' ? 4096 : 512)) result.push({ format, rawValue })
      } catch (error) {
        if (!(error instanceof NotFoundException || error instanceof ChecksumException || error instanceof FormatException || error instanceof ReaderException)) throw error
      } finally { reader.reset() }
    }
    return result
  }
  const found = read(pixels, width, height, readers)
  if (found.length || readers.every(({ format }) => format === 'QR_CODE')) return found
  const rotated = new Uint8ClampedArray(pixels.length)
  for (let y = 0; y < height; y++) for (let x = 0; x < width; x++) rotated[(width - 1 - x) * height + y] = pixels[y * width + x]!
  return read(rotated, height, width, readers.filter(({ format }) => format !== 'QR_CODE'))
}
