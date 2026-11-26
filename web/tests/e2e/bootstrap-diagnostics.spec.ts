import { createHash } from 'node:crypto'
import { test, expect } from './auth-fixture.ts'
import { provision } from './domain-helpers.ts'

test.use({ trace: 'off', screenshot: 'off', video: 'off' })

test('reports only safe fixture relationships for a real rejected bootstrap', async ({ page, application }) => {
  const wrong = Buffer.alloc(32, 7).toString('base64url')
  expect(wrong).not.toBe(application.bootstrapToken)
  const messages: unknown[][] = [], original = console.error
  console.error = (...values: unknown[]) => { messages.push(values) }
  try {
    await expect(provision(page, { ...application, bootstrapToken: wrong })).rejects.toThrow()
  } finally { console.error = original }
  const diagnostics = messages.filter((values) => values[0] === 'BOOTSTRAP_DIAGNOSTICS')
  expect(diagnostics).toHaveLength(1)
  const other = messages.filter((values) => values[0] !== 'BOOTSTRAP_DIAGNOSTICS')
  expect(other.length).toBeLessThanOrEqual(1)
  for (const warning of other) {
    expect(warning).toHaveLength(1)
    expect(warning[0]).toMatch(/^\(node:\d+\) ExperimentalWarning: SQLite is an experimental feature and might change at any time\n\(Use `node --trace-warnings \.\.\.` to show where the warning was created\)$/)
  }
  const output = String(diagnostics[0]?.[1]), info = JSON.parse(output)
  expect(info).toMatchObject({ status: 401, code: 'AUTHENTICATION_REQUIRED', originMatches: true,
    tokenCanonical: true, databaseReadable: true, rowPresent: true, tokenMatches: false, epochMatches: true, accountCount: 0 })
  for (const secret of [wrong, application.bootstrapToken, application.database, createHash('sha256').update(wrong).digest('hex'), 'location fixture initial password']) {
    expect(JSON.stringify(messages)).not.toContain(secret)
  }
  // This is a separate explicit test action, not an automatic setup retry.
  await expect(provision(page, application)).resolves.toBe('location fixture replacement password')
})
