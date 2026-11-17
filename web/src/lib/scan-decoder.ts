import { createNativeBarcodeDecoder, requiredScanFormats, type ScannedCode } from './scan-native'
import type { createZXingBarcodeDecoder } from './scan-zxing-client'

type Fallback = Awaited<ReturnType<typeof createZXingBarcodeDecoder>>
const interrupted = () => new DOMException('Scan canceled', 'AbortError')

// Discover native coverage first and load ZXing only for missing formats. A
// fallback failure never claims unsupported formats or replaces native results.
export async function createScanDecoder(signal: AbortSignal) {
  if (signal.aborted) throw interrupted()
  const native = await createNativeBarcodeDecoder()
  if (signal.aborted) throw interrupted()
  const nativeFormats = native?.formats ?? []
  const missing = requiredScanFormats.filter((format) => !nativeFormats.includes(format))
  let fallback: Fallback | undefined, fallbackUnavailable = false
  if (missing.length && typeof Worker !== 'function') fallbackUnavailable = true
  else if (missing.length) {
    try {
      const { createZXingBarcodeDecoder } = await import('./scan-zxing-client')
      if (signal.aborted) throw interrupted()
      fallback = await createZXingBarcodeDecoder(missing, signal)
      if (signal.aborted) throw interrupted()
    } catch {
      fallback?.close()
      if (signal.aborted) throw interrupted()
      fallbackUnavailable = true
    }
  }
  if (!native && !fallback) return undefined
  const fallbackFormats = fallback?.formats ?? []
  const formats = requiredScanFormats.filter((format) => nativeFormats.includes(format) || fallbackFormats.includes(format))
  return {
    formats, nativeFormats, fallbackFormats, fallbackUnavailable,
    async detect(video: HTMLVideoElement, current: AbortSignal): Promise<ScannedCode[]> {
      if (current.aborted) throw interrupted()
      const codes = native ? await native.detect(video) : []
      if (current.aborted) throw interrupted()
      const extra = fallback ? await fallback.detect(video, current) : []
      if (current.aborted) throw interrupted()
      return [...codes, ...extra]
    },
    cancel() { fallback?.cancel() },
    close() { fallback?.close() },
  }
}
