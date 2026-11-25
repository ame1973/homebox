import { messages } from '../../src/lib/messages.ts'
import { quoteExactText } from '../../src/lib/text-literal.ts'
import { test, expect } from './auth-fixture.ts'
import { leaveDocument, permitBFCache, returnFromBFCache } from './bfcache-helpers.ts'
import { provision, watchDomainWrites } from './domain-helpers.ts'
import { deletedMovementOwner, firstMovement, movementRoot, prepareMovementScreen } from './location-movements-fixture.ts'

const text = messages.locations

test.use({ launchOptions: { channel: 'chromium', ignoreDefaultArgs: ['--disable-back-forward-cache'] }, trace: 'off', screenshot: 'off', video: 'off', applicationDatabaseFixture: { prepare: prepareMovementScreen } })

test('redacts movement history before actual BFCache and requires fresh authorization and explicit continuation', async ({ page, application }) => {
  await provision(page, application)
  const diagnostics = await permitBFCache(page, application.url)
  const writes = watchDomainWrites(page, ['/api/v1/locations', '/api/v1/location-movements'])
  let reads = 0, sessionReads = 0
  page.on('request', (request) => {
    const path = new URL(request.url()).pathname
    if (path === `/api/v1/locations/${movementRoot}/movements`) reads++
    if (path === '/api/v1/auth/me') sessionReads++
  })
  await page.goto(`/locations/${movementRoot}`)
  const region = page.getByRole('region', { name: text.movementsTitle, exact: true })
  const open = region.getByRole('button', { name: text.movementsOpen, exact: true })
  await expect(open).toHaveAttribute('aria-disabled', 'false'); await open.click()
  await expect(region.getByText(quoteExactText(firstMovement), { exact: true })).toBeVisible()
  await page.evaluate((label) => {
    window.addEventListener('pagehide', () => {
      (window as Window & { __movementsRedacted?: boolean }).__movementsRedacted = !document.body.innerText.includes(label)
    })
  }, quoteExactText(firstMovement))
  const initialReads = reads, initialSessionReads = sessionReads
  await leaveDocument(page, false); await returnFromBFCache(page, diagnostics)
  expect(await page.evaluate(() => (window as Window & { __movementsRedacted?: boolean }).__movementsRedacted)).toBe(true)
  await expect(region.getByText(text.movementsRestore)).toBeVisible()
  await expect(region.getByText(quoteExactText(firstMovement), { exact: true })).toHaveCount(0)
  expect(reads).toBe(initialReads)
  await region.getByRole('button', { name: text.movementsContinue, exact: true }).click()
  await expect(region.getByText(quoteExactText(firstMovement), { exact: true })).toBeVisible()
  await expect(region.getByRole('heading', { name: text.movementsTitle })).toBeFocused()
  expect(sessionReads).toBeGreaterThan(initialSessionReads)
  expect(reads).toBeGreaterThan(initialReads)
  expect(writes).toEqual([])
})

test('redacts deleted-owner lookup input and history before real BFCache and requires fresh explicit selection', async ({ page, application }) => {
  await provision(page, application)
  const diagnostics = await permitBFCache(page, application.url)
  let reads = 0, sessionReads = 0
  page.on('request', (request) => {
    const path = new URL(request.url()).pathname
    if (path === '/api/v1/location-movements') reads++
    if (path === '/api/v1/auth/me') sessionReads++
  })
  await page.goto('/locations')
  const lookup = page.getByRole('region', { name: text.movementLookupTitle, exact: true })
  await lookup.getByRole('radio', { name: text.movementLookupJSON }).check()
  await lookup.getByRole('textbox', { name: text.movementLookupID }).fill(quoteExactText(deletedMovementOwner))
  const choose = lookup.getByRole('button', { name: text.movementLookupChoose })
  await expect(choose).toHaveAttribute('aria-disabled', 'false'); await choose.click()
  const history = lookup.getByRole('region', { name: text.movementsTitle, exact: true })
  await history.getByRole('button', { name: text.movementsOpen }).click()
  await expect(history.getByText(text.movementsDeleted)).toBeVisible()
  await page.evaluate((id) => {
    window.addEventListener('pagehide', () => {
      const input = document.querySelector<HTMLTextAreaElement>('#location-movement-id')
      ;(window as Window & { __lookupRedacted?: boolean }).__lookupRedacted = !document.body.innerText.includes(id) && !input?.value.includes(id)
    })
  }, quoteExactText(deletedMovementOwner))
  const initialReads = reads, initialSessionReads = sessionReads
  await leaveDocument(page, false); await returnFromBFCache(page, diagnostics)
  expect(await page.evaluate(() => (window as Window & { __lookupRedacted?: boolean }).__lookupRedacted)).toBe(true)
  await expect(lookup.getByText(text.movementLookupRestore)).toBeVisible()
  await expect(lookup.getByRole('textbox', { name: text.movementLookupID })).toHaveCount(0)
  await expect(history).toHaveCount(0)
  expect(reads).toBe(initialReads)
  await lookup.getByRole('button', { name: text.movementLookupContinue }).click()
  await expect(lookup.getByRole('textbox', { name: text.movementLookupID })).toHaveValue('')
  await expect(lookup.getByRole('heading', { name: text.movementLookupTitle })).toBeFocused()
  expect(sessionReads).toBeGreaterThan(initialSessionReads); expect(reads).toBe(initialReads)
})
