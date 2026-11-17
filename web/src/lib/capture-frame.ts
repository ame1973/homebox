import { subscribeSessionCleared } from './session-lifecycle'

export const maxFrameDimension = 16_384
export const maxFramePixels = 64_000_000
export const maxFrameBytes = 32 * 1024 * 1024

export type CapturedFrame = Readonly<{ kind: 'captured'; blob: Blob; width: number; height: number }>
export type FrameCaptureResult = CapturedFrame | Readonly<{ kind: 'not-ready' | 'too-large' | 'failed' | 'cancelled' | 'busy' | 'timeout' }>

// Canvas encoding cannot be canceled. Retain one document-wide slot across
// route remounts, and require a reload if the native callback never returns.
let encodingBusy = false, encodingTimedOut = false
const capturedFrames = new WeakSet<object>()

// A canvas-generated JPEG already has its video orientation baked into pixels.
// Keep the original immutable wrapper in memory; untrusted files and restored
// drafts need a separate, validated import path.
export function isCapturedFrame(value: unknown): value is CapturedFrame {
  return typeof value === 'object' && value !== null && capturedFrames.has(value)
}

function currentVideo(video: HTMLVideoElement, stream: MediaStream): boolean {
  try {
    const tracks = stream.getVideoTracks()
    return document.visibilityState === 'visible' && video.srcObject === stream && video.readyState >= 2 &&
      tracks.length > 0 && tracks.every((track) => track.readyState === 'live') && stream.getAudioTracks().length === 0
  } catch { return false }
}
function clearCanvas(canvas: HTMLCanvasElement) {
  try { canvas.width = 0; canvas.height = 0 } catch { /* A discarded frame is never returned. */ }
}

// Call only after an explicit user action and a completed-session check. The
// caller owns the signal, must recheck its session/generation after awaiting,
// and clear returned Blobs on session loss, suspension or route departure.
// This helper does not upload data.
export async function captureVideoFrame(video: HTMLVideoElement, stream: MediaStream, signal: AbortSignal): Promise<FrameCaptureResult> {
  if (signal.aborted) return { kind: 'cancelled' }
  if (encodingTimedOut) return { kind: 'timeout' }
  if (encodingBusy) return { kind: 'busy' }
  if (!currentVideo(video, stream)) return { kind: 'not-ready' }

  let frameCanvas: HTMLCanvasElement | undefined, context: CanvasRenderingContext2D, width: number, height: number
  try {
    width = video.videoWidth; height = video.videoHeight
    if (width === 0 || height === 0) return { kind: 'not-ready' }
    if (!Number.isSafeInteger(width) || !Number.isSafeInteger(height) || width < 0 || height < 0 ||
      width > maxFrameDimension || height > maxFrameDimension || width * height > maxFramePixels) return { kind: 'too-large' }
    frameCanvas = document.createElement('canvas')
    frameCanvas.width = width; frameCanvas.height = height
    const available = frameCanvas.getContext('2d', { alpha: false })
    if (!available || typeof frameCanvas.toBlob !== 'function') { clearCanvas(frameCanvas); return { kind: 'failed' } }
    context = available
  } catch { if (frameCanvas) clearCanvas(frameCanvas); return { kind: 'failed' } }
  const canvas = frameCanvas

  encodingBusy = true
  return await new Promise<FrameCaptureResult>((resolve) => {
    let finished = false, nativeFinished = false
    let unsubscribe = () => {}
    const interrupted = () => { finish({ kind: 'cancelled' }) }
    const visibility = () => { if (document.visibilityState !== 'visible') interrupted() }
    const finish = (result: FrameCaptureResult) => {
      if (finished) return
      finished = true
      signal.removeEventListener('abort', interrupted)
      document.removeEventListener('visibilitychange', visibility)
      window.removeEventListener('pagehide', interrupted)
      unsubscribe()
      clearCanvas(canvas)
      resolve(result)
    }
    signal.addEventListener('abort', interrupted, { once: true })
    document.addEventListener('visibilitychange', visibility)
    window.addEventListener('pagehide', interrupted)
    unsubscribe = subscribeSessionCleared(interrupted)
    const deadline = setTimeout(() => { encodingTimedOut = true; finish({ kind: 'timeout' }) }, 30_000)
    const releaseEncoder = () => {
      if (nativeFinished) return false
      nativeFinished = true
      clearTimeout(deadline)
      encodingBusy = false
      return true
    }
    try {
      if (finished || signal.aborted || !currentVideo(video, stream)) {
        releaseEncoder(); finish({ kind: 'cancelled' }); return
      }
      context.drawImage(video, 0, 0, width, height)
      if (finished || signal.aborted || !currentVideo(video, stream)) {
        releaseEncoder(); finish({ kind: 'cancelled' }); return
      }
      canvas.toBlob((blob) => {
        if (!releaseEncoder() || finished) return
        if (signal.aborted || !currentVideo(video, stream)) { finish({ kind: 'cancelled' }); return }
        if (!blob || blob.type !== 'image/jpeg' || !blob.size) { finish({ kind: 'failed' }); return }
        if (blob.size > maxFrameBytes) { finish({ kind: 'too-large' }); return }
        const frame: CapturedFrame = Object.freeze({ kind: 'captured', blob, width, height })
        capturedFrames.add(frame)
        finish(frame)
      }, 'image/jpeg', 0.9)
    } catch {
      releaseEncoder(); finish({ kind: 'failed' })
    }
  })
}
