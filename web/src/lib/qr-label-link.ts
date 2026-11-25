import { parseHomeboxQR, type HomeboxQR } from './scan-qr'
import { makeQRLabelTarget, type QRLabelKind } from './qr-label-target'

export type { QRLabelKind } from './qr-label-target'
export type QRLabelLink = Readonly<HomeboxQR & { url: string }>

// This validates routing representation only. The caller must supply the
// actual application origin and establish current session and source authority.
export function makeQRLabelLink(kind: QRLabelKind, id: string, origin: string): QRLabelLink {
  const target = makeQRLabelTarget(kind, id, origin)
  const destination = parseHomeboxQR(target.url, origin)
  if (!destination || destination.kind !== kind || destination.id !== id) throw new Error('Invalid QR label target')
  return Object.freeze({ ...destination, url: target.url })
}
