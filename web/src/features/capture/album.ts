import { isCapturedFrame, maxFrameBytes, type CapturedFrame } from '@/lib/capture-frame'
import { cropPhotoGeometry, newPhotoGeometry, rotatePhotoGeometry, type PhotoGeometry, type PhotoRect } from './geometry'
import { isImportedFrame, type ImportedFrame } from './image-normalize'

export const maxCapturePhotos = 10
export const maxCaptureAlbumBytes = 64 * 1024 * 1024
export const maxCaptureDescriptionUnits = 20_000

export type AlbumFrame = CapturedFrame | ImportedFrame
export type CapturePhoto = Readonly<{
  id: number
  frame: AlbumFrame
  geometry: PhotoGeometry
  description: string
}>
export type CaptureAlbum = Readonly<{ photos: readonly CapturePhoto[]; coverID: number | null }>
export type CaptureAlbumResult =
  | Readonly<{ kind: 'changed'; album: CaptureAlbum }>
  | Readonly<{ kind: 'invalid' | 'duplicate' | 'missing' | 'count-limit' | 'bytes-limit' }>

// This is volatile editing state, not a saved draft, upload, membership proof
// or write queue. The caller owns Blob lifetime and must clear all snapshots on
// session loss, page suspension and departure.
export function emptyCaptureAlbum(): CaptureAlbum {
  return { photos: [], coverID: null }
}

function validLocalID(id: number): boolean {
  return Number.isSafeInteger(id) && id > 0
}

function frameBytes(frame: AlbumFrame): number | undefined {
  if (!isCapturedFrame(frame) && !isImportedFrame(frame)) return undefined
  if (frame.kind === 'imported' && frame.blob.type !== frame.mime) return undefined
  const size = frame.blob.size
  return Number.isSafeInteger(size) && size >= 1 && size <= maxFrameBytes ? size : undefined
}

function albumBytes(album: CaptureAlbum): number | undefined {
  let total = 0
  for (const photo of album.photos) {
    const size = frameBytes(photo.frame)
    if (size === undefined || size > maxCaptureAlbumBytes - total) return undefined
    total += size
  }
  return total
}

function frameGeometry(frame: AlbumFrame): PhotoGeometry | undefined {
  return frameBytes(frame) === undefined ? undefined : newPhotoGeometry(frame.width, frame.height)
}

export function addCapturedPhoto(album: CaptureAlbum, id: number, frame: AlbumFrame): CaptureAlbumResult {
  if (!validLocalID(id)) return { kind: 'invalid' }
  const geometry = frameGeometry(frame)
  if (!geometry) return { kind: 'invalid' }
  if (album.photos.some((photo) => photo.id === id)) return { kind: 'duplicate' }
  if (album.photos.length >= maxCapturePhotos) return { kind: 'count-limit' }
  const total = albumBytes(album), size = frameBytes(frame)
  if (total === undefined || size === undefined) return { kind: 'invalid' }
  if (size > maxCaptureAlbumBytes - total) return { kind: 'bytes-limit' }
  return { kind: 'changed', album: { ...album, photos: [...album.photos, { id, frame, geometry, description: '' }] } }
}

// Retaking replaces only the selected source and geometry; the local order,
// explicit cover choice and raw description are not silently changed.
export function replaceCapturedPhoto(album: CaptureAlbum, id: number, frame: AlbumFrame): CaptureAlbumResult {
  const index = album.photos.findIndex((photo) => photo.id === id)
  if (index < 0) return { kind: 'missing' }
  const geometry = frameGeometry(frame)
  if (!geometry) return { kind: 'invalid' }
  const current = album.photos[index]!, total = albumBytes(album), previousSize = frameBytes(current.frame), size = frameBytes(frame)
  if (total === undefined || previousSize === undefined || size === undefined) return { kind: 'invalid' }
  if (size > maxCaptureAlbumBytes - (total - previousSize)) return { kind: 'bytes-limit' }
  return { kind: 'changed', album: replaceAt(album, index, { ...current, frame, geometry }) }
}

function replaceAt(album: CaptureAlbum, index: number, photo: CapturePhoto): CaptureAlbum {
  const photos = album.photos.slice()
  photos[index] = photo
  return { ...album, photos }
}

export function removeCapturedPhoto(album: CaptureAlbum, id: number): CaptureAlbum | undefined {
  const index = album.photos.findIndex((photo) => photo.id === id)
  if (index < 0) return undefined
  return { photos: album.photos.filter((photo) => photo.id !== id), coverID: album.coverID === id ? null : album.coverID }
}

export function moveCapturedPhoto(album: CaptureAlbum, id: number, direction: 'earlier' | 'later'): CaptureAlbum | undefined {
  if (direction !== 'earlier' && direction !== 'later') return undefined
  const index = album.photos.findIndex((photo) => photo.id === id)
  if (index < 0) return undefined
  const target = index + (direction === 'earlier' ? -1 : 1)
  if (target < 0 || target >= album.photos.length) return album
  const photos = album.photos.slice(), selected = photos[index]!
  photos[index] = photos[target]!
  photos[target] = selected
  return { ...album, photos }
}

export function chooseCaptureCover(album: CaptureAlbum, id: number | null): CaptureAlbum | undefined {
  if (id !== null && !album.photos.some((photo) => photo.id === id)) return undefined
  return { ...album, coverID: id }
}

// Keep raw text, including temporarily invalid Unicode, for explicit review.
// The owner-link writer still validates its complete 10,000-code-point limit.
export function describeCapturedPhoto(album: CaptureAlbum, id: number, description: string): CaptureAlbum | undefined {
  const index = album.photos.findIndex((photo) => photo.id === id)
  if (index < 0 || typeof description !== 'string' || description.length > maxCaptureDescriptionUnits) return undefined
  return replaceAt(album, index, { ...album.photos[index]!, description })
}

export function rotateCapturedPhoto(album: CaptureAlbum, id: number, direction: 'clockwise' | 'counterclockwise'): CaptureAlbum | undefined {
  const index = album.photos.findIndex((photo) => photo.id === id)
  if (index < 0) return undefined
  const photo = album.photos[index]!, geometry = rotatePhotoGeometry(photo.geometry, direction)
  return geometry ? replaceAt(album, index, { ...photo, geometry }) : undefined
}

export function cropCapturedPhoto(album: CaptureAlbum, id: number, selection: PhotoRect): CaptureAlbum | undefined {
  const index = album.photos.findIndex((photo) => photo.id === id)
  if (index < 0) return undefined
  const photo = album.photos[index]!, geometry = cropPhotoGeometry(photo.geometry, selection)
  return geometry ? replaceAt(album, index, { ...photo, geometry }) : undefined
}

export function resetCapturedPhotoGeometry(album: CaptureAlbum, id: number): CaptureAlbum | undefined {
  const index = album.photos.findIndex((photo) => photo.id === id)
  if (index < 0) return undefined
  const photo = album.photos[index]!, geometry = frameGeometry(photo.frame)
  return geometry ? replaceAt(album, index, { ...photo, geometry }) : undefined
}
