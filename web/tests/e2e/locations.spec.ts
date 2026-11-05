import AxeBuilder from '@axe-core/playwright'
import type { Page } from '@playwright/test'
import type { components } from '../../src/lib/api/schema.d.ts'
import { messages } from '../../src/lib/messages.ts'
import { test, expect } from './auth-fixture.ts'

// Provisioning credentials and cookies must never enter browser artifacts.
test.use({ trace: 'off', screenshot: 'off', video: 'off' })
const text = messages.locations
type Location = components['schemas']['Location']
const input = (page: Page, label: string) => page.getByLabel(new RegExp(`^${label}`))

async function provision(page: Page, application: { url: string; bootstrapToken: string }) {
  const initial = 'location fixture initial password'
  const replacement = 'location fixture replacement password'
  const headers = { Origin: application.url }
  expect((await page.request.post('/api/v1/auth/setup', { headers, data: { bootstrap_token: application.bootstrapToken, username: 'owner', password: initial } })).status()).toBe(200)
  expect((await page.request.post('/api/v1/auth/login', { headers, data: { username: 'owner', password: initial } })).status()).toBe(200)
  const csrf = await page.request.get('/api/v1/auth/csrf')
  expect(csrf.status()).toBe(200)
  expect((await page.request.post('/api/v1/auth/password', { headers: { ...headers, 'X-CSRF-Token': (await csrf.json()).token, 'Idempotency-Key': crypto.randomUUID(), 'X-HomeBox-Client-Version': '0.1.0-dev' }, data: { old_password: initial, new_password: replacement } })).status()).toBe(200)
  expect((await page.request.post('/api/v1/auth/login', { headers, data: { username: 'owner', password: replacement } })).status()).toBe(200)
}

async function writeContext(page: Page, origin: string, revision?: number) {
  const csrf = await page.request.get('/api/v1/auth/csrf')
  expect(csrf.status()).toBe(200)
  return { Origin: origin, 'X-CSRF-Token': (await csrf.json()).token as string, 'Idempotency-Key': crypto.randomUUID(), 'X-HomeBox-Client-Version': '0.1.0-dev', ...(revision === undefined ? {} : { 'If-Match': JSON.stringify(String(revision)) }) }
}

async function seed(page: Page, origin: string, name: string, code: string, parent?: Location): Promise<Location> {
  const response = await page.request.post('/api/v1/locations', { headers: await writeContext(page, origin, parent?.revision), data: { name, code, kind: 'box', parent_id: parent?.id ?? null } })
  expect(response.status()).toBe(200)
  return response.json()
}

async function confirm(page: Page, label: string) {
  const dialog = page.getByRole('alertdialog')
  await expect(dialog.getByRole('heading', { name: text.reviewTitle, exact: true })).toBeVisible()
  await expect(dialog.getByRole('heading', { name: text.reviewTitle, exact: true })).toBeFocused()
  await expect(dialog.getByRole('heading', { name: text.reviewTitle, exact: true })).toBeInViewport()
  await dialog.getByRole('button', { name: label, exact: true }).click()
  await expect(page.getByText(text.done, { exact: true })).toBeVisible()
  await expect(dialog).toHaveCount(0)
}

function mutationRequests(page: Page) {
  const requests: { method: string; path: string; key: string | undefined; proof: string | undefined; body: string | null }[] = []
  page.on('request', (request) => {
    const path = new URL(request.url()).pathname
    if (path.startsWith('/api/v1/locations') && ['POST', 'PATCH', 'DELETE'].includes(request.method())) requests.push({ method: request.method(), path, key: request.headers()['idempotency-key'], proof: request.headers()['if-match'], body: request.postData() })
  })
  return requests
}

async function noOverflow(page: Page) {
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
}

// Separate private stores keep each workflow within the unchanged production
// rate budget. These are actual HTTP mutations, never direct SQL/test endpoints.
test('creates, edits and moves actual locations only after review', async ({ page, application }, info) => {
  await provision(page, application)
  const writes = mutationRequests(page)
  await page.goto('/locations')
  await expect(page.getByRole('heading', { name: text.empty, exact: true })).toBeVisible()
  await page.getByRole('link', { name: text.create, exact: true }).click()
  await page.getByRole('button', { name: text.review, exact: true }).click()
  await expect(input(page, text.name)).toBeFocused()
  await input(page, text.name).fill('書房')
  await page.getByRole('link', { name: text.nav, exact: true }).click()
  await expect(page.getByRole('alertdialog').getByRole('button', { name: text.stay, exact: true })).toBeFocused()
  await page.keyboard.press('Escape')
  await expect(page.getByRole('alertdialog')).toHaveCount(0)
  await expect(input(page, text.name)).toHaveValue('書房')
  await input(page, text.code).fill('ROOM-01')
  await input(page, text.kind).selectOption('room')
  await input(page, text.sortOrder).fill('-2')
  await input(page, text.description).fill('放書與文件的房間。')
  await page.getByRole('button', { name: text.review, exact: true }).click()
  expect(writes).toHaveLength(0)
  expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze()).violations).toEqual([])
  await noOverflow(page)
  await confirm(page, text.confirmCreate)
  await expect(page.getByRole('heading', { level: 1, name: '書房', exact: true })).toBeVisible()
  const rootID = new URL(page.url()).pathname.split('/').at(-1)
  expect(writes).toHaveLength(1)
  expect(writes[0]?.proof).toBeUndefined()
  await page.getByRole('link', { name: text.createChild, exact: true }).click()
  await expect(input(page, text.parent)).toHaveValue(rootID ?? '')
  await input(page, text.name).fill('文件收納箱')
  await input(page, text.code).fill('BOX-01')
  await input(page, text.kind).selectOption('custom')
  await input(page, text.customKind).fill('防潮收納箱')
  await page.getByRole('button', { name: text.review, exact: true }).click()
  expect(writes).toHaveLength(1)
  await confirm(page, text.confirmCreate)
  await expect(page.getByRole('heading', { level: 1, name: '文件收納箱', exact: true })).toBeVisible()
  expect(writes[1]?.proof).toBe('"1"')
  await page.getByRole('link', { name: text.edit, exact: true }).click()
  await input(page, text.name).fill('已整理的文件箱')
  await input(page, text.description).fill('保留原有代碼與自訂類型。')
  await page.getByRole('button', { name: text.review, exact: true }).click()
  expect(writes).toHaveLength(2)
  await confirm(page, text.confirmSave)
  expect(JSON.parse(writes[2]?.body ?? '{}')).toEqual({ name: '已整理的文件箱', description: '保留原有代碼與自訂類型。' })
  await page.getByRole('button', { name: text.move, exact: true }).click()
  await input(page, text.destination).selectOption('')
  await page.getByRole('button', { name: text.review, exact: true }).click()
  expect(writes).toHaveLength(3)
  await confirm(page, text.confirmMove)
  expect(JSON.parse(writes[3]?.body ?? '{}')).toEqual({ target_parent_id: null })
  await expect(page.getByRole('navigation', { name: text.parentLabel, exact: true }).getByRole('link')).toHaveCount(1)
  for (const colorScheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme })
    await expect(page.locator('html')).toHaveAttribute('data-theme', colorScheme)
    expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze()).violations).toEqual([])
    await noOverflow(page)
    await page.screenshot({ path: info.outputPath(`location-detail-${colorScheme}.png`), fullPage: true })
  }
  await page.evaluate(() => { document.documentElement.style.fontSize = '200%' })
  await noOverflow(page)
  await page.getByRole('link', { name: text.back, exact: true }).click()
  await expect(page.getByRole('heading', { name: text.tree, exact: true })).toBeVisible()
  await input(page, text.search).fill('ROOM-01')
  await expect(page.getByRole('list', { name: text.tree, exact: true }).getByRole('listitem')).toHaveCount(1)
  await noOverflow(page)
})

test('requires an explicit subtree archive, restores one node and retains deleted codes', async ({ page, application }) => {
  await provision(page, application)
  const root = await seed(page, application.url, '儲藏室', 'STORE-01')
  const child = await seed(page, application.url, '備品箱', 'SPARES-01', root)
  const writes = mutationRequests(page)
  await page.goto(`/locations/${root.id}`)
  await page.getByRole('button', { name: text.archive, exact: true }).click()
  const scope = input(page, text.archiveChildren)
  await expect(scope).not.toBeChecked()
  await page.getByRole('button', { name: text.review, exact: true }).click()
  await expect(page.getByText(text.children, { exact: true })).toBeVisible()
  expect(writes).toHaveLength(0)
  await scope.check()
  await input(page, text.reason).fill('暫時不使用這個分支。')
  await page.getByRole('button', { name: text.review, exact: true }).click()
  await confirm(page, text.confirmArchive)
  await expect(page.getByRole('button', { name: text.createChild, exact: true })).toBeDisabled()
  await page.getByRole('button', { name: text.restore, exact: true }).click()
  await confirm(page, text.confirmRestore)
  await expect(page.getByText(`${child.code} (${text.archived})`, { exact: true })).toBeVisible()
  await page.getByRole('link', { name: new RegExp(child.name) }).click()
  await page.getByRole('button', { name: text.restore, exact: true }).click()
  await confirm(page, text.confirmRestore)
  await page.getByRole('button', { name: text.delete, exact: true }).click()
  await expect(page.getByRole('alertdialog').getByText(child.code, { exact: true })).toBeVisible()
  expect(writes).toHaveLength(3)
  await confirm(page, text.confirmDelete)
  await expect(page).toHaveURL(/\/locations$/)
  await page.getByRole('link', { name: text.create, exact: true }).click()
  await input(page, text.name).fill('另一個箱子')
  await input(page, text.code).fill(child.code)
  await page.getByRole('button', { name: text.review, exact: true }).click()
  const dialog = page.getByRole('alertdialog')
  await dialog.getByRole('button', { name: text.confirmCreate, exact: true }).click()
  await expect(dialog.getByText(text.codeConflict, { exact: true })).toBeVisible()
  await expect(dialog.getByRole('button', { name: text.confirmCreate, exact: true })).toBeDisabled()
  expect(writes).toHaveLength(5)
})

test('reconciles a conflict explicitly and retries a committed malformed reply with the original key and version', async ({ page, context, application }) => {
  await provision(page, application)
  const location = await seed(page, application.url, '原有名稱', 'RACE-01')
  await page.goto(`/locations/${location.id}/edit`)
  await input(page, text.description).fill('我明確修改的說明')
  const other = await page.request.patch(`/api/v1/locations/${location.id}`, { headers: await writeContext(page, application.url, 1), data: { name: '另一個裝置的名稱' } })
  expect(other.status()).toBe(200)
  const writes = mutationRequests(page)
  await page.getByRole('button', { name: text.review, exact: true }).click()
  let dialog = page.getByRole('alertdialog')
  await dialog.getByRole('button', { name: text.confirmSave, exact: true }).click()
  await expect(dialog.getByText(text.conflict, { exact: true })).toBeVisible()
  expect(writes[0]?.proof).toBe('"1"')
  await dialog.getByRole('button', { name: text.cancel, exact: true }).click()
  await page.getByRole('button', { name: text.keepEdits, exact: true }).click()
  await expect(input(page, text.name)).toHaveValue('另一個裝置的名稱')
  await expect(input(page, text.description)).toHaveValue('我明確修改的說明')
  let intercepted = 0
  await page.route(`**/api/v1/locations/${location.id}`, async (route) => {
    if (route.request().method() !== 'PATCH') { await route.continue(); return }
    intercepted++
    const response = await route.fetch()
    expect(response.status()).toBe(200)
    if (intercepted === 1) await route.fulfill({ response, body: '{"incomplete":true}' })
    else await route.fulfill({ response })
  })
  await page.getByRole('button', { name: text.review, exact: true }).click()
  dialog = page.getByRole('alertdialog')
  await dialog.getByRole('button', { name: text.confirmSave, exact: true }).click()
  await expect(dialog.getByRole('heading', { name: text.uncertain, exact: true })).toBeVisible()
  expect(writes).toHaveLength(2)
  expect(writes[1]?.proof).toBe('"2"')
  expect(writes[1]?.key).not.toBe(writes[0]?.key)
  expect(JSON.parse(writes[1]?.body ?? '{}')).toEqual({ description: '我明確修改的說明' })
  await context.setOffline(true)
  await expect(dialog.getByRole('button', { name: text.retryOriginal, exact: true })).toBeDisabled()
  await context.setOffline(false)
  await expect(dialog.getByRole('button', { name: text.retryOriginal, exact: true })).toBeEnabled()
  expect(intercepted).toBe(1)
  expect(writes).toHaveLength(2)
  await dialog.getByRole('button', { name: text.retryOriginal, exact: true }).click()
  await expect(page.getByText(text.done, { exact: true })).toBeVisible()
  await expect(page.getByRole('heading', { level: 1, name: '另一個裝置的名稱', exact: true })).toBeVisible()
  expect(intercepted).toBe(2)
  expect(writes[2]).toEqual(writes[1])
  const latest = await page.request.get(`/api/v1/locations/${location.id}`)
  expect(latest.status()).toBe(200)
  expect(await latest.json()).toMatchObject({ revision: 3, name: '另一個裝置的名稱', description: '我明確修改的說明' })
})

test('returns from a real login to the protected location alias without replaying input', async ({ page, application }) => {
  await provision(page, application)
  const location = await seed(page, application.url, '深連結的收納箱', 'LINK-01')
  expect((await page.request.post('/api/v1/auth/logout', { headers: await writeContext(page, application.url) })).status()).toBe(200)
  const writes = mutationRequests(page)
  await page.goto(`/l/${location.id}`)
  await expect(page.getByRole('heading', { name: messages.auth.loginHeading, exact: true })).toBeVisible()
  await input(page, messages.auth.username).fill('owner')
  await input(page, messages.auth.password).fill('location fixture replacement password')
  await page.getByRole('button', { name: messages.auth.login, exact: true }).click()
  await expect(page).toHaveURL(new RegExp(`/locations/${location.id}$`))
  await expect(page.getByRole('heading', { level: 1, name: location.name, exact: true })).toBeVisible()
  expect(writes).toHaveLength(0)
  await expect(page.getByRole('link', { name: text.nav, exact: true })).toHaveAttribute('aria-current', 'page')
})

test('keeps long real paths, native parent selection and review usable at 200 percent text', async ({ page, application }, info) => {
  await provision(page, application)
  const root = await seed(page, application.url, '書房文件與備用品整理'.repeat(20), 'LONG-'.repeat(20))
  let deepest = root
  for (let level = 1; level <= 7; level++) deepest = await seed(page, application.url, `第 ${level} 層的文件與家庭備用品收納箱`, `DEPTH-${level}`, deepest)
  await page.goto(`/locations/${deepest.id}`)
  await expect(page.getByRole('heading', { level: 1, name: deepest.name, exact: true })).toBeVisible()
  const path = page.getByRole('navigation', { name: text.parentLabel, exact: true })
  await expect(path.getByRole('link')).toHaveCount(1)
  await path.getByText(`${text.parentLabel}（7）`, { exact: true }).click()
  await expect(path.getByRole('link')).toHaveCount(8)
  await page.evaluate(() => { document.documentElement.style.fontSize = '200%' })
  await noOverflow(page)
  expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze()).violations).toEqual([])
  await page.getByRole('link', { name: text.createChild, exact: true }).click()
  await expect(input(page, text.parent)).toHaveValue(deepest.id)
  await input(page, text.name).fill('新整理的文件箱')
  await input(page, text.code).fill('ZOOMED-CHILD')
  await input(page, text.kind).selectOption('box')
  await noOverflow(page)
  await page.screenshot({ path: info.outputPath('location-editor-200-percent.png'), fullPage: true })
  await page.getByRole('button', { name: text.review, exact: true }).click()
  const dialog = page.getByRole('alertdialog')
  await expect(dialog.getByRole('heading', { name: text.reviewTitle, exact: true })).toBeFocused()
  await expect(dialog.getByRole('heading', { name: text.reviewTitle, exact: true })).toBeInViewport()
  await noOverflow(page)
  const sizing = await dialog.evaluate((element) => ({ width: element.clientWidth, scroll: element.scrollWidth, font: getComputedStyle(document.documentElement).fontSize, viewport: innerWidth, padding: getComputedStyle(element).padding, children: Array.from(element.children).map((child) => { const box = child.getBoundingClientRect(); return { tag: child.tagName, slot: (child as HTMLElement).dataset.slot, width: box.width, left: box.left, right: box.right, classes: child.className } }) }))
  expect(sizing.scroll, JSON.stringify(sizing)).toBeLessThanOrEqual(sizing.width)
  expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze()).violations).toEqual([])
  await page.screenshot({ path: info.outputPath('location-review-200-percent.png') })
  await dialog.getByRole('button', { name: text.cancel, exact: true }).click()
  await expect(dialog).toHaveCount(0)
  await expect(input(page, text.name)).toHaveValue('新整理的文件箱')
})
