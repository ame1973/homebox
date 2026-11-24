import { isCapturedFrame, maxFrameBytes, type CapturedFrame } from '@/lib/capture-frame'
import { subscribeSessionCleared } from '@/lib/session-lifecycle'
import { photoDisplaySize, type PhotoGeometry } from './geometry'
import { acquirePhotoProcessing } from './photo-processing-slot'

export type PhotoRenderResult =
  | Readonly<{ kind: 'rendered'; blob: Blob; width: number; height: number }>
  | Readonly<{ kind: 'invalid' | 'failed' | 'too-large' | 'cancelled' | 'busy' | 'timeout' }>

function clearCanvas(canvas: HTMLCanvasElement | undefined) {
  try { if (canvas) { canvas.width = 0; canvas.height = 0 } } catch { /* Discarded pixels are never returned. */ }
}
function closeBitmap(bitmap: ImageBitmap | undefined) {
  try { bitmap?.close() } catch { /* Closing is best effort after discarding. */ }
}

// Call after a completed-session check and an explicit action. The caller must
// recheck its own session/generation after awaiting, clear any returned Blob on
// suspension or departure, and retain the original capture for later edits.
// This does not accept selected files, persist drafts, upload or sanitize bytes.
export async function renderCapturedPhoto(frame: CapturedFrame, value: PhotoGeometry, signal: AbortSignal): Promise<PhotoRenderResult> {
  if (signal.aborted) return { kind: 'cancelled' }
  const slot = acquirePhotoProcessing()
  if (slot.kind !== 'held') return { kind: slot.kind }
  if (document.visibilityState !== 'visible') { slot.release(); return { kind: 'cancelled' } }
  if (!isCapturedFrame(frame)) { slot.release(); return { kind: 'invalid' } }
  let geometry: PhotoGeometry
  try {
    geometry = { sourceWidth: value.sourceWidth, sourceHeight: value.sourceHeight, crop: { ...value.crop }, turns: value.turns }
  } catch { slot.release(); return { kind: 'invalid' } }
  const size = photoDisplaySize(geometry)
  if (!size || frame.width !== geometry.sourceWidth || frame.height !== geometry.sourceHeight) { slot.release(); return { kind: 'invalid' } }
  const { width, height } = size
  if (typeof createImageBitmap !== 'function') { slot.release(); return { kind: 'failed' } }

  return await new Promise<PhotoRenderResult>((resolve) => {
    let finished = false, canvas: HTMLCanvasElement | undefined
    let unsubscribe = () => {}
    const finish = (result: PhotoRenderResult) => {
      if (finished) return
      finished = true
      signal.removeEventListener('abort', interrupted)
      document.removeEventListener('visibilitychange', visibility)
      window.removeEventListener('pagehide', interrupted)
      unsubscribe()
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
    async function render() {
      let bitmap: ImageBitmap | undefined
      try {
        if (!valid()) { release(); finish({ kind: 'cancelled' }); return }
        // The source was produced by canvas capture, so its pixels already
        // have the intended orientation. Ignore any image orientation metadata.
        bitmap = await createImageBitmap(frame.blob, { imageOrientation: 'none' })
        if (!valid()) { release(); finish({ kind: 'cancelled' }); return }
        if (bitmap.width !== frame.width || bitmap.height !== frame.height) { release(); finish({ kind: 'invalid' }); return }
        canvas = document.createElement('canvas')
        canvas.width = width; canvas.height = height
        if (canvas.width !== width || canvas.height !== height) throw new Error('Canvas dimensions unavailable')
        const context = canvas.getContext('2d', { alpha: false })
        if (!context || typeof canvas.toBlob !== 'function') throw new Error('Canvas unavailable')
        context.imageSmoothingEnabled = false
        const { crop, turns } = geometry
        if (turns === 0) context.setTransform(1, 0, 0, 1, 0, 0)
        else if (turns === 1) context.setTransform(0, 1, -1, 0, crop.height, 0)
        else if (turns === 2) context.setTransform(-1, 0, 0, -1, crop.width, crop.height)
        else context.setTransform(0, -1, 1, 0, 0, crop.width)
        context.drawImage(bitmap, crop.x, crop.y, crop.width, crop.height, 0, 0, crop.width, crop.height)
        closeBitmap(bitmap); bitmap = undefined
        if (!valid()) { release(); finish({ kind: 'cancelled' }); return }
        canvas.toBlob((blob) => {
          if (!release() || finished) return
          if (!valid()) { finish({ kind: 'cancelled' }); return }
          if (!blob || blob.type !== 'image/jpeg' || !blob.size) { finish({ kind: 'failed' }); return }
          if (blob.size > maxFrameBytes) { finish({ kind: 'too-large' }); return }
          finish({ kind: 'rendered', blob, width, height })
        }, 'image/jpeg', 0.9)
      } catch {
        release(); finish({ kind: 'failed' })
      } finally { closeBitmap(bitmap) }
    }
    void render()
  })
}
