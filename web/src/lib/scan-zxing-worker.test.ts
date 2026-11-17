import { afterEach, expect, it, vi } from 'vitest'

afterEach(() => { vi.unstubAllGlobals(); vi.resetModules() })

it('posts a ready message, rejects malformed work and decodes a bounded frame without returning diagnostics', async () => {
  const postMessage = vi.fn()
  const scope: { postMessage: typeof postMessage; onmessage: ((event: MessageEvent<unknown>) => void) | null } = { postMessage, onmessage: null }
  vi.stubGlobal('self', scope)
  await import('./scan-zxing-worker')
  expect(postMessage).toHaveBeenCalledWith({ type: 'ready' })
  scope.onmessage?.({ data: { id: 1, pixels: 'not pixels', width: 50, height: 50, formats: ['QR_CODE'] } } as MessageEvent<unknown>)
  expect(postMessage).toHaveBeenLastCalledWith({ type: 'error', id: 1 })
  scope.onmessage?.({ data: { id: 2, pixels: new Uint8ClampedArray(50 * 50).fill(255), width: 50, height: 50, formats: ['QR_CODE'] } } as MessageEvent<unknown>)
  expect(postMessage).toHaveBeenLastCalledWith({ type: 'result', id: 2, codes: [] })
})
