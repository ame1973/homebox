import { messages } from '../../src/lib/messages.ts'
import { test, expect } from './auth-fixture.ts'
import { leaveDocument, permitBFCache, returnFromBFCache } from './bfcache-helpers.ts'
import { provision, watchDomainWrites, writeContext } from './domain-helpers.ts'

const text = messages.locations

test.use({ launchOptions: { channel: 'chromium', ignoreDefaultArgs: ['--disable-back-forward-cache'] }, trace: 'off', screenshot: 'off', video: 'off' })

test('redacts contents before actual BFCache and requires a fresh session and explicit first-page read', async ({ page, application }) => {
  await provision(page, application)
  const created = await page.request.post('/api/v1/locations', { headers: await writeContext(page, application.url), data: { name: '內容還原位置', code: 'CONTENTS-BFCACHE' } })
  expect(created.status()).toBe(200)
  const root = await created.json() as { id: string }
  const item = await page.request.post('/api/v1/items', { headers: await writeContext(page, application.url), data: { name: '內容還原物品' } })
  expect(item.status()).toBe(200)
  const owner = await item.json() as { id: string; revision: number }
  const unit = await page.request.post(`/api/v1/items/${owner.id}/units`, { headers: await writeContext(page, application.url, owner.revision), data: { location_id: root.id } })
  expect(unit.status()).toBe(200)
  const diagnostics = await permitBFCache(page, application.url)
  const writes = watchDomainWrites(page, ['/api/v1/locations', '/api/v1/units'])
  let reads = 0, sessionReads = 0
  page.on('request', (request) => {
    const path = new URL(request.url()).pathname
    if (path === `/api/v1/locations/${root.id}/contents`) reads++
    if (path === '/api/v1/auth/me') sessionReads++
  })
  await page.goto(`/locations/${root.id}`)
  const region = page.getByRole('region', { name: text.contentsTitle, exact: true })
  const open = region.getByRole('button', { name: text.contentsOpen })
  await expect(open).toHaveAttribute('aria-disabled', 'false'); await open.click()
  await expect(region.getByText(text.contentsUnits, { exact: true })).toBeVisible()
  await page.evaluate((label) => {
    window.addEventListener('pagehide', () => {
      (window as Window & { __contentsRedacted?: boolean }).__contentsRedacted = !document.body.innerText.includes(label)
    })
  }, text.contentsUnits)
  const initialReads = reads, initialSessionReads = sessionReads
  await leaveDocument(page, false); await returnFromBFCache(page, diagnostics)
  expect(await page.evaluate(() => (window as Window & { __contentsRedacted?: boolean }).__contentsRedacted)).toBe(true)
  await expect(region.getByText(text.contentsRestore)).toBeVisible()
  await expect(region.getByText(text.contentsUnits, { exact: true })).toHaveCount(0)
  expect(reads).toBe(initialReads)
  await region.getByRole('button', { name: text.contentsContinue }).click()
  await expect(region.getByText(text.contentsUnits, { exact: true })).toBeVisible()
  await expect(region.getByRole('heading', { name: text.contentsTitle })).toBeFocused()
  expect(sessionReads).toBeGreaterThan(initialSessionReads)
  expect(reads).toBeGreaterThan(initialReads)
  expect(writes).toEqual([])
})
