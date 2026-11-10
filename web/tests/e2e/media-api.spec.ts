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
      writeFileSync(entry, `export * from ${JSON.stringify(fileURLToPath(new URL('../../src/lib/api/media.ts', import.meta.url)))};\n`)
      await build({ configFile: false, logLevel: 'silent', build: { outDir: sdk, minify: false, lib: { entry, formats: ['es'], fileName: 'media-sdk' } } })
      cpSync(join(sdk, 'media-sdk.js'), join(web, 'assets', 'media-sdk.js'))
      await provideFixture(web)
    } finally { rmSync(directory, { recursive: true, force: true }) }
  }, { scope: 'worker' }],
})

test.use({ trace: 'off', screenshot: 'off', video: 'off', applicationDatabaseFixture: { prepare(database) {
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
    for (let index = 0; index < 51; index++) seedLink(`owner-${String(index).padStart(3, '0')}`, 'photo', owner, 1)
    seedLink('sibling', 'photo', sibling, 5)
    seedLink('reserved', 'links', sibling, 1)
    db.exec('COMMIT')
    expect(db.prepare('PRAGMA foreign_key_check').all()).toEqual([])
  } finally { db.close() }
} } })

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
