import assert from 'node:assert/strict'
import { createHash, webcrypto } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { runInNewContext } from 'node:vm'

function runtime(fetch, cacheBody = '<!doctype html>cached shell', waitForCache = async () => {}) {
  const bytes = new TextEncoder().encode(cacheBody)
  const config = {
    cacheName: 'homebox-shell-v1-test',
    assets: [{ path: '/index.html', size: bytes.length, sha256: createHash('sha256').update(bytes).digest('hex'), mime: ['text/html'] }],
  }
  const source = readFileSync(new URL('../src/pwa/shell-worker.js', import.meta.url), 'utf8').replace('__HOMEBOX_SHELL_CONFIG__', JSON.stringify(config))
  const listeners = new Map(), timers = [], cacheReads = []
  const self = { location: { origin: 'https://homebox.test' }, addEventListener: (kind, callback) => listeners.set(kind, callback) }
  const caches = { match: async (path) => { cacheReads.push(path); await waitForCache(); return new Response(bytes, { headers: { 'Content-Type': 'text/html' } }) } }
  const setTimeout = (callback, milliseconds) => { const timer = { callback, milliseconds, cleared: false }; timers.push(timer); return timer }
  const clearTimeout = (timer) => { timer.cleared = true }
  runInNewContext(source, { self, caches, fetch, setTimeout, clearTimeout, crypto: webcrypto, Headers, Response, URL, AbortController, Uint8Array })
  const dispatch = (request) => {
    let pending
    listeners.get('fetch')({ request, respondWith: (promise) => { pending = promise } })
    return pending
  }
  const navigate = (signal) => {
    const pending = dispatch({ url: 'https://homebox.test/locations', method: 'GET', mode: 'navigate', signal, headers: new Headers(), cache: 'default' })
    assert.ok(pending)
    return pending
  }
  return { dispatch, navigate, timers, cacheReads }
}

test('private, encoded, cross-origin and non-GET requests are not handled', () => {
  const worker = runtime(async () => { throw new Error('must not be called') })
  const signal = new AbortController().signal
  const paths = [
    '/api', '/api/v1/items', '/%61pi/v1/items', '/api%2Fv1/items', '/auth/login', '/media', '/backups/test',
    '/exports/test', '/worker/test', '/data/test', '/secrets/test', '/dev/test', '/licenses', '/.git/config',
    '/items/example.js', '/foo%5cbar', '/foo%00bar', '/foo%ZZ', '//double',
  ]
  for (const path of paths) {
    const pending = worker.dispatch({ url: `https://homebox.test${path}`, method: 'GET', mode: 'navigate', signal, headers: new Headers(), cache: 'default' })
    assert.equal(pending, undefined, path)
  }
  for (const request of [
    { url: 'https://other.test/locations', method: 'GET' },
    { url: 'https://homebox.test/locations', method: 'POST' },
    { url: 'https://homebox.test/api/v1/items', method: 'GET', mode: 'cors' },
    { url: 'https://homebox.test/pwa-version.json', method: 'GET', mode: 'cors' },
  ]) {
    assert.equal(worker.dispatch({ mode: 'navigate', signal, headers: new Headers(), cache: 'default', ...request }), undefined)
  }
  assert.deepEqual(worker.cacheReads, [])
  assert.deepEqual(worker.timers, [])
})

test('a stalled public navigation is bounded and falls back to verified shell', async () => {
  let receivedSignal
  const worker = runtime((_request, options) => new Promise((_resolve, reject) => {
    receivedSignal = options.signal
    receivedSignal.addEventListener('abort', () => reject(new Error('aborted')), { once: true })
  }))
  const pending = worker.navigate(new AbortController().signal)
  assert.equal(worker.timers.length, 1)
  assert.equal(worker.timers[0].milliseconds, 8000)
  worker.timers[0].callback()
  const response = await pending
  assert.equal(receivedSignal.aborted, true)
  assert.equal(await response.text(), '<!doctype html>cached shell')
  assert.equal(response.headers.get('Cache-Control'), 'no-store')
  assert.match(response.headers.get('Content-Security-Policy'), /script-src 'self'/)
  assert.deepEqual(worker.cacheReads, ['/index.html'])
  assert.equal(worker.timers[0].cleared, true)
})

test('a stalled public response body also falls back within the deadline', async () => {
  const worker = runtime(async (_request, options) => new Response(new ReadableStream({
    start(controller) { options.signal.addEventListener('abort', () => controller.error(new Error('body aborted')), { once: true }) },
  }), { status: 200, headers: { 'Content-Type': 'text/html' } }))
  const pending = worker.navigate(new AbortController().signal)
  await new Promise((resolve) => setImmediate(resolve))
  assert.equal(worker.timers[0].cleared, false)
  worker.timers[0].callback()
  assert.equal(await (await pending).text(), '<!doctype html>cached shell')
  assert.deepEqual(worker.cacheReads, ['/index.html'])
  assert.equal(worker.timers[0].cleared, true)
})

test('an explicitly canceled navigation does not return cached data', async () => {
  const worker = runtime((_request, options) => new Promise((_resolve, reject) => {
    options.signal.addEventListener('abort', () => reject(new Error('aborted')), { once: true })
  }))
  const controller = new AbortController()
  const pending = worker.navigate(controller.signal)
  controller.abort()
  await assert.rejects(pending, /aborted/)
  assert.deepEqual(worker.cacheReads, [])
  assert.equal(worker.timers[0].cleared, true)
})

test('cancellation during an offline cache read cannot return a stale shell', async () => {
  let release
  const held = new Promise((resolve) => { release = resolve })
  const worker = runtime(async () => { throw new Error('offline') }, '<!doctype html>cached shell', () => held)
  const controller = new AbortController()
  const pending = worker.navigate(controller.signal)
  await new Promise((resolve) => setImmediate(resolve))
  assert.deepEqual(worker.cacheReads, ['/index.html'])
  controller.abort()
  release()
  await assert.rejects(pending, /offline/)
  assert.equal(worker.timers[0].cleared, true)
})

test('an oversized online shell fails closed to the verified cached version', async () => {
  const worker = runtime(async () => new Response(new Uint8Array(4 * 1024 * 1024 + 1), { status: 200 }))
  const response = await worker.navigate(new AbortController().signal)
  assert.equal(await response.text(), '<!doctype html>cached shell')
  assert.deepEqual(worker.cacheReads, ['/index.html'])
  assert.equal(worker.timers[0].cleared, true)
})

test('a successful bounded online response preserves its body and security policy', async () => {
  const worker = runtime(async () => new Response('new shell', { status: 200, headers: {
    'Content-Type': 'text/html', 'Content-Security-Policy': "default-src 'self'", 'Content-Encoding': 'gzip', 'Content-Length': '100',
  } }))
  const response = await worker.navigate(new AbortController().signal)
  assert.equal(await response.text(), 'new shell')
  assert.equal(response.headers.get('Content-Security-Policy'), "default-src 'self'")
  assert.equal(response.headers.get('Content-Encoding'), null)
  assert.equal(response.headers.get('Content-Length'), null)
  assert.deepEqual(worker.cacheReads, [])
  assert.equal(worker.timers[0].cleared, true)
})

test('unexpected successful navigation statuses do not replace the verified shell', async () => {
  for (const status of [201, 206]) {
    const worker = runtime(async () => new Response('unexpected body', { status }))
    const response = await worker.navigate(new AbortController().signal)
    assert.equal(response.status, 200, `unexpected ${status}`)
    assert.equal(await response.text(), '<!doctype html>cached shell')
    assert.deepEqual(worker.cacheReads, ['/index.html'])
  }
})

test('an online HTTP failure is not silently replaced by the offline shell', async () => {
  const worker = runtime(async () => new Response('unavailable', { status: 503 }))
  const response = await worker.navigate(new AbortController().signal)
  assert.equal(response.status, 503)
  assert.equal(await response.text(), 'unavailable')
  assert.deepEqual(worker.cacheReads, [])
  assert.equal(worker.timers[0].cleared, true)
})
