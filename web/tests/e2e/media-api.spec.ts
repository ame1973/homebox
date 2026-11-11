import { cpSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { DatabaseSync } from 'node:sqlite'
import { fileURLToPath } from 'node:url'
import { build } from 'vite'
import AxeBuilder from '@axe-core/playwright'
import type { Page } from '@playwright/test'
import { messages } from '../../src/lib/messages.ts'
import { test as applicationTest, expect } from './auth-fixture.ts'
import { noOverflow, provision, watchDomainWrites, writeContext } from './domain-helpers.ts'

const owner = 'media-sdk-owner', sibling = 'media-sdk-sibling', empty = 'media-sdk-empty'
const test = applicationTest.extend<{}, { applicationWebDirectory: string }>({
  applicationWebDirectory: [async ({ browserName }, provideFixture) => {
    const directory = mkdtempSync(join(tmpdir(), `homebox-media-sdk-${browserName}-`))
    const web = join(directory, 'web'), sdk = join(directory, 'sdk'), entry = join(directory, 'entry.ts')
    try {
      cpSync(fileURLToPath(new URL('../../dist', import.meta.url)), web, { recursive: true })
      // The private SDK bundle is never a production test entrypoint.
      writeFileSync(entry, `export * from ${JSON.stringify(fileURLToPath(new URL('../../src/lib/api/media.ts', import.meta.url)))};\nexport * from ${JSON.stringify(fileURLToPath(new URL('../../src/lib/api/media-writes.ts', import.meta.url)))};\nexport { fetchItem, fetchItemByIdentity } from ${JSON.stringify(fileURLToPath(new URL('../../src/lib/api/catalog.ts', import.meta.url)))};\nexport { createUnit, fetchUnitByIdentity, moveUnit } from ${JSON.stringify(fileURLToPath(new URL('../../src/lib/api/holdings.ts', import.meta.url)))};\nexport { createLocation, fetchLocation } from ${JSON.stringify(fileURLToPath(new URL('../../src/lib/api/locations.ts', import.meta.url)))};\nexport * from ${JSON.stringify(fileURLToPath(new URL('../../src/features/media/model.ts', import.meta.url)))};\nexport * from ${JSON.stringify(fileURLToPath(new URL('../../src/features/media/commands.ts', import.meta.url)))};\nexport * from ${JSON.stringify(fileURLToPath(new URL('../../src/features/media/form.ts', import.meta.url)))};\n`)
      await build({ configFile: false, logLevel: 'silent', resolve: { alias: { '@': fileURLToPath(new URL('../../src', import.meta.url)) } },
        build: { outDir: sdk, minify: false, lib: { entry, formats: ['es'], fileName: 'media-sdk' } } })
      cpSync(join(sdk, 'media-sdk.js'), join(web, 'assets', 'media-sdk.js'))
      await provideFixture(web)
    } finally { rmSync(directory, { recursive: true, force: true }) }
  }, { scope: 'worker' }],
})

function prepareMediaFixture(database: string, count = 51, legacy = false, specialMedia = false, specialLinks: readonly string[] = [],
  exactMedia: { ids: readonly string[]; countedID?: string } = { ids: [] }) {
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
    if (specialMedia) {
      const id = 'photo\r\n😀'
      media.run(id, 'private-control', digest, 'image/heic', now)
      baseline('media', id, 2, { id, sha256: digest, thumb_url: null, width: 10, height: 20, thumb_sha256: null, mime_type: 'image/heic', bytes: 30, perceptual_hash: null })
    }
    for (const [index, id] of exactMedia.ids.entries()) {
      media.run(id, `private-retained-media-${index}`, digest, 'image/heic', now)
      baseline('media', id, 1, { id, sha256: digest, thumb_url: null, width: 10, height: 20, thumb_sha256: null, mime_type: 'image/heic', bytes: 30, perceptual_hash: null })
    }
    const addLink = db.prepare('INSERT INTO media_links(id,media_id,item_id,is_cover,sort_order,description) VALUES(?,?,?,1,-3,?)')
    const seedLink = (id: string, mediaID: string, ownerID: string, revision: number) => {
      const description = '\0Exact é\n'
      addLink.run(id, mediaID, ownerID, description)
      baseline('media_link', id, revision, { id, media_id: mediaID, owner_type: 'item', owner_id: ownerID, is_cover: true, sort_order: -3, description })
    }
    const countedMediaID = exactMedia.countedID ?? 'photo'
    for (let index = 0; index < count; index++) seedLink(`owner-${String(index).padStart(3, '0')}`, countedMediaID, owner, 1)
    seedLink('sibling', countedMediaID, sibling, 5)
    seedLink('reserved', 'links', sibling, 1)
    for (const id of specialLinks) seedLink(id, 'photo', empty, 1)
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

async function lookupExistingMedia(page: Page, id: string) {
  await page.getByRole('textbox', { name: messages.media.mediaID }).fill(JSON.stringify(id))
  await page.getByRole('button', { name: messages.media.lookupMedia }).click()
  await expect(page.getByRole('heading', { name: messages.media.selectedMedia })).toBeVisible()
}

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

test.describe('global Media discovery SDK', () => {
  const ids = ['\0Media\r\n', '.', '..', 'legacy/photo', 'legacy\\photo', '☕😀', '😀'.repeat(128),
    ...Array.from({ length: 205 }, (_, index) => `global-${String(index).padStart(3, '0')}`)]
  test.use({ applicationDatabaseFixture: { prepare(database) { prepareMediaFixture(database, 2, false, false, [], { ids }) } } })

  test('discovers linked and unlinked actual Media with scoped live pages and a fresh session', async ({ page, application }) => {
    const password = await provision(page, application)
    const mediaRequests: { method: string; path: string }[] = []
    page.on('request', (request) => {
      const path = new URL(request.url()).pathname
      if (path.startsWith('/api/v1/media')) mediaRequests.push({ method: request.method(), path })
    })
    await page.goto('/')
    const result = await page.evaluate(async () => {
      const modulePath = '/assets/media-sdk.js', api = await import(modulePath), signal = () => new AbortController().signal
      const first = await api.fetchMediaPage({ limit: 100 }, signal())
      const second = await api.fetchMediaPage({ limit: 100, cursor: first.next_cursor }, signal())
      const third = await api.fetchMediaPage({ limit: 100, cursor: second.next_cursor }, signal())
      const small = await api.fetchMediaPage({ limit: 1 }, signal())
      const membership = await api.fetchMediaLinksPage('photo', { limit: 1 }, signal())
      const failure = async (work: () => Promise<unknown>) => {
        try { await work(); return null } catch (error) { return error instanceof Error && 'code' in error ? error.code : 'unexpected' }
      }
      return { first, second, third, small, wrongSize: await failure(() => api.fetchMediaPage({ limit: 2, cursor: small.next_cursor }, signal())),
        foreignCursor: await failure(() => api.fetchMediaPage({ limit: 1, cursor: membership.next_cursor }, signal())),
        reverseCursor: await failure(() => api.fetchMediaLinksPage('photo', { limit: 1, cursor: small.next_cursor }, signal())) }
    })
    expect(result.first.data).toHaveLength(100); expect(result.second.data).toHaveLength(100)
    expect(result.third.data).toHaveLength(ids.length + 2 - 200); expect(result.third.next_cursor).toBeNull()
    const discovered = [...result.first.data, ...result.second.data, ...result.third.data]
    expect(new Set(discovered.map((value: { id: string }) => value.id)).size).toBe(ids.length + 2)
    expect(discovered.map((value: { id: string }) => value.id)).toEqual(expect.arrayContaining([...ids, 'photo', 'links']))
    expect(discovered.find((value: { id: string }) => value.id === 'photo')).toMatchObject({ revision: 7, mime_type: 'image/heic', verification: 'unverified', thumb_url: null, thumb_sha256: null })
    expect(discovered.find((value: { id: string }) => value.id === ids[0])).toMatchObject({ revision: 1, verification: 'unverified' })
    expect(result.small.data[0].id).toBe(ids[0])
    expect(result.wrongSize).toBe('INVALID_CURSOR'); expect(result.foreignCursor).toBe('INVALID_CURSOR'); expect(result.reverseCursor).toBe('INVALID_CURSOR')
    expect(JSON.stringify(discovered)).not.toContain('private-')

    const headers = await writeContext(page, application.url)
    expect((await page.request.post('/api/v1/auth/logout', { headers })).status()).toBe(200)
    expect((await page.request.post('/api/v1/auth/login', { headers: { Origin: application.url }, data: { username: 'owner', password } })).status()).toBe(200)
    const afterLogin = await page.evaluate(async (cursor) => {
      const modulePath = '/assets/media-sdk.js', api = await import(modulePath)
      return api.fetchMediaPage({ limit: 1, cursor }, new AbortController().signal)
    }, result.small.next_cursor)
    expect(afterLogin.data[0].id).toBe('.')
    expect(mediaRequests.every(({ method, path }) => method === 'GET' && !path.endsWith('/content') && !path.endsWith('/thumbnail'))).toBe(true)
  })

  test('returns an offline failure without scheduling a reconnect read', async ({ page, application, context }) => {
    await provision(page, application); await page.goto('/')
    await page.evaluate(async () => { const modulePath = '/assets/media-sdk.js'; await import(modulePath) })
    const requests: string[] = []
    page.on('request', (request) => {
      if (new URL(request.url()).pathname === '/api/v1/media') requests.push(request.method())
    })
    await context.setOffline(true)
    const code = await page.evaluate(async () => {
      const modulePath = '/assets/media-sdk.js', api = await import(modulePath)
      try { await api.fetchMediaPage({}, new AbortController().signal); return 'unexpected' }
      catch (error) { return error instanceof Error && 'code' in error ? error.code : 'unexpected' }
    })
    expect(code).toBe('NETWORK_ERROR'); expect(requests).toEqual(['GET'])
    await context.setOffline(false)
    await page.evaluate(() => window.dispatchEvent(new Event('online')))
    expect(requests).toEqual(['GET'])
    const current = await page.evaluate(async () => {
      const modulePath = '/assets/media-sdk.js', api = await import(modulePath)
      return api.fetchMediaPage({}, new AbortController().signal)
    })
    expect(current.data).toHaveLength(50); expect(requests).toEqual(['GET', 'GET'])
  })

  test('withholds a corrupted real page and never retries a read automatically', async ({ page, application }) => {
    await provision(page, application)
    let reads = 0, tamper = true
    await page.route((url) => url.pathname === '/api/v1/media', async (route) => {
      reads++
      const response = await route.fetch()
      if (!tamper) { await route.fulfill({ response }); return }
      tamper = false
      const body = await response.json()
      body.data[1].id = body.data[0].id
      await route.fulfill({ response, body: JSON.stringify(body) })
    })
    await page.goto('/')
    const code = await page.evaluate(async () => {
      const modulePath = '/assets/media-sdk.js', api = await import(modulePath)
      try { await api.fetchMediaPage({ limit: 2 }, new AbortController().signal); return 'unexpected' }
      catch (error) { return error instanceof Error && 'code' in error ? error.code : 'unexpected' }
    })
    expect(code).toBe('INVALID_RESPONSE'); expect(reads).toBe(1)
    await page.evaluate(() => window.dispatchEvent(new Event('online')))
    expect(reads).toBe(1)
    const current = await page.evaluate(async () => {
      const modulePath = '/assets/media-sdk.js', api = await import(modulePath)
      return api.fetchMediaPage({ limit: 2 }, new AbortController().signal)
    })
    expect(current.data).toHaveLength(2); expect(current.next_cursor).toBeTruthy(); expect(reads).toBe(2)
  })
})

test.describe('global Media attachment picker', () => {
  const firstID = '\0Media\r\n', exactID = 'zz/legacy\0😀\r\n', longID = '😀'.repeat(128)
  const ids = [firstID, exactID, longID, ...Array.from({ length: 61 }, (_, index) => `global-${String(index).padStart(3, '0')}`)]
  test.use({ applicationDatabaseFixture: { prepare(database) { prepareMediaFixture(database, 2, true, false, [], { ids }) } } })
  const exactMembership = (id: string) => `/api/v1/media-by-identity/links?${new URLSearchParams({ id })}`
  const choose = (id: string) => `${messages.media.pickerChoose}：${JSON.stringify(id)}`

  async function openPicker(page: Page) {
    await page.getByText(messages.media.picker, { exact: true }).click()
    await expect(page.getByRole('button', { name: choose(firstID) })).toBeVisible()
  }
  async function nextPage(page: Page) {
    const next = page.getByRole('navigation', { name: messages.media.pickerPagination }).getByRole('button', { name: messages.catalog.nextPage })
    const rows = page.getByRole('list', { name: messages.media.pickerList }).locator('li')
    const before = await rows.first().textContent()
    await expect(next).toBeEnabled(); await next.click()
    await expect.poll(async () => await page.getByText(messages.media.invalidRead, { exact: true }).isVisible() ||
      await rows.count() > 0 && await rows.first().textContent() !== before).toBe(true)
  }

  test('pages to an unlinked exact ID and re-reads both scopes after the listing changes', async ({ page, application }) => {
    await provision(page, application)
    const writes = watchDomainWrites(page, ['/api/v1/media']), reads: { path: string; id: string | null }[] = [], streams: string[] = []
    page.on('request', (request) => {
      const url = new URL(request.url())
      if (request.method() === 'GET' && url.pathname.startsWith('/api/v1/media')) reads.push({ path: url.pathname, id: url.searchParams.get('id') })
      if (request.method() === 'GET' && /\/(?:content|thumbnail)$/.test(url.pathname)) streams.push(request.url())
    })
    await page.goto(`/photos/attach?owner_type=item&owner_id=${empty}`)
    await page.getByRole('textbox', { name: messages.media.description }).fill('Keep this draft')
    expect((await (await page.request.get(exactMembership(exactID))).json()).data).toEqual([])
    await openPicker(page)
    for (let index = 0; index < 3; index++) {
      await nextPage(page)
      await expect(page.getByRole('navigation', { name: messages.media.pickerPagination })).toBeVisible()
    }
    const target = page.getByRole('button', { name: choose(exactID) })
    await expect(target).toBeVisible()
    const concurrent = await page.request.post(exactMembership(exactID), { headers: await writeContext(page, application.url, 1), data: { item_id: owner, owner_revision: 1 } })
    expect(concurrent.status()).toBe(200)
    await target.click()
    await expect(page.getByRole('textbox', { name: messages.media.mediaID })).toHaveValue(JSON.stringify(exactID))
    await expect(page.getByRole('heading', { name: messages.media.selectedMedia })).toBeVisible()
    await expect(page.getByText(`${messages.media.membershipCount}1`, { exact: true })).toBeVisible()
    await expect(page.getByRole('textbox', { name: messages.media.description })).toHaveValue('Keep this draft')
    expect(reads.filter((read) => read.path === '/api/v1/media-by-identity' && read.id === exactID)).toHaveLength(2)
    expect(reads.filter((read) => read.path === '/api/v1/media-by-identity/links' && read.id === exactID)).toHaveLength(1)
    await page.getByRole('button', { name: messages.media.reviewAttach }).click()
    const dialog = page.getByRole('alertdialog')
    await expect(dialog).toContainText(JSON.stringify(exactID)); expect(writes).toHaveLength(0)
    await dialog.getByRole('button', { name: messages.media.confirmAttach }).click()
    await expect(page.getByText(messages.mutations.done, { exact: true })).toBeVisible()
    expect(writes).toHaveLength(1); expect(writes[0]).toMatchObject({ path: '/api/v1/media-by-identity/links', method: 'POST', proof: '"2"' })
    expect(JSON.parse(writes[0]!.body!)).toEqual({ item_id: empty, owner_revision: 1, is_cover: false, sort_order: 0, description: 'Keep this draft' })
    expect(streams).toEqual([])
  })

  test('selects an exact discovered ID for actual Item, Unit and Location owners', async ({ page, application }) => {
    await provision(page, application)
    const writes = watchDomainWrites(page, ['/api/v1/media'])
    const scopes = [['item', empty], ['unit', 'legacy/unit\0😀'], ['location', 'tree']] as const
    for (const [kind, id] of scopes) {
      await page.goto(`/photos/attach?${new URLSearchParams({ owner_type: kind, owner_id: id })}`)
      await openPicker(page)
      await page.getByRole('button', { name: choose(firstID) }).click()
      await expect(page.getByRole('heading', { name: messages.media.selectedMedia })).toBeVisible()
      await page.getByRole('button', { name: messages.media.reviewAttach }).click()
      await page.getByRole('alertdialog').getByRole('button', { name: messages.media.confirmAttach }).click()
      await expect(page.getByText(messages.mutations.done, { exact: true })).toBeVisible()
    }
    expect(writes).toHaveLength(3)
    expect(writes.map((write) => write.proof)).toEqual(['"1"', '"2"', '"3"'])
    expect(writes.map((write) => JSON.parse(write.body!))).toEqual([
      { item_id: empty, owner_revision: 1, is_cover: false, sort_order: 0, description: '' },
      { item_unit_id: 'legacy/unit\0😀', owner_revision: 1, is_cover: false, sort_order: 0, description: '' },
      { location_id: 'tree', owner_revision: 1, is_cover: false, sort_order: 0, description: '' },
    ])
    const links = await (await page.request.get(exactMembership(firstID))).json()
    expect(links.data).toHaveLength(3)
    expect(links.data.map((link: { owner_type: string }) => link.owner_type).sort()).toEqual(['item', 'location', 'unit'])
  })

  test('withholds duplicate and cyclic live pages, then restarts without losing the draft', async ({ page, application }) => {
    await provision(page, application)
    let mode: 'duplicate' | 'cycle' | 'none' = 'duplicate', firstCursor: string | null = null, firstPageID = ''
    await page.route((url) => url.pathname === '/api/v1/media', async (route) => {
      const response = await route.fetch(), body = await response.json(), cursor = new URL(route.request().url()).searchParams.get('cursor')
      if (cursor === null) { firstCursor = body.next_cursor; firstPageID = body.data[0].id }
      else if (mode === 'duplicate') { body.data[0].id = firstPageID; mode = 'none' }
      else if (mode === 'cycle' && cursor !== firstCursor) { body.next_cursor = firstCursor; mode = 'none' }
      await route.fulfill({ response, body: JSON.stringify(body) })
    })
    const writes = watchDomainWrites(page, ['/api/v1/media'])
    await page.goto(`/photos/attach?owner_type=item&owner_id=${empty}`)
    await page.getByRole('textbox', { name: messages.media.description }).fill('Retained draft')
    await openPicker(page); await nextPage(page)
    await expect(page.getByText(messages.media.invalidRead, { exact: true })).toBeVisible()
    await expect(page.getByRole('list', { name: messages.media.pickerList })).toHaveCount(0)
    mode = 'cycle'
    await page.getByRole('button', { name: messages.catalog.firstPage }).click()
    await expect(page.getByRole('button', { name: choose(firstID) })).toBeVisible()
    await nextPage(page)
    await expect(page.getByRole('button', { name: choose('global-019') })).toBeVisible()
    await nextPage(page)
    await expect(page.getByText(messages.media.invalidRead, { exact: true })).toBeVisible()
    await expect(page.getByRole('list', { name: messages.media.pickerList })).toHaveCount(0)
    await page.getByRole('button', { name: messages.catalog.firstPage }).click()
    await expect(page.getByRole('button', { name: choose(firstID) })).toBeVisible()
    await expect(page.getByRole('textbox', { name: messages.media.description })).toHaveValue('Retained draft')
    expect(writes).toEqual([])
  })

  test('recovers from a failed read and does not replay a write after reconnecting', async ({ page, application, context }) => {
    await provision(page, application)
    let reads = 0
    await page.route((url) => url.pathname === '/api/v1/media', async (route) => { reads++; if (reads === 1) await route.abort('failed'); else await route.continue() })
    const writes = watchDomainWrites(page, ['/api/v1/media'])
    await page.goto(`/photos/attach?owner_type=item&owner_id=${empty}`)
    const description = page.getByRole('textbox', { name: messages.media.description })
    await description.fill('Offline draft')
    await page.getByText(messages.media.picker, { exact: true }).click()
    await expect(page.getByText(messages.locations.network, { exact: true })).toBeVisible()
    await expect(page.getByText(messages.media.pickerEmpty, { exact: true })).toHaveCount(0)
    await page.getByRole('button', { name: messages.media.pickerRefresh }).click()
    await expect(page.getByRole('button', { name: choose(firstID) })).toBeVisible()
    expect(reads).toBe(2)
    try {
      await context.setOffline(true)
      await expect(page.getByText(messages.media.pickerUnavailable, { exact: true })).toBeVisible()
      await expect(description).toHaveValue('Offline draft')
      await context.setOffline(false)
      await expect(page.getByRole('button', { name: choose(firstID) })).toBeEnabled()
      await expect(description).toHaveValue('Offline draft')
      expect(writes).toEqual([])
    } finally { await context.setOffline(false) }
  })

  test('purges a dirty draft when the picker receives a real expired-session response', async ({ page, application }) => {
    const password = await provision(page, application)
    await page.goto(`/photos/attach?owner_type=item&owner_id=${empty}`)
    await page.getByRole('textbox', { name: messages.media.description }).fill('Private picker draft')
    await openPicker(page)
    await page.context().clearCookies()
    await page.getByRole('button', { name: messages.media.pickerRefresh }).click()
    await expect(page.getByRole('heading', { name: messages.auth.loginHeading })).toBeVisible()
    await page.getByLabel(new RegExp(`^${messages.auth.username}`)).fill('owner')
    await page.getByLabel(new RegExp(`^${messages.auth.password}`)).fill(password)
    await page.getByRole('button', { name: messages.auth.login, exact: true }).click()
    await expect(page).toHaveURL(new RegExp(`/photos/attach\\?owner_type=item&owner_id=${empty}$`))
    await expect(page.getByRole('textbox', { name: messages.media.description })).toHaveValue('')
    await expect(page.getByRole('textbox', { name: messages.media.mediaID })).toHaveValue('')
  })

  test('purges the session when a delayed 401 arrives after the picker closes', async ({ page, application }) => {
    await provision(page, application)
    let report!: (status: number) => void, release!: () => void
    const responseReady = new Promise<number>((resolve) => { report = resolve })
    const released = new Promise<void>((resolve) => { release = resolve })
    await page.route((url) => url.pathname === '/api/v1/media', async (route) => {
      const response = await route.fetch(); report(response.status()); await released; await route.fulfill({ response })
    })
    await page.goto(`/photos/attach?owner_type=item&owner_id=${empty}`)
    await page.getByRole('textbox', { name: messages.media.description }).fill('Private delayed draft')
    await page.context().clearCookies()
    const summary = page.getByText(messages.media.picker, { exact: true })
    try {
      await summary.click()
      expect(await responseReady).toBe(401)
      await summary.click(); release()
      await expect(page.getByRole('heading', { name: messages.auth.loginHeading })).toBeVisible()
      expect(new URL(page.url()).pathname).toBe('/login')
    } finally { release() }
  })

  test('reconciles a lost selected attachment only with its original key after source removal', async ({ page, application }) => {
    const password = await provision(page, application)
    let attempts = 0, committedStatus = 0
    const writes: { key: string | undefined; proof: string | undefined; body: unknown }[] = []
    await page.route((url) => url.pathname === '/api/v1/media-by-identity/links' && url.searchParams.get('id') === firstID, async (route) => {
      if (route.request().method() !== 'POST') { await route.continue(); return }
      attempts++; writes.push({ key: route.request().headers()['idempotency-key'], proof: route.request().headers()['if-match'], body: route.request().postDataJSON() })
      if (attempts !== 1) { await route.continue(); return }
      committedStatus = (await route.fetch()).status(); await route.abort('failed')
    })
    await page.goto(`/photos/attach?owner_type=item&owner_id=${empty}`)
    await openPicker(page); await page.getByRole('button', { name: choose(firstID) }).click()
    await expect(page.getByRole('heading', { name: messages.media.selectedMedia })).toBeVisible()
    await page.getByRole('button', { name: messages.media.reviewAttach }).click()
    await page.getByRole('alertdialog').getByRole('button', { name: messages.media.confirmAttach }).click()
    await expect(page.getByRole('heading', { name: messages.mutations.uncertain })).toBeVisible()
    expect(committedStatus).toBe(200); expect(attempts).toBe(1)
    expect((await page.request.delete(`/api/v1/items/${empty}`, { headers: await writeContext(page, application.url, 2) })).status()).toBe(200)
    expect((await page.request.post('/api/v1/auth/login', { headers: { Origin: application.url }, data: { username: 'owner', password } })).status()).toBe(200)
    await page.getByRole('alertdialog').getByRole('button', { name: messages.mutations.readCurrent }).click()
    await page.getByRole('alertdialog').getByRole('button', { name: messages.mutations.cancel }).click()
    await expect(page.getByText(messages.media.missing, { exact: true })).toBeVisible()
    await page.evaluate(() => window.dispatchEvent(new Event('online')))
    expect(attempts).toBe(1)
    await page.getByRole('button', { name: messages.mutations.openOriginal }).click()
    await page.getByRole('alertdialog').getByRole('button', { name: messages.mutations.retryOriginal }).click()
    await expect(page.getByText(messages.mutations.done, { exact: true })).toBeVisible()
    expect(attempts).toBe(2); expect(writes[0]?.key).toBeTruthy(); expect(writes[1]?.key).toBe(writes[0]?.key)
    expect(writes.map(({ proof, body }) => ({ proof, body }))).toEqual(Array(2).fill({ proof: '"1"', body: { item_id: empty, owner_revision: 1, is_cover: false, sort_order: 0, description: '' } }))
  })

  test('supports keyboard selection and accessible enlarged light/dark pages without image requests', async ({ page, application }, info) => {
    await provision(page, application)
    const streams: string[] = []
    page.on('request', (request) => { if (request.method() === 'GET' && /\/(?:content|thumbnail)$/.test(new URL(request.url()).pathname)) streams.push(request.url()) })
    await page.goto(`/photos/attach?owner_type=item&owner_id=${empty}`)
    if (info.project.name === 'small-phone') await page.addStyleTag({ content: 'html { font-size: 200% }' })
    const summary = page.getByText(messages.media.picker, { exact: true })
    await summary.focus(); await expect(summary).toBeFocused(); await page.keyboard.press('Enter')
    await expect(page.getByRole('button', { name: choose(firstID) })).toBeVisible()
    for (let index = 0; index < 3; index++) { await nextPage(page); await expect(page.getByRole('navigation', { name: messages.media.pickerPagination })).toBeVisible() }
    const target = page.getByRole('button', { name: choose(longID) })
    await expect(target).toBeVisible()
    for (const colorScheme of ['light', 'dark'] as const) {
      await page.emulateMedia({ colorScheme }); await expect(page.locator('html')).toHaveAttribute('data-theme', colorScheme)
      await target.scrollIntoViewIfNeeded(); await expect(target).toBeInViewport()
      expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze()).violations).toEqual([])
      await noOverflow(page)
      if (info.project.name === 'small-phone') expect(await page.evaluate(() => document.documentElement.scrollWidth <= 320)).toBe(true)
      expect(await target.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)
      if (info.project.name === 'small-phone' || info.project.name === 'desktop') await page.screenshot({ path: info.outputPath(`media-picker-${colorScheme}.png`), fullPage: true })
    }
    await target.focus(); await page.keyboard.press('Enter')
    await expect(summary).toBeFocused()
    await expect(page.getByRole('heading', { name: messages.media.selectedMedia })).toBeVisible()
    await expect(page.getByRole('textbox', { name: messages.media.mediaID })).toHaveValue(JSON.stringify(longID))
    expect(streams).toEqual([]); expect(await page.locator('img').count()).toBe(0)
  })
})

test.describe('large selected Media picker review', () => {
  const id = 'legacy/\0😀\r\n'
  test.use({ applicationDatabaseFixture: { prepare(database) { prepareMediaFixture(database, 205, false, false, [], { ids: [id], countedID: id }) } } })
  test('completes 205 owner links and 206 Media links after discovery', async ({ page, application }) => {
    await provision(page, application)
    const writes = watchDomainWrites(page, ['/api/v1/media'])
    await page.goto(`/photos/attach?owner_type=item&owner_id=${owner}`)
    await page.getByText(messages.media.picker, { exact: true }).click()
    await page.getByRole('button', { name: `${messages.media.pickerChoose}：${JSON.stringify(id)}` }).click()
    await expect(page.getByRole('heading', { name: messages.media.selectedMedia })).toBeVisible()
    await expect(page.getByText(`${messages.media.ownerLinkCount}205`, { exact: true })).toBeVisible()
    await expect(page.getByText(`${messages.media.membershipCount}206`, { exact: true })).toBeVisible()
    await expect(page.getByText(messages.media.alreadyLinked, { exact: true })).toBeVisible()
    await page.getByRole('button', { name: messages.media.reviewAttach }).click()
    await expect(page.getByRole('alertdialog')).toContainText(JSON.stringify(id)); expect(writes).toHaveLength(0)
    await page.getByRole('alertdialog').getByRole('button', { name: messages.media.confirmAttach }).click()
    await expect(page.getByText(messages.mutations.done, { exact: true })).toBeVisible()
    expect(writes).toHaveLength(1); expect(writes[0]).toMatchObject({ path: '/api/v1/media-by-identity/links', proof: '"1"' })
  })
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

  test('shows and unlinks the last real owner link after completing all signed pages', async ({ page, application }) => {
    await provision(page, application)
    const requests: string[] = []
    page.on('request', (request) => {
      const url = new URL(request.url())
      if (request.method() === 'GET' && url.pathname === '/api/v1/media/links') requests.push(url.search)
    })
    await page.goto(`/photos?owner_type=item&owner_id=${owner}`)
    await expect(page.getByText(`${messages.media.total}205`)).toBeVisible()
    expect(requests).toHaveLength(3)
    await expect(page.getByRole('list', { name: messages.media.list }).getByRole('listitem')).toHaveCount(205)
    const last = page.getByRole('button', { name: `${messages.media.remove} ${JSON.stringify('owner-204')}` })
    await last.scrollIntoViewIfNeeded(); await last.click()
    await page.getByRole('alertdialog').getByRole('button', { name: messages.media.confirmRemove }).click()
    await expect(page.getByText(`${messages.media.total}204`)).toBeVisible()
    await expect(page.getByRole('button', { name: `${messages.media.remove} ${JSON.stringify('owner-204')}` })).toHaveCount(0)
  })

  test('reviews and attaches against all 205 owner links and 206 Media links', async ({ page, application }) => {
    await provision(page, application)
    const writes = watchDomainWrites(page, ['/api/v1/media'])
    await page.goto(`/photos/attach?owner_type=item&owner_id=${owner}`)
    await lookupExistingMedia(page, 'photo')
    await expect(page.getByText(`${messages.media.ownerLinkCount}205`, { exact: true })).toBeVisible()
    await expect(page.getByText(`${messages.media.membershipCount}206`, { exact: true })).toBeVisible()
    await page.getByRole('button', { name: messages.media.reviewAttach }).click()
    const dialog = page.getByRole('alertdialog')
    await expect(dialog).toContainText('205'); await expect(dialog).toContainText('206')
    await dialog.getByRole('button', { name: messages.media.confirmAttach }).click()
    await expect(page.getByText(messages.mutations.done, { exact: true })).toBeVisible()
    expect(writes).toHaveLength(1); expect(writes[0]).toMatchObject({ method: 'POST', path: '/api/v1/media/photo/links', proof: '"7"' })
    expect(JSON.parse(writes[0]!.body!)).toEqual({ item_id: owner, owner_revision: 1, is_cover: false, sort_order: 0, description: '' })
    await expect(page.getByText(`${messages.media.total}206`, { exact: true })).toBeVisible()
  })

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

  test('opens retained path-unaddressable Unit and reserved Location owners in the real interface', async ({ page, application }) => {
    await provision(page, application)
    const itemID = 'legacy/item\0😀', unitID = 'legacy/unit\0😀'
    await page.goto(`/photos?owner_type=item&owner_id=${encodeURIComponent(itemID)}`)
    await expect(page.getByRole('heading', { name: 'Legacy owner' })).toBeVisible()
    await expect(page.getByRole('link', { name: messages.media.backItems })).toHaveAttribute('href', '/items')
    await expect(page.getByText(`${messages.media.total}1`)).toBeVisible()
    await page.goto(`/photos?owner_type=unit&owner_id=${encodeURIComponent(unitID)}`)
    await expect(page.getByRole('heading', { name: 'LEGACY-ASSET' })).toBeVisible()
    await expect(page.getByText(`${messages.media.total}1`)).toBeVisible()
    await expect(page.getByRole('link', { name: messages.media.backItems })).toHaveAttribute('href', '/items')
    await page.getByRole('button', { name: `${messages.media.remove} ${JSON.stringify('legacy-unit-link')}` }).click()
    await page.getByRole('alertdialog').getByRole('button', { name: messages.media.confirmRemove }).click()
    await expect(page.getByText(messages.mutations.done, { exact: true })).toBeVisible()
    await expect(page.getByText(messages.media.empty, { exact: true })).toBeVisible()
    await page.goto('/photos?owner_type=location&owner_id=tree')
    await expect(page.getByRole('heading', { name: 'Legacy room' })).toBeVisible()
    await expect(page.getByRole('link', { name: messages.media.backLocations })).toHaveAttribute('href', '/locations')
    await expect(page.getByText(`${messages.media.total}1`)).toBeVisible()
  })

  test('edits an addressable link of a path-unaddressable Unit using its independent proof', async ({ page, application }) => {
    await provision(page, application)
    const unitID = 'legacy/unit\0😀', writes = watchDomainWrites(page, ['/api/v1/media'])
    await page.goto(`/photos?owner_type=unit&owner_id=${encodeURIComponent(unitID)}`)
    await page.getByRole('link', { name: `${messages.media.edit} ${JSON.stringify('legacy-unit-link')}` }).click()
    await expect(page.getByRole('heading', { name: 'LEGACY-ASSET' })).toBeVisible()
    await page.getByRole('textbox', { name: messages.media.description }).fill('Retained unit caption')
    await page.getByRole('button', { name: messages.media.reviewEdit }).click()
    await page.getByRole('alertdialog').getByRole('button', { name: messages.media.confirmEdit }).click()
    await expect(page.getByText(messages.mutations.done, { exact: true })).toBeVisible()
    expect(writes).toHaveLength(1); expect(writes[0]).toMatchObject({ method: 'PATCH', path: '/api/v1/media/links/legacy-unit-link', proof: '"1"', body: JSON.stringify({ description: 'Retained unit caption' }) })
    await expect(page.getByText('"Retained unit caption"', { exact: true })).toBeVisible()
  })

  test('attaches existing Media to a retained Unit and reserved Location using their actual independent proofs', async ({ page, application }) => {
    await provision(page, application)
    const unitID = 'legacy/unit\0😀', writes = watchDomainWrites(page, ['/api/v1/media'])
    await page.goto(`/photos?owner_type=unit&owner_id=${encodeURIComponent(unitID)}`)
    await page.getByRole('link', { name: messages.media.attach, exact: true }).click()
    await lookupExistingMedia(page, 'links')
    await page.getByRole('button', { name: messages.media.reviewAttach }).click()
    await page.getByRole('alertdialog').getByRole('button', { name: messages.media.confirmAttach }).click()
    await expect(page.getByText(messages.mutations.done, { exact: true })).toBeVisible()
    await page.goto('/photos?owner_type=location&owner_id=tree')
    await page.getByRole('link', { name: messages.media.attach, exact: true }).click()
    await lookupExistingMedia(page, 'links')
    await page.getByRole('button', { name: messages.media.reviewAttach }).click()
    await page.getByRole('alertdialog').getByRole('button', { name: messages.media.confirmAttach }).click()
    await expect(page.getByText(messages.mutations.done, { exact: true })).toBeVisible()
    expect(writes.map((write) => write.proof)).toEqual(['"3"', '"4"'])
    expect(writes.map((write) => JSON.parse(write.body!))).toEqual([
      { item_unit_id: unitID, owner_revision: 1, is_cover: false, sort_order: 0, description: '' },
      { location_id: 'tree', owner_revision: 1, is_cover: false, sort_order: 0, description: '' },
    ])
  })

  test('resolves real path-unaddressable Item and Unit plus reserved Location owners from actual source', async ({ page, application }) => {
    await provision(page, application)
    const requests: string[] = [], itemIDs: string[] = [], unitIDs: string[] = []
    page.on('request', (request) => {
      if (request.method() !== 'GET') return
      const url = new URL(request.url())
      requests.push(url.pathname)
      if (url.pathname === '/api/v1/items-by-identity') itemIDs.push(...url.searchParams.getAll('id'))
      if (url.pathname === '/api/v1/units') unitIDs.push(...url.searchParams.getAll('id'))
    })
    await page.goto('/')
    const result = await page.evaluate(async () => {
      const modulePath = '/assets/media-sdk.js', api = await import(modulePath), signal = () => new AbortController().signal
      return {
        item: await api.readOwnerMedia({ kind: 'item', id: 'legacy/item\0😀' }, signal()),
        itemLookup: await api.fetchItemByIdentity('legacy/item\0😀', signal()),
        unit: await api.readOwnerMedia({ kind: 'unit', id: 'legacy/unit\0😀' }, signal()),
        unitLookup: await api.fetchUnitByIdentity('legacy/unit\0😀', signal()),
        location: await api.readOwnerMedia({ kind: 'location', id: 'tree' }, signal()),
      }
    })
    expect(result.item.owner).toMatchObject({ kind: 'item', value: { id: 'legacy/item\0😀', revision: 1 } })
    expect(result.item.links).toMatchObject([{ id: 'legacy-item-link', owner_id: 'legacy/item\0😀' }])
    expect(result.itemLookup).toEqual(result.item.owner.value)
    expect(result.unit.owner).toMatchObject({ kind: 'unit', value: { id: 'legacy/unit\0😀', item_id: 'legacy/item\0😀', revision: 1 } })
    expect(result.unit.links).toMatchObject([{ id: 'legacy-unit-link', owner_id: 'legacy/unit\0😀' }])
    expect(result.unitLookup).toEqual(result.unit.owner.value)
    expect(result.location.owner).toMatchObject({ kind: 'location', value: { id: 'tree', revision: 1 } })
    expect(result.location.links).toMatchObject([{ id: 'legacy-location-link', owner_id: 'tree' }])
    expect(requests.filter((path) => path === '/api/v1/items')).toHaveLength(0)
    expect(requests.filter((path) => path === '/api/v1/items-by-identity')).toHaveLength(3)
    expect(itemIDs).toEqual(['legacy/item\0😀', 'legacy/item\0😀', 'legacy/item\0😀'])
    expect(requests.filter((path) => path === '/api/v1/units')).toHaveLength(3)
    expect(unitIDs).toEqual(['legacy/unit\0😀', 'legacy/unit\0😀', 'legacy/unit\0😀'])
    expect(requests.filter((path) => path === '/api/v1/locations/tree')).toHaveLength(2)
    expect(requests.filter((path) => path === '/api/v1/media/links')).toHaveLength(3)
  })

  test('withholds exact Item review when a real link changes between its source reads', async ({ page, application }) => {
    await provision(page, application)
    const id = 'legacy/item\0😀'
    let changed = false, patchStatus = 0
    await page.route((url) => url.pathname === '/api/v1/media/links' && url.searchParams.get('owner_id') === id, async (route) => {
      const response = await route.fetch()
      if (!changed) {
        changed = true
        const headers = await writeContext(page, application.url, 1)
        patchStatus = (await page.request.patch('/api/v1/media/links/legacy-item-link', { headers, data: { description: 'Changed during exact Item review' } })).status()
      }
      await route.fulfill({ response })
    })
    await page.goto('/')
    const result = await page.evaluate(async (id) => {
      const modulePath = '/assets/media-sdk.js', api = await import(modulePath), signal = () => new AbortController().signal
      let first: unknown
      try { await api.readOwnerMedia({ kind: 'item', id }, signal()); first = 'unexpected' }
      catch (error) { first = error instanceof Error && 'code' in error ? error.code : 'unexpected' }
      return { first, current: await api.readOwnerMedia({ kind: 'item', id }, signal()) }
    }, id)
    expect(changed).toBe(true); expect(patchStatus).toBe(200); expect(result.first).toBe('INVALID_RESPONSE')
    expect(result.current.owner).toMatchObject({ kind: 'item', value: { id, revision: 2 } })
    expect(result.current.links).toContainEqual(expect.objectContaining({ id: 'legacy-item-link', revision: 2, description: 'Changed during exact Item review' }))
  })
})

test('freezes complete review scopes and delivers attach, edit and unlink commands with the actual Go server', async ({ page, application }) => {
  await provision(page, application)
  await page.goto('/')
  const result = await page.evaluate(async ({ empty }) => {
    const modulePath = '/assets/media-sdk.js', api = await import(modulePath), signal = () => new AbortController().signal
    const csrf = (await (await fetch('/api/v1/auth/csrf')).json()).token as string
    const owner = await api.readOwnerMedia({ kind: 'item', id: empty }, signal())
    const membership = await api.readMediaMembership('links', signal())
    const body = { is_cover: true, sort_order: 0, description: 'Original review' }
    const attach = api.freezeMediaCommand({ kind: 'attach', owner, membership, body })
    owner.owner.value.revision = 99; membership.media.revision = 99; body.description = 'Changed after review'
    const frozen = Object.isFrozen(attach) && Object.isFrozen(attach.owner.links) && Object.isFrozen(attach.membership.media)
    const created = await api.sendMediaCommand(attach, csrf, signal())
    const createReplay = await api.sendMediaCommand(attach, csrf, signal())
    const beforeEdit = await api.readOwnerMedia({ kind: 'item', id: empty }, signal())
    const patch = api.freezeMediaCommand({ kind: 'patch-link', source: beforeEdit, linkID: created.id, body: { description: 'Reviewed é\nedit' } })
    const changed = await api.sendMediaCommand(patch, csrf, signal())
    const editReplay = await api.sendMediaCommand(patch, csrf, signal())
    const beforeUnlink = await api.readOwnerMedia({ kind: 'item', id: empty }, signal())
    const unlink = api.freezeMediaCommand({ kind: 'unlink', source: beforeUnlink, linkID: created.id })
    const removed = await api.sendMediaCommand(unlink, csrf, signal())
    const unlinkReplay = await api.sendMediaCommand(unlink, csrf, signal())
    return { frozen, created, createReplay, changed, editReplay, removed, unlinkReplay,
      after: await api.readOwnerMedia({ kind: 'item', id: empty }, signal()),
      membership: await api.readMediaMembership('links', signal()), hasCSRF: Object.hasOwn(attach, 'csrfToken') }
  }, { empty })
  expect(result.frozen).toBe(true); expect(result.hasCSRF).toBe(false)
  expect(result.created).toMatchObject({ owner_id: empty, media_id: 'links', revision: 1, is_cover: true, sort_order: 0, description: 'Original review' })
  expect(result.createReplay).toEqual(result.created)
  expect(result.changed).toMatchObject({ id: result.created.id, revision: 2, description: 'Reviewed é\nedit' })
  expect(result.editReplay).toEqual(result.changed)
  expect(result.removed).toMatchObject({ ok: true, revision: 3 }); expect(result.unlinkReplay).toEqual(result.removed)
  expect(result.after.links).toEqual([])
  expect(result.membership.media).toMatchObject({ id: 'links', revision: 6, verification: 'unverified', thumb_url: null })
  expect(result.membership.links).toMatchObject([{ id: 'reserved', owner_id: sibling }])
})

test.describe('exact retained Media SDK identities', () => {
  const ids = ['.', '..', 'legacy/photo', 'legacy\\photo', '\0', 'legacy/\0😀\r\n', 'percent%2F/plus+ space?&=##', '😀'.repeat(127) + '/']
  test.use({ applicationDatabaseFixture: { prepare(database) { prepareMediaFixture(database, 0, false, false, [], { ids }) } } })

  test('reads and attaches each exact identity with actual source proofs and explicit same-key reconciliation', async ({ page, application }) => {
    await provision(page, application)
    const writes: { query: [string, string][]; proof: string | undefined; key: string | undefined; body: unknown }[] = []
    page.on('request', (request) => {
      const url = new URL(request.url())
      if (url.pathname !== '/api/v1/media-by-identity/links' || request.method() !== 'POST') return
      writes.push({ query: Array.from(url.searchParams), proof: request.headers()['if-match'], key: request.headers()['idempotency-key'], body: request.postDataJSON() })
    })
    await page.goto('/')
    const result = await page.evaluate(async ({ ids, empty }) => {
      const modulePath = '/assets/media-sdk.js', api = await import(modulePath), signal = () => new AbortController().signal
      const csrf = (await (await fetch('/api/v1/auth/csrf')).json()).token as string
      const decisions = []
      for (const id of ids) {
        const media = await api.fetchMedia(id, signal()), item = await api.fetchItem(empty, signal())
        const context = { csrfToken: csrf, idempotencyKey: crypto.randomUUID(), signal: signal(), media, owner: { kind: 'item', value: item } }
        const created = await api.attachMedia(id, { description: 'Exact retained Media' }, context)
        const replay = await api.attachMedia(id, { description: 'Exact retained Media' }, context)
        const membership = await api.fetchMediaLinksPage(id, {}, signal())
        decisions.push({ media, item, created, replay, membership })
      }
      return decisions
    }, { ids, empty })
    expect(result).toHaveLength(ids.length); expect(writes).toHaveLength(ids.length * 2)
    for (const [index, id] of ids.entries()) {
      const decision = result[index]!, requests = writes.slice(index * 2, index * 2 + 2)
      expect(decision.media).toMatchObject({ id, revision: 1, verification: 'unverified', thumb_url: null, thumb_sha256: null })
      expect(decision.item.revision).toBe(index + 1)
      expect(decision.created).toMatchObject({ media_id: id, owner_type: 'item', owner_id: empty, revision: 1, description: 'Exact retained Media' })
      expect(decision.replay).toEqual(decision.created)
      expect(decision.membership.data).toMatchObject([{ id: decision.created.id, media_id: id, media: { id, revision: 2 } }])
      expect(decision.membership.next_cursor).toBeNull()
      expect(requests.map(({ query, proof, body }) => ({ query, proof, body }))).toEqual(Array(2).fill({ query: [['id', id]], proof: '"1"',
        body: { item_id: empty, owner_revision: index + 1, description: 'Exact retained Media' } }))
      expect(requests[0]?.key).toBeTruthy(); expect(requests[1]?.key).toBe(requests[0]?.key)
    }
    expect(JSON.stringify(result)).not.toContain('private-retained-media')
  })

  test('attaches an exact Media identity to real Item, Unit and Location owners', async ({ page, application }) => {
    await provision(page, application); await page.goto('/')
    const id = 'legacy/\0😀\r\n'
    const result = await page.evaluate(async ({ id, empty }) => {
      const modulePath = '/assets/media-sdk.js', api = await import(modulePath), signal = () => new AbortController().signal
      const csrf = (await (await fetch('/api/v1/auth/csrf')).json()).token as string
      const context = () => ({ csrfToken: csrf, idempotencyKey: crypto.randomUUID(), signal: signal() })
      const location = await api.createLocation({ name: 'Exact Media room', code: 'EXACT-MEDIA' }, context())
      const item = await api.fetchItem(empty, signal())
      const unit = await api.createUnit(empty, { location_id: null }, { ...context(), expectedRevision: item.revision })
      const currentItem = await api.fetchItem(empty, signal())
      const owners = [{ kind: 'item', value: currentItem }, { kind: 'unit', value: unit }, { kind: 'location', value: location }]
      const links = []
      for (const owner of owners) {
        const media = await api.fetchMedia(id, signal())
        links.push(await api.attachMedia(id, {}, { ...context(), media, owner }))
      }
      return { owners, links, media: await api.fetchMedia(id, signal()), membership: await api.fetchMediaLinksPage(id, {}, signal()) }
    }, { id, empty })
    expect(result.links.map((link: { owner_type: string; owner_id: string; media_id: string; revision: number }) => ({
      kind: link.owner_type, id: link.owner_id, mediaID: link.media_id, revision: link.revision,
    }))).toEqual(result.owners.map((owner: { kind: string; value: { id: string } }) => ({ kind: owner.kind, id: owner.value.id, mediaID: id, revision: 1 })))
    expect(result.media).toMatchObject({ id, revision: 4, verification: 'unverified', thumb_url: null })
    expect(result.membership.data).toHaveLength(3)
  })

  test('reconciles a committed lost exact-identity attachment after owner deletion and fresh login', async ({ page, application }) => {
    const password = await provision(page, application), id = 'legacy/\0😀\r\n'
    const keys: (string | undefined)[] = [], proofs: (string | undefined)[] = [], bodies: unknown[] = []
    let committedStatus = 0
    await page.route((url) => url.pathname === '/api/v1/media-by-identity/links' && url.searchParams.get('id') === id, async (route) => {
      if (route.request().method() !== 'POST') { await route.continue(); return }
      keys.push(route.request().headers()['idempotency-key']); proofs.push(route.request().headers()['if-match']); bodies.push(route.request().postDataJSON())
      if (keys.length > 1) { await route.continue(); return }
      committedStatus = (await route.fetch()).status(); await route.abort('failed')
    })
    await page.goto('/')
    const state = await page.evaluateHandle(async ({ id, empty }) => {
      const modulePath = '/assets/media-sdk.js', api = await import(modulePath), signal = () => new AbortController().signal
      const csrf = (await (await fetch('/api/v1/auth/csrf')).json()).token as string
      const media = await api.fetchMedia(id, signal()), item = await api.fetchItem(empty, signal())
      return { api, csrf, media, owner: { kind: 'item', value: item }, key: crypto.randomUUID() }
    }, { id, empty })
    try {
      const first = await state.evaluate(async ({ api, csrf, media, owner, key }) => {
        try { await api.attachMedia(media.id, {}, { csrfToken: csrf, idempotencyKey: key, signal: new AbortController().signal, media, owner }); return 'unexpected' }
        catch (error) { return error instanceof Error && 'code' in error ? error.code : 'unexpected' }
      })
      expect(first).toBe('NETWORK_ERROR'); expect(committedStatus).toBe(200); expect(keys).toHaveLength(1)
      const before = await state.evaluate(async ({ api, media }) => ({
        media: await api.fetchMedia(media.id, new AbortController().signal), membership: await api.fetchMediaLinksPage(media.id, {}, new AbortController().signal),
      }))
      expect(before.media.revision).toBe(2); expect(before.membership.data).toHaveLength(1)
      expect((await page.request.delete(`/api/v1/items/${empty}`, { headers: await writeContext(page, application.url, 2) })).status()).toBe(200)
      expect((await page.request.post('/api/v1/auth/login', { headers: { Origin: application.url }, data: { username: 'owner', password } })).status()).toBe(200)
      const csrf = (await (await page.request.get('/api/v1/auth/csrf')).json()).token as string
      await page.evaluate(() => window.dispatchEvent(new Event('online'))); expect(keys).toHaveLength(1)
      const replay = await state.evaluate(async ({ api, media, owner, key }, csrf) => api.attachMedia(media.id, {}, {
        csrfToken: csrf, idempotencyKey: key, signal: new AbortController().signal, media, owner,
      }), csrf)
      expect(replay).toMatchObject({ media_id: id, owner_id: empty, revision: 1 })
      expect(keys).toHaveLength(2); expect(keys[0]).toBeTruthy(); expect(keys[1]).toBe(keys[0])
      expect(proofs).toEqual(['"1"', '"1"']); expect(bodies).toEqual(Array(2).fill({ item_id: empty, owner_revision: 1 }))
      const after = await state.evaluate(async ({ api, media }) => ({
        media: await api.fetchMedia(media.id, new AbortController().signal), membership: await api.fetchMediaLinksPage(media.id, {}, new AbortController().signal),
      }))
      expect(after.media.revision).toBe(3); expect(after.membership).toEqual({ data: [], next_cursor: null })
    } finally { await state.dispose() }
  })

  test('does not replay an offline exact-identity attachment on reconnect', async ({ page, application }) => {
    await provision(page, application); await page.goto('/')
    const id = 'legacy/\0😀\r\n', writes: string[] = []
    page.on('request', (request) => { if (request.method() === 'POST' && new URL(request.url()).pathname === '/api/v1/media-by-identity/links') writes.push(request.headers()['idempotency-key'] ?? '') })
    const state = await page.evaluateHandle(async ({ id, empty }) => {
      const modulePath = '/assets/media-sdk.js', api = await import(modulePath), signal = () => new AbortController().signal
      const csrf = (await (await fetch('/api/v1/auth/csrf')).json()).token as string
      return { api, csrf, media: await api.fetchMedia(id, signal()), owner: { kind: 'item', value: await api.fetchItem(empty, signal()) }, key: crypto.randomUUID() }
    }, { id, empty })
    try {
      await page.context().setOffline(true)
      const first = await state.evaluate(async ({ api, csrf, media, owner, key }) => {
        try { await api.attachMedia(media.id, {}, { csrfToken: csrf, idempotencyKey: key, signal: new AbortController().signal, media, owner }); return 'unexpected' }
        catch (error) { return error instanceof Error && 'code' in error ? error.code : 'unexpected' }
      })
      expect(first).toBe('NETWORK_ERROR'); expect(writes).toHaveLength(1)
      await page.context().setOffline(false)
      const before = await state.evaluate(async ({ api, media }) => api.fetchMediaLinksPage(media.id, {}, new AbortController().signal))
      expect(before).toEqual({ data: [], next_cursor: null }); expect(writes).toHaveLength(1)
      const retry = await state.evaluate(async ({ api, csrf, media, owner, key }) => api.attachMedia(media.id, {}, {
        csrfToken: csrf, idempotencyKey: key, signal: new AbortController().signal, media, owner,
      }))
      expect(retry).toMatchObject({ media_id: id, owner_id: empty, revision: 1 }); expect(writes).toHaveLength(2); expect(writes[1]).toBe(writes[0])
    } finally { await page.context().setOffline(false); await state.dispose() }
  })
})

test.describe('large exact Media SDK membership', () => {
  const id = 'large/\0😀', ids = [id]
  test.use({ applicationDatabaseFixture: { prepare(database) { prepareMediaFixture(database, 205, false, false, [], { ids, countedID: id }) } } })

  test('reads all actual signed pages without allowing the cursor to cross Media scopes', async ({ page, application }) => {
    await provision(page, application)
    const queries: [string, string][][] = []
    page.on('request', (request) => {
      const url = new URL(request.url())
      if (request.method() === 'GET' && url.pathname === '/api/v1/media-by-identity/links') queries.push(Array.from(url.searchParams))
    })
    await page.goto('/')
    const result = await page.evaluate(async ({ id }) => {
      const modulePath = '/assets/media-sdk.js', api = await import(modulePath), signal = () => new AbortController().signal
      const first = await api.fetchMediaLinksPage(id, { limit: 100 }, signal())
      const second = await api.fetchMediaLinksPage(id, { limit: 100, cursor: first.next_cursor }, signal())
      const third = await api.fetchMediaLinksPage(id, { limit: 100, cursor: second.next_cursor }, signal())
      let wrongScope: unknown
      try { await api.fetchMediaLinksPage('photo', { limit: 100, cursor: first.next_cursor }, signal()); wrongScope = 'unexpected' }
      catch (error) { wrongScope = error instanceof Error && 'code' in error ? error.code : 'unexpected' }
      return { first, second, third, wrongScope }
    }, { id })
    expect(result.first.data).toHaveLength(100); expect(result.second.data).toHaveLength(100); expect(result.third.data).toHaveLength(6)
    expect(result.third.next_cursor).toBeNull(); expect(result.wrongScope).toBe('INVALID_CURSOR')
    const links = [...result.first.data, ...result.second.data, ...result.third.data]
    expect(new Set(links.map((link: { id: string }) => link.id)).size).toBe(206)
    expect(links.every((link: { media_id: string; media: { id: string; revision: number } }) => link.media_id === id && link.media.id === id && link.media.revision === 1)).toBe(true)
    expect(queries).toHaveLength(3)
    const parsed = queries.map((query) => new URLSearchParams(query))
    expect(parsed.map((query) => query.get('id'))).toEqual([id, id, id])
    expect(parsed.map((query) => query.get('limit'))).toEqual(['100', '100', '100'])
    expect(parsed[0]?.has('cursor')).toBe(false); expect(parsed[1]?.get('cursor')).toBe(result.first.next_cursor); expect(parsed[2]?.get('cursor')).toBe(result.second.next_cursor)
  })

  test('brackets complete exact Media membership and freezes original proofs before attachment', async ({ page, application }) => {
    await provision(page, application)
    const writes: { query: [string, string][]; proof: string | undefined; key: string | undefined; body: unknown }[] = []
    page.on('request', (request) => {
      const url = new URL(request.url())
      if (request.method() === 'POST' && url.pathname === '/api/v1/media-by-identity/links') writes.push({
        query: Array.from(url.searchParams), proof: request.headers()['if-match'], key: request.headers()['idempotency-key'], body: request.postDataJSON(),
      })
    })
    await page.goto('/')
    const result = await page.evaluate(async ({ id, owner }) => {
      const modulePath = '/assets/media-sdk.js', api = await import(modulePath), signal = () => new AbortController().signal
      const csrf = (await (await fetch('/api/v1/auth/csrf')).json()).token as string
      const selectedOwner = await api.readOwnerMedia({ kind: 'item', id: owner }, signal())
      const membership = await api.readMediaMembership(id, signal())
      const command = api.freezeMediaCommand({ kind: 'attach', owner: selectedOwner, membership, body: { is_cover: false, sort_order: 0, description: 'Complete exact scope' } })
      const frozen = Object.isFrozen(command) && Object.isFrozen(command.owner.links) && Object.isFrozen(command.membership.media)
      const created = await api.sendMediaCommand(command, csrf, signal()), replay = await api.sendMediaCommand(command, csrf, signal())
      return { owner: selectedOwner, membership, frozen, created, replay, current: await api.readMediaMembership(id, signal()) }
    }, { id, owner })
    expect(result.owner.links).toHaveLength(205); expect(result.membership.links).toHaveLength(206)
    expect(result.membership.media).toMatchObject({ id, revision: 1 }); expect(result.frozen).toBe(true)
    expect(result.created).toMatchObject({ media_id: id, owner_type: 'item', owner_id: owner, revision: 1, description: 'Complete exact scope' })
    expect(result.replay).toEqual(result.created)
    expect(result.current.media).toMatchObject({ id, revision: 2 }); expect(result.current.links).toHaveLength(207)
    expect(result.current.links).toContainEqual(expect.objectContaining({ id: result.created.id, media_id: id }))
    expect(writes.map(({ query, proof, body }) => ({ query, proof, body }))).toEqual(Array(2).fill({ query: [['id', id]], proof: '"1"',
      body: { item_id: owner, owner_revision: 1, is_cover: false, sort_order: 0, description: 'Complete exact scope' } }))
    expect(writes[0]?.key).toBeTruthy(); expect(writes[1]?.key).toBe(writes[0]?.key)
  })

  test('reviews a rendered attachment against both complete exact Media scopes', async ({ page, application }) => {
    await provision(page, application)
    const writes: { query: [string, string][]; proof: string | undefined; key: string | undefined; body: unknown }[] = [], membershipQueries: [string, string][][] = []
    page.on('request', (request) => {
      const url = new URL(request.url())
      if (url.pathname !== '/api/v1/media-by-identity/links') return
      if (request.method() === 'GET') membershipQueries.push(Array.from(url.searchParams))
      if (request.method() === 'POST') writes.push({ query: Array.from(url.searchParams), proof: request.headers()['if-match'], key: request.headers()['idempotency-key'], body: request.postDataJSON() })
    })
    await page.goto(`/photos/attach?owner_type=item&owner_id=${owner}`)
    await lookupExistingMedia(page, id)
    await expect(page.getByText(`${messages.media.ownerLinkCount}205`, { exact: true })).toBeVisible()
    await expect(page.getByText(`${messages.media.membershipCount}206`, { exact: true })).toBeVisible()
    await expect(page.getByText(messages.media.alreadyLinked, { exact: true })).toBeVisible()
    expect(membershipQueries).toHaveLength(3)
    expect(membershipQueries.every((query) => new URLSearchParams(query).get('id') === id)).toBe(true)
    await page.getByRole('button', { name: messages.media.reviewAttach }).click()
    const dialog = page.getByRole('alertdialog')
    await expect(dialog.getByText(messages.media.alreadyLinked, { exact: true })).toBeVisible()
    expect(writes).toHaveLength(0)
    await dialog.getByRole('button', { name: messages.media.confirmAttach }).click()
    await expect(page.getByText(messages.mutations.done, { exact: true })).toBeVisible()
    expect(writes).toHaveLength(1); expect(writes[0]?.key).toBeTruthy()
    expect(writes[0]).toMatchObject({ query: [['id', id]], proof: '"1"', body: { item_id: owner, owner_revision: 1, is_cover: false, sort_order: 0, description: '' } })
    const actual = await page.evaluate(async (id) => {
      const modulePath = '/assets/media-sdk.js', api = await import(modulePath)
      return api.readMediaMembership(id, new AbortController().signal)
    }, id)
    expect(actual.media).toMatchObject({ id, revision: 2 }); expect(actual.links).toHaveLength(207)
    expect(actual.links.filter((link: { owner_id: string }) => link.owner_id === owner)).toHaveLength(206)
  })

  test('withholds an exact Media review when a real link changes between bracketed reads', async ({ page, application }) => {
    await provision(page, application)
    let changed = false, patchStatus = 0
    await page.route((url) => url.pathname === '/api/v1/media-by-identity/links' && url.searchParams.get('id') === id, async (route) => {
      const response = await route.fetch()
      if (!changed) {
        changed = true
        patchStatus = (await page.request.patch('/api/v1/media/links/owner-000', {
          headers: await writeContext(page, application.url, 1), data: { description: 'Concurrent exact Media change' },
        })).status()
      }
      await route.fulfill({ response })
    })
    await page.goto('/')
    const result = await page.evaluate(async ({ id }) => {
      const modulePath = '/assets/media-sdk.js', api = await import(modulePath), signal = () => new AbortController().signal
      let first: unknown
      try { await api.readMediaMembership(id, signal()); first = 'unexpected' }
      catch (error) { first = error instanceof Error && 'code' in error ? error.code : 'unexpected' }
      return { first, current: await api.readMediaMembership(id, signal()) }
    }, { id })
    expect(changed).toBe(true); expect(patchStatus).toBe(200); expect(result.first).toBe('INVALID_RESPONSE')
    expect(result.current.media).toMatchObject({ id, revision: 2 }); expect(result.current.links).toHaveLength(206)
    expect(result.current.links).toContainEqual(expect.objectContaining({ id: 'owner-000', revision: 2, description: 'Concurrent exact Media change' }))
  })
})

test.describe('exact retained MediaLink SDK identities', () => {
  const ids = ['legacy/link', 'legacy\\link', '\0', '.', '..', 'legacy/\0😀', '😀'.repeat(127) + '/']
  test.use({ applicationDatabaseFixture: { prepare(database) { prepareMediaFixture(database, 0, false, false, ids) } } })

  test('edits and unlinks exact retained IDs with independent proofs against the real Go server', async ({ page, application }) => {
    await provision(page, application)
    const writes: { method: string; id: string | null; proof: string | undefined; key: string | undefined; body: unknown }[] = []
    page.on('request', (request) => {
      const url = new URL(request.url())
      if (url.pathname !== '/api/v1/media/links' || !['PATCH', 'DELETE'].includes(request.method())) return
      writes.push({ method: request.method(), id: url.searchParams.get('id'), proof: request.headers()['if-match'],
        key: request.headers()['idempotency-key'], body: request.postData() === null ? null : request.postDataJSON() })
    })
    await page.goto('/')
    const result = await page.evaluate(async ({ empty }) => {
      const modulePath = '/assets/media-sdk.js', api = await import(modulePath), signal = () => new AbortController().signal
      const csrf = (await (await fetch('/api/v1/auth/csrf')).json()).token as string
      const source = await api.fetchOwnerMediaLinksPage('item', empty, { limit: 100 }, signal())
      const decisions = []
      for (const link of source.data) {
        const editContext = { csrfToken: csrf, idempotencyKey: crypto.randomUUID(), signal: signal(), link }
        const edited = await api.patchMediaLink(link.id, { description: 'Exact review' }, editContext)
        const editedReplay = await api.patchMediaLink(link.id, { description: 'Exact review' }, editContext)
        const unlinkContext = { csrfToken: csrf, idempotencyKey: crypto.randomUUID(), signal: signal(), link: edited }
        const removed = await api.unlinkMediaLink(link.id, unlinkContext)
        const removedReplay = await api.unlinkMediaLink(link.id, unlinkContext)
        decisions.push({ original: link, edited, editedReplay, removed, removedReplay })
      }
      return { decisions, after: await api.fetchOwnerMediaLinksPage('item', empty, {}, signal()), image: await api.fetchMedia('photo', signal()) }
    }, { empty })
    expect(result.decisions.map((decision: { original: { id: string } }) => decision.original.id).sort()).toEqual([...ids].sort())
    for (const decision of result.decisions) {
      expect(decision.edited).toMatchObject({ id: decision.original.id, revision: 2, description: 'Exact review', sort_order: -3 })
      expect(decision.editedReplay).toEqual(decision.edited)
      expect(decision.removed).toMatchObject({ ok: true, revision: 3 }); expect(decision.removedReplay).toEqual(decision.removed)
    }
    expect(result.after).toEqual({ data: [], next_cursor: null })
    expect(result.image.revision).toBe(7 + 2 * ids.length)
    expect(writes).toHaveLength(4 * ids.length)
    for (const id of ids) {
      const selected = writes.filter((write) => write.id === id)
      expect(selected.map(({ method, proof, body }) => ({ method, proof, body }))).toEqual([
        { method: 'PATCH', proof: '"1"', body: { description: 'Exact review' } },
        { method: 'PATCH', proof: '"1"', body: { description: 'Exact review' } },
        { method: 'DELETE', proof: '"2"', body: null }, { method: 'DELETE', proof: '"2"', body: null },
      ])
      expect(selected[0]?.key).toBeTruthy(); expect(selected[0]?.key).toBe(selected[1]?.key)
      expect(selected[2]?.key).toBeTruthy(); expect(selected[2]?.key).toBe(selected[3]?.key); expect(selected[2]?.key).not.toBe(selected[0]?.key)
    }
  })

  test('reconciles a committed lost unlink only with the same key after owner deletion', async ({ page, application }) => {
    await provision(page, application)
    const id = 'legacy/\0😀', keys: (string | undefined)[] = [], proofs: (string | undefined)[] = []
    let committedStatus = 0
    await page.route((url) => url.pathname === '/api/v1/media/links' && url.searchParams.get('id') === id, async (route) => {
      if (route.request().method() !== 'DELETE') { await route.continue(); return }
      keys.push(route.request().headers()['idempotency-key']); proofs.push(route.request().headers()['if-match'])
      if (keys.length > 1) { await route.continue(); return }
      committedStatus = (await route.fetch()).status(); await route.abort('failed')
    })
    await page.goto('/')
    const state = await page.evaluateHandle(async ({ empty, id }) => {
      const modulePath = '/assets/media-sdk.js', api = await import(modulePath), signal = () => new AbortController().signal
      const csrf = (await (await fetch('/api/v1/auth/csrf')).json()).token as string
      const source = await api.fetchOwnerMediaLinksPage('item', empty, { limit: 100 }, signal())
      const link = source.data.find((value: { id: string }) => value.id === id)
      if (!link) throw new Error('Retained link missing')
      return { api, csrf, link, key: crypto.randomUUID() }
    }, { empty, id })
    const first = await state.evaluate(async ({ api, csrf, link, key }) => {
      try { await api.unlinkMediaLink(link.id, { csrfToken: csrf, idempotencyKey: key, signal: new AbortController().signal, link }); return 'unexpected' }
      catch (error) { return error instanceof Error && 'code' in error ? error.code : 'unexpected' }
    })
    expect(first).toBe('NETWORK_ERROR'); expect(committedStatus).toBe(200); expect(keys).toHaveLength(1)
    const deletion = await page.request.delete(`/api/v1/items/${empty}`, { headers: await writeContext(page, application.url, 2) })
    expect(deletion.status()).toBe(200)
    await page.evaluate(() => window.dispatchEvent(new Event('online')))
    expect(keys).toHaveLength(1)
    const retry = await state.evaluate(async ({ api, csrf, link, key }) => api.unlinkMediaLink(link.id, { csrfToken: csrf, idempotencyKey: key, signal: new AbortController().signal, link }))
    expect(retry).toMatchObject({ ok: true, revision: 2 }); expect(keys).toHaveLength(2)
    expect(keys[1]).toBe(keys[0]); expect(proofs).toEqual(['"1"', '"1"'])
    await state.dispose()
  })
})

test.describe('exact retained MediaLink review screens', () => {
  const identities = [
    ['slash', 'legacy/link'], ['backslash', 'legacy\\link'], ['NUL', '\0'], ['dot', '.'], ['dot-dot', '..'],
    ['Unicode and controls', 'legacy/\0😀\r\n'], ['128 code points', '😀'.repeat(127) + '/'],
  ] as const
  const ids = identities.map(([, id]) => id), list = `/photos?${new URLSearchParams({ owner_type: 'item', owner_id: empty })}`
  const identityURL = (id: string) => `/api/v1/media/links?${new URLSearchParams({ id })}`
  test.use({ applicationDatabaseFixture: { prepare(database) { prepareMediaFixture(database, 0, false, false, ids) } } })

  function watchIdentityWrites(page: Page) {
    const writes: { method: string; query: [string, string][]; key: string | undefined; proof: string | undefined; body: unknown }[] = []
    page.on('request', (request) => {
      const url = new URL(request.url())
      if (url.pathname !== '/api/v1/media/links' || !['PATCH', 'DELETE'].includes(request.method())) return
      writes.push({ method: request.method(), query: Array.from(url.searchParams), key: request.headers()['idempotency-key'],
        proof: request.headers()['if-match'], body: request.postData() === null ? null : request.postDataJSON() })
    })
    return writes
  }

  for (const [label, id] of identities) {
    test(`reviews, edits and unlinks the exact ${label} identity`, async ({ page, application }) => {
      await provision(page, application)
      const writes = watchIdentityWrites(page)
      await page.goto(list)
      await page.getByRole('link', { name: `${messages.media.edit} ${JSON.stringify(id)}`, exact: true }).click()
      await expect(page.getByRole('textbox', { name: messages.media.order })).toHaveValue('-3')
      expect(new URL(page.url()).searchParams.get('link_id')).toBe(id)
      await page.getByRole('textbox', { name: messages.media.description }).fill('Reviewed exact identity')
      await page.getByRole('button', { name: messages.media.reviewEdit }).click()
      const edit = page.getByRole('alertdialog')
      await expect(edit).toContainText(JSON.stringify(id)); expect(writes).toHaveLength(0)
      await edit.getByRole('button', { name: messages.media.confirmEdit }).click()
      await expect(page.getByText(messages.mutations.done, { exact: true })).toBeVisible()
      const remove = page.getByRole('button', { name: `${messages.media.remove} ${JSON.stringify(id)}`, exact: true })
      await expect(remove).toBeEnabled(); await remove.click()
      const unlink = page.getByRole('alertdialog')
      await expect(unlink).toContainText(JSON.stringify(id)); expect(writes).toHaveLength(1)
      await unlink.getByRole('button', { name: messages.media.confirmRemove }).click()
      await expect.poll(() => writes.length).toBe(2)
      await expect(unlink).toHaveCount(0)
      await expect(page.getByText(`${messages.media.total}${ids.length - 1}`, { exact: true })).toBeVisible()
      await expect(remove).toHaveCount(0)
      expect(writes.map(({ method, query, proof, body }) => ({ method, query, proof, body }))).toEqual([
        { method: 'PATCH', query: [['id', id]], proof: '"1"', body: { description: 'Reviewed exact identity' } },
        { method: 'DELETE', query: [['id', id]], proof: '"2"', body: null },
      ])
      expect(writes[0]?.key).toBeTruthy(); expect(writes[1]?.key).toBeTruthy(); expect(writes[0]?.key).not.toBe(writes[1]?.key)
      const remaining = await (await page.request.get(`/api/v1/media/links?owner_type=item&owner_id=${empty}`)).json()
      expect(remaining.data).toHaveLength(ids.length - 1)
      expect(remaining.data.some((value: { id: string }) => value.id === id)).toBe(false)
    })
  }

  test('requires explicit adoption after a concurrent edit to the exact identity', async ({ page, application }) => {
    await provision(page, application)
    const id = 'legacy/\0😀\r\n', writes = watchIdentityWrites(page)
    await page.goto(list)
    await page.getByRole('link', { name: `${messages.media.edit} ${JSON.stringify(id)}`, exact: true }).click()
    const order = page.getByRole('textbox', { name: messages.media.order }), description = page.getByRole('textbox', { name: messages.media.description })
    await order.fill('0'); await description.fill('Local draft')
    await page.getByRole('button', { name: messages.media.reviewEdit }).click()
    const changed = await page.request.patch(identityURL(id), { headers: await writeContext(page, application.url, 1), data: { is_cover: false, sort_order: 6, description: 'Concurrent' } })
    expect(changed.status()).toBe(200)
    const dialog = page.getByRole('alertdialog')
    await dialog.getByRole('button', { name: messages.media.confirmEdit }).click()
    await expect(dialog.getByText(messages.locations.conflict)).toBeVisible()
    await expect(dialog.getByText(messages.media.reviewStale)).toBeVisible()
    await expect(dialog.getByRole('button', { name: messages.media.confirmEdit })).toBeDisabled()
    await dialog.getByRole('button', { name: messages.mutations.cancel }).click()
    await expect(page.getByText(messages.media.changed, { exact: true })).toBeVisible()
    await order.fill('01'); await page.getByRole('button', { name: messages.media.keepEdits }).click()
    await expect(order).toHaveValue('01'); await expect(description).toHaveValue('Local draft')
    await expect(page.getByRole('checkbox', { name: messages.media.cover })).not.toBeChecked()
    await page.getByRole('button', { name: messages.media.reviewEdit }).click()
    await expect(page.getByText(messages.media.orderError, { exact: true })).toBeVisible()
    await order.fill('0'); await page.getByRole('button', { name: messages.media.reviewEdit }).click()
    await page.getByRole('alertdialog').getByRole('button', { name: messages.media.confirmEdit }).click()
    await expect(page.getByText(messages.mutations.done, { exact: true })).toBeVisible()
    expect(writes.map(({ method, query, proof, body }) => ({ method, query, proof, body }))).toEqual([
      { method: 'PATCH', query: [['id', id]], proof: '"1"', body: { sort_order: 0, description: 'Local draft' } },
      { method: 'PATCH', query: [['id', id]], proof: '"2"', body: { sort_order: 0, description: 'Local draft' } },
    ])
    expect(writes[0]?.key).toBeTruthy(); expect(writes[0]?.key).not.toBe(writes[1]?.key)
  })

  for (const kind of ['edit', 'unlink'] as const) {
    test(`reconciles a committed lost ${kind} after the owner is deleted`, async ({ page, application }) => {
      await provision(page, application)
      const id = 'legacy/\0😀\r\n', method = kind === 'edit' ? 'PATCH' : 'DELETE', writes = watchIdentityWrites(page)
      let committedStatus = 0, attempts = 0
      await page.route((url) => url.pathname === '/api/v1/media/links' && url.searchParams.get('id') === id, async (route) => {
        if (route.request().method() !== method) { await route.continue(); return }
        if (++attempts > 1) { await route.continue(); return }
        committedStatus = (await route.fetch()).status(); await route.abort('failed')
      })
      await page.goto(list)
      if (kind === 'edit') {
        await page.getByRole('link', { name: `${messages.media.edit} ${JSON.stringify(id)}`, exact: true }).click()
        await page.getByRole('textbox', { name: messages.media.description }).fill('Lost reply')
        await page.getByRole('button', { name: messages.media.reviewEdit }).click()
      } else await page.getByRole('button', { name: `${messages.media.remove} ${JSON.stringify(id)}`, exact: true }).click()
      await page.getByRole('alertdialog').getByRole('button', { name: kind === 'edit' ? messages.media.confirmEdit : messages.media.confirmRemove }).click()
      await expect(page.getByRole('heading', { name: messages.mutations.uncertain })).toBeVisible()
      expect(committedStatus).toBe(200); expect(attempts).toBe(1)
      expect((await page.request.delete(`/api/v1/items/${empty}`, { headers: await writeContext(page, application.url, 2) })).status()).toBe(200)
      await page.getByRole('alertdialog').getByRole('button', { name: messages.mutations.readCurrent }).click()
      await page.getByRole('alertdialog').getByRole('button', { name: messages.mutations.cancel }).click()
      await expect(page.getByText(messages.media.missing, { exact: true })).toBeVisible()
      await page.evaluate(() => window.dispatchEvent(new Event('online')))
      expect(attempts).toBe(1)
      await page.getByRole('button', { name: messages.mutations.openOriginal }).click()
      await page.getByRole('alertdialog').getByRole('button', { name: messages.mutations.retryOriginal }).click()
      await expect(page.getByText(messages.mutations.done, { exact: true })).toBeVisible()
      expect(attempts).toBe(2); expect(writes).toHaveLength(2)
      expect(writes[0]?.key).toBeTruthy(); expect(writes[1]?.key).toBe(writes[0]?.key)
      expect(writes.map(({ method, query, proof, body }) => ({ method, query, proof, body }))).toEqual(Array(2).fill({
        method, query: [['id', id]], proof: '"1"', body: kind === 'edit' ? { description: 'Lost reply' } : null,
      }))
    })
  }

  test('keeps an offline draft without submitting or replaying its exact identity', async ({ page, application }) => {
    await provision(page, application)
    const id = 'legacy\\link', writes = watchIdentityWrites(page)
    await page.goto(list)
    const remove = page.getByRole('button', { name: `${messages.media.remove} ${JSON.stringify(id)}`, exact: true })
    await expect(remove).toBeEnabled()
    try {
      await page.context().setOffline(true)
      await expect(remove).toBeDisabled(); await page.context().setOffline(false)
      await expect(remove).toBeEnabled(); expect(writes).toHaveLength(0)
      await page.getByRole('link', { name: `${messages.media.edit} ${JSON.stringify(id)}`, exact: true }).click()
      const description = page.getByRole('textbox', { name: messages.media.description }), review = page.getByRole('button', { name: messages.media.reviewEdit })
      await description.fill('Offline draft'); await page.context().setOffline(true)
      await expect(review).toBeDisabled(); await expect(description).toHaveValue('Offline draft')
      await page.context().setOffline(false); await expect(review).toBeEnabled(); expect(writes).toHaveLength(0)
      await review.click(); await page.getByRole('alertdialog').getByRole('button', { name: messages.media.confirmEdit }).click()
      await expect(page.getByText(messages.mutations.done, { exact: true })).toBeVisible()
      expect(writes).toMatchObject([{ method: 'PATCH', query: [['id', id]], proof: '"1"', body: { description: 'Offline draft' } }])
    } finally { await page.context().setOffline(false) }
  })

  test('returns through login with the exact link identity but without its draft', async ({ page, application }) => {
    const password = await provision(page, application), id = 'legacy/\0😀\r\n'
    await page.goto(list)
    await page.getByRole('link', { name: `${messages.media.edit} ${JSON.stringify(id)}`, exact: true }).click()
    await page.getByRole('textbox', { name: messages.media.description }).fill('Private draft')
    await page.context().clearCookies(); await page.getByRole('button', { name: messages.media.refresh }).click()
    await expect(page.getByRole('heading', { name: messages.auth.loginHeading })).toBeVisible()
    await page.getByLabel(new RegExp(`^${messages.auth.username}`)).fill('owner')
    await page.getByLabel(new RegExp(`^${messages.auth.password}`)).fill(password)
    await page.getByRole('button', { name: messages.auth.login, exact: true }).click()
    await expect(page.getByRole('textbox', { name: messages.media.description })).toHaveValue('\0Exact é\n')
    const url = new URL(page.url())
    expect(url.pathname).toBe('/photos/edit')
    expect(Array.from(url.searchParams)).toEqual([['owner_type', 'item'], ['owner_id', empty], ['link_id', id]])
  })

  test('keeps long exact identities accessible at enlarged text in both themes', async ({ page, application }, info) => {
    await provision(page, application)
    const id = '😀'.repeat(127) + '/'
    await page.goto(list); await page.addStyleTag({ content: 'html { font-size: 200% }' })
    const check = async () => {
      await noOverflow(page)
      if (info.project.name === 'small-phone') expect(await page.evaluate(() => document.documentElement.scrollWidth <= 320)).toBe(true)
      expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze()).violations).toEqual([])
    }
    for (const colorScheme of ['light', 'dark'] as const) {
      await page.emulateMedia({ colorScheme }); await expect(page.locator('html')).toHaveAttribute('data-theme', colorScheme)
      await check()
    }
    await page.getByRole('link', { name: `${messages.media.edit} ${JSON.stringify(id)}`, exact: true }).click()
    await expect(page.getByRole('textbox', { name: messages.media.order })).toHaveValue('-3')
    await page.getByRole('textbox', { name: messages.media.description }).fill('Accessible review')
    for (const colorScheme of ['light', 'dark'] as const) {
      await page.emulateMedia({ colorScheme }); await expect(page.locator('html')).toHaveAttribute('data-theme', colorScheme)
      await check()
      if (info.project.name === 'small-phone' || info.project.name === 'desktop') await page.screenshot({ path: info.outputPath(`exact-link-editor-${colorScheme}.png`), fullPage: true })
    }
    await page.getByRole('button', { name: messages.media.reviewEdit }).click()
    const dialog = page.getByRole('alertdialog')
    await expect(dialog.getByRole('heading', { name: messages.media.editReviewTitle })).toBeFocused()
    for (const colorScheme of ['light', 'dark'] as const) {
      await page.emulateMedia({ colorScheme }); await expect(page.locator('html')).toHaveAttribute('data-theme', colorScheme)
      await check()
      expect(await dialog.getByRole('button', { name: messages.media.confirmEdit }).evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)
      if (info.project.name === 'small-phone' || info.project.name === 'desktop') await page.screenshot({ path: info.outputPath(`exact-link-review-${colorScheme}.png`), fullPage: true })
    }
  })
})

test.describe('encoded MediaLink identity metacharacters', () => {
  const id = 'link&owner_id=other?x#y%+\\'
  test.use({ applicationDatabaseFixture: { prepare(database) { prepareMediaFixture(database, 0, false, false, [id]) } } })

  test('keeps query metacharacters in the selected identity without adding parameters', async ({ page, application }) => {
    await provision(page, application)
    const writes: { method: string; query: [string, string][]; proof: string | undefined }[] = []
    page.on('request', (request) => {
      const url = new URL(request.url())
      if (url.pathname === '/api/v1/media/links' && ['PATCH', 'DELETE'].includes(request.method())) {
        writes.push({ method: request.method(), query: Array.from(url.searchParams), proof: request.headers()['if-match'] })
      }
    })
    await page.goto(`/photos?owner_type=item&owner_id=${empty}`)
    await page.getByRole('link', { name: `${messages.media.edit} ${JSON.stringify(id)}`, exact: true }).click()
    await expect(page.getByRole('textbox', { name: messages.media.order })).toHaveValue('-3')
    expect(Array.from(new URL(page.url()).searchParams)).toEqual([['owner_type', 'item'], ['owner_id', empty], ['link_id', id]])
    await page.getByRole('textbox', { name: messages.media.description }).fill('Exact metacharacters')
    await page.getByRole('button', { name: messages.media.reviewEdit }).click()
    await page.getByRole('alertdialog').getByRole('button', { name: messages.media.confirmEdit }).click()
    const remove = page.getByRole('button', { name: `${messages.media.remove} ${JSON.stringify(id)}`, exact: true })
    await expect(remove).toBeEnabled(); await remove.click()
    const dialog = page.getByRole('alertdialog')
    await dialog.getByRole('button', { name: messages.media.confirmRemove }).click()
    await expect.poll(() => writes.length).toBe(2); await expect(dialog).toHaveCount(0)
    await expect(page.getByText(`${messages.media.total}0`, { exact: true })).toBeVisible()
    expect(writes).toEqual([
      { method: 'PATCH', query: [['id', id]], proof: '"1"' },
      { method: 'DELETE', query: [['id', id]], proof: '"2"' },
    ])
  })
})

test.describe('exact retained Media identity input', () => {
  test.use({ applicationDatabaseFixture: { prepare(database) { prepareMediaFixture(database, 0, false, true) } } })

  test('preserves escaped CR/LF and Unicode in the actual Media path and original proof', async ({ page, application }) => {
    await provision(page, application)
    const id = 'photo\r\n😀', writes = watchDomainWrites(page, ['/api/v1/media'])
    await page.goto(`/photos/attach?owner_type=item&owner_id=${empty}`)
    await lookupExistingMedia(page, id)
    await expect(page.getByRole('textbox', { name: messages.media.mediaID })).toHaveValue(JSON.stringify(id))
    await page.getByRole('button', { name: messages.media.reviewAttach }).click()
    await page.getByRole('alertdialog').getByRole('button', { name: messages.media.confirmAttach }).click()
    await expect(page.getByText(messages.mutations.done, { exact: true })).toBeVisible()
    expect(writes).toHaveLength(1); expect(writes[0]).toMatchObject({ method: 'POST', path: `/api/v1/media/${encodeURIComponent(id)}/links`, proof: '"2"' })
    const links = await (await page.request.get(`/api/v1/media/links?owner_type=item&owner_id=${empty}`)).json()
    expect(links.data).toMatchObject([{ owner_id: empty, media_id: id, revision: 1 }])
  })
})

test.describe('exact retained Media attachment review screens', () => {
  const identities = [
    ['dot', '.'], ['dot-dot', '..'], ['slash', 'legacy/photo'], ['backslash', 'legacy\\photo'], ['NUL', '\0'],
    ['Unicode and controls', 'legacy/\0😀\r\n'], ['query metacharacters', 'percent%2F/plus+?&=#'], ['128 code points', '😀'.repeat(127) + '/'],
  ] as const
  const ids = identities.map(([, id]) => id)
  const endpoint = (id: string) => `/api/v1/media-by-identity/links?${new URLSearchParams({ id })}`
  test.use({ applicationDatabaseFixture: { prepare(database) { prepareMediaFixture(database, 0, true, false, [], { ids }) } } })

  function watchAttachments(page: Page) {
    const writes: { query: [string, string][]; key: string | undefined; proof: string | undefined; body: unknown }[] = []
    page.on('request', (request) => {
      const url = new URL(request.url())
      if (url.pathname !== '/api/v1/media-by-identity/links' || request.method() !== 'POST') return
      writes.push({ query: Array.from(url.searchParams), key: request.headers()['idempotency-key'], proof: request.headers()['if-match'], body: request.postDataJSON() })
    })
    return writes
  }

  for (const [label, id] of identities) {
    test(`reviews and attaches the exact ${label} Media identity`, async ({ page, application }) => {
      await provision(page, application)
      const writes = watchAttachments(page), reads: { path: string; query: [string, string][] }[] = []
      page.on('request', (request) => {
        const url = new URL(request.url())
        if (request.method() === 'GET' && url.pathname.startsWith('/api/v1/media-by-identity')) reads.push({ path: url.pathname, query: Array.from(url.searchParams) })
      })
      await page.goto(`/photos/attach?owner_type=item&owner_id=${empty}`)
      await lookupExistingMedia(page, id)
      await expect(page.getByRole('textbox', { name: messages.media.mediaID })).toHaveValue(JSON.stringify(id))
      await expect(page.getByText(messages.media.unverified, { exact: true })).toBeVisible()
      await expect(page.getByText(`${messages.media.membershipCount}0`, { exact: true })).toBeVisible()
      await page.getByRole('button', { name: messages.media.reviewAttach }).click()
      const dialog = page.getByRole('alertdialog')
      await expect(dialog).toContainText(JSON.stringify(id)); expect(writes).toHaveLength(0)
      expect(reads.filter((read) => read.path === '/api/v1/media-by-identity').every((read) => read.query.length === 1 && read.query[0]?.[1] === id)).toBe(true)
      expect(reads.filter((read) => read.path === '/api/v1/media-by-identity')).toHaveLength(2)
      expect(reads.filter((read) => read.path === '/api/v1/media-by-identity/links')).toHaveLength(1)
      await dialog.getByRole('button', { name: messages.media.confirmAttach }).click()
      await expect(page.getByText(messages.mutations.done, { exact: true })).toBeVisible()
      expect(writes).toHaveLength(1); expect(writes[0]).toMatchObject({ query: [['id', id]], proof: '"1"',
        body: { item_id: empty, owner_revision: 1, is_cover: false, sort_order: 0, description: '' } })
      expect(writes[0]?.key).toBeTruthy()
      const response = await page.request.get(endpoint(id))
      expect(response.status()).toBe(200)
      const membership = await response.json()
      expect(membership.data).toMatchObject([{ media_id: id, owner_type: 'item', owner_id: empty, revision: 1, media: { id, revision: 2 } }])
      expect(membership.next_cursor).toBeNull()
    })
  }

  test('attaches exact Media to retained Unit and reserved Location owners', async ({ page, application }) => {
    await provision(page, application)
    const id = 'legacy/\0😀\r\n', unitID = 'legacy/unit\0😀', writes = watchAttachments(page)
    await page.goto(`/photos/attach?owner_type=unit&owner_id=${encodeURIComponent(unitID)}`)
    await lookupExistingMedia(page, id)
    await page.getByRole('button', { name: messages.media.reviewAttach }).click()
    await page.getByRole('alertdialog').getByRole('button', { name: messages.media.confirmAttach }).click()
    await expect(page.getByText(messages.mutations.done, { exact: true })).toBeVisible()
    await page.goto('/photos/attach?owner_type=location&owner_id=tree')
    await lookupExistingMedia(page, id)
    await page.getByRole('button', { name: messages.media.reviewAttach }).click()
    await page.getByRole('alertdialog').getByRole('button', { name: messages.media.confirmAttach }).click()
    await expect(page.getByText(messages.mutations.done, { exact: true })).toBeVisible()
    expect(writes.map(({ query, proof, body }) => ({ query, proof, body }))).toEqual([
      { query: [['id', id]], proof: '"1"', body: { item_unit_id: unitID, owner_revision: 1, is_cover: false, sort_order: 0, description: '' } },
      { query: [['id', id]], proof: '"2"', body: { location_id: 'tree', owner_revision: 1, is_cover: false, sort_order: 0, description: '' } },
    ])
    expect(writes[0]?.key).toBeTruthy(); expect(writes[1]?.key).toBeTruthy(); expect(writes[0]?.key).not.toBe(writes[1]?.key)
    const actual = await (await page.request.get(endpoint(id))).json()
    expect(actual.data).toHaveLength(2)
    expect(actual.data).toContainEqual(expect.objectContaining({ media_id: id, owner_type: 'unit', owner_id: unitID }))
    expect(actual.data).toContainEqual(expect.objectContaining({ media_id: id, owner_type: 'location', owner_id: 'tree' }))
  })

  test('adopts changed exact Media and owner sources only after explicit review', async ({ page, application }) => {
    await provision(page, application)
    const id = 'legacy/\0😀\r\n', writes = watchAttachments(page)
    await page.goto(`/photos/attach?owner_type=item&owner_id=${empty}`)
    await lookupExistingMedia(page, id)
    const order = page.getByRole('textbox', { name: messages.media.order }), description = page.getByRole('textbox', { name: messages.media.description })
    await description.fill('Exact local draft')
    await page.getByRole('button', { name: messages.media.reviewAttach }).click()
    expect((await page.request.post(endpoint(id), { headers: await writeContext(page, application.url, 1), data: { item_id: owner, owner_revision: 1 } })).status()).toBe(200)
    expect((await page.request.patch(`/api/v1/items/${empty}`, { headers: await writeContext(page, application.url, 1), data: { name: 'Changed exact owner' } })).status()).toBe(200)
    const dialog = page.getByRole('alertdialog')
    await dialog.getByRole('button', { name: messages.media.confirmAttach }).click()
    await expect(dialog.getByText(messages.locations.conflict)).toBeVisible()
    await expect(dialog.getByText(messages.media.attachReviewStale)).toBeVisible()
    await expect(dialog.getByRole('button', { name: messages.media.confirmAttach })).toBeDisabled()
    await dialog.getByRole('button', { name: messages.mutations.cancel }).click()
    await expect(page.getByText(messages.media.attachChanged, { exact: true })).toBeVisible()
    await order.fill('01'); await page.getByRole('button', { name: messages.media.adoptSources }).click()
    await expect(order).toHaveValue('01'); await expect(description).toHaveValue('Exact local draft')
    await page.getByRole('button', { name: messages.media.reviewAttach }).click()
    await expect(page.getByText(messages.media.attachOrderError, { exact: true })).toBeVisible()
    await order.fill('0'); await page.getByRole('button', { name: messages.media.reviewAttach }).click()
    await page.getByRole('alertdialog').getByRole('button', { name: messages.media.confirmAttach }).click()
    await expect(page.getByText(messages.mutations.done, { exact: true })).toBeVisible()
    expect(writes.map(({ query, proof, body }) => ({ query, proof, body }))).toEqual([
      { query: [['id', id]], proof: '"1"', body: { item_id: empty, owner_revision: 1, is_cover: false, sort_order: 0, description: 'Exact local draft' } },
      { query: [['id', id]], proof: '"2"', body: { item_id: empty, owner_revision: 2, is_cover: false, sort_order: 0, description: 'Exact local draft' } },
    ])
    expect(writes[0]?.key).toBeTruthy(); expect(writes[1]?.key).toBeTruthy(); expect(writes[0]?.key).not.toBe(writes[1]?.key)
  })

  test('reconciles a lost exact attachment after owner deletion and a fresh login', async ({ page, application }) => {
    const password = await provision(page, application), id = 'legacy/\0😀\r\n', writes = watchAttachments(page)
    let attempts = 0, committedStatus = 0
    await page.route((url) => url.pathname === '/api/v1/media-by-identity/links' && url.searchParams.get('id') === id, async (route) => {
      if (route.request().method() !== 'POST') { await route.continue(); return }
      attempts++
      if (attempts !== 1) { await route.continue(); return }
      committedStatus = (await route.fetch()).status(); await route.abort('failed')
    })
    await page.goto(`/photos/attach?owner_type=item&owner_id=${empty}`)
    await lookupExistingMedia(page, id)
    await page.getByRole('button', { name: messages.media.reviewAttach }).click()
    await page.getByRole('alertdialog').getByRole('button', { name: messages.media.confirmAttach }).click()
    await expect(page.getByRole('heading', { name: messages.mutations.uncertain })).toBeVisible()
    expect(committedStatus).toBe(200); expect(attempts).toBe(1)
    expect((await page.request.delete(`/api/v1/items/${empty}`, { headers: await writeContext(page, application.url, 2) })).status()).toBe(200)
    expect((await page.request.post('/api/v1/auth/login', { headers: { Origin: application.url }, data: { username: 'owner', password } })).status()).toBe(200)
    await page.getByRole('alertdialog').getByRole('button', { name: messages.mutations.readCurrent }).click()
    await page.getByRole('alertdialog').getByRole('button', { name: messages.mutations.cancel }).click()
    await expect(page.getByText(messages.media.missing, { exact: true })).toBeVisible()
    await page.evaluate(() => window.dispatchEvent(new Event('online')))
    expect(attempts).toBe(1)
    await page.getByRole('button', { name: messages.mutations.openOriginal }).click()
    await page.getByRole('alertdialog').getByRole('button', { name: messages.mutations.retryOriginal }).click()
    await expect(page.getByText(messages.mutations.done, { exact: true })).toBeVisible()
    expect(attempts).toBe(2); expect(writes).toHaveLength(2)
    expect(writes[0]?.key).toBeTruthy(); expect(writes[1]?.key).toBe(writes[0]?.key)
    expect(writes.map(({ query, proof, body }) => ({ query, proof, body }))).toEqual(Array(2).fill({ query: [['id', id]], proof: '"1"',
      body: { item_id: empty, owner_revision: 1, is_cover: false, sort_order: 0, description: '' } }))
    expect(await (await page.request.get(endpoint(id))).json()).toEqual({ data: [], next_cursor: null })
    expect(await (await page.request.get(`/api/v1/media-by-identity?${new URLSearchParams({ id })}`)).json()).toMatchObject({ id, revision: 3 })
  })

  test('preserves an offline exact attachment draft without reconnect replay', async ({ page, application }) => {
    await provision(page, application)
    const id = 'legacy/\0😀\r\n', writes = watchAttachments(page)
    await page.goto(`/photos/attach?owner_type=item&owner_id=${empty}`)
    await lookupExistingMedia(page, id)
    const description = page.getByRole('textbox', { name: messages.media.description }), review = page.getByRole('button', { name: messages.media.reviewAttach })
    await description.fill('Offline exact draft')
    try {
      await page.context().setOffline(true)
      await expect(page.getByText(messages.media.offline, { exact: true })).toBeVisible()
      await expect(description).toHaveValue('Offline exact draft'); await expect(review).toBeDisabled()
      await page.context().setOffline(false); await expect(review).toBeEnabled()
      expect(writes).toHaveLength(0)
      await review.click(); await page.getByRole('alertdialog').getByRole('button', { name: messages.media.confirmAttach }).click()
      await expect(page.getByText(messages.mutations.done, { exact: true })).toBeVisible()
      expect(writes).toMatchObject([{ query: [['id', id]], proof: '"1"', body: { item_id: empty, owner_revision: 1, is_cover: false, sort_order: 0, description: 'Offline exact draft' } }])
    } finally { await page.context().setOffline(false) }
  })

  test('allows another exact Media lookup after a missing source without losing the draft', async ({ page, application }) => {
    await provision(page, application)
    const id = 'legacy/\0😀\r\n', writes = watchAttachments(page)
    await page.goto(`/photos/attach?owner_type=item&owner_id=${empty}`)
    await page.getByRole('textbox', { name: messages.media.description }).fill('Keep this draft')
    await page.getByRole('textbox', { name: messages.media.mediaID }).fill(JSON.stringify('missing/\0😀'))
    await page.getByRole('button', { name: messages.media.lookupMedia }).click()
    await expect(page.getByText(messages.media.mediaMissing, { exact: true })).toBeVisible()
    await lookupExistingMedia(page, id)
    await expect(page.getByRole('textbox', { name: messages.media.description })).toHaveValue('Keep this draft')
    await page.getByRole('button', { name: messages.media.reviewAttach }).click()
    await expect(page.getByRole('alertdialog')).toContainText(JSON.stringify(id))
    expect(writes).toHaveLength(0)
  })

  test('purges the selected exact Media and reviewed draft on session loss', async ({ page, application }) => {
    const password = await provision(page, application), id = 'legacy/\0😀\r\n', ownerID = 'legacy/item\0😀', writes = watchAttachments(page)
    await page.goto(`/photos/attach?owner_type=item&owner_id=${encodeURIComponent(ownerID)}`)
    await lookupExistingMedia(page, id)
    await page.getByRole('textbox', { name: messages.media.description }).fill('Private exact draft')
    await page.getByRole('button', { name: messages.media.reviewAttach }).click()
    await page.context().clearCookies()
    await page.getByRole('alertdialog').getByRole('button', { name: messages.media.confirmAttach }).click()
    await expect(page.getByRole('heading', { name: messages.auth.loginHeading })).toBeVisible()
    await page.getByLabel(new RegExp(`^${messages.auth.username}`)).fill('owner')
    await page.getByLabel(new RegExp(`^${messages.auth.password}`)).fill(password)
    await page.getByRole('button', { name: messages.auth.login, exact: true }).click()
    await expect(page.getByRole('textbox', { name: messages.media.mediaID })).toHaveValue('')
    await expect(page.getByRole('textbox', { name: messages.media.description })).toHaveValue('')
    expect(new URL(page.url()).pathname).toBe('/photos/attach')
    expect(Array.from(new URL(page.url()).searchParams)).toEqual([['owner_type', 'item'], ['owner_id', ownerID]])
    await expect(page.getByRole('heading', { name: messages.media.selectedMedia })).toHaveCount(0)
    await expect(page.getByRole('button', { name: messages.mutations.openOriginal })).toHaveCount(0)
    expect(writes).toHaveLength(0)
  })

  test('keeps a long exact Media identity accessible without requesting image bytes', async ({ page, application }, info) => {
    await provision(page, application)
    const id = '😀'.repeat(127) + '/', streams: string[] = []
    page.on('request', (request) => {
      if (request.method() === 'GET' && /\/api\/v1\/media\/[^/]+\/(?:content|thumbnail)$/.test(new URL(request.url()).pathname)) streams.push(request.url())
    })
    await page.goto(`/photos/attach?owner_type=item&owner_id=${empty}`)
    await lookupExistingMedia(page, id); await page.addStyleTag({ content: 'html { font-size: 200% }' })
    const check = async () => {
      await noOverflow(page)
      if (info.project.name === 'small-phone') expect(await page.evaluate(() => document.documentElement.scrollWidth <= 320)).toBe(true)
      expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze()).violations).toEqual([])
    }
    for (const colorScheme of ['light', 'dark'] as const) {
      await page.emulateMedia({ colorScheme }); await expect(page.locator('html')).toHaveAttribute('data-theme', colorScheme)
      await check()
      expect(await page.locator('dd').filter({ hasText: JSON.stringify(id) }).evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)
      if (info.project.name === 'small-phone' || info.project.name === 'desktop') await page.screenshot({ path: info.outputPath(`exact-media-attach-${colorScheme}.png`), fullPage: true })
    }
    await page.getByRole('button', { name: messages.media.reviewAttach }).click()
    const dialog = page.getByRole('alertdialog')
    await expect(dialog.getByRole('heading', { name: messages.media.attachReviewTitle })).toBeFocused()
    for (const colorScheme of ['light', 'dark'] as const) {
      await page.emulateMedia({ colorScheme }); await expect(page.locator('html')).toHaveAttribute('data-theme', colorScheme)
      await check()
      expect(await dialog.getByRole('button', { name: messages.media.confirmAttach }).evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)
      if (info.project.name === 'small-phone' || info.project.name === 'desktop') await page.screenshot({ path: info.outputPath(`exact-media-review-${colorScheme}.png`), fullPage: true })
    }
    expect(streams).toEqual([])
  })
})

test('attaches an existing shared Media after foreground review without loading image bytes', async ({ page, application }, info) => {
  await provision(page, application)
  const writes = watchDomainWrites(page, ['/api/v1/media']), streams: string[] = []
  page.on('request', (request) => { if (request.method() === 'GET' && /\/api\/v1\/media\/[^/]+\/(?:content|thumbnail)$/.test(new URL(request.url()).pathname)) streams.push(request.url()) })
  await page.goto(`/photos?owner_type=item&owner_id=${empty}`)
  await page.getByRole('link', { name: messages.media.attach, exact: true }).click()
  await expect(page).toHaveURL(new RegExp(`/photos/attach\\?owner_type=item&owner_id=${empty}$`))
  await lookupExistingMedia(page, 'links')
  await expect(page.getByText(messages.media.unverified, { exact: true })).toBeVisible()
  await expect(page.getByText(`${messages.media.ownerLinkCount}0`, { exact: true })).toBeVisible()
  await expect(page.getByText(`${messages.media.membershipCount}1`, { exact: true })).toBeVisible()
  await page.getByText(messages.media.membershipList, { exact: true }).click()
  const member = page.getByText(`${messages.media.ownerKinds.item}：${JSON.stringify(sibling)}`, { exact: true })
  await expect(member).toBeVisible()
  expect(streams).toEqual([])
  if (info.project.name === 'small-phone') await page.addStyleTag({ content: 'html { font-size: 200% }' })
  for (const colorScheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme })
    await expect(page.locator('html')).toHaveAttribute('data-theme', colorScheme)
    await member.scrollIntoViewIfNeeded(); await expect(member).toBeInViewport()
    expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze()).violations).toEqual([])
    await noOverflow(page)
    if (info.project.name === 'small-phone') {
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= 320)).toBe(true)
      expect(await page.getByText(messages.media.attachOrderHelp, { exact: true }).evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)
    }
    for (const name of [messages.media.lookupMedia, messages.media.reviewAttach]) expect(await page.getByRole('button', { name }).evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)
    if (info.project.name === 'small-phone' || info.project.name === 'desktop') await page.screenshot({ path: info.outputPath(`media-attach-${colorScheme}.png`), fullPage: true })
  }
  const order = page.getByRole('textbox', { name: messages.media.order }), cover = page.getByRole('checkbox', { name: messages.media.cover })
  await order.fill('01'); await cover.check()
  await expect(page.getByText(messages.media.attachOrderError, { exact: true })).toBeVisible()
  await order.fill('1'); await cover.uncheck()
  await expect(page.getByText(messages.media.attachOrderError, { exact: true })).toHaveCount(0)
  await cover.check()
  await order.fill('2')
  await page.getByRole('textbox', { name: messages.media.description }).fill('Exact é\ncaption')
  await page.getByRole('button', { name: messages.media.reviewAttach }).click()
  const dialog = page.getByRole('alertdialog')
  await expect(dialog.getByRole('heading', { name: messages.media.attachReviewTitle })).toBeFocused()
  await expect(dialog).toContainText(messages.media.attachReviewHelp)
  expect(writes).toHaveLength(0)
  expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze()).violations).toEqual([])
  await noOverflow(page)
  if (info.project.name === 'small-phone') expect(await page.evaluate(() => document.documentElement.scrollWidth <= 320)).toBe(true)
  expect(await dialog.getByRole('button', { name: messages.media.confirmAttach }).evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)
  await dialog.getByRole('button', { name: messages.media.confirmAttach }).click()
  await expect(page).toHaveURL(new RegExp(`/photos\\?owner_type=item&owner_id=${empty}$`))
  await expect(page.getByText(messages.mutations.done, { exact: true })).toBeVisible()
  expect(writes).toHaveLength(1); expect(writes[0]).toMatchObject({ method: 'POST', path: '/api/v1/media/links/links', proof: '"3"' })
  expect(JSON.parse(writes[0]!.body!)).toEqual({ item_id: empty, owner_revision: 1, is_cover: true, sort_order: 2, description: 'Exact é\ncaption' })
  const links = await (await page.request.get(`/api/v1/media/links?owner_type=item&owner_id=${empty}`)).json()
  expect(links.data).toMatchObject([{ owner_id: empty, media_id: 'links', revision: 1, is_cover: true, sort_order: 2, description: 'Exact é\ncaption' }])
  const shared = await (await page.request.get('/api/v1/media/links/links')).json()
  expect(shared.data).toHaveLength(2); expect(shared.data).toContainEqual(expect.objectContaining({ id: 'reserved', owner_id: sibling }))
  expect(streams).toEqual([])
})

test('requires explicit adoption of both changed attachment sources and preserves invalid raw input', async ({ page, application }) => {
  await provision(page, application)
  const writes = watchDomainWrites(page, ['/api/v1/media'])
  await page.goto(`/photos/attach?owner_type=item&owner_id=${empty}`)
  await lookupExistingMedia(page, 'links')
  const order = page.getByRole('textbox', { name: messages.media.order }), description = page.getByRole('textbox', { name: messages.media.description })
  await description.fill('Local caption')
  await page.getByRole('button', { name: messages.media.reviewAttach }).click()
  const mediaChange = await page.request.post('/api/v1/media/links/links', { headers: await writeContext(page, application.url, 3), data: { item_id: owner, owner_revision: 1 } })
  expect(mediaChange.status()).toBe(200)
  const ownerChange = await page.request.patch(`/api/v1/items/${empty}`, { headers: await writeContext(page, application.url, 1), data: { name: 'Changed owner' } })
  expect(ownerChange.status()).toBe(200)
  const dialog = page.getByRole('alertdialog')
  await dialog.getByRole('button', { name: messages.media.confirmAttach }).click()
  await expect(dialog.getByText(messages.locations.conflict)).toBeVisible()
  await expect(dialog.getByText(messages.media.attachReviewStale)).toBeVisible()
  await expect(dialog.getByRole('button', { name: messages.media.confirmAttach })).toBeDisabled()
  await dialog.getByRole('button', { name: messages.mutations.cancel }).click()
  await expect(page.getByText(messages.media.attachChanged, { exact: true })).toBeVisible()
  await order.fill('01')
  await page.getByRole('button', { name: messages.media.adoptSources }).click()
  await expect(order).toHaveValue('01'); await expect(description).toHaveValue('Local caption')
  await page.getByRole('button', { name: messages.media.reviewAttach }).click()
  await expect(page.getByText(messages.media.attachOrderError, { exact: true })).toBeVisible()
  await order.fill('0')
  await page.getByRole('button', { name: messages.media.reviewAttach }).click()
  await page.getByRole('alertdialog').getByRole('button', { name: messages.media.confirmAttach }).click()
  await expect(page.getByText(messages.mutations.done, { exact: true })).toBeVisible()
  expect(writes).toHaveLength(2); expect(writes.map((write) => write.proof)).toEqual(['"3"', '"4"'])
  expect(writes.map((write) => JSON.parse(write.body!).owner_revision)).toEqual([1, 2])
  expect(writes[0]?.key).not.toBe(writes[1]?.key)
})

test('reconciles a committed attachment with its original key after the real owner disappears', async ({ page, application }) => {
  await provision(page, application)
  let posts = 0, committedStatus = 0
  const keys: (string | undefined)[] = [], proofs: (string | undefined)[] = [], bodies: unknown[] = []
  await page.route((url) => url.pathname === '/api/v1/media/links/links', async (route) => {
    if (route.request().method() !== 'POST') { await route.continue(); return }
    posts++; keys.push(route.request().headers()['idempotency-key']); proofs.push(route.request().headers()['if-match']); bodies.push(route.request().postDataJSON())
    if (posts !== 1) { await route.continue(); return }
    committedStatus = (await route.fetch()).status(); await route.abort('failed')
  })
  await page.goto(`/photos/attach?owner_type=item&owner_id=${empty}`)
  await lookupExistingMedia(page, 'links')
  await page.getByRole('button', { name: messages.media.reviewAttach }).click()
  await page.getByRole('alertdialog').getByRole('button', { name: messages.media.confirmAttach }).click()
  await expect(page.getByRole('heading', { name: messages.mutations.uncertain })).toBeVisible()
  expect(committedStatus).toBe(200); expect(posts).toBe(1)
  const deletion = await page.request.delete(`/api/v1/items/${empty}`, { headers: await writeContext(page, application.url, 2) })
  expect(deletion.status()).toBe(200)
  await page.getByRole('alertdialog').getByRole('button', { name: messages.mutations.readCurrent }).click()
  await page.getByRole('alertdialog').getByRole('button', { name: messages.mutations.cancel }).click()
  await expect(page.getByText(messages.media.missing, { exact: true })).toBeVisible()
  await expect(page.getByRole('textbox', { name: messages.media.description })).toBeDisabled()
  await page.evaluate(() => window.dispatchEvent(new Event('online')))
  expect(posts).toBe(1)
  await page.getByRole('button', { name: messages.mutations.openOriginal }).click()
  await page.getByRole('alertdialog').getByRole('button', { name: messages.mutations.retryOriginal }).click()
  await expect(page.getByText(messages.mutations.done, { exact: true })).toBeVisible()
  expect(posts).toBe(2); expect(keys[0]).toBeTruthy(); expect(keys[1]).toBe(keys[0]); expect(proofs).toEqual(['"3"', '"3"'])
  expect(bodies).toEqual([{ item_id: empty, owner_revision: 1, is_cover: false, sort_order: 0, description: '' }, { item_id: empty, owner_revision: 1, is_cover: false, sort_order: 0, description: '' }])
})

test('shows lookup errors only when needed without reserving a large empty region', async ({ page, application }, info) => {
  await provision(page, application)
  await page.goto(`/photos/attach?owner_type=item&owner_id=${empty}`)
  if (info.project.name === 'small-phone') await page.addStyleTag({ content: 'html { font-size: 200% }' })
  const id = page.getByRole('textbox', { name: messages.media.mediaID }), error = page.locator('[id$="-media-error"]')
  await expect(id).toBeVisible()
  expect(await error.evaluate((element) => element.getBoundingClientRect().height)).toBe(0)
  await id.fill('null')
  await page.getByRole('button', { name: messages.media.lookupMedia }).click()
  await expect(id).toHaveAttribute('aria-invalid', 'true')
  await expect(error.getByRole('alert')).toHaveText(messages.media.mediaIDError)
  await id.fill('"links"')
  await expect(id).toHaveAttribute('aria-invalid', 'false')
  expect(await error.evaluate((element) => element.getBoundingClientRect().height)).toBe(0)
  await page.getByRole('button', { name: messages.media.lookupMedia }).click()
  await expect(page.getByRole('heading', { name: messages.media.selectedMedia })).toBeVisible()
  await noOverflow(page)
  if (info.project.name === 'small-phone') expect(await page.evaluate(() => document.documentElement.scrollWidth <= 320)).toBe(true)
})

test('warns before adding another independent link to an already linked Media', async ({ page, application }) => {
  await provision(page, application)
  const writes = watchDomainWrites(page, ['/api/v1/media'])
  await page.goto(`/photos/attach?owner_type=item&owner_id=${sibling}`)
  await lookupExistingMedia(page, 'links')
  await expect(page.getByText(messages.media.alreadyLinked, { exact: true })).toBeVisible()
  await page.getByRole('button', { name: messages.media.reviewAttach }).click()
  const dialog = page.getByRole('alertdialog')
  await expect(dialog.getByText(messages.media.alreadyLinked, { exact: true })).toBeVisible()
  expect(writes).toHaveLength(0)
  await dialog.getByRole('button', { name: messages.media.confirmAttach }).click()
  await expect(page.getByText(messages.mutations.done, { exact: true })).toBeVisible()
  expect(writes).toHaveLength(1); expect(writes[0]).toMatchObject({ method: 'POST', path: '/api/v1/media/links/links', proof: '"3"' })
  expect(JSON.parse(writes[0]!.body!)).toEqual({ item_id: sibling, owner_revision: 1, is_cover: false, sort_order: 0, description: '' })
  const result = await (await page.request.get(`/api/v1/media/links?owner_type=item&owner_id=${sibling}`)).json()
  const links = result.data.filter((link: { media_id: string }) => link.media_id === 'links')
  expect(links).toHaveLength(2)
  expect(links).toContainEqual(expect.objectContaining({ id: 'reserved', is_cover: true, revision: 1 }))
  expect(links).toContainEqual(expect.objectContaining({ is_cover: false, revision: 1 }))
  expect(new Set(links.map((link: { id: string }) => link.id)).size).toBe(2)
})

test('refuses a conflicting new cover without changing existing links', async ({ page, application }) => {
  await provision(page, application)
  const writes = watchDomainWrites(page, ['/api/v1/media'])
  await page.goto(`/photos/attach?owner_type=item&owner_id=${sibling}`)
  await lookupExistingMedia(page, 'links')
  await expect(page.getByText(`${messages.media.ownerLinkCount}2`, { exact: true })).toBeVisible()
  await page.getByRole('checkbox', { name: messages.media.cover }).check()
  await page.getByRole('button', { name: messages.media.reviewAttach }).click()
  await expect(page.getByText(messages.media.coverConflict, { exact: true })).toBeVisible()
  expect(writes).toHaveLength(0)
  const links = await (await page.request.get(`/api/v1/media/links?owner_type=item&owner_id=${sibling}`)).json()
  expect(links.data.filter((link: { is_cover: boolean }) => link.is_cover)).toHaveLength(2)
})

test('preserves an offline attachment draft without submitting or replaying it', async ({ page, application }) => {
  await provision(page, application)
  const writes = watchDomainWrites(page, ['/api/v1/media'])
  await page.goto(`/photos/attach?owner_type=item&owner_id=${empty}`)
  await lookupExistingMedia(page, 'links')
  const description = page.getByRole('textbox', { name: messages.media.description }), review = page.getByRole('button', { name: messages.media.reviewAttach })
  await description.fill('Offline draft')
  try {
    await page.context().setOffline(true)
    await expect(page.getByText(messages.media.offline, { exact: true })).toBeVisible()
    await expect(description).toHaveValue('Offline draft'); await expect(review).toBeDisabled()
    await page.context().setOffline(false)
    await expect(review).toBeEnabled()
    expect(writes).toHaveLength(0)
  } finally { await page.context().setOffline(false) }
})

test('shows actual Item links and unlinks one shared image through foreground review', async ({ page, application }, info) => {
  await provision(page, application)
  const writes = watchDomainWrites(page, ['/api/v1/media']), streams: string[] = []
  page.on('request', (request) => { if (request.method() === 'GET' && /\/api\/v1\/media\/[^/]+\/(?:content|thumbnail)$/.test(new URL(request.url()).pathname)) streams.push(request.url()) })
  await page.goto(`/items/${sibling}`)
  await page.getByRole('link', { name: messages.media.nav, exact: true }).click()
  await expect(page).toHaveURL(new RegExp(`/photos\\?owner_type=item&owner_id=${sibling}$`))
  await expect(page.getByRole('heading', { name: messages.media.title, exact: true })).toBeVisible()
  await expect(page.getByText(`${messages.media.total}2`)).toBeVisible()
  await expect(page.getByText(messages.media.storageUnavailable)).toBeVisible()
  await expect(page.getByText(messages.media.unverified, { exact: true })).toHaveCount(2)
  expect(streams).toEqual([])
  if (info.project.name === 'small-phone') {
    await page.addStyleTag({ content: 'html { font-size: 200% }' })
    const action = page.getByRole('button', { name: `${messages.media.remove} ${JSON.stringify('sibling')}` })
    expect(await action.evaluate((element) => getComputedStyle(element.closest('li')!).contentVisibility)).toBe('auto')
    expect(await action.evaluate((element) => element.getBoundingClientRect().top > innerHeight)).toBe(true)
    await action.evaluate((element) => { element.style.transitionDuration = '10s' })
  }
  for (const colorScheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme })
    await expect(page.locator('html')).toHaveAttribute('data-theme', colorScheme)
    expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze()).violations).toEqual([])
    await noOverflow(page)
    const action = page.getByRole('button', { name: `${messages.media.remove} ${JSON.stringify('sibling')}` })
    expect(await action.evaluate((node) => node.scrollWidth <= node.clientWidth)).toBe(true)
    if (info.project.name === 'small-phone') {
      const state = await action.evaluate((element) => ({ color: getComputedStyle(element).color, expected: getComputedStyle(document.body).color,
        transitions: element.getAnimations().filter((animation): animation is CSSTransition => animation instanceof CSSTransition).map((animation) => animation.transitionProperty) }))
      expect(state.color).toBe(state.expected); expect(state.transitions).not.toContain('color'); expect(state.transitions).not.toContain('background-color')
    }
    if (info.project.name === 'small-phone' || info.project.name === 'desktop') await page.screenshot({ path: info.outputPath(`media-owner-${colorScheme}.png`), fullPage: true })
  }
  await page.getByRole('button', { name: `${messages.media.remove} ${JSON.stringify('sibling')}` }).click()
  const dialog = page.getByRole('alertdialog')
  await expect(dialog.getByRole('heading', { name: messages.media.reviewTitle })).toBeFocused()
  await expect(dialog).toContainText(messages.media.removeHelp)
  await expect(dialog).toContainText('sibling')
  expect(writes).toHaveLength(0)
  expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze()).violations).toEqual([])
  await noOverflow(page)
  expect(await dialog.getByRole('button', { name: messages.media.confirmRemove }).evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)
  await dialog.getByRole('button', { name: messages.media.confirmRemove }).click()
  await expect(page.getByText(messages.mutations.done, { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: `${messages.media.remove} ${JSON.stringify('sibling')}` })).toHaveCount(0)
  expect(writes).toHaveLength(1); expect(writes[0]).toMatchObject({ method: 'DELETE', path: '/api/v1/media/links/sibling', proof: '"5"', body: null })
  const remaining = await (await page.request.get('/api/v1/media/photo/links?limit=100')).json()
  expect(remaining.data).toHaveLength(51); expect(remaining.data.every((value: { owner_id: string }) => value.owner_id === owner)).toBe(true)
  expect(streams).toEqual([])
})

test('edits only selected link metadata after foreground review and retains legacy order', async ({ page, application }, info) => {
  await provision(page, application)
  const writes = watchDomainWrites(page, ['/api/v1/media']), streams: string[] = []
  page.on('request', (request) => { if (request.method() === 'GET' && /\/api\/v1\/media\/[^/]+\/(?:content|thumbnail)$/.test(new URL(request.url()).pathname)) streams.push(request.url()) })
  await page.goto(`/photos?owner_type=item&owner_id=${sibling}`)
  await page.getByRole('link', { name: `${messages.media.edit} ${JSON.stringify('sibling')}` }).click()
  await expect(page).toHaveURL(new RegExp(`/photos/edit\\?owner_type=item&owner_id=${sibling}&link_id=sibling$`))
  await expect(page.getByRole('heading', { name: messages.media.editTitle })).toBeVisible()
  await expect(page.getByRole('textbox', { name: messages.media.order })).toHaveValue('-3')
  await expect(page.getByRole('checkbox', { name: messages.media.cover })).toBeChecked()
  if (info.project.name === 'small-phone') await page.addStyleTag({ content: 'html { font-size: 200% }' })
  for (const colorScheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme })
    await expect(page.locator('html')).toHaveAttribute('data-theme', colorScheme)
    expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze()).violations).toEqual([])
    await noOverflow(page)
    if (info.project.name === 'small-phone') {
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= 320)).toBe(true)
      expect(await page.getByText(messages.media.orderHelp, { exact: true }).evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)
    }
    expect(await page.getByRole('button', { name: messages.media.reviewEdit }).evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)
    if (info.project.name === 'small-phone' || info.project.name === 'desktop') await page.screenshot({ path: info.outputPath(`media-edit-${colorScheme}.png`), fullPage: true })
  }
  await page.getByRole('textbox', { name: messages.media.description }).clear()
  await page.getByRole('button', { name: messages.media.reviewEdit }).click()
  const dialog = page.getByRole('alertdialog')
  await expect(dialog.getByRole('heading', { name: messages.media.editReviewTitle })).toBeFocused()
  await expect(dialog).toContainText(messages.media.previous); await expect(dialog).toContainText(messages.media.proposed)
  expect(writes).toHaveLength(0)
  expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze()).violations).toEqual([])
  await noOverflow(page)
  if (info.project.name === 'small-phone') expect(await page.evaluate(() => document.documentElement.scrollWidth <= 320)).toBe(true)
  expect(await dialog.getByRole('button', { name: messages.media.confirmEdit }).evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)
  await dialog.getByRole('button', { name: messages.media.confirmEdit }).click()
  await expect(page).toHaveURL(new RegExp(`/photos\\?owner_type=item&owner_id=${sibling}$`))
  await expect(page.getByText(messages.mutations.done, { exact: true })).toBeVisible()
  expect(writes).toHaveLength(1); expect(writes[0]).toMatchObject({ method: 'PATCH', path: '/api/v1/media/links/sibling', proof: '"5"', body: JSON.stringify({ description: '' }) })
  const links = await (await page.request.get(`/api/v1/media/links?owner_type=item&owner_id=${sibling}`)).json()
  expect(links.data).toContainEqual(expect.objectContaining({ id: 'sibling', revision: 6, is_cover: true, sort_order: -3, description: '' }))
  expect(links.data).toContainEqual(expect.objectContaining({ id: 'reserved', is_cover: true }))
  expect(streams).toEqual([])
})

test('does not rewrite untouched retained control characters when changing only order', async ({ page, application }) => {
  await provision(page, application)
  const writes = watchDomainWrites(page, ['/api/v1/media'])
  await page.goto(`/photos/edit?owner_type=item&owner_id=${sibling}&link_id=sibling`)
  await expect(page.getByRole('textbox', { name: messages.media.description })).toHaveValue('\0Exact é\n')
  await page.getByRole('textbox', { name: messages.media.order }).fill('0')
  await page.getByRole('button', { name: messages.media.reviewEdit }).click()
  await page.getByRole('alertdialog').getByRole('button', { name: messages.media.confirmEdit }).click()
  await expect(page.getByText(messages.mutations.done, { exact: true })).toBeVisible()
  expect(writes).toHaveLength(1); expect(writes[0]).toMatchObject({ proof: '"5"', body: JSON.stringify({ sort_order: 0 }) })
  const links = await (await page.request.get(`/api/v1/media/links?owner_type=item&owner_id=${sibling}`)).json()
  expect(links.data).toContainEqual(expect.objectContaining({ id: 'sibling', sort_order: 0, description: '\0Exact é\n' }))
})

test('requires explicit adoption after a real concurrent edit and preserves invalid raw text', async ({ page, application }) => {
  await provision(page, application)
  const writes = watchDomainWrites(page, ['/api/v1/media'])
  await page.goto(`/photos/edit?owner_type=item&owner_id=${sibling}&link_id=sibling`)
  const order = page.getByRole('textbox', { name: messages.media.order }), description = page.getByRole('textbox', { name: messages.media.description })
  await order.fill('0'); await description.fill('Local é\n')
  await page.getByRole('button', { name: messages.media.reviewEdit }).click()
  const changed = await page.request.patch('/api/v1/media/links/sibling', { headers: await writeContext(page, application.url, 5), data: { is_cover: false, sort_order: 6, description: 'Concurrent' } })
  expect(changed.status()).toBe(200)
  const dialog = page.getByRole('alertdialog')
  await dialog.getByRole('button', { name: messages.media.confirmEdit }).click()
  await expect(dialog.getByText(messages.locations.conflict)).toBeVisible()
  await expect(dialog.getByText(messages.media.reviewStale)).toBeVisible()
  await expect(dialog.getByRole('button', { name: messages.media.confirmEdit })).toBeDisabled()
  await dialog.getByRole('button', { name: messages.mutations.cancel }).click()
  await expect(page.getByText(messages.media.changed, { exact: true })).toBeVisible()
  await order.fill('01')
  await page.getByRole('button', { name: messages.media.keepEdits }).click()
  await expect(order).toHaveValue('01'); await expect(description).toHaveValue('Local é\n')
  await expect(page.getByRole('checkbox', { name: messages.media.cover })).not.toBeChecked()
  await page.getByRole('button', { name: messages.media.reviewEdit }).click()
  await expect(page.getByText(messages.media.orderError, { exact: true })).toBeVisible()
  await order.fill('0')
  await page.getByRole('button', { name: messages.media.reviewEdit }).click()
  await page.getByRole('alertdialog').getByRole('button', { name: messages.media.confirmEdit }).click()
  await expect(page.getByText(messages.mutations.done, { exact: true })).toBeVisible()
  expect(writes.map((write) => ({ proof: write.proof, body: JSON.parse(write.body!) }))).toEqual([
    { proof: '"5"', body: { sort_order: 0, description: 'Local é\n' } },
    { proof: '"6"', body: { sort_order: 0, description: 'Local é\n' } },
  ])
  expect(writes[0]?.key).not.toBe(writes[1]?.key)
})

test('reconciles a committed edit with the original key after the actual owner disappears', async ({ page, application }) => {
  await provision(page, application)
  let patches = 0, committedStatus = 0
  const keys: (string | undefined)[] = [], proofs: (string | undefined)[] = [], bodies: unknown[] = []
  await page.route((url) => url.pathname === '/api/v1/media/links/sibling', async (route) => {
    if (route.request().method() !== 'PATCH') { await route.continue(); return }
    patches++; keys.push(route.request().headers()['idempotency-key']); proofs.push(route.request().headers()['if-match']); bodies.push(route.request().postDataJSON())
    if (patches !== 1) { await route.continue(); return }
    committedStatus = (await route.fetch()).status(); await route.abort('failed')
  })
  await page.goto(`/photos/edit?owner_type=item&owner_id=${sibling}&link_id=sibling`)
  await page.getByRole('textbox', { name: messages.media.description }).clear()
  await page.getByRole('button', { name: messages.media.reviewEdit }).click()
  await page.getByRole('alertdialog').getByRole('button', { name: messages.media.confirmEdit }).click()
  await expect(page.getByRole('heading', { name: messages.mutations.uncertain })).toBeVisible()
  expect(committedStatus).toBe(200); expect(patches).toBe(1)
  const deletion = await page.request.delete(`/api/v1/items/${sibling}`, { headers: await writeContext(page, application.url, 2) })
  expect(deletion.status()).toBe(200)
  await page.getByRole('alertdialog').getByRole('button', { name: messages.mutations.readCurrent }).click()
  await page.getByRole('alertdialog').getByRole('button', { name: messages.mutations.cancel }).click()
  await expect(page.getByText(messages.media.missing, { exact: true })).toBeVisible()
  await expect(page.getByRole('textbox', { name: messages.media.description })).toBeDisabled()
  await page.evaluate(() => window.dispatchEvent(new Event('online')))
  expect(patches).toBe(1)
  await page.getByRole('button', { name: messages.mutations.openOriginal }).click()
  await page.getByRole('alertdialog').getByRole('button', { name: messages.mutations.retryOriginal }).click()
  await expect(page.getByText(messages.mutations.done, { exact: true })).toBeVisible()
  expect(patches).toBe(2); expect(keys[0]).toBeTruthy(); expect(keys[1]).toBe(keys[0]); expect(proofs).toEqual(['"5"', '"5"']); expect(bodies).toEqual([{ description: '' }, { description: '' }])
})

test('keeps an uncertain unlink after the actual owner disappears and explicitly retries the original key', async ({ page, application }) => {
  await provision(page, application)
  let deletes = 0, committedStatus = 0
  const keys: (string | undefined)[] = [], proofs: (string | undefined)[] = []
  await page.route((url) => url.pathname === '/api/v1/media/links/sibling', async (route) => {
    if (route.request().method() !== 'DELETE') { await route.continue(); return }
    deletes++; keys.push(route.request().headers()['idempotency-key']); proofs.push(route.request().headers()['if-match'])
    if (deletes !== 1) { await route.continue(); return }
    committedStatus = (await route.fetch()).status()
    await route.abort('failed')
  })
  await page.goto(`/photos?owner_type=item&owner_id=${sibling}`)
  await page.getByRole('button', { name: `${messages.media.remove} ${JSON.stringify('sibling')}` }).click()
  await page.getByRole('alertdialog').getByRole('button', { name: messages.media.confirmRemove }).click()
  await expect(page.getByRole('heading', { name: messages.mutations.uncertain })).toBeVisible()
  expect(committedStatus).toBe(200); expect(deletes).toBe(1)
  const deletion = await page.request.delete(`/api/v1/items/${sibling}`, { headers: await writeContext(page, application.url, 2) })
  expect(deletion.status()).toBe(200)
  await page.getByRole('alertdialog').getByRole('button', { name: messages.mutations.readCurrent }).click()
  await page.getByRole('alertdialog').getByRole('button', { name: messages.mutations.cancel }).click()
  await expect(page.getByText(messages.media.missing, { exact: true })).toBeVisible()
  await page.evaluate(() => window.dispatchEvent(new Event('online')))
  expect(deletes).toBe(1)
  await page.getByRole('button', { name: messages.mutations.openOriginal }).click()
  await page.getByRole('alertdialog').getByRole('button', { name: messages.mutations.retryOriginal }).click()
  await expect(page.getByText(messages.mutations.done, { exact: true })).toBeVisible()
  expect(deletes).toBe(2); expect(keys[0]).toBeTruthy(); expect(keys[1]).toBe(keys[0]); expect(proofs).toEqual(['"5"', '"5"'])
})

test('requires a fresh review after an actual concurrent link edit', async ({ page, application }) => {
  await provision(page, application)
  const writes = watchDomainWrites(page, ['/api/v1/media'])
  await page.goto(`/photos?owner_type=item&owner_id=${sibling}`)
  const remove = page.getByRole('button', { name: `${messages.media.remove} ${JSON.stringify('sibling')}` })
  await remove.click()
  const changed = await page.request.patch('/api/v1/media/links/sibling', { headers: await writeContext(page, application.url, 5), data: { description: 'Concurrent' } })
  expect(changed.status()).toBe(200)
  const dialog = page.getByRole('alertdialog')
  await dialog.getByRole('button', { name: messages.media.confirmRemove }).click()
  await expect(dialog.getByText(messages.locations.conflict)).toBeVisible()
  await expect(dialog.getByText(messages.media.reviewStale)).toBeVisible()
  await expect(dialog.getByRole('button', { name: messages.media.confirmRemove })).toBeDisabled()
  await dialog.getByRole('button', { name: messages.mutations.cancel }).click()
  await remove.click()
  await page.getByRole('alertdialog').getByRole('button', { name: messages.media.confirmRemove }).click()
  await expect(page.getByText(messages.mutations.done, { exact: true })).toBeVisible()
  expect(writes.map((write) => write.proof)).toEqual(['"5"', '"6"'])
  expect(writes[0]?.key).not.toBe(writes[1]?.key)
})

test('does not submit or replay a Media unlink while offline', async ({ page, application }) => {
  await provision(page, application)
  const writes = watchDomainWrites(page, ['/api/v1/media'])
  await page.goto(`/photos?owner_type=item&owner_id=${sibling}`)
  const remove = page.getByRole('button', { name: `${messages.media.remove} ${JSON.stringify('sibling')}` })
  await expect(remove).toBeEnabled()
  try {
    await page.context().setOffline(true)
    await expect(page.getByText(messages.media.offline, { exact: true })).toBeVisible()
    await expect(remove).toBeDisabled()
    await page.context().setOffline(false)
    await expect(remove).toBeEnabled()
    expect(writes).toHaveLength(0)
  } finally { await page.context().setOffline(false) }
})

test('opens and unlinks actual Unit and Location photos from their existing detail pages', async ({ page, application }) => {
  await provision(page, application)
  const locationResponse = await page.request.post('/api/v1/locations', { headers: await writeContext(page, application.url), data: { name: 'Photo room', code: 'PHOTO-ROOM' } })
  expect(locationResponse.status()).toBe(200)
  const location = await locationResponse.json()
  const unitResponse = await page.request.post(`/api/v1/items/${empty}/units`, { headers: await writeContext(page, application.url, 1), data: { location_id: null } })
  expect(unitResponse.status()).toBe(200)
  const unit = await unitResponse.json()
  const unitLinkResponse = await page.request.post('/api/v1/media/links/links', { headers: await writeContext(page, application.url, 3), data: { item_unit_id: unit.id, owner_revision: unit.revision } })
  expect(unitLinkResponse.status()).toBe(200)
  const unitLink = await unitLinkResponse.json()
  const locationLinkResponse = await page.request.post('/api/v1/media/links/links', { headers: await writeContext(page, application.url, 4), data: { location_id: location.id, owner_revision: location.revision } })
  expect(locationLinkResponse.status()).toBe(200)
  const locationLink = await locationLinkResponse.json()
  const writes = watchDomainWrites(page, ['/api/v1/media'])
  await page.goto(`/units/${unit.id}`)
  await page.getByRole('link', { name: messages.media.nav, exact: true }).click()
  await expect(page.getByRole('heading', { name: unit.asset_code, exact: true })).toBeVisible()
  await page.getByRole('button', { name: `${messages.media.remove} ${JSON.stringify(unitLink.id)}` }).click()
  await page.getByRole('alertdialog').getByRole('button', { name: messages.media.confirmRemove }).click()
  await expect(page.getByText(messages.media.empty, { exact: true })).toBeVisible()
  await page.goto(`/locations/${location.id}`)
  await page.getByRole('link', { name: messages.media.nav, exact: true }).click()
  await expect(page.getByRole('heading', { name: 'Photo room', exact: true })).toBeVisible()
  await page.getByRole('button', { name: `${messages.media.remove} ${JSON.stringify(locationLink.id)}` }).click()
  await page.getByRole('alertdialog').getByRole('button', { name: messages.media.confirmRemove }).click()
  await expect(page.getByText(messages.media.empty, { exact: true })).toBeVisible()
  expect(writes.map((write) => ({ method: write.method, proof: write.proof, body: write.body }))).toEqual([
    { method: 'DELETE', proof: '"1"', body: null }, { method: 'DELETE', proof: '"1"', body: null },
  ])
  const remaining = await (await page.request.get('/api/v1/media/links/links')).json()
  expect(remaining.data).toMatchObject([{ id: 'reserved', owner_id: sibling }])
})

test('returns through login with only the exact Media owner scope and safe catalog navigation', async ({ page, application }) => {
  const password = await provision(page, application)
  await page.goto('/items?status=active')
  await page.getByRole('link', { name: new RegExp(sibling) }).click()
  await page.getByRole('link', { name: messages.media.nav, exact: true }).click()
  await expect(page.getByText(`${messages.media.total}2`)).toBeVisible()
  await page.context().clearCookies()
  await page.reload()
  await expect(page.getByRole('heading', { name: messages.auth.loginHeading })).toBeVisible()
  await page.getByLabel(new RegExp(`^${messages.auth.username}`)).fill('owner')
  await page.getByLabel(new RegExp(`^${messages.auth.password}`)).fill(password)
  await page.getByRole('button', { name: messages.auth.login, exact: true }).click()
  await expect(page).toHaveURL(new RegExp(`/photos\\?owner_type=item&owner_id=${sibling}$`))
  await expect(page.getByText(`${messages.media.total}2`)).toBeVisible()
  await page.getByRole('link', { name: messages.media.backItem }).click()
  await page.getByRole('link', { name: messages.catalog.back, exact: true }).click()
  await expect(page).toHaveURL(/\/items\?status=active$/)
})

test('returns through login to the attachment owner without carrying the selected Media or draft', async ({ page, application }) => {
  const password = await provision(page, application)
  await page.goto('/items?status=active')
  await page.getByRole('link', { name: new RegExp(empty) }).click()
  await page.getByRole('link', { name: messages.media.nav, exact: true }).click()
  await page.getByRole('link', { name: messages.media.attach, exact: true }).click()
  await lookupExistingMedia(page, 'links')
  await page.getByRole('textbox', { name: messages.media.description }).fill('Private draft')
  await page.context().clearCookies()
  await page.getByRole('button', { name: messages.media.refresh }).click()
  await expect(page.getByRole('heading', { name: messages.auth.loginHeading })).toBeVisible()
  await page.getByLabel(new RegExp(`^${messages.auth.username}`)).fill('owner')
  await page.getByLabel(new RegExp(`^${messages.auth.password}`)).fill(password)
  await page.getByRole('button', { name: messages.auth.login, exact: true }).click()
  await expect(page).toHaveURL(new RegExp(`/photos/attach\\?owner_type=item&owner_id=${empty}$`))
  await expect(page.getByRole('textbox', { name: messages.media.mediaID })).toHaveValue('')
  await expect(page.getByRole('textbox', { name: messages.media.description })).toHaveValue('')
  await expect(page.getByRole('heading', { name: messages.media.selectedMedia })).toHaveCount(0)
  await page.getByRole('link', { name: messages.media.backLinks }).click()
  await page.getByRole('link', { name: messages.media.backItem }).click()
  await page.getByRole('link', { name: messages.catalog.back, exact: true }).click()
  await expect(page).toHaveURL(/\/items\?status=active$/)
})

test('returns through login to the exact selected Media link without carrying a draft', async ({ page, application }) => {
  const password = await provision(page, application)
  await page.goto('/items?status=active')
  await page.getByRole('link', { name: new RegExp(sibling) }).click()
  await page.getByRole('link', { name: messages.media.nav, exact: true }).click()
  await page.getByRole('link', { name: `${messages.media.edit} ${JSON.stringify('sibling')}` }).click()
  await expect(page.getByRole('textbox', { name: messages.media.order })).toHaveValue('-3')
  await page.context().clearCookies(); await page.reload()
  await expect(page.getByRole('heading', { name: messages.auth.loginHeading })).toBeVisible()
  await page.getByLabel(new RegExp(`^${messages.auth.username}`)).fill('owner')
  await page.getByLabel(new RegExp(`^${messages.auth.password}`)).fill(password)
  await page.getByRole('button', { name: messages.auth.login, exact: true }).click()
  await expect(page).toHaveURL(new RegExp(`/photos/edit\\?owner_type=item&owner_id=${sibling}&link_id=sibling$`))
  await expect(page.getByRole('textbox', { name: messages.media.order })).toHaveValue('-3')
  await page.getByRole('link', { name: messages.media.backLinks }).click()
  await page.getByRole('link', { name: messages.media.backItem }).click()
  await page.getByRole('link', { name: messages.catalog.back, exact: true }).click()
  await expect(page).toHaveURL(/\/items\?status=active$/)
})

test('preserves an offline Media edit without submitting it on reconnect', async ({ page, application }) => {
  await provision(page, application)
  const writes = watchDomainWrites(page, ['/api/v1/media'])
  await page.goto(`/photos/edit?owner_type=item&owner_id=${sibling}&link_id=sibling`)
  const description = page.getByRole('textbox', { name: messages.media.description }), review = page.getByRole('button', { name: messages.media.reviewEdit })
  await description.fill('Offline draft')
  try {
    await page.context().setOffline(true)
    await expect(page.getByText(messages.media.offline, { exact: true })).toBeVisible()
    await expect(description).toHaveValue('Offline draft'); await expect(review).toBeDisabled()
    await page.context().setOffline(false)
    await expect(review).toBeEnabled()
    expect(writes).toHaveLength(0)
  } finally { await page.context().setOffline(false) }
})

test('preserves legacy order and raw edits during explicit adoption against real Media source', async ({ page, application }) => {
  await provision(page, application)
  const bodies: unknown[] = []
  page.on('request', (request) => {
    if (request.method() === 'PATCH' && new URL(request.url()).pathname === '/api/v1/media/links/sibling') bodies.push(request.postDataJSON())
  })
  await page.goto('/')
  const result = await page.evaluate(async ({ sibling }) => {
    const modulePath = '/assets/media-sdk.js', api = await import(modulePath), signal = () => new AbortController().signal
    const csrf = (await (await fetch('/api/v1/auth/csrf')).json()).token as string
    const source = await api.readOwnerMedia({ kind: 'item', id: sibling }, signal())
    const original = source.links.find((value: { id: string }) => value.id === 'sibling')
    if (!original) throw new Error('Fixture link missing')
    const defaults = api.mediaLinkDefaults(original)
    const firstBody = api.mediaLinkPatch({ ...defaults, description: '' }, original)
    const first = await api.sendMediaCommand(api.freezeMediaCommand({ kind: 'patch-link', source, linkID: original.id, body: firstBody }), csrf, signal())
    const reviewed = await api.readOwnerMedia({ kind: 'item', id: sibling }, signal())
    const before = reviewed.links.find((value: { id: string }) => value.id === 'sibling')
    if (!before) throw new Error('Reviewed link missing')
    const local = { ...api.mediaLinkDefaults(before), sort_order: '01', description: 'Local é\n' }
    await api.patchMediaLink(before.id, { is_cover: false, sort_order: 6, description: 'Concurrent' }, {
      csrfToken: csrf, idempotencyKey: crypto.randomUUID(), signal: signal(), link: before,
    })
    const updated = await api.readOwnerMedia({ kind: 'item', id: sibling }, signal())
    const current = updated.links.find((value: { id: string }) => value.id === 'sibling')
    if (!current) throw new Error('Current link missing')
    const retained = api.retainMediaLinkEdits(local, before, current)
    let invalid: unknown
    try { api.mediaLinkPatch(retained, current) } catch (error) { invalid = error instanceof Error && 'code' in error ? error.code : 'unexpected' }
    const finalBody = api.mediaLinkPatch({ ...retained, sort_order: '0' }, current)
    const final = await api.sendMediaCommand(api.freezeMediaCommand({ kind: 'patch-link', source: updated, linkID: current.id, body: finalBody }), csrf, signal())
    return { defaults, first, retained, invalid, final }
  }, { sibling })
  expect(result.defaults).toEqual({ is_cover: true, sort_order: '-3', description: '\0Exact é\n' })
  expect(result.first).toMatchObject({ revision: 6, is_cover: true, sort_order: -3, description: '' })
  expect(result.retained).toEqual({ is_cover: false, sort_order: '01', description: 'Local é\n' })
  expect(result.invalid).toBe('VALIDATION_ERROR')
  expect(result.final).toMatchObject({ revision: 8, is_cover: false, sort_order: 0, description: 'Local é\n' })
  expect(bodies).toEqual([{ description: '' }, { is_cover: false, sort_order: 6, description: 'Concurrent' }, { sort_order: 0, description: 'Local é\n' }])
})

test('invalidates Unit location scope even when the Unit moves after link review', async ({ page, application }) => {
  await provision(page, application)
  await page.goto('/')
  const result = await page.evaluate(async ({ empty }) => {
    const modulePath = '/assets/media-sdk.js', api = await import(modulePath), signal = () => new AbortController().signal
    const csrf = (await (await fetch('/api/v1/auth/csrf')).json()).token as string
    const context = () => ({ csrfToken: csrf, idempotencyKey: crypto.randomUUID(), signal: signal() })
    const location = await api.createLocation({ name: 'Moved room', code: 'MOVED' }, context())
    const item = await api.fetchItem(empty, signal())
    const unit = await api.createUnit(empty, { location_id: null }, { ...context(), expectedRevision: item.revision })
    const media = await api.fetchMedia('links', signal())
    const link = await api.attachMedia('links', {}, { ...context(), media, owner: { kind: 'unit', value: unit } })
    const source = await api.readOwnerMedia({ kind: 'unit', id: unit.id }, signal())
    const command = api.freezeMediaCommand({ kind: 'unlink', source, linkID: link.id })
    const moved = await api.moveUnit(unit.id, { target_location_id: location.id }, { ...context(), itemID: empty, expectedRevision: source.owner.value.revision })
    const before = await api.fetchLocation(location.id, signal())
    const removed = await api.sendMediaCommand(command, csrf, signal())
    const after = await api.fetchLocation(location.id, signal())
    return { capturedLocation: command.source.owner.value.location_id, roots: api.mediaCommandReadRoots(command), moved, before, after, removed }
  }, { empty })
  expect(result.capturedLocation).toBeNull(); expect(result.roots).toContainEqual(['locations'])
  expect(result.moved.location_id).toBe(result.before.id)
  expect(result.removed).toMatchObject({ ok: true, revision: 2 })
  expect(result.after.revision).toBe(result.before.revision + 1)
})

test('reconciles an actual lost unlink response with the identical frozen review command', async ({ page, application }) => {
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
    const source = await api.readOwnerMedia({ kind: 'item', id: sibling }, signal())
    return { api, csrf, command: api.freezeMediaCommand({ kind: 'unlink', source, linkID: 'sibling' }), sibling }
  }, { sibling })
  const first = await state.evaluate(async ({ api, csrf, command }) => {
    try { await api.sendMediaCommand(command, csrf, new AbortController().signal); return 'unexpected' }
    catch (error) { return error instanceof Error && 'code' in error ? error.code : 'unexpected' }
  })
  expect(first).toBe('NETWORK_ERROR'); expect(committedStatus).toBe(200); expect(deletes).toBe(1)
  const current = await state.evaluate(async ({ api, sibling }) => {
    window.dispatchEvent(new Event('online'))
    return { owner: await api.readOwnerMedia({ kind: 'item', id: sibling }, new AbortController().signal),
      media: await api.fetchMedia('photo', new AbortController().signal) }
  })
  expect(current.owner.links).toMatchObject([{ id: 'reserved' }]); expect(current.media.revision).toBe(8); expect(deletes).toBe(1)
  const retry = await state.evaluate(async ({ api, csrf, command }) => api.sendMediaCommand(command, csrf, new AbortController().signal))
  expect(retry).toMatchObject({ ok: true, revision: 6 }); expect(retry.operation_id).toBeTruthy(); expect(deletes).toBe(2)
  await state.dispose()
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
