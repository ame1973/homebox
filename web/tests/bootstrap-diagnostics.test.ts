import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { existsSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { DatabaseSync } from 'node:sqlite'
import { test, type TestContext } from 'node:test'
import { inspectBootstrapFixture, safeBootstrapCode } from './e2e/bootstrap-diagnostics.ts'

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
    epochMatches: true, requestBeforeCreated: false, responseAfterExpiry: false, accountCount: 0 })
  assert.deepEqual(readFileSync(path), before)
  const printed = JSON.stringify(result)
  for (const sensitive of [path, token, hash(token), 'epoch-fixture']) assert.ok(!printed.includes(sensitive))
})

test('distinguishes wrong token, epoch, missing rows and unparseable times without printing values', (t) => {
  const mismatch = fixture(t, { epoch: 'other-private-epoch' })
  const wrong = Buffer.alloc(32, 8).toString('base64url')
  assert.deepEqual(inspectBootstrapFixture(mismatch.path, wrong, 0, Date.parse('2026-01-01T01:00:00Z')),
    { tokenCanonical: true, databaseReadable: true, rowPresent: true, tokenMatches: false, epochMatches: false,
      requestBeforeCreated: true, responseAfterExpiry: true, accountCount: 0 })
  const missing = fixture(t, { bootstrap: false })
  assert.deepEqual(inspectBootstrapFixture(missing.path, 'not-a-token', 0, 1),
    { tokenCanonical: false, databaseReadable: true, rowPresent: false, tokenMatches: null, epochMatches: null,
      requestBeforeCreated: null, responseAfterExpiry: null, accountCount: 0 })
  const invalid = fixture(t, { created: 'private-invalid-time', expires: 'private-invalid-time' })
  const result = inspectBootstrapFixture(invalid.path, token, 0, 1)
  assert.equal(result.requestBeforeCreated, null)
  assert.equal(result.responseAfterExpiry, null)
  assert.ok(!JSON.stringify(result).includes('private-invalid-time'))
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
