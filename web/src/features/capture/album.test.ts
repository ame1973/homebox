import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { captureVideoFrame, maxFrameBytes, type CapturedFrame } from '@/lib/capture-frame'
import { photoDisplaySize } from './geometry'
import {
  addCapturedPhoto, chooseCaptureCover, cropCapturedPhoto, describeCapturedPhoto, emptyCaptureAlbum,
  maxCaptureAlbumBytes, maxCaptureDescriptionUnits, maxCapturePhotos, moveCapturedPhoto, removeCapturedPhoto,
  replaceCapturedPhoto, resetCapturedPhotoGeometry, rotateCapturedPhoto, type CaptureAlbum, type CaptureAlbumResult,
} from './album'

let blob: Blob

beforeEach(() => {
  vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
  vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockImplementation((() => ({ drawImage: vi.fn() } as unknown as CanvasRenderingContext2D)) as unknown as typeof HTMLCanvasElement.prototype.getContext)
  vi.spyOn(HTMLCanvasElement.prototype, 'toBlob').mockImplementation((callback) => callback(blob))
})
afterEach(() => { vi.restoreAllMocks() })

async function capture(size = 1, width = 4, height = 3): Promise<CapturedFrame> {
  blob = new Blob([new Uint8Array([0xff])], { type: 'image/jpeg' })
  Object.defineProperty(blob, 'size', { value: size, configurable: true })
  const track = { readyState: 'live' }
  const stream = { getVideoTracks: () => [track], getAudioTracks: () => [] } as unknown as MediaStream
  const video = { srcObject: stream, readyState: 2, videoWidth: width, videoHeight: height } as HTMLVideoElement
  const result = await captureVideoFrame(video, stream, new AbortController().signal)
  if (result.kind !== 'captured') throw new Error(`Capture fixture failed: ${result.kind}`)
  return result
}

function changed(result: CaptureAlbumResult): CaptureAlbum {
  if (result.kind !== 'changed') throw new Error(`Album fixture failed: ${result.kind}`)
  return result.album
}
function required(value: CaptureAlbum | undefined): CaptureAlbum {
  if (!value) throw new Error('Album operation failed')
  return value
}

describe('volatile capture album', () => {
  it('adds only genuine camera captures with independent local identities', async () => {
    const frame = await capture(), initial = emptyCaptureAlbum()
    const first = changed(addCapturedPhoto(initial, 1, frame)), second = changed(addCapturedPhoto(first, 2, frame))
    expect(initial).toEqual({ photos: [], coverID: null })
    expect(first.photos).toHaveLength(1); expect(second.photos.map((photo) => photo.id)).toEqual([1, 2])
    expect(second.photos[0]?.frame).toBe(frame); expect(second.photos[0]?.description).toBe('')
    expect(second.coverID).toBeNull()
    expect(addCapturedPhoto(second, 2, frame)).toEqual({ kind: 'duplicate' })
    for (const id of [0, -1, 1.5, Number.NaN, Number.MAX_SAFE_INTEGER + 1]) {
      expect(addCapturedPhoto(second, id, frame)).toEqual({ kind: 'invalid' })
    }
    expect(addCapturedPhoto(second, 3, { ...frame })).toEqual({ kind: 'invalid' })
  })

  it('enforces explicit photo-count and encoded-byte budgets without truncation', async () => {
    const small = await capture(), big = await capture(maxFrameBytes), medium = await capture(maxFrameBytes / 2)
    let album = emptyCaptureAlbum()
    for (let id = 1; id <= maxCapturePhotos; id++) album = changed(addCapturedPhoto(album, id, small))
    expect(addCapturedPhoto(album, maxCapturePhotos + 1, small)).toEqual({ kind: 'count-limit' })
    let bounded = changed(addCapturedPhoto(emptyCaptureAlbum(), 1, big))
    bounded = changed(addCapturedPhoto(bounded, 2, medium)); bounded = changed(addCapturedPhoto(bounded, 3, medium))
    expect(bounded.photos.reduce((sum, photo) => sum + photo.frame.blob.size, 0)).toBe(maxCaptureAlbumBytes)
    expect(addCapturedPhoto(bounded, 4, small)).toEqual({ kind: 'bytes-limit' })
    expect(replaceCapturedPhoto(bounded, 2, big)).toEqual({ kind: 'bytes-limit' })
    expect(bounded.photos[1]?.frame).toBe(medium)
    bounded = changed(replaceCapturedPhoto(bounded, 2, small))
    expect(addCapturedPhoto(bounded, 4, small).kind).toBe('changed')
  })

  it('keeps the chosen cover attached to its photo through reorder and clears it on removal', async () => {
    const frame = await capture()
    let album = changed(addCapturedPhoto(emptyCaptureAlbum(), 1, frame))
    album = changed(addCapturedPhoto(album, 2, frame)); album = changed(addCapturedPhoto(album, 3, frame))
    const original = album
    album = required(chooseCaptureCover(album, 2)); album = required(moveCapturedPhoto(album, 2, 'later'))
    expect(original.photos.map((photo) => photo.id)).toEqual([1, 2, 3])
    expect(album.photos.map((photo) => photo.id)).toEqual([1, 3, 2]); expect(album.coverID).toBe(2)
    expect(moveCapturedPhoto(album, 2, 'later')).toBe(album)
    expect(required(moveCapturedPhoto(album, 2, 'earlier')).photos.map((photo) => photo.id)).toEqual([1, 2, 3])
    expect(required(removeCapturedPhoto(album, 1)).coverID).toBe(2)
    expect(moveCapturedPhoto(album, 99, 'earlier')).toBeUndefined()
    expect(moveCapturedPhoto(album, 1, 'elsewhere' as never)).toBeUndefined()
    expect(chooseCaptureCover(album, 99)).toBeUndefined()
    const removed = required(removeCapturedPhoto(album, 2))
    expect(removed.photos.map((photo) => photo.id)).toEqual([1, 3]); expect(removed.coverID).toBeNull()
    expect(removeCapturedPhoto(removed, 2)).toBeUndefined()
    expect(required(chooseCaptureCover(album, null)).coverID).toBeNull()
  })

  it('keeps original pixels and raw descriptions through crop, rotation, reset and retake', async () => {
    const original = await capture(), retake = await capture(2, 7, 5)
    let album = changed(addCapturedPhoto(emptyCaptureAlbum(), 4, original))
    album = required(chooseCaptureCover(album, 4))
    album = required(describeCapturedPhoto(album, 4, 'line\n\ud800'))
    const beforeEdit = album
    album = required(cropCapturedPhoto(album, 4, { x: 1, y: 1, width: 3, height: 2 }))
    album = required(rotateCapturedPhoto(album, 4, 'clockwise'))
    expect(photoDisplaySize(album.photos[0]!.geometry)).toEqual({ width: 2, height: 3 })
    expect(album.photos[0]?.frame).toBe(original); expect(album.photos[0]?.description).toBe('line\n\ud800')
    expect(beforeEdit.photos[0]?.geometry.crop).toEqual({ x: 0, y: 0, width: 4, height: 3 })
    expect(cropCapturedPhoto(album, 4, { x: 0, y: 0, width: 3, height: 3 })).toBeUndefined()
    const reset = required(resetCapturedPhotoGeometry(album, 4))
    expect(reset.photos[0]?.geometry).toEqual({ sourceWidth: 4, sourceHeight: 3, crop: { x: 0, y: 0, width: 4, height: 3 }, turns: 0 })
    const replaced = changed(replaceCapturedPhoto(album, 4, retake))
    expect(replaced.photos[0]?.frame).toBe(retake); expect(replaced.photos[0]?.description).toBe('line\n\ud800')
    expect(replaced.photos[0]?.geometry).toEqual({ sourceWidth: 7, sourceHeight: 5, crop: { x: 0, y: 0, width: 7, height: 5 }, turns: 0 })
    expect(replaced.coverID).toBe(4)
    expect(replaceCapturedPhoto(replaced, 99, retake)).toEqual({ kind: 'missing' })
    expect(replaceCapturedPhoto(replaced, 4, { ...retake })).toEqual({ kind: 'invalid' })
  })

  it('preserves bounded invalid text but never silently trims oversized input', async () => {
    const frame = await capture(), album = changed(addCapturedPhoto(emptyCaptureAlbum(), 1, frame))
    const text = '🙂'.repeat(maxCaptureDescriptionUnits / 2)
    const edited = required(describeCapturedPhoto(album, 1, text))
    expect(edited.photos[0]?.description).toBe(text)
    expect(describeCapturedPhoto(edited, 1, text + 'a')).toBeUndefined()
    expect(edited.photos[0]?.description).toBe(text)
    expect(describeCapturedPhoto(edited, 99, 'text')).toBeUndefined()
    expect(describeCapturedPhoto(edited, 1, null as never)).toBeUndefined()
    expect(rotateCapturedPhoto(edited, 99, 'clockwise')).toBeUndefined()
    expect(rotateCapturedPhoto(edited, 1, 'diagonal' as never)).toBeUndefined()
    expect(cropCapturedPhoto(edited, 99, { x: 0, y: 0, width: 1, height: 1 })).toBeUndefined()
    expect(resetCapturedPhotoGeometry(edited, 99)).toBeUndefined()
  })

  it('rejects an altered or non-finite Blob size before changing an album', async () => {
    const frame = await capture(), replacement = await capture(), album = changed(addCapturedPhoto(emptyCaptureAlbum(), 1, frame))
    Object.defineProperty(frame.blob, 'size', { value: Number.NaN, configurable: true })
    expect(addCapturedPhoto(album, 2, frame)).toEqual({ kind: 'invalid' })
    expect(addCapturedPhoto(album, 2, replacement)).toEqual({ kind: 'invalid' })
    expect(replaceCapturedPhoto(album, 1, frame)).toEqual({ kind: 'invalid' })
    expect(replaceCapturedPhoto(album, 1, replacement)).toEqual({ kind: 'invalid' })
    expect(resetCapturedPhotoGeometry(album, 1)).toBeUndefined()
  })
})
