import { validText } from './api/client'
import { pathIDSchema } from './api/domain'
import { holdingReturnPath, unitRoute } from './holding-navigation'
import { locationReturnPath, locationRoute } from './location-navigation'

export type HomeboxQR = Readonly<{ kind: 'location' | 'unit'; id: string; path: string }>

function validURLText(value: string, maximum: number): boolean {
  if (!validText(value, 1, maximum)) return false
  for (const character of value) {
    const code = character.charCodeAt(0)
    if (code <= 0x20 || code === 0x7f || character === '\\') return false
  }
  return true
}

// Never return the scanned URL itself: only an exact, same-origin QR identity
// can become a validated internal route. Other text remains untrusted display data.
export function parseHomeboxQR(value: unknown, origin: unknown): HomeboxQR | undefined {
  if (typeof value !== 'string' || typeof origin !== 'string' || !validURLText(value, 4096) || !validURLText(origin, 2048)) return undefined
  const rawPath = /^https?:\/\/[^/?#@]+(\/[^?#]*)$/i.exec(value)?.[1]
  if (!rawPath || !/^https?:\/\/[^/?#@]+\/?$/i.test(origin)) return undefined
  const match = /^\/(l|u)\/([^/]+)$/.exec(rawPath)
  if (!match?.[1] || !match[2]) return undefined
  try {
    const scanned = new URL(value)
    const trusted = new URL(origin)
    if (scanned.origin !== trusted.origin || scanned.username || scanned.password || trusted.username || trusted.password) return undefined
    const id = decodeURIComponent(match[2])
    // React Router treats a literal encoded-slash spelling in an ID as a slash
    // after decoding, so it cannot safely identify the original path resource.
    if (!pathIDSchema.safeParse(id).success || id.includes('%2F')) return undefined
    if (match[1] === 'l') {
      const path = locationReturnPath(locationRoute(id))
      return path ? { kind: 'location', id, path } : undefined
    }
    const path = holdingReturnPath(unitRoute(id))
    return path ? { kind: 'unit', id, path } : undefined
  } catch { return undefined }
}
