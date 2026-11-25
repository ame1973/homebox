import { pathIDSchema } from './api/domain'
import { parseHomeboxQR, type HomeboxQR } from './scan-qr'

export type QRLabelKind = HomeboxQR['kind']
export type QRLabelLink = Readonly<HomeboxQR & { url: string }>

// This validates routing representation only. The caller must supply the
// actual application origin and establish current session and source authority.
export function makeQRLabelLink(kind: QRLabelKind, id: string, origin: string): QRLabelLink {
  const invalid = () => new Error('Invalid QR label target')
  if ((kind !== 'location' && kind !== 'unit') || typeof id !== 'string' || !pathIDSchema.safeParse(id).success || /[^A-Za-z0-9._~-]/u.test(id) ||
    typeof origin !== 'string' || origin.length > 512) throw invalid()
  let trusted: URL
  try { trusted = new URL(origin) } catch { throw invalid() }
  if (!['http:', 'https:'].includes(trusted.protocol) || trusted.origin !== origin || trusted.username || trusted.password || trusted.pathname !== '/' || trusted.search || trusted.hash) throw invalid()
  const url = `${origin}/${kind === 'location' ? 'l' : 'u'}/${id}`
  const destination = parseHomeboxQR(url, origin)
  if (!destination || destination.kind !== kind || destination.id !== id) throw invalid()
  return Object.freeze({ ...destination, url })
}
