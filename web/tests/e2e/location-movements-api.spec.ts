import { cpSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { DatabaseSync } from 'node:sqlite'
import { fileURLToPath } from 'node:url'
import { build } from 'vite'
import { test as applicationTest, expect } from './auth-fixture.ts'
import { provision, writeContext } from './domain-helpers.ts'

const test = applicationTest.extend<{}, { applicationWebDirectory: string }>({
  applicationWebDirectory: [async ({ browserName }, provideFixture) => {
    const directory = mkdtempSync(join(tmpdir(), `homebox-location-movements-sdk-${browserName}-`))
    const web = join(directory, 'web'), sdk = join(directory, 'sdk'), entry = join(directory, 'entry.ts')
    try {
      cpSync(fileURLToPath(new URL('../../dist', import.meta.url)), web, { recursive: true })
      // This SDK entry exists only in the disposable browser fixture.
      writeFileSync(entry, `export * from ${JSON.stringify(fileURLToPath(new URL('../../src/lib/api/locations.ts', import.meta.url)))};\n`)
      await build({ configFile: false, logLevel: 'silent', build: { outDir: sdk, minify: false, lib: { entry, formats: ['es'], fileName: 'location-movements-sdk' } } })
      cpSync(join(sdk, 'location-movements-sdk.js'), join(web, 'assets', 'location-movements-sdk.js'))
      await provideFixture(web)
    } finally { rmSync(directory, { recursive: true, force: true }) }
  }, { scope: 'worker' }],
})

test.use({ trace: 'off', screenshot: 'off', video: 'off' })

test('reads actual moves, interchanges cursors and retains deleted history without offline replay', async ({ page, context, application }) => {
  await provision(page, application)
  async function post(path: string, data: object, revision?: number) {
    const response = await page.request.post(path, { headers: await writeContext(page, application.url, revision), data })
    expect(response.status(), path).toBe(200)
    return response.json() as Promise<{ id: string; revision: number }>
  }
  const first = await post('/api/v1/locations', { name: 'Movement first', code: 'MOVEMENT-FIRST' })
  const second = await post('/api/v1/locations', { name: 'Movement second', code: 'MOVEMENT-SECOND' })
  let subject = await post('/api/v1/locations', { name: 'Movement subject', code: 'MOVEMENT-SUBJECT', parent_id: first.id }, first.revision)
  subject = await post(`/api/v1/locations/${subject.id}/move`, { target_parent_id: second.id }, subject.revision)
  subject = await post(`/api/v1/locations/${subject.id}/move`, { target_parent_id: null }, subject.revision)
  const methods: string[] = []
  const endpoint = (url: URL) => url.pathname === `/api/v1/locations/${subject.id}/movements`
  page.on('request', (request) => { if (endpoint(new URL(request.url()))) methods.push(request.method()) })
  await page.goto('/')
  const read = (query: object) => page.evaluate(async ({ id, values }) => {
    const modulePath = '/assets/location-movements-sdk.js', api = await import(modulePath)
    try { return await api.fetchLocationMovements(id, values, new AbortController().signal) }
    catch (error) { return { code: error instanceof Error && 'code' in error ? error.code : 'unexpected' } }
  }, { id: subject.id, values: query })
  const pageOne = await read({ limit: 1 })
  expect(pageOne.value).toMatchObject({ location_id: subject.id, deleted: false, data: [{ location_id: subject.id }] })
  expect(pageOne.value.next_cursor).toBeTruthy()
  const alias = await page.request.get('/api/v1/location-movements', { params: { id: subject.id, limit: '1', cursor: pageOne.value.next_cursor }, headers: { Origin: application.url } })
  expect(alias.status()).toBe(200)
  const pageTwo = await read({ limit: 1, cursor: pageOne.value.next_cursor })
  expect(pageTwo.value).toEqual(await alias.json())
  expect(pageTwo.revision).toBe(pageOne.revision)
  expect(pageTwo.value.next_cursor).toBeNull()
  expect(await read({ limit: 2, cursor: pageOne.value.next_cursor })).toEqual({ code: 'INVALID_CURSOR' })
  const ids = new Set([...pageOne.value.data, ...pageTwo.value.data].map((event: { id: string }) => event.id))
  expect(ids.size).toBe(2)
  expect([...pageOne.value.data, ...pageTwo.value.data]).toEqual(expect.arrayContaining([
    expect.objectContaining({ from_parent_id: first.id, to_parent_id: second.id }),
    expect.objectContaining({ from_parent_id: second.id, to_parent_id: null }),
  ]))
  const removed = await page.request.delete(`/api/v1/locations/${subject.id}`, { headers: await writeContext(page, application.url, subject.revision) })
  expect(removed.status()).toBe(200)
  expect(await read({ limit: 1, cursor: pageOne.value.next_cursor })).toEqual({ code: 'INVALID_CURSOR' })
  const retained = await read({})
  expect(retained.value).toMatchObject({ location_id: subject.id, deleted: true, data: expect.arrayContaining([...pageOne.value.data, ...pageTwo.value.data]), next_cursor: null })
  expect(retained.revision).toBeGreaterThan(pageOne.revision)
  for (const value of [{ ...retained.value, location_id: 'other' }, { ...retained.value, next_cursor: '' },
    JSON.parse(JSON.stringify(retained.value).replace(/}$/, ',"__proto__":{"private":true}}'))]) {
    await page.route(endpoint, (route) => route.fulfill({ status: 200, json: value, headers: { ETag: `"${retained.revision}"` } }))
    expect(await read({})).toEqual({ code: 'INVALID_RESPONSE' })
    await page.unroute(endpoint)
  }
  const before = methods.length
  await context.setOffline(true)
  expect(await read({})).toEqual({ code: 'NETWORK_ERROR' })
  await context.setOffline(false)
  await page.evaluate(() => window.dispatchEvent(new Event('online')))
  expect(methods).toHaveLength(before + 1)
  expect(methods.every((method) => method === 'GET')).toBe(true)
})

const legacyOwners = ['legacy/location\0😀', '.', '..', 'back\\slash', 'literal%2F', 'name+plus', 'space here', 'what?now', 'unité', 'semi;colon', "quote'group", 'star*value', 'paren(value)', 'new', 'tree', 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~']
const bulkOwner = legacyOwners[0]!
function prepareLegacyMovements(database: string) {
  // Offline fixtures contain actual retained source and counters, not invented receipts.
  const db = new DatabaseSync(database)
  try {
    db.exec('PRAGMA foreign_keys=ON; BEGIN IMMEDIATE')
    const location = db.prepare("INSERT INTO locations(id,code,name,created_at,updated_at,deleted_at) VALUES(?,?,?,'legacy','legacy',?)")
    const counter = db.prepare('INSERT INTO projection_revisions(entity_type,entity_id,revision) VALUES(?,?,?)')
    const movement = db.prepare('INSERT INTO location_events(id,location_id,occurred_at,from_parent_id,to_parent_id) VALUES(?,?,?,?,?)')
    location.run('legacy-parent', 'LEGACY-PARENT', 'Historical parent', 'legacy deletion')
    counter.run('location', 'legacy-parent', 3)
    for (const [index, id] of legacyOwners.entries()) {
      location.run(id, `LEGACY-MOVEMENT-${index}`, 'Retained owner', index === 0 ? 'legacy deletion' : null)
      counter.run('location', id, 7)
      if (index > 0) {
        const eventID = `other-event-${String(index).padStart(3, '0')}`
        movement.run(eventID, id, 'legacy\0時間', 'legacy-parent', null)
        counter.run('location_event', eventID, 1)
      }
    }
    for (const id of ['\0event', ...Array.from({ length: 205 }, (_, index) => `event-${String(index).padStart(3, '0')}`), '', '😀']) {
      movement.run(id, bulkOwner, 'legacy\0時間', 'legacy-parent', null)
      counter.run('location_event', id, 1)
    }
    db.exec('COMMIT')
    expect(db.prepare('PRAGMA foreign_key_check').all()).toEqual([])
  } finally { db.close() }
}

test.describe('retained legacy movement identities', () => {
  test.use({ applicationDatabaseFixture: { prepare: prepareLegacyMovements } })
  test('preserves exact owners, source text and UTF-8 paging across a fresh session', async ({ page, application }) => {
    const password = await provision(page, application)
    await page.goto('/')
    const requests: string[] = []
    page.on('request', (request) => {
      const url = new URL(request.url())
      if (url.pathname === '/api/v1/location-movements' || url.pathname.endsWith('/movements')) requests.push(url.pathname)
    })
    const read = (id: string, query: object = {}) => page.evaluate(async ({ id, query }) => {
      const modulePath = '/assets/location-movements-sdk.js', api = await import(modulePath)
      return api.fetchLocationMovements(id, query, new AbortController().signal)
    }, { id, query })
    for (const id of legacyOwners.slice(1)) {
      const result = await read(id)
      expect(result).toMatchObject({ revision: 7, value: { location_id: id, deleted: false, data: [{ location_id: id, occurred_at: 'legacy\0時間', from_parent_id: 'legacy-parent', to_parent_id: null }], next_cursor: null } })
      expect(requests.at(-1)).toBe(/^[A-Za-z0-9._~-]+$/u.test(id) && id !== '.' && id !== '..' ? `/api/v1/locations/${id}/movements` : '/api/v1/location-movements')
    }
    const pages = [await read(bulkOwner, { limit: 100 })]
    expect(pages[0].value.next_cursor).toBeTruthy()
    expect((await page.request.post('/api/v1/auth/logout', { headers: await writeContext(page, application.url) })).status()).toBe(200)
    expect((await page.request.post('/api/v1/auth/login', { headers: { Origin: application.url }, data: { username: 'owner', password } })).status()).toBe(200)
    while (pages.at(-1).value.next_cursor) {
      expect(pages.length).toBeLessThan(4)
      pages.push(await read(bulkOwner, { limit: 100, cursor: pages.at(-1).value.next_cursor }))
    }
    expect(pages).toHaveLength(3)
    expect(pages.every((result) => result.revision === 7 && result.value.location_id === bulkOwner && result.value.deleted)).toBe(true)
    const rows = pages.flatMap((result) => result.value.data)
    expect(rows).toHaveLength(208)
    expect(new Set(rows.map((row: { id: string }) => row.id)).size).toBe(208)
    expect(rows[0]).toMatchObject({ id: '\0event', location_id: bulkOwner, occurred_at: 'legacy\0時間', from_parent_id: 'legacy-parent', to_parent_id: null })
    expect(rows.slice(-2).map((row: { id: string }) => row.id)).toEqual(['', '😀'])
  })
})
