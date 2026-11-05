import AxeBuilder from '@axe-core/playwright'
import { test, expect } from './auth-fixture.ts'
import { messages } from '../../src/lib/messages.ts'

// Credential bodies/cookies must not enter traces, videos, or automatic failure images.
test.use({ trace: 'off', screenshot: 'off', video: 'off' })
const text = messages.auth
const initialPassword = 'browser initial private password'
const newPassword = 'browser replacement private password'
const input = (page: import('@playwright/test').Page, label: string) => page.getByLabel(new RegExp(`^${label}`))

async function setup(page: import('@playwright/test').Page, token: string) {
  await page.goto('/setup')
  await input(page, text.bootstrap).fill(token)
  await input(page, text.username).fill('owner')
  await input(page, text.password).fill(initialPassword)
  await input(page, text.confirmation).fill(initialPassword)
  await page.getByRole('button', { name: text.setup, exact: true }).click()
  await expect(page.getByText(text.setupDone, { exact: true })).toBeVisible()
}

async function login(page: import('@playwright/test').Page, password = initialPassword) {
  await input(page, text.username).fill('owner')
  await input(page, text.password).fill(password)
  await page.getByRole('button', { name: text.login, exact: true }).click()
  await expect(page.getByRole('heading', { name: text.passwordHeading, exact: true })).toBeVisible()
}

async function changePassword(page: import('@playwright/test').Page) {
  await input(page, text.oldPassword).fill(initialPassword)
  await input(page, text.newPassword).fill(newPassword)
  await input(page, text.confirmation).fill(newPassword)
  await page.getByRole('button', { name: text.changePassword, exact: true }).click()
}

test('real setup, mandatory rotation, fresh login and explicit logout', async ({ page, context, application }, info) => {
  const posts: string[] = []
  page.on('request', (request) => { if (request.method() === 'POST') posts.push(new URL(request.url()).pathname) })
  await setup(page, application.bootstrapToken)
  expect((await context.cookies()).filter((cookie) => cookie.name === 'homebox_session')).toHaveLength(0)
  await login(page)
  await expect(page.getByText(text.mandatory, { exact: true })).toBeVisible()
  const cookie = (await context.cookies()).find((cookie) => cookie.name === 'homebox_session')
  expect(cookie?.httpOnly).toBe(true)
  expect(cookie?.sameSite).toBe('Strict')
  expect(await page.evaluate(() => document.cookie.includes('homebox_session'))).toBe(false)
  const forbidden = await page.request.get('/api/v1/items')
  expect(forbidden.status()).toBe(403)
  expect((await forbidden.json()).error.code).toBe('PASSWORD_CHANGE_REQUIRED')
  await changePassword(page)
  await expect(page.getByText(text.passwordDone, { exact: true })).toBeVisible()
  expect((await context.cookies()).filter((cookie) => cookie.name === 'homebox_session')).toHaveLength(0)
  await login(page, newPassword)
  await expect(page.getByText(text.mandatory, { exact: true })).toHaveCount(0)
  for (const colorScheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme })
    await expect(page.locator('html')).toHaveAttribute('data-theme', colorScheme)
    expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze()).violations).toEqual([])
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    for (const button of await page.getByRole('button').all()) {
      const box = await button.boundingBox()
      expect(box?.height).toBeGreaterThanOrEqual(44)
      expect(box?.width).toBeGreaterThanOrEqual(44)
    }
    // All credential fields are empty at this point; no token/password image is captured.
    await page.screenshot({ path: info.outputPath(`account-${colorScheme}.png`), fullPage: true })
  }
  await page.getByRole('button', { name: text.logout, exact: true }).click()
  const dialog = page.getByRole('alertdialog')
  await expect(dialog.getByRole('button', { name: text.cancel, exact: true })).toBeFocused()
  expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa']).analyze()).violations).toEqual([])
  await dialog.getByRole('button', { name: text.cancel, exact: true }).click()
  expect(posts.filter((path) => path.endsWith('/logout'))).toHaveLength(0)
  await page.getByRole('button', { name: text.logout, exact: true }).click()
  await dialog.getByRole('button', { name: text.logout, exact: true }).click()
  await expect(page.getByText(text.logoutDone, { exact: true })).toBeVisible()
  expect(posts).toEqual(['/api/v1/auth/setup', '/api/v1/auth/login', '/api/v1/auth/password', '/api/v1/auth/login', '/api/v1/auth/logout'])
  expect((await context.cookies()).filter((cookie) => cookie.name === 'homebox_session')).toHaveLength(0)
})

test('dirty navigation, keyboard validation and offline reconnect never replay credentials', async ({ page, context }) => {
  const posts: string[] = []
  page.on('request', (request) => { if (request.method() === 'POST') posts.push(request.url()) })
  await page.goto('/')
  await page.getByRole('link', { name: messages.app.account, exact: true }).click()
  await page.getByRole('button', { name: text.login, exact: true }).click()
  await expect(page.getByText(text.usernameBounds, { exact: true })).toBeVisible()
  await expect(input(page, text.username)).toBeFocused()
  await input(page, text.username).fill('owner')
  await page.evaluate(() => history.back())
  await expect(page.getByRole('alertdialog').getByRole('button', { name: text.stay, exact: true })).toBeFocused()
  await page.keyboard.press('Escape')
  await expect(page).toHaveURL(/\/login$/)
  await expect(input(page, text.username)).toHaveValue('owner')
  await page.getByRole('link', { name: text.firstTime, exact: true }).click()
  let dialog = page.getByRole('alertdialog')
  await expect(dialog.getByRole('button', { name: text.stay, exact: true })).toBeFocused()
  await page.keyboard.press('Escape')
  await expect(dialog).toHaveCount(0)
  await expect(page).toHaveURL(/\/login$/)
  await expect(input(page, text.username)).toHaveValue('owner')
  await context.setOffline(true)
  await expect(page.getByText(text.offline, { exact: true })).toBeVisible()
  await context.setOffline(false)
  await expect(input(page, text.username)).toHaveValue('')
  expect(posts).toEqual([])
  await input(page, text.password).fill('not submitted private text')
  await page.getByRole('link', { name: text.firstTime, exact: true }).click()
  dialog = page.getByRole('alertdialog')
  await dialog.getByRole('button', { name: text.leave, exact: true }).click()
  await expect(input(page, text.bootstrap)).toHaveValue('')
  await expect(input(page, text.password)).toHaveValue('')
  expect(posts).toEqual([])
})

test('a committed password change with a malformed reply is uncertain, not replayed', async ({ page, application }) => {
  await setup(page, application.bootstrapToken)
  await login(page)
  let writes = 0
  await page.route('**/api/v1/auth/password', async (route) => {
    writes++
    const response = await route.fetch()
    expect(response.status()).toBe(200)
    await route.fulfill({ response, body: '<html>incomplete proxy reply</html>', headers: { ...response.headers(), 'content-type': 'text/html' } })
  })
  await changePassword(page)
  await expect(page.getByText(text.uncertain, { exact: true })).toBeVisible()
  await expect(page.getByText(text.passwordDone, { exact: true })).toHaveCount(0)
  expect(writes).toBe(1)
  await page.unroute('**/api/v1/auth/password')
  await login(page, newPassword)
  expect(writes).toBe(1)
  await expect(page.getByText(text.mandatory, { exact: true })).toHaveCount(0)
})

test('recovers a failed lazy account chunk explicitly and supports 200 percent text', async ({ page }) => {
  await page.goto('/')
  await page.route('**/assets/account-page-*.js', (route) => route.abort('failed'))
  await page.getByRole('link', { name: messages.app.account, exact: true }).click()
  await expect(page.getByRole('heading', { name: messages.app.loadError })).toBeVisible()
  await page.unroute('**/assets/account-page-*.js')
  await page.getByRole('button', { name: messages.app.reload, exact: true }).click()
  await expect(page.getByRole('heading', { name: text.loginHeading, exact: true })).toBeVisible()
  await page.evaluate(() => { document.documentElement.style.fontSize = '200%' })
  await expect(input(page, text.username)).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  await page.getByRole('link', { name: text.firstTime, exact: true }).click()
  await expect(input(page, text.bootstrap)).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  await page.getByRole('button', { name: messages.theme.label, exact: true }).click()
  const menu = page.getByRole('menu')
  await expect(menu).toBeVisible()
  const box = await menu.boundingBox()
  expect(box?.x).toBeGreaterThanOrEqual(0)
  expect((box?.x ?? 0) + (box?.width ?? 0)).toBeLessThanOrEqual(page.viewportSize()?.width ?? 0)
  await page.keyboard.press('Escape')
  await expect(menu).toHaveCount(0)
  expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa']).analyze()).violations).toEqual([])
})
