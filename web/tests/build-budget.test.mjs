import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { randomBytes } from 'node:crypto'
import { copyFileSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'

function fixture(t, { initial = 100, lazy = 100, manifest } = {}) {
  const root = mkdtempSync(join(tmpdir(), 'homebox-budgets-'))
  t.after(() => rmSync(root, { recursive: true, force: true }))
  mkdirSync(join(root, 'scripts'))
  mkdirSync(join(root, 'dist', '.vite'), { recursive: true })
  const script = join(root, 'scripts', 'check-build.mjs')
  copyFileSync(new URL('../scripts/check-build.mjs', import.meta.url), script)
  writeFileSync(join(root, 'dist', 'entry.js'), randomBytes(initial))
  writeFileSync(join(root, 'dist', 'shared.js'), 'shared')
  writeFileSync(join(root, 'dist', 'lazy.js'), randomBytes(lazy))
  writeFileSync(join(root, 'dist', '.vite', 'manifest.json'), JSON.stringify(manifest ?? {
    'index.html': { file: 'entry.js', isEntry: true, imports: ['shared', 'shared'] },
    shared: { file: 'shared.js' },
    lazy: { file: 'lazy.js', isDynamicEntry: true },
  }))
  return () => execFileSync(process.execPath, [script], { encoding: 'utf8', stdio: 'pipe' })
}

test('accepts a small build and deduplicates static imports', (t) => {
  assert.match(fixture(t)(), /Initial JavaScript:/)
})
test('rejects excessive initial JavaScript', (t) => {
  assert.throws(fixture(t, { initial: 300 * 1024 }), /Initial JavaScript exceeds/)
})
test('rejects an oversized lazy chunk', (t) => {
  assert.throws(fixture(t, { lazy: 200 * 1024 }), /Lazy chunk exceeds/)
})
test('rejects a missing entrypoint', (t) => {
  assert.throws(fixture(t, { manifest: {} }), /no entry chunk/)
})
test('rejects a broken static-import graph', (t) => {
  assert.throws(fixture(t, { manifest: { index: { file: 'entry.js', isEntry: true, imports: ['missing'] } } }), /Missing manifest chunk/)
})
