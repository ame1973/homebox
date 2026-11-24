// @vitest-environment node
import { afterEach, expect, it, vi } from 'vitest'
import { maxFrameBytes } from '@/lib/capture-frame'

type Scope = { postMessage: ReturnType<typeof vi.fn>; onmessage: ((event: MessageEvent<unknown>) => void) | null }
const png = 'iVBORw0KGgoAAAANSUhEUgAAAAIAAAADCAYAAAC56t6BAAAAHElEQVR4nAXBAQEAMAjAIHwyo9l8B5EcqnhhtB+Pawm7X97uPgAAAABJRU5ErkJggg=='
const bytes = (value: string) => Uint8Array.from(atob(value), (character) => character.charCodeAt(0))

async function worker(): Promise<Scope> {
  vi.resetModules()
  const scope: Scope = { postMessage: vi.fn(), onmessage: null }
  vi.stubGlobal('self', scope)
  await import('./image-prepare-worker')
  return scope
}
afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks() })

it('reads and prepares one bounded nameless Blob without returning source bytes or diagnostics', async () => {
  const scope = await worker(), raw = bytes(png), blob = new Blob([raw], { type: '' })
  scope.onmessage?.({ data: { type: 'prepare', blob } } as MessageEvent<unknown>)
  await vi.waitFor(() => expect(scope.postMessage).toHaveBeenCalledOnce())
  const result = scope.postMessage.mock.calls[0]?.[0] as { type: string; kind: string; mime: string; width: number; height: number; orientation: number; blob: Blob }
  expect(result).toMatchObject({ type: 'result', kind: 'prepared', mime: 'image/png', width: 2, height: 3, orientation: 1 })
  expect(result.blob).toBeInstanceOf(Blob); expect(result.blob.type).toBe('image/png')
  expect(new Uint8Array(await result.blob.arrayBuffer())).toEqual(raw)
  expect(result).not.toHaveProperty('name'); expect(result).not.toHaveProperty('bytes')
  scope.onmessage?.({ data: { type: 'prepare', blob } } as MessageEvent<unknown>)
  await Promise.resolve(); expect(scope.postMessage).toHaveBeenCalledOnce()
})

it.each([null, {}, { type: 'other', blob: new Blob(['x']) }, { type: 'prepare', blob: 'bad' },
  { type: 'prepare', blob: new Blob() }, { type: 'prepare', blob: new Blob(['x'], { type: 'image/png' }) },
  { type: 'prepare', blob: new File(['x'], 'private-name.png') }])(
  'rejects an invalid message without exposing its data', async (request) => {
    const scope = await worker()
    scope.onmessage?.({ data: request } as MessageEvent<unknown>)
    await vi.waitFor(() => expect(scope.postMessage).toHaveBeenCalledOnce())
    expect(scope.postMessage).toHaveBeenCalledWith({ type: 'result', kind: 'invalid' })
  },
)

it('rejects oversized sources, inconsistent reads and read exceptions', async () => {
  for (const mode of ['oversized', 'mismatch', 'throw'] as const) {
    const scope = await worker(), blob = new Blob(['x'])
    if (mode === 'oversized') Object.defineProperty(blob, 'size', { value: maxFrameBytes + 1 })
    if (mode === 'mismatch') vi.spyOn(blob, 'arrayBuffer').mockResolvedValue(new ArrayBuffer(2))
    if (mode === 'throw') vi.spyOn(blob, 'arrayBuffer').mockRejectedValue(new Error('private file path'))
    scope.onmessage?.({ data: { type: 'prepare', blob } } as MessageEvent<unknown>)
    await vi.waitFor(() => expect(scope.postMessage).toHaveBeenCalledOnce())
    expect(scope.postMessage).toHaveBeenCalledWith({ type: 'result', kind: mode === 'oversized' ? 'too-large' : 'failed' })
  }
})

it('forwards safe format errors without attempting to decode or exposing raw file text', async () => {
  const scope = await worker(), blob = new Blob(['<svg>private text</svg>'])
  scope.onmessage?.({ data: { type: 'prepare', blob } } as MessageEvent<unknown>)
  await vi.waitFor(() => expect(scope.postMessage).toHaveBeenCalledOnce())
  expect(scope.postMessage).toHaveBeenCalledWith({ type: 'result', kind: 'unsupported' })
})
