import { describe, expect, it } from 'vitest'
import { cropPhotoGeometry, newPhotoGeometry, photoDisplaySize, rotatePhotoGeometry, validPhotoGeometry, type PhotoGeometry } from './geometry'

function required<T>(value: T | undefined): T {
  if (value === undefined) throw new Error('Expected valid photo geometry')
  return value
}

function pixels(width: number, height: number): number[][] {
  return Array.from({ length: height }, (_, y) => Array.from({ length: width }, (_, x) => y * 100 + x))
}

function turnClockwise(image: number[][]): number[][] {
  const height = image.length, width = image[0]?.length ?? 0
  return Array.from({ length: width }, (_, y) => Array.from({ length: height }, (_, x) => image[height - 1 - x]![y]!))
}

function renderGeometry(geometry: PhotoGeometry): number[][] {
  const source = pixels(geometry.sourceWidth, geometry.sourceHeight), crop = geometry.crop
  let result = source.slice(crop.y, crop.y + crop.height).map((row) => row.slice(crop.x, crop.x + crop.width))
  for (let index = 0; index < geometry.turns; index++) result = turnClockwise(result)
  return result
}

describe('lossless photo crop and rotation geometry', () => {
  it('retains the source dimensions and whole original rectangle', () => {
    const geometry = required(newPhotoGeometry(640, 480))
    expect(geometry).toEqual({ sourceWidth: 640, sourceHeight: 480, crop: { x: 0, y: 0, width: 640, height: 480 }, turns: 0 })
    expect(photoDisplaySize(geometry)).toEqual({ width: 640, height: 480 })
    expect(validPhotoGeometry(geometry)).toBe(true)
  })

  it('rotates clockwise and counterclockwise without changing original crop pixels', () => {
    const original = required(newPhotoGeometry(4, 3))
    const cropped = required(cropPhotoGeometry(original, { x: 1, y: 1, width: 2, height: 2 }))
    const rotated = required(rotatePhotoGeometry(cropped, 'clockwise'))
    expect(rotated.crop).toEqual({ x: 1, y: 1, width: 2, height: 2 })
    expect(rotated.crop).not.toBe(cropped.crop)
    expect(renderGeometry(rotated)).toEqual(turnClockwise(renderGeometry(cropped)))
    expect(rotatePhotoGeometry(rotated, 'counterclockwise')).toEqual(cropped)
    expect(original).toEqual(newPhotoGeometry(4, 3))
  })

  it('maps every possible displayed rectangle back to the correct source pixels', () => {
    for (let sourceWidth = 1; sourceWidth <= 4; sourceWidth++) {
      for (let sourceHeight = 1; sourceHeight <= 4; sourceHeight++) {
        let geometry = required(newPhotoGeometry(sourceWidth, sourceHeight))
        for (let turns = 0; turns < 4; turns++) {
          const before = renderGeometry(geometry), display = required(photoDisplaySize(geometry))
          expect(before.length).toBe(display.height); expect(before[0]).toHaveLength(display.width)
          for (let y = 0; y < display.height; y++) {
            for (let x = 0; x < display.width; x++) {
              for (let height = 1; height <= display.height - y; height++) {
                for (let width = 1; width <= display.width - x; width++) {
                  const selected = before.slice(y, y + height).map((row) => row.slice(x, x + width))
                  const next = required(cropPhotoGeometry(geometry, { x, y, width, height }))
                  expect(renderGeometry(next)).toEqual(selected)
                  expect(photoDisplaySize(next)).toEqual({ width, height })
                }
              }
            }
          }
          geometry = required(rotatePhotoGeometry(geometry, 'clockwise'))
        }
        expect(geometry).toEqual(newPhotoGeometry(sourceWidth, sourceHeight))
      }
    }
  })

  it('composes multiple rotations and crops without sampling outside the previous result', () => {
    let geometry = required(newPhotoGeometry(5, 4)), expected = pixels(5, 4)
    geometry = required(rotatePhotoGeometry(geometry, 'clockwise')); expected = turnClockwise(expected)
    geometry = required(cropPhotoGeometry(geometry, { x: 1, y: 1, width: 2, height: 3 }))
    expected = expected.slice(1, 4).map((row) => row.slice(1, 3))
    geometry = required(rotatePhotoGeometry(geometry, 'clockwise')); expected = turnClockwise(expected)
    geometry = required(cropPhotoGeometry(geometry, { x: 0, y: 1, width: 2, height: 1 }))
    expected = expected.slice(1, 2).map((row) => row.slice(0, 2))
    expect(renderGeometry(geometry)).toEqual(expected)
    expect(photoDisplaySize(geometry)).toEqual({ width: 2, height: 1 })
  })

  it('rejects malformed dimensions, unsafe pixels, and invalid crop selections', () => {
    for (const [width, height] of [[0, 1], [1, 0], [-1, 1], [1.5, 1], [Number.NaN, 1], [Number.POSITIVE_INFINITY, 1],
      [16_385, 1], [16_384, 16_384]] as const) expect(newPhotoGeometry(width, height)).toBeUndefined()
    const maximum = required(newPhotoGeometry(8_000, 8_000))
    expect(cropPhotoGeometry(maximum, { x: 7_999, y: 7_999, width: 1, height: 1 })?.crop).toEqual({ x: 7_999, y: 7_999, width: 1, height: 1 })
    const original = required(newPhotoGeometry(4, 3))
    for (const selection of [
      { x: -1, y: 0, width: 1, height: 1 }, { x: 0, y: -1, width: 1, height: 1 },
      { x: 0, y: 0, width: 0, height: 1 }, { x: 0, y: 0, width: 1, height: 0 },
      { x: 3, y: 0, width: 2, height: 1 }, { x: 0, y: 2, width: 1, height: 2 },
      { x: 0.5, y: 0, width: 1, height: 1 }, { x: 0, y: Number.NaN, width: 1, height: 1 },
      { x: 0, y: 0, width: Number.POSITIVE_INFINITY, height: 1 },
    ]) expect(cropPhotoGeometry(original, selection)).toBeUndefined()
    expect(original).toEqual(newPhotoGeometry(4, 3))
  })

  it('rejects invalid retained geometry and directions rather than repairing them', () => {
    const valid = required(newPhotoGeometry(4, 3))
    for (const malformed of [null, undefined, {}, { ...valid, crop: null }, { ...valid, turns: 4 },
      { ...valid, sourceWidth: 2 }, { ...valid, crop: { x: 1, y: 0, width: 4, height: 1 } }]) {
      const value = malformed as PhotoGeometry
      expect(validPhotoGeometry(value)).toBe(false)
      expect(photoDisplaySize(value)).toBeUndefined()
      expect(rotatePhotoGeometry(value, 'clockwise')).toBeUndefined()
      expect(cropPhotoGeometry(value, { x: 0, y: 0, width: 1, height: 1 })).toBeUndefined()
    }
    expect(cropPhotoGeometry(valid, null as never)).toBeUndefined()
    expect(rotatePhotoGeometry(valid, 'invalid' as never)).toBeUndefined()
  })
})
