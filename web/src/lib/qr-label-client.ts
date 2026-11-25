import { makeQRLabelLink, type QRLabelKind, type QRLabelLink } from './qr-label-link'
import { subscribeSessionCleared } from './session-lifecycle'

export type PreparedQRLabel = Readonly<{ kind: 'ready'; link: QRLabelLink; size: number; modules: Uint8Array }>
export type QRLabelPreparation = PreparedQRLabel | Readonly<{ kind: 'invalid' | 'busy' | 'cancelled' | 'timeout' | 'failed' }>

// Only one short-lived worker is admitted across remounts. If termination
// fails, do not start another until a real document reload.
let active: object | undefined, terminationFailed = false

function reply(data: unknown, link: QRLabelLink): QRLabelPreparation {
  if (!data || typeof data !== 'object' || Array.isArray(data) || Object.keys(data).some((key) => !['type', 'size', 'modules'].includes(key)) ||
    !('type' in data) || data.type !== 'result' || !('size' in data) || typeof data.size !== 'number' || !Number.isSafeInteger(data.size) ||
    data.size < 29 || data.size > 185 || (data.size - 25) % 4 !== 0 || !('modules' in data) || !(data.modules instanceof ArrayBuffer) || data.modules.byteLength !== data.size * data.size) return { kind: 'failed' }
  const size = data.size, modules = new Uint8Array(data.modules)
  for (let y = 0; y < size; y++) for (let x = 0; x < size; x++) {
    const value = modules[y * size + x]
    if (value !== 0 && value !== 1 || (x < 4 || y < 4 || x >= size - 4 || y >= size - 4) && value !== 0) return { kind: 'failed' }
  }
  function finder(left: number, top: number): boolean {
    for (let y = 0; y < 7; y++) for (let x = 0; x < 7; x++) {
      const black = x === 0 || x === 6 || y === 0 || y === 6 || x >= 2 && x <= 4 && y >= 2 && y <= 4
      if (modules[(top + y) * size + left + x] !== Number(black)) return false
    }
    return true
  }
  if (!finder(4, 4) || !finder(size - 11, 4) || !finder(4, size - 11)) return { kind: 'failed' }
  return Object.freeze({ kind: 'ready', link, size, modules })
}

// Call only after an explicit action and a current completed-session/source
// check. The caller must recheck its generation and discard returned modules on
// suspension or session loss; a matrix is not source authority or a safe print.
export async function prepareQRLabel(kind: QRLabelKind, id: string, origin: string, signal: AbortSignal): Promise<QRLabelPreparation> {
  if (signal.aborted || document.visibilityState !== 'visible') return { kind: 'cancelled' }
  if (terminationFailed) return { kind: 'failed' }
  if (active) return { kind: 'busy' }
  let link: QRLabelLink
  try { link = makeQRLabelLink(kind, id, origin) } catch { return { kind: 'invalid' } }
  const token = {}; active = token
  return await new Promise<QRLabelPreparation>((resolve) => {
    let finished = false, worker: Worker | undefined, deadline: ReturnType<typeof setTimeout> | undefined
    let unsubscribe = () => {}
    const interrupted = () => { finish({ kind: 'cancelled' }) }
    const visibility = () => { if (document.visibilityState !== 'visible') interrupted() }
    const terminate = (instance: Worker) => {
      try { instance.terminate() } catch { terminationFailed = true }
    }
    const finish = (value: QRLabelPreparation) => {
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
      const instance = new Worker(new URL('./qr-label-worker.ts', import.meta.url), { type: 'module' })
      if (finished) { terminate(instance); return }
      worker = instance
      instance.onmessage = ({ data }: MessageEvent<unknown>) => {
        if (finished) return
        if (signal.aborted || document.visibilityState !== 'visible') { interrupted(); return }
        finish(reply(data, link))
      }
      instance.onerror = (event) => { event.preventDefault(); finish({ kind: 'failed' }) }
      instance.onmessageerror = () => { finish({ kind: 'failed' }) }
      deadline = setTimeout(() => { finish({ kind: 'timeout' }) }, 8_000)
      if (signal.aborted || document.visibilityState !== 'visible') { interrupted(); return }
      instance.postMessage({ type: 'encode', kind, id, origin })
    } catch { finish({ kind: 'failed' }) }
  })
}
