import { useEffect, useRef, useState } from 'react'
import type { QueryClient } from '@tanstack/react-query'
import { clearSessionQueries } from '../../src/features/auth/use-auth-action'
import { useCameraStream } from '../../src/lib/use-camera-stream'

// This harness is built only into a private Playwright bundle, never the app.
export function CameraHarness({ client }: { client: QueryClient }) {
  const [active, setActive] = useState(true)
  const camera = useCameraStream(active)
  const video = useRef<HTMLVideoElement>(null)
  useEffect(() => {
    const element = video.current
    if (!element) return
    element.srcObject = camera.stream
    return () => { element.srcObject = null }
  }, [camera.stream])
  return <section aria-label="Camera test">
    <output aria-label="Camera state">{camera.status}{camera.error ? `:${camera.error}` : ''}</output>
    <button type="button" onClick={() => { void camera.start() }}>Start camera</button>
    <button type="button" onClick={() => { void camera.start(camera.facing === 'environment' ? 'user' : 'environment') }}>Switch camera</button>
    <button type="button" onClick={camera.stop}>Stop camera</button>
    <button type="button" onClick={() => { clearSessionQueries(client); setActive(false) }}>Purge session</button>
    <button type="button" onClick={() => { setActive(true) }}>New session</button>
    <video ref={video} aria-label="Live camera" autoPlay muted playsInline />
  </section>
}
