import BarcodeFormat from '@zxing/library/esm/core/BarcodeFormat'
import EncodeHintType from '@zxing/library/esm/core/EncodeHintType'
import QRCodeWriter from '@zxing/library/esm/core/qrcode/QRCodeWriter'
import { makeQRLabelTarget, type QRLabelKind, type QRLabelTarget } from './qr-label-target'

export type QRLabelMatrix = Readonly<{ target: QRLabelTarget; size: number; modules: Uint8Array }>

// This synchronous encoder is internal groundwork. A caller must establish
// session/source authority, run it off the UI thread and discard stale results.
export function encodeQRLabel(kind: QRLabelKind, id: string, origin: string): QRLabelMatrix {
  const target = makeQRLabelTarget(kind, id, origin)
  try {
    const hints = new Map([[EncodeHintType.CHARACTER_SET, 'UTF-8'], [EncodeHintType.ERROR_CORRECTION, 'M']])
    const matrix = new QRCodeWriter().encode(target.url, BarcodeFormat.QR_CODE, 0, 0, hints)
    const size = matrix.getWidth()
    if (size !== matrix.getHeight() || size < 29 || size > 185 || (size - 25) % 4 !== 0) throw new Error('Unexpected QR matrix')
    const modules = new Uint8Array(size * size)
    for (let y = 0; y < size; y++) for (let x = 0; x < size; x++) modules[y * size + x] = matrix.get(x, y) ? 1 : 0
    return { target, size, modules }
  } catch { throw new Error('QR encoding failed') }
}
