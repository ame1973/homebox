import { execFileSync } from 'node:child_process'
import { copyFileSync, cpSync, mkdirSync, mkdtempSync, readFileSync, rmSync, unlinkSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import AxeBuilder from '@axe-core/playwright'
import { messages } from '../../src/lib/messages.ts'
import { test as applicationTest, expect } from './auth-fixture.ts'
import { leaveDocument, permitBFCache, returnFromBFCache } from './bfcache-helpers.ts'
import { noOverflow, provision, watchDomainWrites } from './domain-helpers.ts'

applicationTest.use({ launchOptions: { channel: 'chromium', ignoreDefaultArgs: ['--disable-back-forward-cache'] }, trace: 'off', screenshot: 'off', video: 'off' })
const text = messages.shell

applicationTest('prepares the first public shell and opens the interface offline without private data', async ({ page, context, application }) => {
  await provision(page, application)
  const writes = watchDomainWrites(page, ['/api/v1/'])
  await page.goto('/')
  const shell = page.getByLabel(text.label)
  await expect(shell.getByText(text.phases.ready)).toBeVisible()
  await expect(shell.getByText(text.help.ready)).toBeVisible()
  const expected = await page.evaluate(() => document.querySelector<HTMLMetaElement>('meta[name="homebox-shell-worker"]')?.content)
  expect(await page.evaluate(async () => (await navigator.serviceWorker.getRegistration('/'))?.active?.scriptURL)).toBe(`${application.url}${expected}`)
  await context.setOffline(true)
  const offline = await context.newPage()
  await offline.goto(`${application.url}/locations`, { waitUntil: 'domcontentloaded' })
  await expect(offline.locator('#root')).not.toBeEmpty()
  await expect(offline.getByLabel(text.label).getByText(text.phases.error)).toBeVisible()
  expect(await offline.evaluate(async () => {
    try { await fetch('/api/v1/auth/me', { cache: 'no-store' }); return 'unexpected' } catch { return 'network-error' }
  })).toBe('network-error')
  expect(await offline.evaluate(async () => {
    const names = await caches.keys(), keys: string[] = []
    for (const name of names) for (const request of await (await caches.open(name)).keys()) keys.push(new URL(request.url).pathname)
    return keys.some((path) => /^\/(api|auth|media|backups|exports)(\/|$)/.test(path) || path === '/pwa-version.json')
  })).toBe(false)
  await context.setOffline(false)
  await offline.getByLabel(text.label).getByRole('button', { name: text.retry }).click()
  await expect(offline.getByLabel(text.label).getByText(text.phases.ready)).toBeVisible()
  expect(writes).toEqual([])
})

applicationTest('keeps a failed initial version check explicit and restores focus after retry', async ({ page, application }) => {
  const version = (url: URL) => url.pathname === '/pwa-version.json'
  await page.route(version, (route) => route.fulfill({ status: 503, body: 'unavailable' }))
  await page.goto(application.url)
  const shell = page.getByLabel(text.label)
  await expect(shell.getByText(text.phases.error)).toBeVisible()
  expect(await page.evaluate(async () => (await navigator.serviceWorker.getRegistrations()).length)).toBe(0)
  await page.unroute(version)
  const retry = shell.getByRole('button', { name: text.retry })
  await retry.focus(); await expect(retry).toBeFocused()
  await retry.press('Enter')
  await expect(shell.getByText(text.phases.ready)).toBeVisible()
  await expect(shell.getByRole('button', { name: text.check })).toBeFocused()
})

applicationTest('continues online without changing a registration when Web Locks are unavailable', async ({ page, application }) => {
  await page.addInitScript(() => { Object.defineProperty(navigator, 'locks', { configurable: true, value: undefined }) })
  await page.goto(application.url)
  const shell = page.getByLabel(text.label)
  await expect(shell.getByText(text.phases.unsupported)).toBeVisible()
  await expect(shell.getByText(text.help.unsupported)).toBeVisible()
  await expect(shell.getByRole('button')).toHaveCount(0)
  expect(await page.evaluate(async () => (await navigator.serviceWorker.getRegistrations()).length)).toBe(0)
  await expect(page.getByText(messages.connection.ok)).toBeVisible()
})

applicationTest('serializes automatic installation across two application tabs', async ({ page, context, application }) => {
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
  await expect(page.getByLabel(text.label).getByText(text.phases.ready)).toBeVisible()
  await expect(other.getByLabel(text.label).getByText(text.phases.ready)).toBeVisible()
  const counts = await Promise.all([page, other].map((tab) => tab.evaluate(() => (window as Window & { __registerCount?: number }).__registerCount ?? 0)))
  expect(counts[0]! + counts[1]!).toBe(1)
})

applicationTest('recognizes a real root installed between its two registration reads', async ({ page, context, application }) => {
  await page.addInitScript(() => {
    const original = ServiceWorkerContainer.prototype.getRegistration
    let first = true
    ServiceWorkerContainer.prototype.getRegistration = async function (...args) {
      const registration = await original.apply(this, args)
      if (first && !registration) {
        first = false
        await new Promise<void>((resolve) => {
          const state = window as Window & { __emptyRoot?: boolean; __releaseRoot?: () => void }
          state.__releaseRoot = resolve; state.__emptyRoot = true
        })
      } else first = false
      return registration
    }
  })
  await page.goto(application.url)
  await expect.poll(() => page.evaluate(() => (window as Window & { __emptyRoot?: boolean }).__emptyRoot)).toBe(true)
  const other = await context.newPage()
  await other.goto(application.url)
  await expect(other.getByLabel(text.label).getByText(text.phases.ready)).toBeVisible()
  await page.evaluate(() => (window as Window & { __releaseRoot?: () => void }).__releaseRoot?.())
  await expect(page.getByLabel(text.label).getByText(text.phases.ready)).toBeVisible()
  const expected = await page.evaluate(() => document.querySelector<HTMLMetaElement>('meta[name="homebox-shell-worker"]')?.content)
  expect(await page.evaluate(async () => (await navigator.serviceWorker.getRegistration('/'))?.active?.scriptURL)).toBe(`${application.url}${expected}`)
})

applicationTest('rechecks the public status after actual BFCache without re-registering', async ({ page, application }) => {
  const diagnostics = await permitBFCache(page, application.url)
  await page.route((url) => url.pathname === '/pwa-version.json', (route) => route.continue())
  await page.addInitScript(() => {
    const original = ServiceWorkerContainer.prototype.register
    ServiceWorkerContainer.prototype.register = function (...args) {
      const state = window as Window & { __registerCount?: number }
      state.__registerCount = (state.__registerCount ?? 0) + 1
      return original.apply(this, args)
    }
  })
  await page.goto('/')
  await expect(page.getByLabel(text.label).getByText(text.phases.ready)).toBeVisible()
  await leaveDocument(page, false)
  await returnFromBFCache(page, diagnostics)
  await expect(page.getByLabel(text.label).getByText(text.phases.ready)).toBeVisible()
  expect(await page.evaluate(() => (window as Window & { __registerCount?: number }).__registerCount)).toBe(1)
})

applicationTest('keeps the error action accessible at 320px and 200% text in both themes', async ({ page, application }) => {
  await page.route((url) => url.pathname === '/pwa-version.json', (route) => route.abort())
  await page.setViewportSize({ width: 320, height: 568 })
  await page.goto(application.url)
  const shell = page.getByLabel(text.label)
  await expect(shell.getByText(text.phases.error)).toBeVisible()
  await page.locator('html').evaluate((element) => { element.style.fontSize = '200%' })
  const retry = shell.getByRole('button', { name: text.retry })
  await retry.focus(); await expect(retry).toBeFocused()
  for (const colorScheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme })
    await expect(page.locator('html')).toHaveAttribute('data-theme', colorScheme)
    const dimensions = await page.evaluate(() => ({ width: innerWidth, overflowing: [...document.querySelectorAll<HTMLElement>('*')].map((element) => ({ tag: element.tagName, className: element.className, right: element.getBoundingClientRect().right, width: element.getBoundingClientRect().width })).filter((value) => value.right > 320.5 || value.width > 320.5).slice(0, 20) }))
    expect(dimensions.width, JSON.stringify(dimensions.overflowing)).toBe(320)
    await noOverflow(page)
    expect(await shell.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)
    expect(await retry.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)
    expect((await retry.boundingBox())?.height).toBeGreaterThanOrEqual(44)
    expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze()).violations).toEqual([])
  }
})

const upgradeTest = applicationTest.extend<{}, { applicationWebDirectory: string }>({
  applicationWebDirectory: [async ({ browserName }, provideFixture) => {
    const root = mkdtempSync(join(tmpdir(), `homebox-shell-ui-${browserName}-`))
    try {
      const web = join(root, 'dist')
      cpSync(fileURLToPath(new URL('../../dist', import.meta.url)), web, { recursive: true })
      mkdirSync(join(root, 'scripts')); mkdirSync(join(root, 'src', 'pwa'), { recursive: true })
      copyFileSync(fileURLToPath(new URL('../../scripts/build-shell-worker.mjs', import.meta.url)), join(root, 'scripts', 'build-shell-worker.mjs'))
      copyFileSync(fileURLToPath(new URL('../../src/pwa/shell-worker.js', import.meta.url)), join(root, 'src', 'pwa', 'shell-worker.js'))
      writeFileSync(join(web, 'foreign-worker.js'), 'self.addEventListener("install", () => {});\n')
      await provideFixture(web)
    } finally { rmSync(root, { recursive: true, force: true }) }
  }, { scope: 'worker' }],
})

upgradeTest('shows a foreign root worker without replacing it', async ({ page, context, application }) => {
  await page.addInitScript(() => { Object.defineProperty(navigator, 'locks', { configurable: true, value: undefined }) })
  await page.goto('/')
  await expect(page.getByLabel(text.label).getByText(text.phases.unsupported)).toBeVisible()
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
  const other = await context.newPage()
  await other.goto(application.url)
  const shell = other.getByLabel(text.label)
  await expect(shell.getByText(text.phases.foreign)).toBeVisible()
  await shell.getByRole('button', { name: text.check }).click()
  await expect(shell.getByText(text.phases.foreign)).toBeVisible()
  expect(await other.evaluate(async () => (await navigator.serviceWorker.getRegistration('/'))?.active?.scriptURL)).toBe(`${application.url}/foreign-worker.js`)
})

upgradeTest('requires an explicit update preparation while two actual unsaved forms remain intact', async ({ page, context, application, applicationWebDirectory }) => {
  await provision(page, application)
  await page.goto('/')
  await expect(page.getByLabel(text.label).getByText(text.phases.ready)).toBeVisible()
  const first = await page.evaluate(() => document.querySelector<HTMLMetaElement>('meta[name="homebox-shell-worker"]')!.content)
  await page.goto('/items/new')
  const shell = page.getByLabel(text.label)
  await expect(shell.getByText(text.phases.ready)).toBeVisible()
  const name = new RegExp(`^${messages.catalog.fields.name}`)
  const oldDraft = page.getByLabel(name)
  await oldDraft.fill('Old application tab draft')
  const writes = watchDomainWrites(page, ['/api/v1/'])
  const root = dirname(applicationWebDirectory), indexPath = join(applicationWebDirectory, 'index.html')
  const index = readFileSync(indexPath, 'utf8')
  if (!index.includes(first)) throw new Error('Missing initial worker URL')
  writeFileSync(indexPath, index.replace(first, '__HOMEBOX_SHELL_WORKER__'))
  const manifestPath = join(applicationWebDirectory, '.vite', 'manifest.json')
  const manifest = JSON.parse(readFileSync(manifestPath, 'utf8')) as Record<string, unknown>
  manifest['private-shell-ui-test'] = { file: 'assets/shell-ui-extra-AbCd1234.js', isDynamicEntry: true }
  writeFileSync(join(applicationWebDirectory, 'assets', 'shell-ui-extra-AbCd1234.js'), 'export const shellUITest = true')
  writeFileSync(manifestPath, JSON.stringify(manifest))
  const versionPath = join(applicationWebDirectory, 'pwa-version.json')
  expect(JSON.parse(readFileSync(versionPath, 'utf8'))).toEqual({ worker: first })
  unlinkSync(versionPath)
  execFileSync(process.execPath, [join(root, 'scripts', 'build-shell-worker.mjs')], { stdio: 'pipe' })
  const next = (JSON.parse(readFileSync(versionPath, 'utf8')) as { worker: string }).worker
  expect(next).not.toBe(first)
  await shell.getByRole('button', { name: text.check }).click()
  await expect(shell.getByText(text.phases.outdated)).toBeVisible()
  await expect(oldDraft).toHaveValue('Old application tab draft')
  expect(await page.evaluate(async () => (await navigator.serviceWorker.getRegistration('/'))?.waiting?.scriptURL ?? null)).toBeNull()
  const other = await context.newPage()
  await other.goto(`${application.url}/items/new`)
  const otherShell = other.getByLabel(text.label)
  await expect(otherShell.getByText(text.phases.available)).toBeVisible()
  const newDraft = other.getByLabel(name)
  await newDraft.fill('New application tab draft')
  await other.setViewportSize({ width: 320, height: 568 })
  await other.locator('html').evaluate((element) => { element.style.fontSize = '200%' })
  const prepare = otherShell.getByRole('button', { name: text.prepareUpdate })
  for (const colorScheme of ['light', 'dark'] as const) {
    await other.emulateMedia({ colorScheme })
    await expect(other.locator('html')).toHaveAttribute('data-theme', colorScheme)
    expect(await other.evaluate(() => innerWidth)).toBe(320)
    await noOverflow(other)
    expect(await prepare.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)
    expect((await prepare.boundingBox())?.height).toBeGreaterThanOrEqual(44)
    expect((await new AxeBuilder({ page: other }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze()).violations).toEqual([])
  }
  await prepare.focus(); await prepare.press('Enter')
  await expect(otherShell.getByText(text.phases.waiting)).toBeVisible()
  const waitingAction = otherShell.getByRole('button', { name: text.check })
  await expect(waitingAction).toBeFocused()
  expect(await other.evaluate(() => innerWidth)).toBe(320)
  await noOverflow(other)
  expect(await waitingAction.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)
  expect((await waitingAction.boundingBox())?.height).toBeGreaterThanOrEqual(44)
  expect((await new AxeBuilder({ page: other }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze()).violations).toEqual([])
  expect(await page.evaluate(async () => (await navigator.serviceWorker.getRegistration('/'))?.active?.scriptURL)).toBe(`${application.url}${first}`)
  expect(await other.evaluate(async () => (await navigator.serviceWorker.getRegistration('/'))?.waiting?.scriptURL)).toBe(`${application.url}${next}`)
  await expect(oldDraft).toHaveValue('Old application tab draft')
  await expect(newDraft).toHaveValue('New application tab draft')
  expect(writes).toEqual([])
})
