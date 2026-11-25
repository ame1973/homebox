import { encodeQRLabel } from './qr-label-encode'

type WorkerScope = { onmessage: ((event: MessageEvent<unknown>) => void) | null; postMessage: (value: unknown, transfer?: Transferable[]) => void }
const scope = self as unknown as WorkerScope
let started = false

// One bounded target per worker; raw encoder diagnostics never leave it.
scope.onmessage = ({ data }) => {
  if (started) return
  started = true
  try {
    if (!data || typeof data !== 'object' || Array.isArray(data) || Object.keys(data).some((key) => !['type', 'kind', 'id', 'origin'].includes(key)) ||
      !('type' in data) || data.type !== 'encode' || !('kind' in data) || data.kind !== 'location' && data.kind !== 'unit' ||
      !('id' in data) || typeof data.id !== 'string' || !('origin' in data) || typeof data.origin !== 'string') throw new Error('Invalid QR request')
    const result = encodeQRLabel(data.kind, data.id, data.origin), buffer = result.modules.buffer
    if (!(buffer instanceof ArrayBuffer) || result.modules.byteOffset !== 0 || buffer.byteLength !== result.modules.byteLength) throw new Error('Invalid QR buffer')
    scope.postMessage({ type: 'result', size: result.size, modules: buffer }, [buffer])
  } catch {
    try { scope.postMessage({ type: 'error' }) } catch { /* A stopped worker cannot deliver a result. */ }
  }
}
