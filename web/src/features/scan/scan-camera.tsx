import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { Link } from 'react-router'
import { Camera, RefreshCw, SwitchCamera } from 'lucide-react'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { mutationControl as control } from '@/components/mutation-review'
import { validText } from '@/lib/api/client'
import { messages } from '@/lib/messages'
import { requiredScanFormats, type ScannedCode } from '@/lib/scan-native'
import { parseHomeboxQR } from '@/lib/scan-qr'
import { quoteExactText } from '@/lib/text-literal'
import { useCameraStream } from '@/lib/use-camera-stream'
import { useNativeScan } from './use-native-scan'

const text = messages.scan
type TorchState = Readonly<{ track: MediaStreamTrack; enabled: boolean; pending: boolean; failed: boolean }>

export function ScanCamera({ sessionActive, onUseCode }: { sessionActive: boolean; onUseCode: (code: ScannedCode) => void }) {
  const camera = useCameraStream(sessionActive)
  const video = useRef<HTMLVideoElement>(null), controls = useRef<HTMLDivElement>(null)
  const startButton = useRef<HTMLButtonElement>(null), resultHeading = useRef<HTMLHeadingElement>(null), restoreFocus = useRef(false)
  const stopCamera = camera.stop
  const halt = useCallback(() => {
    restoreFocus.current = !!controls.current?.contains(document.activeElement)
    stopCamera()
  }, [stopCamera])
  const scanner = useNativeScan({ stream: camera.stream, video, active: sessionActive, onHalt: halt })
  const [playbackError, setPlaybackError] = useState(false)
  const [torchState, setTorchState] = useState<TorchState>()
  const stream = camera.stream
  const track = stream?.getVideoTracks()[0]
  const torch = torchState?.track === track ? torchState : undefined
  const torchSupported = useMemo(() => {
    try { return !!track && typeof track.applyConstraints === 'function' && (track.getCapabilities?.() as MediaTrackCapabilities & { torch?: boolean } | undefined)?.torch === true }
    catch { return false }
  }, [track])

  useLayoutEffect(() => {
    if (!restoreFocus.current || camera.status !== 'idle') return
    restoreFocus.current = false
    if (scanner.results) resultHeading.current?.focus()
    else startButton.current?.focus()
  }, [camera.status, scanner.results])

  useEffect(() => {
    const element = video.current
    if (!stream || !element) return
    let active = true
    element.srcObject = stream
    void Promise.resolve().then(() => active ? element.play() : undefined).catch(() => { if (active) setPlaybackError(true) })
    return () => { active = false; element.pause(); if (element.srcObject === stream) element.srcObject = null }
  }, [stream])

  function start() { scanner.reset(); setPlaybackError(false); setTorchState(undefined); void camera.start(camera.facing) }
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
    {scanner.status === 'checking' ? <p role="status" className="text-sm">{text.nativeChecking}</p>
      : scanner.status === 'unavailable' ? <div className="space-y-2"><p role="status" className="text-sm leading-relaxed">{text.nativeUnavailable}</p><Button type="button" variant="outline" disabled={scanner.failure === 'timeout'} className={control} onClick={scanner.retryDiscovery}>{text.nativeRetry}</Button></div>
        : <div className="space-y-2 text-sm leading-relaxed">
          {scanner.nativeFormats.length > 0 && <p><span className="font-medium">{text.nativeFormats}</span>：{scanner.nativeFormats.map((format) => messages.owned.symbologies[format]).join('、')}</p>}
          {scanner.fallbackFormats.length > 0 && <p><span className="font-medium">{text.fallbackFormats}</span>：{scanner.fallbackFormats.map((format) => messages.owned.symbologies[format]).join('、')}</p>}
          {requiredScanFormats.some((format) => !scanner.formats.includes(format)) && <p><span className="font-medium">{text.nativeMissing}</span>：{requiredScanFormats.filter((format) => !scanner.formats.includes(format)).map((format) => messages.owned.symbologies[format]).join('、')}</p>}
          {scanner.fallbackUnavailable && <div className="space-y-2"><p role="status">{text.fallbackUnavailable}</p><Button type="button" variant="outline" disabled={scanner.failure === 'timeout'} className={control} onClick={scanner.retryDiscovery}>{text.nativeRetry}</Button></div>}
        </div>}
    {!sessionActive && <p role="status" className="text-base">{text.cameraSession}</p>}
    {scanner.failure && <Alert role="alert" className="p-4"><AlertDescription className="text-base">{scanner.failure === 'timeout' ? text.nativeTimeout : text.nativeError}</AlertDescription></Alert>}
    {camera.status === 'requesting' && <p role="status" className="text-base">{text.cameraWaiting}</p>}
    {camera.status === 'error' && <Alert role="alert" className="p-4"><AlertDescription className="text-base">{text.cameraErrors[camera.error]}</AlertDescription></Alert>}
    {camera.status === 'active' && <>
      <p role="status" className="text-sm text-muted-foreground">{text.cameraLive}；{camera.facing === 'environment' ? text.cameraRear : text.cameraFront}</p>
      <video ref={video} aria-label={text.cameraVideo} autoPlay muted playsInline className="aspect-video w-full rounded-md bg-black object-contain" />
      {playbackError && <Alert role="alert" className="space-y-3 p-4"><AlertTitle>{text.cameraPlaybackError}</AlertTitle><AlertDescription><Button type="button" variant="outline" className={`${control} gap-2`} onClick={() => { void retryPlayback() }}><RefreshCw aria-hidden="true" />{text.cameraPlaybackRetry}</Button></AlertDescription></Alert>}
      {torch?.failed && <Alert role="alert" className="p-4"><AlertDescription className="text-base">{text.cameraTorchError}</AlertDescription></Alert>}
    </>}
    <div ref={controls} className="flex flex-wrap gap-2">
      {camera.status === 'idle' || camera.status === 'error' ? <Button ref={startButton} type="button" disabled={!sessionActive} className={`${control} gap-2`} onClick={start}><Camera aria-hidden="true" />{text.cameraStart}</Button>
        : <Button type="button" variant="outline" className={control} onClick={stop}>{text.cameraStop}</Button>}
      {camera.status === 'active' && <Button type="button" variant="outline" className={`${control} gap-2`} onClick={changeCamera}><SwitchCamera aria-hidden="true" />{text.cameraSwitch}</Button>}
      {camera.status === 'active' && scanner.status === 'ready' && !scanner.failure && <Button type="button" variant="outline" className={control} onClick={scanner.paused ? scanner.resume : scanner.pause}>{scanner.paused ? text.nativeResume : text.nativePause}</Button>}
      {camera.status === 'active' && torchSupported && <Button type="button" variant="outline" aria-pressed={torch?.enabled ?? false} disabled={torch?.pending} className={control} onClick={() => { void toggleTorch() }}>{torch?.enabled ? text.cameraTorchOff : text.cameraTorchOn}</Button>}
    </div>
    {camera.status === 'active' && scanner.paused && !scanner.failure && <p role="status" className="text-sm">{text.nativePaused}</p>}
    {scanner.results && <section aria-labelledby="native-scan-results" className="min-w-0 space-y-4">
      <h3 ref={resultHeading} id="native-scan-results" tabIndex={-1} className="text-xl font-semibold focus:outline-2 focus:outline-ring">{text.nativeResults}</h3>
      <p role="status" className="text-sm leading-relaxed">{text.nativeResultsHelp}</p>
      {scanner.results.some((code) => code.format === 'EAN_13') && scanner.results.some((code) => code.format === 'UPC_A') && <p className="text-sm leading-relaxed">{text.nativeAmbiguous}</p>}
      {scanner.truncated && <p role="status" className="text-sm leading-relaxed">{text.nativeTruncated}</p>}
      <ul aria-label={text.nativeResults} className="divide-y rounded-lg border">{scanner.results.map((code) => {
        const destination = code.format === 'QR_CODE' ? parseHomeboxQR(code.rawValue, window.location.origin) : undefined
        return <li key={JSON.stringify([code.format, code.rawValue])} className="min-w-0 space-y-3 p-4">
          <p className="text-sm font-medium">{messages.owned.symbologies[code.format]}</p>
          <p className="whitespace-pre-wrap text-base [overflow-wrap:anywhere]">{quoteExactText(code.rawValue)}</p>
          {code.format === 'QR_CODE' && !destination && <p className="text-sm leading-relaxed">{text.nativeUntrustedQR}</p>}
          <div className="flex flex-wrap gap-2">
            {destination && <Button asChild variant="outline" className={control}><Link to={destination.path}>{destination.kind === 'location' ? text.nativeOpenLocation : text.nativeOpenUnit}</Link></Button>}
            {validText(code.rawValue, 1, 512) ? <Button type="button" variant="outline" className={control} onClick={() => { onUseCode(code); scanner.clear() }}>{text.nativeUse}</Button>
              : <p className="text-sm leading-relaxed">{text.nativeTooLong}</p>}
          </div>
        </li>
      })}</ul>
      <Button type="button" variant="outline" className={control} onClick={() => { scanner.clear(); startButton.current?.focus() }}>{text.nativeClear}</Button>
    </section>}
  </section>
}
