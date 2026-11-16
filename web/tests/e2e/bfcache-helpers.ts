import { expect, type Page } from '@playwright/test'

export async function permitBFCache(page: Page, origin: string) {
  const original = await page.request.get('/scan')
  expect(original.headers()['cache-control']).toContain('no-store')
  let privateResponses = 0
  // Keep production headers unchanged. Override only this browser's responses
  // to exercise browsers that can restore private no-store pages from BFCache.
  await page.route((url) => url.origin === origin, async (route) => {
    if (!['document', 'fetch', 'xhr'].includes(route.request().resourceType())) { await route.continue(); return }
    const response = await route.fetch(), headers = { ...response.headers() }
    if (headers['cache-control']?.includes('no-store')) privateResponses++
    headers['cache-control'] = 'private, max-age=0'
    await route.fulfill({ response, headers })
  })
  const cdp = await page.context().newCDPSession(page)
  await cdp.send('Page.enable')
  const notUsed: unknown[] = []
  cdp.on('Page.backForwardCacheNotUsed', (event) => notUsed.push(event))
  return { privateResponses: () => privateResponses, notUsed }
}

export async function leaveDocument(page: Page, dirty: boolean) {
  await page.evaluate(() => {
    const probe = window as Window & { __homeboxRestored?: boolean }
    probe.__homeboxRestored = false
    window.addEventListener('pageshow', (event) => { probe.__homeboxRestored = event.persisted })
  })
  let confirmedLeave = false
  if (dirty) page.once('dialog', async (dialog) => { confirmedLeave = dialog.type() === 'beforeunload'; await dialog.accept() })
  await page.goto('/api/v1/health/live')
  if (dirty) expect(confirmedLeave).toBe(true)
}

export async function returnFromBFCache(page: Page, diagnostics: { privateResponses: () => number; notUsed: unknown[] }) {
  await page.goBack({ waitUntil: 'commit' })
  const probe = await page.evaluate(() => ({
    restored: (window as Window & { __homeboxRestored?: boolean }).__homeboxRestored === true,
    reasons: (performance.getEntriesByType('navigation')[0] as PerformanceNavigationTiming & { notRestoredReasons?: unknown })?.notRestoredReasons,
  }))
  expect(probe.restored, JSON.stringify({ reasons: probe.reasons, notUsed: diagnostics.notUsed })).toBe(true)
  expect(diagnostics.privateResponses()).toBeGreaterThan(0)
}
