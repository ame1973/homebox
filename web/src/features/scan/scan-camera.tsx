import { useEffect, useMemo, useRef, useState } from 'react'
import { Camera, RefreshCw, SwitchCamera } from 'lucide-react'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { mutationControl as control } from '@/components/mutation-review'
import { messages } from '@/lib/messages'
import { useCameraStream } from '@/lib/use-camera-stream'

const text = messages.scan
type TorchState = Readonly<{ track: MediaStreamTrack; enabled: boolean; pending: boolean; failed: boolean }>

export function ScanCamera({ sessionActive }: { sessionActive: boolean }) {
  const camera = useCameraStream(sessionActive)
  const video = useRef<HTMLVideoElement>(null)
  const [playbackError, setPlaybackError] = useState(false)
  const [torchState, setTorchState] = useState<TorchState>()
  const stream = camera.stream
  const track = stream?.getVideoTracks()[0]
  const torch = torchState?.track === track ? torchState : undefined
  const torchSupported = useMemo(() => {
    try { return !!track && typeof track.applyConstraints === 'function' && (track.getCapabilities?.() as MediaTrackCapabilities & { torch?: boolean } | undefined)?.torch === true }
    catch { return false }
  }, [track])

  useEffect(() => {
    const element = video.current
    if (!stream || !element) return
    let active = true
    element.srcObject = stream
    void Promise.resolve().then(() => active ? element.play() : undefined).catch(() => { if (active) setPlaybackError(true) })
    return () => { active = false; element.pause(); if (element.srcObject === stream) element.srcObject = null }
  }, [stream])

  function start() { setPlaybackError(false); setTorchState(undefined); void camera.start(camera.facing) }
  function stop() { camera.stop(); setPlaybackError(false); setTorchState(undefined) }
  function changeCamera() { setPlaybackError(false); setTorchState(undefined); void camera.start(camera.facing === 'environment' ? 'user' : 'environment') }
  async function retryPlayback() {
    const element = video.current, original = stream
    if (!element || !original) return
    try { await element.play(); if (element.isConnected && element.srcObject === original) setPlaybackError(false) }
    catch { if (element.isConnected && element.srcObject === original) setPlaybackError(true) }
  }
  async function toggleTorch() {
    if (!track || !stream || torch?.pending) return
    const enabled = torch?.enabled ?? false
    setTorchState({ track, enabled, pending: true, failed: false })
    try {
      await track.applyConstraints({ advanced: [{ torch: !enabled } as MediaTrackConstraintSet] })
      if (track.readyState === 'live' && video.current?.srcObject === stream) setTorchState({ track, enabled: !enabled, pending: false, failed: false })
    } catch {
      if (track.readyState === 'live' && video.current?.srcObject === stream) setTorchState({ track, enabled, pending: false, failed: true })
    }
  }

  return <section aria-labelledby="scan-camera-title" className="min-w-0 max-w-3xl space-y-4 rounded-lg border p-5 sm:p-6">
    <h2 id="scan-camera-title" className="text-2xl font-semibold">{text.cameraTitle}</h2>
    <p className="text-base leading-relaxed">{text.cameraUnavailable}</p>
    <p className="text-sm leading-relaxed text-muted-foreground">{text.cameraPermission}</p>
    {!sessionActive && <p role="status" className="text-base">{text.cameraSession}</p>}
    {camera.status === 'requesting' && <p role="status" className="text-base">{text.cameraWaiting}</p>}
    {camera.status === 'error' && <Alert role="alert" className="p-4"><AlertDescription className="text-base">{text.cameraErrors[camera.error]}</AlertDescription></Alert>}
    {camera.status === 'active' && <>
      <p role="status" className="text-sm text-muted-foreground">{text.cameraLive}；{camera.facing === 'environment' ? text.cameraRear : text.cameraFront}</p>
      <video ref={video} aria-label={text.cameraVideo} autoPlay muted playsInline className="aspect-video w-full rounded-md bg-black object-contain" />
      {playbackError && <Alert role="alert" className="space-y-3 p-4"><AlertTitle>{text.cameraPlaybackError}</AlertTitle><AlertDescription><Button type="button" variant="outline" className={`${control} gap-2`} onClick={() => { void retryPlayback() }}><RefreshCw aria-hidden="true" />{text.cameraPlaybackRetry}</Button></AlertDescription></Alert>}
      {torch?.failed && <Alert role="alert" className="p-4"><AlertDescription className="text-base">{text.cameraTorchError}</AlertDescription></Alert>}
    </>}
    <div className="flex flex-wrap gap-2">
      {camera.status === 'idle' || camera.status === 'error' ? <Button type="button" disabled={!sessionActive} className={`${control} gap-2`} onClick={start}><Camera aria-hidden="true" />{text.cameraStart}</Button>
        : <Button type="button" variant="outline" className={control} onClick={stop}>{text.cameraStop}</Button>}
      {camera.status === 'active' && <Button type="button" variant="outline" className={`${control} gap-2`} onClick={changeCamera}><SwitchCamera aria-hidden="true" />{text.cameraSwitch}</Button>}
      {camera.status === 'active' && torchSupported && <Button type="button" variant="outline" aria-pressed={torch?.enabled ?? false} disabled={torch?.pending} className={control} onClick={() => { void toggleTorch() }}>{torch?.enabled ? text.cameraTorchOff : text.cameraTorchOn}</Button>}
    </div>
  </section>
}
