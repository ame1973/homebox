import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { gzipSync } from 'node:zlib'

const root = new URL('../dist/', import.meta.url)
const manifest = JSON.parse(await readFile(new URL('.vite/manifest.json', root), 'utf8'))
const initial = new Set()
const initialAssets = new Set()
function visit(key) {
  const chunk = manifest[key]
  assert(chunk, `Missing manifest chunk: ${key}`)
  if (initial.has(chunk.file)) return
  initial.add(chunk.file)
  for (const asset of chunk.assets ?? []) initialAssets.add(asset)
  for (const dependency of chunk.imports ?? []) visit(dependency)
}
for (const [key, chunk] of Object.entries(manifest)) if (chunk.isEntry) visit(key)
assert(initial.size > 0, 'The build has no entry chunk')
for (const file of [...initial, ...initialAssets]) {
  assert(!/(?:^|\/)scan-zxing-worker(?:-|\.)/.test(file), 'Scanner worker must stay out of the initial application graph')
  assert(!/(?:^|\/)qr-label-worker(?:-|\.)/.test(file), 'QR label worker must stay out of the initial application graph')
}
let initialBytes = 0
for (const file of initial) initialBytes += gzipSync(await readFile(new URL(file, root))).length
assert(initialBytes <= 250 * 1024, `Initial JavaScript exceeds 250 KiB gzip: ${initialBytes}`)
const workerAssets = new Set()
for (const chunk of Object.values(manifest)) {
  for (const asset of chunk.assets ?? []) if (asset.endsWith('.js')) workerAssets.add(asset)
  if (!chunk.isDynamicEntry) continue
  const bytes = gzipSync(await readFile(new URL(chunk.file, root))).length
  assert(bytes <= 150 * 1024, `Lazy chunk exceeds 150 KiB gzip: ${chunk.file} (${bytes})`)
}
for (const file of workerAssets) {
  const bytes = gzipSync(await readFile(new URL(file, root))).length
  assert(bytes <= 150 * 1024, `Worker asset exceeds 150 KiB gzip: ${file} (${bytes})`)
}
console.log(`Initial JavaScript: ${(initialBytes / 1024).toFixed(1)} KiB gzip (budget 250 KiB); lazy chunks and worker assets <=150 KiB.`)
