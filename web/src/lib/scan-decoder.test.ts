import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as native from './scan-native'
import * as zxing from './scan-zxing-client'
import { createScanDecoder } from './scan-decoder'

vi.mock('./scan-native', async (original) => ({ ...await original<typeof import('./scan-native')>(), createNativeBarcodeDecoder: vi.fn() }))
vi.mock('./scan-zxing-client', () => ({ createZXingBarcodeDecoder: vi.fn() }))

describe('combined barcode decoder', () => {
  const nativeDetect = vi.fn<() => Promise<native.ScannedCode[]>>()
  const fallbackDetect = vi.fn<() => Promise<native.ScannedCode[]>>()
  const cancel = vi.fn(), close = vi.fn()
  beforeEach(() => {
    vi.stubGlobal('Worker', class {})
    nativeDetect.mockReset().mockResolvedValue([])
    fallbackDetect.mockReset().mockResolvedValue([])
    cancel.mockReset(); close.mockReset()
    vi.mocked(native.createNativeBarcodeDecoder).mockReset().mockResolvedValue(undefined)
    vi.mocked(zxing.createZXingBarcodeDecoder).mockReset().mockImplementation(async (formats) => ({ formats, detect: fallbackDetect, cancel, close }))
  })

  afterEach(() => { vi.unstubAllGlobals() })

  it('does not load a fallback when native covers every required format', async () => {
    vi.mocked(native.createNativeBarcodeDecoder).mockResolvedValue({ formats: [...native.requiredScanFormats], detect: nativeDetect })
    const decoder = await createScanDecoder(new AbortController().signal)
    expect(decoder?.nativeFormats).toEqual(native.requiredScanFormats)
    expect(decoder?.fallbackFormats).toEqual([])
    expect(decoder?.fallbackUnavailable).toBe(false)
    expect(zxing.createZXingBarcodeDecoder).not.toHaveBeenCalled()
    decoder?.close()
  })

  it('combines native with only the missing formats without rewriting results', async () => {
    const nativeResults: native.ScannedCode[] = [{ format: 'EAN_13', rawValue: '0042100005264' }]
    const fallbackResults: native.ScannedCode[] = [{ format: 'UPC_A', rawValue: '042100005264' }]
    nativeDetect.mockResolvedValue(nativeResults); fallbackDetect.mockResolvedValue(fallbackResults)
    vi.mocked(native.createNativeBarcodeDecoder).mockResolvedValue({ formats: ['EAN_13', 'QR_CODE'], detect: nativeDetect })
    const signal = new AbortController().signal, decoder = await createScanDecoder(signal)
    expect(zxing.createZXingBarcodeDecoder).toHaveBeenCalledExactlyOnceWith(['EAN_8', 'UPC_A', 'UPC_E', 'CODE_128'], signal)
    expect(decoder?.formats).toEqual(native.requiredScanFormats)
    expect(decoder?.fallbackFormats).toEqual(['EAN_8', 'UPC_A', 'UPC_E', 'CODE_128'])
    expect(await decoder?.detect(document.createElement('video'), signal)).toEqual([...nativeResults, ...fallbackResults])
    decoder?.cancel(); decoder?.close()
    expect(cancel).toHaveBeenCalledOnce(); expect(close).toHaveBeenCalledOnce()
  })

  it('loads all formats when native is absent and reports unavailable fallback honestly', async () => {
    const signal = new AbortController().signal
    const fallbackOnly = await createScanDecoder(signal)
    expect(fallbackOnly?.nativeFormats).toEqual([])
    expect(fallbackOnly?.fallbackFormats).toEqual(native.requiredScanFormats)
    fallbackOnly?.close()
    vi.mocked(zxing.createZXingBarcodeDecoder).mockRejectedValue(new Error('private worker detail'))
    expect(await createScanDecoder(signal)).toBeUndefined()
    vi.mocked(native.createNativeBarcodeDecoder).mockResolvedValue({ formats: ['EAN_13'], detect: nativeDetect })
    const partial = await createScanDecoder(signal)
    expect(partial?.formats).toEqual(['EAN_13'])
    expect(partial?.fallbackUnavailable).toBe(true)
    expect(await partial?.detect(document.createElement('video'), signal)).toEqual([])
  })

  it('keeps native formats available when workers are not supported', async () => {
    vi.stubGlobal('Worker', undefined)
    vi.mocked(native.createNativeBarcodeDecoder).mockResolvedValue({ formats: ['EAN_8'], detect: nativeDetect })
    const decoder = await createScanDecoder(new AbortController().signal)
    expect(decoder?.formats).toEqual(['EAN_8'])
    expect(decoder?.fallbackUnavailable).toBe(true)
    expect(zxing.createZXingBarcodeDecoder).not.toHaveBeenCalled()
  })

  it('does not start or retain a fallback after an aborted native discovery', async () => {
    let finish!: (value: Awaited<ReturnType<typeof native.createNativeBarcodeDecoder>>) => void
    vi.mocked(native.createNativeBarcodeDecoder).mockReturnValue(new Promise((resolve) => { finish = resolve }))
    const controller = new AbortController(), pending = createScanDecoder(controller.signal)
    controller.abort(); finish(undefined)
    await expect(pending).rejects.toMatchObject({ name: 'AbortError' })
    expect(zxing.createZXingBarcodeDecoder).not.toHaveBeenCalled()
  })

  it('closes a late fallback created after discovery cancellation', async () => {
    let finish!: (value: Awaited<ReturnType<typeof zxing.createZXingBarcodeDecoder>>) => void
    vi.mocked(zxing.createZXingBarcodeDecoder).mockReturnValue(new Promise((resolve) => { finish = resolve }))
    const controller = new AbortController(), pending = createScanDecoder(controller.signal)
    await vi.waitFor(() => expect(zxing.createZXingBarcodeDecoder).toHaveBeenCalledOnce())
    controller.abort(); finish({ formats: native.requiredScanFormats, detect: fallbackDetect, cancel, close })
    await expect(pending).rejects.toMatchObject({ name: 'AbortError' })
    expect(close).toHaveBeenCalled()
  })

  it('never passes a frame to the fallback after a native result is invalidated', async () => {
    let finish!: (value: native.ScannedCode[]) => void
    nativeDetect.mockReturnValue(new Promise((resolve) => { finish = resolve }))
    vi.mocked(native.createNativeBarcodeDecoder).mockResolvedValue({ formats: ['EAN_13'], detect: nativeDetect })
    const decoder = await createScanDecoder(new AbortController().signal)
    const controller = new AbortController(), pending = decoder!.detect(document.createElement('video'), controller.signal)
    controller.abort(); finish([{ format: 'EAN_13', rawValue: 'stale' }])
    await expect(pending).rejects.toMatchObject({ name: 'AbortError' })
    expect(fallbackDetect).not.toHaveBeenCalled()
    decoder?.close()
  })
})
