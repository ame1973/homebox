import { maxFrameBytes } from '@/lib/capture-frame'
import { prepareImageDecodeInput } from './image-decode-input'

type WorkerScope = { onmessage: ((event: MessageEvent<unknown>) => void) | null; postMessage: (value: unknown) => void }
const scope = self as unknown as WorkerScope
let started = false

function send(value: unknown) {
  try { scope.postMessage(value) } catch { /* A stopped worker cannot deliver a result. */ }
}

// One nameless, bounded Blob per worker. No filename, raw source or native
// decoder is exposed to the caller. Termination discards unobserved results.
scope.onmessage = ({ data }) => {
  if (started) return
  started = true
  void (async () => {
    try {
      const request = data as { type?: unknown; blob?: unknown } | null
      if (!request || request.type !== 'prepare' || !(request.blob instanceof Blob) ||
        typeof File !== 'undefined' && request.blob instanceof File || request.blob.type !== '' ||
        !Number.isSafeInteger(request.blob.size) || request.blob.size < 1) {
        send({ type: 'result', kind: 'invalid' }); return
      }
      const size = request.blob.size
      if (size > maxFrameBytes) { send({ type: 'result', kind: 'too-large' }); return }
      const buffer = await request.blob.arrayBuffer()
      if (!(buffer instanceof ArrayBuffer) || buffer.byteLength !== size) { send({ type: 'result', kind: 'failed' }); return }
      const result = prepareImageDecodeInput(new Uint8Array(buffer))
      if (result.kind !== 'prepared') { send({ type: 'result', kind: result.kind }); return }
      const blob = new Blob([result.bytes], { type: result.mime })
      send({ type: 'result', kind: 'prepared', mime: result.mime, width: result.width, height: result.height, orientation: result.orientation, blob })
    } catch { send({ type: 'result', kind: 'failed' }) }
  })()
}
