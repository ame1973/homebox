type Lease = Readonly<{ kind: 'held'; release: () => boolean; timeout: () => void }>
type Acquisition = Lease | Readonly<{ kind: 'busy' | 'timeout' }>

// Native image decoding and canvas encoding cannot be canceled. Share one
// document-wide slot across photo rendering and selected-image normalization.
let owner: object | undefined, timedOut = false

export function acquirePhotoProcessing(): Acquisition {
  if (timedOut) return { kind: 'timeout' }
  if (owner) return { kind: 'busy' }
  const token = {}; owner = token
  let released = false
  return {
    kind: 'held',
    release: () => {
      if (released) return false
      released = true
      if (owner === token) owner = undefined
      return true
    },
    timeout: () => { if (!released && owner === token) timedOut = true },
  }
}
