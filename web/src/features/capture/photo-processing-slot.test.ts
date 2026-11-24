import { expect, it, vi } from 'vitest'

it('keeps a single owner, ignores stale completion/timeout and latches a current timeout until reload', async () => {
  vi.resetModules()
  const { acquirePhotoProcessing } = await import('./photo-processing-slot')
  const first = acquirePhotoProcessing()
  if (first.kind !== 'held') throw new Error('Expected first lease')
  expect(acquirePhotoProcessing()).toEqual({ kind: 'busy' })
  expect(first.release()).toBe(true); expect(first.release()).toBe(false)
  const second = acquirePhotoProcessing()
  if (second.kind !== 'held') throw new Error('Expected second lease')
  first.timeout(); expect(first.release()).toBe(false)
  expect(acquirePhotoProcessing()).toEqual({ kind: 'busy' })
  second.timeout(); expect(acquirePhotoProcessing()).toEqual({ kind: 'timeout' })
  expect(second.release()).toBe(true)
  expect(acquirePhotoProcessing()).toEqual({ kind: 'timeout' })
})
