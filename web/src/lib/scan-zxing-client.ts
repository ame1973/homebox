import { maxScanDimension, maxScanPixels, requiredScanFormats, type ScanFormat, type ScannedCode } from './scan-native'
import { validText } from './text-literal'

type Deferred<T> = { resolve: (value: T) => void; reject: (error: Error) => void }
type Pending = Deferred<ScannedCode[]> & { id: number }
const interrupted = () => new DOMException('Scan canceled', 'AbortError')
const failure = () => new Error('Barcode decoder unavailable')

function captureFrame(video: HTMLVideoElement) {
  const stream = video.srcObject as MediaStream | null
  const tracks = typeof stream?.getTracks === 'function' ? stream.getTracks() : []
  const live = () => video.srcObject === stream && document.visibilityState === 'visible' &&
    tracks.some((track) => track.kind === 'video' && track.readyState === 'live') &&
    tracks.every((track) => track.kind === 'video' && track.readyState === 'live')
  const sourceWidth = video.videoWidth, sourceHeight = video.videoHeight
  if (!live() || video.readyState < 2 || !Number.isSafeInteger(sourceWidth) || !Number.isSafeInteger(sourceHeight) ||
    sourceWidth < 1 || sourceHeight < 1 || sourceWidth > 16384 || sourceHeight > 16384 || sourceWidth * sourceHeight > 64000000) throw failure()
  const scale = Math.min(1, maxScanDimension / sourceWidth, maxScanDimension / sourceHeight, Math.sqrt(maxScanPixels / (sourceWidth * sourceHeight)))
  const width = Math.floor(sourceWidth * scale), height = Math.floor(sourceHeight * scale)
  if (width < 32 || height < 32) throw failure()
  const canvas = document.createElement('canvas')
  try {
    canvas.width = width; canvas.height = height
    const context = canvas.getContext('2d', { willReadFrequently: true })
    if (!context) throw failure()
    context.drawImage(video, 0, 0, width, height)
    const rgba = context.getImageData(0, 0, width, height).data
    if (rgba.length !== width * height * 4 || !live()) throw failure()
    const pixels = new Uint8ClampedArray(width * height)
    for (let i = 0, j = 0; j < pixels.length; i += 4, j++) {
      const gray = (306 * rgba[i]! + 601 * rgba[i + 1]! + 117 * rgba[i + 2]! + 512) >> 10
      const alpha = rgba[i + 3]!
      pixels[j] = Math.round((gray * alpha + 255 * (255 - alpha)) / 255)
    }
    return { pixels, width, height }
  } finally { canvas.width = 0; canvas.height = 0 }
}

function validCodes(value: unknown, formats: readonly ScanFormat[]): value is ScannedCode[] {
  if (!Array.isArray(value) || value.length > formats.length) return false
  const seen = new Set<string>()
  return value.every((code: unknown) => {
    if (typeof code !== 'object' || code === null || !('format' in code) || !('rawValue' in code) ||
      typeof code.format !== 'string' || !formats.includes(code.format as ScanFormat) ||
      typeof code.rawValue !== 'string' || !validText(code.rawValue, 1, code.format === 'QR_CODE' ? 4096 : 512) || seen.has(code.format)) return false
    seen.add(code.format)
    return true
  })
}

// A worker can be terminated on every invalidation; native detector promises
// have a separate cross-remount gate because the browser cannot cancel them.
export async function createZXingBarcodeDecoder(formats: readonly ScanFormat[], signal: AbortSignal) {
  if (!Array.isArray(formats) || formats.length < 1 || formats.length > requiredScanFormats.length || new Set(formats).size !== formats.length ||
    formats.some((format) => !requiredScanFormats.includes(format))) throw failure()
  const selected: readonly ScanFormat[] = [...formats]
  let worker: Worker | undefined, startup: Deferred<void> | undefined, pending: Pending | undefined
  let closed = false, busy = false, sequence = 0

  function stop(error: Error) {
    const old = worker; worker = undefined
    old?.terminate()
    const initializing = startup; startup = undefined; initializing?.reject(error)
    const request = pending; pending = undefined; request?.reject(error)
  }
  async function ensureWorker(current: AbortSignal) {
    if (closed || current.aborted) throw interrupted()
    if (worker) return
    let instance: Worker
    try { instance = new Worker(new URL('./scan-zxing-worker.ts', import.meta.url), { type: 'module' }) }
    catch { throw failure() }
    worker = instance
    const ready = new Promise<void>((resolve, reject) => { startup = { resolve, reject } })
    instance.onmessage = ({ data }: MessageEvent<unknown>) => {
      if (worker !== instance) return
      if (typeof data !== 'object' || data === null || !('type' in data)) { stop(failure()); return }
      if (startup && data.type === 'ready') {
        const waiting = startup; startup = undefined; waiting.resolve(); return
      }
      if (!pending || !('id' in data) || data.id !== pending.id || data.type !== 'result' || !('codes' in data) || !validCodes(data.codes, selected)) {
        stop(failure()); return
      }
      const waiting = pending; pending = undefined
      waiting.resolve(data.codes.map(({ format, rawValue }) => ({ format, rawValue })))
    }
    instance.onerror = (event) => { event.preventDefault(); if (worker === instance) stop(failure()) }
    instance.onmessageerror = () => { if (worker === instance) stop(failure()) }
    const abort = () => { if (worker === instance) stop(interrupted()) }
    const timer = setTimeout(() => { if (worker === instance) stop(failure()) }, 8_000)
    current.addEventListener('abort', abort, { once: true })
    if (current.aborted) abort()
    try { await ready } finally { clearTimeout(timer); current.removeEventListener('abort', abort) }
    if (closed || current.aborted || worker !== instance) throw interrupted()
  }
  async function detect(video: HTMLVideoElement, current: AbortSignal): Promise<ScannedCode[]> {
    if (closed || current.aborted) throw interrupted()
    if (busy) throw failure()
    busy = true
    try {
      await ensureWorker(current)
      if (current.aborted || closed || !worker) throw interrupted()
      const instance = worker, frame = captureFrame(video)
      if (current.aborted || closed || worker !== instance) { frame.pixels.fill(0); throw interrupted() }
      if (sequence >= Number.MAX_SAFE_INTEGER) throw failure()
      const id = ++sequence
      const reply = new Promise<ScannedCode[]>((resolve, reject) => { pending = { id, resolve, reject } })
      const abort = () => { if (worker === instance) stop(interrupted()) }
      const timer = setTimeout(() => { if (worker === instance) stop(failure()) }, 8_000)
      current.addEventListener('abort', abort, { once: true })
      if (current.aborted) abort()
      else {
        try { instance.postMessage({ id, ...frame, formats: selected }, [frame.pixels.buffer]) }
        catch { stop(failure()) }
      }
      try {
        const codes = await reply
        if (current.aborted || closed || worker !== instance) throw interrupted()
        return codes
      } finally { clearTimeout(timer); current.removeEventListener('abort', abort) }
    } finally { busy = false }
  }
  function cancel() { stop(interrupted()) }
  function close() { closed = true; stop(interrupted()) }
  try { await ensureWorker(signal) } catch (error) { close(); throw error }
  return { formats: selected, detect, cancel, close }
}
