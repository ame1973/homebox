import { cpSync, mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { build } from 'vite'
import { test as applicationTest, expect } from './auth-fixture.ts'

const test = applicationTest.extend<{}, { applicationWebDirectory: string }>({
  applicationWebDirectory: [async ({ browserName }, provideFixture) => {
    const directory = mkdtempSync(join(tmpdir(), `homebox-session-channel-${browserName}-`))
    const web = join(directory, 'web'), bundle = join(directory, 'bundle')
    try {
      cpSync(fileURLToPath(new URL('../../dist', import.meta.url)), web, { recursive: true })
      // The real lifecycle module is exposed only in this disposable fixture.
      const entry = fileURLToPath(new URL('../../src/lib/session-lifecycle.ts', import.meta.url))
      await build({ configFile: false, logLevel: 'silent', build: { outDir: bundle, minify: false, lib: { entry, formats: ['es'], fileName: 'session-lifecycle-test' } } })
      cpSync(join(bundle, 'session-lifecycle-test.js'), join(web, 'assets', 'session-lifecycle-test.js'))
      await provideFixture(web)
    } finally { rmSync(directory, { recursive: true, force: true }) }
  }, { scope: 'worker' }],
})

test.use({ trace: 'off', screenshot: 'off', video: 'off' })

test('does not interpret its own asynchronous session broadcast as a remote clear', async ({ page }) => {
  await page.goto('/')
  const result = await page.evaluate(async () => {
    const path = '/assets/session-lifecycle-test.js', lifecycle = await import(path)
    const first: string[] = [], second: string[] = []
    let changed = () => {}
    const removeFirst = lifecycle.subscribeSessionCleared((source: string) => { first.push(source); changed() })
    const removeSecond = lifecycle.subscribeSessionCleared((source: string) => { second.push(source); changed() })
    const observer = new BroadcastChannel('homebox:session-cleared')
    const received = new Promise<boolean>((resolve) => {
      const deadline = setTimeout(() => resolve(false), 2000)
      observer.onmessage = (event) => { if (event.data === 'cleared') { clearTimeout(deadline); resolve(true) } }
    })
    lifecycle.notifySessionCleared()
    const sent = await received
    // Give the native asynchronous broadcasts a bounded delivery window.
    await new Promise((resolve) => setTimeout(resolve, 150))
    const ownFirst = first.slice(), ownSecond = second.slice()
    const baselineFirst = first.length, baselineSecond = second.length
    const foreign = new BroadcastChannel('homebox:session-cleared')
    const delivered = await new Promise<boolean>((resolve) => {
      const deadline = setTimeout(() => resolve(false), 2000)
      changed = () => {
        if (first.length > baselineFirst && second.length > baselineSecond) { clearTimeout(deadline); resolve(true) }
      }
      foreign.postMessage('cleared')
    })
    const remoteFirst = first.slice(baselineFirst), remoteSecond = second.slice(baselineSecond)
    removeFirst(); removeSecond(); foreign.close(); observer.close()
    return { sent, ownFirst, ownSecond, delivered, remoteFirst, remoteSecond }
  })
  expect(result).toEqual({ sent: true, ownFirst: ['local'], ownSecond: ['local'], delivered: true, remoteFirst: ['remote'], remoteSecond: ['remote'] })
})
