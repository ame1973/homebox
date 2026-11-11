import { afterEach, describe, expect, it, vi } from 'vitest'
import { createNativeBarcodeDecoder } from './scan-native'

afterEach(() => { vi.unstubAllGlobals() })

describe('native barcode decoder', () => {
  it('falls back when the browser does not expose its supported formats', async () => {
    vi.stubGlobal('BarcodeDetector', undefined)
    expect(await createNativeBarcodeDecoder()).toBeUndefined()
    vi.stubGlobal('BarcodeDetector', class {})
    expect(await createNativeBarcodeDecoder()).toBeUndefined()
  })

  it.each(['reject', 'invalid', 'none', 'constructor'] as const)('falls back when native discovery is %s', async (failure) => {
    const supported = vi.fn(async () => {
      if (failure === 'reject') throw new Error('private native diagnostic')
      if (failure === 'invalid') return 'qr_code'
      if (failure === 'none') return ['aztec', 'unknown']
      return ['qr_code']
    })
    class FakeDetector {
      static getSupportedFormats = supported
      constructor() { if (failure === 'constructor') throw new Error('private native diagnostic') }
    }
    vi.stubGlobal('BarcodeDetector', FakeDetector)
    expect(await createNativeBarcodeDecoder()).toBeUndefined()
    expect(supported).toHaveBeenCalledOnce()
  })

  it('reports partial native coverage and preserves only supported, bounded exact results', async () => {
    const detect = vi.fn(async () => [
      { format: 'ean_13', rawValue: '00012345678905' },
      { format: 'qr_code', rawValue: 'https://example.test/家具?x=+\n' },
      { format: 'ean_13', rawValue: '\0 retained ' },
      { format: 'ean_8', rawValue: '12345670' },
      { format: 'code_128', rawValue: 'unsupported by this detector' },
      { format: 'aztec', rawValue: 'unknown' },
      { format: 'qr_code', rawValue: '' },
      { format: 'qr_code', rawValue: '\ud800' },
      { format: 'qr_code', rawValue: 'x'.repeat(4097) },
      { format: 'ean_13', rawValue: 'x'.repeat(513) },
      { format: 'ean_13', rawValue: 12345678 },
      null, {},
    ])
    const options: unknown[] = []
    class FakeDetector {
      static getSupportedFormats = async () => ['qr_code', 'ean_13', 'ean_8', 'ean_13', 'aztec']
      constructor(value: unknown) { options.push(value) }
      detect = detect
    }
    vi.stubGlobal('BarcodeDetector', FakeDetector)
    const decoder = await createNativeBarcodeDecoder()
    expect(decoder?.formats).toEqual(['QR_CODE', 'EAN_13', 'EAN_8'])
    expect(options).toEqual([{ formats: ['qr_code', 'ean_13', 'ean_8'] }])
    const video = document.createElement('video')
    expect(await decoder?.detect(video)).toEqual([
      { format: 'EAN_13', rawValue: '00012345678905' },
      { format: 'QR_CODE', rawValue: 'https://example.test/家具?x=+\n' },
      { format: 'EAN_13', rawValue: '\0 retained ' },
      { format: 'EAN_8', rawValue: '12345670' },
    ])
    expect(detect).toHaveBeenCalledExactlyOnceWith(video)
  })

  it('recognizes every supported native format without changing its text or namespace', async () => {
    class FakeDetector {
      static getSupportedFormats = async () => ['upc_e', 'code_128', 'upc_a', 'ean_8', 'ean_13', 'qr_code']
      async detect() { return [
        { format: 'upc_a', rawValue: '012345678905' },
        { format: 'upc_e', rawValue: '01234565' },
        { format: 'code_128', rawValue: '000ABC' },
        { format: 'qr_code', rawValue: '😀'.repeat(4096) },
      ] }
    }
    vi.stubGlobal('BarcodeDetector', FakeDetector)
    const decoder = await createNativeBarcodeDecoder()
    expect(decoder?.formats).toEqual(['QR_CODE', 'EAN_13', 'EAN_8', 'UPC_A', 'UPC_E', 'CODE_128'])
    expect(await decoder?.detect(document.createElement('canvas'))).toEqual([
      { format: 'UPC_A', rawValue: '012345678905' },
      { format: 'UPC_E', rawValue: '01234565' },
      { format: 'CODE_128', rawValue: '000ABC' },
      { format: 'QR_CODE', rawValue: '😀'.repeat(4096) },
    ])
  })

  it('propagates native detection failures and invalid response shapes for the caller to recover', async () => {
    const detect = vi.fn<() => Promise<unknown>>()
    class FakeDetector {
      static getSupportedFormats = async () => ['qr_code']
      detect = detect
    }
    vi.stubGlobal('BarcodeDetector', FakeDetector)
    const decoder = await createNativeBarcodeDecoder()
    const image = document.createElement('img')
    detect.mockResolvedValueOnce({ format: 'qr_code', rawValue: 'not an array' })
    await expect(decoder?.detect(image)).rejects.toThrow('Invalid native barcode response')
    const failure = new Error('native unavailable')
    detect.mockRejectedValueOnce(failure)
    await expect(decoder?.detect(image)).rejects.toBe(failure)
  })
})
