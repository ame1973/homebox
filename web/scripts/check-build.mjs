import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { gzipSync } from 'node:zlib'

const root = new URL('../dist/', import.meta.url)
const manifest = JSON.parse(await readFile(new URL('.vite/manifest.json', root), 'utf8'))
const initial = new Set()
function visit(key) {
  const chunk = manifest[key]
  assert(chunk, `Missing manifest chunk: ${key}`)
  if (initial.has(chunk.file)) return
  initial.add(chunk.file)
  for (const dependency of chunk.imports ?? []) visit(dependency)
}
for (const [key, chunk] of Object.entries(manifest)) if (chunk.isEntry) visit(key)
assert(initial.size > 0, 'The build has no entry chunk')
let initialBytes = 0
for (const file of initial) initialBytes += gzipSync(await readFile(new URL(file, root))).length
assert(initialBytes <= 250 * 1024, `Initial JavaScript exceeds 250 KiB gzip: ${initialBytes}`)
for (const chunk of Object.values(manifest)) {
  if (!chunk.isDynamicEntry) continue
  const bytes = gzipSync(await readFile(new URL(chunk.file, root))).length
  assert(bytes <= 150 * 1024, `Lazy chunk exceeds 150 KiB gzip: ${chunk.file} (${bytes})`)
}
console.log(`Initial JavaScript: ${(initialBytes / 1024).toFixed(1)} KiB gzip (budget 250 KiB); lazy chunks <=150 KiB.`)
