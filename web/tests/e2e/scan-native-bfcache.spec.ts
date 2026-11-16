import { messages } from '../../src/lib/messages.ts'
import { test, expect } from './auth-fixture.ts'
import { leaveDocument, permitBFCache, returnFromBFCache } from './bfcache-helpers.ts'
import { provision, watchDomainWrites } from './domain-helpers.ts'
import { installNativeProbe } from './native-scan-probe.ts'

// Full Chromium exercises actual BFCache; the detector and camera are test
// substitutes, not evidence of native pixel decoding or a physical device.
test.use({ launchOptions: { channel: 'chromium', ignoreDefaultArgs: ['--disable-back-forward-cache'], args: ['--use-fake-device-for-media-stream', '--use-fake-ui-for-media-stream'] }, permissions: ['camera'], trace: 'off', screenshot: 'off', video: 'off' })
const text = messages.scan

test('clears detected private text before an actual BFCache restoration', async ({ page, application }) => {
  await installNativeProbe(page); await provision(page, application)
  const diagnostics = await permitBFCache(page, application.url), writes = watchDomainWrites(page, ['/api/v1/items', '/api/v1/barcodes'])
  await page.goto('/scan')
  await page.getByRole('textbox', { name: text.input }).fill('private manual value')
  await page.evaluate(() => { window.__nativeScanProbe.queue.push([{ format: 'qr_code', rawValue: 'private detected value' }]) })
  await page.getByRole('button', { name: text.cameraStart }).click()
  await expect(page.getByRole('list', { name: text.nativeResults })).toContainText('private detected value')
  expect(await page.evaluate(() => window.__nativeScanProbe.tracks[0]?.readyState)).toBe('ended')
  await leaveDocument(page, false)
  await returnFromBFCache(page, diagnostics)
  await expect(page.getByText(text.restoreReady)).toBeVisible()
  await expect(page.locator('body')).not.toContainText('private detected value')
  await expect(page.locator('body')).not.toContainText('private manual value')
  await page.getByRole('button', { name: text.restoreContinue }).click()
  await expect(page.getByRole('textbox', { name: text.input })).toHaveValue('')
  await expect(page.getByRole('list', { name: text.nativeResults })).toHaveCount(0)
  expect(await page.evaluate(() => window.__nativeScanProbe.tracks.length)).toBe(1)
  expect(writes).toEqual([])
})

test('ignores a pending detection returned after an actual BFCache restoration', async ({ page, application }) => {
  await installNativeProbe(page); await provision(page, application)
  const diagnostics = await permitBFCache(page, application.url), writes = watchDomainWrites(page, ['/api/v1/items', '/api/v1/barcodes'])
  await page.goto('/scan')
  await page.evaluate(() => { window.__nativeScanProbe.hold = true })
  await page.getByRole('button', { name: text.cameraStart }).click()
  await expect.poll(() => page.evaluate(() => !!window.__nativeScanProbe.finish)).toBe(true)
  await leaveDocument(page, false)
  await returnFromBFCache(page, diagnostics)
  await expect(page.getByText(text.restoreReady)).toBeVisible()
  expect(await page.evaluate(() => window.__nativeScanProbe.tracks[0]?.readyState)).toBe('ended')
  await page.evaluate(() => { window.__nativeScanProbe.finish?.([{ format: 'qr_code', rawValue: 'private late result' }]) })
  await page.getByRole('button', { name: text.restoreContinue }).click()
  await expect(page.getByRole('button', { name: text.cameraStart })).toBeEnabled()
  await expect(page.getByRole('list', { name: text.nativeResults })).toHaveCount(0)
  await expect(page.locator('body')).not.toContainText('private late result')
  expect(await page.evaluate(() => window.__nativeScanProbe.calls)).toBe(1)
  expect(writes).toEqual([])
})
