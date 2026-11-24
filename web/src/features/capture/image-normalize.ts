import { maxFrameBytes } from '@/lib/capture-frame'
import { subscribeSessionCleared } from '@/lib/session-lifecycle'
import { isPreparedSelectedImage, type PreparedSelectedImage } from './image-prepare-client'
import { acquirePhotoProcessing } from './photo-processing-slot'

export type ImportedFrame = Readonly<{ kind: 'imported'; blob: Blob; width: number; height: number }>
export type ImageNormalizationResult = ImportedFrame | Readonly<{ kind: 'invalid' | 'failed' | 'too-large' | 'cancelled' | 'busy' | 'timeout' }>
const importedFrames = new WeakSet<object>()

// An imported frame is a current-document, canvas-generated image. It is not
// a camera capture, saved draft, server-verified image or upload authorization.
export function isImportedFrame(value: unknown): value is ImportedFrame {
  return typeof value === 'object' && value !== null && importedFrames.has(value)
}

function clearCanvas(canvas: HTMLCanvasElement | undefined) {
  try { if (canvas) { canvas.width = 0; canvas.height = 0 } } catch { /* Clearing is best effort. */ }
}
function closeBitmap(bitmap: ImageBitmap | undefined) {
  try { bitmap?.close() } catch { /* Closing is best effort. */ }
}

// The caller must act only after a current completed-session check, recheck its
// session/generation after awaiting, and discard all Blob references on session
// loss, suspension or departure. Native decoding/encoding cannot be canceled;
// the shared slot stays held until completion and latches on a 30-second hang.
// Canvas re-encoding may change color profiles or high-bit-depth precision;
// JPEG is lossy. The eventual file-selection UI must disclose this beforehand.
export async function normalizeSelectedImage(source: PreparedSelectedImage, signal: AbortSignal): Promise<ImageNormalizationResult> {
  if (signal.aborted) return { kind: 'cancelled' }
  const slot = acquirePhotoProcessing()
  if (slot.kind !== 'held') return { kind: slot.kind }
  if (document.visibilityState !== 'visible') { slot.release(); return { kind: 'cancelled' } }
  if (!isPreparedSelectedImage(source)) { slot.release(); return { kind: 'invalid' } }
  if (typeof createImageBitmap !== 'function') { slot.release(); return { kind: 'failed' } }
  const { width: originalWidth, height: originalHeight, orientation, mime } = source
  const width = orientation >= 5 ? originalHeight : originalWidth, height = orientation >= 5 ? originalWidth : originalHeight
  const output = mime === 'image/jpeg' ? 'image/jpeg' : 'image/png'

  return await new Promise<ImageNormalizationResult>((resolve) => {
    let finished = false, canvas: HTMLCanvasElement | undefined
    let unsubscribe = () => {}
    const finish = (result: ImageNormalizationResult) => {
      if (finished) return
      finished = true
      signal.removeEventListener('abort', interrupted)
      document.removeEventListener('visibilitychange', visibility)
      window.removeEventListener('pagehide', interrupted)
      try { unsubscribe() } catch { /* Discarding does not depend on signaling. */ }
      clearCanvas(canvas)
      resolve(result)
    }
    const interrupted = () => { finish({ kind: 'cancelled' }) }
    const visibility = () => { if (document.visibilityState !== 'visible') interrupted() }
    signal.addEventListener('abort', interrupted, { once: true })
    document.addEventListener('visibilitychange', visibility)
    window.addEventListener('pagehide', interrupted)
    unsubscribe = subscribeSessionCleared(interrupted)
    const deadline = setTimeout(() => { slot.timeout(); finish({ kind: 'timeout' }) }, 30_000)
    const release = () => {
      if (!slot.release()) return false
      clearTimeout(deadline)
      return true
    }
    const valid = () => !finished && !signal.aborted && document.visibilityState === 'visible'
    async function normalize() {
      let bitmap: ImageBitmap | undefined
      try {
        if (!valid()) { release(); finish({ kind: 'cancelled' }); return }
        // The worker removed orientation metadata; no browser-specific implicit
        // rotation is needed before applying the original TIFF transform below.
        bitmap = await createImageBitmap(source.blob, { imageOrientation: 'none' })
        if (!valid()) { release(); finish({ kind: 'cancelled' }); return }
        if (bitmap.width !== originalWidth || bitmap.height !== originalHeight) { release(); finish({ kind: 'invalid' }); return }
        canvas = document.createElement('canvas')
        canvas.width = width; canvas.height = height
        if (canvas.width !== width || canvas.height !== height) throw new Error('Canvas dimensions unavailable')
        const context = canvas.getContext('2d', { alpha: output === 'image/png' })
        if (!context || typeof canvas.toBlob !== 'function') throw new Error('Canvas unavailable')
        context.imageSmoothingEnabled = false
        if (orientation === 1) context.setTransform(1, 0, 0, 1, 0, 0)
        else if (orientation === 2) context.setTransform(-1, 0, 0, 1, originalWidth, 0)
        else if (orientation === 3) context.setTransform(-1, 0, 0, -1, originalWidth, originalHeight)
        else if (orientation === 4) context.setTransform(1, 0, 0, -1, 0, originalHeight)
        else if (orientation === 5) context.setTransform(0, 1, 1, 0, 0, 0)
        else if (orientation === 6) context.setTransform(0, 1, -1, 0, originalHeight, 0)
        else if (orientation === 7) context.setTransform(0, -1, -1, 0, originalHeight, originalWidth)
        else context.setTransform(0, -1, 1, 0, 0, originalWidth)
        context.drawImage(bitmap, 0, 0)
        closeBitmap(bitmap); bitmap = undefined
        if (!valid()) { release(); finish({ kind: 'cancelled' }); return }
        const encoded = (blob: Blob | null) => {
          if (!release() || finished) return
          if (!valid()) { finish({ kind: 'cancelled' }); return }
          if (!blob || blob.type !== output || !Number.isSafeInteger(blob.size) || blob.size < 1) { finish({ kind: 'failed' }); return }
          if (blob.size > maxFrameBytes) { finish({ kind: 'too-large' }); return }
          const frame: ImportedFrame = Object.freeze({ kind: 'imported', blob, width, height })
          importedFrames.add(frame)
          finish(frame)
        }
        if (output === 'image/jpeg') canvas.toBlob(encoded, output, 0.9)
        else canvas.toBlob(encoded, output)
      } catch { release(); finish({ kind: 'failed' }) }
      finally { closeBitmap(bitmap) }
    }
    void normalize()
  })
}
