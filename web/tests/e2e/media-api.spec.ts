import { cpSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { DatabaseSync } from 'node:sqlite'
import { fileURLToPath } from 'node:url'
import { build } from 'vite'
import { test as applicationTest, expect } from './auth-fixture.ts'
import { provision, writeContext } from './domain-helpers.ts'

const owner = 'media-sdk-owner', sibling = 'media-sdk-sibling', empty = 'media-sdk-empty'
const test = applicationTest.extend<{}, { applicationWebDirectory: string }>({
  applicationWebDirectory: [async ({ browserName }, provideFixture) => {
    const directory = mkdtempSync(join(tmpdir(), `homebox-media-sdk-${browserName}-`))
    const web = join(directory, 'web'), sdk = join(directory, 'sdk'), entry = join(directory, 'entry.ts')
    try {
      cpSync(fileURLToPath(new URL('../../dist', import.meta.url)), web, { recursive: true })
      // The private SDK bundle is never a production test entrypoint.
      writeFileSync(entry, `export * from ${JSON.stringify(fileURLToPath(new URL('../../src/lib/api/media.ts', import.meta.url)))};\nexport * from ${JSON.stringify(fileURLToPath(new URL('../../src/lib/api/media-writes.ts', import.meta.url)))};\nexport { fetchItem } from ${JSON.stringify(fileURLToPath(new URL('../../src/lib/api/catalog.ts', import.meta.url)))};\nexport { createUnit, fetchUnitByIdentity } from ${JSON.stringify(fileURLToPath(new URL('../../src/lib/api/holdings.ts', import.meta.url)))};\nexport { createLocation } from ${JSON.stringify(fileURLToPath(new URL('../../src/lib/api/locations.ts', import.meta.url)))};\nexport * from ${JSON.stringify(fileURLToPath(new URL('../../src/features/media/model.ts', import.meta.url)))};\n`)
      await build({ configFile: false, logLevel: 'silent', resolve: { alias: { '@': fileURLToPath(new URL('../../src', import.meta.url)) } },
        build: { outDir: sdk, minify: false, lib: { entry, formats: ['es'], fileName: 'media-sdk' } } })
      cpSync(join(sdk, 'media-sdk.js'), join(web, 'assets', 'media-sdk.js'))
      await provideFixture(web)
    } finally { rmSync(directory, { recursive: true, force: true }) }
  }, { scope: 'worker' }],
})

function prepareMediaFixture(database: string, count = 51, legacy = false) {
  // Seed retained source only while the server is stopped. No file, HTTP
  // mutation, session, audit receipt or verified image is invented.
  const db = new DatabaseSync(database), now = '2026-11-06T00:00:00Z', digest = 'a'.repeat(64)
  try {
    db.exec('PRAGMA foreign_keys=ON; BEGIN IMMEDIATE')
    const counter = db.prepare('INSERT INTO projection_revisions(entity_type,entity_id,revision) VALUES(?,?,?)')
    const current = db.prepare('INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at) VALUES(?,?,?,?,?)')
    const baseline = (kind: string, id: string, revision: number, payload: object) => { counter.run(kind, id, revision); current.run(kind, id, revision, JSON.stringify(payload), now) }
    const item = db.prepare('INSERT INTO items(id,name,tracking_mode,created_at,updated_at) VALUES(?,?,?,?,?)')
    for (const id of [owner, sibling, empty]) {
      item.run(id, id, 'individual', now, now)
      baseline('item', id, 1, { id, name: id, brand: null, model: null, category_id: null, description: '', notes: '', tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1, status: 'active' })
      counter.run('search_doc', JSON.stringify(['item', id]), 1)
    }
    const media = db.prepare('INSERT INTO media(id,storage_key,sha256,mime_type,width,height,bytes,created_at) VALUES(?,?,?,?,10,20,30,?)')
    for (const image of [{ id: 'photo', mime: 'image/heic', revision: 7 }, { id: 'links', mime: 'image/png', revision: 3 }]) {
      media.run(image.id, `private-${image.id}`, digest, image.mime, now)
      baseline('media', image.id, image.revision, { id: image.id, sha256: digest, thumb_url: null, width: 10, height: 20, thumb_sha256: null, mime_type: image.mime, bytes: 30, perceptual_hash: null })
    }
    const addLink = db.prepare('INSERT INTO media_links(id,media_id,item_id,is_cover,sort_order,description) VALUES(?,?,?,1,-3,?)')
    const seedLink = (id: string, mediaID: string, ownerID: string, revision: number) => {
      const description = '\0Exact é\n'
      addLink.run(id, mediaID, ownerID, description)
      baseline('media_link', id, revision, { id, media_id: mediaID, owner_type: 'item', owner_id: ownerID, is_cover: true, sort_order: -3, description })
    }
    for (let index = 0; index < count; index++) seedLink(`owner-${String(index).padStart(3, '0')}`, 'photo', owner, 1)
    seedLink('sibling', 'photo', sibling, 5)
    seedLink('reserved', 'links', sibling, 1)
    if (legacy) {
      const itemID = 'legacy/item\0😀', unitID = 'legacy/unit\0😀', locationID = 'tree'
      item.run(itemID, 'Legacy owner', 'individual', now, now)
      baseline('item', itemID, 1, { id: itemID, name: 'Legacy owner', brand: null, model: null, category_id: null, description: '', notes: '', tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1, status: 'active' })
      counter.run('search_doc', JSON.stringify(['item', itemID]), 1)
      db.prepare('INSERT INTO item_units(id,item_id,asset_code,status,created_at,updated_at) VALUES(?,?,?,?,?,?)').run(unitID, itemID, 'LEGACY-ASSET', 'stored', now, now)
      baseline('unit', unitID, 1, { id: unitID, item_id: itemID, location_id: null, asset_code: 'LEGACY-ASSET', serial_number: null, notes: '', status: 'stored' })
      counter.run('search_doc', JSON.stringify(['unit', unitID]), 1)
      db.prepare('INSERT INTO locations(id,code,name,kind,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?)').run(locationID, 'LEGACY-ROOM', 'Legacy room', 'room', 'active', now, now)
      baseline('location', locationID, 1, { id: locationID, parent_id: null, code: 'LEGACY-ROOM', name: 'Legacy room', kind: 'room', status: 'active', sort_order: 0, description: '' })
      seedLink('legacy-item-link', 'photo', itemID, 1)
      for (const [id, kind, ownerID, column] of [['legacy-unit-link', 'unit', unitID, 'item_unit_id'], ['legacy-location-link', 'location', locationID, 'location_id']] as const) {
        db.prepare(`INSERT INTO media_links(id,media_id,${column},is_cover,sort_order,description) VALUES(?,?,?,0,0,'')`).run(id, 'photo', ownerID)
        baseline('media_link', id, 1, { id, media_id: 'photo', owner_type: kind, owner_id: ownerID, is_cover: false, sort_order: 0, description: '' })
      }
    }
    db.exec('COMMIT')
    expect(db.prepare('PRAGMA foreign_key_check').all()).toEqual([])
  } finally { db.close() }
}

test.use({ trace: 'off', screenshot: 'off', video: 'off', applicationDatabaseFixture: { prepare: prepareMediaFixture } })

test('reads genuine scoped Media pages and observes owner deletion without inventing images', async ({ page, application }) => {
  await provision(page, application)
  await page.goto('/')
  const result = await page.evaluate(async ({ owner, sibling, empty }) => {
    const modulePath = '/assets/media-sdk.js'
    const api = await import(modulePath)
    const signal = () => new AbortController().signal
    const failure = async (work: () => Promise<unknown>) => {
      try { await work(); return null } catch (error) { return error instanceof Error && 'code' in error ? error.code : 'unexpected' }
    }
    const image = await api.fetchMedia('photo', signal())
    const reserved = await api.fetchMedia('links', signal())
    const ownerFirst = await api.fetchOwnerMediaLinksPage('item', owner, { limit: 50 }, signal())
    const ownerLast = await api.fetchOwnerMediaLinksPage('item', owner, { limit: 50, cursor: ownerFirst.next_cursor }, signal())
    const mediaFirst = await api.fetchMediaLinksPage('photo', { limit: 50 }, signal())
    const mediaLast = await api.fetchMediaLinksPage('photo', { limit: 50, cursor: mediaFirst.next_cursor }, signal())
    const wrongScope = await failure(() => api.fetchOwnerMediaLinksPage('item', sibling, { limit: 50, cursor: ownerFirst.next_cursor }, signal()))
    const crossScope = await failure(() => api.fetchOwnerMediaLinksPage('item', owner, { limit: 50, cursor: mediaFirst.next_cursor }, signal()))
    const missing = await failure(() => api.fetchOwnerMediaLinksPage('item', 'absent', {}, signal()))
    const emptyPage = await api.fetchOwnerMediaLinksPage('item', empty, {}, signal())
    const reservedLinks = await api.fetchMediaLinksPage('links', {}, signal())
    return { image, reserved, ownerFirst, ownerLast, mediaFirst, mediaLast, wrongScope, crossScope, missing, emptyPage, reservedLinks }
  }, { owner, sibling, empty })
  expect(result.image).toMatchObject({ id: 'photo', revision: 7, mime_type: 'image/heic', verification: 'unverified', thumb_url: null, thumb_sha256: null })
  expect(result.reserved).toMatchObject({ id: 'links', revision: 3, verification: 'unverified' })
  expect(result.ownerFirst.data).toHaveLength(50)
  expect(result.ownerLast.data).toHaveLength(1)
  expect(result.mediaFirst.data).toHaveLength(50)
  expect(result.mediaLast.data).toHaveLength(2)
  expect(result.ownerLast.next_cursor).toBeNull(); expect(result.mediaLast.next_cursor).toBeNull()
  expect(result.ownerFirst.data[0]).toMatchObject({ media_id: 'photo', owner_id: owner, sort_order: -3, description: '\0Exact é\n', media: { id: 'photo', revision: 7 } })
  expect(result.wrongScope).toBe('INVALID_CURSOR'); expect(result.crossScope).toBe('INVALID_CURSOR'); expect(result.missing).toBe('NOT_FOUND')
  expect(result.emptyPage).toEqual({ data: [], next_cursor: null })
  expect(result.reservedLinks.data).toMatchObject([{ id: 'reserved', owner_id: sibling, media: { id: 'links' } }])
  expect(JSON.stringify(result)).not.toContain('private-photo')

  const headers = await writeContext(page, application.url, 1)
  const first = await page.request.delete(`/api/v1/items/${owner}`, { headers })
  expect(first.status()).toBe(200)
  const ack = await first.json()
  const replay = await page.request.delete(`/api/v1/items/${owner}`, { headers })
  expect(replay.status()).toBe(200); expect(await replay.json()).toEqual(ack)
  const after = await page.evaluate(async () => {
    const modulePath = '/assets/media-sdk.js', api = await import(modulePath)
    return { image: await api.fetchMedia('photo', new AbortController().signal), links: await api.fetchMediaLinksPage('photo', {}, new AbortController().signal) }
  })
  expect(after.image.revision).toBe(8)
  expect(after.links.data).toMatchObject([{ id: 'sibling', revision: 5, owner_id: sibling, media: { id: 'photo', revision: 8 } }])
})

test('attaches the reserved Media through same-origin browser requests and reconciles the original key', async ({ page, application }) => {
  await provision(page, application)
  await page.goto('/')
  const result = await page.evaluate(async ({ empty }) => {
    const csrfResponse = await fetch('/api/v1/auth/csrf')
    const csrf = (await csrfResponse.json()).token as string
    const headers = { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf, 'X-HomeBox-Client-Version': '0.1.0-dev', 'Idempotency-Key': crypto.randomUUID(), 'If-Match': '"3"' }
    const body = JSON.stringify({ item_id: empty, owner_revision: 1, is_cover: true, sort_order: 0, description: 'Exact é\ncaption' })
    const attach = async () => {
      const response = await fetch('/api/v1/media/links/links', { method: 'POST', headers, body })
      return { status: response.status, etag: response.headers.get('ETag'), cache: response.headers.get('Cache-Control'), value: await response.json() }
    }
    const first = await attach(), retry = await attach()
    const imageResponse = await fetch('/api/v1/media/links/view')
    const ownerResponse = await fetch(`/api/v1/media/links?owner_type=item&owner_id=${encodeURIComponent(empty)}`)
    const membershipResponse = await fetch('/api/v1/media/links/links')
    return { csrfStatus: csrfResponse.status, first, retry, imageStatus: imageResponse.status, image: await imageResponse.json(), owner: await ownerResponse.json(), membership: await membershipResponse.json() }
  }, { empty })
  expect(result.csrfStatus).toBe(200)
  expect(result.first).toMatchObject({ status: 200, etag: '"1"', cache: 'no-store', value: { media_id: 'links', owner_type: 'item', owner_id: empty, revision: 1, is_cover: true, sort_order: 0, description: 'Exact é\ncaption' } })
  expect(result.first.value.id).toBeTruthy()
  expect(Object.keys(result.first.value).sort()).toEqual(['description', 'id', 'is_cover', 'media_id', 'owner_id', 'owner_type', 'revision', 'sort_order'])
  expect(result.retry).toEqual(result.first)
  expect(result.imageStatus).toBe(200)
  expect(result.image).toMatchObject({ id: 'links', revision: 4, verification: 'unverified', thumb_url: null, thumb_sha256: null })
  expect(result.owner.data).toMatchObject([{ id: result.first.value.id, media: { id: 'links', revision: 4 } }])
  expect(result.membership.data).toHaveLength(2)
  expect(result.membership.data.some((link: { id: string }) => link.id === result.first.value.id)).toBe(true)
  expect(JSON.stringify(result)).not.toContain('private-links')
})

test('edits an independent MediaLink through same-origin browser requests with explicit original-key reconciliation', async ({ page, application }) => {
  await provision(page, application)
  await page.goto('/')
  const result = await page.evaluate(async ({ sibling }) => {
    const csrfResponse = await fetch('/api/v1/auth/csrf')
    const csrf = (await csrfResponse.json()).token as string
    const headers = { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf, 'X-HomeBox-Client-Version': '0.1.0-dev', 'Idempotency-Key': crypto.randomUUID(), 'If-Match': '"5"' }
    const body = JSON.stringify({ is_cover: false, sort_order: 0, description: 'Reviewed é\ncaption' })
    const edit = async (requestHeaders: typeof headers) => {
      const response = await fetch('/api/v1/media/links/sibling', { method: 'PATCH', headers: requestHeaders, body })
      return { status: response.status, etag: response.headers.get('ETag'), cache: response.headers.get('Cache-Control'), value: await response.json() }
    }
    const first = await edit(headers), retry = await edit(headers)
    const stale = await edit({ ...headers, 'Idempotency-Key': crypto.randomUUID() })
    const equal = await edit({ ...headers, 'Idempotency-Key': crypto.randomUUID(), 'If-Match': '"6"' })
    const imageResponse = await fetch('/api/v1/media/photo')
    const ownerResponse = await fetch(`/api/v1/media/links?owner_type=item&owner_id=${encodeURIComponent(sibling)}`)
    const reservedResponse = await fetch('/api/v1/media/links/view')
    return { csrfStatus: csrfResponse.status, first, retry, stale, equal, image: await imageResponse.json(), owner: await ownerResponse.json(), reserved: await reservedResponse.json() }
  }, { sibling })
  expect(result.csrfStatus).toBe(200)
  expect(result.first).toMatchObject({ status: 200, etag: '"6"', cache: 'no-store', value: { id: 'sibling', media_id: 'photo', owner_type: 'item', owner_id: sibling, revision: 6, is_cover: false, sort_order: 0, description: 'Reviewed é\ncaption' } })
  expect(Object.keys(result.first.value).sort()).toEqual(['description', 'id', 'is_cover', 'media_id', 'owner_id', 'owner_type', 'revision', 'sort_order'])
  expect(result.retry).toEqual(result.first)
  expect(result.stale).toMatchObject({ status: 409, etag: null, value: { error: { code: 'REVISION_CONFLICT' } } })
  expect(result.equal).toEqual(result.first)
  expect(result.image).toMatchObject({ id: 'photo', revision: 8, verification: 'unverified', thumb_url: null, thumb_sha256: null })
  expect(result.reserved).toMatchObject({ id: 'links', revision: 3 })
  expect(result.owner.data).toContainEqual(expect.objectContaining({ id: 'sibling', revision: 6, media: expect.objectContaining({ id: 'photo', revision: 8 }) }))
  expect(JSON.stringify(result)).not.toContain('private-photo')
})

test('unlinks one shared MediaLink through same-origin browser requests with explicit original-key reconciliation', async ({ page, application }) => {
  await provision(page, application)
  await page.goto('/')
  const result = await page.evaluate(async ({ sibling }) => {
    const csrfResponse = await fetch('/api/v1/auth/csrf')
    const csrf = (await csrfResponse.json()).token as string
    const headers = { 'X-CSRF-Token': csrf, 'X-HomeBox-Client-Version': '0.1.0-dev', 'Idempotency-Key': crypto.randomUUID(), 'If-Match': '"5"' }
    const unlink = async (requestHeaders: typeof headers) => {
      const response = await fetch('/api/v1/media/links/sibling', { method: 'DELETE', headers: requestHeaders })
      return { status: response.status, etag: response.headers.get('ETag'), cache: response.headers.get('Cache-Control'), value: await response.json() }
    }
    const first = await unlink(headers), retry = await unlink(headers)
    const changedProof = await unlink({ ...headers, 'If-Match': '"6"' })
    const missing = await unlink({ ...headers, 'Idempotency-Key': crypto.randomUUID() })
    const imageResponse = await fetch('/api/v1/media/photo')
    const ownerResponse = await fetch(`/api/v1/media/links?owner_type=item&owner_id=${encodeURIComponent(sibling)}`)
    const membershipResponse = await fetch('/api/v1/media/photo/links?limit=100')
    const reservedResponse = await fetch('/api/v1/media/links/view')
    return { csrfStatus: csrfResponse.status, first, retry, changedProof, missing, image: await imageResponse.json(), owner: await ownerResponse.json(), membership: await membershipResponse.json(), reserved: await reservedResponse.json() }
  }, { sibling })
  expect(result.csrfStatus).toBe(200)
  expect(result.first).toMatchObject({ status: 200, etag: null, cache: 'no-store', value: { ok: true, revision: 6 } })
  expect(result.first.value.operation_id).toBeTruthy()
  expect(Object.keys(result.first.value).sort()).toEqual(['ok', 'operation_id', 'revision'])
  expect(result.retry).toEqual(result.first)
  expect(result.changedProof).toMatchObject({ status: 409, etag: null, value: { error: { code: 'IDEMPOTENCY_CONFLICT' } } })
  expect(result.missing).toMatchObject({ status: 404, etag: null, value: { error: { code: 'NOT_FOUND' } } })
  expect(result.image).toMatchObject({ id: 'photo', revision: 8, verification: 'unverified', thumb_url: null, thumb_sha256: null })
  expect(result.reserved).toMatchObject({ id: 'links', revision: 3 })
  expect(result.owner.data).toMatchObject([{ id: 'reserved', media_id: 'links', owner_id: sibling }])
  expect(result.membership.data).toHaveLength(51)
  expect(result.membership.data.every((link: { id: string }) => link.id !== 'sibling')).toBe(true)
  expect(result.membership.next_cursor).toBeNull()
  expect(JSON.stringify(result)).not.toContain('private-photo')
})

test('writes reviewed MediaLinks through the actual SDK and reconciles the original proofs', async ({ page, application }) => {
  await provision(page, application)
  await page.goto('/')
  const result = await page.evaluate(async ({ sibling, empty }) => {
    const modulePath = '/assets/media-sdk.js', api = await import(modulePath), signal = () => new AbortController().signal
    const csrf = (await (await fetch('/api/v1/auth/csrf')).json()).token as string
    const failure = async (work: () => Promise<unknown>) => {
      try { await work(); return null } catch (error) { return error instanceof Error && 'code' in error ? error.code : 'unexpected' }
    }
    const media = await api.fetchMedia('links', signal()), item = await api.fetchItem(empty, signal())
    const attach = { csrfToken: csrf, idempotencyKey: crypto.randomUUID(), signal: signal(), media, owner: { kind: 'item', value: item } }
    const created = await api.attachMedia('links', {}, attach)
    const createReplay = await api.attachMedia('links', {}, attach)
    const changedBody = await failure(() => api.attachMedia('links', { description: '' }, attach))
    const staleMedia = await failure(() => api.attachMedia('links', {}, { ...attach, idempotencyKey: crypto.randomUUID() }))
    const page = await api.fetchOwnerMediaLinksPage('item', sibling, {}, signal())
    const original = page.data.find((link: { id: string }) => link.id === 'sibling')
    if (!original) throw new Error('Fixture link missing')
    const edit = { csrfToken: csrf, idempotencyKey: crypto.randomUUID(), signal: signal(), link: original }
    const changed = await api.patchMediaLink('sibling', { description: 'SDK reviewed é\n' }, edit)
    const editReplay = await api.patchMediaLink('sibling', { description: 'SDK reviewed é\n' }, edit)
    const staleLink = await failure(() => api.patchMediaLink('sibling', { description: 'Another' }, { ...edit, idempotencyKey: crypto.randomUUID() }))
    const equal = await api.patchMediaLink('sibling', { description: 'SDK reviewed é\n' }, { ...edit, idempotencyKey: crypto.randomUUID(), link: changed })
    const unlink = { csrfToken: csrf, idempotencyKey: crypto.randomUUID(), signal: signal(), link: changed }
    const removed = await api.unlinkMediaLink('sibling', unlink), removeReplay = await api.unlinkMediaLink('sibling', unlink)
    const missing = await failure(() => api.unlinkMediaLink('sibling', { ...unlink, idempotencyKey: crypto.randomUUID() }))
    const changedProof = await failure(() => api.unlinkMediaLink('sibling', { ...unlink, link: { ...changed, revision: 7 } }))
    return { created, createReplay, changedBody, staleMedia, changed, editReplay, staleLink, equal, removed, removeReplay, missing, changedProof,
      media: await api.fetchMedia('links', signal()), photo: await api.fetchMedia('photo', signal()),
      empty: await api.fetchOwnerMediaLinksPage('item', empty, {}, signal()), sibling: await api.fetchOwnerMediaLinksPage('item', sibling, {}, signal()) }
  }, { sibling, empty })
  expect(result.created).toMatchObject({ media_id: 'links', owner_type: 'item', owner_id: empty, revision: 1, is_cover: false, sort_order: 0, description: '' })
  expect(result.createReplay).toEqual(result.created)
  expect(result.changedBody).toBe('IDEMPOTENCY_CONFLICT'); expect(result.staleMedia).toBe('REVISION_CONFLICT')
  expect(result.changed).toMatchObject({ id: 'sibling', media_id: 'photo', owner_id: sibling, revision: 6, is_cover: true, sort_order: -3, description: 'SDK reviewed é\n' })
  expect(result.editReplay).toEqual(result.changed); expect(result.equal).toEqual(result.changed); expect(result.staleLink).toBe('REVISION_CONFLICT')
  expect(result.removed).toMatchObject({ ok: true, revision: 7 }); expect(result.removed.operation_id).toBeTruthy()
  expect(result.removeReplay).toEqual(result.removed); expect(result.missing).toBe('NOT_FOUND'); expect(result.changedProof).toBe('IDEMPOTENCY_CONFLICT')
  expect(result.media).toMatchObject({ id: 'links', revision: 4, verification: 'unverified', thumb_url: null })
  expect(result.photo).toMatchObject({ id: 'photo', revision: 9, verification: 'unverified', thumb_url: null })
  expect(result.empty.data).toMatchObject([{ id: result.created.id, media_id: 'links' }])
  expect(result.sibling.data).toMatchObject([{ id: 'reserved', media_id: 'links' }])
  expect(JSON.stringify(result)).not.toContain('private-photo')
})

test('attaches reviewed Media to actual Unit and Location owners through the SDK', async ({ page, application }) => {
  await provision(page, application)
  await page.goto('/')
  const result = await page.evaluate(async ({ empty }) => {
    const modulePath = '/assets/media-sdk.js', api = await import(modulePath), signal = () => new AbortController().signal
    const csrf = (await (await fetch('/api/v1/auth/csrf')).json()).token as string
    const context = () => ({ csrfToken: csrf, idempotencyKey: crypto.randomUUID(), signal: signal() })
    const location = await api.createLocation({ name: 'SDK room', code: 'SDK-ROOM' }, context())
    const item = await api.fetchItem(empty, signal())
    const unit = await api.createUnit(empty, { location_id: null }, { ...context(), expectedRevision: item.revision })
    const firstMedia = await api.fetchMedia('links', signal())
    const unitLink = await api.attachMedia('links', { description: 'Unit view' }, { ...context(), media: firstMedia, owner: { kind: 'unit', value: unit } })
    const secondMedia = await api.fetchMedia('links', signal())
    const locationLink = await api.attachMedia('links', { is_cover: true }, { ...context(), media: secondMedia, owner: { kind: 'location', value: location } })
    return { location, unit, unitLink, locationLink, media: await api.fetchMedia('links', signal()),
      unitPage: await api.fetchOwnerMediaLinksPage('unit', unit.id, {}, signal()),
      locationPage: await api.fetchOwnerMediaLinksPage('location', location.id, {}, signal()) }
  }, { empty })
  expect(result.unitLink).toMatchObject({ media_id: 'links', owner_type: 'unit', owner_id: result.unit.id, revision: 1, is_cover: false, description: 'Unit view' })
  expect(result.locationLink).toMatchObject({ media_id: 'links', owner_type: 'location', owner_id: result.location.id, revision: 1, is_cover: true, description: '' })
  expect(result.unitPage.data).toMatchObject([{ id: result.unitLink.id, owner_id: result.unit.id }])
  expect(result.locationPage.data).toMatchObject([{ id: result.locationLink.id, owner_id: result.location.id }])
  expect(result.media).toMatchObject({ id: 'links', revision: 5, verification: 'unverified', thumb_url: null })
})

test('reconciles a committed unlink after the SDK loses the real server response', async ({ page, application }) => {
  await provision(page, application)
  let deletes = 0, committedStatus = 0
  await page.route((url) => url.pathname === '/api/v1/media/links/sibling', async (route) => {
    if (route.request().method() !== 'DELETE') { await route.continue(); return }
    deletes++
    if (deletes !== 1) { await route.continue(); return }
    const response = await route.fetch()
    committedStatus = response.status()
    await route.abort('failed')
  })
  await page.goto('/')
  const state = await page.evaluateHandle(async ({ sibling }) => {
    const modulePath = '/assets/media-sdk.js', api = await import(modulePath), signal = () => new AbortController().signal
    const csrf = (await (await fetch('/api/v1/auth/csrf')).json()).token as string
    const page = await api.fetchOwnerMediaLinksPage('item', sibling, {}, signal())
    const link = page.data.find((value: { id: string }) => value.id === 'sibling')
    if (!link) throw new Error('Fixture link missing')
    return { api, csrf, link, key: crypto.randomUUID(), sibling }
  }, { sibling })
  const first = await state.evaluate(async ({ api, csrf, link, key }) => {
    try { await api.unlinkMediaLink('sibling', { csrfToken: csrf, idempotencyKey: key, signal: new AbortController().signal, link }); return 'unexpected' }
    catch (error) { return error instanceof Error && 'code' in error ? error.code : 'unexpected' }
  })
  expect(first).toBe('NETWORK_ERROR'); expect(committedStatus).toBe(200); expect(deletes).toBe(1)
  const actual = await state.evaluate(async ({ api, sibling }) => ({
    owner: await api.fetchOwnerMediaLinksPage('item', sibling, {}, new AbortController().signal),
    media: await api.fetchMedia('photo', new AbortController().signal),
  }))
  expect(actual.owner.data).toMatchObject([{ id: 'reserved', media_id: 'links' }])
  expect(actual.media.revision).toBe(8); expect(deletes).toBe(1)
  const retry = await state.evaluate(async ({ api, csrf, link, key }) => api.unlinkMediaLink('sibling', {
    csrfToken: csrf, idempotencyKey: key, signal: new AbortController().signal, link,
  }))
  expect(retry).toMatchObject({ ok: true, revision: 6 }); expect(retry.operation_id).toBeTruthy(); expect(deletes).toBe(2)
  await state.dispose()
})

test('does not replay an offline SDK unlink until an explicit original-key retry', async ({ page, application }) => {
  await provision(page, application)
  await page.goto('/')
  let deletes = 0
  page.on('request', (request) => { if (request.method() === 'DELETE' && new URL(request.url()).pathname === '/api/v1/media/links/sibling') deletes++ })
  const state = await page.evaluateHandle(async ({ sibling }) => {
    const modulePath = '/assets/media-sdk.js', api = await import(modulePath), signal = () => new AbortController().signal
    const csrf = (await (await fetch('/api/v1/auth/csrf')).json()).token as string
    const page = await api.fetchOwnerMediaLinksPage('item', sibling, {}, signal())
    const link = page.data.find((value: { id: string }) => value.id === 'sibling')
    if (!link) throw new Error('Fixture link missing')
    return { api, csrf, link, key: crypto.randomUUID(), sibling }
  }, { sibling })
  try {
    await page.context().setOffline(true)
    const first = await state.evaluate(async ({ api, csrf, link, key }) => {
      try { await api.unlinkMediaLink('sibling', { csrfToken: csrf, idempotencyKey: key, signal: new AbortController().signal, link }); return 'unexpected' }
      catch (error) { return error instanceof Error && 'code' in error ? error.code : 'unexpected' }
    })
    expect(first).toBe('NETWORK_ERROR'); expect(deletes).toBe(1)
    await page.context().setOffline(false)
    const before = await state.evaluate(async ({ api, sibling }) => ({
      owner: await api.fetchOwnerMediaLinksPage('item', sibling, {}, new AbortController().signal),
      media: await api.fetchMedia('photo', new AbortController().signal),
    }))
    expect(before.owner.data).toContainEqual(expect.objectContaining({ id: 'sibling', revision: 5 }))
    expect(before.media.revision).toBe(7); expect(deletes).toBe(1)
    const retry = await state.evaluate(async ({ api, csrf, link, key }) => api.unlinkMediaLink('sibling', {
      csrfToken: csrf, idempotencyKey: key, signal: new AbortController().signal, link,
    }))
    expect(retry).toMatchObject({ ok: true, revision: 6 }); expect(deletes).toBe(2)
  } finally {
    await page.context().setOffline(false)
    await state.dispose()
  }
})

for (const scope of ['owner', 'media'] as const) {
  test(`withholds the ${scope} review when a real link changes between its bracketed reads`, async ({ page, application }) => {
    await provision(page, application)
    let changed = false, patchStatus = 0
    await page.route((url) => url.pathname === (scope === 'owner' ? '/api/v1/media/links' : '/api/v1/media/photo/links'), async (route) => {
      const response = await route.fetch()
      if (!changed) {
        changed = true
        const headers = await writeContext(page, application.url, scope === 'owner' ? 1 : 5)
        const id = scope === 'owner' ? 'owner-000' : 'sibling'
        patchStatus = (await page.request.patch(`/api/v1/media/links/${id}`, { headers, data: { description: 'Changed during review' } })).status()
      }
      await route.fulfill({ response })
    })
    await page.goto('/')
    const result = await page.evaluate(async ({ scope, owner }) => {
      const modulePath = '/assets/media-sdk.js', api = await import(modulePath), signal = () => new AbortController().signal
      const read = () => scope === 'owner' ? api.readOwnerMedia({ kind: 'item', id: owner }, signal()) : api.readMediaMembership('photo', signal())
      let first: unknown
      try { await read(); first = 'unexpected' } catch (error) { first = error instanceof Error && 'code' in error ? error.code : 'unexpected' }
      return { first, current: await read() }
    }, { scope, owner })
    expect(changed).toBe(true); expect(patchStatus).toBe(200); expect(result.first).toBe('INVALID_RESPONSE')
    if (scope === 'owner') {
      expect(result.current.owner).toMatchObject({ kind: 'item', value: { id: owner, revision: 2 } })
      expect(result.current.links).toContainEqual(expect.objectContaining({ id: 'owner-000', revision: 2, description: 'Changed during review' }))
    } else {
      expect(result.current.media).toMatchObject({ id: 'photo', revision: 8 })
      expect(result.current.links).toContainEqual(expect.objectContaining({ id: 'sibling', revision: 6, description: 'Changed during review' }))
    }
  })
}

test.describe('complete large Media review scopes', () => {
  test.use({ applicationDatabaseFixture: { prepare(database) { prepareMediaFixture(database, 205) } } })

  test('reads all signed owner and Media pages between matching actual sources', async ({ page, application }) => {
    await provision(page, application)
    const ownerRequests: string[] = [], mediaRequests: string[] = []
    page.on('request', (request) => {
      const url = new URL(request.url())
      if (request.method() !== 'GET') return
      if (url.pathname === '/api/v1/media/links') ownerRequests.push(url.search)
      if (url.pathname === '/api/v1/media/photo/links') mediaRequests.push(url.search)
    })
    await page.goto('/')
    const result = await page.evaluate(async ({ owner }) => {
      const modulePath = '/assets/media-sdk.js', api = await import(modulePath), signal = () => new AbortController().signal
      return { owner: await api.readOwnerMedia({ kind: 'item', id: owner }, signal()), media: await api.readMediaMembership('photo', signal()) }
    }, { owner })
    expect(result.owner.owner).toMatchObject({ kind: 'item', value: { id: owner, revision: 1 } })
    expect(result.owner.links).toHaveLength(205); expect(result.media.links).toHaveLength(206)
    expect(result.media.media).toMatchObject({ id: 'photo', revision: 7, verification: 'unverified', thumb_url: null })
    expect(result.media.links).toContainEqual(expect.objectContaining({ id: 'sibling', owner_id: sibling, revision: 5 }))
    for (const requests of [ownerRequests, mediaRequests]) {
      expect(requests).toHaveLength(3)
      const queries = requests.map((query) => new URLSearchParams(query))
      expect(queries.map((query) => query.get('limit'))).toEqual(['100', '100', '100'])
      expect(queries[0]?.has('cursor')).toBe(false)
      expect(queries[1]?.get('cursor')).toBeTruthy(); expect(queries[2]?.get('cursor')).toBeTruthy()
      expect(queries[1]?.get('cursor')).not.toBe(queries[2]?.get('cursor'))
    }
    expect(JSON.stringify(result)).not.toContain('private-photo')
  })
})

test.describe('retained Media owner identities', () => {
  test.use({ applicationDatabaseFixture: { prepare(database) { prepareMediaFixture(database, 51, true) } } })

  test('resolves real path-unaddressable Item and Unit plus reserved Location owners from actual source', async ({ page, application }) => {
    await provision(page, application)
    const requests: string[] = [], unitIDs: string[] = []
    page.on('request', (request) => {
      if (request.method() !== 'GET') return
      const url = new URL(request.url())
      requests.push(url.pathname)
      if (url.pathname === '/api/v1/units') unitIDs.push(...url.searchParams.getAll('id'))
    })
    await page.goto('/')
    const result = await page.evaluate(async () => {
      const modulePath = '/assets/media-sdk.js', api = await import(modulePath), signal = () => new AbortController().signal
      return {
        item: await api.readOwnerMedia({ kind: 'item', id: 'legacy/item\0😀' }, signal()),
        unit: await api.readOwnerMedia({ kind: 'unit', id: 'legacy/unit\0😀' }, signal()),
        unitLookup: await api.fetchUnitByIdentity('legacy/unit\0😀', signal()),
        location: await api.readOwnerMedia({ kind: 'location', id: 'tree' }, signal()),
      }
    })
    expect(result.item.owner).toMatchObject({ kind: 'item', value: { id: 'legacy/item\0😀', revision: 1 } })
    expect(result.item.links).toMatchObject([{ id: 'legacy-item-link', owner_id: 'legacy/item\0😀' }])
    expect(result.unit.owner).toMatchObject({ kind: 'unit', value: { id: 'legacy/unit\0😀', item_id: 'legacy/item\0😀', revision: 1 } })
    expect(result.unit.links).toMatchObject([{ id: 'legacy-unit-link', owner_id: 'legacy/unit\0😀' }])
    expect(result.unitLookup).toEqual(result.unit.owner.value)
    expect(result.location.owner).toMatchObject({ kind: 'location', value: { id: 'tree', revision: 1 } })
    expect(result.location.links).toMatchObject([{ id: 'legacy-location-link', owner_id: 'tree' }])
    expect(requests.filter((path) => path === '/api/v1/items')).toHaveLength(2)
    expect(requests.filter((path) => path === '/api/v1/units')).toHaveLength(3)
    expect(unitIDs).toEqual(['legacy/unit\0😀', 'legacy/unit\0😀', 'legacy/unit\0😀'])
    expect(requests.filter((path) => path === '/api/v1/locations/tree')).toHaveLength(2)
    expect(requests.filter((path) => path === '/api/v1/media/links')).toHaveLength(3)
  })
})

test('rejects inconsistent real-server page replies rather than returning a partial selection', async ({ page, application }) => {
  await provision(page, application)
  let tamper = true
  await page.route((url) => url.pathname === '/api/v1/media/photo/links', async (route) => {
    const response = await route.fetch()
    if (!tamper) { await route.fulfill({ response }); return }
    tamper = false
    const body = await response.json()
    body.data[1].media.revision++
    await route.fulfill({ response, body: JSON.stringify(body) })
  })
  await page.goto('/')
  const result = await page.evaluate(async () => {
    const modulePath = '/assets/media-sdk.js', api = await import(modulePath), signal = () => new AbortController().signal
    let code: unknown
    try { await api.fetchMediaLinksPage('photo', { limit: 2 }, signal()) } catch (error) { code = error instanceof Error && 'code' in error ? error.code : 'unexpected' }
    const next = await api.fetchMediaLinksPage('photo', { limit: 2 }, signal())
    return { code, next }
  })
  expect(result.code).toBe('INVALID_RESPONSE')
  expect(result.next.data).toHaveLength(2)
  expect(result.next.next_cursor).toBeTruthy()
})
