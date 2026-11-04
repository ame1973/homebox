// Only same-application location routes may survive an authentication round-trip.
// Never accept an arbitrary URL/redirect target from history state or a query.
export function locationReturnPath(value: unknown): string | undefined {
  if (typeof value !== 'string' || value.length > 2048 || !value.startsWith('/locations') || value.includes('\\') || value.includes('#')) return undefined
  try {
    const url = new URL(value, 'https://homebox.invalid')
    if (url.origin !== 'https://homebox.invalid') return undefined
    const match = /^\/locations(?:\/([^/]+)(\/edit)?)?$/.exec(url.pathname)
    if (!match) return undefined
    const validID = (id: string) => id.length > 0 && id.length <= 256 && Array.from(id).length <= 128 &&
      !['.', '..', 'tree'].includes(id) && !['/', '\\', '\0'].some((character) => id.includes(character))
    const id = match[1] ? decodeURIComponent(match[1]) : undefined
    if (id !== undefined && (!validID(id) || id === 'new' && match[2])) return undefined
    if (url.search) {
      if (id !== 'new') return undefined
      const entries = Array.from(url.searchParams.entries())
      if (entries.length !== 1 || entries[0]?.[0] !== 'parent' || !validID(entries[0][1])) return undefined
    }
    return url.pathname + url.search
  } catch { return undefined }
}
