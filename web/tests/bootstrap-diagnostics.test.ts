import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { existsSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { DatabaseSync } from 'node:sqlite'
import { test, type TestContext } from 'node:test'
import { inspectBootstrapFixture, safeBootstrapCode } from './e2e/bootstrap-diagnostics.ts'
import { runInBootstrapWindow, waitForBootstrapWindow } from './e2e/bootstrap-clock.ts'

const token = Buffer.alloc(32, 7).toString('base64url')
const hash = (value: string) => createHash('sha256').update(value).digest('hex')

function fixture(t: TestContext, options: { bootstrap?: boolean; epoch?: string; created?: string; expires?: string } = {}) {
  const directory = mkdtempSync(join(tmpdir(), 'homebox-bootstrap-diagnostic-'))
  t.after(() => rmSync(directory, { recursive: true, force: true }))
  const path = join(directory, 'homebox.sqlite'), db = new DatabaseSync(path)
  db.exec('CREATE TABLE auth_bootstrap(singleton INTEGER, token_hash TEXT, created_at TEXT, expires_at TEXT, dataset_epoch TEXT); CREATE TABLE sync_state(singleton INTEGER, dataset_epoch TEXT); CREATE TABLE admin_accounts(id TEXT)')
  db.prepare('INSERT INTO sync_state VALUES(1, ?)').run('epoch-fixture')
  if (options.bootstrap !== false) db.prepare('INSERT INTO auth_bootstrap VALUES(1, ?, ?, ?, ?)').run(hash(token), options.created ?? '2026-01-01T00:00:00Z', options.expires ?? '2026-01-01T00:30:00Z', options.epoch ?? 'epoch-fixture')
  db.close()
  return { directory, path }
}

test('reports only bounded relationships from a read-only fixture snapshot', (t) => {
  const { path } = fixture(t), before = readFileSync(path)
  const result = inspectBootstrapFixture(path, token, Date.parse('2026-01-01T00:00:01Z'), Date.parse('2026-01-01T00:00:02Z'))
  assert.deepEqual(result, { tokenCanonical: true, databaseReadable: true, rowPresent: true, tokenMatches: true,
    epochMatches: true, fixtureBeforeCreated: null, requestBeforeFixtureCheck: null, requestBeforeCreated: false, responseBeforeCreated: false, responseAfterExpiry: false, accountCount: 0 })
  assert.deepEqual(readFileSync(path), before)
  const printed = JSON.stringify(result)
  for (const sensitive of [path, token, hash(token), 'epoch-fixture']) assert.ok(!printed.includes(sensitive))
})

test('distinguishes wrong token, epoch, missing rows and unparseable times without printing values', (t) => {
  const mismatch = fixture(t, { epoch: 'other-private-epoch' })
  const wrong = Buffer.alloc(32, 8).toString('base64url')
  assert.deepEqual(inspectBootstrapFixture(mismatch.path, wrong, 0, Date.parse('2026-01-01T01:00:00Z')),
    { tokenCanonical: true, databaseReadable: true, rowPresent: true, tokenMatches: false, epochMatches: false,
      fixtureBeforeCreated: null, requestBeforeFixtureCheck: null, requestBeforeCreated: true, responseBeforeCreated: false, responseAfterExpiry: true, accountCount: 0 })
  const missing = fixture(t, { bootstrap: false })
  assert.deepEqual(inspectBootstrapFixture(missing.path, 'not-a-token', 0, 1),
    { tokenCanonical: false, databaseReadable: true, rowPresent: false, tokenMatches: null, epochMatches: null,
      fixtureBeforeCreated: null, requestBeforeFixtureCheck: null, requestBeforeCreated: null, responseBeforeCreated: null, responseAfterExpiry: null, accountCount: 0 })
  const invalid = fixture(t, { created: 'private-invalid-time', expires: 'private-invalid-time' })
  const result = inspectBootstrapFixture(invalid.path, token, 0, 1)
  assert.equal(result.requestBeforeCreated, null)
  assert.equal(result.responseBeforeCreated, null)
  assert.equal(result.responseAfterExpiry, null)
  assert.ok(!JSON.stringify(result).includes('private-invalid-time'))
})

test('distinguishes a response completed before issuance without reporting timestamps', (t) => {
  const { path } = fixture(t)
  const issued = Date.parse('2026-01-01T00:00:00Z')
  const result = inspectBootstrapFixture(path, token, issued - 2, issued - 1)
  assert.equal(result.requestBeforeCreated, true)
  assert.equal(result.responseBeforeCreated, true)
  assert.equal(result.responseAfterExpiry, false)
  for (const sensitive of [path, token, hash(token), String(issued)]) assert.ok(!JSON.stringify(result).includes(sensitive))
})

test('marks same-millisecond timestamp ordering as unknown', (t) => {
  const created = '2026-01-01T00:00:00.123456789Z', expires = '2026-01-01T00:30:00.987654321Z'
  const { path } = fixture(t, { created, expires })
  const sameCreated = Date.parse(created), sameExpiry = Date.parse(expires)
  const start = inspectBootstrapFixture(path, token, sameCreated, sameCreated)
  assert.equal(start.requestBeforeCreated, null)
  assert.equal(start.responseBeforeCreated, null)
  const end = inspectBootstrapFixture(path, token, sameCreated + 1, sameExpiry)
  assert.equal(end.responseAfterExpiry, null)
})

test('waits before the first setup request when the observed clock precedes issuance', async () => {
  const issued = Date.parse('2026-11-27T12:00:00.123Z')
  const expires = '2026-11-27T12:30:00.123456789Z'
  let now = issued - 250
  const waits: number[] = []
  const waited = await waitForBootstrapWindow(expires, () => now, async (delay) => { waits.push(delay); now += delay })
  assert.equal(waited, true)
  assert.deepEqual(waits, [251])
  assert.equal(now, issued + 1)
  assert.equal(await waitForBootstrapWindow(expires, () => now, async () => { throw new Error('unexpected wait') }), false)
  now = issued - 9999
  assert.equal(await waitForBootstrapWindow(expires, () => now, async (delay) => { assert.equal(delay, 10_000); now += delay }), true)
})

test('fails closed on invalid, expired, excessively early or repeatedly rolled-back clocks', async () => {
  const issued = Date.parse('2026-11-27T12:00:00.000Z'), expires = '2026-11-27T12:30:00Z'
  const neverWait = async () => { throw new Error('unexpected wait') }
  for (const value of [undefined, null, 123, '', 'not a timestamp']) {
    await assert.rejects(waitForBootstrapWindow(value, () => issued, neverWait), /Invalid private bootstrap metadata/)
  }
  await assert.rejects(waitForBootstrapWindow(expires, () => NaN, neverWait), /clock/)
  await assert.rejects(waitForBootstrapWindow(expires, () => issued + 30 * 60_000, neverWait), /expired/)
  await assert.rejects(waitForBootstrapWindow(expires, () => issued - 10_000, neverWait), /clock/)
  let now = issued - 10
  await assert.rejects(waitForBootstrapWindow(expires, () => now, async () => { now -= 10 }), /clock/)
  now = issued - 10
  await assert.rejects(waitForBootstrapWindow(expires, () => now, async () => { now = issued + 30 * 60_000 }), /expired/)
})

test('checks the clock again immediately before one setup request', async () => {
  const issued = Date.parse('2026-11-27T12:00:00Z'), expires = '2026-11-27T12:30:00Z'
  const readings = [issued + 5, issued - 1]
  let requests = 0
  await assert.rejects(runInBootstrapWindow(expires, async () => { requests++; return 'sent' }, () => readings.shift() ?? NaN), /clock/)
  assert.equal(requests, 0)

  const result = await runInBootstrapWindow(expires, async (checkedAt) => {
    requests++
    assert.equal(checkedAt, issued + 1)
    return 'sent'
  }, () => issued + 1)
  assert.equal(result, 'sent')
  assert.equal(requests, 1)
  await assert.rejects(runInBootstrapWindow(expires, async () => { requests++; throw new Error('failed request') }, () => issued + 1), /failed request/)
  assert.equal(requests, 2)
  for (const finalClock of [issued, issued + 30 * 60_000, NaN]) {
    const times = [issued + 1, finalClock]
    await assert.rejects(runInBootstrapWindow(expires, async () => { requests++; return 'sent' }, () => times.shift() ?? NaN), /clock|expired/)
    assert.equal(requests, 2)
  }
})

test('a bounded wait precedes the only bootstrap dispatch', async () => {
  const issued = Date.parse('2026-11-27T12:00:00Z'), expires = '2026-11-27T12:30:00Z'
  let now = issued - 20, requests = 0
  const warnings: unknown[][] = [], original = console.warn
  console.warn = (...values: unknown[]) => { warnings.push(values) }
  try {
    const result = await runInBootstrapWindow(expires, async (checkedAt) => {
      requests++
      assert.equal(checkedAt, issued + 1)
      return 'sent'
    }, () => now, async (delay) => { assert.equal(delay, 21); now += delay })
    assert.equal(result, 'sent')
  } finally { console.warn = original }
  assert.equal(requests, 1)
  assert.deepEqual(warnings, [['Private bootstrap clock was before issuance; waited before the setup request']])
})

test('records only bounded relationships between fixture and request clocks', (t) => {
  const { path } = fixture(t), issued = Date.parse('2026-01-01T00:00:00Z')
  const result = inspectBootstrapFixture(path, token, issued - 1, issued + 1, issued + 10)
  assert.equal(result.fixtureBeforeCreated, false)
  assert.equal(result.requestBeforeFixtureCheck, true)
  const before = inspectBootstrapFixture(path, token, issued + 2, issued + 3, issued - 1)
  assert.equal(before.fixtureBeforeCreated, true)
  assert.equal(before.requestBeforeFixtureCheck, false)
  const ambiguous = inspectBootstrapFixture(path, token, issued, issued + 1, issued)
  assert.equal(ambiguous.fixtureBeforeCreated, null)
  assert.equal(ambiguous.requestBeforeFixtureCheck, null)
  for (const sensitive of [path, token, hash(token), String(issued)]) assert.ok(!JSON.stringify(result).includes(sensitive))
})

test('does not create a missing database or expose errors, and only accepts known response codes', (t) => {
  const { directory } = fixture(t), missing = join(directory, 'private-missing.sqlite')
  const result = inspectBootstrapFixture(missing, token, 0, 1)
  assert.equal(result.databaseReadable, false)
  assert.equal(existsSync(missing), false)
  assert.ok(!JSON.stringify(result).includes(missing))
  assert.equal(safeBootstrapCode('AUTHENTICATION_REQUIRED'), 'AUTHENTICATION_REQUIRED')
  assert.equal(safeBootstrapCode('private arbitrary message'), 'OTHER')
  assert.equal(safeBootstrapCode(null), 'OTHER')
})
