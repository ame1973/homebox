import { cpSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { build } from 'vite'
import { test as applicationTest, expect } from './auth-fixture.ts'
import { provision, watchDomainWrites } from './domain-helpers.ts'

const test = applicationTest.extend<{}, { applicationWebDirectory: string }>({
  applicationWebDirectory: [async ({ browserName }, provideFixture) => {
    const directory = mkdtempSync(join(tmpdir(), `homebox-owned-sdk-${browserName}-`))
    const web = join(directory, 'web')
    const sdk = join(directory, 'sdk')
    const entry = join(directory, 'entry.ts')
    try {
      cpSync(fileURLToPath(new URL('../../dist', import.meta.url)), web, { recursive: true })
      // This private entry composes real modules, never a production test route.
      writeFileSync(entry, `export * from ${JSON.stringify(fileURLToPath(new URL('../../src/lib/api/catalog-links.ts', import.meta.url)))};\nexport {createItem} from ${JSON.stringify(fileURLToPath(new URL('../../src/lib/api/catalog.ts', import.meta.url)))};\nexport {createTag} from ${JSON.stringify(fileURLToPath(new URL('../../src/lib/api/taxonomy.ts', import.meta.url)))};\n`)
      await build({ configFile: false, logLevel: 'silent', build: {
        outDir: sdk, minify: false, lib: { entry, formats: ['es'], fileName: 'owned-sdk' },
      } })
      cpSync(join(sdk, 'owned-sdk.js'), join(web, 'assets', 'owned-sdk.js'))
      await provideFixture(web)
    } finally { rmSync(directory, { recursive: true, force: true }) }
  }, { scope: 'worker' }],
})

test.use({ trace: 'off', screenshot: 'off', video: 'off' })

test('drives all fourteen real owned adapters with independent proofs and signed scopes', async ({ page, application }) => {
  await provision(page, application)
  const writes = watchDomainWrites(page, ['/api/v1/items', '/api/v1/barcodes', '/api/v1/aliases', '/api/v1/attributes'])
  await page.goto('/')
  const result = await page.evaluate(async () => {
    const modulePath = '/assets/owned-sdk.js'
    const api = await import(modulePath)
    const csrf = await fetch('/api/v1/auth/csrf', { credentials: 'same-origin', cache: 'no-store' })
    if (!csrf.ok) throw new Error('Owned SDK session unavailable')
    const csrfToken: string = (await csrf.json()).token
    const context = () => ({ csrfToken, idempotencyKey: crypto.randomUUID(), signal: new AbortController().signal })
    const read = () => new AbortController().signal
    const failureCode = async (work: () => Promise<unknown>) => {
      try { await work(); return null } catch (error) { return error instanceof Error && 'code' in error ? error.code : 'unexpected' }
    }
    const item = await api.createItem({ name: 'Owned SDK fixture' }, context())
    let ownerRevision = item.revision
    const rows = []
    for (const spec of [
      { kind: 'barcode', create: api.createBarcode, patch: api.patchBarcode, remove: api.deleteBarcode, page: api.fetchItemBarcodesPage,
        first: { symbology: 'UPC_A', raw_value: '036000291452' }, second: { symbology: 'CODE_128', raw_value: '00123' }, changed: { symbology: 'UPC_E', raw_value: '04252614' } },
      { kind: 'alias', create: api.createAlias, patch: api.patchAlias, remove: api.deleteAlias, page: api.fetchItemAliasesPage,
        first: { alias: '  Exact é 😀  ' }, second: { alias: 'SecondAlias' }, changed: { alias: 'ReviewedAlias' } },
      { kind: 'attribute', create: api.createAttribute, patch: api.patchAttribute, remove: api.deleteAttribute, page: api.fetchItemAttributesPage,
        first: { attribute_key: 'RepeatedKey', attribute_value: 'Original' }, second: { attribute_key: 'RepeatedKey', attribute_value: 'Second' }, changed: { attribute_value: '' } },
    ]) {
      const original = { ...context(), expectedRevision: ownerRevision }
      const created = await spec.create(item.id, spec.first, original)
      ownerRevision++
      const second = await spec.create(item.id, spec.second, { ...context(), expectedRevision: ownerRevision })
      ownerRevision++
      const firstPage = await spec.page(item.id, { limit: 1 }, read())
      if (!firstPage.next_cursor) throw new Error('Expected owned signed continuation')
      const lastPage = await spec.page(item.id, { limit: 1, cursor: firstPage.next_cursor }, read())
      const scope = await failureCode(() => spec.page('other-owner', { limit: 1, cursor: firstPage.next_cursor }, read()))
      const patchContext = { ...context(), itemID: item.id, expectedRevision: created.revision }
      const changed = await spec.patch(created.id, spec.changed, patchContext)
      ownerRevision++
      const stale = await failureCode(() => spec.patch(created.id, spec.first, { ...context(), itemID: item.id, expectedRevision: created.revision }))
      const deletion = { ...context(), itemID: item.id, expectedRevision: changed.revision }
      const deleted = await spec.remove(created.id, deletion)
      ownerRevision++
      const frozenDeletion = await spec.remove(created.id, deletion)
      const frozenCreation = await spec.create(item.id, spec.first, original)
      rows.push({ kind: spec.kind, created, second, firstPage, lastPage, scope, changed, stale, deleted, frozenDeletion, frozenCreation })
    }
    const tag = await api.createTag({ name: 'Owned SDK assigned tag' }, context())
    const membership = { ...context(), expectedRevision: ownerRevision }
    const assigned = await api.replaceItemTags(item.id, { tag_ids: [tag.id] }, membership)
    const assignedPage = await api.fetchItemTagsPage(item.id, {}, read())
    const cleared = await api.replaceItemTags(item.id, { tag_ids: [] }, { ...context(), expectedRevision: assigned.revision })
    const frozenSet = await api.replaceItemTags(item.id, { tag_ids: [tag.id] }, membership)
    const afterReplay = await api.fetchItemTagsPage(item.id, {}, read())
    return { item, rows, assigned, assignedPage, cleared, frozenSet, afterReplay }
  })
  expect(result.rows).toHaveLength(3)
  for (const row of result.rows) {
    expect(row.created).toMatchObject({ item_id: result.item.id, revision: 1 })
    expect(row.changed).toMatchObject({ id: row.created.id, item_id: result.item.id, revision: 2 })
    expect(row.lastPage.next_cursor).toBeNull()
    expect(new Set([...row.firstPage.data, ...row.lastPage.data].map((value: { id: string }) => value.id))).toEqual(new Set([row.created.id, row.second.id]))
    expect(row.scope).toBe('INVALID_CURSOR')
    expect(row.stale).toBe('REVISION_CONFLICT')
    expect(row.deleted).toMatchObject({ ok: true, revision: 3 })
    expect(row.frozenDeletion).toEqual(row.deleted)
    expect(row.frozenCreation).toEqual(row.created)
  }
  expect(result.rows[0]?.created.raw_value).toBe('036000291452')
  expect(result.rows[0]?.changed.normalized_value).toBe('04252614')
  expect(result.rows[1]?.created.alias).toBe('  Exact é 😀  ')
  expect(result.rows[2]?.changed).toMatchObject({ attribute_key: 'RepeatedKey', attribute_value: '' })
  expect(result.assignedPage.data).toHaveLength(1)
  expect(result.assigned.id).toBe(result.item.id)
  expect(result.cleared.revision).toBe(result.assigned.revision + 1)
  expect(result.frozenSet).toEqual(result.assigned)
  expect(result.afterReplay).toEqual({ data: [], next_cursor: null })
  const deletions = writes.filter((request) => request.method === 'DELETE')
  expect(deletions).toHaveLength(6)
  for (let index = 0; index < deletions.length; index += 2) {
    expect(deletions[index]?.proof).toBe('"2"')
    expect(deletions[index + 1]).toEqual(deletions[index])
  }
})

test('rejects a genuinely committed wrong-owner reply and only reconciles the identical original request', async ({ page, application }) => {
  await provision(page, application)
  const writes = watchDomainWrites(page, ['/api/v1/items'])
  let attempts = 0
  await page.route('**/api/v1/items/*/aliases', async (route) => {
    if (route.request().method() !== 'POST') { await route.continue(); return }
    const response = await route.fetch()
    if (++attempts === 1) {
      const committed = await response.json()
      await route.fulfill({ response, body: JSON.stringify({ ...committed, item_id: 'wrong-owner' }) })
    } else await route.fulfill({ response })
  })
  await page.goto('/')
  const result = await page.evaluate(async () => {
    const modulePath = '/assets/owned-sdk.js'
    const api = await import(modulePath)
    const csrf = await fetch('/api/v1/auth/csrf', { credentials: 'same-origin', cache: 'no-store' })
    const csrfToken: string = (await csrf.json()).token
    const root = () => ({ csrfToken, idempotencyKey: crypto.randomUUID(), signal: new AbortController().signal })
    const item = await api.createItem({ name: 'Unknown owned outcome' }, root())
    const original = { ...root(), expectedRevision: item.revision }
    const body = { alias: 'Unknown original alias' }
    let code: unknown
    try { await api.createAlias(item.id, body, original) } catch (error) { code = error instanceof Error && 'code' in error ? error.code : 'unexpected' }
    window.dispatchEvent(new Event('online'))
    const before = await api.fetchItemAliasesPage(item.id, {}, new AbortController().signal)
    const reconciled = await api.createAlias(item.id, body, original)
    const after = await api.fetchItemAliasesPage(item.id, {}, new AbortController().signal)
    return { code, before, reconciled, after }
  })
  expect(result.code).toBe('INVALID_RESPONSE')
  expect(result.before.data).toHaveLength(1)
  expect(result.reconciled).toEqual(result.before.data[0])
  expect(result.after).toEqual(result.before)
  expect(attempts).toBe(2)
  const creations = writes.filter((request) => request.path.endsWith('/aliases'))
  expect(creations).toHaveLength(2)
  expect(creations[1]).toEqual(creations[0])
})
