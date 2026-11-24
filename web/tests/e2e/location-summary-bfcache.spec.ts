import { messages } from '../../src/lib/messages.ts'
import { test, expect } from './auth-fixture.ts'
import { leaveDocument, permitBFCache, returnFromBFCache } from './bfcache-helpers.ts'
import { provision, watchDomainWrites, writeContext } from './domain-helpers.ts'

const text = messages.locations

test.use({ launchOptions: { channel: 'chromium', ignoreDefaultArgs: ['--disable-back-forward-cache'] }, trace: 'off', screenshot: 'off', video: 'off' })

test('redacts the summary before actual BFCache and requires fresh session confirmation and continuation', async ({ page, application }) => {
  await provision(page, application)
  const created = await page.request.post('/api/v1/locations', { headers: await writeContext(page, application.url), data: { name: '還原位置', code: 'SUMMARY-BFCACHE' } })
  expect(created.status()).toBe(200)
  const root = await created.json() as { id: string }
  const diagnostics = await permitBFCache(page, application.url)
  const writes = watchDomainWrites(page, ['/api/v1/locations'])
  let reads = 0, sessionReads = 0
  page.on('request', (request) => {
    const path = new URL(request.url()).pathname
    if (path === `/api/v1/locations/${root.id}/summary`) reads++
    if (path === '/api/v1/auth/me') sessionReads++
  })
  await page.goto(`/locations/${root.id}`)
  const region = page.getByRole('region', { name: text.summaryTitle, exact: true })
  await expect(region.getByText(text.summaryDirectUnits, { exact: true })).toBeVisible()
  await page.evaluate((label) => {
    window.addEventListener('pagehide', () => {
      (window as Window & { __summaryRedacted?: boolean }).__summaryRedacted = !document.body.innerText.includes(label)
    })
  }, text.summaryDirectUnits)
  const initialReads = reads, initialSessionReads = sessionReads
  await leaveDocument(page, false)
  await returnFromBFCache(page, diagnostics)
  expect(await page.evaluate(() => (window as Window & { __summaryRedacted?: boolean }).__summaryRedacted)).toBe(true)
  await expect(region.getByText(text.summaryRestore)).toBeVisible()
  await expect(region.getByText(text.summaryDirectUnits, { exact: true })).toHaveCount(0)
  expect(reads).toBe(initialReads)
  await region.getByRole('button', { name: text.summaryContinue }).click()
  await expect(region.getByText(text.summaryDirectUnits, { exact: true })).toBeVisible()
  await expect(region.getByRole('heading', { name: text.summaryTitle })).toBeFocused()
  expect(sessionReads).toBeGreaterThan(initialSessionReads)
  expect(reads).toBeGreaterThan(initialReads)
  expect(writes).toEqual([])
})
