import { cpSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { build } from 'vite'
import { test as applicationTest, expect } from './auth-fixture.ts'
import { provision, watchDomainWrites, writeContext } from './domain-helpers.ts'

const test = applicationTest.extend<{}, { applicationWebDirectory: string }>({
  applicationWebDirectory: [async ({ browserName }, provideFixture) => {
    const directory = mkdtempSync(join(tmpdir(), `homebox-purchase-sdk-${browserName}-`))
    const web = join(directory, 'web'), sdk = join(directory, 'sdk'), entry = join(directory, 'entry.ts')
    try {
      cpSync(fileURLToPath(new URL('../../dist', import.meta.url)), web, { recursive: true })
      // Build the real SDK privately, never a production test entry or fake API.
      writeFileSync(entry, `export * from ${JSON.stringify(fileURLToPath(new URL('../../src/lib/api/purchases.ts', import.meta.url)))};\nexport {createItem,fetchItem,patchItem} from ${JSON.stringify(fileURLToPath(new URL('../../src/lib/api/catalog.ts', import.meta.url)))};\nexport {createUnit,fetchUnit,fetchStockPage} from ${JSON.stringify(fileURLToPath(new URL('../../src/lib/api/holdings.ts', import.meta.url)))};\nexport * from ${JSON.stringify(fileURLToPath(new URL('../../src/features/purchases/model.ts', import.meta.url)))};\nexport * from ${JSON.stringify(fileURLToPath(new URL('../../src/features/purchases/form.ts', import.meta.url)))};\nexport * from ${JSON.stringify(fileURLToPath(new URL('../../src/features/purchases/commands.ts', import.meta.url)))};\n`)
      await build({ configFile: false, logLevel: 'silent', build: { outDir: sdk, minify: false, lib: { entry, formats: ['es'], fileName: 'purchase-sdk' } } })
      cpSync(join(sdk, 'purchase-sdk.js'), join(web, 'assets', 'purchase-sdk.js'))
      await provideFixture(web)
    } finally { rmSync(directory, { recursive: true, force: true }) }
  }, { scope: 'worker' }],
})

test.use({ trace: 'off', screenshot: 'off', video: 'off' })

test('drives all seven purchase adapters with original dual proofs, complete scopes and fresh-login frozen replies', async ({ page, application }) => {
  const replacement = await provision(page, application)
  const writes = watchDomainWrites(page, ['/api/v1/purchases'])
  await page.goto('/')
  const result = await page.evaluate(async () => {
    const modulePath = '/assets/purchase-sdk.js'
    const api = await import(modulePath)
    const csrf = await fetch('/api/v1/auth/csrf', { credentials: 'same-origin', cache: 'no-store' })
    if (!csrf.ok) throw new Error('Purchase SDK session unavailable')
    const csrfToken: string = (await csrf.json()).token
    const root = () => ({ csrfToken, idempotencyKey: crypto.randomUUID(), signal: new AbortController().signal })
    const read = () => new AbortController().signal
    const failureCode = async (work: () => Promise<unknown>) => { try { await work(); return null } catch (error) { return error instanceof Error && 'code' in error ? error.code : 'unexpected' } }
    const item = await api.createItem({ name: 'Purchase quantity', tracking_mode: 'quantity', unit_code: 'kg', base_unit: 'g', scale_factor: 1000 }, root())
    const individual = await api.createItem({ name: 'Purchase individual' }, root())
    const unit = await api.createUnit(individual.id, { location_id: null, serial_number: '', notes: 'Physical source remains exact' }, { ...root(), expectedRevision: individual.revision })
    const headerBody = { merchant_name: '  Merchant é 😀  ', purchased_at: '2026-11-06T12:00:00.123456789+08:00', currency: 'JPY', receipt_note: '\0Exact receipt\n' }
    const headerContext = root()
    const created = await api.createPurchase(headerBody, headerContext)
    const lineBody = { item_id: item.id, item_revision: item.revision, item_unit_id: null, quantity_base: 1500, paid_minor: 0 }
    const lineCommand = { ...root(), purchase: created, item }
    const allocated = await api.createPurchaseLine(created.id, lineBody, lineCommand)
    const quantityAfter = await api.fetchItem(item.id, read())
    const parentAfter = await api.fetchPurchase(created.id, read())
    const individualAfter = await api.fetchItem(individual.id, read())
    const unitLine = await api.createPurchaseLine(created.id, { item_id: individual.id, item_revision: individualAfter.revision, item_unit_id: unit.id, quantity_base: 1, paid_minor: 120000 }, { ...root(), purchase: parentAfter, item: individualAfter })
    const nextParent = await api.fetchPurchase(created.id, read())
    const otherLine = await api.createPurchaseLine(created.id, { item_id: item.id, item_revision: quantityAfter.revision, item_unit_id: null, quantity_base: 17, paid_minor: 123 }, { ...root(), purchase: nextParent, item: quantityAfter })
    const beforeCorrection = await api.fetchPurchase(created.id, read())
    const patchBody = { merchant_name: 'Corrected merchant', receipt_note: '' }
    const patchCommand = { ...root(), purchase: beforeCorrection }
    const corrected = await api.patchPurchase(created.id, patchBody, patchCommand)
    const noop = await api.patchPurchase(created.id, { receipt_note: '', purchased_at: headerBody.purchased_at }, { ...root(), purchase: corrected })
    const staleItem = await failureCode(() => api.createPurchaseLine(created.id, lineBody, { ...root(), purchase: corrected, item }))
    const staleParent = await failureCode(() => api.patchPurchase(created.id, { receipt_note: 'Not reviewed' }, { ...root(), purchase: created }))
    const locked = await failureCode(() => api.patchPurchase(created.id, { currency: 'USD' }, { ...root(), purchase: corrected }))
    const firstLines = await api.fetchPurchaseLinesPage(created.id, { limit: 1 }, read())
    if (!firstLines.next_cursor) throw new Error('Expected independent Line continuation')
    const lines = [...firstLines.data]
    let cursor = firstLines.next_cursor
    while (cursor) {
      const next = await api.fetchPurchaseLinesPage(created.id, { limit: 1, cursor }, read())
      lines.push(...next.data); cursor = next.next_cursor
    }
    const associated = await api.fetchItemPurchasesPage(item.id, {}, read())
    const individualAssociated = await api.fetchItemPurchasesPage(individual.id, {}, read())
    const stock = await api.fetchStockPage({ item_id: item.id }, read())
    const physical = await api.fetchUnit(unit.id, read())
    const secondHeader = await api.createPurchase({ merchant_name: 'Unallocated', purchased_at: '2026-11-06T00:00:00Z', currency: 'TWD' }, root())
    const first = await api.fetchPurchasesPage({ limit: 1 }, read())
    if (!first.next_cursor) throw new Error('Expected global Purchase continuation')
    const last = await api.fetchPurchasesPage({ limit: 1, cursor: first.next_cursor }, read())
    const changedSize = await failureCode(() => api.fetchPurchasesPage({ limit: 2, cursor: first.next_cursor }, read()))
    const filtered = await api.fetchPurchasesPage({ from: '2026-11-06', to: '2026-11-06', currency: 'JPY', merchant: 'Corrected merchant', status: 'active', scope: 'subtree' }, read())
    const crossCollection = await failureCode(() => api.fetchPurchaseLinesPage(created.id, { limit: 1, cursor: first.next_cursor }, read()))
    const crossOwner = await failureCode(() => api.fetchPurchaseLinesPage(secondHeader.id, { limit: 1, cursor: firstLines.next_cursor }, read()))
    const frozenHeader = await api.createPurchase(headerBody, headerContext)
    const frozenLine = await api.createPurchaseLine(created.id, lineBody, lineCommand)
    const later = await api.patchPurchase(created.id, { receipt_note: 'Later current source' }, { ...root(), purchase: corrected })
    const frozenPatch = await api.patchPurchase(created.id, patchBody, patchCommand)
    const current = await api.fetchPurchase(created.id, read())
    return { item, individual, unit, created, allocated, unitLine, otherLine, beforeCorrection, corrected, noop, staleItem, staleParent, locked,
      lines, associated, individualAssociated, stock, physical, secondHeader, first, last, changedSize, filtered, crossCollection, crossOwner,
      frozenHeader, frozenLine, later, frozenPatch, current,
      original: { headerBody, headerKey: headerContext.idempotencyKey, lineBody, lineKey: lineCommand.idempotencyKey, patchBody, patchKey: patchCommand.idempotencyKey, purchase: created, item, beforeCorrection } }
  })
  expect(result.created).toMatchObject({ revision: 1, purchased_at: '2026-11-06T04:00:00.123456789Z', receipt_note: '\0Exact receipt\n', currency: 'JPY' })
  expect(result.allocated).toMatchObject({ revision: 1, purchase_id: result.created.id, item_id: result.item.id, item_unit_id: null, quantity_base: 1500, paid_minor: 0, note: '' })
  expect(result.unitLine).toMatchObject({ revision: 1, item_id: result.individual.id, item_unit_id: result.unit.id, quantity_base: 1, paid_minor: 120000 })
  expect(result.otherLine).toMatchObject({ revision: 1, item_unit_id: null, quantity_base: 17, paid_minor: 123 })
  expect(result.beforeCorrection.revision).toBe(4)
  expect(result.corrected).toMatchObject({ revision: 5, merchant_name: 'Corrected merchant', receipt_note: '', currency: 'JPY' })
  expect(result.noop).toEqual(result.corrected)
  expect(result.staleItem).toBe('REVISION_CONFLICT'); expect(result.staleParent).toBe('REVISION_CONFLICT'); expect(result.locked).toBe('PURCHASE_CURRENCY_LOCKED')
  expect(result.lines).toHaveLength(3)
  expect(new Set(result.lines.map((row: { id: string }) => row.id))).toEqual(new Set([result.allocated.id, result.unitLine.id, result.otherLine.id]))
  expect(result.lines.reduce((sum: number, row: { paid_minor: number }) => sum + row.paid_minor, 0)).toBe(120123)
  expect(result.associated).toEqual({ data: [result.corrected], next_cursor: null })
  expect(result.individualAssociated).toEqual(result.associated)
  expect(result.stock).toEqual({ data: [], next_cursor: null }); expect(result.physical).toEqual(result.unit)
  expect(result.last.next_cursor).toBeNull()
  expect(new Set([...result.first.data, ...result.last.data].map((row: { id: string }) => row.id))).toEqual(new Set([result.created.id, result.secondHeader.id]))
  expect(result.changedSize).toBe('INVALID_CURSOR'); expect(result.crossCollection).toBe('INVALID_CURSOR'); expect(result.crossOwner).toBe('INVALID_CURSOR')
  expect(result.filtered).toEqual({ data: [result.corrected], next_cursor: null })
  expect(result.frozenHeader).toEqual(result.created); expect(result.frozenLine).toEqual(result.allocated); expect(result.frozenPatch).toEqual(result.corrected)
  expect(result.current).toEqual(result.later); expect(result.current).toMatchObject({ revision: 6, receipt_note: 'Later current source' })

  const logout = await writeContext(page, application.url)
  expect((await page.request.post('/api/v1/auth/logout', { headers: logout })).status()).toBe(200)
  expect((await page.request.post('/api/v1/auth/login', { headers: { Origin: application.url }, data: { username: 'owner', password: replacement } })).status()).toBe(200)
  const reconciled = await page.evaluate(async (original) => {
    const modulePath = '/assets/purchase-sdk.js'
    const api = await import(modulePath)
    const csrf = await fetch('/api/v1/auth/csrf', { credentials: 'same-origin', cache: 'no-store' })
    if (!csrf.ok) throw new Error('Fresh Purchase SDK session unavailable')
    const csrfToken: string = (await csrf.json()).token
    const root = (key: string) => ({ csrfToken, idempotencyKey: key, signal: new AbortController().signal })
    return { header: await api.createPurchase(original.headerBody, root(original.headerKey)),
      line: await api.createPurchaseLine(original.purchase.id, original.lineBody, { ...root(original.lineKey), purchase: original.purchase, item: original.item }),
      patch: await api.patchPurchase(original.purchase.id, original.patchBody, { ...root(original.patchKey), purchase: original.beforeCorrection }),
      current: await api.fetchPurchase(original.purchase.id, new AbortController().signal) }
  }, result.original)
  expect(reconciled).toEqual({ header: result.created, line: result.allocated, patch: result.corrected, current: result.later })
  for (const key of [result.original.headerKey, result.original.lineKey, result.original.patchKey]) {
    const attempts = writes.filter((request) => request.key === key)
    expect(attempts).toHaveLength(3)
    expect(attempts[1]).toEqual(attempts[0]); expect(attempts[2]).toEqual(attempts[0])
  }
  const firstLine = writes.find((request) => request.key === result.original.lineKey)
  expect(firstLine?.proof).toBe('"1"')
  expect(JSON.parse(firstLine?.body ?? '{}')).toMatchObject({ item_revision: 1, item_unit_id: null, quantity_base: 1500, paid_minor: 0 })
})

test('rejects genuine first-allocation Item and Purchase races without reinterpreting money or measurement', async ({ page, application }) => {
  await provision(page, application)
  await page.goto('/')
  const result = await page.evaluate(async () => {
    const modulePath = '/assets/purchase-sdk.js'
    const api = await import(modulePath)
    const csrf = await fetch('/api/v1/auth/csrf', { credentials: 'same-origin', cache: 'no-store' })
    const csrfToken: string = (await csrf.json()).token
    const root = () => ({ csrfToken, idempotencyKey: crypto.randomUUID(), signal: new AbortController().signal })
    const read = () => new AbortController().signal
    const failureCode = async (work: () => Promise<unknown>) => { try { await work(); return null } catch (error) { return error instanceof Error && 'code' in error ? error.code : 'unexpected' } }
    const item = await api.createItem({ name: 'Original interpretation', tracking_mode: 'quantity', unit_code: 'kg', base_unit: 'g', scale_factor: 1000 }, root())
    const purchase = await api.createPurchase({ merchant_name: 'Reviewed', purchased_at: '2026-11-06T00:00:00Z', currency: 'JPY' }, root())
    const changedItem = await api.patchItem(item.id, { unit_code: 'L', base_unit: 'ml', scale_factor: 1000 }, { ...root(), expectedRevision: item.revision })
    const body = { item_id: item.id, item_revision: item.revision, item_unit_id: null, quantity_base: 1500, paid_minor: 123 }
    const staleItem = await failureCode(() => api.createPurchaseLine(purchase.id, body, { ...root(), purchase, item }))
    const empty = await api.fetchPurchaseLinesPage(purchase.id, {}, read())
    const changedPurchase = await api.patchPurchase(purchase.id, { currency: 'USD' }, { ...root(), purchase })
    const staleParent = await failureCode(() => api.createPurchaseLine(purchase.id, { ...body, item_revision: changedItem.revision }, { ...root(), purchase, item: changedItem }))
    const adopted = await api.createPurchaseLine(purchase.id, { ...body, item_revision: changedItem.revision }, { ...root(), purchase: changedPurchase, item: changedItem })
    const actualItem = await api.fetchItem(item.id, read())
    const actualPurchase = await api.fetchPurchase(purchase.id, read())
    const unsupported = await failureCode(() => api.createPurchase({ merchant_name: 'Unsupported', purchased_at: '2026-11-06T00:00:00Z', currency: 'XAU' }, root()))
    const owner = await api.createItem({ name: 'Individual allocation owner' }, root())
    const other = await api.createItem({ name: 'Another Unit owner' }, root())
    const unit = await api.createUnit(other.id, { location_id: null }, { ...root(), expectedRevision: other.revision })
    const wrongUnitOwner = await failureCode(() => api.createPurchaseLine(purchase.id, { item_id: owner.id, item_revision: owner.revision, item_unit_id: unit.id, quantity_base: 1, paid_minor: 0 }, { ...root(), purchase: actualPurchase, item: owner }))
    const lines = await api.fetchPurchaseLinesPage(purchase.id, {}, read())
    return { staleItem, staleParent, empty, changedItem, changedPurchase, adopted, actualItem, actualPurchase, unsupported, wrongUnitOwner, lines }
  })
  expect(result.staleItem).toBe('REVISION_CONFLICT'); expect(result.staleParent).toBe('REVISION_CONFLICT')
  expect(result.empty).toEqual({ data: [], next_cursor: null })
  expect(result.changedItem).toMatchObject({ revision: 2, unit_code: 'L', base_unit: 'ml', scale_factor: 1000 })
  expect(result.changedPurchase).toMatchObject({ revision: 2, currency: 'USD' })
  expect(result.adopted).toMatchObject({ revision: 1, item_unit_id: null, quantity_base: 1500, paid_minor: 123 })
  expect(result.actualItem).toMatchObject({ revision: 3, unit_code: 'L', base_unit: 'ml', scale_factor: 1000 })
  expect(result.actualPurchase).toMatchObject({ revision: 3, currency: 'USD' })
  expect(result.unsupported).toBe('PURCHASE_CURRENCY_CONFLICT'); expect(result.wrongUnitOwner).toBe('VALIDATION_ERROR')
  expect(result.lines).toEqual({ data: [result.adopted], next_cursor: null })
})

for (const failure of ['wrong-owner', 'multiplied-money', 'parent-revision', 'lost-reply'] as const) {
  test(`reconciles a genuinely committed ${failure} allocation only through its explicit original command`, async ({ page, application }) => {
    await provision(page, application)
    const writes = watchDomainWrites(page, ['/api/v1/purchases'])
    let attempts = 0
    await page.route('**/api/v1/purchases/*/lines', async (route) => {
      if (route.request().method() !== 'POST') { await route.continue(); return }
      const response = await route.fetch()
      expect(response.status()).toBe(200)
      if (++attempts !== 1) { await route.fulfill({ response }); return }
      if (failure === 'lost-reply') { await route.abort('failed'); return }
      const line = await response.json()
      const body = { ...line, ...(failure === 'wrong-owner' ? { purchase_id: 'wrong-owner' } : failure === 'multiplied-money' ? { paid_minor: line.paid_minor * line.quantity_base } : { revision: 2 }) }
      await route.fulfill({ response, body: JSON.stringify(body), headers: { ...response.headers(), ...(failure === 'parent-revision' ? { etag: '"2"' } : {}) } })
    })
    await page.goto('/')
    const result = await page.evaluate(async () => {
      const modulePath = '/assets/purchase-sdk.js'
      const api = await import(modulePath)
      const csrf = await fetch('/api/v1/auth/csrf', { credentials: 'same-origin', cache: 'no-store' })
      const csrfToken: string = (await csrf.json()).token
      const root = () => ({ csrfToken, idempotencyKey: crypto.randomUUID(), signal: new AbortController().signal })
      const read = () => new AbortController().signal
      const item = await api.createItem({ name: 'Unknown allocation', tracking_mode: 'quantity', unit_code: 'kg', base_unit: 'g', scale_factor: 1000 }, root())
      const purchase = await api.createPurchase({ merchant_name: 'Unknown result', purchased_at: '2026-11-06T00:00:00Z', currency: 'JPY' }, root())
      const body = { item_id: item.id, item_revision: item.revision, item_unit_id: null, quantity_base: 1500, paid_minor: 123 }
      const original = { ...root(), purchase, item }
      let code: unknown
      try { await api.createPurchaseLine(purchase.id, body, original) } catch (error) { code = error instanceof Error && 'code' in error ? error.code : 'unexpected' }
      window.dispatchEvent(new Event('online'))
      const before = await api.fetchPurchaseLinesPage(purchase.id, {}, read())
      const current = await api.fetchPurchase(purchase.id, read())
      const later = await api.patchPurchase(purchase.id, { receipt_note: 'Later current context' }, { ...root(), purchase: current })
      const reconciled = await api.createPurchaseLine(purchase.id, body, original)
      const after = await api.fetchPurchaseLinesPage(purchase.id, {}, read())
      const final = await api.fetchPurchase(purchase.id, read())
      return { code, before, later, reconciled, after, final }
    })
    expect(result.code).toBe(failure === 'lost-reply' ? 'NETWORK_ERROR' : 'INVALID_RESPONSE')
    expect(result.before.data).toHaveLength(1)
    expect(result.reconciled).toEqual(result.before.data[0])
    expect(result.reconciled).toMatchObject({ revision: 1, item_unit_id: null, quantity_base: 1500, paid_minor: 123 })
    expect(result.after).toEqual(result.before); expect(result.final).toEqual(result.later)
    expect(result.final.revision).toBe(3); expect(attempts).toBe(2)
    const allocations = writes.filter((request) => request.path.endsWith('/lines'))
    expect(allocations).toHaveLength(2); expect(allocations[1]).toEqual(allocations[0])
  })
}

for (const failure of ['nanosecond-loss', 'wrong-currency'] as const) {
  test(`rejects a committed header with ${failure} and reconciles without creating another Purchase`, async ({ page, application }) => {
    await provision(page, application)
    const writes = watchDomainWrites(page, ['/api/v1/purchases'])
    let attempts = 0
    await page.route('**/api/v1/purchases', async (route) => {
      if (route.request().method() !== 'POST') { await route.continue(); return }
      const response = await route.fetch()
      expect(response.status()).toBe(200)
      if (++attempts !== 1) { await route.fulfill({ response }); return }
      const value = await response.json()
      await route.fulfill({ response, body: JSON.stringify({ ...value, ...(failure === 'nanosecond-loss' ? { purchased_at: '2026-11-06T04:00:00.123456788Z' } : { currency: 'USD' }) }) })
    })
    await page.goto('/')
    const result = await page.evaluate(async () => {
      const modulePath = '/assets/purchase-sdk.js'
      const api = await import(modulePath)
      const csrf = await fetch('/api/v1/auth/csrf', { credentials: 'same-origin', cache: 'no-store' })
      const csrfToken: string = (await csrf.json()).token
      const body = { merchant_name: 'Exact original header', purchased_at: '2026-11-06T12:00:00.123456789+08:00', currency: 'JPY' }
      const original = { csrfToken, idempotencyKey: crypto.randomUUID(), signal: new AbortController().signal }
      let code: unknown
      try { await api.createPurchase(body, original) } catch (error) { code = error instanceof Error && 'code' in error ? error.code : 'unexpected' }
      window.dispatchEvent(new Event('online'))
      const before = await api.fetchPurchasesPage({}, new AbortController().signal)
      const reconciled = await api.createPurchase(body, original)
      const after = await api.fetchPurchasesPage({}, new AbortController().signal)
      return { code, before, reconciled, after }
    })
    expect(result.code).toBe('INVALID_RESPONSE')
    expect(result.before.data).toHaveLength(1); expect(result.reconciled).toEqual(result.before.data[0]); expect(result.after).toEqual(result.before)
    expect(result.reconciled).toMatchObject({ revision: 1, currency: 'JPY', purchased_at: '2026-11-06T04:00:00.123456789Z', receipt_note: '' })
    expect(attempts).toBe(2)
    const creations = writes.filter((request) => request.method === 'POST')
    expect(creations).toHaveLength(2); expect(creations[1]).toEqual(creations[0])
  })
}

test('drives complete purchase review models, exact forms and original commands with explicit whole-interpretation adoption', async ({ page, application }) => {
  await provision(page, application)
  const writes = watchDomainWrites(page, ['/api/v1/purchases'])
  await page.goto('/')
  const result = await page.evaluate(async () => {
    const modulePath = '/assets/purchase-sdk.js'
    const api = await import(modulePath)
    const csrf = await fetch('/api/v1/auth/csrf', { credentials: 'same-origin', cache: 'no-store' })
    const csrfToken: string = (await csrf.json()).token
    const root = () => ({ csrfToken, idempotencyKey: crypto.randomUUID(), signal: new AbortController().signal })
    const read = () => new AbortController().signal
    const failureCode = async (work: () => Promise<unknown>) => { try { await work(); return null } catch (error) { return error instanceof Error && 'code' in error ? error.code : 'unexpected' } }
    const creation = api.freezePurchaseCommand({ kind: 'create-purchase', body: api.purchaseCreateBody({ ...api.purchaseDefaults(), merchant_name: '  Reviewed model é  ', purchased_at: '2026-11-06T12:00:00.123456789+08:00', currency: 'JPY' }) })
    const purchase = await api.sendPurchaseCommand(creation, csrfToken, read())
    const item = await api.createItem({ name: 'Original model interpretation', tracking_mode: 'quantity', unit_code: 'kg', base_unit: 'g', scale_factor: 1000 }, root())
    const original = await api.readPurchaseAllocation(purchase.id, item.id, read())
    const draft = { ...api.purchaseLineDefaults(), allocation: 'none', quantity_base: '1500', paid_minor: '123' }
    const oldCommand = api.freezePurchaseCommand({ kind: 'create-line', ...api.purchaseLineProposal(draft, original) })
    await api.patchItem(item.id, { unit_code: 'L', base_unit: 'ml', scale_factor: 100 }, { ...root(), expectedRevision: item.revision })
    await api.patchPurchase(purchase.id, { currency: 'USD' }, { ...root(), purchase })
    const stale = await failureCode(() => api.sendPurchaseCommand(oldCommand, csrfToken, read()))
    const current = await api.readPurchaseAllocation(purchase.id, item.id, read())
    const adopted = api.adoptPurchaseAllocation(draft, original, current)
    const allocation = api.freezePurchaseCommand({ kind: 'create-line', ...api.purchaseLineProposal(adopted.values, adopted.source) })
    const line = await api.sendPurchaseCommand(allocation, csrfToken, read())
    const detail = await api.readPurchaseDetail(purchase.id, read())
    const patch = api.purchasePatch({ ...api.purchaseDefaults(detail.purchase), receipt_note: '  Reviewed correction é\n' }, detail.purchase)
    const correction = api.freezePurchaseCommand({ kind: 'patch-purchase', source: detail, body: patch })
    const corrected = await api.sendPurchaseCommand(correction, csrfToken, read())
    const associated = await api.readItemPurchases(item.id, read())
    let locked: unknown
    try { api.freezePurchaseCommand({ kind: 'patch-purchase', source: detail, body: { currency: 'EUR' } }) } catch (error) { locked = error instanceof Error && 'code' in error ? error.code : 'unexpected' }
    const frozenLine = await api.sendPurchaseCommand(allocation, csrfToken, read())
    const frozenHeader = await api.sendPurchaseCommand(creation, csrfToken, read())
    const final = await api.readPurchaseDetail(purchase.id, read())
    const stock = await api.fetchStockPage({ item_id: item.id }, read())
    return { purchase, item, original, draft, adopted, stale, line, detail, corrected, associated, locked, frozenLine, frozenHeader, final, stock,
      keys: { creation: creation.key, allocation: allocation.key, correction: correction.key } }
  })
  expect(result.purchase).toMatchObject({ revision: 1, currency: 'JPY', purchased_at: '2026-11-06T04:00:00.123456789Z', receipt_note: '' })
  expect(result.original).toMatchObject({ kind: 'quantity', purchase: { revision: 1, currency: 'JPY' }, item: { revision: 1, unit_code: 'kg', base_unit: 'g', scale_factor: 1000 } })
  expect(result.original).not.toHaveProperty('units'); expect(result.original).not.toHaveProperty('stock')
  expect(result.stale).toBe('REVISION_CONFLICT')
  expect(result.adopted.values).toEqual(result.draft)
  expect(result.adopted.source).toMatchObject({ kind: 'quantity', purchase: { revision: 2, currency: 'USD' }, item: { revision: 2, unit_code: 'L', base_unit: 'ml', scale_factor: 100 } })
  expect(result.line).toMatchObject({ revision: 1, item_unit_id: null, quantity_base: 1500, paid_minor: 123 })
  expect(result.detail).toEqual({ purchase: { ...result.purchase, revision: 3, currency: 'USD' }, lines: [result.line] })
  expect(result.corrected).toMatchObject({ revision: 4, currency: 'USD', receipt_note: '  Reviewed correction é\n' })
  expect(result.associated).toMatchObject({ item: { revision: 4, base_unit: 'ml' }, purchases: [result.corrected] })
  expect(result.locked).toBe('PURCHASE_CURRENCY_LOCKED')
  expect(result.frozenLine).toEqual(result.line); expect(result.frozenHeader).toEqual(result.purchase)
  expect(result.final).toEqual({ purchase: result.corrected, lines: [result.line] }); expect(result.stock).toEqual({ data: [], next_cursor: null })
  for (const key of [result.keys.creation, result.keys.allocation]) {
    const attempts = writes.filter((request) => request.key === key)
    expect(attempts).toHaveLength(2); expect(attempts[1]).toEqual(attempts[0])
  }
  const allocated = writes.find((request) => request.key === result.keys.allocation)
  expect(allocated?.proof).toBe('"2"')
  expect(JSON.parse(allocated?.body ?? '{}')).toMatchObject({ item_revision: 2, item_unit_id: null, quantity_base: 1500, paid_minor: 123 })
  expect(writes.some((request) => JSON.parse(request.body ?? '{}').currency === 'EUR')).toBe(false)
})

test('withholds a whole Purchase detail when real source changes inside its Line collection interval', async ({ page, application }) => {
  await provision(page, application)
  await page.goto('/')
  const purchase = await page.evaluate(async () => {
    const modulePath = '/assets/purchase-sdk.js'
    const api = await import(modulePath)
    const csrf = await fetch('/api/v1/auth/csrf', { credentials: 'same-origin', cache: 'no-store' })
    return api.createPurchase({ merchant_name: 'Bracketed header', purchased_at: '2026-11-06T00:00:00Z', currency: 'JPY' }, { csrfToken: (await csrf.json()).token, idempotencyKey: crypto.randomUUID(), signal: new AbortController().signal })
  })
  let attempts = 0
  await page.route((url) => url.pathname === `/api/v1/purchases/${purchase.id}/lines`, async (route) => {
    if (route.request().method() !== 'GET') { await route.continue(); return }
    const response = await route.fetch()
    if (++attempts === 1) expect((await page.request.patch(`/api/v1/purchases/${purchase.id}`, { headers: await writeContext(page, application.url, purchase.revision), data: { receipt_note: 'Concurrent actual source' } })).status()).toBe(200)
    await route.fulfill({ response })
  })
  const result = await page.evaluate(async (purchaseID) => {
    const modulePath = '/assets/purchase-sdk.js'
    const api = await import(modulePath)
    let code: unknown
    try { await api.readPurchaseDetail(purchaseID, new AbortController().signal) } catch (error) { code = error instanceof Error && 'code' in error ? error.code : 'unexpected' }
    const current = await api.readPurchaseDetail(purchaseID, new AbortController().signal)
    return { code, current }
  }, purchase.id)
  expect(result.code).toBe('INVALID_RESPONSE')
  expect(result.current).toEqual({ purchase: { ...purchase, revision: 2, receipt_note: 'Concurrent actual source' }, lines: [] })
  expect(attempts).toBe(2)
})

test('withholds a mixed individual allocation context and reviews the actual selected Unit under the changed Item proof', async ({ page, application }) => {
  await provision(page, application)
  await page.goto('/')
  const original = await page.evaluate(async () => {
    const modulePath = '/assets/purchase-sdk.js'
    const api = await import(modulePath)
    const csrf = await fetch('/api/v1/auth/csrf', { credentials: 'same-origin', cache: 'no-store' })
    const csrfToken: string = (await csrf.json()).token
    const root = () => ({ csrfToken, idempotencyKey: crypto.randomUUID(), signal: new AbortController().signal })
    const purchase = await api.createPurchase({ merchant_name: 'Individual review', purchased_at: '2026-11-06T00:00:00Z', currency: 'JPY' }, root())
    const created = await api.createItem({ name: 'Original Unit owner' }, root())
    const unit = await api.createUnit(created.id, { location_id: null, serial_number: '' }, { ...root(), expectedRevision: created.revision })
    const item = await api.fetchItem(created.id, new AbortController().signal)
    return { purchase, item, unit }
  })
  let attempts = 0
  await page.route((url) => url.pathname === `/api/v1/items/${original.item.id}/units`, async (route) => {
    if (route.request().method() !== 'GET') { await route.continue(); return }
    const response = await route.fetch()
    if (++attempts === 1) expect((await page.request.patch(`/api/v1/items/${original.item.id}`, { headers: await writeContext(page, application.url, original.item.revision), data: { name: 'Concurrent actual owner' } })).status()).toBe(200)
    await route.fulfill({ response })
  })
  const result = await page.evaluate(async (original) => {
    const modulePath = '/assets/purchase-sdk.js'
    const api = await import(modulePath)
    const read = () => new AbortController().signal
    let code: unknown
    try { await api.readPurchaseAllocation(original.purchase.id, original.item.id, read()) } catch (error) { code = error instanceof Error && 'code' in error ? error.code : 'unexpected' }
    const current = await api.readPurchaseAllocation(original.purchase.id, original.item.id, read())
    const proposal = api.purchaseLineProposal({ ...api.purchaseLineDefaults(), allocation: 'unit', item_unit_id: original.unit.id, quantity_base: '1', paid_minor: '0' }, current)
    const command = api.freezePurchaseCommand({ kind: 'create-line', ...proposal })
    const csrf = await fetch('/api/v1/auth/csrf', { credentials: 'same-origin', cache: 'no-store' })
    const allocated = await api.sendPurchaseCommand(command, (await csrf.json()).token, read())
    const physical = await api.fetchUnit(original.unit.id, read())
    return { code, current, proposal, allocated, physical }
  }, original)
  expect(result.code).toBe('INVALID_RESPONSE')
  expect(result.current).toEqual({ kind: 'individual', purchase: original.purchase, item: { ...original.item, revision: 3, name: 'Concurrent actual owner' }, units: [original.unit] })
  expect(result.proposal).toMatchObject({ unit: original.unit, body: { item_revision: 3, item_unit_id: original.unit.id, quantity_base: 1, paid_minor: 0 } })
  expect(result.allocated).toMatchObject({ revision: 1, item_id: original.item.id, item_unit_id: original.unit.id, quantity_base: 1, paid_minor: 0 })
  expect(result.physical).toEqual(original.unit); expect(attempts).toBe(2)
})

test('retains an actual frozen review command after committed reply loss and later complete source changes', async ({ page, application }) => {
  await provision(page, application)
  const writes = watchDomainWrites(page, ['/api/v1/purchases'])
  let attempts = 0
  await page.route('**/api/v1/purchases/*/lines', async (route) => {
    if (route.request().method() !== 'POST') { await route.continue(); return }
    const response = await route.fetch()
    expect(response.status()).toBe(200)
    if (++attempts === 1) await route.abort('failed')
    else await route.fulfill({ response })
  })
  await page.goto('/')
  const result = await page.evaluate(async () => {
    const modulePath = '/assets/purchase-sdk.js'
    const api = await import(modulePath)
    const csrf = await fetch('/api/v1/auth/csrf', { credentials: 'same-origin', cache: 'no-store' })
    const csrfToken: string = (await csrf.json()).token
    const root = () => ({ csrfToken, idempotencyKey: crypto.randomUUID(), signal: new AbortController().signal })
    const read = () => new AbortController().signal
    const purchase = await api.createPurchase({ merchant_name: 'Unknown original command', purchased_at: '2026-11-06T00:00:00Z', currency: 'JPY' }, root())
    const item = await api.createItem({ name: 'Unknown allocation', tracking_mode: 'quantity', unit_code: 'kg', base_unit: 'g', scale_factor: 1000 }, root())
    const source = await api.readPurchaseAllocation(purchase.id, item.id, read())
    const command = api.freezePurchaseCommand({ kind: 'create-line', ...api.purchaseLineProposal({ ...api.purchaseLineDefaults(), allocation: 'none', quantity_base: '1500', paid_minor: '123' }, source) })
    let code: unknown
    try { await api.sendPurchaseCommand(command, csrfToken, read()) } catch (error) { code = error instanceof Error && 'code' in error ? error.code : 'unexpected' }
    window.dispatchEvent(new Event('online'))
    const before = await api.readPurchaseDetail(purchase.id, read())
    const correction = api.freezePurchaseCommand({ kind: 'patch-purchase', source: before, body: { merchant_name: 'Later reviewed current merchant' } })
    const later = await api.sendPurchaseCommand(correction, csrfToken, read())
    const refreshed = await api.readPurchaseAllocation(purchase.id, item.id, read())
    const reconciled = await api.sendPurchaseCommand(command, csrfToken, read())
    const final = await api.readPurchaseDetail(purchase.id, read())
    return { code, before, later, refreshed, original: command, reconciled, final }
  })
  expect(result.code).toBe('NETWORK_ERROR')
  expect(result.before.lines).toHaveLength(1)
  expect(result.refreshed).toMatchObject({ purchase: { revision: 3, merchant_name: 'Later reviewed current merchant' }, item: { revision: 3 } })
  expect(result.original).toMatchObject({ purchase: { revision: 1, merchant_name: 'Unknown original command' }, item: { revision: 1, base_unit: 'g' }, unit: null, body: { item_revision: 1, quantity_base: 1500, paid_minor: 123 } })
  expect(result.reconciled).toEqual(result.before.lines[0]); expect(result.final).toEqual({ purchase: result.later, lines: result.before.lines })
  expect(attempts).toBe(2)
  const allocations = writes.filter((request) => request.path.endsWith('/lines'))
  expect(allocations).toHaveLength(2); expect(allocations[1]).toEqual(allocations[0])
})
