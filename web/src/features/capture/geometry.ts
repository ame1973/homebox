import { maxFrameDimension, maxFramePixels } from '@/lib/capture-frame'

export type PhotoRect = Readonly<{ x: number; y: number; width: number; height: number }>
// Source dimensions refer to the decoded, oriented pixels; a later renderer
// must reject a source whose dimensions do not match this original snapshot.
export type PhotoGeometry = Readonly<{ sourceWidth: number; sourceHeight: number; crop: PhotoRect; turns: 0 | 1 | 2 | 3 }>

type Size = Readonly<{ width: number; height: number }>

function validDimension(value: number): boolean {
  return Number.isSafeInteger(value) && value >= 1 && value <= maxFrameDimension
}

function validRect(rect: PhotoRect, outer: Size): boolean {
  return !!rect && typeof rect === 'object' && Number.isSafeInteger(rect.x) && Number.isSafeInteger(rect.y) && rect.x >= 0 && rect.y >= 0 &&
    validDimension(rect.width) && validDimension(rect.height) && rect.x + rect.width <= outer.width && rect.y + rect.height <= outer.height
}

export function validPhotoGeometry(value: PhotoGeometry): boolean {
  return !!value && typeof value === 'object' && validDimension(value.sourceWidth) && validDimension(value.sourceHeight) && value.sourceWidth * value.sourceHeight <= maxFramePixels &&
    Number.isSafeInteger(value.turns) && value.turns >= 0 && value.turns <= 3 && validRect(value.crop, { width: value.sourceWidth, height: value.sourceHeight })
}

export function newPhotoGeometry(width: number, height: number): PhotoGeometry | undefined {
  const geometry: PhotoGeometry = { sourceWidth: width, sourceHeight: height, crop: { x: 0, y: 0, width, height }, turns: 0 }
  return validPhotoGeometry(geometry) ? geometry : undefined
}

export function photoDisplaySize(value: PhotoGeometry): Size | undefined {
  if (!validPhotoGeometry(value)) return undefined
  return value.turns % 2 ? { width: value.crop.height, height: value.crop.width } : { width: value.crop.width, height: value.crop.height }
}

// A rotation changes only display orientation; the crop stays in source pixels.
export function rotatePhotoGeometry(value: PhotoGeometry, direction: 'clockwise' | 'counterclockwise'): PhotoGeometry | undefined {
  if (!validPhotoGeometry(value) || direction !== 'clockwise' && direction !== 'counterclockwise') return undefined
  const turns = (value.turns + (direction === 'clockwise' ? 1 : 3)) % 4
  if (turns !== 0 && turns !== 1 && turns !== 2 && turns !== 3) return undefined
  return { ...value, crop: { ...value.crop }, turns }
}

// Selection uses current displayed pixel coordinates; preserve the exact source
// rectangle so repeated crop/rotation never resamples or silently enlarges it.
export function cropPhotoGeometry(value: PhotoGeometry, selection: PhotoRect): PhotoGeometry | undefined {
  const display = photoDisplaySize(value)
  if (!display || !validRect(selection, display)) return undefined
  const { crop, turns } = value
  const { x, y, width, height } = selection
  const next: PhotoRect = turns === 0 ? { x: crop.x + x, y: crop.y + y, width, height }
    : turns === 1 ? { x: crop.x + y, y: crop.y + crop.height - x - width, width: height, height: width }
      : turns === 2 ? { x: crop.x + crop.width - x - width, y: crop.y + crop.height - y - height, width, height }
        : { x: crop.x + crop.width - y - height, y: crop.y + x, width: height, height: width }
  const result: PhotoGeometry = { ...value, crop: next }
  return validPhotoGeometry(result) ? result : undefined
}
