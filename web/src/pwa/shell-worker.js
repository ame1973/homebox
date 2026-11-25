// This worker caches only the build's verified public shell, never private data.
const shellConfig = __HOMEBOX_SHELL_CONFIG__
const shellAssets = new Map(shellConfig.assets.map((entry) => [entry.path, entry]))
const offlineCSP = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
  "img-src 'self' blob: data:; font-src 'self'; connect-src 'self'; media-src 'self' blob:; " +
  "worker-src 'self' blob:; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'"

async function readBytes(response, limit, exact) {
  if (!response.body) {
    if (exact) throw new Error('Missing shell response body')
    return new Uint8Array()
  }
  const reader = response.body.getReader()
  const chunks = []
  let size = 0
  try {
    while (true) {
      const { value, done } = await reader.read()
      if (done) break
      if (value.byteLength > limit - size) throw new Error('Oversized shell response')
      chunks.push(value.slice())
      size += value.byteLength
    }
  } catch (error) {
    void reader.cancel().catch(() => {})
    throw error
  } finally {
    reader.releaseLock()
  }
  if (exact && size !== limit) throw new Error('Incomplete shell response')
  const result = new Uint8Array(size)
  let offset = 0
  for (const chunk of chunks) {
    result.set(chunk, offset)
    offset += chunk.byteLength
  }
  return result
}

async function verifyBytes(response, entry) {
  if (response.status !== 200) throw new Error('Invalid cached shell response')
  const bytes = await readBytes(response, entry.size, true)
  const digest = await crypto.subtle.digest('SHA-256', bytes)
  const actual = Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, '0')).join('')
  if (actual !== entry.sha256) throw new Error('Shell integrity mismatch')
  return bytes
}

// Do not trust mutable Cache Storage headers. Match the server's CSP and other
// security policy, including the non-cacheable HTML response, on every return.
function shellResponse(entry, bytes) {
  const mime = entry.mime[0]
  const charset = mime.startsWith('text/') || mime === 'application/manifest+json' ? '; charset=utf-8' : ''
  const cache = entry.path === '/index.html' ? 'no-store' : entry.path.startsWith('/assets/') ? 'public, max-age=31536000, immutable' : 'no-cache'
  const headers = new Headers({
    'Content-Type': mime + charset,
    'Cache-Control': cache,
    'Content-Security-Policy': offlineCSP,
    'X-Content-Type-Options': 'nosniff',
    'X-Frame-Options': 'DENY',
    'Referrer-Policy': 'no-referrer',
    'Permissions-Policy': 'camera=(self), microphone=(), geolocation=()',
  })
  return new Response(bytes, { status: 200, headers })
}

async function installShell() {
  const cache = await caches.open(shellConfig.cacheName)
  for (const entry of shellConfig.assets) {
    const controller = new AbortController()
    const timeout = setTimeout(() => controller.abort(), 15_000)
    try {
      // Go's static handler redirects /index.html to /, so fetch the canonical
      // public shell URL while storing it under the stable offline cache key.
      const source = entry.path === '/index.html' ? '/' : entry.path
      const response = await fetch(source, { cache: 'no-store', credentials: 'omit', redirect: 'error', signal: controller.signal })
      const type = (response.headers.get('Content-Type') ?? '').split(';', 1)[0].trim().toLowerCase()
      if (response.status !== 200 || response.redirected || !entry.mime.includes(type)) throw new Error('Invalid shell response')
      const bytes = await verifyBytes(response, entry)
      await cache.put(entry.path, shellResponse(entry, bytes))
    } finally {
      clearTimeout(timeout)
    }
  }
}

// Install failure leaves any old shell untouched. No automatic skipWaiting,
// clients.claim or cache deletion is performed by this worker.
self.addEventListener('install', (event) => {
  event.waitUntil(installShell())
})

function offlineNavigation(url) {
  let path
  try { path = decodeURIComponent(url.pathname) } catch { return false }
  if (!path.startsWith('/') || path.includes('\\') || path.includes('//')) return false
  for (const character of path) {
    const code = character.charCodeAt(0)
    if (code < 32 || code === 127) return false
  }
  if (path.split('/').some((segment) => segment.startsWith('.'))) return false
  for (const prefix of ['/api', '/auth', '/media', '/backups', '/exports', '/worker', '/data', '/secrets', '/dev', '/licenses']) {
    if (path === prefix || path.startsWith(`${prefix}/`)) return false
  }
  const lastSegment = path.replace(/\/$/, '').split('/').at(-1)
  return !lastSegment || !lastSegment.includes('.')
}

async function cached(path) {
  const entry = shellAssets.get(path)
  if (!entry) return undefined
  try {
    const response = await caches.match(path, { cacheName: shellConfig.cacheName })
    if (!response) return undefined
    return shellResponse(entry, await verifyBytes(response, entry))
  } catch {
    return undefined
  }
}

async function networkFirst(request, fallback) {
  const controller = new AbortController()
  const abort = () => controller.abort()
  if (request.signal.aborted) abort()
  else request.signal.addEventListener('abort', abort, { once: true })
  const timeout = setTimeout(abort, 8000)
  try {
    const response = await fetch(request, { signal: controller.signal })
    if (response.status >= 200 && response.status < 300 && response.status !== 200) throw new Error('Invalid shell response')
    if (response.status !== 200 || response.redirected) return response
    const bytes = await readBytes(response, 4 * 1024 * 1024, false)
    if (request.signal.aborted) throw new Error('Navigation canceled')
    const headers = new Headers(response.headers)
    headers.delete('Content-Length')
    headers.delete('Content-Encoding')
    headers.delete('Transfer-Encoding')
    return new Response(bytes, { status: response.status, statusText: response.statusText, headers })
  } catch (error) {
    if (request.signal.aborted) throw error
    const response = await cached(fallback)
    if (request.signal.aborted) throw error
    if (response) return response
    throw error
  } finally {
    clearTimeout(timeout)
    request.signal.removeEventListener('abort', abort)
  }
}

async function cacheFirst(request, entry, useCache) {
  const response = useCache ? await cached(entry.path) : undefined
  if (request.signal.aborted) throw new Error('Request canceled')
  if (response) return response
  const controller = new AbortController()
  const abort = () => controller.abort()
  request.signal.addEventListener('abort', abort, { once: true })
  const timeout = setTimeout(abort, 8000)
  try {
    const network = await fetch(request, { signal: controller.signal, redirect: 'error' })
    if (network.status !== 200) throw new Error('Invalid shell response')
    const type = (network.headers.get('Content-Type') ?? '').split(';', 1)[0].trim().toLowerCase()
    if (network.redirected || !entry.mime.includes(type)) throw new Error('Invalid shell response')
    const bytes = await verifyBytes(network, entry)
    if (request.signal.aborted) throw new Error('Request canceled')
    return shellResponse(entry, bytes)
  } finally {
    clearTimeout(timeout)
    request.signal.removeEventListener('abort', abort)
  }
}

self.addEventListener('fetch', (event) => {
  const request = event.request
  const url = new URL(request.url)
  if (request.method !== 'GET' || url.origin !== self.location.origin) return
  if (request.mode === 'navigate') {
    if (offlineNavigation(url)) event.respondWith(networkFirst(request, '/index.html'))
    return
  }
  if (request.headers.has('Range') || !shellAssets.has(url.pathname) || url.pathname === '/index.html') return
  if (url.pathname.startsWith('/assets/')) {
    const useCache = !url.search && !['no-store', 'reload', 'no-cache'].includes(request.cache)
    event.respondWith(cacheFirst(request, shellAssets.get(url.pathname), useCache))
  } else if (!url.search && request.cache !== 'no-store') {
    event.respondWith(networkFirst(request, url.pathname))
  }
})
