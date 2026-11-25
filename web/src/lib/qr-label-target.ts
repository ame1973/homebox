export type QRLabelKind = 'location' | 'unit'
export type QRLabelTarget = Readonly<{ kind: QRLabelKind; id: string; url: string }>

// This is worker-safe representation validation, not an actual-origin,
// authenticated-session or retained-source check. The caller owns those gates.
export function makeQRLabelTarget(kind: QRLabelKind, id: string, origin: string): QRLabelTarget {
  const invalid = () => new Error('Invalid QR label target')
  if ((kind !== 'location' && kind !== 'unit') || typeof id !== 'string' || id.length < 1 || id.length > 128 ||
    id === '.' || id === '..' || kind === 'location' && id === 'tree' || /[^A-Za-z0-9._~-]/u.test(id) ||
    typeof origin !== 'string' || origin.length > 512) throw invalid()
  let trusted: URL
  try { trusted = new URL(origin) } catch { throw invalid() }
  if (!['http:', 'https:'].includes(trusted.protocol) || trusted.origin !== origin || trusted.username || trusted.password || trusted.pathname !== '/' || trusted.search || trusted.hash) throw invalid()
  return Object.freeze({ kind, id, url: `${origin}/${kind === 'location' ? 'l' : 'u'}/${id}` })
}
