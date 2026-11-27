import { createHash } from 'node:crypto'
import { DatabaseSync } from 'node:sqlite'

const codes = new Set(['AUTHENTICATION_REQUIRED', 'SETUP_CLOSED', 'VALIDATION_ERROR', 'RATE_LIMITED', 'SERVICE_UNAVAILABLE'])
export function safeBootstrapCode(value: unknown): string {
  return typeof value === 'string' && codes.has(value) ? value : 'OTHER'
}

function earlierMillisecond(left: unknown, right: unknown): boolean | null {
  return typeof left === 'number' && typeof right === 'number' && Number.isSafeInteger(left) && Number.isSafeInteger(right) && left !== right ? left < right : null
}

// This examines a disposable test store after a failed request. Relationships
// are not proof of the exact database state while the request was processed.
export function inspectBootstrapFixture(database: string, token: string, before: number, after: number, fixtureCheckedAt?: number) {
  const tokenCanonical = /^[A-Za-z0-9_-]{43}$/.test(token) && Buffer.from(token, 'base64url').toString('base64url') === token
  const unavailable = { tokenCanonical, databaseReadable: false, rowPresent: null, tokenMatches: null,
    epochMatches: null, fixtureBeforeCreated: null, requestBeforeFixtureCheck: null, requestBeforeCreated: null, responseBeforeCreated: null, responseAfterExpiry: null, accountCount: null }
  try {
    const db = new DatabaseSync(database, { readOnly: true })
    try {
      db.exec('BEGIN')
      const row = db.prepare('SELECT b.token_hash,b.created_at,b.expires_at,b.dataset_epoch,d.dataset_epoch AS actual_epoch FROM auth_bootstrap b LEFT JOIN sync_state d ON d.singleton=1 WHERE b.singleton=1').get() as {
        token_hash: string; created_at: string; expires_at: string; dataset_epoch: string; actual_epoch: string | null
      } | undefined
      const count = db.prepare('SELECT count(*) AS value FROM admin_accounts').get() as { value: number }
      // Date.parse and Date.now discard sub-millisecond ordering at equality.
      const created = row ? Date.parse(row.created_at) : NaN, expires = row ? Date.parse(row.expires_at) : NaN
      const result = { tokenCanonical, databaseReadable: true, rowPresent: !!row,
        tokenMatches: row ? row.token_hash === createHash('sha256').update(token).digest('hex') : null,
        epochMatches: row ? row.actual_epoch !== null && row.dataset_epoch === row.actual_epoch : null,
        fixtureBeforeCreated: row ? earlierMillisecond(fixtureCheckedAt, created) : null,
        requestBeforeFixtureCheck: earlierMillisecond(before, fixtureCheckedAt),
        requestBeforeCreated: row ? earlierMillisecond(before, created) : null,
        responseBeforeCreated: row ? earlierMillisecond(after, created) : null,
        responseAfterExpiry: row ? earlierMillisecond(expires, after) : null,
        accountCount: Number.isSafeInteger(count.value) && count.value >= 0 ? count.value : null }
      db.exec('COMMIT')
      return result
    } finally { db.close() }
  } catch { return unavailable }
}
