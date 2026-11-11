import { validText } from './api/client'

const nativeFormats = {
  qr_code: 'QR_CODE', ean_13: 'EAN_13', ean_8: 'EAN_8', upc_a: 'UPC_A', upc_e: 'UPC_E', code_128: 'CODE_128',
} as const

type NativeFormat = keyof typeof nativeFormats
export type ScanFormat = typeof nativeFormats[NativeFormat]
export type ScannedCode = Readonly<{ format: ScanFormat; rawValue: string }>

interface NativeDetector {
  detect(source: ImageBitmapSource): Promise<unknown>
}
interface NativeDetectorConstructor {
  new(options: { formats: NativeFormat[] }): NativeDetector
  getSupportedFormats?: () => Promise<unknown>
}

// Native implementations often support only a subset. Report the actual
// coverage so the caller can use a fallback for every remaining format.
export async function createNativeBarcodeDecoder() {
  try {
    const Detector = (globalThis as { BarcodeDetector?: NativeDetectorConstructor }).BarcodeDetector
    if (typeof Detector !== 'function' || typeof Detector.getSupportedFormats !== 'function') return undefined
    const supported = await Detector.getSupportedFormats()
    if (!Array.isArray(supported)) return undefined
    const selected = (Object.keys(nativeFormats) as NativeFormat[]).filter((format) => supported.includes(format))
    if (selected.length === 0) return undefined
    const detector = new Detector({ formats: selected })
    const allowed = new Set(selected)
    return {
      formats: selected.map((format) => nativeFormats[format]),
      async detect(source: ImageBitmapSource): Promise<ScannedCode[]> {
        const results = await detector.detect(source)
        if (!Array.isArray(results)) throw new Error('Invalid native barcode response')
        const codes: ScannedCode[] = []
        for (const result of results) {
          if (typeof result !== 'object' || result === null || typeof result.format !== 'string' ||
            !Object.hasOwn(nativeFormats, result.format) || !allowed.has(result.format as NativeFormat)) continue
          const format = nativeFormats[result.format as NativeFormat]
          if (typeof result.rawValue === 'string' && validText(result.rawValue, 1, format === 'QR_CODE' ? 4096 : 512)) {
            codes.push({ format, rawValue: result.rawValue })
          }
        }
        return codes
      },
    }
  } catch { return undefined }
}
