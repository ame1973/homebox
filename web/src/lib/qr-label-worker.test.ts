/** @vitest-environment node */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { decodeZXing } from './scan-zxing-core'

class WorkerScope {
  onmessage: ((event: MessageEvent<unknown>) => void) | null = null
  readonly sent: { value: unknown; transfer: Transferable[] | undefined }[] = []
  postMessage(value: unknown, transfer?: Transferable[]) { this.sent.push({ value, transfer }) }
  receive(data: unknown) { this.onmessage?.({ data } as MessageEvent<unknown>) }
}
let scope: WorkerScope
beforeEach(() => { vi.resetModules(); scope = new WorkerScope(); vi.stubGlobal('self', scope) })
afterEach(() => { vi.unstubAllGlobals() })

describe('QR label worker boundary', () => {
  it('transfers one bounded result and preserves the exact QR destination', async () => {
    await import('./qr-label-worker')
    scope.receive({ type: 'encode', kind: 'unit', id: 'unit-1', origin: 'https://homebox.example' })
    expect(scope.sent).toHaveLength(1)
    const result = scope.sent[0]!.value as { type: string; size: number; modules: ArrayBuffer }
    expect(result.type).toBe('result'); expect(result.modules).toBeInstanceOf(ArrayBuffer)
    expect(scope.sent[0]!.transfer).toEqual([result.modules]); expect(result.modules.byteLength).toBe(result.size * result.size)
    const modules = new Uint8Array(result.modules), width = result.size * 4, pixels = new Uint8ClampedArray(width * width)
    for (let y = 0; y < width; y++) for (let x = 0; x < width; x++) pixels[y * width + x] = modules[Math.floor(y / 4) * result.size + Math.floor(x / 4)] ? 0 : 255
    expect(decodeZXing(pixels, width, width, ['QR_CODE'])).toEqual([{ format: 'QR_CODE', rawValue: 'https://homebox.example/u/unit-1' }])
    scope.receive({ type: 'encode', kind: 'unit', id: 'other', origin: 'https://homebox.example' })
    expect(scope.sent).toHaveLength(1)
  })

  it.each([null, {}, { type: 'encode', kind: 'unknown', id: 'private', origin: 'https://homebox.example' },
    { type: 'encode', kind: 'unit', id: 'private/path', origin: 'https://homebox.example' },
    { type: 'encode', kind: 'unit', id: 'private', origin: 'https://homebox.example', extra: 'secret' },
    JSON.parse('{"type":"encode","kind":"unit","id":"private","origin":"https://homebox.example","__proto__":1}')])
  ('returns only a static error for an invalid request %#', async (request) => {
    await import('./qr-label-worker')
    scope.receive(request)
    expect(scope.sent).toEqual([{ value: { type: 'error' }, transfer: undefined }])
  })
})
