import { cpSync, mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { build } from 'vite'
import { test as applicationTest, expect } from './auth-fixture.ts'
import { provision, watchDomainWrites } from './domain-helpers.ts'

// Bundle the actual SDK without exposing a test entrypoint in the product build.
// The real Go server serves this private copy on its own configured origin.
const test = applicationTest.extend<{}, { applicationWebDirectory: string }>({
  applicationWebDirectory: [async ({ browserName }, provideFixture) => {
    const directory = mkdtempSync(join(tmpdir(), `homebox-catalog-sdk-${browserName}-`))
    const web = join(directory, 'web')
    const sdk = join(directory, 'sdk')
    try {
      cpSync(fileURLToPath(new URL('../../dist', import.meta.url)), web, { recursive: true })
      await build({ configFile: false, logLevel: 'silent', build: {
        outDir: sdk, minify: false, lib: { entry: fileURLToPath(new URL('../../src/lib/api/catalog.ts', import.meta.url)), formats: ['es'], fileName: 'catalog-sdk' },
      } })
      cpSync(join(sdk, 'catalog-sdk.js'), join(web, 'assets', 'catalog-sdk.js'))
      await provideFixture(web)
    } finally { rmSync(directory, { recursive: true, force: true }) }
  }, { scope: 'worker' }],
})

// Provisioning, cookies and caller-held CSRF must never enter test artifacts.
test.use({ trace: 'off', screenshot: 'off', video: 'off' })

test('drives all six actual catalog adapters with original proofs and live signed pages', async ({ page, application }) => {
  await provision(page, application)
  const pageErrors: string[] = []
  page.on('pageerror', (error) => pageErrors.push(error.name))
  const writes = watchDomainWrites(page, ['/api/v1/items'])
  await page.goto('/')
  const result = await page.evaluate(async () => {
    const modulePath = '/assets/catalog-sdk.js'
    const api = await import(modulePath)
    const csrf = await fetch('/api/v1/auth/csrf', { credentials: 'same-origin', cache: 'no-store' })
    if (!csrf.ok) throw new Error('Catalog SDK session unavailable')
    const csrfToken: string = (await csrf.json()).token
    const context = () => ({ csrfToken, idempotencyKey: crypto.randomUUID(), signal: new AbortController().signal })
    const read = () => new AbortController().signal
    const failureCode = async (work: () => Promise<unknown>) => {
      try { await work(); return null } catch (error) { return error instanceof Error && 'code' in error ? error.code : 'unexpected' }
    }
    const original = { name: 'Catalog SDK 濾芯 😀 é' }
    const creation = context()
    const created = await api.createItem(original, creation)
    const measured = await api.createItem({ name: 'Catalog SDK 咖啡', tracking_mode: 'quantity', unit_code: 'kg', base_unit: 'g', scale_factor: 1000 }, context())
    const current = await api.fetchItem(created.id, read())
    const query = { limit: 1, q: 'Catalog SDK' }
    const first = await api.fetchItemsPage(query, read())
    if (!first.next_cursor) throw new Error('Expected a real signed catalog continuation')
    const second = await api.fetchItemsPage({ ...query, cursor: first.next_cursor }, read())
    const changedScope = await failureCode(() => api.fetchItemsPage({ ...query, q: 'different', cursor: first.next_cursor! }, read()))
    const filtered = await api.fetchItemsPage({ from: '0001-01-01', to: '9999-12-31', currency: 'TWD', category_id: 'absent-category', location_id: 'absent-location', scope: 'direct', status: 'active', merchant: ' Exact Merchant ', q: 'Catalog SDK', limit: 1 }, read())
    const patch = { name: 'Reviewed catalog rename', brand: null, model: '', notes: '  Exact 😀  ' }
    const patchContext = { ...context(), expectedRevision: current.revision }
    const patched = await api.patchItem(created.id, patch, patchContext)
    const stale = await failureCode(() => api.patchItem(created.id, { name: 'Must not rebase' }, { ...context(), expectedRevision: current.revision }))
    const frozenCreation = await api.createItem(original, creation)
    const archived = await api.archiveItem(created.id, { reason: 'Preserve catalog history' }, { ...context(), expectedRevision: patched.revision })
    const restored = await api.patchItem(created.id, { status: 'active' }, { ...context(), expectedRevision: archived.revision })
    const frozenPatch = await api.patchItem(created.id, patch, patchContext)
    const afterHistoricalReply = await api.fetchItem(created.id, read())
    const deletion = { ...context(), expectedRevision: restored.revision }
    const deleted = await api.deleteItem(created.id, deletion)
    const frozenDeletion = await api.deleteItem(created.id, deletion)
    const missing = await failureCode(() => api.fetchItem(created.id, read()))
    await api.deleteItem(measured.id, { ...context(), expectedRevision: measured.revision })
    return { created, measured, current, first, second, changedScope, filtered, patched, stale, frozenCreation, archived, restored, frozenPatch, afterHistoricalReply, deleted, frozenDeletion, missing }
  })
  expect(result.created).toMatchObject({ revision: 1, tracking_mode: 'individual', status: 'active', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1, brand: null, model: null, category_id: null })
  expect(result.measured).toMatchObject({ revision: 1, tracking_mode: 'quantity', unit_code: 'kg', base_unit: 'g', scale_factor: 1000 })
  expect(result.current).toEqual(result.created)
  expect(new Set([...result.first.data, ...result.second.data].map((item: { id: string }) => item.id))).toEqual(new Set([result.created.id, result.measured.id]))
  expect(result.second.next_cursor).toBeNull()
  expect(result.changedScope).toBe('INVALID_CURSOR')
  expect(result.filtered).toEqual({ data: [], next_cursor: null })
  expect(result.patched).toMatchObject({ revision: 2, name: 'Reviewed catalog rename', brand: null, model: '', notes: '  Exact 😀  ' })
  expect(result.stale).toBe('REVISION_CONFLICT')
  expect(result.frozenCreation).toEqual(result.created)
  expect(result.archived).toMatchObject({ revision: 3, status: 'archived' })
  expect(result.restored).toMatchObject({ revision: 4, status: 'active' })
  expect(result.frozenPatch).toEqual(result.patched)
  expect(result.afterHistoricalReply).toEqual(result.restored)
  expect(result.deleted).toMatchObject({ ok: true, revision: 5 })
  expect(result.frozenDeletion).toEqual(result.deleted)
  expect(result.missing).toBe('NOT_FOUND')
  const creations = writes.filter((request) => request.method === 'POST' && request.path === '/api/v1/items')
  expect(creations).toHaveLength(3)
  expect(creations[0]?.proof).toBeUndefined()
  expect(creations[2]).toEqual(creations[0])
  const deletions = writes.filter((request) => request.method === 'DELETE' && request.path.endsWith(`/${result.created.id}`))
  expect(deletions).toHaveLength(2)
  expect(deletions[0]?.proof).toBe('"4"')
  expect(deletions[1]).toEqual(deletions[0])
  expect(pageErrors).toEqual([])
})

test('rejects a genuinely committed malformed reply and reconciles only an explicit original-key retry', async ({ page, application }) => {
  await provision(page, application)
  const writes = watchDomainWrites(page, ['/api/v1/items'])
  let attempts = 0
  await page.route('**/api/v1/items', async (route) => {
    if (route.request().method() !== 'POST') { await route.continue(); return }
    const response = await route.fetch()
    if (++attempts === 1) {
      const committed = await response.json()
      await route.fulfill({ response, body: JSON.stringify({ ...committed, base_unit: 'g\n' }) })
    } else await route.fulfill({ response })
  })
  await page.goto('/')
  const result = await page.evaluate(async () => {
    const modulePath = '/assets/catalog-sdk.js'
    const api = await import(modulePath)
    const csrf = await fetch('/api/v1/auth/csrf', { credentials: 'same-origin', cache: 'no-store' })
    if (!csrf.ok) throw new Error('Catalog SDK session unavailable')
    const context = { csrfToken: (await csrf.json()).token as string, idempotencyKey: crypto.randomUUID(), signal: new AbortController().signal }
    const body = { name: 'Unknown committed catalog outcome' }
    let code: unknown
    try { await api.createItem(body, context) } catch (error) { code = error instanceof Error && 'code' in error ? error.code : 'unexpected' }
    window.dispatchEvent(new Event('online'))
    const beforeRetry = await api.fetchItemsPage({ q: body.name }, new AbortController().signal)
    const reconciled = await api.createItem(body, context)
    const afterRetry = await api.fetchItemsPage({ q: body.name }, new AbortController().signal)
    return { code, beforeRetry, reconciled, afterRetry }
  })
  expect(result.code).toBe('INVALID_RESPONSE')
  expect(result.beforeRetry.data).toHaveLength(1)
  expect(result.reconciled).toEqual(result.beforeRetry.data[0])
  expect(result.afterRetry).toEqual(result.beforeRetry)
  expect(result.reconciled.revision).toBe(1)
  expect(attempts).toBe(2)
  expect(writes).toHaveLength(2)
  expect(writes[1]).toEqual(writes[0])
})
