import { cpSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { DatabaseSync } from 'node:sqlite'
import { fileURLToPath } from 'node:url'
import { build } from 'vite'
import { test as applicationTest, expect } from './auth-fixture.ts'
import { provision, watchDomainWrites, writeContext } from './domain-helpers.ts'

const test = applicationTest.extend<{}, { applicationWebDirectory: string }>({
  applicationWebDirectory: [async ({ browserName }, provideFixture) => {
    const directory = mkdtempSync(join(tmpdir(), `homebox-owned-sdk-${browserName}-`))
    const web = join(directory, 'web')
    const sdk = join(directory, 'sdk')
    const entry = join(directory, 'entry.ts')
    try {
      cpSync(fileURLToPath(new URL('../../dist', import.meta.url)), web, { recursive: true })
      // This private entry composes real modules, never a production test route.
      writeFileSync(entry, `export * from ${JSON.stringify(fileURLToPath(new URL('../../src/lib/api/catalog-links.ts', import.meta.url)))};\nexport * from ${JSON.stringify(fileURLToPath(new URL('../../src/lib/api/barcode-search.ts', import.meta.url)))};\nexport {createItem, archiveItem} from ${JSON.stringify(fileURLToPath(new URL('../../src/lib/api/catalog.ts', import.meta.url)))};\nexport {createTag} from ${JSON.stringify(fileURLToPath(new URL('../../src/lib/api/taxonomy.ts', import.meta.url)))};\n`)
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

test('searches actual shared product writes without treating a barcode as a unique Unit', async ({ page, application }) => {
  const password = await provision(page, application)
  const searchWrites = watchDomainWrites(page, ['/api/v1/search'])
  await page.goto('/')
  const result = await page.evaluate(async () => {
    const modulePath = '/assets/owned-sdk.js', api = await import(modulePath)
    const csrf = await fetch('/api/v1/auth/csrf', { credentials: 'same-origin', cache: 'no-store' })
    if (!csrf.ok) throw new Error('Barcode SDK session unavailable')
    const csrfToken: string = (await csrf.json()).token
    const root = () => ({ csrfToken, idempotencyKey: crypto.randomUUID(), signal: new AbortController().signal })
    const read = () => new AbortController().signal
    const failure = async (work: () => Promise<unknown>) => {
      try { await work(); return null } catch (error) { return error instanceof Error && 'code' in error ? error.code : 'unexpected' }
    }
    const owner = await api.createItem({ name: 'Shared first' }, root())
    const other = await api.createItem({ name: 'Shared second' }, root())
    const firstBarcode = await api.createBarcode(owner.id, { symbology: 'UPC_A', raw_value: '036000291452' }, { ...root(), expectedRevision: 1 })
    const otherBarcode = await api.createBarcode(other.id, { symbology: 'UPC_A', raw_value: '036000291452' }, { ...root(), expectedRevision: 1 })
    const thirdBarcode = await api.createBarcode(owner.id, { symbology: 'OTHER', raw_value: '036000291452' }, { ...root(), expectedRevision: 2 })
    await api.archiveItem(other.id, {}, { ...root(), expectedRevision: 2 })
    const all = await api.fetchBarcodeMatches({ value: '036000291452' }, read())
    const filtered = await api.fetchBarcodeMatches({ value: '036000291452', symbology: 'UPC_A' }, read())
    const different = await api.fetchBarcodeMatches({ value: '36000291452' }, read())
    const first = await api.fetchBarcodeMatches({ value: '036000291452', limit: 1 }, read())
    if (!first.next_cursor) throw new Error('Missing signed barcode continuation')
    const scope = await failure(() => api.fetchBarcodeMatches({ value: '036000291452', symbology: 'UPC_A', limit: 1, cursor: first.next_cursor }, read()))
    return { owner, other, firstBarcode, otherBarcode, thirdBarcode, all, filtered, different, first, scope }
  })
  expect(result.all.matches).toHaveLength(3); expect(result.all.next_cursor).toBeNull()
  expect(new Set(result.all.matches.map((row: { barcode_id: string }) => row.barcode_id)))
    .toEqual(new Set([result.firstBarcode.id, result.otherBarcode.id, result.thirdBarcode.id]))
  expect(result.filtered.matches).toHaveLength(2)
  expect(result.filtered.matches.map((row: { item_id: string }) => row.item_id)).toEqual(expect.arrayContaining([result.owner.id, result.other.id]))
  expect(result.different).toEqual({ matches: [], next_cursor: null }); expect(result.scope).toBe('INVALID_CURSOR')
  expect(searchWrites).toEqual([])
  expect((await page.request.post('/api/v1/auth/logout', { headers: await writeContext(page, application.url) })).status()).toBe(200)
  expect((await page.request.post('/api/v1/auth/login', { headers: { Origin: application.url }, data: { username: 'owner', password } })).status()).toBe(200)
  const continuation = await page.evaluate(async (cursor) => {
    const modulePath = '/assets/owned-sdk.js', api = await import(modulePath)
    return api.fetchBarcodeMatches({ value: '036000291452', limit: 1, cursor }, new AbortController().signal)
  }, result.first.next_cursor)
  expect(continuation.matches).toHaveLength(1)
  expect(continuation.matches[0].barcode_id).not.toBe(result.first.matches[0].barcode_id)
})

test.describe('retained exact barcode discovery SDK', () => {
  const value = '\0legacy + é\r\n00123'
  const ids = ['\0'.repeat(128), '\0barcode\r\n', '.', '..', 'a/b\\', '☕😀', '😀'.repeat(128), '\u{10ffff}',
    ...Array.from({ length: 205 }, (_, index) => `barcode-${String(index).padStart(3, '0')}`)]
  test.use({ applicationDatabaseFixture: { prepare(database) {
    // Retained rows and their actual counters/current payloads are seeded only
    // while the server is stopped; no mutation receipts or events are invented.
    const db = new DatabaseSync(database), now = '2026-11-06T00:00:00Z'
    try {
      db.exec('PRAGMA foreign_keys=ON; BEGIN IMMEDIATE')
      const counter = db.prepare('INSERT INTO projection_revisions(entity_type,entity_id,revision) VALUES(?,?,?)')
      const current = db.prepare('INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at) VALUES(?,?,?,?,?)')
      const baseline = (kind: string, id: string, revision: number, payload: object) => { counter.run(kind, id, revision); current.run(kind, id, revision, JSON.stringify(payload), now) }
      const item = db.prepare('INSERT INTO items(id,name,tracking_mode,status,created_at,updated_at) VALUES(?,?,?,?,?,?)')
      const barcode = db.prepare('INSERT INTO item_barcodes(id,item_id,symbology,raw_value,normalized_value,created_at) VALUES(?,?,?,?,?,?)')
      for (const [index, id] of ids.entries()) {
        const owner = index === 0 ? 'legacy/item\0😀' : `barcode-owner-${String(index).padStart(3, '0')}`
        const status = index === 1 ? 'archived' : 'active', symbology = index % 2 === 0 ? 'EAN_13' : 'QR_CODE', raw = 'retained raw differs'
        item.run(owner, 'Private owner name', 'individual', status, now, now)
        baseline('item', owner, 7, { id: owner, name: 'Private owner name', tracking_mode: 'individual', unit_code: 'pcs', category_id: null, brand: null,
          model: null, description: '', notes: '', base_unit: 'pcs', scale_factor: 1, status })
        counter.run('search_doc', JSON.stringify(['item', owner]), 1)
        barcode.run(id, owner, symbology, raw, value, now)
        baseline('barcode', id, 5, { id, item_id: owner, symbology, raw_value: raw, normalized_value: value })
      }
      db.exec('COMMIT')
      expect(db.prepare('PRAGMA foreign_key_check').all()).toEqual([])
    } finally { db.close() }
  } } })

  test('pages actual legacy sources and continues only the same scope after fresh login', async ({ page, application }) => {
    const password = await provision(page, application)
    const searches: string[] = []
    page.on('request', (request) => { if (new URL(request.url()).pathname === '/api/v1/search/barcodes') searches.push(request.method()) })
    await page.goto('/')
    const result = await page.evaluate(async (value) => {
      const modulePath = '/assets/owned-sdk.js', api = await import(modulePath), signal = () => new AbortController().signal
      const first = await api.fetchBarcodeMatches({ value, limit: 100 }, signal())
      const second = await api.fetchBarcodeMatches({ value, limit: 100, cursor: first.next_cursor }, signal())
      const third = await api.fetchBarcodeMatches({ value, limit: 100, cursor: second.next_cursor }, signal())
      const small = await api.fetchBarcodeMatches({ value, limit: 1 }, signal())
      const next = await api.fetchBarcodeMatches({ value, limit: 1, cursor: small.next_cursor }, signal())
      const legacy = await api.fetchBarcodeMatches({ value, symbology: 'EAN_13', limit: 100 }, signal())
      const failure = async (work: () => Promise<unknown>) => {
        try { await work(); return null } catch (error) { return error instanceof Error && 'code' in error ? error.code : 'unexpected' }
      }
      return { first, second, third, small, next, legacy,
        wrongValue: await failure(() => api.fetchBarcodeMatches({ value: value + 'x', limit: 1, cursor: small.next_cursor }, signal())),
        wrongSize: await failure(() => api.fetchBarcodeMatches({ value, limit: 2, cursor: small.next_cursor }, signal())),
        wrongNamespace: await failure(() => api.fetchBarcodeMatches({ value, symbology: 'EAN_13', limit: 1, cursor: small.next_cursor }, signal())) }
    }, value)
    expect(result.first.matches).toHaveLength(100); expect(result.second.matches).toHaveLength(100)
    expect(result.third.matches).toHaveLength(ids.length - 200); expect(result.third.next_cursor).toBeNull()
    const all = [...result.first.matches, ...result.second.matches, ...result.third.matches]
    expect(new Set(all.map((row: { barcode_id: string }) => row.barcode_id))).toEqual(new Set(ids))
    expect(all.every((row: { normalized_value: string }) => row.normalized_value === value)).toBe(true)
    expect(result.small.matches[0]).toMatchObject({ barcode_id: ids[0], item_id: 'legacy/item\0😀', symbology: 'EAN_13', normalized_value: value })
    expect(result.next.matches[0].barcode_id).toBe(ids[1])
    expect(result.legacy.matches).toHaveLength(100); expect(result.legacy.next_cursor).toBeTruthy()
    expect(result.legacy.matches.every((row: { symbology: string }) => row.symbology === 'EAN_13')).toBe(true)
    expect([result.wrongValue, result.wrongSize, result.wrongNamespace]).toEqual(['INVALID_CURSOR', 'INVALID_CURSOR', 'INVALID_CURSOR'])
    expect(JSON.stringify(result)).not.toContain('retained raw differs'); expect(JSON.stringify(result)).not.toContain('Private owner name')
    expect((await page.request.post('/api/v1/auth/logout', { headers: await writeContext(page, application.url) })).status()).toBe(200)
    expect((await page.request.post('/api/v1/auth/login', { headers: { Origin: application.url }, data: { username: 'owner', password } })).status()).toBe(200)
    const afterLogin = await page.evaluate(async ({ value, cursor }) => {
      const modulePath = '/assets/owned-sdk.js', api = await import(modulePath)
      return api.fetchBarcodeMatches({ value, limit: 1, cursor }, new AbortController().signal)
    }, { value, cursor: result.small.next_cursor })
    expect(afterLogin.matches[0].barcode_id).toBe(ids[1])
    expect(searches.every((method) => method === 'GET')).toBe(true)
  })

  test('rejects a corrupted real page and never retries after corruption or offline reconnect', async ({ page, application, context }) => {
    await provision(page, application)
    let reads = 0, tamper = true
    const exactPath = (url: URL) => url.pathname === '/api/v1/search/barcodes'
    await page.route(exactPath, async (route) => {
      reads++
      const response = await route.fetch()
      if (!tamper) { await route.fulfill({ response }); return }
      tamper = false
      const body = await response.json()
      body.matches[1].barcode_id = body.matches[0].barcode_id
      await route.fulfill({ response, body: JSON.stringify(body) })
    })
    await page.goto('/')
    const code = await page.evaluate(async (value) => {
      const modulePath = '/assets/owned-sdk.js', api = await import(modulePath)
      try { await api.fetchBarcodeMatches({ value, limit: 2 }, new AbortController().signal); return 'unexpected' }
      catch (error) { return error instanceof Error && 'code' in error ? error.code : 'unexpected' }
    }, value)
    expect(code).toBe('INVALID_RESPONSE'); expect(reads).toBe(1)
    await page.evaluate(() => window.dispatchEvent(new Event('online'))); expect(reads).toBe(1)
    const current = await page.evaluate(async (value) => {
      const modulePath = '/assets/owned-sdk.js', api = await import(modulePath)
      return api.fetchBarcodeMatches({ value, limit: 2 }, new AbortController().signal)
    }, value)
    expect(current.matches).toHaveLength(2); expect(current.next_cursor).toBeTruthy(); expect(reads).toBe(2)
    await page.unroute(exactPath)
    const requests: string[] = []
    page.on('request', (request) => { if (exactPath(new URL(request.url()))) requests.push(request.method()) })
    await context.setOffline(true)
    try {
      const offline = await page.evaluate(async (value) => {
        const modulePath = '/assets/owned-sdk.js', api = await import(modulePath)
        try { await api.fetchBarcodeMatches({ value }, new AbortController().signal); return 'unexpected' }
        catch (error) { return error instanceof Error && 'code' in error ? error.code : 'unexpected' }
      }, value)
      expect(offline).toBe('NETWORK_ERROR'); expect(requests).toEqual(['GET'])
    } finally { await context.setOffline(false) }
    await page.evaluate(() => window.dispatchEvent(new Event('online'))); expect(requests).toEqual(['GET'])
    const refreshed = await page.evaluate(async (value) => {
      const modulePath = '/assets/owned-sdk.js', api = await import(modulePath)
      return api.fetchBarcodeMatches({ value }, new AbortController().signal)
    }, value)
    expect(refreshed.matches).toHaveLength(50); expect(requests).toEqual(['GET', 'GET'])
  })
})
