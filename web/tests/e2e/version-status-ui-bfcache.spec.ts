import { messages } from '../../src/lib/messages.ts'
import { test, expect } from './auth-fixture.ts'
import { leaveDocument, permitBFCache, returnFromBFCache } from './bfcache-helpers.ts'
import { provision, watchDomainWrites } from './domain-helpers.ts'

const text = messages.versionStatus

test.use({ launchOptions: { channel: 'chromium', ignoreDefaultArgs: ['--disable-back-forward-cache'] }, trace: 'off', screenshot: 'off', video: 'off' })

test('redacts version information through actual BFCache until fresh authorization and continuation', async ({ page, application }) => {
  await provision(page, application)
  const diagnostics = await permitBFCache(page, application.url), writes = watchDomainWrites(page, ['/api/v1/system/version'])
  // Preserve the actual no-store version response required by its adapter.
  await page.route((url) => url.pathname === '/api/v1/system/version', (route) => route.continue())
  let reads = 0
  page.on('request', (request) => { if (new URL(request.url()).pathname === '/api/v1/system/version') reads++ })
  const versionResponse = page.waitForResponse((response) => new URL(response.url()).pathname === '/api/v1/system/version')
  await page.goto('/settings/version')
  expect((await versionResponse).headers()['cache-control']).toBe('no-store')
  await expect(page.getByText(text.writeAccepted)).toBeVisible()
  const initialReads = reads
  await leaveDocument(page, false)
  await returnFromBFCache(page, diagnostics)
  await expect(page.getByRole('heading', { name: text.restoreTitle })).toBeVisible()
  await expect(page.getByText(messages.scan.restoreReady)).toBeVisible()
  await expect(page.getByText(text.writeAccepted)).toHaveCount(0)
  expect(reads).toBe(initialReads)
  await page.getByRole('button', { name: messages.scan.restoreContinue }).click()
  await expect(page.getByText(text.writeAccepted)).toBeVisible()
  expect(reads).toBeGreaterThan(initialReads)
  expect(writes).toEqual([])
})
