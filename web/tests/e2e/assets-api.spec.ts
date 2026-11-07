import { cpSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { build } from 'vite'
import { test as applicationTest, expect } from './auth-fixture.ts'
import { provision, watchDomainWrites, writeContext } from './domain-helpers.ts'

const test = applicationTest.extend<{}, { applicationWebDirectory: string }>({
  applicationWebDirectory: [async ({ browserName }, provideFixture) => {
    const directory = mkdtempSync(join(tmpdir(), `homebox-assets-sdk-${browserName}-`))
    const web = join(directory, 'web'), sdk = join(directory, 'sdk'), entry = join(directory, 'entry.ts')
    try {
      cpSync(fileURLToPath(new URL('../../dist', import.meta.url)), web, { recursive: true })
      // Bundle actual modules privately; no production test entry or fake service.
      writeFileSync(entry, `export * from ${JSON.stringify(fileURLToPath(new URL('../../src/lib/api/assets.ts', import.meta.url)))};\nexport {createItem,fetchItem,patchItem} from ${JSON.stringify(fileURLToPath(new URL('../../src/lib/api/catalog.ts', import.meta.url)))};\nexport {createUnit,fetchUnit,patchUnit} from ${JSON.stringify(fileURLToPath(new URL('../../src/lib/api/holdings.ts', import.meta.url)))};\nexport {createPurchase,createPurchaseLine} from ${JSON.stringify(fileURLToPath(new URL('../../src/lib/api/purchases.ts', import.meta.url)))};\nexport * from ${JSON.stringify(fileURLToPath(new URL('../../src/features/assets/model.ts', import.meta.url)))};\nexport * from ${JSON.stringify(fileURLToPath(new URL('../../src/features/assets/form.ts', import.meta.url)))};\n`)
      await build({ configFile: false, logLevel: 'silent', build: { outDir: sdk, minify: false, lib: { entry, formats: ['es'], fileName: 'assets-sdk' } } })
      cpSync(join(sdk, 'assets-sdk.js'), join(web, 'assets', 'assets-sdk.js'))
      await provideFixture(web)
    } finally { rmSync(directory, { recursive: true, force: true }) }
  }, { scope: 'worker' }],
})

test.use({ trace: 'off', screenshot: 'off', video: 'off' })

test('drives all eleven financial adapters with original contexts, complete pages and fresh-login frozen replies', async ({ page, application }) => {
  const replacement = await provision(page, application)
  const writes = watchDomainWrites(page, ['/api/v1/units'])
  await page.goto('/')
  const result = await page.evaluate(async () => {
    const modulePath = '/assets/assets-sdk.js'
    const api = await import(modulePath)
    const csrf = await fetch('/api/v1/auth/csrf', { credentials: 'same-origin', cache: 'no-store' })
    if (!csrf.ok) throw new Error('Asset SDK session unavailable')
    const csrfToken: string = (await csrf.json()).token
    const root = () => ({ csrfToken, idempotencyKey: crypto.randomUUID(), signal: new AbortController().signal })
    const read = () => new AbortController().signal
    const failureCode = async (work: () => Promise<unknown>) => { try { await work(); return null } catch (error) { return error instanceof Error && 'code' in error ? error.code : 'unexpected' } }
    const owner = await api.createItem({ name: 'Actual Asset SDK owner' }, root())
    const unit = await api.createUnit(owner.id, { location_id: null, serial_number: '', notes: 'Original physical source' }, { ...root(), expectedRevision: owner.revision })
    const item = await api.fetchItem(owner.id, read())
    const absent = await failureCode(() => api.fetchAsset(unit.id, read()))
    const profileBody = { item_revision: item.revision, asset_revision: null, cost_basis: 'manual', currency: 'JPY', manual_acquisition_minor: 100,
      in_service_at: '2024-11-07T12:00:00+08:00', retired_at: null, estimated_residual_minor: 20, usage_unit: 'times' }
    const profileCommand = { ...root(), item, unit, asset: null }
    const profile = await api.putAssetProfile(unit.id, profileBody, profileCommand)
    const initial = await api.fetchAsset(unit.id, read())
    const emptyUsage = await api.fetchAssetUsagePage(unit.id, {}, read())
    const emptyCosts = await api.fetchAssetCostsPage(unit.id, {}, read())
    const changedItem = await api.fetchItem(item.id, read())
    const correctedProfile = await api.putAssetProfile(unit.id, { ...profileBody, item_revision: changedItem.revision, asset_revision: profile.revision, estimated_residual_minor: 30 }, { ...root(), unit, item: changedItem, asset: profile })
    const useBody = { delta: 3, usage_unit: 'times', event_type: 'use', occurred_at: '2026-11-07T12:00:00.123456789+08:00', note: '\0Exact use é\n' }
    const useCommand = { ...root(), asset: correctedProfile }
    const use = await api.recordAssetUsage(unit.id, useBody, useCommand)
    const afterUse = await api.fetchAsset(unit.id, read())
    const correction = await api.recordAssetUsage(unit.id, { delta: -1, usage_unit: 'times', event_type: 'correction', occurred_at: useBody.occurred_at, correction_of: use.id }, { ...root(), asset: afterUse })
    const afterCorrection = await api.fetchAsset(unit.id, read())
    const costBody = { amount_minor: 5, currency: 'JPY', kind: 'repair', direction: 'expense', occurred_at: useBody.occurred_at }
    const costCommand = { ...root(), asset: afterCorrection }
    const charge = await api.recordAssetCost(unit.id, costBody, costCommand)
    const afterCharge = await api.fetchAsset(unit.id, read())
    const adjustment = await api.recordAssetCost(unit.id, { amount_minor: 2, currency: 'JPY', kind: 'adjustment', direction: 'credit', occurred_at: useBody.occurred_at, correction_of: charge.id }, { ...root(), asset: afterCharge })
    const beforeRetirement = await api.fetchAsset(unit.id, read())
    const firstUse = await api.fetchAssetUsagePage(unit.id, { limit: 1 }, read())
    const firstCost = await api.fetchAssetCostsPage(unit.id, { limit: 1 }, read())
    if (!firstUse.next_cursor || !firstCost.next_cursor) throw new Error('Expected real independent history continuations')
    const lastUse = await api.fetchAssetUsagePage(unit.id, { limit: 1, cursor: firstUse.next_cursor }, read())
    const lastCost = await api.fetchAssetCostsPage(unit.id, { limit: 1, cursor: firstCost.next_cursor }, read())
    const crossCollection = await failureCode(() => api.fetchAssetCostsPage(unit.id, { limit: 1, cursor: firstUse.next_cursor }, read()))
    const retireBody = { unit_revision: unit.revision, retired_at: useBody.occurred_at, note: 'Explicit retirement\n' }
    const retireCommand = { ...root(), asset: beforeRetirement, unit }
    const retired = await api.retireAsset(unit.id, retireBody, retireCommand)
    const beforeDisposal = await api.fetchAsset(unit.id, read())
    const disposeBody = { unit_revision: retired.revision, kind: 'sold', disposed_at: useBody.occurred_at, currency: 'JPY', realized_proceeds_minor: 200, note: 'Explicit proceeds\n' }
    const disposeCommand = { ...root(), asset: beforeDisposal, unit: retired, disposal: null }
    const disposal = await api.disposeAsset(unit.id, disposeBody, disposeCommand)
    const recorded = await api.fetchAssetDisposal(unit.id, read())
    const physical = await api.fetchUnit(unit.id, read())
    const summary = await api.fetchAssetCostSummary(unit.id, { as_of: '2026-11-08T12:00:00.123456789+08:00' }, read())
    const current = await api.fetchAsset(unit.id, read())
    const nextItem = await api.fetchItem(item.id, read())
    const sibling = await api.createUnit(item.id, { location_id: null }, { ...root(), expectedRevision: nextItem.revision })
    const siblingItem = await api.fetchItem(item.id, read())
    const second = await api.putAssetProfile(sibling.id, { ...profileBody, item_revision: siblingItem.revision, cost_basis: 'purchase', manual_acquisition_minor: null, in_service_at: null, estimated_residual_minor: null }, { ...root(), unit: sibling, item: siblingItem, asset: null })
    const first = await api.fetchAssetsPage({ limit: 1 }, read())
    if (!first.next_cursor) throw new Error('Expected real global Asset continuation')
    const last = await api.fetchAssetsPage({ limit: 1, cursor: first.next_cursor }, read())
    const changedSize = await failureCode(() => api.fetchAssetsPage({ limit: 2, cursor: first.next_cursor }, read()))
    return { unit, item, absent, profile, initial, emptyUsage, emptyCosts, correctedProfile, use, correction, charge, adjustment,
      firstUse, lastUse, firstCost, lastCost, crossCollection, retired, disposal, recorded, physical, summary, current, second, first, last, changedSize,
      original: { unit, item, profileBody, profileKey: profileCommand.idempotencyKey, useBody, useKey: useCommand.idempotencyKey, useAsset: correctedProfile,
        costBody, costKey: costCommand.idempotencyKey, costAsset: afterCorrection, retireBody, retireKey: retireCommand.idempotencyKey, retireAsset: beforeRetirement,
        disposeBody, disposeKey: disposeCommand.idempotencyKey, disposeAsset: beforeDisposal, retired } }
  })
  expect(result.absent).toBe('NOT_FOUND'); expect(result.profile).toEqual(result.initial)
  expect(result.profile).toMatchObject({ revision: 1, cost_basis: 'manual', manual_acquisition_minor: 100, in_service_at: '2024-11-07T04:00:00.000000000Z' })
  expect(result.emptyUsage).toEqual({ data: [], next_cursor: null }); expect(result.emptyCosts).toEqual({ data: [], next_cursor: null })
  expect(result.correctedProfile).toMatchObject({ revision: 2, estimated_residual_minor: 30 })
  expect(result.use).toMatchObject({ revision: 1, delta: 3, occurred_at: '2026-11-07T04:00:00.123456789Z', note: '\0Exact use é\n', correction_of: null })
  expect(result.correction).toMatchObject({ revision: 1, delta: -1, correction_of: result.use.id })
  expect(result.charge).toMatchObject({ revision: 1, amount_minor: 5, direction: 'expense' }); expect(result.adjustment).toMatchObject({ revision: 1, amount_minor: 2, direction: 'credit', correction_of: result.charge.id })
  expect(result.lastUse.next_cursor).toBeNull(); expect(result.lastCost.next_cursor).toBeNull()
  expect(new Set([...result.firstUse.data, ...result.lastUse.data].map((event: { id: string }) => event.id))).toEqual(new Set([result.use.id, result.correction.id]))
  expect(new Set([...result.firstCost.data, ...result.lastCost.data].map((event: { id: string }) => event.id))).toEqual(new Set([result.charge.id, result.adjustment.id]))
  expect(result.crossCollection).toBe('INVALID_CURSOR'); expect(result.changedSize).toBe('INVALID_CURSOR')
  expect(result.retired).toEqual({ ...result.unit, revision: 2, status: 'retired' })
  expect(result.disposal).toEqual(result.recorded); expect(result.disposal).toMatchObject({ revision: 1, realized_proceeds_minor: 200, note: 'Explicit proceeds\n' })
  expect(result.physical).toEqual({ ...result.unit, revision: 3, status: 'disposed' })
  expect(result.current).toMatchObject({ revision: 8, retired_at: '2026-11-07T04:00:00.123456789Z' })
  expect(result.summary).toMatchObject({ acquisition_minor: 100, extra_minor: 3, realized_proceeds_minor: 200, estimated_residual_minor: 30, net_cost_minor: -97,
    projected_net_cost_minor: 73, elapsed_days: 731, total_usage: 2, as_of: '2026-11-08T04:00:00.123456789Z', timezone: 'UTC', day_policy: 'inclusive_civil_days' })
  expect(result.last.next_cursor).toBeNull()
  expect(new Set([...result.first.data, ...result.last.data].map((asset: { item_unit_id: string }) => asset.item_unit_id))).toEqual(new Set([result.unit.id, result.second.item_unit_id]))

  const logout = await writeContext(page, application.url)
  expect((await page.request.post('/api/v1/auth/logout', { headers: logout })).status()).toBe(200)
  expect((await page.request.post('/api/v1/auth/login', { headers: { Origin: application.url }, data: { username: 'owner', password: replacement } })).status()).toBe(200)
  const reconciled = await page.evaluate(async (original) => {
    const modulePath = '/assets/assets-sdk.js'
    const api = await import(modulePath)
    const csrf = await fetch('/api/v1/auth/csrf', { credentials: 'same-origin', cache: 'no-store' })
    if (!csrf.ok) throw new Error('Fresh Asset SDK session unavailable')
    const csrfToken: string = (await csrf.json()).token
    const root = (idempotencyKey: string) => ({ csrfToken, idempotencyKey, signal: new AbortController().signal })
    return { profile: await api.putAssetProfile(original.unit.id, original.profileBody, { ...root(original.profileKey), unit: original.unit, item: original.item, asset: null }),
      use: await api.recordAssetUsage(original.unit.id, original.useBody, { ...root(original.useKey), asset: original.useAsset }),
      cost: await api.recordAssetCost(original.unit.id, original.costBody, { ...root(original.costKey), asset: original.costAsset }),
      retire: await api.retireAsset(original.unit.id, original.retireBody, { ...root(original.retireKey), asset: original.retireAsset, unit: original.unit }),
      disposal: await api.disposeAsset(original.unit.id, original.disposeBody, { ...root(original.disposeKey), asset: original.disposeAsset, unit: original.retired, disposal: null }),
      current: await api.fetchAsset(original.unit.id, new AbortController().signal) }
  }, result.original)
  expect(reconciled).toEqual({ profile: result.profile, use: result.use, cost: result.charge, retire: result.retired, disposal: result.disposal, current: result.current })
  for (const key of [result.original.profileKey, result.original.useKey, result.original.costKey, result.original.retireKey, result.original.disposeKey]) {
    const attempts = writes.filter((request) => request.key === key)
    expect(attempts).toHaveLength(2); expect(attempts[1]).toEqual(attempts[0])
  }
  expect(writes.find((request) => request.key === result.original.profileKey)?.proof).toBe('"1"')
  expect(writes.find((request) => request.key === result.original.useKey)?.proof).toBe('"2"')
  expect(writes.find((request) => request.key === result.original.retireKey)?.proof).toBe('"6"')
})

test('rejects genuine profile/purchase/physical proof races and accepts unchanged financial interpretation without another Unit proof', async ({ page, application }) => {
  await provision(page, application)
  await page.goto('/')
  const result = await page.evaluate(async () => {
    const modulePath = '/assets/assets-sdk.js'
    const api = await import(modulePath)
    const csrf = await fetch('/api/v1/auth/csrf', { credentials: 'same-origin', cache: 'no-store' })
    const csrfToken: string = (await csrf.json()).token
    const root = () => ({ csrfToken, idempotencyKey: crypto.randomUUID(), signal: new AbortController().signal })
    const read = () => new AbortController().signal
    const failureCode = async (work: () => Promise<unknown>) => { try { await work(); return null } catch (error) { return error instanceof Error && 'code' in error ? error.code : 'unexpected' } }
    const owner = await api.createItem({ name: 'Reviewed acquisition' }, root())
    const unit = await api.createUnit(owner.id, { location_id: null }, { ...root(), expectedRevision: owner.revision })
    const item = await api.fetchItem(owner.id, read())
    const body = { item_revision: item.revision, asset_revision: null, cost_basis: 'purchase', currency: 'JPY', manual_acquisition_minor: null,
      in_service_at: null, retired_at: null, estimated_residual_minor: null, usage_unit: 'times' }
    const unsupported = await failureCode(() => api.putAssetProfile(unit.id, { ...body, currency: 'XAU' }, { ...root(), unit, item, asset: null }))
    const profile = await api.putAssetProfile(unit.id, body, { ...root(), unit, item, asset: null })
    const purchase = await api.createPurchase({ merchant_name: 'Exact\nacquisition é', purchased_at: '2026-11-07T00:00:00Z', currency: 'JPY' }, root())
    const beforeLine = await api.fetchItem(item.id, read())
    const line = await api.createPurchaseLine(purchase.id, { item_id: item.id, item_revision: beforeLine.revision, item_unit_id: unit.id, quantity_base: 1, paid_minor: 0 }, { ...root(), item: beforeLine, purchase })
    const acquired = await api.fetchAsset(unit.id, read())
    const staleProfile = await failureCode(() => api.putAssetProfile(unit.id, body, { ...root(), item, unit, asset: null }))
    const useBody = { delta: 1, usage_unit: 'times', event_type: 'use', occurred_at: '2026-11-07T00:00:00.123456789Z' }
    const staleUse = await failureCode(() => api.recordAssetUsage(unit.id, useBody, { ...root(), asset: profile }))
    const staleCost = await failureCode(() => api.recordAssetCost(unit.id, { amount_minor: 1, currency: 'JPY', kind: 'repair', direction: 'expense', occurred_at: useBody.occurred_at }, { ...root(), asset: profile }))
    const moved = await api.patchUnit(unit.id, { notes: 'Concurrent physical source' }, { ...root(), expectedRevision: unit.revision, itemID: item.id })
    const staleRetire = await failureCode(() => api.retireAsset(unit.id, { unit_revision: unit.revision, retired_at: useBody.occurred_at }, { ...root(), asset: acquired, unit }))
    const staleDisposal = await failureCode(() => api.disposeAsset(unit.id, { unit_revision: unit.revision, kind: 'other', disposed_at: useBody.occurred_at, currency: 'JPY', realized_proceeds_minor: 0 }, { ...root(), asset: acquired, unit, disposal: null }))
    const use = await api.recordAssetUsage(unit.id, useBody, { ...root(), asset: acquired })
    const current = await api.fetchAsset(unit.id, read())
    const physical = await api.fetchUnit(unit.id, read())
    const page = await api.fetchAssetsPage({ from: '2026-11-07', to: '2026-11-07', currency: 'JPY', merchant: 'Exact\nacquisition é', status: 'active' }, read())
    const summary = await api.fetchAssetCostSummary(unit.id, {}, read())
    return { unsupported, profile, line, acquired, staleProfile, staleUse, staleCost, moved, staleRetire, staleDisposal, use, current, physical, page, summary }
  })
  expect(result.unsupported).toBe('ASSET_CURRENCY_CONFLICT'); expect(result.profile.revision).toBe(1)
  expect(result.line).toMatchObject({ revision: 1, quantity_base: 1, paid_minor: 0 }); expect(result.acquired.revision).toBe(2)
  for (const code of [result.staleProfile, result.staleUse, result.staleCost, result.staleRetire, result.staleDisposal]) expect(code).toBe('REVISION_CONFLICT')
  expect(result.use).toMatchObject({ revision: 1, delta: 1 }); expect(result.current.revision).toBe(3)
  expect(result.physical).toEqual(result.moved); expect(result.physical).toMatchObject({ revision: 2, status: 'stored', notes: 'Concurrent physical source' })
  expect(result.page).toEqual({ data: [result.current], next_cursor: null })
  expect(result.summary).toMatchObject({ acquisition_minor: 0, extra_minor: 0, net_cost_minor: 0, estimated_residual_minor: null, projected_net_cost_minor: null,
    elapsed_days: null, total_usage: 1, per_day_minor: null, per_use_minor: 0, projected_per_day_minor: null, projected_per_use_minor: null })
})

for (const kind of ['profile', 'usage', 'cost', 'retire', 'disposal'] as const) {
  for (const failure of ['altered-promised-field', 'lost-reply'] as const) {
    test(`reconciles a genuinely committed ${failure} ${kind} only with its complete original command`, async ({ page, application }) => {
      await provision(page, application)
      const writes = watchDomainWrites(page, ['/api/v1/units'])
      const tail = { profile: 'asset', usage: 'usage', cost: 'costs', retire: 'retire', disposal: 'disposal' }[kind]
      let attempts = 0
      await page.route((url) => new RegExp(`^/api/v1/units/[^/]+/${tail}$`).test(url.pathname), async (route) => {
        if (route.request().method() !== (kind === 'profile' ? 'PUT' : 'POST')) { await route.continue(); return }
        const response = await route.fetch()
        expect(response.status()).toBe(200)
        if (++attempts !== 1) { await route.fulfill({ response }); return }
        if (failure === 'lost-reply') { await route.abort('failed'); return }
        const source = await response.json()
        const changed = kind === 'profile' ? { manual_acquisition_minor: source.manual_acquisition_minor + 1 } : kind === 'usage' ? { delta: source.delta + 1 } :
          kind === 'cost' ? { direction: 'credit' } : kind === 'retire' ? { location_id: 'wrong-placement' } : { realized_proceeds_minor: source.realized_proceeds_minor + 1 }
        await route.fulfill({ response, body: JSON.stringify({ ...source, ...changed }) })
      })
      await page.goto('/')
      const result = await page.evaluate(async ({ kind }) => {
        const modulePath = '/assets/assets-sdk.js'
        const api = await import(modulePath)
        const csrf = await fetch('/api/v1/auth/csrf', { credentials: 'same-origin', cache: 'no-store' })
        const csrfToken: string = (await csrf.json()).token
        const root = () => ({ csrfToken, idempotencyKey: crypto.randomUUID(), signal: new AbortController().signal })
        const read = () => new AbortController().signal
        const owner = await api.createItem({ name: 'Unknown financial result' }, root())
        const unit = await api.createUnit(owner.id, { location_id: null, notes: 'Original physical source' }, { ...root(), expectedRevision: owner.revision })
        const item = await api.fetchItem(owner.id, read())
        const profileBody = { item_revision: item.revision, asset_revision: null, cost_basis: 'manual', currency: 'JPY', manual_acquisition_minor: 100,
          in_service_at: null, retired_at: null, estimated_residual_minor: null, usage_unit: 'times' }
        const asset = kind === 'profile' ? null : await api.putAssetProfile(unit.id, profileBody, { ...root(), unit, item, asset: null })
        const instant = '2026-11-07T04:00:00.123456789Z'
        const body = kind === 'profile' ? profileBody : kind === 'usage' ? { delta: 1, usage_unit: 'times', event_type: 'use', occurred_at: instant } :
          kind === 'cost' ? { amount_minor: 5, currency: 'JPY', kind: 'repair', direction: 'expense', occurred_at: instant } :
          kind === 'retire' ? { unit_revision: unit.revision, retired_at: instant } : { unit_revision: unit.revision, kind: 'sold', disposed_at: instant, currency: 'JPY', realized_proceeds_minor: 20 }
        const command = { ...root(), unit, item, asset, disposal: null }
        const invoke = () => kind === 'profile' ? api.putAssetProfile(unit.id, body, command) : kind === 'usage' ? api.recordAssetUsage(unit.id, body, command) :
          kind === 'cost' ? api.recordAssetCost(unit.id, body, command) : kind === 'retire' ? api.retireAsset(unit.id, body, command) : api.disposeAsset(unit.id, body, command)
        let code = null
        try { await invoke() } catch (error) { code = error instanceof Error && 'code' in error ? error.code : 'unexpected' }
        const committed = kind === 'profile' ? await api.fetchAsset(unit.id, read()) : kind === 'usage' ? (await api.fetchAssetUsagePage(unit.id, {}, read())).data[0] :
          kind === 'cost' ? (await api.fetchAssetCostsPage(unit.id, {}, read())).data[0] : kind === 'retire' ? await api.fetchUnit(unit.id, read()) : await api.fetchAssetDisposal(unit.id, read())
        if (kind === 'retire' || kind === 'disposal') {
          const physical = await api.fetchUnit(unit.id, read())
          await api.patchUnit(unit.id, { notes: 'Later current physical source' }, { ...root(), expectedRevision: physical.revision, itemID: item.id })
        } else {
          const financial = await api.fetchAsset(unit.id, read())
          if (kind === 'usage') await api.recordAssetCost(unit.id, { amount_minor: 1, currency: 'JPY', kind: 'fee', direction: 'expense', occurred_at: instant }, { ...root(), asset: financial })
          else await api.recordAssetUsage(unit.id, { delta: 1, usage_unit: 'times', event_type: 'use', occurred_at: instant }, { ...root(), asset: financial })
        }
        window.dispatchEvent(new Event('online'))
        return { code, committed, current: await api.fetchAsset(unit.id, read()), physical: await api.fetchUnit(unit.id, read()),
          original: { kind, unit, item, asset, body, key: command.idempotencyKey } }
      }, { kind })
      expect(result.code).toBe(failure === 'lost-reply' ? 'NETWORK_ERROR' : 'INVALID_RESPONSE')
      expect(attempts).toBe(1)
      const reconciled = await page.evaluate(async (original) => {
        const modulePath = '/assets/assets-sdk.js'
        const api = await import(modulePath)
        const csrf = await fetch('/api/v1/auth/csrf', { credentials: 'same-origin', cache: 'no-store' })
        const csrfToken: string = (await csrf.json()).token
        const context = { csrfToken, idempotencyKey: original.key, signal: new AbortController().signal, unit: original.unit, item: original.item, asset: original.asset, disposal: null }
        const value = original.kind === 'profile' ? await api.putAssetProfile(original.unit.id, original.body, context) : original.kind === 'usage' ? await api.recordAssetUsage(original.unit.id, original.body, context) :
          original.kind === 'cost' ? await api.recordAssetCost(original.unit.id, original.body, context) : original.kind === 'retire' ? await api.retireAsset(original.unit.id, original.body, context) : await api.disposeAsset(original.unit.id, original.body, context)
        const read = () => new AbortController().signal
        return { value, current: await api.fetchAsset(original.unit.id, read()), physical: await api.fetchUnit(original.unit.id, read()),
          usage: await api.fetchAssetUsagePage(original.unit.id, {}, read()), costs: await api.fetchAssetCostsPage(original.unit.id, {}, read()) }
      }, result.original)
      expect(attempts).toBe(2); expect(reconciled.value).toEqual(result.committed)
      expect(reconciled.current).toEqual(result.current); expect(reconciled.physical).toEqual(result.physical)
      if (kind === 'usage') expect(reconciled.usage.data).toEqual([result.committed])
      if (kind === 'cost') expect(reconciled.costs.data).toEqual([result.committed])
      if (kind === 'retire' || kind === 'disposal') expect(reconciled.physical.notes).toBe('Later current physical source')
      const originalRequests = writes.filter((request) => request.key === result.original.key)
      expect(originalRequests).toHaveLength(2); expect(originalRequests[1]).toEqual(originalRequests[0])
    })
  }
}

test('reads coherent profile, selected history, lifecycle and exact detail sources without sibling or quantity authority', async ({ page, application }) => {
  await provision(page, application)
  const readPaths: string[] = []
  page.on('request', (request) => { if (request.method() === 'GET') readPaths.push(new URL(request.url()).pathname) })
  await page.goto('/')
  const result = await page.evaluate(async () => {
    const modulePath = '/assets/assets-sdk.js'
    const api = await import(modulePath)
    const response = await fetch('/api/v1/auth/csrf', { credentials: 'same-origin', cache: 'no-store' })
    const csrfToken: string = (await response.json()).token
    const root = () => ({ csrfToken, idempotencyKey: crypto.randomUUID(), signal: new AbortController().signal })
    const read = () => new AbortController().signal
    const owner = await api.createItem({ name: 'Coherent financial source' }, root())
    const unit = await api.createUnit(owner.id, { location_id: null }, { ...root(), expectedRevision: owner.revision })
    const item = await api.fetchItem(owner.id, read())
    const profile = await api.putAssetProfile(unit.id, { item_revision: item.revision, asset_revision: null, cost_basis: 'manual', currency: 'JPY', manual_acquisition_minor: 100,
      in_service_at: null, retired_at: null, estimated_residual_minor: null, usage_unit: 'times' }, { ...root(), item, unit, asset: null })
    const usage = await api.recordAssetUsage(unit.id, { delta: 1, usage_unit: 'times', event_type: 'use', occurred_at: '2026-11-07T00:00:00.123456789Z' }, { ...root(), asset: profile })
    const afterUse = await api.fetchAsset(unit.id, read())
    const cost = await api.recordAssetCost(unit.id, { amount_minor: 1, currency: 'JPY', kind: 'repair', direction: 'expense', occurred_at: '2026-11-07T00:00:00.123456789Z' }, { ...root(), asset: afterUse })
    return { unit, usage, cost, item: await api.fetchItem(item.id, read()), asset: await api.fetchAsset(unit.id, read()),
      profile: await api.readAssetProfile(unit.id, read()), uses: await api.readAssetUsage(unit.id, read()), costs: await api.readAssetCosts(unit.id, read()),
      lifecycle: await api.readAssetLifecycle(unit.id, read()), detail: await api.readAssetDetail(unit.id, { as_of: '2026-11-07T12:00:00.123456789+08:00' }, read()) }
  })
  for (const source of [result.profile, result.uses, result.costs, result.lifecycle, result.detail]) {
    expect(source.unit).toEqual(result.unit); expect(source.item).toEqual(result.item); expect(source.asset).toEqual(result.asset)
  }
  expect(result.uses.usage).toEqual([result.usage]); expect(Object.hasOwn(result.uses, 'costs')).toBe(false)
  expect(result.costs.costs).toEqual([result.cost]); expect(Object.hasOwn(result.costs, 'usage')).toBe(false)
  expect(result.lifecycle.disposal).toBeNull(); expect(result.detail.disposal).toBeNull()
  expect(result.detail.summary).toMatchObject({ acquisition_minor: 100, extra_minor: 1, net_cost_minor: 101, total_usage: 1, elapsed_days: null,
    projected_net_cost_minor: null, per_day_minor: null, per_use_minor: 101, as_of: '2026-11-07T04:00:00.123456789Z' })
  expect(readPaths).not.toContain(`/api/v1/items/${result.item.id}/units`); expect(readPaths).not.toContain('/api/v1/inventory')
})

test('withholds a profile created inside the real reviewed absence interval until an explicit fresh read', async ({ page, application }) => {
  await provision(page, application)
  await page.goto('/')
  const seed = await page.evaluate(async () => {
    const modulePath = '/assets/assets-sdk.js'
    const api = await import(modulePath)
    const response = await fetch('/api/v1/auth/csrf', { credentials: 'same-origin', cache: 'no-store' })
    const csrfToken: string = (await response.json()).token
    const root = () => ({ csrfToken, idempotencyKey: crypto.randomUUID(), signal: new AbortController().signal })
    const item = await api.createItem({ name: 'Reviewed profile absence' }, root())
    const unit = await api.createUnit(item.id, { location_id: null }, { ...root(), expectedRevision: item.revision })
    return { unit, item: await api.fetchItem(item.id, new AbortController().signal) }
  })
  let raced = false
  await page.route((url) => url.pathname === `/api/v1/units/${seed.unit.id}/asset`, async (route) => {
    if (route.request().method() !== 'GET' || raced) { await route.continue(); return }
    const original = await route.fetch()
    expect(original.status()).toBe(404)
    raced = true
    const headers = await writeContext(page, application.url, seed.unit.revision)
    const created = await page.request.put(`/api/v1/units/${seed.unit.id}/asset`, { headers, data: { item_revision: seed.item.revision, asset_revision: null, cost_basis: 'manual', currency: 'JPY',
      manual_acquisition_minor: 0, in_service_at: null, retired_at: null, estimated_residual_minor: null, usage_unit: 'times' } })
    expect(created.status()).toBe(200)
    await route.fulfill({ response: original })
  })
  const code = await page.evaluate(async (id) => {
    const modulePath = '/assets/assets-sdk.js'
    const api = await import(modulePath)
    try { await api.readAssetProfile(id, new AbortController().signal); return null } catch (error) { return error instanceof Error && 'code' in error ? error.code : 'unexpected' }
  }, seed.unit.id)
  expect(raced).toBe(true); expect(code).toBe('INVALID_RESPONSE')
  const current = await page.evaluate(async (id) => {
    const modulePath = '/assets/assets-sdk.js'
    const api = await import(modulePath)
    return api.readAssetProfile(id, new AbortController().signal)
  }, seed.unit.id)
  expect(current.asset).toMatchObject({ revision: 1, manual_acquisition_minor: 0 }); expect(current.item.revision).toBe(seed.item.revision + 1)
})

for (const kind of ['usage', 'costs', 'lifecycle', 'detail'] as const) {
  test(`withholds genuine ${kind} source races at queried URLs rather than returning an old prefix`, async ({ page, application }) => {
    await provision(page, application)
    await page.goto('/')
    const seed = await page.evaluate(async () => {
      const modulePath = '/assets/assets-sdk.js'
      const api = await import(modulePath)
      const response = await fetch('/api/v1/auth/csrf', { credentials: 'same-origin', cache: 'no-store' })
      const csrfToken: string = (await response.json()).token
      const root = () => ({ csrfToken, idempotencyKey: crypto.randomUUID(), signal: new AbortController().signal })
      const item = await api.createItem({ name: 'Actual source interval' }, root())
      const unit = await api.createUnit(item.id, { location_id: null }, { ...root(), expectedRevision: item.revision })
      const owner = await api.fetchItem(item.id, new AbortController().signal)
      const asset = await api.putAssetProfile(unit.id, { item_revision: owner.revision, asset_revision: null, cost_basis: 'manual', currency: 'JPY', manual_acquisition_minor: 100,
        in_service_at: null, retired_at: null, estimated_residual_minor: null, usage_unit: 'times' }, { ...root(), unit, item: owner, asset: null })
      return { unit, asset }
    })
    const tail = kind === 'lifecycle' ? 'disposal' : kind === 'detail' ? 'cost-summary' : kind
    let raced = false
    await page.route((url) => url.pathname === `/api/v1/units/${seed.unit.id}/${tail}`, async (route) => {
      if (route.request().method() !== 'GET' || raced) { await route.continue(); return }
      const original = await route.fetch()
      expect(original.status()).toBe(kind === 'lifecycle' ? 404 : 200)
      raced = true
      const headers = await writeContext(page, application.url, seed.asset.revision)
      const body = kind === 'usage' ? { delta: 1, usage_unit: 'times', event_type: 'use', occurred_at: '2026-11-07T00:00:00.123456789Z' } : kind === 'lifecycle' ?
        { unit_revision: seed.unit.revision, kind: 'other', disposed_at: '2026-11-07T00:00:00.123456789Z', currency: 'JPY', realized_proceeds_minor: 0 } :
        { amount_minor: 1, currency: 'JPY', kind: 'repair', direction: 'expense', occurred_at: '2026-11-07T00:00:00.123456789Z' }
      const changed = await page.request.post(`/api/v1/units/${seed.unit.id}/${kind === 'usage' ? 'usage' : kind === 'lifecycle' ? 'disposal' : 'costs'}`, { headers, data: body })
      expect(changed.status()).toBe(200)
      await route.fulfill({ response: original })
    })
    const result = await page.evaluate(async ({ id, kind }) => {
      const modulePath = '/assets/assets-sdk.js'
      const api = await import(modulePath)
      const read = () => kind === 'usage' ? api.readAssetUsage(id, new AbortController().signal) : kind === 'costs' ? api.readAssetCosts(id, new AbortController().signal) :
        kind === 'lifecycle' ? api.readAssetLifecycle(id, new AbortController().signal) : api.readAssetDetail(id, {}, new AbortController().signal)
      let code = null
      try { await read() } catch (error) { code = error instanceof Error && 'code' in error ? error.code : 'unexpected' }
      return { code, current: await read() }
    }, { id: seed.unit.id, kind })
    expect(raced).toBe(true); expect(result.code).toBe('INVALID_RESPONSE'); expect(result.current.asset.revision).toBe(2)
    if (kind === 'usage') expect(result.current.usage).toHaveLength(1)
    if (kind === 'costs') expect(result.current.costs).toHaveLength(1)
    if (kind === 'lifecycle') { expect(result.current.unit.status).toBe('disposed'); expect(result.current.disposal).toMatchObject({ revision: 1, realized_proceeds_minor: 0 }) }
    if (kind === 'detail') expect(result.current.summary).toMatchObject({ extra_minor: 1, net_cost_minor: 101 })
  })
}

for (const kind of ['profile', 'usage', 'cost', 'retire', 'disposal'] as const) {
  test(`submits an exact explicitly reviewed ${kind} form proposal against its actual complete source`, async ({ page, application }) => {
    await provision(page, application)
    const writes = watchDomainWrites(page, ['/api/v1/units'])
    await page.goto('/')
    const result = await page.evaluate(async ({ kind }) => {
      const modulePath = '/assets/assets-sdk.js'
      const api = await import(modulePath)
      const response = await fetch('/api/v1/auth/csrf', { credentials: 'same-origin', cache: 'no-store' })
      const csrfToken: string = (await response.json()).token
      const root = () => ({ csrfToken, idempotencyKey: crypto.randomUUID(), signal: new AbortController().signal })
      const read = () => new AbortController().signal
      const owner = await api.createItem({ name: 'Actual raw financial form' }, root())
      const unit = await api.createUnit(owner.id, { location_id: null, notes: 'No implicit physical change' }, { ...root(), expectedRevision: owner.revision })
      const item = await api.fetchItem(owner.id, read())
      let source, values, body
      if (kind === 'profile') {
        source = await api.readAssetProfile(unit.id, read())
        values = { ...api.assetProfileDefaults(source), cost_basis: 'manual', currency: 'JPY', manual_acquisition_minor: '0', usage_unit: 'times',
          service_decision: 'unknown', retirement_decision: 'unknown', residual_decision: 'value', estimated_residual_minor: '0' }
        body = api.assetProfileBody(values, source)
      } else {
        const profile = await api.putAssetProfile(unit.id, { item_revision: item.revision, asset_revision: null, cost_basis: 'manual', currency: 'JPY', manual_acquisition_minor: 100,
          in_service_at: '2026-01-01T00:00:00Z', retired_at: null, estimated_residual_minor: null, usage_unit: 'times' }, { ...root(), unit, item, asset: null })
        if (kind === 'usage') {
          await api.recordAssetUsage(unit.id, { delta: 2, usage_unit: 'times', event_type: 'use', occurred_at: '2026-11-07T04:00:00.123456789Z' }, { ...root(), asset: profile })
          source = await api.readAssetUsage(unit.id, read())
          values = { ...api.assetUsageDefaults(source), delta: '-1', event_type: 'correction', correction_of: source.usage[0].id,
            occurred_at: '2026-11-07T12:00:00.123456789+08:00', note: '\0Raw correction é\n' }
          body = api.assetUsageBody(values, source)
        } else if (kind === 'cost') {
          await api.recordAssetCost(unit.id, { amount_minor: 5, currency: 'JPY', kind: 'repair', direction: 'expense', occurred_at: '2026-11-07T04:00:00.123456789Z' }, { ...root(), asset: profile })
          source = await api.readAssetCosts(unit.id, read())
          values = { ...api.assetCostDefaults(source), amount_minor: '2', kind: 'adjustment', direction: 'credit', correction_of: source.costs[0].id,
            occurred_at: '2026-11-07T12:00:00.123456789+08:00', note: '\0Raw adjustment é\n' }
          body = api.assetCostBody(values, source)
        } else {
          source = await api.readAssetLifecycle(unit.id, read())
          if (kind === 'retire') {
            values = { ...api.assetRetireDefaults(source), retired_at: '2026-11-07T12:00:00.123456789+08:00', note: 'Raw retirement\n' }
            body = api.assetRetireBody(values, source)
          } else {
            // Disposal date is an independent decision, not an inferred service
            // bound or another physical movement time.
            values = { ...api.assetDisposalDefaults(source), kind: 'other', realized_proceeds_minor: '0', disposed_at: '1999-01-01T00:00:00Z', note: 'Raw zero proceeds\n' }
            body = api.assetDisposalBody(values, source)
          }
        }
      }
      const command = { ...root(), ...source }
      const value = kind === 'profile' ? await api.putAssetProfile(unit.id, body, command) : kind === 'usage' ? await api.recordAssetUsage(unit.id, body, command) :
        kind === 'cost' ? await api.recordAssetCost(unit.id, body, command) : kind === 'retire' ? await api.retireAsset(unit.id, body, command) : await api.disposeAsset(unit.id, body, command)
      return { value, unit, source, values, body, key: command.idempotencyKey, current: await api.fetchAsset(unit.id, read()), physical: await api.fetchUnit(unit.id, read()) }
    }, { kind })
    expect(result.value.revision).toBe(kind === 'retire' ? 2 : 1)
    if (kind === 'profile') {
      expect(result.body).toMatchObject({ item_revision: result.source.item.revision, asset_revision: null, manual_acquisition_minor: 0, in_service_at: null, retired_at: null, estimated_residual_minor: 0 })
      expect(result.value).toMatchObject({ manual_acquisition_minor: 0, estimated_residual_minor: 0 })
    }
    if (kind === 'usage') expect(result.value).toMatchObject({ delta: -1, correction_of: result.source.usage[0].id, occurred_at: '2026-11-07T04:00:00.123456789Z', note: '\0Raw correction é\n' })
    if (kind === 'cost') expect(result.value).toMatchObject({ amount_minor: 2, direction: 'credit', correction_of: result.source.costs[0].id, note: '\0Raw adjustment é\n' })
    if (kind === 'retire') { expect(result.value.status).toBe('retired'); expect(result.current.retired_at).toBe('2026-11-07T04:00:00.123456789Z') }
    if (kind === 'disposal') { expect(result.value).toMatchObject({ kind: 'other', realized_proceeds_minor: 0, disposed_at: '1999-01-01T00:00:00.000000000Z' }); expect(result.current.retired_at).toBeNull() }
    if (kind === 'profile' || kind === 'usage' || kind === 'cost') expect(result.physical).toEqual(result.unit)
    else expect(result.physical).toEqual({ ...result.unit, revision: 2, status: kind === 'retire' ? 'retired' : 'disposed' })
    const attempts = writes.filter((request) => request.key === result.key)
    expect(attempts).toHaveLength(1)
    expect(attempts[0]?.proof).toBe(`"${kind === 'profile' ? result.source.unit.revision : result.source.asset.revision}"`)
    expect(JSON.parse(attempts[0]?.body ?? '{}')).toEqual(result.body)
  })
}
