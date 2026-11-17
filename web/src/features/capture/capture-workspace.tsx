import { useCallback, useEffect, useLayoutEffect, useRef, useState, type FormEvent } from 'react'
import { flushSync } from 'react-dom'
import { Camera, RotateCcw, RotateCw, SwitchCamera } from 'lucide-react'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { DraftLeaveGuard } from '@/components/draft-leave-guard'
import { mutationControl as control } from '@/components/mutation-review'
import { validText } from '@/lib/api/client'
import { parseSafeCount } from '@/lib/api/domain'
import { captureVideoFrame, type FrameCaptureResult } from '@/lib/capture-frame'
import { messages } from '@/lib/messages'
import { subscribeSessionCleared } from '@/lib/session-lifecycle'
import { useCameraStream } from '@/lib/use-camera-stream'
import {
  addCapturedPhoto, chooseCaptureCover, cropCapturedPhoto, describeCapturedPhoto, emptyCaptureAlbum,
  maxCaptureDescriptionUnits, maxCapturePhotos, moveCapturedPhoto, removeCapturedPhoto,
  replaceCapturedPhoto, resetCapturedPhotoGeometry, rotateCapturedPhoto, type CaptureAlbum,
} from './album'
import { photoDisplaySize, type PhotoGeometry } from './geometry'

const text = messages.capture, cameraText = messages.scan

type CropDraft = { x: string; y: string; width: string; height: string }
type Confirmation = 'remove' | 'retake'
const emptyCrop = (): CropDraft => ({ x: '', y: '', width: '', height: '' })

function previewTransform(value: PhotoGeometry): string {
  const { crop: { x, y, width, height }, turns } = value
  if (turns === 1) return `matrix(0 1 -1 0 ${height + y} ${-x})`
  if (turns === 2) return `matrix(-1 0 0 -1 ${width + x} ${height + y})`
  if (turns === 3) return `matrix(0 -1 1 0 ${-y} ${width + x})`
  return `matrix(1 0 0 1 ${-x} ${-y})`
}

// The album and object URLs belong to this mounted page only. No operation here
// stores, uploads, or attaches a photo; the owner must be re-read before writing.
export function CaptureWorkspace({ active }: { active: boolean }) {
  const camera = useCameraStream(active)
  const [album, setAlbum] = useState<CaptureAlbum>(emptyCaptureAlbum)
  const [selectedID, setSelectedID] = useState<number>()
  const [urls, setURLs] = useState<Record<number, string>>({})
  const [crops, setCrops] = useState<Record<number, CropDraft>>({})
  const [cropError, setCropError] = useState(false)
  const [capturing, setCapturing] = useState(false)
  const [captureError, setCaptureError] = useState<keyof typeof text.frameErrors>()
  const [notice, setNotice] = useState<string>()
  const [playbackError, setPlaybackError] = useState(false)
  const [confirmation, setConfirmation] = useState<Confirmation>()
  const [timedOut, setTimedOut] = useState(false)
  const albumRef = useRef(album), urlRefs = useRef(new Map<number, string>())
  const mounted = useRef(false), enabled = useRef(false), sessionClosed = useRef(false), allowLeaving = useRef(false)
  const generation = useRef(0), nextID = useRef(1), pending = useRef<AbortController | undefined>(undefined)
  const retakeID = useRef<number | null>(null), video = useRef<HTMLVideoElement>(null)
  const cameraControls = useRef<HTMLDivElement>(null), previewHeading = useRef<HTMLHeadingElement>(null), cameraHeading = useRef<HTMLHeadingElement>(null), startButton = useRef<HTMLButtonElement>(null)
  const confirmationHeading = useRef<HTMLHeadingElement>(null), confirmationTrigger = useRef<HTMLButtonElement | null>(null), confirmedAction = useRef<Confirmation | undefined>(undefined)
  const restoreFocus = useRef<'preview' | 'start' | 'camera' | undefined>(undefined)
  const stopCamera = camera.stop, stream = camera.stream
  const selected = album.photos.find((photo) => photo.id === selectedID)
  const size = selected && photoDisplaySize(selected.geometry)
  const crop = selectedID === undefined ? emptyCrop() : crops[selectedID] ?? emptyCrop()
  const locked = capturing || camera.status === 'active' || camera.status === 'requesting'

  const cancelPending = useCallback(() => {
    generation.current++
    pending.current?.abort()
    pending.current = undefined
    if (mounted.current) setCapturing(false)
  }, [])
  const discard = useCallback(() => {
    cancelPending()
    stopCamera()
    retakeID.current = null
    allowLeaving.current = true
    albumRef.current = emptyCaptureAlbum()
    for (const url of urlRefs.current.values()) {
      try { URL.revokeObjectURL(url) } catch { /* The reference is still discarded. */ }
    }
    urlRefs.current.clear()
    if (mounted.current) {
      setAlbum(albumRef.current); setSelectedID(undefined); setURLs({}); setCrops({}); setCropError(false)
      setCaptureError(undefined); setConfirmation(undefined); setPlaybackError(false)
    }
  }, [cancelPending, stopCamera])

  useLayoutEffect(() => {
    mounted.current = true
    const visibility = () => {
      if (document.visibilityState === 'visible') return
      const lost = albumRef.current.photos.length > 0 || !!pending.current
      flushSync(() => { discard(); if (lost) setNotice(text.backgroundCleared) })
    }
    const pagehide = () => { flushSync(discard) }
    const cleared = (source: 'local' | 'remote') => {
      sessionClosed.current = true; enabled.current = false
      if (source === 'remote') flushSync(discard)
      else discard()
    }
    document.addEventListener('visibilitychange', visibility)
    window.addEventListener('pagehide', pagehide)
    const unsubscribe = subscribeSessionCleared(cleared)
    return () => {
      mounted.current = false
      document.removeEventListener('visibilitychange', visibility)
      window.removeEventListener('pagehide', pagehide)
      unsubscribe()
      discard()
    }
  }, [discard])
  useLayoutEffect(() => {
    enabled.current = active && !sessionClosed.current
    if (!enabled.current) { cancelPending(); stopCamera() }
  }, [active, cancelPending, stopCamera])
  useLayoutEffect(() => {
    if (stream) return
    if (pending.current) cancelPending()
  }, [stream, cancelPending])
  useLayoutEffect(() => {
    if (!restoreFocus.current || locked) return
    const destination = restoreFocus.current === 'preview' ? previewHeading.current : restoreFocus.current === 'start' ? startButton.current : cameraHeading.current
    if (destination) { restoreFocus.current = undefined; destination.focus() }
  }, [locked, selectedID, album])
  useEffect(() => {
    const element = video.current
    if (!stream || !element) return
    let current = true
    element.srcObject = stream
    void Promise.resolve().then(() => current ? element.play() : undefined).catch(() => { if (current) setPlaybackError(true) })
    return () => { current = false; element.pause(); if (element.srcObject === stream) element.srcObject = null }
  }, [stream])

  function apply(next: CaptureAlbum) { albumRef.current = next; setAlbum(next) }
  function updateURL(id: number, blob?: Blob) {
    const old = urlRefs.current.get(id)
    if (old) { try { URL.revokeObjectURL(old) } catch { /* The reference is still discarded. */ }; urlRefs.current.delete(id) }
    if (blob) {
      try { urlRefs.current.set(id, URL.createObjectURL(blob)) } catch { /* Keep the photo without a preview. */ }
    }
    setURLs(Object.fromEntries(urlRefs.current))
  }
  function start(id: number | null) {
    if (!enabled.current || document.visibilityState !== 'visible' || capturing || timedOut) return
    if (id === null && albumRef.current.photos.length >= maxCapturePhotos) return
    if (id !== null && !albumRef.current.photos.some((photo) => photo.id === id)) return
    cancelPending(); retakeID.current = id; setCaptureError(undefined); setNotice(undefined); setPlaybackError(false)
    void camera.start(camera.facing)
  }
  function stop() {
    const focus = !!cameraControls.current?.contains(document.activeElement)
    cancelPending(); retakeID.current = null; stopCamera(); setPlaybackError(false)
    if (focus) restoreFocus.current = albumRef.current.photos.length >= maxCapturePhotos || timedOut ? 'camera' : 'start'
  }
  function switchCamera() {
    if (!enabled.current || capturing) return
    cancelPending(); setPlaybackError(false)
    void camera.start(camera.facing === 'environment' ? 'user' : 'environment')
  }
  async function retryPlayback() {
    const element = video.current, original = stream
    if (!element || !original) return
    try { await element.play(); if (element.isConnected && element.srcObject === original) setPlaybackError(false) }
    catch { if (element.isConnected && element.srcObject === original) setPlaybackError(true) }
  }
  async function capture() {
    const element = video.current, original = stream
    if (!enabled.current || !element || !original || pending.current || timedOut || document.visibilityState !== 'visible') return
    const controller = new AbortController(), current = ++generation.current, id = retakeID.current
    pending.current = controller; setCapturing(true); setCaptureError(undefined)
    let result: FrameCaptureResult
    try { result = await captureVideoFrame(element, original, controller.signal) }
    catch { result = { kind: 'failed' } }
    if (!mounted.current || !enabled.current || controller.signal.aborted || pending.current !== controller || generation.current !== current ||
      document.visibilityState !== 'visible' || video.current?.srcObject !== original) return
    const focus = !!cameraControls.current?.contains(document.activeElement)
    pending.current = undefined; setCapturing(false); stopCamera(); retakeID.current = null
    if (result.kind !== 'captured') {
      if (result.kind === 'timeout') setTimedOut(true)
      setCaptureError(result.kind)
      if (focus) restoreFocus.current = result.kind === 'timeout' || albumRef.current.photos.length >= maxCapturePhotos ? 'camera' : 'start'
      return
    }
    const photoID = id ?? nextID.current
    const change = id === null ? addCapturedPhoto(albumRef.current, photoID, result) : replaceCapturedPhoto(albumRef.current, photoID, result)
    if (change.kind !== 'changed') {
      setCaptureError(change.kind)
      if (focus) restoreFocus.current = albumRef.current.photos.length >= maxCapturePhotos ? 'camera' : 'start'
      return
    }
    if (id === null) nextID.current++
    allowLeaving.current = false
    updateURL(photoID, result.blob); apply(change.album); setSelectedID(photoID); setCropError(false)
    setCrops((previous) => { const next = { ...previous }; delete next[photoID]; return next })
    if (focus) restoreFocus.current = 'preview'
  }
  function select(id: number) { if (!locked) { setSelectedID(id); setCropError(false); setCaptureError(undefined) } }
  function move(direction: 'earlier' | 'later') {
    if (selectedID === undefined || locked) return
    const next = moveCapturedPhoto(albumRef.current, selectedID, direction)
    if (next) apply(next)
  }
  function cover() {
    if (selectedID === undefined || locked) return
    const next = chooseCaptureCover(albumRef.current, albumRef.current.coverID === selectedID ? null : selectedID)
    if (next) apply(next)
  }
  function rotate(direction: 'clockwise' | 'counterclockwise') {
    if (selectedID === undefined || locked) return
    const next = rotateCapturedPhoto(albumRef.current, selectedID, direction)
    if (next) { apply(next); setCropError(false) }
  }
  function cropPhoto(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (selectedID === undefined || locked) return
    const values = crops[selectedID] ?? emptyCrop()
    const x = parseSafeCount(values.x), y = parseSafeCount(values.y), width = parseSafeCount(values.width, 1), height = parseSafeCount(values.height, 1)
    const next = x === undefined || y === undefined || width === undefined || height === undefined ? undefined : cropCapturedPhoto(albumRef.current, selectedID, { x, y, width, height })
    if (!next) { setCropError(true); return }
    apply(next); setCropError(false)
  }
  function remove() {
    if (selectedID === undefined) return
    const index = albumRef.current.photos.findIndex((photo) => photo.id === selectedID)
    const next = removeCapturedPhoto(albumRef.current, selectedID)
    if (!next) return
    updateURL(selectedID); apply(next)
    setCrops((previous) => { const copy = { ...previous }; delete copy[selectedID]; return copy })
    const replacement = next.photos[Math.min(index, next.photos.length - 1)]
    setSelectedID(replacement?.id); setCropError(false); restoreFocus.current = replacement ? 'preview' : timedOut ? 'camera' : 'start'
  }
  const canLeave = useCallback(() => allowLeaving.current, [])
  const leave = useCallback(() => { allowLeaving.current = true; discard() }, [discard])
  const blocked = useCallback(() => { cancelPending(); stopCamera() }, [cancelPending, stopCamera])
  const stay = useCallback(() => {}, [])

  const index = selected ? album.photos.findIndex((photo) => photo.id === selected.id) : -1
  return <>
    {!active ? <p role="status" className="min-h-24 py-4">{text.inactive}</p> : <div className="min-w-0 space-y-7">
    {notice && <Alert role="status" className="p-4"><AlertDescription className="text-base">{notice}</AlertDescription></Alert>}
    <div className="grid min-w-0 items-start gap-7 lg:grid-cols-2">
      <section aria-labelledby="capture-camera-title" className="min-w-0 space-y-4 rounded-lg border p-5 sm:p-6">
        <h2 ref={cameraHeading} id="capture-camera-title" tabIndex={-1} className="rounded-sm text-2xl font-semibold focus-visible:outline-2 focus-visible:outline-ring">{text.cameraTitle}</h2><p className="text-base leading-relaxed">{text.cameraHelp}</p>
        <p className="text-sm leading-relaxed text-muted-foreground">{cameraText.cameraPermission}</p>
        {album.photos.length >= maxCapturePhotos && <p role="status" className="text-sm">{text.cameraLimit}</p>}
        {captureError && <Alert role="alert" className="p-4"><AlertDescription className="text-base">{text.frameErrors[captureError]}</AlertDescription></Alert>}
        {camera.status === 'requesting' && <p role="status">{cameraText.cameraWaiting}</p>}
        {camera.status === 'error' && <Alert role="alert" className="p-4"><AlertDescription className="text-base">{cameraText.cameraErrors[camera.error]}</AlertDescription></Alert>}
        {camera.status === 'active' && <><p role="status" className="text-sm text-muted-foreground">{cameraText.cameraLive}；{camera.facing === 'environment' ? cameraText.cameraRear : cameraText.cameraFront}</p>
          <video ref={video} aria-label={cameraText.cameraVideo} autoPlay muted playsInline className="aspect-video w-full rounded-md bg-black object-contain" />
          {playbackError && <Alert role="alert" className="space-y-3 p-4"><AlertTitle>{cameraText.cameraPlaybackError}</AlertTitle><AlertDescription><Button type="button" variant="outline" className={control} onClick={() => { void retryPlayback() }}>{cameraText.cameraPlaybackRetry}</Button></AlertDescription></Alert>}
        </>}
        {capturing && <p role="status">{text.taking}</p>}
        <div ref={cameraControls} className="flex flex-wrap gap-2">
          {camera.status === 'idle' || camera.status === 'error' ? <Button ref={startButton} type="button" disabled={album.photos.length >= maxCapturePhotos || timedOut} className={`${control} gap-2`} onClick={() => start(null)}><Camera aria-hidden="true" />{cameraText.cameraStart}</Button>
            : <Button type="button" variant="outline" className={control} onClick={stop}>{cameraText.cameraStop}</Button>}
          {camera.status === 'active' && <><Button type="button" disabled={capturing || timedOut || playbackError} className={control} onClick={() => { void capture() }}>{text.take}</Button>
            <Button type="button" variant="outline" disabled={capturing} className={`${control} gap-2`} onClick={switchCamera}><SwitchCamera aria-hidden="true" />{cameraText.cameraSwitch}</Button></>}
        </div>
      </section>
      <section aria-labelledby="capture-album-title" className="min-w-0 space-y-4">
        <h2 id="capture-album-title" className="text-2xl font-semibold">{text.album}</h2><p className="text-sm leading-relaxed text-muted-foreground">{text.albumHelp}</p>
        <p role="status" className="text-sm">{text.count}{album.photos.length}</p>
        {album.photos.length === 0 ? <p className="border-y py-5">{text.empty}</p> : <ol className="flex flex-wrap gap-2" aria-label={text.album}>{album.photos.map((photo, position) => <li key={photo.id}>
          <Button type="button" variant="outline" aria-pressed={photo.id === selectedID} disabled={locked} className={`${control} aria-pressed:border-primary aria-pressed:bg-muted`} onClick={() => select(photo.id)}>{text.photo} {position + 1}{photo.id === album.coverID ? `（${text.cover}）` : ''}</Button>
        </li>)}</ol>}
        {selected && size && <div className="min-w-0 space-y-4 border-t pt-5">
          <h3 ref={previewHeading} tabIndex={-1} className="rounded-sm text-xl font-semibold focus-visible:outline-2 focus-visible:outline-ring">{text.selected}：{text.photo} {index + 1}</h3>
          {urls[selected.id] ? <svg role="img" aria-label={text.preview} viewBox={`0 0 ${size.width} ${size.height}`} width={size.width} height={size.height} className="h-auto max-h-[32rem] w-full overflow-hidden rounded-md bg-muted">
            <image href={urls[selected.id]} width={selected.frame.width} height={selected.frame.height} preserveAspectRatio="none" transform={previewTransform(selected.geometry)} />
          </svg> : <p role="status">{text.noPreview}</p>}
          <p className="text-sm leading-relaxed text-muted-foreground">{text.dimensions}：{size.width} × {size.height}<br />{text.original}：{selected.frame.width} × {selected.frame.height}</p>
          <div className="flex flex-wrap gap-2"><Button type="button" variant="outline" disabled={locked || index === 0} className={control} onClick={() => move('earlier')}>{text.earlier}</Button>
            <Button type="button" variant="outline" disabled={locked || index === album.photos.length - 1} className={control} onClick={() => move('later')}>{text.later}</Button>
            <Button type="button" variant="outline" disabled={locked} aria-pressed={album.coverID === selected.id} className={control} onClick={cover}>{album.coverID === selected.id ? text.clearCover : text.setCover}</Button>
            <Button type="button" variant="outline" disabled={locked || timedOut} className={control} onClick={(event) => { confirmationTrigger.current = event.currentTarget; confirmedAction.current = undefined; setConfirmation('retake') }}>{text.retake}</Button>
            <Button type="button" variant="outline" disabled={locked} className={control} onClick={(event) => { confirmationTrigger.current = event.currentTarget; confirmedAction.current = undefined; setConfirmation('remove') }}>{text.remove}</Button></div>
          <div className="space-y-2"><label htmlFor="capture-description" className="block text-base font-medium">{text.description}</label><Textarea id="capture-description" value={selected.description} disabled={locked} maxLength={maxCaptureDescriptionUnits} rows={3} aria-invalid={!validText(selected.description, 0, 10_000)} aria-describedby="capture-description-help" className="text-base md:text-base" onChange={(event) => { const next = describeCapturedPhoto(albumRef.current, selected.id, event.target.value); if (next) apply(next) }} />
            <p id="capture-description-help" className="text-sm text-muted-foreground">{text.descriptionHelp}</p>{!validText(selected.description, 0, 10_000) && <p role="alert" className="text-sm text-destructive">{text.descriptionError}</p>}</div>
          <div className="flex flex-wrap gap-2"><Button type="button" variant="outline" disabled={locked} className={`${control} gap-2`} onClick={() => rotate('counterclockwise')}><RotateCcw aria-hidden="true" />{text.rotateLeft}</Button>
            <Button type="button" variant="outline" disabled={locked} className={`${control} gap-2`} onClick={() => rotate('clockwise')}><RotateCw aria-hidden="true" />{text.rotateRight}</Button>
            <Button type="button" variant="outline" disabled={locked} className={control} onClick={() => { const next = resetCapturedPhotoGeometry(albumRef.current, selected.id); if (next) { apply(next); setCropError(false) } }}>{text.reset}</Button></div>
          <form onSubmit={cropPhoto} noValidate className="min-w-0 space-y-3"><h4 className="text-lg font-semibold">{text.cropTitle}</h4><p className="text-sm leading-relaxed text-muted-foreground">{text.cropHelp}</p>
            <div className="grid min-w-0 gap-3 sm:grid-cols-2">{([['x', text.cropX], ['y', text.cropY], ['width', text.cropWidth], ['height', text.cropHeight]] as const).map(([key, label]) => <div key={key} className="min-w-0 space-y-2"><label className="block text-base font-medium" htmlFor={`capture-crop-${key}`}>{label}</label><Input id={`capture-crop-${key}`} type="text" inputMode="numeric" autoComplete="off" maxLength={20} value={crop[key]} disabled={locked} aria-invalid={cropError} aria-describedby={cropError ? 'capture-crop-error' : undefined} className="min-h-12 text-base md:text-base" onChange={(event) => { const value = event.target.value; setCrops((previous) => ({ ...previous, [selected.id]: { ...(previous[selected.id] ?? emptyCrop()), [key]: value } })); setCropError(false) }} /></div>)}</div>
            {cropError && <p id="capture-crop-error" role="alert" className="text-sm text-destructive">{text.cropError}</p>}<Button type="submit" variant="outline" disabled={locked} className={control}>{text.crop}</Button>
          </form>
        </div>}
      </section>
    </div>
    <AlertDialog open={confirmation !== undefined} onOpenChange={(open) => { if (!open) setConfirmation(undefined) }}><AlertDialogContent className="max-h-[calc(100dvh-2rem)] w-[calc(100%_-_2rem)] max-w-lg animate-none! overflow-y-auto p-4 sm:p-6"
      onOpenAutoFocus={(event) => { event.preventDefault(); confirmationHeading.current?.focus({ preventScroll: true }) }}
      onCloseAutoFocus={(event) => {
        event.preventDefault()
        const trigger = confirmationTrigger.current
        const destination = confirmedAction.current === 'remove' ? previewHeading.current ?? startButton.current ?? cameraHeading.current
          : confirmedAction.current === 'retake' ? cameraHeading.current : trigger?.isConnected && !trigger.disabled ? trigger : cameraHeading.current
        destination?.focus(); confirmationTrigger.current = null; confirmedAction.current = undefined
      }}
      onEscapeKeyDown={(event) => event.preventDefault()} onKeyDownCapture={(event) => { if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); setConfirmation(undefined) } }}>
      <AlertDialogHeader className="place-items-start text-left"><AlertDialogTitle ref={confirmationHeading} tabIndex={-1} className="rounded-sm text-xl [overflow-wrap:anywhere] focus-visible:outline-2 focus-visible:outline-ring">{confirmation === 'retake' ? text.retakeTitle : text.removeTitle}</AlertDialogTitle><AlertDialogDescription className="text-base leading-relaxed">{confirmation === 'retake' ? text.retakeHelp : text.removeHelp}</AlertDialogDescription></AlertDialogHeader>
      <AlertDialogFooter><AlertDialogCancel className={control}>{text.cancel}</AlertDialogCancel><AlertDialogAction className={control} onClick={() => { const choice = confirmation; confirmedAction.current = choice; setConfirmation(undefined); if (choice === 'retake' && selectedID !== undefined) start(selectedID); else if (choice === 'remove') remove() }}>{confirmation === 'retake' ? text.confirmRetake : text.confirmRemove}</AlertDialogAction></AlertDialogFooter>
    </AlertDialogContent></AlertDialog>
    </div>}
    <DraftLeaveGuard labels={text} when={album.photos.length > 0} canLeave={canLeave} onLeave={leave} onBlocked={blocked} onStay={stay} />
  </>
}
