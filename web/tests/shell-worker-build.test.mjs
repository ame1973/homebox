import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'

function fixture(t) {
  const root = mkdtempSync(join(tmpdir(), 'homebox-shell-build-'))
  t.after(() => rmSync(root, { recursive: true, force: true }))
  mkdirSync(join(root, 'scripts'))
  mkdirSync(join(root, 'src', 'pwa'), { recursive: true })
  mkdirSync(join(root, 'dist', '.vite'), { recursive: true })
  mkdirSync(join(root, 'dist', 'assets'))
  mkdirSync(join(root, 'dist', 'icons'))
  copyFileSync(new URL('../scripts/build-shell-worker.mjs', import.meta.url), join(root, 'scripts', 'build-shell-worker.mjs'))
  copyFileSync(new URL('../src/pwa/shell-worker.js', import.meta.url), join(root, 'src', 'pwa', 'shell-worker.js'))
  writeFileSync(join(root, 'package.json'), JSON.stringify({ version: '0.1.0-dev' }))
  writeFileSync(join(root, 'dist', 'index.html'), '<!doctype html><meta name="homebox-shell-worker" content="__HOMEBOX_SHELL_WORKER__"><main>HomeBox</main>')
  for (const name of ['favicon.svg', 'manifest.webmanifest', 'icons/homebox.svg', 'icons/homebox-180.png', 'icons/homebox-192.png', 'icons/homebox-512.png']) {
    writeFileSync(join(root, 'dist', name), `public ${name}`)
  }
  for (const name of ['index-AbCd1234.js', 'index-1234abcd.css', 'lazy-AbCd1234.js', 'worker-AbCd1234.js']) {
    writeFileSync(join(root, 'dist', 'assets', name), `asset ${name}`)
  }
  const manifest = {
    'index.html': { file: 'assets/index-AbCd1234.js', isEntry: true, css: ['assets/index-1234abcd.css'], dynamicImports: ['lazy'] },
    lazy: { file: 'assets/lazy-AbCd1234.js', isDynamicEntry: true, assets: ['assets/worker-AbCd1234.js'] },
  }
  const manifestPath = join(root, 'dist', '.vite', 'manifest.json')
  writeFileSync(manifestPath, JSON.stringify(manifest))
  const run = () => execFileSync(process.execPath, [join(root, 'scripts', 'build-shell-worker.mjs')], { encoding: 'utf8', stdio: 'pipe' })
  return { root, manifest, manifestPath, run }
}

function built(root) {
  const html = readFileSync(join(root, 'dist', 'index.html'), 'utf8')
  const path = html.match(/name="homebox-shell-worker" content="(\/pwa-worker-[a-f0-9]{24}\.js)"/)?.[1]
  assert.ok(path, 'missing versioned root worker URL')
  const worker = readFileSync(join(root, 'dist', path), 'utf8')
  const config = JSON.parse(worker.match(/^const shellConfig = (.+)$/m)?.[1] ?? 'null')
  const version = readFileSync(join(root, 'dist', 'pwa-version.json'), 'utf8')
  const build = readFileSync(join(root, 'dist', 'web-build.json'), 'utf8')
  assert.ok(config)
  return { html, path, worker, config, version, build }
}

test('builds a deterministic integrity-bound private shell worker', (t) => {
  const first = fixture(t), second = fixture(t)
  first.run()
  second.run()
  const result = built(first.root), other = built(second.root)
  assert.equal(result.path, other.path)
  assert.equal(result.html, other.html)
  assert.equal(result.worker, other.worker)
  assert.equal(result.version, other.version)
  assert.equal(result.build, other.build)
  assert.equal(result.version, `${JSON.stringify({ worker: result.path })}\n`)
  assert.equal(result.build, `${JSON.stringify({ worker: result.path, web_version: '0.1.0-dev' })}\n`)
  assert.equal(result.config.cacheName, `homebox-shell-v1-${result.path.slice('/pwa-worker-'.length, -'.js'.length)}`)
  assert.deepEqual(result.config.assets.map((entry) => entry.path), [
    '/assets/index-1234abcd.css', '/assets/index-AbCd1234.js', '/assets/lazy-AbCd1234.js', '/assets/worker-AbCd1234.js',
    '/favicon.svg', '/icons/homebox-180.png', '/icons/homebox-192.png', '/icons/homebox-512.png', '/icons/homebox.svg', '/index.html', '/manifest.webmanifest',
  ])
  for (const entry of result.config.assets) {
    const bytes = readFileSync(join(first.root, 'dist', entry.path))
    assert.equal(entry.size, bytes.length)
    assert.equal(entry.sha256, createHash('sha256').update(bytes).digest('hex'))
  }
  assert.ok(!result.worker.includes('__HOMEBOX_SHELL_CONFIG__'))
  assert.ok(!result.html.includes('__HOMEBOX_SHELL_WORKER__'))
})

test('changes its worker identity when a shell file or worker changes', (t) => {
  const first = fixture(t), changed = fixture(t), workerChanged = fixture(t), versionChanged = fixture(t)
  writeFileSync(join(changed.root, 'dist', 'assets', 'lazy-AbCd1234.js'), 'changed content')
  writeFileSync(join(workerChanged.root, 'src', 'pwa', 'shell-worker.js'), '\n// A different worker version.\n', { flag: 'a' })
  writeFileSync(join(versionChanged.root, 'package.json'), JSON.stringify({ version: '0.1.0' }))
  first.run(); changed.run(); workerChanged.run(); versionChanged.run()
  assert.notEqual(built(first.root).path, built(changed.root).path)
  assert.notEqual(built(first.root).path, built(workerChanged.root).path)
  assert.notEqual(built(first.root).path, built(versionChanged.root).path)
  assert.equal(JSON.parse(built(versionChanged.root).build).web_version, '0.1.0')
})

test('rejects unsafe, missing or oversized files without publishing a worker', (t) => {
  for (const scenario of ['path traversal', 'absolute path', 'unhashed asset', 'missing file', 'oversized file', 'missing dependency', 'too many files', 'missing placeholder', 'duplicate placeholder', 'occupied version', 'occupied build', 'missing web version', 'empty web version', 'invalid web version', 'oversized web version']) {
    const f = fixture(t)
    if (scenario === 'path traversal') f.manifest.lazy.file = 'assets/../../secrets.js'
    if (scenario === 'absolute path') f.manifest.lazy.file = '/api/v1/private.js'
    if (scenario === 'unhashed asset') f.manifest.lazy.file = 'assets/lazy.js'
    if (scenario === 'missing file') f.manifest.lazy.file = 'assets/missing-AbCd1234.js'
    if (scenario === 'oversized file') writeFileSync(join(f.root, 'dist', 'assets', 'lazy-AbCd1234.js'), Buffer.alloc(4 * 1024 * 1024 + 1))
    if (scenario === 'missing dependency') f.manifest.lazy.imports = ['missing']
    if (scenario === 'too many files') {
      for (let i = 0; i < 260; i++) f.manifest[`extra-${i}`] = { file: `assets/extra${i}-AbCd1234.js` }
    }
    if (scenario === 'missing placeholder') writeFileSync(join(f.root, 'dist', 'index.html'), '<!doctype html>')
    if (scenario === 'duplicate placeholder') writeFileSync(join(f.root, 'dist', 'index.html'), '__HOMEBOX_SHELL_WORKER__ __HOMEBOX_SHELL_WORKER__')
    if (scenario === 'occupied version') writeFileSync(join(f.root, 'dist', 'pwa-version.json'), 'existing version')
    if (scenario === 'occupied build') writeFileSync(join(f.root, 'dist', 'web-build.json'), 'existing build')
    if (scenario === 'missing web version') writeFileSync(join(f.root, 'package.json'), '{}')
    if (scenario === 'empty web version') writeFileSync(join(f.root, 'package.json'), JSON.stringify({ version: '' }))
    if (scenario === 'invalid web version') writeFileSync(join(f.root, 'package.json'), JSON.stringify({ version: '1.0.0\nother' }))
    if (scenario === 'oversized web version') writeFileSync(join(f.root, 'package.json'), JSON.stringify({ version: '1'.repeat(33) }))
    writeFileSync(f.manifestPath, JSON.stringify(f.manifest))
    const before = readFileSync(join(f.root, 'dist', 'index.html'))
    assert.throws(f.run, undefined, scenario)
    assert.ok(readFileSync(join(f.root, 'dist', 'index.html')).equals(before), scenario)
    assert.ok(readdirSync(join(f.root, 'dist')).every((name) => !name.startsWith('pwa-worker-')), scenario)
    if (scenario === 'occupied version') assert.equal(readFileSync(join(f.root, 'dist', 'pwa-version.json'), 'utf8'), 'existing version')
    else assert.ok(!readdirSync(join(f.root, 'dist')).includes('pwa-version.json'), scenario)
    if (scenario === 'occupied build') assert.equal(readFileSync(join(f.root, 'dist', 'web-build.json'), 'utf8'), 'existing build')
    else assert.ok(!readdirSync(join(f.root, 'dist')).includes('web-build.json'), scenario)
  }
})
