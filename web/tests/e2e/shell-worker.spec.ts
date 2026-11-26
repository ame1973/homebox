import { execFileSync } from 'node:child_process'
import { copyFileSync, cpSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, unlinkSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import type { Page } from '@playwright/test'
import { messages } from '../../src/lib/messages.ts'
import { test as applicationTest, expect } from './auth-fixture.ts'
import { provision, watchDomainWrites, writeContext } from './domain-helpers.ts'

applicationTest.use({ trace: 'off', screenshot: 'off', video: 'off' })
applicationTest.beforeEach(async ({ context }) => {
  // These cases register workers themselves to isolate the low-level worker.
  await context.addInitScript(() => { Object.defineProperty(navigator, 'locks', { configurable: true, value: undefined }) })
})

async function registerShell(page: Page) {
  return page.evaluate(async () => {
    const path = document.querySelector<HTMLMetaElement>('meta[name="homebox-shell-worker"]')?.content
    if (!path || !/^\/pwa-worker-[a-f0-9]{24}\.js$/.test(path)) throw new Error('Invalid worker path')
    const registration = await navigator.serviceWorker.register(path, { scope: '/', updateViaCache: 'none' })
    const worker = registration.installing ?? registration.waiting ?? registration.active
    if (!worker) throw new Error('Worker did not enter the registration')
    const state = await new Promise<string>((resolve, reject) => {
      const timeout = setTimeout(() => reject(new Error('Worker install did not finish')), 30_000)
      const check = () => {
        if (worker.state === 'activated' || worker.state === 'redundant') {
          clearTimeout(timeout)
          worker.removeEventListener('statechange', check)
          resolve(worker.state)
        }
      }
      worker.addEventListener('statechange', check)
      check()
    })
    return { path, scope: registration.scope, state }
  })
}

applicationTest('privately installs only verified public shell assets without caching API responses or writes', async ({ page, context, application }) => {
  await provision(page, application)
  const marker = 'private-fixture-name-5be28c3f'
  const created = await page.request.post('/api/v1/items', { headers: await writeContext(page, application.url), data: { name: marker } })
  expect(created.status()).toBe(200)
  const shellResponse = await page.goto('/')
  const expectedCSP = shellResponse?.headers()['content-security-policy']
  expect(expectedCSP).toBeTruthy()
  const canonicalIndex = await page.request.get('/index.html', { maxRedirects: 0 })
  expect(canonicalIndex.status()).toBe(301)
  expect(canonicalIndex.headers().location).toBe('./')
  expect(await page.evaluate(async () => (await navigator.serviceWorker.getRegistrations()).length)).toBe(0)
  await page.evaluate(async () => {
    const prior = await caches.open('homebox-shell-v1-prior-fixture')
    await prior.put('/sentinel', new Response('keep prior shell'))
    const unrelated = await caches.open('another-app-fixture')
    await unrelated.put('/sentinel', new Response('keep unrelated cache'))
  })
  const installed = await registerShell(page)
  const installedKeys = await page.evaluate(async () => {
    const result: Record<string, string[]> = {}
    for (const name of await caches.keys()) result[name] = (await (await caches.open(name)).keys()).map((request) => new URL(request.url).pathname)
    return result
  })
  expect(installed.state, JSON.stringify(installedKeys)).toBe('activated')
  expect(installed.scope).toBe(`${application.url}/`)
  const versionResponse = await page.request.get('/pwa-version.json')
  expect(versionResponse.status()).toBe(200)
  expect(versionResponse.headers()['cache-control']).toBe('no-store')
  expect(versionResponse.headers()['content-type']).toBe('application/json; charset=utf-8')
  expect(await versionResponse.json()).toEqual({ worker: installed.path })
  const buildResponse = await page.request.get('/web-build.json')
  expect(buildResponse.status()).toBe(404)
  expect(buildResponse.headers()['cache-control']).toBe('no-store')
  const workerResponse = await page.request.get(installed.path)
  expect(workerResponse.status()).toBe(200)
  expect(workerResponse.headers()['cache-control']).toBe('public, max-age=31536000, immutable')
  expect(workerResponse.headers()['content-type']).toMatch(/^(text|application)\/javascript/)
  const source = await workerResponse.text()
  const config = JSON.parse(source.match(/^const shellConfig = (.+)$/m)?.[1] ?? 'null') as { cacheName: string; assets: { path: string }[] }
  expect(config.cacheName).toMatch(/^homebox-shell-v1-[a-f0-9]{24}$/)
  await page.reload()
  await expect.poll(() => page.evaluate(() => navigator.serviceWorker.controller?.state)).toBe('activated')

  const privateRead = await page.evaluate(async (name) => {
    const response = await fetch('/api/v1/items?limit=50')
    return { status: response.status, contains: (await response.text()).includes(name) }
  }, marker)
  expect(privateRead).toEqual({ status: 200, contains: true })
  const inspection = await page.evaluate(async ({ cacheName, name }) => {
    const cache = await caches.open(cacheName)
    const keys = (await cache.keys()).map((request) => new URL(request.url).pathname).sort()
    const html = await cache.match('/index.html')
    let leaked = false
    for (const request of await cache.keys()) {
      const response = await cache.match(request)
      if (response && (await response.text()).includes(name)) leaked = true
    }
    const prior = await (await caches.open('homebox-shell-v1-prior-fixture')).match('/sentinel')
    const unrelated = await (await caches.open('another-app-fixture')).match('/sentinel')
    return {
      keys, leaked, htmlCSP: html?.headers.get('Content-Security-Policy'),
      htmlCache: html?.headers.get('Cache-Control'), prior: await prior?.text(), unrelated: await unrelated?.text(),
    }
  }, { cacheName: config.cacheName, name: marker })
  expect(inspection.keys).toEqual(config.assets.map((entry) => entry.path).sort())
  expect(inspection.keys).not.toContain('/web-build.json')
  expect(inspection.keys.every((path) => !/^\/(api|auth|media|backups|exports|worker|data|secrets|dev)(\/|$)/.test(path))).toBe(true)
  expect(inspection.leaked).toBe(false)
  expect(inspection.htmlCSP).toContain("script-src 'self'")
  expect(inspection.htmlCache).toBe('no-store')
  expect(inspection.prior).toBe('keep prior shell')
  expect(inspection.unrelated).toBe('keep unrelated cache')

  const entry = config.assets.find((value) => /^\/assets\/index-.*\.js$/.test(value.path))?.path
  expect(entry).toBeTruthy()
  const ranged = await page.evaluate(async (path) => {
    const response = await fetch(path, { headers: { Range: 'bytes=0-9' } })
    return { status: response.status, contentRange: response.headers.get('Content-Range') }
  }, entry!)
  expect(ranged.status).toBe(206)
  expect(ranged.contentRange).toMatch(/^bytes 0-9\//)

  const writes = watchDomainWrites(page, ['/api/v1/items'])
  await context.setOffline(true)
  const offline = await page.evaluate(async (path) => {
    const outcome = async (input: string, init?: RequestInit) => {
      try { return (await fetch(input, init)).status } catch { return 'network-error' }
    }
    return {
      publicAsset: await outcome(path),
      queryAsset: await outcome(`${path}?never-cached=private`),
      noStoreAsset: await outcome(path, { cache: 'no-store' }),
      version: await outcome('/pwa-version.json', { cache: 'no-store' }),
      health: await outcome('/api/v1/health/live'),
      account: await outcome('/api/v1/auth/me'),
      item: await outcome('/api/v1/items?limit=50'),
      write: await outcome('/api/v1/items', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' }),
      head: await outcome(path, { method: 'HEAD', cache: 'no-store' }),
    }
  }, entry!)
  expect(offline).toEqual({ publicAsset: 200, queryAsset: 'network-error', noStoreAsset: 'network-error', version: 'network-error', health: 'network-error', account: 'network-error', item: 'network-error', write: 'network-error', head: 'network-error' })
  const poisoned = await page.evaluate(async ({ cacheName, path }) => {
    const cache = await caches.open(cacheName)
    const original = await cache.match(path)
    if (!original) throw new Error('Missing cached asset')
    const expected = new Uint8Array(await original.clone().arrayBuffer())
    const bytes = expected.slice()
    bytes[bytes.length - 1] = bytes[bytes.length - 1]! ^ 1
    await cache.put(path, new Response(bytes, { headers: original.headers }))
    let result: string
    try {
      const response = await fetch(path)
      const received = new Uint8Array(await response.arrayBuffer())
      result = response.status === 200 && received.length === expected.length && received.every((byte, index) => byte === expected[index]) ? 'original' : 'corrupt'
    } catch { result = 'network-error' }
    await cache.put(path, original)
    return result
  }, { cacheName: config.cacheName, path: entry! })
  expect(['network-error', 'original']).toContain(poisoned)
  await page.evaluate(async (cacheName) => {
    const cache = await caches.open(cacheName)
    const original = await cache.match('/index.html')
    if (!original) throw new Error('Missing cached shell')
    const headers = new Headers(original.headers)
    headers.set('Content-Security-Policy', "default-src * 'unsafe-inline'")
    headers.set('X-Frame-Options', 'ALLOWALL')
    await cache.put('/index.html', new Response(await original.arrayBuffer(), { headers }))
  }, config.cacheName)
  const attempts = writes.length
  const restoredShell = await page.goto('/locations?example=offline')
  expect(restoredShell?.headers()['content-security-policy']).toBe(expectedCSP)
  expect(restoredShell?.headers()['x-frame-options']).toBe('DENY')
  expect(await page.title()).toBe('HomeBox')
  await expect(page.locator('#root')).not.toBeEmpty()
  const privatePaths = ['/api/v1/health/live', '/%61pi/v1/health/live', '/api%2Fv1/health/live', '/auth/login', '/media', '/backups/test', '/exports/test', '/worker/test', '/data/test', '/secrets/test', '/dev/test', '/licenses']
  const outcomes = await Promise.all(privatePaths.map(async (path) => {
    const privatePage = await context.newPage()
    try {
      await privatePage.goto(`${application.url}${path}`, { waitUntil: 'commit' })
      return { path, blocked: false }
    } catch {
      return { path, blocked: true }
    } finally {
      await privatePage.close()
    }
  }))
  expect(outcomes).toEqual(privatePaths.map((path) => ({ path, blocked: true })))
  await context.setOffline(false)
  expect(await page.evaluate(async () => (await fetch('/api/v1/health/live')).status)).toBe(200)
  expect(writes).toHaveLength(attempts)

  let releaseSlow: () => void = () => {}
  let observedWorkerRequest = false
  const held = new Promise<void>((resolve) => { releaseSlow = resolve })
  const slowURL = (url: URL) => url.origin === application.url && url.pathname === '/locations' && url.search === '?slow-shell=1'
  await context.route(slowURL, async (route) => {
    if (!route.request().serviceWorker()) return route.continue()
    observedWorkerRequest = true
    await held
    await route.abort().catch(() => {})
  })
  try {
    const started = Date.now()
    const delayed = await page.goto('/locations?slow-shell=1', { waitUntil: 'domcontentloaded' })
    expect(delayed?.status()).toBe(200)
    expect(delayed?.headers()['content-security-policy']).toBe(expectedCSP)
    expect(observedWorkerRequest).toBe(true)
    expect(Date.now() - started).toBeGreaterThanOrEqual(7000)
    expect(Date.now() - started).toBeLessThan(15000)
    expect(await page.title()).toBe('HomeBox')
  } finally {
    releaseSlow()
    await context.unroute(slowURL)
  }

})

applicationTest('rejects corrupted or evicted cached HTML rather than inventing an offline shell', async ({ page, context, application }) => {
  await page.goto('/')
  const installed = await registerShell(page)
  expect(installed.state).toBe('activated')
  await page.reload()
  await expect.poll(() => page.evaluate(() => navigator.serviceWorker.controller?.state)).toBe('activated')
  const cacheName = `homebox-shell-v1-${installed.path.slice('/pwa-worker-'.length, -'.js'.length)}`
  await context.setOffline(true)
  await page.evaluate(async (name) => {
    const cache = await caches.open(name)
    const original = await cache.match('/index.html')
    if (!original) throw new Error('Missing cached shell')
    const bytes = new Uint8Array(await original.arrayBuffer())
    bytes[bytes.length - 1] = bytes[bytes.length - 1]! ^ 1
    await cache.put('/index.html', new Response(bytes, { headers: original.headers }))
  }, cacheName)
  // The fixture closes the isolated context; closing this failed offline page
  // directly can hang Chromium's target teardown.
  const missingShell = await context.newPage()
  await expect(missingShell.goto(`${application.url}/items/new`, { waitUntil: 'commit', timeout: 12_000 })).rejects.toThrow(/net::ERR_(FAILED|INTERNET_DISCONNECTED)/)
  await page.evaluate(async (name) => { await (await caches.open(name)).delete('/index.html') }, cacheName)
  await expect(missingShell.goto(`${application.url}/scan`, { waitUntil: 'commit', timeout: 12_000 })).rejects.toThrow(/net::ERR_(FAILED|INTERNET_DISCONNECTED)/)
})

const upgradeTest = applicationTest.extend<{}, { applicationWebDirectory: string }>({
  applicationWebDirectory: [async ({ browserName }, provideFixture) => {
    const root = mkdtempSync(join(tmpdir(), `homebox-shell-upgrade-${browserName}-`))
    try {
      const web = join(root, 'dist')
      cpSync(fileURLToPath(new URL('../../dist', import.meta.url)), web, { recursive: true })
      mkdirSync(join(root, 'scripts'))
      mkdirSync(join(root, 'src', 'pwa'), { recursive: true })
      copyFileSync(fileURLToPath(new URL('../../package.json', import.meta.url)), join(root, 'package.json'))
      copyFileSync(fileURLToPath(new URL('../../scripts/build-shell-worker.mjs', import.meta.url)), join(root, 'scripts', 'build-shell-worker.mjs'))
      copyFileSync(fileURLToPath(new URL('../../src/pwa/shell-worker.js', import.meta.url)), join(root, 'src', 'pwa', 'shell-worker.js'))
      await provideFixture(web)
    } finally { rmSync(root, { recursive: true, force: true }) }
  }, { scope: 'worker' }],
})

upgradeTest('keeps a new verified worker waiting while two tabs retain unsaved forms', async ({ page, context, application, applicationWebDirectory }) => {
  await provision(page, application)
  await page.goto('/')
  const first = await registerShell(page)
  expect(first.state).toBe('activated')
  await page.reload()
  await expect.poll(() => page.evaluate(() => navigator.serviceWorker.controller?.scriptURL)).toBe(`${application.url}${first.path}`)
  await page.goto('/items/new')
  const name = new RegExp(`^${messages.catalog.fields.name}`)
  const firstDraft = page.getByLabel(name)
  await firstDraft.fill('First unsaved item')
  const second = await context.newPage()
  await second.goto(`${application.url}/items/new`)
  const secondDraft = second.getByLabel(name)
  await secondDraft.fill('Second unsaved item')
  const writes = watchDomainWrites(page, ['/api/v1/items'])

  const assetsDirectory = join(applicationWebDirectory, 'assets')
  const target = readdirSync(assetsDirectory).find((file) => /^capture-page-.*\.js$/.test(file))
  if (!target) throw new Error('Missing lazy capture chunk')
  const targetPath = join(assetsDirectory, target)
  const originalBytes = readFileSync(targetPath)
  const alteredBytes = Buffer.from(originalBytes)
  alteredBytes[alteredBytes.length - 1] = alteredBytes[alteredBytes.length - 1]! ^ 1
  const firstCache = `homebox-shell-v1-${first.path.slice('/pwa-worker-'.length, -'.js'.length)}`
  await page.evaluate(async ({ cacheName, asset }) => { await (await caches.open(cacheName)).delete(asset) }, { cacheName: firstCache, asset: `/assets/${target}` })
  try {
    writeFileSync(targetPath, alteredBytes)
    const direct = await page.request.get(`/assets/${target}`)
    expect(await direct.body()).toEqual(alteredBytes)
    for (const request of [
      { cache: 'reload', suffix: '' },
      { cache: 'no-store', suffix: '' },
      { cache: 'reload', suffix: '?explicit-query=1' },
    ] as const) {
      const outcome = await page.evaluate(async ({ asset, expected, cache }) => {
        try {
          const response = await fetch(asset, { cache })
          const bytes = Array.from(new Uint8Array(await response.arrayBuffer()))
          return response.status === 200 && bytes.length === expected.length && bytes.every((byte, index) => byte === expected[index]) ? 'original' : 'corrupt'
        } catch { return 'network-error' }
      }, { asset: `/assets/${target}${request.suffix}`, expected: [...originalBytes], cache: request.cache })
      expect(['network-error', 'original'], JSON.stringify(request)).toContain(outcome)
    }
    for (const status of [201, 206]) {
      const intercepted = (url: URL) => url.origin === application.url && url.pathname === `/assets/${target}`
      await context.route(intercepted, async (route) => {
        if (!route.request().serviceWorker()) return route.continue()
        const headers: Record<string, string> = status === 206 ? { 'Content-Range': 'bytes 0-3/4' } : {}
        await route.fulfill({ status, headers, contentType: 'text/javascript', body: 'evil' })
      })
      try {
        const result = await page.evaluate(async (asset) => {
          try { return (await fetch(asset, { cache: 'reload' })).status } catch { return 'network-error' }
        }, `/assets/${target}`)
        expect(result, `unexpected ${status} asset response`).toBe('network-error')
      } finally {
        await context.unroute(intercepted)
      }
    }
  } finally {
    writeFileSync(targetPath, originalBytes)
  }

  const root = dirname(applicationWebDirectory)
  const indexPath = join(applicationWebDirectory, 'index.html')
  const index = readFileSync(indexPath, 'utf8')
  if (!index.includes(first.path)) throw new Error('Missing original worker path')
  writeFileSync(indexPath, index.replace(first.path, '__HOMEBOX_SHELL_WORKER__'))
  const manifestPath = join(applicationWebDirectory, '.vite', 'manifest.json')
  const manifest = JSON.parse(readFileSync(manifestPath, 'utf8')) as Record<string, unknown>
  manifest['private-upgrade-test'] = { file: 'assets/shell-extra-AbCd1234.js', isDynamicEntry: true }
  writeFileSync(join(applicationWebDirectory, 'assets', 'shell-extra-AbCd1234.js'), 'export const privateUpgradeTest = true')
  writeFileSync(manifestPath, JSON.stringify(manifest))
  const versionPath = join(applicationWebDirectory, 'pwa-version.json')
  const buildPath = join(applicationWebDirectory, 'web-build.json')
  const packageVersion = (JSON.parse(readFileSync(join(root, 'package.json'), 'utf8')) as { version: string }).version
  expect(JSON.parse(readFileSync(versionPath, 'utf8'))).toEqual({ worker: first.path })
  expect(JSON.parse(readFileSync(buildPath, 'utf8'))).toEqual({ worker: first.path, web_version: packageVersion })
  unlinkSync(versionPath)
  unlinkSync(buildPath)
  execFileSync(process.execPath, [join(root, 'scripts', 'build-shell-worker.mjs')], { stdio: 'pipe' })
  const nextPath = readFileSync(indexPath, 'utf8').match(/name="homebox-shell-worker" content="(\/pwa-worker-[a-f0-9]{24}\.js)"/)?.[1]
  expect(nextPath).toBeTruthy()
  expect(nextPath).not.toBe(first.path)
  expect(JSON.parse(readFileSync(buildPath, 'utf8'))).toEqual({ worker: nextPath, web_version: packageVersion })

  const result = await page.evaluate(async (path) => {
    const registration = await navigator.serviceWorker.register(path, { scope: '/', updateViaCache: 'none' })
    const worker = registration.installing ?? registration.waiting
    if (!worker) throw new Error('No new worker')
    const state = await new Promise<string>((resolve, reject) => {
      const timeout = setTimeout(() => reject(new Error('New worker did not finish installation')), 30_000)
      const check = () => {
        if (worker.state === 'installed' || worker.state === 'redundant') {
          clearTimeout(timeout)
          worker.removeEventListener('statechange', check)
          resolve(worker.state)
        }
      }
      worker.addEventListener('statechange', check)
      check()
    })
    return { state, path: worker.scriptURL }
  }, nextPath!)
  expect(result).toEqual({ state: 'installed', path: `${application.url}${nextPath}` })
  await expect.poll(() => page.evaluate(async () => (await navigator.serviceWorker.getRegistration('/'))?.waiting?.scriptURL)).toBe(`${application.url}${nextPath}`)
  expect(await page.evaluate(() => navigator.serviceWorker.controller?.scriptURL)).toBe(`${application.url}${first.path}`)
  expect(await second.evaluate(() => navigator.serviceWorker.controller?.scriptURL)).toBe(`${application.url}${first.path}`)
  await expect(firstDraft).toHaveValue('First unsaved item')
  await expect(secondDraft).toHaveValue('Second unsaved item')
  expect(writes).toHaveLength(0)
  const names = await page.evaluate(() => caches.keys())
  expect(names).toContain(`homebox-shell-v1-${first.path.slice('/pwa-worker-'.length, -'.js'.length)}`)
  expect(names).toContain(`homebox-shell-v1-${nextPath!.slice('/pwa-worker-'.length, -'.js'.length)}`)
  await second.close()
})

const corruptTest = applicationTest.extend<{}, { applicationWebDirectory: string }>({
  applicationWebDirectory: [async ({ browserName }, provideFixture) => {
    const directory = mkdtempSync(join(tmpdir(), `homebox-corrupt-shell-${browserName}-`))
    try {
      const web = join(directory, 'web')
      cpSync(fileURLToPath(new URL('../../dist', import.meta.url)), web, { recursive: true })
      const assets = join(web, 'assets')
      const target = readdirSync(assets).find((name) => /^capture-page-.*\.js$/.test(name))
      if (!target) throw new Error('Missing lazy capture chunk')
      const path = join(assets, target), bytes = readFileSync(path)
      bytes[bytes.length - 1] = bytes[bytes.length - 1]! ^ 1
      writeFileSync(path, bytes)
      await provideFixture(web)
    } finally { rmSync(directory, { recursive: true, force: true }) }
  }, { scope: 'worker' }],
})

corruptTest('rejects a same-size altered shell asset without activating or deleting an old cache', async ({ page, application }) => {
  await page.goto(application.url)
  await page.evaluate(async () => {
    const prior = await caches.open('homebox-shell-v1-prior-fixture')
    await prior.put('/sentinel', new Response('keep prior shell'))
  })
  const result = await registerShell(page)
  expect(result.state).toBe('redundant')
  const state = await page.evaluate(async () => {
    const registration = await navigator.serviceWorker.getRegistration('/')
    const prior = await (await caches.open('homebox-shell-v1-prior-fixture')).match('/sentinel')
    return { active: registration?.active?.state ?? null, controller: navigator.serviceWorker.controller?.state ?? null, prior: await prior?.text() }
  })
  expect(state).toEqual({ active: null, controller: null, prior: 'keep prior shell' })
})
