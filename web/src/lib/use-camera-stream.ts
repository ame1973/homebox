import { useCallback, useLayoutEffect, useRef, useState } from 'react'
import { subscribeSessionCleared } from '@/lib/session-lifecycle'

export type CameraFacing = 'environment' | 'user'
export type CameraError = 'insecure' | 'unsupported' | 'permission-denied' | 'not-found' | 'busy' | 'interrupted' | 'unknown'
export type CameraState =
  | { status: 'idle' | 'requesting'; facing: CameraFacing; stream: null; error: null }
  | { status: 'active'; facing: CameraFacing; stream: MediaStream; error: null }
  | { status: 'error'; facing: CameraFacing; stream: null; error: CameraError }

function cameraError(cause: unknown): CameraError {
  const name = typeof cause === 'object' && cause !== null && 'name' in cause ? cause.name : undefined
  switch (name) {
    case 'NotAllowedError': case 'PermissionDeniedError': case 'SecurityError': return 'permission-denied'
    case 'NotFoundError': case 'DevicesNotFoundError': case 'OverconstrainedError': return 'not-found'
    case 'NotReadableError': case 'TrackStartError': return 'busy'
    case 'AbortError': return 'interrupted'
    case 'TypeError': case 'NotSupportedError': return 'unsupported'
    default: return 'unknown'
  }
}

function stopTracks(stream: MediaStream) {
  for (const track of stream.getTracks()) track.stop()
}

// Call start directly from a user gesture. A session becoming active or a page
// becoming visible never opens a camera; stop also invalidates pending requests.
export function useCameraStream(sessionActive: boolean) {
  const [state, setState] = useState<CameraState>({ status: 'idle', facing: 'environment', stream: null, error: null })
  const mounted = useRef(false)
  const enabled = useRef(false)
  const generation = useRef(0)
  const current = useRef<{ stream: MediaStream; detach: () => void } | null>(null)

  const release = useCallback(() => {
    generation.current++
    const previous = current.current
    current.current = null
    if (previous) { previous.detach(); stopTracks(previous.stream) }
  }, [])

  const stop = useCallback(() => {
    release()
    if (mounted.current) setState((old) => old.status === 'idle' ? old : { status: 'idle', facing: old.facing, stream: null, error: null })
  }, [release])

  useLayoutEffect(() => {
    mounted.current = true
    const visibility = () => { if (document.visibilityState !== 'visible') stop() }
    const pagehide = () => { stop() }
    const sessionCleared = () => { enabled.current = false; stop() }
    document.addEventListener('visibilitychange', visibility)
    window.addEventListener('pagehide', pagehide)
    const unsubscribe = subscribeSessionCleared(sessionCleared)
    return () => {
      mounted.current = false
      document.removeEventListener('visibilitychange', visibility)
      window.removeEventListener('pagehide', pagehide)
      unsubscribe()
      release()
    }
  }, [release, stop])

  useLayoutEffect(() => {
    enabled.current = sessionActive
    if (!sessionActive) stop()
  }, [sessionActive, stop])

  const start = useCallback(async (facing: CameraFacing = 'environment') => {
    if (!mounted.current || !enabled.current) return
    release()
    const request = generation.current
    const stillCurrent = () => mounted.current && enabled.current && generation.current === request
    if (document.visibilityState !== 'visible') {
      setState({ status: 'idle', facing, stream: null, error: null })
      return
    }
    if (!window.isSecureContext) {
      setState({ status: 'error', facing, stream: null, error: 'insecure' })
      return
    }
    const devices = navigator.mediaDevices
    if (!devices || typeof devices.getUserMedia !== 'function') {
      setState({ status: 'error', facing, stream: null, error: 'unsupported' })
      return
    }
    setState({ status: 'requesting', facing, stream: null, error: null })
    let acquired: MediaStream | null = null
    try {
      const stream = acquired = await devices.getUserMedia({ audio: false, video: { facingMode: { ideal: facing } } })
      if (!stillCurrent() || document.visibilityState !== 'visible') {
        stopTracks(stream)
        if (stillCurrent()) stop()
        return
      }
      const tracks = stream.getTracks()
      const video = stream.getVideoTracks()
      if (video.length === 0 || video.some((track) => track.readyState !== 'live') || tracks.some((track) => track.kind === 'audio')) {
        stopTracks(stream)
        setState({ status: 'error', facing, stream: null, error: 'not-found' })
        return
      }
      const ended = () => {
        if (!stillCurrent() || current.current?.stream !== stream) return
        release()
        setState({ status: 'error', facing, stream: null, error: 'interrupted' })
      }
      for (const track of video) track.addEventListener('ended', ended)
      current.current = { stream, detach: () => { for (const track of video) track.removeEventListener('ended', ended) } }
      if (video.some((track) => track.readyState !== 'live')) { ended(); return }
      setState({ status: 'active', facing, stream, error: null })
      acquired = null
    } catch (cause) {
      const report = stillCurrent()
      if (acquired) {
        if (current.current?.stream === acquired) release()
        else stopTracks(acquired)
      }
      if (report) setState({ status: 'error', facing, stream: null, error: cameraError(cause) })
    }
  }, [release, stop])

  return { ...state, start, stop }
}
