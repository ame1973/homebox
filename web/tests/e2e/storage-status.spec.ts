import AxeBuilder from '@axe-core/playwright'
import { messages } from '../../src/lib/messages.ts'
import { test, expect } from './auth-fixture.ts'
import { noOverflow, provision, watchDomainWrites } from './domain-helpers.ts'

const text = messages.deviceStorage

declare global {
  interface Window {
    __storageProbe: { calls: number; active: boolean; result: boolean | 'pending' | 'error' | null }
  }
}

test.use({ trace: 'off', screenshot: 'off', video: 'off' })

test('reads actual browser storage offline and calls native persistence only on a user action', async ({ page, context, application }) => {
  await context.addInitScript(() => {
    const manager = navigator.storage, nativePersist = manager.persist.bind(manager)
    window.__storageProbe = { calls: 0, active: false, result: null }
    Object.defineProperty(manager, 'persist', { configurable: true, value: () => {
      window.__storageProbe.calls++
      window.__storageProbe.active = navigator.userActivation.isActive
      window.__storageProbe.result = 'pending'
      return nativePersist().then((result) => { window.__storageProbe.result = result; return result }, (error) => { window.__storageProbe.result = 'error'; throw error })
    } })
  })
  await provision(page, application)
  const writes = watchDomainWrites(page, ['/api/v1/'])
  await page.goto('/settings')
  const region = page.getByRole('region', { name: text.title })
  await expect(region.getByText(text.loading)).toHaveCount(0)
  expect(await page.evaluate(() => typeof navigator.storage.estimate)).toBe('function')
  const estimate = await page.evaluate(() => navigator.storage.estimate())
  for (const [label, value] of [[text.usage, estimate.usage], [text.quota, estimate.quota]] as const) {
    if (typeof value === 'number' && Number.isSafeInteger(value) && value >= 0) {
      await expect(region.getByText(label, { exact: true }).locator('..').locator('dd')).not.toHaveText(text.unknown)
    }
  }
  expect(await page.evaluate(() => window.__storageProbe.calls)).toBe(0)
  await expect(page.getByLabel(messages.shell.label).getByText(messages.shell.phases.ready)).toBeVisible()
  await context.setOffline(true)
  const offline = await context.newPage()
  const offlineWrites = watchDomainWrites(offline, ['/api/v1/'])
  await offline.goto(`${application.url}/settings`, { waitUntil: 'domcontentloaded' })
  await expect(offline.getByText(messages.taxonomy.offline, { exact: true })).toBeVisible()
  const offlineRegion = offline.getByRole('region', { name: text.title })
  await expect(offlineRegion).toBeVisible()
  await expect(offlineRegion.getByText(text.loading)).toHaveCount(0)
  const request = offlineRegion.getByRole('button', { name: text.request })
  if (await request.isEnabled()) {
    await request.click()
    await expect.poll(() => offline.evaluate(() => window.__storageProbe.result)).not.toBe('pending')
    const observation = await offline.evaluate(() => window.__storageProbe)
    expect(observation.calls).toBe(1)
    expect(observation.active).toBe(true)
    if (observation.result === true) await expect(offlineRegion.getByText(text.persistent)).toBeVisible()
    else if (observation.result === false) await expect(offlineRegion.getByText(text.requestDenied)).toBeVisible()
    else await expect(offlineRegion.getByText(text.requestFailed)).toBeVisible()
  } else {
    await expect(offlineRegion.getByText(text.persistent)).toBeVisible()
    expect(await offline.evaluate(() => window.__storageProbe.calls)).toBe(0)
  }
  expect(writes).toEqual([])
  expect(offlineWrites).toEqual([])
})

test('presents denial, exact gesture control and accessible estimates in both themes', async ({ page, application }, testInfo) => {
  await page.addInitScript(() => {
    window.__storageProbe = { calls: 0, active: false, result: null }
    Object.defineProperty(navigator, 'storage', { configurable: true, value: {
      estimate: async () => ({ usage: 1048576, quota: 2147483648 }), persisted: async () => false,
      persist: async () => { window.__storageProbe.calls++; window.__storageProbe.active = navigator.userActivation.isActive; return false },
    } })
  })
  await provision(page, application)
  const writes = watchDomainWrites(page, ['/api/v1/'])
  await page.setViewportSize({ width: 320, height: 568 })
  await page.goto('/settings')
  const region = page.getByRole('region', { name: text.title })
  await expect(region.getByText('1 MiB')).toBeVisible()
  await expect(region.getByText('2 GiB')).toBeVisible()
  expect(await page.evaluate(() => window.__storageProbe.calls)).toBe(0)
  await page.locator('html').evaluate((element) => { element.style.fontSize = '200%' })
  for (const colorScheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme }); await expect(page.locator('html')).toHaveAttribute('data-theme', colorScheme)
    await noOverflow(page)
    const headingLines = await region.getByRole('heading', { name: text.title }).evaluate((element) => {
      const style = getComputedStyle(element)
      return element.getBoundingClientRect().height / Number.parseFloat(style.lineHeight)
    })
    expect(headingLines).toBeLessThanOrEqual(2.1)
    for (const button of await region.getByRole('button').all()) {
      const dimensions = await button.evaluate((element) => {
        const style = getComputedStyle(element)
        return { fits: element.scrollWidth <= element.clientWidth,
          lines: (element.clientHeight - Number.parseFloat(style.paddingTop) - Number.parseFloat(style.paddingBottom)) / Number.parseFloat(style.lineHeight) }
      })
      expect(dimensions.fits).toBe(true)
      expect(dimensions.lines).toBeLessThanOrEqual(await button.textContent() === text.refresh ? 1.1 : 2.1)
      expect((await button.boundingBox())?.height).toBeGreaterThanOrEqual(44)
    }
    expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze()).violations).toEqual([])
    // The captured region contains only synthetic estimates, not credentials.
    if (colorScheme === 'dark') await region.screenshot({ path: testInfo.outputPath('device-storage.png') })
  }
  const request = region.getByRole('button', { name: text.request })
  await request.focus()
  await expect(request).toBeFocused()
  await page.keyboard.press('Enter')
  await expect(region.getByText(text.requestDenied)).toBeVisible()
  await expect(request).toBeFocused()
  expect(await page.evaluate(() => window.__storageProbe)).toMatchObject({ calls: 1, active: true })
  await region.getByRole('button', { name: text.refresh }).click()
  await expect(region.getByText(text.notPersistent)).toBeVisible()
  expect(await page.evaluate(() => window.__storageProbe.calls)).toBe(1)
  expect(writes).toEqual([])
})

test('shows unsupported storage without requesting permission', async ({ page, application }) => {
  await page.addInitScript(() => { Object.defineProperty(navigator, 'storage', { configurable: true, value: undefined }) })
  await provision(page, application)
  await page.goto('/settings')
  const region = page.getByRole('region', { name: text.title })
  await expect(region.getByText(text.unknown)).toHaveCount(3)
  await expect(region.getByRole('button', { name: text.request })).toBeDisabled()
  await expect(region.getByText(text.notBackup)).toBeVisible()
})
