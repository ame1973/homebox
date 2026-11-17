import { decodeZXing } from './scan-zxing-core'
import type { ScanFormat } from './scan-native'

type WorkerRequest = Readonly<{ id: number; pixels: Uint8ClampedArray; width: number; height: number; formats: readonly ScanFormat[] }>
type WorkerScope = { onmessage: ((event: MessageEvent<unknown>) => void) | null; postMessage: (value: unknown) => void }
const scope = self as unknown as WorkerScope

scope.onmessage = ({ data }) => {
  const request = data as Partial<WorkerRequest> | null
  const id = request?.id
  try {
    if (!request || !Number.isSafeInteger(id) || id! < 1 || !(request.pixels instanceof Uint8ClampedArray) ||
      typeof request.width !== 'number' || typeof request.height !== 'number' || !Array.isArray(request.formats)) throw new Error('Invalid scan request')
    const codes = decodeZXing(request.pixels, request.width, request.height, request.formats)
    scope.postMessage({ type: 'result', id, codes })
  } catch {
    // Do not send decoder diagnostics or captured pixels back to the UI.
    scope.postMessage({ type: 'error', id })
  }
}
scope.postMessage({ type: 'ready' })
