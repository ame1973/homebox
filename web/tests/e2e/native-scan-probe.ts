import type { Page } from '@playwright/test'

type NativeCode = { format: string; rawValue: string }
type NativeScanProbe = {
  calls: number
  requested: string[]
  frames: { width: number; currentTrack: boolean }[]
  tracks: MediaStreamTrack[]
  queue: NativeCode[][]
  hold: boolean
  fail: boolean
  finish?: (codes: NativeCode[]) => void
}
declare global { interface Window { __nativeScanProbe: NativeScanProbe } }

// This test-only detector returns controlled results; it does not decode pixels.
// The camera itself remains Chromium's real fake-device MediaStream.
export async function installNativeProbe(page: Page) {
  await page.addInitScript(() => {
    const probe: NativeScanProbe = { calls: 0, requested: [], frames: [], tracks: [], queue: [], hold: false, fail: false }
    window.__nativeScanProbe = probe
    const devices = navigator.mediaDevices
    if (devices?.getUserMedia) {
      const acquire = devices.getUserMedia.bind(devices)
      devices.getUserMedia = async (constraints) => {
        const stream = await acquire(constraints)
        probe.tracks.push(...stream.getVideoTracks())
        return stream
      }
    }
    class TestBarcodeDetector {
      static async getSupportedFormats() { return ['qr_code', 'ean_13', 'upc_a'] }
      constructor(options: { formats: string[] }) { probe.requested = options.formats }
      async detect(video: HTMLVideoElement) {
        probe.calls++
        const stream = video.srcObject as MediaStream | null
        probe.frames.push({ width: video.videoWidth, currentTrack: !!stream?.getVideoTracks().some((track) => probe.tracks.at(-1) === track) })
        if (probe.hold) {
          probe.hold = false
          return await new Promise<NativeCode[]>((resolve) => { probe.finish = resolve })
        }
        if (probe.fail) { probe.fail = false; throw new Error('private native detector diagnostics') }
        return probe.queue.shift() ?? []
      }
    }
    Object.defineProperty(window, 'BarcodeDetector', { configurable: true, value: TestBarcodeDetector })
  })
}
