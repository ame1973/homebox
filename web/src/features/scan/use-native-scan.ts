import { useEffect, useLayoutEffect, useRef, useState, useSyncExternalStore, type RefObject } from 'react'
import { createScanDecoder } from '@/lib/scan-decoder'
import { isLiveScanFrame, type ScannedCode } from '@/lib/scan-native'
import { subscribeSessionCleared } from '@/lib/session-lifecycle'

type Decoder = NonNullable<Awaited<ReturnType<typeof createScanDecoder>>>
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
  const currentDecoder = useRef<Decoder | undefined>(undefined)
  const discoveryAbort = useRef<AbortController | undefined>(undefined)
  const detectionAbort = useRef<AbortController | undefined>(undefined)

  useEffect(() => {
    let current = true, owned: Decoder | undefined
    const controller = new AbortController()
    discoveryAbort.current = controller
    const deadline = setTimeout(() => { controller.abort(); if (current) setDecoder(null) }, 8_000)
    void createScanDecoder(controller.signal).then((value) => {
      if (!current || controller.signal.aborted) { value?.close(); return }
      clearTimeout(deadline); owned = value; currentDecoder.current = value; setDecoder(value ?? null)
    }, () => { if (current) { clearTimeout(deadline); setDecoder(null) } })
    return () => {
      current = false; clearTimeout(deadline); controller.abort(); owned?.close()
      if (discoveryAbort.current === controller) discoveryAbort.current = undefined
      if (currentDecoder.current === owned) currentDecoder.current = undefined
    }
  }, [discovery])
  useLayoutEffect(() => { generation.current++; detectionAbort.current?.abort() }, [active, stream])
  useLayoutEffect(() => { if (timedOut) onHalt() }, [onHalt, timedOut])
  useLayoutEffect(() => {
    const invalidateSession = () => {
      invalidate(generation); discoveryAbort.current?.abort(); detectionAbort.current?.abort(); currentDecoder.current?.cancel()
      setDetection(undefined); setPaused(true)
    }
    const visibility = () => { if (document.visibilityState !== 'visible') invalidateSession() }
    document.addEventListener('visibilitychange', visibility)
    window.addEventListener('pagehide', invalidateSession)
    const unsubscribe = subscribeSessionCleared(invalidateSession)
    return () => {
      invalidate(generation); discoveryAbort.current?.abort(); detectionAbort.current?.abort(); currentDecoder.current?.cancel()
      document.removeEventListener('visibilitychange', visibility); window.removeEventListener('pagehide', invalidateSession); unsubscribe()
    }
  }, [])
  useEffect(() => {
    if (!stream || !active || !decoder || paused || failure || detection) return
    const detector: Decoder = decoder, source: MediaStream = stream
    const current = ++generation.current, controller = new AbortController()
    detectionAbort.current = controller
    let canceled = false, timer: ReturnType<typeof setTimeout> | undefined
    const valid = () => !canceled && !controller.signal.aborted && !nativeTimedOut && generation.current === current && document.visibilityState === 'visible' && source.getVideoTracks().some((track) => track.readyState === 'live')
    const schedule = () => { if (valid()) timer = setTimeout(() => { void tick() }, 250) }
    const ready = (element: HTMLVideoElement) => video.current === element && isLiveScanFrame(element, source) && !!element.videoWidth && !!element.videoHeight
    async function tick() {
      if (!valid()) return
      const element = video.current
      if (!element || !ready(element) || nativeBusy) { schedule(); return }
      nativeBusy = true
      let expired = false
      const frame = new AbortController(), listeners: [EventTarget, string][] = []
      const invalidateFrame = () => {
        if (frame.signal.aborted) return
        frame.abort(); detector.cancel()
      }
      const deadline = setTimeout(() => {
        expired = true
        controller.abort(); detector.cancel()
        nativeTimedOut = true
        for (const listener of timeoutListeners) listener()
      }, 8_000)
      try {
        const events: [EventTarget, string][] = [
          [controller.signal, 'abort'], [element, 'pause'], [element, 'ended'], [element, 'emptied'],
          [source, 'addtrack'], [source, 'removetrack'],
          ...source.getTracks().flatMap((track): [EventTarget, string][] => [[track, 'mute'], [track, 'ended']]),
        ]
        for (const [target, name] of events) { target.addEventListener(name, invalidateFrame); listeners.push([target, name]) }
        if (controller.signal.aborted) invalidateFrame()
        if (valid() && !frame.signal.aborted && ready(element)) {
          const codes = await detector.detect(element, frame.signal)
          if (expired || !valid()) return
          if (!frame.signal.aborted && ready(element) && codes.length) {
            generation.current++
            setDetection(boundedCodes(codes)); setPaused(true); onHalt()
            return
          }
        }
      } catch {
        if (!expired && valid() && !frame.signal.aborted && ready(element)) { generation.current++; setFailed(true); setPaused(true); onHalt() }
      } finally {
        for (const [target, name] of listeners) target.removeEventListener(name, invalidateFrame)
        clearTimeout(deadline)
        nativeBusy = false
      }
      schedule()
    }
    timer = setTimeout(() => { void tick() }, 0)
    return () => {
      canceled = true; controller.abort(); detector.cancel()
      if (detectionAbort.current === controller) detectionAbort.current = undefined
      if (timer !== undefined) clearTimeout(timer)
      invalidate(generation)
    }
  }, [active, decoder, detection, failure, onHalt, paused, stream, video])

  function pause() { generation.current++; detectionAbort.current?.abort(); currentDecoder.current?.cancel(); setPaused(true) }
  function resume() { setPaused(false) }
  function clear() { generation.current++; setDetection(undefined) }
  function reset() {
    generation.current++; detectionAbort.current?.abort(); setDetection(undefined)
    if (failure !== 'timeout') { setPaused(false); setFailed(false) }
  }
  function retryDiscovery() {
    if (failure === 'timeout') return
    generation.current++; discoveryAbort.current?.abort(); detectionAbort.current?.abort(); currentDecoder.current?.cancel()
    setDecoder(undefined); setDiscovery((value) => value + 1)
  }
  return { status: decoder === undefined ? 'checking' as const : decoder === null ? 'unavailable' as const : 'ready' as const,
    formats: decoder?.formats ?? [], nativeFormats: decoder?.nativeFormats ?? [], fallbackFormats: decoder?.fallbackFormats ?? [],
    fallbackUnavailable: decoder?.fallbackUnavailable ?? false, results: detection?.codes, truncated: detection?.truncated ?? false,
    paused, failed: failure !== undefined, failure, pause, resume, clear, reset, retryDiscovery }
}
