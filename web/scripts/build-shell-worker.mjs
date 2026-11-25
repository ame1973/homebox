import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { lstat, readFile, writeFile } from 'node:fs/promises'
import { extname } from 'node:path'
import { gzipSync } from 'node:zlib'

const dist = new URL('../dist/', import.meta.url)
const workerSource = await readFile(new URL('../src/pwa/shell-worker.js', import.meta.url), 'utf8')
const generatorSource = await readFile(new URL(import.meta.url))
const manifest = JSON.parse(await readFile(new URL('.vite/manifest.json', dist), 'utf8'))
assert(manifest && typeof manifest === 'object' && !Array.isArray(manifest), 'Invalid Vite manifest')
const indexURL = new URL('index.html', dist)
const indexStat = await lstat(indexURL)
assert(indexStat.isFile(), 'Invalid shell entry')
const index = await readFile(indexURL, 'utf8')
const placeholder = '__HOMEBOX_SHELL_WORKER__'
assert(index.split(placeholder).length === 2, 'Expected exactly one shell worker placeholder')
assert(workerSource.split('__HOMEBOX_SHELL_CONFIG__').length === 2, 'Expected exactly one shell worker configuration placeholder')

const publicFiles = [
  'favicon.svg', 'manifest.webmanifest', 'icons/homebox.svg', 'icons/homebox-180.png',
  'icons/homebox-192.png', 'icons/homebox-512.png',
]
const hashedAsset = /^assets\/[A-Za-z0-9][A-Za-z0-9_-]*-[A-Za-z0-9_-]{8,}\.(?:js|css|woff2|png|svg|webp|ico)$/
const files = new Set(publicFiles)
let hasEntry = false
for (const value of Object.values(manifest)) {
  assert(value && typeof value === 'object' && typeof value.file === 'string', 'Invalid Vite chunk')
  if (value.isEntry) hasEntry = true
  for (const key of ['imports', 'dynamicImports', 'css', 'assets']) {
    assert(value[key] === undefined || Array.isArray(value[key]), `Invalid Vite ${key}`)
  }
  for (const dependency of [...(value.imports ?? []), ...(value.dynamicImports ?? [])]) {
    assert(typeof dependency === 'string' && Object.hasOwn(manifest, dependency), 'Missing Vite dependency')
  }
  for (const file of [value.file, ...(value.css ?? []), ...(value.assets ?? [])]) {
    assert(typeof file === 'string' && hashedAsset.test(file), 'Unsafe or unhashed shell asset')
    files.add(file)
  }
}
assert(hasEntry, 'Missing Vite entry')
assert(files.size + 1 <= 256, 'Too many shell assets')

const mimeTypes = {
  '.html': ['text/html'],
  '.js': ['text/javascript', 'application/javascript'],
  '.css': ['text/css'],
  '.svg': ['image/svg+xml'],
  '.png': ['image/png'],
  '.webp': ['image/webp'],
  '.ico': ['image/x-icon', 'image/vnd.microsoft.icon'],
  '.woff2': ['font/woff2'],
  '.webmanifest': ['application/manifest+json'],
}
const hash = (value) => createHash('sha256').update(value).digest('hex')
const entry = (path, bytes) => {
  const mime = mimeTypes[extname(path)]
  assert(mime && bytes.length > 0 && bytes.length <= 4 * 1024 * 1024, 'Invalid or oversized shell asset')
  return { path: `/${path}`, size: bytes.length, sha256: hash(bytes), mime }
}
let total = 0
const assets = []
for (const file of [...files].sort()) {
  const url = new URL(file, dist)
  const stat = await lstat(url)
  assert(stat.isFile() && stat.size > 0 && stat.size <= 4 * 1024 * 1024, 'Invalid or oversized shell asset')
  const bytes = await readFile(url)
  total += bytes.length
  assert(total <= 16 * 1024 * 1024, 'Shell assets exceed the total byte budget')
  assets.push(entry(file, bytes))
}
const version = hash(JSON.stringify({
  schema: 1, index: hash(index), assets, worker: hash(workerSource), generator: hash(generatorSource),
})).slice(0, 24)
const workerPath = `/pwa-worker-${version}.js`
const html = index.replace(placeholder, workerPath)
const htmlEntry = entry('index.html', Buffer.from(html))
assert(total + htmlEntry.size <= 16 * 1024 * 1024, 'Shell assets exceed the total byte budget')
assets.push(htmlEntry)
assets.sort((left, right) => left.path < right.path ? -1 : left.path > right.path ? 1 : 0)
const config = { cacheName: `homebox-shell-v1-${version}`, assets }
const worker = workerSource.replace('__HOMEBOX_SHELL_CONFIG__', JSON.stringify(config))
assert(gzipSync(worker).length <= 150 * 1024, 'Shell worker exceeds 150 KiB gzip')
await writeFile(new URL(workerPath.slice(1), dist), worker, { flag: 'wx' })
await writeFile(indexURL, html)
console.log(`Private shell worker: ${workerPath}; ${assets.length} verified public resources (${total + htmlEntry.size} bytes).`)
