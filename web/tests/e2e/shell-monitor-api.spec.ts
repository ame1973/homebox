import { execFileSync } from 'node:child_process'
import { copyFileSync, cpSync, mkdirSync, mkdtempSync, readFileSync, rmSync, unlinkSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import type { Page } from '@playwright/test'
import { build } from 'vite'
import { messages } from '../../src/lib/messages.ts'
import { test as applicationTest, expect } from './auth-fixture.ts'
import { leaveDocument, permitBFCache, returnFromBFCache } from './bfcache-helpers.ts'
import { provision, watchDomainWrites } from './domain-helpers.ts'

const test = applicationTest.extend<{}, { applicationWebDirectory: string }>({
  applicationWebDirectory: [async ({ browserName }, provideFixture) => {
    const root = mkdtempSync(join(tmpdir(), `homebox-shell-monitor-${browserName}-`))
    const web = join(root, 'dist'), sdk = join(root, 'sdk'), entry = join(root, 'entry.ts')
    try {
      cpSync(fileURLToPath(new URL('../../dist', import.meta.url)), web, { recursive: true })
      mkdirSync(join(root, 'scripts')); mkdirSync(join(root, 'src', 'pwa'), { recursive: true })
      copyFileSync(fileURLToPath(new URL('../../scripts/build-shell-worker.mjs', import.meta.url)), join(root, 'scripts', 'build-shell-worker.mjs'))
      copyFileSync(fileURLToPath(new URL('../../src/pwa/shell-worker.js', import.meta.url)), join(root, 'src', 'pwa', 'shell-worker.js'))
      const module = fileURLToPath(new URL('../../src/lib/shell-monitor.ts', import.meta.url))
      const source = fileURLToPath(new URL('../../src', import.meta.url))
      writeFileSync(entry, `export * from ${JSON.stringify(module)};\n`)
      await build({ configFile: false, logLevel: 'silent', resolve: { alias: { '@': source } }, build: { outDir: sdk, minify: false, lib: { entry, formats: ['es'], fileName: 'shell-monitor-sdk' } } })
      cpSync(join(sdk, 'shell-monitor-sdk.js'), join(web, 'assets', 'shell-monitor-sdk.js'))
      writeFileSync(join(web, 'foreign-worker.js'), 'self.addEventListener("install", () => {});\n')
      await provideFixture(web)
    } finally { rmSync(root, { recursive: true, force: true }) }
  }, { scope: 'worker' }],
})

test.use({ launchOptions: { channel: 'chromium', ignoreDefaultArgs: ['--disable-back-forward-cache'] }, trace: 'off', screenshot: 'off', video: 'off' })

type BrowserMonitor = { states: { phase: string; busy: boolean }[]; check: () => boolean; prepare: () => boolean; stop: () => void }
async function start(page: Page) {
  await page.evaluate(async () => {
    const module = '/assets/shell-monitor-sdk.js', sdk = await import(module)
    const states: { phase: string; busy: boolean }[] = []
    const monitor = sdk.monitorShell((value: { phase: string; busy: boolean }) => { states.push(value) })
    ;(window as Window & { __shellMonitor?: BrowserMonitor }).__shellMonitor = { states, ...monitor }
  })
}
const phase = (page: Page) => page.evaluate(() => (window as Window & { __shellMonitor?: BrowserMonitor }).__shellMonitor?.states.at(-1))
const action = (page: Page, kind: 'check' | 'prepare') => page.evaluate((name) => (window as Window & { __shellMonitor?: BrowserMonitor }).__shellMonitor![name](), kind)

function publishSecondVersion(web: string, first: string) {
  const root = dirname(web), indexPath = join(web, 'index.html')
  const index = readFileSync(indexPath, 'utf8')
  if (!index.includes(first)) throw new Error('Missing initial worker URL')
  writeFileSync(indexPath, index.replace(first, '__HOMEBOX_SHELL_WORKER__'))
  const manifestPath = join(web, '.vite', 'manifest.json')
  const manifest = JSON.parse(readFileSync(manifestPath, 'utf8')) as Record<string, unknown>
  manifest['private-shell-monitor-test'] = { file: 'assets/shell-monitor-extra-AbCd1234.js', isDynamicEntry: true }
  writeFileSync(join(web, 'assets', 'shell-monitor-extra-AbCd1234.js'), 'export const shellMonitorTest = true')
  writeFileSync(manifestPath, JSON.stringify(manifest))
  const version = join(web, 'pwa-version.json')
  expect(JSON.parse(readFileSync(version, 'utf8'))).toEqual({ worker: first })
  unlinkSync(version)
  execFileSync(process.execPath, [join(root, 'scripts', 'build-shell-worker.mjs')], { stdio: 'pipe' })
  const next = JSON.parse(readFileSync(version, 'utf8')) as { worker: string }
  expect(next.worker).not.toBe(first)
  return next.worker
}

test('installs a verified public shell after a live cookie-free version read', async ({ page, context, application }) => {
  await provision(page, application)
  const versionRequests: { method: string; headers: Record<string, string> }[] = []
  page.on('request', (request) => {
    if (new URL(request.url()).pathname === '/pwa-version.json') {
      void request.allHeaders().then((headers) => { versionRequests.push({ method: request.method(), headers }) })
    }
  })
  await page.goto('/')
  await start(page)
  await expect.poll(() => phase(page)).toEqual({ phase: 'ready', busy: false })
  expect(versionRequests.length).toBeGreaterThan(0)
  expect(versionRequests.every((request) => request.method === 'GET' && !request.headers.cookie && !request.headers.authorization)).toBe(true)
  const registered = await page.evaluate(async () => (await navigator.serviceWorker.getRegistration('/'))?.active?.scriptURL)
  const expected = await page.evaluate(() => document.querySelector<HTMLMetaElement>('meta[name="homebox-shell-worker"]')?.content)
  expect(registered).toBe(`${application.url}${expected}`)
  const writes = watchDomainWrites(page, ['/api/v1/'])
  await context.setOffline(true)
  const offline = await context.newPage()
  await offline.goto(`${application.url}/locations`, { waitUntil: 'domcontentloaded' })
  await expect(offline.locator('#root')).not.toBeEmpty()
  expect(await offline.evaluate(async () => {
    try { await fetch('/api/v1/auth/me', { cache: 'no-store' }); return 'unexpected' } catch { return 'network-error' }
  })).toBe('network-error')
  await context.setOffline(false)
  expect(writes).toEqual([])
})

test('serializes simultaneous first registration across real browser tabs', async ({ page, context, application }) => {
  await context.addInitScript(() => {
    const original = ServiceWorkerContainer.prototype.register
    ServiceWorkerContainer.prototype.register = function (...args) {
      const state = window as Window & { __registerCount?: number }
      state.__registerCount = (state.__registerCount ?? 0) + 1
      return original.apply(this, args)
    }
  })
  const other = await context.newPage()
  await Promise.all([page.goto(application.url), other.goto(application.url)])
  await Promise.all([start(page), start(other)])
  await expect.poll(() => phase(page)).toEqual({ phase: 'ready', busy: false })
  await expect.poll(() => phase(other)).toEqual({ phase: 'ready', busy: false })
  const counts = await Promise.all([page, other].map((tab) => tab.evaluate(() => (window as Window & { __registerCount?: number }).__registerCount ?? 0)))
  expect(counts[0]! + counts[1]!).toBe(1)
  expect((await page.evaluate(() => navigator.serviceWorker.getRegistrations())).length).toBe(1)
})

test('does not replace an existing foreign root worker', async ({ page, application }) => {
  await page.goto(application.url)
  await page.evaluate(async () => {
    const registration = await navigator.serviceWorker.register('/foreign-worker.js', { scope: '/' })
    const worker = registration.installing ?? registration.active
    if (!worker) throw new Error('Missing foreign worker')
    await new Promise<void>((resolve, reject) => {
      const check = () => {
        if (worker.state === 'activated') resolve()
        else if (worker.state === 'redundant') reject(new Error('Foreign worker failed'))
      }
      worker.addEventListener('statechange', check); check()
    })
  })
  await start(page)
  await expect.poll(() => phase(page)).toEqual({ phase: 'foreign', busy: false })
  expect(await action(page, 'prepare')).toBe(false)
  expect(await page.evaluate(async () => (await navigator.serviceWorker.getRegistration('/'))?.active?.scriptURL)).toBe(`${application.url}/foreign-worker.js`)
})

test('keeps a failed version check explicit until the user retries preparation', async ({ page, application }) => {
  const version = (url: URL) => url.pathname === '/pwa-version.json'
  await page.route(version, (route) => route.fulfill({ status: 200, headers: { 'Cache-Control': 'no-store', 'Content-Type': 'application/json' }, body: '{"worker":"/foreign.js"}\n' }))
  await page.goto(application.url)
  await start(page)
  await expect.poll(() => phase(page)).toEqual({ phase: 'error', busy: false })
  expect(await page.evaluate(async () => (await navigator.serviceWorker.getRegistrations()).length)).toBe(0)
  await page.unroute(version)
  expect(await action(page, 'prepare')).toBe(true)
  await expect.poll(() => phase(page)).toEqual({ phase: 'ready', busy: false })
})

test('checks status without re-registering after actual BFCache restoration', async ({ page, application }) => {
  const diagnostics = await permitBFCache(page, application.url)
  const version = (url: URL) => url.pathname === '/pwa-version.json'
  // This descriptor must keep its real no-store policy during the BFCache fixture.
  await page.route(version, (route) => route.continue())
  await page.addInitScript(() => {
    const original = ServiceWorkerContainer.prototype.register
    ServiceWorkerContainer.prototype.register = function (...args) {
      const state = window as Window & { __registerCount?: number }
      state.__registerCount = (state.__registerCount ?? 0) + 1
      return original.apply(this, args)
    }
  })
  await page.goto(application.url)
  await start(page)
  await expect.poll(() => phase(page)).toEqual({ phase: 'ready', busy: false })
  await page.evaluate(() => {
    window.addEventListener('pagehide', () => {
      const state = window as Window & { __shellMonitor?: BrowserMonitor; __hiddenShell?: string }
      state.__hiddenShell = state.__shellMonitor?.states.at(-1)?.phase
    })
  })
  await leaveDocument(page, false)
  await returnFromBFCache(page, diagnostics)
  expect(await page.evaluate(() => (window as Window & { __hiddenShell?: string }).__hiddenShell)).toBe('paused')
  await expect.poll(() => phase(page)).toEqual({ phase: 'ready', busy: false })
  expect(await page.evaluate(() => (window as Window & { __registerCount?: number }).__registerCount)).toBe(1)
})

test('requires a current page and explicit action while two unsaved tabs retain their forms', async ({ page, context, application, applicationWebDirectory }) => {
  await provision(page, application)
  await page.goto('/')
  await start(page)
  await expect.poll(() => phase(page)).toEqual({ phase: 'ready', busy: false })
  const first = await page.evaluate(() => document.querySelector<HTMLMetaElement>('meta[name="homebox-shell-worker"]')!.content)
  await page.goto('/items/new')
  await start(page)
  await expect.poll(() => phase(page)).toEqual({ phase: 'ready', busy: false })
  const name = new RegExp(`^${messages.catalog.fields.name}`)
  const oldDraft = page.getByLabel(name)
  await oldDraft.fill('Old tab unsaved Item')
  const writes = watchDomainWrites(page, ['/api/v1/'])
  const next = publishSecondVersion(applicationWebDirectory, first)
  expect(await action(page, 'check')).toBe(true)
  await expect.poll(() => phase(page)).toEqual({ phase: 'outdated', busy: false })
  expect(await action(page, 'prepare')).toBe(false)
  expect(await oldDraft.inputValue()).toBe('Old tab unsaved Item')
  const other = await context.newPage()
  await other.goto(`${application.url}/items/new`)
  await start(other)
  await expect.poll(() => phase(other)).toEqual({ phase: 'available', busy: false })
  const newDraft = other.getByLabel(name)
  await newDraft.fill('New tab unsaved Item')
  expect(await action(other, 'prepare')).toBe(true)
  await expect.poll(() => phase(other)).toEqual({ phase: 'waiting', busy: false })
  expect(await page.evaluate(async () => (await navigator.serviceWorker.getRegistration('/'))?.active?.scriptURL)).toBe(`${application.url}${first}`)
  expect(await other.evaluate(async () => (await navigator.serviceWorker.getRegistration('/'))?.waiting?.scriptURL)).toBe(`${application.url}${next}`)
  expect(await page.evaluate(() => navigator.serviceWorker.controller?.scriptURL)).toBe(`${application.url}${first}`)
  expect(await other.evaluate(() => navigator.serviceWorker.controller?.scriptURL)).toBe(`${application.url}${first}`)
  await expect(oldDraft).toHaveValue('Old tab unsaved Item')
  await expect(newDraft).toHaveValue('New tab unsaved Item')
  expect(writes).toEqual([])
  await expect.poll(() => context.serviceWorkers().some((worker) => worker.url() === `${application.url}${next}`)).toBe(true)
  const waiting = context.serviceWorkers().find((worker) => worker.url() === `${application.url}${next}`)!
  for (const tab of [page, other]) {
    let confirmed = false
    tab.once('dialog', async (dialog) => { confirmed = dialog.type() === 'beforeunload'; await dialog.accept() })
    const closed = tab.waitForEvent('close')
    await tab.close({ runBeforeUnload: true }); await closed
    expect(confirmed).toBe(true)
  }
  await expect.poll(async () => {
    try { return await waiting.evaluate(() => (globalThis as unknown as { registration: ServiceWorkerRegistration }).registration.active?.scriptURL) }
    catch { return undefined }
  }).toBe(`${application.url}${next}`)
  const restored = await context.newPage()
  await restored.goto(application.url)
  expect(await restored.evaluate(() => navigator.serviceWorker.controller?.scriptURL)).toBe(`${application.url}${next}`)
})
