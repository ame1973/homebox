import { cpSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { build } from 'vite'
import { test as applicationTest, expect } from './auth-fixture.ts'
import { provision, watchDomainWrites } from './domain-helpers.ts'

const test = applicationTest.extend<{}, { applicationWebDirectory: string }>({
  applicationWebDirectory: [async ({ browserName }, provideFixture) => {
    const directory = mkdtempSync(join(tmpdir(), `homebox-holding-sdk-${browserName}-`))
    const web = join(directory, 'web'), sdk = join(directory, 'sdk'), entry = join(directory, 'entry.ts')
    try {
      cpSync(fileURLToPath(new URL('../../dist', import.meta.url)), web, { recursive: true })
      // Build the real SDK into this private fixture, never a production test entry.
      writeFileSync(entry, `export * from ${JSON.stringify(fileURLToPath(new URL('../../src/lib/api/holdings.ts', import.meta.url)))};\nexport {createItem,fetchItem} from ${JSON.stringify(fileURLToPath(new URL('../../src/lib/api/catalog.ts', import.meta.url)))};\nexport {createLocation} from ${JSON.stringify(fileURLToPath(new URL('../../src/lib/api/locations.ts', import.meta.url)))};\n`)
      await build({ configFile: false, logLevel: 'silent', build: {
        outDir: sdk, minify: false, lib: { entry, formats: ['es'], fileName: 'holding-sdk' },
      } })
      cpSync(join(sdk, 'holding-sdk.js'), join(web, 'assets', 'holding-sdk.js'))
      await provideFixture(web)
    } finally { rmSync(directory, { recursive: true, force: true }) }
  }, { scope: 'worker' }],
})

test.use({ trace: 'off', screenshot: 'off', video: 'off' })

test('drives all seven Unit adapters with original Item/Unit proofs, exact metadata and retained receipts', async ({ page, application }) => {
  await provision(page, application)
  const writes = watchDomainWrites(page, ['/api/v1/items', '/api/v1/units'])
  await page.goto('/')
  const result = await page.evaluate(async () => {
    const modulePath = '/assets/holding-sdk.js'
    const api = await import(modulePath)
    const csrf = await fetch('/api/v1/auth/csrf', { credentials: 'same-origin', cache: 'no-store' })
    if (!csrf.ok) throw new Error('Holding SDK session unavailable')
    const csrfToken: string = (await csrf.json()).token
    const context = () => ({ csrfToken, idempotencyKey: crypto.randomUUID(), signal: new AbortController().signal })
    const read = () => new AbortController().signal
    const failureCode = async (work: () => Promise<unknown>) => {
      try { await work(); return null } catch (error) { return error instanceof Error && 'code' in error ? error.code : 'unexpected' }
    }
    const source = await api.createLocation({ name: 'Unit source', code: 'UNIT-SOURCE' }, context())
    const target = await api.createLocation({ name: 'Unit destination', code: 'UNIT-TARGET' }, context())
    const item = await api.createItem({ name: 'Individually tracked SDK fixture' }, context())
    const other = await api.createItem({ name: 'Another existing owner' }, context())
    const original = { ...context(), expectedRevision: item.revision }
    const created = await api.createUnit(item.id, { location_id: null }, original)
    const owner = await api.fetchItem(item.id, read())
    const second = await api.createUnit(item.id, { location_id: source.id, serial_number: '', notes: '  Exact é 😀  ', status: 'in_use' }, { ...context(), expectedRevision: owner.revision })
    const firstPage = await api.fetchItemUnitsPage(item.id, { limit: 1 }, read())
    if (!firstPage.next_cursor) throw new Error('Expected signed Unit continuation')
    const lastPage = await api.fetchItemUnitsPage(item.id, { limit: 1, cursor: firstPage.next_cursor }, read())
    const wrongOwner = await failureCode(() => api.fetchItemUnitsPage(other.id, { limit: 1, cursor: firstPage.next_cursor }, read()))
    const wrongLimit = await failureCode(() => api.fetchItemUnitsPage(item.id, { limit: 2, cursor: firstPage.next_cursor }, read()))
    const detail = await api.fetchUnit(created.id, read())
    const patched = await api.patchUnit(created.id, { serial_number: '', notes: '  Reviewed é 😀  ' }, { ...context(), itemID: item.id, expectedRevision: created.revision })
    const noop = await api.patchUnit(created.id, { serial_number: '', notes: patched.notes }, { ...context(), itemID: item.id, expectedRevision: patched.revision })
    const stale = await failureCode(() => api.patchUnit(created.id, { notes: 'Not adopted' }, { ...context(), itemID: item.id, expectedRevision: created.revision }))
    const moved = await api.moveUnit(created.id, { target_location_id: target.id }, { ...context(), itemID: item.id, expectedRevision: patched.revision })
    const retired = await api.setUnitStatus(created.id, { status: 'retired', note: 'Non-asset retirement' }, { ...context(), itemID: item.id, expectedRevision: moved.revision })
    const operational = await api.setUnitStatus(created.id, { status: 'in_use' }, { ...context(), itemID: item.id, expectedRevision: retired.revision })
    const disposed = await api.setUnitStatus(created.id, { status: 'disposed', disposal_kind: 'recycled', note: 'Explicit physical action' }, { ...context(), itemID: item.id, expectedRevision: operational.revision })
    const disposedMove = await failureCode(() => api.moveUnit(created.id, { target_location_id: source.id }, { ...context(), itemID: item.id, expectedRevision: disposed.revision }))
    const deletion = { ...context(), itemID: item.id, expectedRevision: disposed.revision }
    const deleted = await api.deleteUnit(created.id, deletion)
    const frozenDeletion = await api.deleteUnit(created.id, deletion)
    const frozenCreation = await api.createUnit(item.id, { location_id: null }, original)
    const missing = await failureCode(() => api.fetchUnit(created.id, read()))
    const remaining = await api.fetchItemUnitsPage(item.id, {}, read())
    return { item, source, target, created, second, firstPage, lastPage, wrongOwner, wrongLimit, detail, patched, noop, stale, moved, retired, operational, disposed, disposedMove, deleted, frozenDeletion, frozenCreation, missing, remaining }
  })
  expect(result.created).toMatchObject({ item_id: result.item.id, revision: 1, location_id: null, serial_number: null, notes: '', status: 'stored' })
  expect(result.created.asset_code).toMatch(/^AST-/)
  expect(result.second).toMatchObject({ item_id: result.item.id, revision: 1, location_id: result.source.id, serial_number: '', notes: '  Exact é 😀  ', status: 'in_use' })
  expect(result.lastPage.next_cursor).toBeNull()
  expect(new Set([...result.firstPage.data, ...result.lastPage.data].map((value: { id: string }) => value.id))).toEqual(new Set([result.created.id, result.second.id]))
  expect(result.wrongOwner).toBe('INVALID_CURSOR')
  expect(result.wrongLimit).toBe('INVALID_CURSOR')
  expect(result.detail).toEqual(result.created)
  expect(result.patched).toMatchObject({ id: result.created.id, item_id: result.item.id, revision: 2, serial_number: '', notes: '  Reviewed é 😀  ' })
  expect(result.noop).toEqual(result.patched)
  expect(result.stale).toBe('REVISION_CONFLICT')
  expect(result.moved).toMatchObject({ revision: 3, location_id: result.target.id })
  expect(result.retired).toMatchObject({ revision: 4, location_id: result.target.id, status: 'retired' })
  expect(result.operational).toMatchObject({ revision: 5, status: 'in_use' })
  expect(result.disposed).toMatchObject({ revision: 6, status: 'disposed' })
  expect(result.disposedMove).toBe('UNIT_DISPOSED')
  expect(result.deleted).toMatchObject({ ok: true, revision: 7 })
  expect(result.frozenDeletion).toEqual(result.deleted)
  expect(result.frozenCreation).toEqual(result.created)
  expect(result.missing).toBe('NOT_FOUND')
  expect(result.remaining).toEqual({ data: [result.second], next_cursor: null })
  const creations = writes.filter((request) => request.method === 'POST' && request.path.endsWith('/units'))
  expect(creations).toHaveLength(3)
  expect(creations[0]?.proof).toBe('"1"')
  expect(creations[1]?.proof).toBe('"2"')
  expect(creations[2]).toEqual(creations[0])
  expect(writes.find((request) => request.method === 'PATCH')?.proof).toBe('"1"')
  const deletions = writes.filter((request) => request.method === 'DELETE')
  expect(deletions).toHaveLength(2)
  expect(deletions[0]?.proof).toBe('"6"')
  expect(deletions[1]).toEqual(deletions[0])
})

test('drives all five Stock adapters with one reviewed Item interpretation, independent counters and zero rows', async ({ page, application }) => {
  await provision(page, application)
  const writes = watchDomainWrites(page, ['/api/v1/stock'])
  await page.goto('/')
  const result = await page.evaluate(async () => {
    const modulePath = '/assets/holding-sdk.js'
    const api = await import(modulePath)
    const csrf = await fetch('/api/v1/auth/csrf', { credentials: 'same-origin', cache: 'no-store' })
    if (!csrf.ok) throw new Error('Holding SDK session unavailable')
    const csrfToken: string = (await csrf.json()).token
    const context = () => ({ csrfToken, idempotencyKey: crypto.randomUUID(), signal: new AbortController().signal })
    const read = () => new AbortController().signal
    const failureCode = async (work: () => Promise<unknown>) => {
      try { await work(); return null } catch (error) { return error instanceof Error && 'code' in error ? error.code : 'unexpected' }
    }
    const source = await api.createLocation({ name: 'Stock source', code: 'STOCK-SOURCE' }, context())
    const target = await api.createLocation({ name: 'Stock target', code: 'STOCK-TARGET' }, context())
    const item = await api.createItem({ name: 'Quantity SDK fixture', tracking_mode: 'quantity', unit_code: 'kg', base_unit: 'g', scale_factor: 1000 }, context())
    const original = { ...context(), item }
    const body = { item_id: item.id, target_location_id: source.id, quantity_base: 1500, note: '  Exact receipt é  ' }
    const received = await api.receiveStock(body, original)
    const owner = await api.fetchItem(item.id, read())
    const targetReceived = await api.receiveStock({ item_id: item.id, target_location_id: target.id, quantity_base: 2000 }, { ...context(), item: owner })
    const other = await api.createItem({ name: 'Unrelated quantity fixture', tracking_mode: 'quantity', unit_code: 'L', base_unit: 'ml', scale_factor: 1000 }, context())
    await api.receiveStock({ item_id: other.id, target_location_id: source.id, quantity_base: 250 }, { ...context(), item: other })
    const transferred = await api.transferStock({ item_id: item.id, source_location_id: source.id, target_location_id: target.id, quantity_base: 500 }, { ...context(), item: await api.fetchItem(item.id, read()) })
    const consumed = await api.consumeStock({ item_id: item.id, source_location_id: source.id, quantity_base: 1000 }, { ...context(), item: await api.fetchItem(item.id, read()) })
    const adjusted = await api.adjustStock({ item_id: item.id, target_location_id: target.id, quantity_base: 0, reason: 'Explicit count', note: 'Absolute, not delta' }, { ...context(), item: await api.fetchItem(item.id, read()) })
    const beforeNoop = await api.fetchItem(item.id, read())
    const noop = await api.adjustStock({ item_id: item.id, target_location_id: target.id, quantity_base: 0, reason: 'Confirmed equal count' }, { ...context(), item: beforeNoop })
    const afterNoop = await api.fetchItem(item.id, read())
    const firstPage = await api.fetchStockPage({ item_id: item.id, limit: 1 }, read())
    if (!firstPage.next_cursor) throw new Error('Expected signed Stock continuation')
    const lastPage = await api.fetchStockPage({ item_id: item.id, limit: 1, cursor: firstPage.next_cursor }, read())
    const wrongScope = await failureCode(() => api.fetchStockPage({ item_id: item.id, limit: 1, cursor: firstPage.next_cursor, location_id: source.id, scope: 'direct' }, read()))
    const wrongItem = await failureCode(() => api.fetchStockPage({ item_id: other.id, limit: 1, cursor: firstPage.next_cursor }, read()))
    const unfiltered = await api.fetchStockPage({}, read())
    const direct = await api.fetchStockPage({ item_id: item.id, location_id: source.id, scope: 'direct', status: 'active' }, read())
    const stale = await failureCode(() => api.receiveStock(body, { ...context(), item }))
    const insufficient = await failureCode(() => api.transferStock({ item_id: item.id, source_location_id: source.id, target_location_id: target.id, quantity_base: 1 }, { ...context(), item: afterNoop }))
    const frozen = await api.receiveStock(body, original)
    const current = await api.fetchStockPage({ item_id: item.id }, read())
    return { item, source, target, owner, received, targetReceived, transferred, consumed, adjusted, beforeNoop, noop, afterNoop, firstPage, lastPage, wrongScope, wrongItem, unfiltered, direct, stale, insufficient, frozen, current }
  })
  expect(result.received).toMatchObject({ item_id: result.item.id, location_id: result.source.id, revision: 1, quantity_base: 1500, unit_code: 'kg', base_unit: 'g', scale_factor: 1000 })
  expect(result.owner.revision).toBe(2)
  expect(result.targetReceived).toMatchObject({ location_id: result.target.id, revision: 1, quantity_base: 2000 })
  expect(result.transferred).toMatchObject({ location_id: result.target.id, revision: 2, quantity_base: 2500 })
  expect(result.consumed).toMatchObject({ location_id: result.source.id, revision: 3, quantity_base: 0 })
  expect(result.adjusted).toMatchObject({ location_id: result.target.id, revision: 3, quantity_base: 0 })
  expect(result.noop).toEqual(result.adjusted)
  expect(result.afterNoop).toEqual(result.beforeNoop)
  expect(result.lastPage.next_cursor).toBeNull()
  expect(new Set([...result.firstPage.data, ...result.lastPage.data].map((value: { location_id: string }) => value.location_id))).toEqual(new Set([result.source.id, result.target.id]))
  expect(result.wrongScope).toBe('INVALID_CURSOR')
  expect(result.wrongItem).toBe('INVALID_CURSOR')
  expect(result.unfiltered.data).toHaveLength(3)
  expect(result.direct).toEqual({ data: [result.consumed], next_cursor: null })
  expect(result.stale).toBe('REVISION_CONFLICT')
  expect(result.insufficient).toBe('INSUFFICIENT_STOCK')
  expect(result.frozen).toEqual(result.received)
  expect(result.current.data).toHaveLength(2)
  for (const row of result.current.data) expect(row).toMatchObject({ quantity_base: 0, revision: 3, unit_code: 'kg', base_unit: 'g', scale_factor: 1000 })
  const receipts = writes.filter((request) => request.key === writes[0]?.key)
  expect(receipts).toHaveLength(2)
  expect(receipts[1]).toEqual(receipts[0])
  expect(writes[1]?.proof).toBe('"2"')
  expect(writes.find((request) => request.path.endsWith('/transfer'))?.proof).toBe('"3"')
})

for (const failure of ['wrong-owner', 'lost-reply'] as const) {
  test(`reconciles a genuinely committed ${failure} Unit reply only under the identical original request`, async ({ page, application }) => {
    await provision(page, application)
    const writes = watchDomainWrites(page, ['/api/v1/items'])
    let attempts = 0
    await page.route('**/api/v1/items/*/units', async (route) => {
      if (route.request().method() !== 'POST') { await route.continue(); return }
      const response = await route.fetch()
      if (++attempts !== 1) { await route.fulfill({ response }); return }
      expect(response.status()).toBe(200)
      if (failure === 'lost-reply') await route.abort('failed')
      else await route.fulfill({ response, body: JSON.stringify({ ...await response.json(), item_id: 'wrong-owner' }) })
    })
    await page.goto('/')
    const result = await page.evaluate(async () => {
      const modulePath = '/assets/holding-sdk.js'
      const api = await import(modulePath)
      const csrf = await fetch('/api/v1/auth/csrf', { credentials: 'same-origin', cache: 'no-store' })
      const csrfToken: string = (await csrf.json()).token
      const root = () => ({ csrfToken, idempotencyKey: crypto.randomUUID(), signal: new AbortController().signal })
      const item = await api.createItem({ name: 'Unknown Unit result' }, root())
      const original = { ...root(), expectedRevision: item.revision }
      const body = { location_id: null }
      let code: unknown
      try { await api.createUnit(item.id, body, original) } catch (error) { code = error instanceof Error && 'code' in error ? error.code : 'unexpected' }
      window.dispatchEvent(new Event('online'))
      const before = await api.fetchItemUnitsPage(item.id, {}, new AbortController().signal)
      const reconciled = await api.createUnit(item.id, body, original)
      const after = await api.fetchItemUnitsPage(item.id, {}, new AbortController().signal)
      return { code, before, reconciled, after }
    })
    expect(result.code).toBe(failure === 'lost-reply' ? 'NETWORK_ERROR' : 'INVALID_RESPONSE')
    expect(result.before.data).toHaveLength(1)
    expect(result.reconciled).toEqual(result.before.data[0])
    expect(result.after).toEqual(result.before)
    expect(attempts).toBe(2)
    const creations = writes.filter((request) => request.path.endsWith('/units'))
    expect(creations).toHaveLength(2)
    expect(creations[1]).toEqual(creations[0])
  })
}

test('rejects a genuinely committed wrong-measurement Stock reply and reconciles its frozen result without rebasing', async ({ page, application }) => {
  await provision(page, application)
  const writes = watchDomainWrites(page, ['/api/v1/stock'])
  let attempts = 0
  await page.route('**/api/v1/stock/receive', async (route) => {
    const response = await route.fetch()
    expect(response.status()).toBe(200)
    if (++attempts === 1) await route.fulfill({ response, body: JSON.stringify({ ...await response.json(), base_unit: 'ml', scale_factor: 1 }) })
    else await route.fulfill({ response })
  })
  await page.goto('/')
  const result = await page.evaluate(async () => {
    const modulePath = '/assets/holding-sdk.js'
    const api = await import(modulePath)
    const csrf = await fetch('/api/v1/auth/csrf', { credentials: 'same-origin', cache: 'no-store' })
    const csrfToken: string = (await csrf.json()).token
    const root = () => ({ csrfToken, idempotencyKey: crypto.randomUUID(), signal: new AbortController().signal })
    const read = () => new AbortController().signal
    const location = await api.createLocation({ name: 'Unknown receipt location', code: 'UNKNOWN-RECEIPT' }, root())
    const item = await api.createItem({ name: 'Unknown Stock result', tracking_mode: 'quantity', unit_code: 'kg', base_unit: 'g', scale_factor: 1000 }, root())
    const original = { ...root(), item }
    const body = { item_id: item.id, target_location_id: location.id, quantity_base: 1000 }
    let code: unknown
    try { await api.receiveStock(body, original) } catch (error) { code = error instanceof Error && 'code' in error ? error.code : 'unexpected' }
    window.dispatchEvent(new Event('online'))
    const before = await api.fetchStockPage({}, read())
    const owner = await api.fetchItem(item.id, read())
    const changed = await api.adjustStock({ ...body, quantity_base: 0, reason: 'Separately reviewed later count' }, { ...root(), item: owner })
    const reconciled = await api.receiveStock(body, original)
    const after = await api.fetchStockPage({}, read())
    return { code, before, changed, reconciled, after }
  })
  expect(result.code).toBe('INVALID_RESPONSE')
  expect(result.before.data).toHaveLength(1)
  expect(result.before.data[0]).toMatchObject({ quantity_base: 1000, revision: 1, base_unit: 'g', scale_factor: 1000 })
  expect(result.changed).toMatchObject({ quantity_base: 0, revision: 2 })
  expect(result.reconciled).toEqual(result.before.data[0])
  expect(result.after).toEqual({ data: [result.changed], next_cursor: null })
  expect(attempts).toBe(2)
  const receipts = writes.filter((request) => request.path.endsWith('/receive'))
  expect(receipts).toHaveLength(2)
  expect(receipts[1]).toEqual(receipts[0])
})
