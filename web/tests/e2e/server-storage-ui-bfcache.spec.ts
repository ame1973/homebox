import { messages } from '../../src/lib/messages.ts'
import { test, expect } from './auth-fixture.ts'
import { leaveDocument, permitBFCache, returnFromBFCache } from './bfcache-helpers.ts'
import { provision, watchDomainWrites } from './domain-helpers.ts'

const text = messages.serverStorage

test.use({ launchOptions: { channel: 'chromium', ignoreDefaultArgs: ['--disable-back-forward-cache'] }, trace: 'off', screenshot: 'off', video: 'off' })

test('redacts server storage through actual BFCache until fresh authorization and continuation', async ({ page, application }) => {
  await provision(page, application)
  const diagnostics = await permitBFCache(page, application.url), writes = watchDomainWrites(page, ['/api/v1/system/storage'])
  // Preserve the actual no-store storage response required by its adapter.
  await page.route((url) => url.pathname === '/api/v1/system/storage', (route) => route.continue())
  let reads = 0
  page.on('request', (request) => { if (new URL(request.url()).pathname === '/api/v1/system/storage') reads++ })
  const observation = page.waitForResponse((response) => new URL(response.url()).pathname === '/api/v1/system/storage')
  await page.goto('/settings/storage')
  expect((await observation).headers()['cache-control']).toBe('no-store')
  await expect(page.getByText(text.filesystemHelp)).toBeVisible()
  const initialReads = reads
  await leaveDocument(page, false)
  await returnFromBFCache(page, diagnostics)
  await expect(page.getByRole('heading', { name: text.restoreTitle })).toBeVisible()
  await expect(page.getByText(messages.scan.restoreReady)).toBeVisible()
  await expect(page.getByText(text.filesystemHelp)).toHaveCount(0)
  expect(reads).toBe(initialReads)
  await page.getByRole('button', { name: messages.scan.restoreContinue }).click()
  await expect(page.getByText(text.filesystemHelp)).toBeVisible()
  expect(reads).toBeGreaterThan(initialReads)
  expect(writes).toEqual([])
})
