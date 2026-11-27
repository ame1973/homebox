import type { Page } from '@playwright/test'
import { expect, type Application } from './auth-fixture.ts'
import { runInBootstrapWindow } from './bootstrap-clock.ts'

// Domain browser tests use real setup/rotation and isolated serving stores. Never
// record provisioning/cookies in traces, videos or automatic failure images.
export async function provision(page: Page, application: Application): Promise<string> {
  const initial = 'location fixture initial password'
  const replacement = 'location fixture replacement password'
  const headers = { Origin: application.url }
  let before = 0
  const setup = await runInBootstrapWindow(application.bootstrapExpiresAt, (checkedAt) => {
    before = checkedAt
    return page.request.post('/api/v1/auth/setup', { headers, data: { bootstrap_token: application.bootstrapToken, username: 'owner', password: initial } })
  })
  const after = Date.now()
  if (setup.status() === 401) {
    // Read only the test-owned SQLite fixture from this separate Node process.
    // Never report credentials, database paths, raw hashes or server messages.
    try {
      const { inspectBootstrapFixture, safeBootstrapCode } = await import('./bootstrap-diagnostics.ts')
      const body: unknown = await setup.json().catch(() => undefined)
      const code = typeof body === 'object' && body !== null && 'error' in body && typeof body.error === 'object' && body.error !== null && 'code' in body.error ? body.error.code : undefined
      console.error('BOOTSTRAP_DIAGNOSTICS', JSON.stringify({ status: setup.status(), code: safeBootstrapCode(code),
        originMatches: new URL(setup.url()).origin === application.url, ...inspectBootstrapFixture(application.database, application.bootstrapToken, before, after, application.bootstrapFixtureCheckedAt) }))
    } catch { /* Preserve the original failed setup assertion. */ }
  }
  expect(setup.status()).toBe(200)
  expect((await page.request.post('/api/v1/auth/login', { headers, data: { username: 'owner', password: initial } })).status()).toBe(200)
  const csrf = await page.request.get('/api/v1/auth/csrf')
  expect(csrf.status()).toBe(200)
  expect((await page.request.post('/api/v1/auth/password', { headers: { ...headers, 'X-CSRF-Token': (await csrf.json()).token, 'Idempotency-Key': crypto.randomUUID(), 'X-HomeBox-Client-Version': '0.1.0-dev' }, data: { old_password: initial, new_password: replacement } })).status()).toBe(200)
  expect((await page.request.post('/api/v1/auth/login', { headers, data: { username: 'owner', password: replacement } })).status()).toBe(200)
  return replacement
}

export async function writeContext(page: Page, origin: string, revision?: number) {
  const csrf = await page.request.get('/api/v1/auth/csrf')
  expect(csrf.status()).toBe(200)
  return { Origin: origin, 'X-CSRF-Token': (await csrf.json()).token as string, 'Idempotency-Key': crypto.randomUUID(), 'X-HomeBox-Client-Version': '0.1.0-dev', ...(revision === undefined ? {} : { 'If-Match': JSON.stringify(String(revision)) }) }
}

export function watchDomainWrites(page: Page, prefixes: readonly string[]) {
  const requests: { method: string; path: string; key: string | undefined; proof: string | undefined; body: string | null }[] = []
  page.on('request', (request) => {
    const path = new URL(request.url()).pathname
    if (prefixes.some((prefix) => path.startsWith(prefix)) && ['POST', 'PUT', 'PATCH', 'DELETE'].includes(request.method())) requests.push({ method: request.method(), path, key: request.headers()['idempotency-key'], proof: request.headers()['if-match'], body: request.postData() })
  })
  return requests
}

export async function noOverflow(page: Page) {
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
}
