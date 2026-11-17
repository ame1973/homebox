import { messages } from '../../src/lib/messages.ts'
import { test, expect } from './auth-fixture.ts'
import { leaveDocument, permitBFCache, returnFromBFCache } from './bfcache-helpers.ts'
import { provision, watchDomainWrites } from './domain-helpers.ts'

const text = messages.mediaRecovery

test.use({ launchOptions: { channel: 'chromium', ignoreDefaultArgs: ['--disable-back-forward-cache'] }, trace: 'off', screenshot: 'off', video: 'off' })

test('redacts the prior status through actual BFCache and rereads only after fresh session confirmation', async ({ page, application }) => {
  await provision(page, application)
  const diagnostics = await permitBFCache(page, application.url), writes = watchDomainWrites(page, ['/api/v1/system/media-recovery'])
  let reads = 0
  page.on('request', (request) => { if (new URL(request.url()).pathname === '/api/v1/system/media-recovery') reads++ })
  await page.goto('/settings/media-recovery')
  await expect(page.getByRole('heading', { name: text.phases.idle }).or(page.getByRole('heading', { name: text.phases.unavailable }))).toBeVisible()
  const initialReads = reads
  await leaveDocument(page, false)
  await returnFromBFCache(page, diagnostics)
  await expect(page.getByRole('heading', { name: text.restoreTitle })).toBeVisible()
  await expect(page.getByText(messages.scan.restoreReady)).toBeVisible()
  await expect(page.getByRole('heading', { name: text.phases.idle })).toHaveCount(0)
  await expect(page.getByRole('heading', { name: text.phases.unavailable })).toHaveCount(0)
  expect(reads).toBe(initialReads)
  await page.getByRole('button', { name: messages.scan.restoreContinue }).click()
  await expect(page.getByRole('heading', { name: text.phases.idle }).or(page.getByRole('heading', { name: text.phases.unavailable }))).toBeVisible()
  expect(reads).toBeGreaterThan(initialReads)
  expect(writes).toEqual([])
})
