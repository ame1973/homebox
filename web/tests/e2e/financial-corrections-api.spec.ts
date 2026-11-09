import { cpSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { build } from 'vite'
import { test as applicationTest, expect } from './auth-fixture.ts'
import { provision, watchDomainWrites, writeContext } from './domain-helpers.ts'

const test = applicationTest.extend<{}, { applicationWebDirectory: string }>({
  applicationWebDirectory: [async ({ browserName }, provideFixture) => {
    const directory = mkdtempSync(join(tmpdir(), `homebox-financial-corrections-${browserName}-`))
    const web = join(directory, 'web'), sdk = join(directory, 'sdk'), entry = join(directory, 'entry.ts')
    try {
      cpSync(fileURLToPath(new URL('../../dist', import.meta.url)), web, { recursive: true })
      // Bundle the actual SDK privately, without a production test entrypoint.
      writeFileSync(entry, `export * from ${JSON.stringify(fileURLToPath(new URL('../../src/lib/api/financial-corrections.ts', import.meta.url)))};\nexport * from ${JSON.stringify(fileURLToPath(new URL('../../src/lib/api/assets.ts', import.meta.url)))};\nexport * from ${JSON.stringify(fileURLToPath(new URL('../../src/lib/api/purchases.ts', import.meta.url)))};\nexport {createItem,fetchItem,patchItem} from ${JSON.stringify(fileURLToPath(new URL('../../src/lib/api/catalog.ts', import.meta.url)))};\nexport {createUnit,fetchUnit} from ${JSON.stringify(fileURLToPath(new URL('../../src/lib/api/holdings.ts', import.meta.url)))};\nexport {readPurchaseCorrection,readPurchaseCorrectionHistory} from ${JSON.stringify(fileURLToPath(new URL('../../src/features/purchases/correction-model.ts', import.meta.url)))};\nexport {readDisposalCorrection} from ${JSON.stringify(fileURLToPath(new URL('../../src/features/assets/correction-model.ts', import.meta.url)))};\nexport {purchaseCorrectionDefaults,purchaseCorrectionProposal,adoptPurchaseCorrection} from ${JSON.stringify(fileURLToPath(new URL('../../src/features/purchases/correction-form.ts', import.meta.url)))};\nexport {disposalCorrectionDefaults,disposalCorrectionProposal,adoptDisposalCorrection} from ${JSON.stringify(fileURLToPath(new URL('../../src/features/assets/correction-form.ts', import.meta.url)))};\n`)
      await build({ configFile: false, logLevel: 'silent', build: { outDir: sdk, minify: false, lib: { entry, formats: ['es'], fileName: 'financial-corrections-sdk' } } })
      cpSync(join(sdk, 'financial-corrections-sdk.js'), join(web, 'assets', 'financial-corrections-sdk.js'))
      await provideFixture(web)
    } finally { rmSync(directory, { recursive: true, force: true }) }
  }, { scope: 'worker' }],
})
test.use({ trace: 'off', screenshot: 'off', video: 'off' })

test('drives all five financial correction adapters against the real Go server and reconciles original keys after fresh login', async ({ page, application }) => {
  const password = await provision(page, application), writes = watchDomainWrites(page, ['/api/v1/purchases', '/api/v1/units']), cursors: string[] = []
  page.on('request', (request) => { const url = new URL(request.url()), cursor = url.searchParams.get('cursor'); if (request.method() === 'GET' && cursor !== null) cursors.push(cursor) })
  await page.goto('/')
  const result = await page.evaluate(async () => {
    const modulePath = '/assets/financial-corrections-sdk.js', api = await import(modulePath)
    const csrf = await fetch('/api/v1/auth/csrf', { credentials: 'same-origin', cache: 'no-store' }); if (!csrf.ok) throw new Error('Financial session unavailable')
    const csrfToken: string = (await csrf.json()).token
    const root = () => ({ csrfToken, idempotencyKey: crypto.randomUUID(), signal: new AbortController().signal }), read = () => new AbortController().signal
    const failure = async (work: () => Promise<unknown>) => { try { await work(); return null } catch (error) { return error instanceof Error && 'code' in error ? error.code : 'unexpected' } }
    const item = await api.createItem({ name: 'Corrected quantity', tracking_mode: 'quantity', unit_code: 'kg', base_unit: 'g', scale_factor: 1000 }, root())
    const purchase = await api.createPurchase({ merchant_name: 'Actual merchant', purchased_at: '2026-11-07T12:00:00.123456789+08:00', currency: 'JPY' }, root())
    const line = await api.createPurchaseLine(purchase.id, { item_id: item.id, item_revision: item.revision, item_unit_id: null, quantity_base: 1500, paid_minor: 0 }, { ...root(), purchase, item })
    const reviewedPurchase = await api.fetchPurchase(purchase.id, read()), reviewedItem = await api.fetchItem(item.id, read())
    const lineBody = { line_id: line.id, line_revision: line.revision, item_revision: reviewedItem.revision, action: 'replace', replacement: { item_id: reviewedItem.id, item_revision: reviewedItem.revision, item_unit_id: null, quantity_base: 2500, paid_minor: 99 }, reason: '\0Correct exact allocation é' }
    const lineContext = { ...root(), purchase: reviewedPurchase, line, item: reviewedItem, replacementItem: reviewedItem }
    const firstLine = await api.correctPurchaseLine(purchase.id, lineBody, lineContext)
    if (!firstLine.after) throw new Error('Expected a posted replacement')
    const currentPurchase = await api.fetchPurchase(purchase.id, read()), currentItem = await api.fetchItem(item.id, read())
    const secondLine = await api.correctPurchaseLine(purchase.id, { line_id: firstLine.after.id, line_revision: firstLine.after.revision, item_revision: currentItem.revision, action: 'void', replacement: null, reason: 'Explicitly void replacement' }, { ...root(), purchase: currentPurchase, line: firstLine.after, item: currentItem, replacementItem: null })
    const firstLinePage = await api.fetchPurchaseCorrectionsPage(purchase.id, { limit: 1 }, read())
    if (!firstLinePage.next_cursor) throw new Error('Expected Purchase correction continuation')
    const lastLinePage = await api.fetchPurchaseCorrectionsPage(purchase.id, { limit: 1, cursor: firstLinePage.next_cursor }, read())
    const oversizedCursor = 'x'.repeat(2044) + '😀x'
    const oversizedPurchase = await failure(() => api.fetchPurchaseCorrectionsPage(purchase.id, { cursor: oversizedCursor }, read()))
    const currentLines = await api.fetchPurchaseLinesPage(purchase.id, {}, read())

    const owner = await api.createItem({ name: 'Corrected physical Unit' }, root())
    const unit = await api.createUnit(owner.id, { location_id: null, notes: 'Physical source' }, { ...root(), expectedRevision: owner.revision })
    const actualOwner = await api.fetchItem(owner.id, read())
    const profile = await api.putAssetProfile(unit.id, { item_revision: actualOwner.revision, asset_revision: null, cost_basis: 'manual', currency: 'JPY', manual_acquisition_minor: 100, in_service_at: null, retired_at: null, estimated_residual_minor: 30, usage_unit: 'times' }, { ...root(), unit, item: actualOwner, asset: null })
    const posted = await api.disposeAsset(unit.id, { unit_revision: unit.revision, kind: 'sold', disposed_at: '2026-11-07T12:00:00.123456789+08:00', currency: 'JPY', realized_proceeds_minor: 200 }, { ...root(), asset: profile, unit, disposal: null })
    const physical = await api.fetchUnit(unit.id, read()), record = await api.fetchDisposalRecord(unit.id, read()), originalAsset = await api.fetchAsset(unit.id, read())
    const disposalBody = { asset_revision: originalAsset.revision, action: 'replace', replacement: { kind: 'sold', disposed_at: '2026-11-07T12:00:00.123456789+08:00', currency: 'JPY', realized_proceeds_minor: 0 }, reason: 'Correct to explicit zero' }
    const disposalContext = { ...root(), asset: originalAsset, disposal: record }
    const zero = await api.correctDisposal(unit.id, disposalBody, disposalContext)
    const afterZero = await api.fetchDisposalRecord(unit.id, read()), assetAfterZero = await api.fetchAsset(unit.id, read())
    const financialVoid = await api.correctDisposal(unit.id, { asset_revision: assetAfterZero.revision, action: 'void', replacement: null, reason: 'Explicit financial void' }, { ...root(), asset: assetAfterZero, disposal: afterZero })
    const voided = await api.fetchDisposalRecord(unit.id, read()), absent = await failure(() => api.fetchAssetDisposal(unit.id, read()))
    const voidSummary = await api.fetchAssetCostSummary(unit.id, {}, read()), assetAfterVoid = await api.fetchAsset(unit.id, read())
    const reinstated = await api.correctDisposal(unit.id, { asset_revision: assetAfterVoid.revision, action: 'replace', replacement: { kind: 'sold', disposed_at: '2026-11-07T12:00:00.123456789+08:00', currency: 'JPY', realized_proceeds_minor: 300 }, reason: 'Explicitly reinstate' }, { ...root(), asset: assetAfterVoid, disposal: voided })
    const currentRecord = await api.fetchDisposalRecord(unit.id, read()), finalSummary = await api.fetchAssetCostSummary(unit.id, {}, read())
    const firstDisposalPage = await api.fetchDisposalCorrectionsPage(unit.id, { limit: 1 }, read())
    if (!firstDisposalPage.next_cursor) throw new Error('Expected Disposal correction continuation')
    const secondDisposalPage = await api.fetchDisposalCorrectionsPage(unit.id, { limit: 1, cursor: firstDisposalPage.next_cursor }, read())
    if (!secondDisposalPage.next_cursor) throw new Error('Expected final Disposal correction continuation')
    const thirdDisposalPage = await api.fetchDisposalCorrectionsPage(unit.id, { limit: 1, cursor: secondDisposalPage.next_cursor }, read())
    const crossScope = await failure(() => api.fetchDisposalCorrectionsPage(unit.id, { limit: 1, cursor: firstLinePage.next_cursor }, read()))
    const oversizedDisposal = await failure(() => api.fetchDisposalCorrectionsPage(unit.id, { cursor: oversizedCursor }, read()))
    const afterPhysical = await api.fetchUnit(unit.id, read())
    return { item, purchase, line, firstLine, secondLine, firstLinePage, lastLinePage, currentLines, unit, posted, physical, zero, financialVoid, voided, absent, voidSummary, reinstated, currentRecord, finalSummary, firstDisposalPage, secondDisposalPage, thirdDisposalPage, crossScope, oversizedCursor, oversizedPurchase, oversizedDisposal, afterPhysical,
      original: { lineBody, lineKey: lineContext.idempotencyKey, purchase: reviewedPurchase, line, item: reviewedItem, disposalBody, disposalKey: disposalContext.idempotencyKey, asset: originalAsset, record } }
  })
  expect(result.firstLine).toMatchObject({ revision: 1, action: 'replace', before: { paid_minor: 0 }, after: { revision: 1, paid_minor: 99, quantity_base: 2500 } })
  expect(result.firstLine.after.id).not.toBe(result.line.id); expect(result.secondLine).toMatchObject({ action: 'void', after: null })
  expect(result.currentLines).toEqual({ data: [], next_cursor: null }); expect(result.lastLinePage.next_cursor).toBeNull()
  expect(new Set([...result.firstLinePage.data, ...result.lastLinePage.data].map((entry: { id: string }) => entry.id))).toEqual(new Set([result.firstLine.id, result.secondLine.id]))
  expect(result.zero.after.current).toMatchObject({ realized_proceeds_minor: 0, disposed_at: result.posted.disposed_at }); expect(result.financialVoid.after.current).toBeNull()
  expect(result.voided).toMatchObject({ current: null, original: { realized_proceeds_minor: 200 } }); expect(result.absent).toBe('NOT_FOUND')
  expect(result.voidSummary).toMatchObject({ realized_proceeds_minor: 0, net_cost_minor: 100, projected_net_cost_minor: 70 })
  expect(result.reinstated.after.current).toMatchObject({ realized_proceeds_minor: 300, disposed_at: result.posted.disposed_at }); expect(result.currentRecord).toEqual(result.reinstated.after)
  expect(result.finalSummary).toMatchObject({ realized_proceeds_minor: 300, net_cost_minor: -200, projected_net_cost_minor: 70 })
  expect(result.thirdDisposalPage.next_cursor).toBeNull(); expect(new Set([...result.firstDisposalPage.data, ...result.secondDisposalPage.data, ...result.thirdDisposalPage.data].map((entry: { id: string }) => entry.id))).toEqual(new Set([result.zero.id, result.financialVoid.id, result.reinstated.id]))
  expect(result.crossScope).toBe('INVALID_CURSOR'); expect(result.oversizedPurchase).toBe('VALIDATION_ERROR'); expect(result.oversizedDisposal).toBe('VALIDATION_ERROR'); expect(cursors).not.toContain(result.oversizedCursor)
  expect(result.afterPhysical).toEqual(result.physical); expect(result.physical).toMatchObject({ status: 'disposed', revision: 2 })
  expect((await page.request.post('/api/v1/auth/logout', { headers: await writeContext(page, application.url) })).status()).toBe(200)
  expect((await page.request.post('/api/v1/auth/login', { headers: { Origin: application.url }, data: { username: 'owner', password } })).status()).toBe(200)
  const replay = await page.evaluate(async (original) => {
    const modulePath = '/assets/financial-corrections-sdk.js', api = await import(modulePath)
    const csrf = await fetch('/api/v1/auth/csrf', { credentials: 'same-origin', cache: 'no-store' }); if (!csrf.ok) throw new Error('Fresh financial session unavailable')
    const csrfToken: string = (await csrf.json()).token, root = (idempotencyKey: string) => ({ csrfToken, idempotencyKey, signal: new AbortController().signal })
    return { purchase: await api.correctPurchaseLine(original.purchase.id, original.lineBody, { ...root(original.lineKey), purchase: original.purchase, line: original.line, item: original.item, replacementItem: original.item }),
      disposal: await api.correctDisposal(original.record.item_unit_id, original.disposalBody, { ...root(original.disposalKey), asset: original.asset, disposal: original.record }),
      current: await api.fetchDisposalRecord(original.record.item_unit_id, new AbortController().signal), lines: await api.fetchPurchaseLinesPage(original.purchase.id, {}, new AbortController().signal) }
  }, result.original)
  expect(replay).toEqual({ purchase: result.firstLine, disposal: result.zero, current: result.currentRecord, lines: result.currentLines })
  for (const key of [result.original.lineKey, result.original.disposalKey]) {
    const attempts = writes.filter((request) => request.key === key); expect(attempts).toHaveLength(2); expect(attempts[1]).toEqual(attempts[0])
  }
})

test('preserves separate original and replacement Item proofs for a real cross-Item allocation', async ({ page, application }) => {
  await provision(page, application); const writes = watchDomainWrites(page, ['/api/v1/purchases']); await page.goto('/')
  const result = await page.evaluate(async () => {
    const modulePath = '/assets/financial-corrections-sdk.js', api = await import(modulePath)
    const csrf = await fetch('/api/v1/auth/csrf', { credentials: 'same-origin', cache: 'no-store' }); if (!csrf.ok) throw new Error('Financial session unavailable')
    const csrfToken: string = (await csrf.json()).token, root = () => ({ csrfToken, idempotencyKey: crypto.randomUUID(), signal: new AbortController().signal }), read = () => new AbortController().signal
    const failure = async (work: () => Promise<unknown>) => { try { await work(); return null } catch (error) { return error instanceof Error && 'code' in error ? error.code : 'unexpected' } }
    const oldOwner = await api.createItem({ name: 'Original quantity', tracking_mode: 'quantity', unit_code: 'kg', base_unit: 'g', scale_factor: 1000 }, root())
    const newOwner = await api.createItem({ name: 'Replacement individual' }, root())
    const unit = await api.createUnit(newOwner.id, { location_id: null, notes: 'Unchanged replacement Unit' }, { ...root(), expectedRevision: newOwner.revision })
    const replacement = await api.fetchItem(newOwner.id, read())
    const purchase = await api.createPurchase({ merchant_name: 'Cross-owner merchant', purchased_at: '2026-11-07T00:00:00Z', currency: 'JPY' }, root())
    const line = await api.createPurchaseLine(purchase.id, { item_id: oldOwner.id, item_revision: oldOwner.revision, item_unit_id: null, quantity_base: 1500, paid_minor: 0 }, { ...root(), purchase, item: oldOwner })
    const reviewedPurchase = await api.fetchPurchase(purchase.id, read()), oldItem = await api.fetchItem(oldOwner.id, read())
    const originalBody = { line_id: line.id, line_revision: line.revision, item_revision: oldItem.revision, action: 'replace', replacement: { item_id: replacement.id, item_revision: replacement.revision, item_unit_id: unit.id, quantity_base: 1, paid_minor: 500 }, reason: 'Correct ownership without moving inventory' }
    const first = root(); await api.patchItem(oldItem.id, { notes: 'Concurrent original Item change' }, { ...root(), expectedRevision: oldItem.revision })
    const staleOriginal = await failure(() => api.correctPurchaseLine(purchase.id, originalBody, { ...first, purchase: reviewedPurchase, line, item: oldItem, replacementItem: replacement }))
    const freshOld = await api.fetchItem(oldItem.id, read()), second = root(), secondBody = { ...originalBody, item_revision: freshOld.revision }
    await api.patchItem(replacement.id, { notes: 'Concurrent replacement Item change' }, { ...root(), expectedRevision: replacement.revision })
    const staleReplacement = await failure(() => api.correctPurchaseLine(purchase.id, secondBody, { ...second, purchase: reviewedPurchase, line, item: freshOld, replacementItem: replacement }))
    const freshReplacement = await api.fetchItem(replacement.id, read()), final = root()
    const finalBody = { ...secondBody, replacement: { ...secondBody.replacement, item_revision: freshReplacement.revision } }
    const corrected = await api.correctPurchaseLine(purchase.id, finalBody, { ...final, purchase: reviewedPurchase, line, item: freshOld, replacementItem: freshReplacement })
    window.dispatchEvent(new Event('online'))
    return { unit, oldItem, replacement, freshOld, freshReplacement, staleOriginal, staleReplacement, corrected, firstKey: first.idempotencyKey, secondKey: second.idempotencyKey, finalKey: final.idempotencyKey,
      physical: await api.fetchUnit(unit.id, read()), lines: await api.fetchPurchaseLinesPage(purchase.id, {}, read()), history: await api.fetchPurchaseCorrectionsPage(purchase.id, {}, read()) }
  })
  expect(result.staleOriginal).toBe('REVISION_CONFLICT'); expect(result.staleReplacement).toBe('REVISION_CONFLICT')
  expect(result.freshOld.revision).toBe(result.oldItem.revision + 1); expect(result.freshReplacement.revision).toBe(result.replacement.revision + 1)
  expect(result.corrected).toMatchObject({ action: 'replace', before: { item_id: result.oldItem.id, quantity_base: 1500, paid_minor: 0 }, after: { item_id: result.replacement.id, item_unit_id: result.unit.id, quantity_base: 1, paid_minor: 500 } })
  expect(result.lines.data).toEqual([result.corrected.after]); expect(result.history.data).toEqual([result.corrected]); expect(result.physical).toEqual(result.unit)
  for (const key of [result.firstKey, result.secondKey, result.finalKey]) expect(writes.filter((request) => request.key === key)).toHaveLength(1)
})

test('reconciles genuinely committed altered correction replies only by explicit original keys after later changes', async ({ page, application }) => {
  await provision(page, application); await page.goto('/')
  const original = await page.evaluate(async () => {
    const modulePath = '/assets/financial-corrections-sdk.js', api = await import(modulePath)
    const csrf = await fetch('/api/v1/auth/csrf', { credentials: 'same-origin', cache: 'no-store' }); if (!csrf.ok) throw new Error('Financial session unavailable')
    const csrfToken: string = (await csrf.json()).token, root = () => ({ csrfToken, idempotencyKey: crypto.randomUUID(), signal: new AbortController().signal }), read = () => new AbortController().signal
    const item = await api.createItem({ name: 'Uncertain quantity', tracking_mode: 'quantity' }, root())
    const purchase = await api.createPurchase({ merchant_name: 'Uncertain merchant', purchased_at: '2026-11-07T00:00:00Z', currency: 'JPY' }, root())
    const line = await api.createPurchaseLine(purchase.id, { item_id: item.id, item_revision: item.revision, item_unit_id: null, quantity_base: 1, paid_minor: 0 }, { ...root(), purchase, item })
    const reviewedPurchase = await api.fetchPurchase(purchase.id, read()), reviewedItem = await api.fetchItem(item.id, read())
    const lineBody = { line_id: line.id, line_revision: line.revision, item_revision: reviewedItem.revision, action: 'replace', replacement: { item_id: reviewedItem.id, item_revision: reviewedItem.revision, item_unit_id: null, quantity_base: 2, paid_minor: 99 }, reason: 'Correct after review' }
    const owner = await api.createItem({ name: 'Uncertain physical Unit' }, root())
    const unit = await api.createUnit(owner.id, { location_id: null }, { ...root(), expectedRevision: owner.revision }), actualOwner = await api.fetchItem(owner.id, read())
    const profile = await api.putAssetProfile(unit.id, { item_revision: actualOwner.revision, asset_revision: null, cost_basis: 'manual', currency: 'JPY', manual_acquisition_minor: 100, in_service_at: null, retired_at: null, estimated_residual_minor: null, usage_unit: 'times' }, { ...root(), unit, item: actualOwner, asset: null })
    await api.disposeAsset(unit.id, { unit_revision: unit.revision, kind: 'sold', disposed_at: '2026-11-07T00:00:00Z', currency: 'JPY', realized_proceeds_minor: 200 }, { ...root(), asset: profile, unit, disposal: null })
    const disposal = await api.fetchDisposalRecord(unit.id, read()), asset = await api.fetchAsset(unit.id, read()), physical = await api.fetchUnit(unit.id, read())
    const disposalBody = { asset_revision: asset.revision, action: 'replace', replacement: { kind: 'sold', disposed_at: '2026-11-07T00:00:00Z', currency: 'JPY', realized_proceeds_minor: 0 }, reason: 'Correct to zero' }
    return { purchase: reviewedPurchase, item: reviewedItem, line, lineBody, lineKey: crypto.randomUUID(), disposal, asset, disposalBody, disposalKey: crypto.randomUUID(), physical }
  })
  const writes = watchDomainWrites(page, ['/api/v1/purchases', '/api/v1/units']), attempts = new Map<string, number>()
  await page.route((url) => url.pathname === `/api/v1/purchases/${original.purchase.id}/line-corrections` || url.pathname === `/api/v1/units/${original.disposal.item_unit_id}/disposal-corrections`, async (route) => {
    const request = route.request(), key = request.headers()['idempotency-key']
    if (request.method() !== 'POST' || key !== original.lineKey && key !== original.disposalKey) { await route.continue(); return }
    const response = await route.fetch(); expect(response.status()).toBe(200)
    const count = attempts.get(key) ?? 0; attempts.set(key, count + 1)
    if (count) { await route.fulfill({ response }); return }
    const body = await response.json()
    if (key === original.lineKey) body.after.paid_minor = 98
    else body.after.current.realized_proceeds_minor = 1
    await route.fulfill({ response, body: JSON.stringify(body) })
  })
  const unknown = await page.evaluate(async (source) => {
    const modulePath = '/assets/financial-corrections-sdk.js', api = await import(modulePath)
    const csrf = await fetch('/api/v1/auth/csrf', { credentials: 'same-origin', cache: 'no-store' }); if (!csrf.ok) throw new Error('Financial session unavailable')
    const csrfToken: string = (await csrf.json()).token, root = (idempotencyKey: string) => ({ csrfToken, idempotencyKey, signal: new AbortController().signal }), read = () => new AbortController().signal
    const failure = async (work: () => Promise<unknown>) => { try { await work(); return null } catch (error) { return error instanceof Error && 'code' in error ? error.code : 'unexpected' } }
    const purchaseError = await failure(() => api.correctPurchaseLine(source.purchase.id, source.lineBody, { ...root(source.lineKey), purchase: source.purchase, line: source.line, item: source.item, replacementItem: source.item }))
    const disposalError = await failure(() => api.correctDisposal(source.disposal.item_unit_id, source.disposalBody, { ...root(source.disposalKey), asset: source.asset, disposal: source.disposal }))
    window.dispatchEvent(new Event('online'))
    return { purchaseError, disposalError, lines: await api.fetchPurchaseLinesPage(source.purchase.id, {}, read()), disposal: await api.fetchDisposalRecord(source.disposal.item_unit_id, read()) }
  }, original)
  expect(unknown.purchaseError).toBe('INVALID_RESPONSE'); expect(unknown.disposalError).toBe('INVALID_RESPONSE')
  expect(unknown.lines.data).toHaveLength(1); expect(unknown.lines.data[0]).toMatchObject({ paid_minor: 99, quantity_base: 2 }); expect(unknown.disposal.current).toMatchObject({ realized_proceeds_minor: 0 })
  expect(writes.filter((request) => request.key === original.lineKey)).toHaveLength(1); expect(writes.filter((request) => request.key === original.disposalKey)).toHaveLength(1)
  const later = await page.evaluate(async (source) => {
    const modulePath = '/assets/financial-corrections-sdk.js', api = await import(modulePath)
    const csrf = await fetch('/api/v1/auth/csrf', { credentials: 'same-origin', cache: 'no-store' }); if (!csrf.ok) throw new Error('Financial session unavailable')
    const csrfToken: string = (await csrf.json()).token, root = (idempotencyKey = crypto.randomUUID()) => ({ csrfToken, idempotencyKey, signal: new AbortController().signal }), read = () => new AbortController().signal
    const lines = await api.fetchPurchaseLinesPage(source.purchase.id, {}, read()), line = lines.data[0]
    const purchase = await api.fetchPurchase(source.purchase.id, read()), item = await api.fetchItem(source.item.id, read())
    await api.correctPurchaseLine(source.purchase.id, { line_id: line.id, line_revision: line.revision, item_revision: item.revision, action: 'void', replacement: null, reason: 'Later separate decision' }, { ...root(), purchase, line, item, replacementItem: null })
    const record = await api.fetchDisposalRecord(source.disposal.item_unit_id, read()), asset = await api.fetchAsset(source.disposal.item_unit_id, read())
    await api.correctDisposal(record.item_unit_id, { asset_revision: asset.revision, action: 'void', replacement: null, reason: 'Later separate void' }, { ...root(), disposal: record, asset })
    const purchaseReplay = await api.correctPurchaseLine(source.purchase.id, source.lineBody, { ...root(source.lineKey), purchase: source.purchase, line: source.line, item: source.item, replacementItem: source.item })
    const disposalReplay = await api.correctDisposal(source.disposal.item_unit_id, source.disposalBody, { ...root(source.disposalKey), asset: source.asset, disposal: source.disposal })
    return { purchaseReplay, disposalReplay, lines: await api.fetchPurchaseLinesPage(source.purchase.id, {}, read()), disposal: await api.fetchDisposalRecord(source.disposal.item_unit_id, read()), physical: await api.fetchUnit(source.disposal.item_unit_id, read()),
      purchaseHistory: await api.fetchPurchaseCorrectionsPage(source.purchase.id, {}, read()), disposalHistory: await api.fetchDisposalCorrectionsPage(source.disposal.item_unit_id, {}, read()) }
  }, original)
  expect(later.purchaseReplay).toEqual(later.purchaseHistory.data.find((entry: { before: { id: string } }) => entry.before.id === original.line.id))
  expect(later.disposalReplay).toEqual(later.disposalHistory.data.find((entry: { before: { revision: number } }) => entry.before.revision === original.disposal.revision))
  expect(later.lines).toEqual({ data: [], next_cursor: null }); expect(later.disposal.current).toBeNull(); expect(later.physical).toEqual(original.physical)
  for (const key of [original.lineKey, original.disposalKey]) {
    const requests = writes.filter((request) => request.key === key); expect(requests).toHaveLength(2); expect(requests[1]).toEqual(requests[0]); expect(attempts.get(key)).toBe(2)
  }
})

test('withholds mixed Purchase correction sources when the real parent changes inside the selected history interval', async ({ page, application }) => {
  await provision(page, application); await page.goto('/')
  const initial = await page.evaluate(async () => {
    const modulePath = '/assets/financial-corrections-sdk.js', api = await import(modulePath)
    const csrf = await fetch('/api/v1/auth/csrf', { credentials: 'same-origin', cache: 'no-store' }); if (!csrf.ok) throw new Error('Financial session unavailable')
    const csrfToken: string = (await csrf.json()).token, root = () => ({ csrfToken, idempotencyKey: crypto.randomUUID(), signal: new AbortController().signal }), read = () => new AbortController().signal
    const item = await api.createItem({ name: 'Correction source quantity', tracking_mode: 'quantity', unit_code: 'kg', base_unit: 'g', scale_factor: 1000 }, root())
    const purchase = await api.createPurchase({ merchant_name: 'Correction source', purchased_at: '2026-11-07T00:00:00Z', currency: 'JPY' }, root())
    const line = await api.createPurchaseLine(purchase.id, { item_id: item.id, item_revision: item.revision, item_unit_id: null, quantity_base: 1500, paid_minor: 0 }, { ...root(), purchase, item })
    const parent = await api.fetchPurchase(purchase.id, read()), currentItem = await api.fetchItem(item.id, read())
    const entry = await api.correctPurchaseLine(purchase.id, { line_id: line.id, line_revision: line.revision, item_revision: currentItem.revision, action: 'replace', replacement: { item_id: item.id, item_revision: currentItem.revision, item_unit_id: null, quantity_base: 2500, paid_minor: 99 }, reason: 'Actual earlier correction' }, { ...root(), purchase: parent, line, item: currentItem, replacementItem: currentItem })
    if (!entry.after) throw new Error('Expected replacement source')
    const nextParent = await api.fetchPurchase(purchase.id, read()), nextItem = await api.fetchItem(item.id, read())
    const second = await api.correctPurchaseLine(purchase.id, { line_id: entry.after.id, line_revision: entry.after.revision, item_revision: nextItem.revision, action: 'replace', replacement: { item_id: item.id, item_revision: nextItem.revision, item_unit_id: null, quantity_base: 3000, paid_minor: 123 }, reason: 'Actual later correction' }, { ...root(), purchase: nextParent, line: entry.after, item: nextItem, replacementItem: nextItem })
    const target = await api.createItem({ name: 'Selected replacement Unit' }, root())
    const unit = await api.createUnit(target.id, { location_id: null }, { ...root(), expectedRevision: target.revision })
    return { purchase: await api.fetchPurchase(purchase.id, read()), item: await api.fetchItem(item.id, read()), target: await api.fetchItem(target.id, read()), unit, line, entry, second }
  })
  expect(initial.second.after).not.toBeNull()
  const reads: string[] = [], writes = watchDomainWrites(page, ['/api/v1/purchases', '/api/v1/units', '/api/v1/stock'])
  page.on('request', (request) => { if (request.method() === 'GET') reads.push(new URL(request.url()).pathname) })
  const reviewed = await page.evaluate(async (source) => {
    const modulePath = '/assets/financial-corrections-sdk.js', api = await import(modulePath), read = () => new AbortController().signal
    return { history: await api.readPurchaseCorrectionHistory(source.purchase.id, read()), selected: await api.readPurchaseCorrection(source.purchase.id, source.second.after.id, source.target.id, read()) }
  }, initial)
  expect(reviewed.history.lines).toEqual([initial.second.after]); expect(new Set(reviewed.history.corrections.map((entry: { id: string }) => entry.id))).toEqual(new Set([initial.entry.id, initial.second.id]))
  expect(reviewed.selected).toMatchObject({ purchase: initial.purchase, line: initial.second.after, item: initial.item, originalUnit: { kind: 'unallocated' }, replacement: { kind: 'individual', item: initial.target, units: [initial.unit] } })
  expect(reads).not.toContain('/api/v1/inventory'); expect(writes).toHaveLength(0)
  let changed = false
  await page.route((url) => url.pathname === `/api/v1/purchases/${initial.purchase.id}/line-corrections`, async (route) => {
    if (route.request().method() !== 'GET' || changed) { await route.continue(); return }
    changed = true; const response = await route.fetch(); expect(response.status()).toBe(200)
    const patch = await page.request.patch(`/api/v1/purchases/${initial.purchase.id}`, { headers: await writeContext(page, application.url, initial.purchase.revision), data: { receipt_note: 'Changed within reviewed interval' } })
    expect(patch.status()).toBe(200); await route.fulfill({ response })
  })
  const raced = await page.evaluate(async (id) => {
    const modulePath = '/assets/financial-corrections-sdk.js', api = await import(modulePath)
    try { return { value: await api.readPurchaseCorrectionHistory(id, new AbortController().signal) } }
    catch (error) { return { code: error instanceof Error && 'code' in error ? error.code : 'unexpected' } }
  }, initial.purchase.id)
  expect(changed).toBe(true); expect(raced).toEqual({ code: 'INVALID_RESPONSE' }); expect(writes).toHaveLength(0)
  const refreshed = await page.evaluate(async (id) => { const modulePath = '/assets/financial-corrections-sdk.js', api = await import(modulePath); return api.readPurchaseCorrectionHistory(id, new AbortController().signal) }, initial.purchase.id)
  expect(refreshed.purchase).toMatchObject({ revision: initial.purchase.revision + 1, receipt_note: 'Changed within reviewed interval' }); expect(new Set(refreshed.corrections.map((entry: { id: string }) => entry.id))).toEqual(new Set([initial.entry.id, initial.second.id]))
})

test('withholds mixed Disposal correction sources when a real financial void lands during history paging', async ({ page, application }) => {
  await provision(page, application); await page.goto('/')
  const initial = await page.evaluate(async () => {
    const modulePath = '/assets/financial-corrections-sdk.js', api = await import(modulePath)
    const csrf = await fetch('/api/v1/auth/csrf', { credentials: 'same-origin', cache: 'no-store' }); if (!csrf.ok) throw new Error('Financial session unavailable')
    const csrfToken: string = (await csrf.json()).token, root = () => ({ csrfToken, idempotencyKey: crypto.randomUUID(), signal: new AbortController().signal }), read = () => new AbortController().signal
    const owner = await api.createItem({ name: 'Correction source disposal' }, root())
    const unit = await api.createUnit(owner.id, { location_id: null, notes: 'Retained physical Unit' }, { ...root(), expectedRevision: owner.revision }), item = await api.fetchItem(owner.id, read())
    const asset = await api.putAssetProfile(unit.id, { item_revision: item.revision, asset_revision: null, cost_basis: 'manual', currency: 'JPY', manual_acquisition_minor: 100, in_service_at: null, retired_at: null, estimated_residual_minor: null, usage_unit: 'times' }, { ...root(), unit, item, asset: null })
    await api.disposeAsset(unit.id, { unit_revision: unit.revision, kind: 'sold', disposed_at: '2026-11-07T00:00:00Z', currency: 'JPY', realized_proceeds_minor: 200 }, { ...root(), asset, unit, disposal: null })
    const record = await api.fetchDisposalRecord(unit.id, read()), currentAsset = await api.fetchAsset(unit.id, read())
    const entry = await api.correctDisposal(unit.id, { asset_revision: currentAsset.revision, action: 'replace', replacement: { kind: 'sold', disposed_at: '2026-11-07T00:00:00Z', currency: 'JPY', realized_proceeds_minor: 0 }, reason: 'Actual zero decision' }, { ...root(), asset: currentAsset, disposal: record })
    return { record: await api.fetchDisposalRecord(unit.id, read()), asset: await api.fetchAsset(unit.id, read()), physical: await api.fetchUnit(unit.id, read()), entry }
  })
  const writes = watchDomainWrites(page, ['/api/v1/units', '/api/v1/stock'])
  const reviewed = await page.evaluate(async (id) => { const modulePath = '/assets/financial-corrections-sdk.js', api = await import(modulePath); return api.readDisposalCorrection(id, new AbortController().signal) }, initial.record.item_unit_id)
  expect(reviewed).toMatchObject({ unit: initial.physical, asset: initial.asset, disposal: initial.record, corrections: [initial.entry] }); expect(writes).toHaveLength(0)
  let changed = false
  await page.route((url) => url.pathname === `/api/v1/units/${initial.record.item_unit_id}/disposal-corrections`, async (route) => {
    if (route.request().method() !== 'GET' || changed) { await route.continue(); return }
    changed = true; const response = await route.fetch(); expect(response.status()).toBe(200)
    const correction = await page.request.post(`/api/v1/units/${initial.record.item_unit_id}/disposal-corrections`, { headers: await writeContext(page, application.url, initial.record.revision), data: { asset_revision: initial.asset.revision, action: 'void', replacement: null, reason: 'Changed within reviewed interval' } })
    expect(correction.status()).toBe(200); await route.fulfill({ response })
  })
  const raced = await page.evaluate(async (id) => {
    const modulePath = '/assets/financial-corrections-sdk.js', api = await import(modulePath)
    try { return { value: await api.readDisposalCorrection(id, new AbortController().signal) } }
    catch (error) { return { code: error instanceof Error && 'code' in error ? error.code : 'unexpected' } }
  }, initial.record.item_unit_id)
  expect(changed).toBe(true); expect(raced).toEqual({ code: 'INVALID_RESPONSE' }); expect(writes).toHaveLength(0)
  const refreshed = await page.evaluate(async (id) => { const modulePath = '/assets/financial-corrections-sdk.js', api = await import(modulePath); return api.readDisposalCorrection(id, new AbortController().signal) }, initial.record.item_unit_id)
  expect(refreshed.disposal).toMatchObject({ revision: initial.record.revision + 1, original: initial.record.original, current: null }); expect(refreshed.corrections).toHaveLength(2); expect(refreshed.corrections).toContainEqual(initial.entry); expect(refreshed.unit).toEqual(initial.physical)
})

test('reviews raw Purchase correction proposals and explicitly adopts a changed replacement Item against real Go', async ({ page, application }) => {
  await provision(page, application); await page.goto('/')
  const writes = watchDomainWrites(page, ['/api/v1/purchases', '/api/v1/units', '/api/v1/stock'])
  const result = await page.evaluate(async () => {
    const modulePath = '/assets/financial-corrections-sdk.js', api = await import(modulePath)
    const csrf = await fetch('/api/v1/auth/csrf', { credentials: 'same-origin', cache: 'no-store' }); if (!csrf.ok) throw new Error('Financial session unavailable')
    const csrfToken: string = (await csrf.json()).token, root = () => ({ csrfToken, idempotencyKey: crypto.randomUUID(), signal: new AbortController().signal }), read = () => new AbortController().signal
    const failure = async (work: () => unknown) => { try { await work(); return null } catch (error) { return error instanceof Error && 'code' in error ? error.code : 'unexpected' } }
    const old = await api.createItem({ name: 'Raw original quantity', tracking_mode: 'quantity', unit_code: 'kg', base_unit: 'g', scale_factor: 1000 }, root())
    const owner = await api.createItem({ name: 'Raw replacement Unit' }, root())
    const unit = await api.createUnit(owner.id, { location_id: null, notes: 'Physical state stays unchanged' }, { ...root(), expectedRevision: owner.revision })
    const target = await api.fetchItem(owner.id, read())
    const purchase = await api.createPurchase({ merchant_name: 'Raw correction', purchased_at: '2026-11-07T00:00:00Z', currency: 'JPY' }, root())
    const line = await api.createPurchaseLine(purchase.id, { item_id: old.id, item_revision: old.revision, item_unit_id: null, quantity_base: 1500, paid_minor: 0 }, { ...root(), purchase, item: old })
    const original = await api.readPurchaseCorrection(purchase.id, line.id, target.id, read())
    const defaults = api.purchaseCorrectionDefaults(), values = { ...defaults, action: 'replace', reason: 'Correct ownership', replacement_item_id: target.id, allocation: 'unit', item_unit_id: unit.id, quantity_base: '1', paid_minor: '0' }
    const empty = await failure(() => api.purchaseCorrectionProposal(defaults, original))
    const first = api.purchaseCorrectionProposal(values, original), staleContext = root()
    await api.patchItem(target.id, { notes: 'Changed after review' }, { ...root(), expectedRevision: target.revision })
    const stale = await failure(() => api.correctPurchaseLine(purchase.id, first.body, { ...staleContext, purchase: first.source.purchase, line: first.source.line, item: first.source.item, replacementItem: first.source.replacement.item }))
    const current = await api.readPurchaseCorrection(purchase.id, line.id, target.id, read())
    const adopted = api.adoptPurchaseCorrection({ ...values, paid_minor: '1e3' }, original, current)
    const invalid = await failure(() => api.purchaseCorrectionProposal(adopted.values, adopted.source))
    const reviewed = api.purchaseCorrectionProposal({ ...adopted.values, paid_minor: '0' }, adopted.source), send = root()
    const correction = await api.correctPurchaseLine(purchase.id, reviewed.body, { ...send, purchase: reviewed.source.purchase, line: reviewed.source.line, item: reviewed.source.item, replacementItem: reviewed.source.replacement.item })
    window.dispatchEvent(new Event('online'))
    return { defaults, empty, stale, invalid, adopted: adopted.values, oldTarget: target, newTarget: current.replacement.item, reviewed, correction, unit,
      physical: await api.fetchUnit(unit.id, read()), lines: await api.fetchPurchaseLinesPage(purchase.id, {}, read()), staleKey: staleContext.idempotencyKey, sentKey: send.idempotencyKey }
  })
  expect(result.defaults).toMatchObject({ action: '', allocation: '', quantity_base: '', paid_minor: '' }); expect(result.empty).toBe('VALIDATION_ERROR')
  expect(result.stale).toBe('REVISION_CONFLICT'); expect(result.invalid).toBe('VALIDATION_ERROR'); expect(result.adopted).toMatchObject({ paid_minor: '1e3', item_unit_id: result.unit.id, quantity_base: '1' })
  expect(result.newTarget.revision).toBe(result.oldTarget.revision + 1); expect(result.reviewed.body.replacement).toMatchObject({ item_id: result.newTarget.id, item_revision: result.newTarget.revision, item_unit_id: result.unit.id, quantity_base: 1, paid_minor: 0 })
  expect(result.correction).toMatchObject({ action: 'replace', before: { quantity_base: 1500, paid_minor: 0 }, after: { item_id: result.newTarget.id, item_unit_id: result.unit.id, quantity_base: 1, paid_minor: 0 } })
  expect(result.lines.data).toEqual([result.correction.after]); expect(result.physical).toEqual(result.unit)
  for (const key of [result.staleKey, result.sentKey]) expect(writes.filter((request) => request.key === key)).toHaveLength(1)
  expect(writes.filter((request) => request.path.startsWith('/api/v1/stock'))).toHaveLength(0)
})

test('reviews raw Disposal void and reinstatement proposals without changing the real physical Unit', async ({ page, application }) => {
  await provision(page, application); await page.goto('/')
  const writes = watchDomainWrites(page, ['/api/v1/units', '/api/v1/stock'])
  const result = await page.evaluate(async () => {
    const modulePath = '/assets/financial-corrections-sdk.js', api = await import(modulePath)
    const csrf = await fetch('/api/v1/auth/csrf', { credentials: 'same-origin', cache: 'no-store' }); if (!csrf.ok) throw new Error('Financial session unavailable')
    const csrfToken: string = (await csrf.json()).token, root = () => ({ csrfToken, idempotencyKey: crypto.randomUUID(), signal: new AbortController().signal }), read = () => new AbortController().signal
    const failure = async (work: () => unknown) => { try { await work(); return null } catch (error) { return error instanceof Error && 'code' in error ? error.code : 'unexpected' } }
    const owner = await api.createItem({ name: 'Raw disposal Unit' }, root())
    const unit = await api.createUnit(owner.id, { location_id: null }, { ...root(), expectedRevision: owner.revision }), item = await api.fetchItem(owner.id, read())
    const asset = await api.putAssetProfile(unit.id, { item_revision: item.revision, asset_revision: null, cost_basis: 'manual', currency: 'JPY', manual_acquisition_minor: 100, in_service_at: null, retired_at: null, estimated_residual_minor: null, usage_unit: 'times' }, { ...root(), unit, item, asset: null })
    const posted = await api.disposeAsset(unit.id, { unit_revision: unit.revision, kind: 'sold', disposed_at: '2026-11-07T12:00:00.123456789+08:00', currency: 'JPY', realized_proceeds_minor: 200 }, { ...root(), asset, unit, disposal: null })
    const original = await api.readDisposalCorrection(unit.id, read()), defaults = api.disposalCorrectionDefaults(original)
    const empty = await failure(() => api.disposalCorrectionProposal(defaults, original))
    const voidValues = { ...defaults, action: 'void', reason: 'Void financial decision', realized_proceeds_minor: 'unparsed draft' }
    const reviewVoid = api.disposalCorrectionProposal(voidValues, original), voidContext = root()
    const voidResult = await api.correctDisposal(unit.id, reviewVoid.body, { ...voidContext, asset: reviewVoid.source.asset, disposal: reviewVoid.source.disposal })
    const current = await api.readDisposalCorrection(unit.id, read())
    const doubleVoid = await failure(() => api.disposalCorrectionProposal(voidValues, current))
    const raw = { ...defaults, action: 'replace', reason: 'Explicit reinstatement', kind: 'sold', realized_proceeds_minor: '', disposed_at: '2026-11-07T04:00:00.123456789Z' }
    const missingZero = await failure(() => api.disposalCorrectionProposal(raw, current))
    const adopted = api.adoptDisposalCorrection({ ...raw, realized_proceeds_minor: '1e3' }, original, current)
    const invalid = await failure(() => api.disposalCorrectionProposal(adopted.values, adopted.source))
    const reviewReplace = api.disposalCorrectionProposal({ ...adopted.values, realized_proceeds_minor: '0' }, adopted.source), replaceContext = root()
    const replacement = await api.correctDisposal(unit.id, reviewReplace.body, { ...replaceContext, asset: reviewReplace.source.asset, disposal: reviewReplace.source.disposal })
    window.dispatchEvent(new Event('online'))
    return { defaults, empty, posted, original, voidResult, current, doubleVoid, missingZero, adopted: adopted.values, invalid, reviewReplace, replacement,
      finalRecord: await api.fetchDisposalRecord(unit.id, read()), physical: await api.fetchUnit(unit.id, read()), voidKey: voidContext.idempotencyKey, replaceKey: replaceContext.idempotencyKey }
  })
  expect(result.defaults).toMatchObject({ action: '', realized_proceeds_minor: '', disposed_at: '', currency: 'JPY' }); expect(result.empty).toBe('VALIDATION_ERROR')
  expect(result.voidResult.after.current).toBeNull(); expect(result.current.disposal).toMatchObject({ current: null, original: result.original.disposal.original })
  expect(result.doubleVoid).toBe('ASSET_HISTORY_LOCKED'); expect(result.missingZero).toBe('VALIDATION_ERROR'); expect(result.invalid).toBe('VALIDATION_ERROR'); expect(result.adopted.realized_proceeds_minor).toBe('1e3')
  expect(result.reviewReplace.body).toMatchObject({ asset_revision: result.current.asset.revision, action: 'replace', replacement: { realized_proceeds_minor: 0 } })
  expect(result.replacement.after.current).toMatchObject({ realized_proceeds_minor: 0, disposed_at: result.posted.disposed_at }); expect(result.finalRecord).toEqual(result.replacement.after)
  expect(result.physical).toEqual(result.original.unit); expect(result.physical.status).toBe('disposed')
  for (const key of [result.voidKey, result.replaceKey]) {
    const requests = writes.filter((request) => request.key === key); expect(requests).toHaveLength(1)
    expect(JSON.parse(requests[0]!.body!)).not.toHaveProperty('unit_revision')
  }
  expect(writes.filter((request) => request.path.startsWith('/api/v1/stock'))).toHaveLength(0)
})
