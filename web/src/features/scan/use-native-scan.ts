import { useEffect, useLayoutEffect, useRef, useState, useSyncExternalStore, type RefObject } from 'react'
import { createNativeBarcodeDecoder, type ScannedCode } from '@/lib/scan-native'
import { subscribeSessionCleared } from '@/lib/session-lifecycle'

type Decoder = NonNullable<Awaited<ReturnType<typeof createNativeBarcodeDecoder>>>
type Detection = Readonly<{ codes: readonly ScannedCode[]; truncated: boolean }>
function invalidate(ref: { current: number }) { ref.current++ }

// Native detection cannot be canceled. Keep its gate across SPA remounts so a
// stuck call cannot accumulate parallel work; a timeout stays latched to reload.
let nativeBusy = false, nativeTimedOut = false
const timeoutListeners = new Set<() => void>()
function subscribeTimeout(listener: () => void) { timeoutListeners.add(listener); return () => { timeoutListeners.delete(listener) } }
function timeoutSnapshot() { return nativeTimedOut }
function serverTimeoutSnapshot() { return false }

// Retain independent formats and exact text; repeated detections are not new
// writes or proof of a unique physical Unit. Bound visible results explicitly.
function boundedCodes(codes: readonly ScannedCode[]): Detection {
  const result: ScannedCode[] = [], seen = new Set<string>()
  for (const code of codes) {
    const key = JSON.stringify([code.format, code.rawValue])
    if (seen.has(key)) continue
    seen.add(key)
    if (result.length === 20) return { codes: result, truncated: true }
    result.push(code)
  }
  return { codes: result, truncated: false }
}

export function useNativeScan({ stream, video, active, onHalt }: {
  stream: MediaStream | null; video: RefObject<HTMLVideoElement | null>; active: boolean; onHalt: () => void
}) {
  const [decoder, setDecoder] = useState<Decoder | null>()
  const [discovery, setDiscovery] = useState(0)
  const [paused, setPaused] = useState(false), [failed, setFailed] = useState(false)
  const timedOut = useSyncExternalStore(subscribeTimeout, timeoutSnapshot, serverTimeoutSnapshot)
  const failure = timedOut ? 'timeout' : failed ? 'error' : undefined
  const [detection, setDetection] = useState<Detection>()
  const [previousActive, setPreviousActive] = useState(active)
  if (previousActive !== active) {
    setPreviousActive(active)
    if (!active) { setDetection(undefined); setPaused(true) }
  }
  const generation = useRef(0)

  useEffect(() => {
    let current = true
    const deadline = setTimeout(() => { current = false; setDecoder(null) }, 8_000)
    void createNativeBarcodeDecoder().then((value) => {
      if (current) { clearTimeout(deadline); setDecoder(value ?? null) }
    }, () => { if (current) { clearTimeout(deadline); setDecoder(null) } })
    return () => { current = false; clearTimeout(deadline) }
  }, [discovery])
  useLayoutEffect(() => { generation.current++ }, [active, stream])
  useLayoutEffect(() => { if (timedOut) onHalt() }, [onHalt, timedOut])
  useLayoutEffect(() => {
    const invalidateSession = () => { invalidate(generation); setDetection(undefined); setPaused(true) }
    const visibility = () => { if (document.visibilityState !== 'visible') invalidateSession() }
    document.addEventListener('visibilitychange', visibility)
    window.addEventListener('pagehide', invalidateSession)
    const unsubscribe = subscribeSessionCleared(invalidateSession)
    return () => {
      invalidate(generation); document.removeEventListener('visibilitychange', visibility); window.removeEventListener('pagehide', invalidateSession); unsubscribe()
    }
  }, [])
  useEffect(() => {
    if (!stream || !active || !decoder || paused || failure || detection) return
    const detector: Decoder = decoder
    const current = ++generation.current
    let canceled = false, timer: ReturnType<typeof setTimeout> | undefined
    const valid = () => !canceled && !nativeTimedOut && generation.current === current && document.visibilityState === 'visible' && stream.getVideoTracks().some((track) => track.readyState === 'live')
    const schedule = () => { if (valid()) timer = setTimeout(() => { void tick() }, 250) }
    async function tick() {
      if (!valid()) return
      const element = video.current
      if (!element || element.srcObject !== stream || element.readyState < 2 || !element.videoWidth || !element.videoHeight || nativeBusy) { schedule(); return }
      nativeBusy = true
      let expired = false
      const deadline = setTimeout(() => {
        expired = true
        nativeTimedOut = true
        for (const listener of timeoutListeners) listener()
      }, 8_000)
      try {
        const codes = await detector.detect(element)
        if (expired || !valid()) return
        if (codes.length) {
          generation.current++
          setDetection(boundedCodes(codes)); setPaused(true); onHalt()
          return
        }
      } catch {
        if (!expired && valid()) { generation.current++; setFailed(true); setPaused(true); onHalt() }
      } finally {
        clearTimeout(deadline)
        nativeBusy = false
      }
      schedule()
    }
    timer = setTimeout(() => { void tick() }, 0)
    return () => { canceled = true; if (timer !== undefined) clearTimeout(timer); invalidate(generation) }
  }, [active, decoder, detection, failure, onHalt, paused, stream, video])

  function pause() { generation.current++; setPaused(true) }
  function resume() { setPaused(false) }
  function clear() { generation.current++; setDetection(undefined) }
  function reset() {
    generation.current++; setDetection(undefined)
    if (failure !== 'timeout') { setPaused(false); setFailed(false) }
  }
  function retryDiscovery() {
    if (failure === 'timeout') return
    generation.current++; setDecoder(undefined); setDiscovery((value) => value + 1)
  }
  return { status: decoder === undefined ? 'checking' as const : decoder === null ? 'unavailable' as const : 'ready' as const,
    formats: decoder?.formats ?? [], results: detection?.codes, truncated: detection?.truncated ?? false, paused, failed: failure !== undefined, failure, pause, resume, clear, reset, retryDiscovery }
}
