import { maxFrameBytes, maxFrameDimension, maxFramePixels } from '@/lib/capture-frame'
import { subscribeSessionCleared } from '@/lib/session-lifecycle'

type Prepared = Readonly<{ kind: 'prepared'; mime: 'image/jpeg' | 'image/png' | 'image/webp'; width: number; height: number; orientation: number; blob: Blob }>
export type SelectedImagePreparation = Prepared | Readonly<{ kind: 'invalid' | 'unsupported' | 'too-large' | 'failed' | 'busy' | 'timeout' | 'cancelled' }>

// A terminated worker cannot publish a late result. Keep one in-flight slot
// across SPA remounts; if termination itself fails, require a real reload.
let active: object | undefined, terminationFailed = false

function reply(data: unknown, sourceSize: number): SelectedImagePreparation {
  if (!data || typeof data !== 'object' || !('type' in data) || data.type !== 'result' || !('kind' in data)) return { kind: 'failed' }
  if (data.kind === 'invalid' || data.kind === 'unsupported' || data.kind === 'too-large' || data.kind === 'failed') return { kind: data.kind }
  if (data.kind !== 'prepared' || !('mime' in data) || data.mime !== 'image/jpeg' && data.mime !== 'image/png' && data.mime !== 'image/webp' ||
    !('width' in data) || typeof data.width !== 'number' || !Number.isSafeInteger(data.width) || data.width < 1 || data.width > maxFrameDimension ||
    !('height' in data) || typeof data.height !== 'number' || !Number.isSafeInteger(data.height) || data.height < 1 || data.height > maxFrameDimension ||
    data.width * data.height > maxFramePixels || !('orientation' in data) || typeof data.orientation !== 'number' ||
    !Number.isSafeInteger(data.orientation) || data.orientation < 1 || data.orientation > 8 || !('blob' in data) || !(data.blob instanceof Blob) ||
    typeof File !== 'undefined' && data.blob instanceof File || data.blob.type !== data.mime || !Number.isSafeInteger(data.blob.size) ||
    data.blob.size < 1 || data.blob.size > sourceSize) return { kind: 'failed' }
  return Object.freeze({ kind: 'prepared', mime: data.mime, width: data.width, height: data.height, orientation: data.orientation, blob: data.blob })
}

// Call from an explicit action after a current completed-session check. The
// caller must recheck its session/generation after awaiting and discard the
// intermediate on suspension or departure. This is not a decoded image,
// trusted frame, stored draft, upload or proof of native decoder safety.
export async function prepareSelectedImage(source: Blob, signal: AbortSignal): Promise<SelectedImagePreparation> {
  if (signal.aborted || document.visibilityState !== 'visible') return { kind: 'cancelled' }
  if (terminationFailed) return { kind: 'failed' }
  if (active) return { kind: 'busy' }
  if (!(source instanceof Blob) || !Number.isSafeInteger(source.size) || source.size < 1) return { kind: 'invalid' }
  const size = source.size
  if (size > maxFrameBytes) return { kind: 'too-large' }
  const token = {}; active = token

  return await new Promise<SelectedImagePreparation>((resolve) => {
    let finished = false, worker: Worker | undefined, deadline: ReturnType<typeof setTimeout> | undefined
    let unsubscribe = () => {}
    const interrupted = () => { finish({ kind: 'cancelled' }) }
    const visibility = () => { if (document.visibilityState !== 'visible') interrupted() }
    const terminate = (instance: Worker) => {
      try { instance.terminate() } catch { terminationFailed = true }
    }
    const finish = (value: SelectedImagePreparation) => {
      if (finished) return
      finished = true
      signal.removeEventListener('abort', interrupted)
      document.removeEventListener('visibilitychange', visibility)
      window.removeEventListener('pagehide', interrupted)
      try { unsubscribe() } catch { /* The result is still discarded. */ }
      if (deadline !== undefined) clearTimeout(deadline)
      if (worker) terminate(worker)
      if (active === token) active = undefined
      resolve(terminationFailed ? { kind: 'failed' } : value)
    }
    try {
      signal.addEventListener('abort', interrupted, { once: true })
      document.addEventListener('visibilitychange', visibility)
      window.addEventListener('pagehide', interrupted)
      unsubscribe = subscribeSessionCleared(interrupted)
      if (signal.aborted || document.visibilityState !== 'visible') { interrupted(); return }
      // Slicing removes the File name, timestamp and declared MIME while
      // retaining its immutable bytes; the worker determines the actual type.
      const blob = source.slice(0, size)
      if (finished) return
      if (blob.size !== size || blob.type !== '') { finish({ kind: 'failed' }); return }
      const instance = new Worker(new URL('./image-prepare-worker.ts', import.meta.url), { type: 'module' })
      if (finished) { terminate(instance); return }
      worker = instance
      instance.onmessage = ({ data }: MessageEvent<unknown>) => {
        if (finished) return
        if (signal.aborted || document.visibilityState !== 'visible') { interrupted(); return }
        finish(reply(data, size))
      }
      instance.onerror = (event) => { event.preventDefault(); finish({ kind: 'failed' }) }
      instance.onmessageerror = () => { finish({ kind: 'failed' }) }
      deadline = setTimeout(() => { finish({ kind: 'timeout' }) }, 30_000)
      if (signal.aborted || document.visibilityState !== 'visible') { interrupted(); return }
      instance.postMessage({ type: 'prepare', blob })
    } catch { finish({ kind: 'failed' }) }
  })
}
