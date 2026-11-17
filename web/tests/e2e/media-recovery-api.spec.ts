import { cpSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { build } from 'vite'
import { test as applicationTest, expect } from './auth-fixture.ts'
import { provision, writeContext } from './domain-helpers.ts'

const test = applicationTest.extend<{}, { applicationWebDirectory: string }>({
  applicationWebDirectory: [async ({ browserName }, provideFixture) => {
    const directory = mkdtempSync(join(tmpdir(), `homebox-recovery-sdk-${browserName}-`))
    const web = join(directory, 'web'), sdk = join(directory, 'sdk'), entry = join(directory, 'entry.ts')
    try {
      cpSync(fileURLToPath(new URL('../../dist', import.meta.url)), web, { recursive: true })
      // This SDK bundle is private to the browser fixture, not a production entrypoint.
      writeFileSync(entry, `export * from ${JSON.stringify(fileURLToPath(new URL('../../src/lib/api/media-recovery.ts', import.meta.url)))};\n`)
      await build({ configFile: false, logLevel: 'silent', build: { outDir: sdk, minify: false, lib: { entry, formats: ['es'], fileName: 'recovery-sdk' } } })
      cpSync(join(sdk, 'recovery-sdk.js'), join(web, 'assets', 'recovery-sdk.js'))
      await provideFixture(web)
    } finally { rmSync(directory, { recursive: true, force: true }) }
  }, { scope: 'worker' }],
})

test.use({ trace: 'off', screenshot: 'off', video: 'off' })

test('reads a protected recovery snapshot through the real server without replay', async ({ page, context, application }) => {
  const requests: string[] = [], replies: { status: number; cache: string | undefined; etag: string | undefined }[] = []
  page.on('request', (request) => {
    if (new URL(request.url()).pathname === '/api/v1/system/media-recovery') requests.push(request.method())
  })
  page.on('response', (response) => {
    if (new URL(response.url()).pathname === '/api/v1/system/media-recovery') {
      const headers = response.headers()
      replies.push({ status: response.status(), cache: headers['cache-control'], etag: headers.etag })
    }
  })
  await page.goto('/')
  const read = () => page.evaluate(async () => {
    const modulePath = '/assets/recovery-sdk.js', api = await import(modulePath)
    try { return await api.fetchMediaRecoveryStatus(new AbortController().signal) }
    catch (error) { return { code: error instanceof Error && 'code' in error ? error.code : 'unexpected' } }
  })
  expect(await read()).toEqual({ code: 'AUTHENTICATION_REQUIRED' })
  await provision(page, application)
  expect(await read()).toEqual({ phase: 'unavailable', completed: 0, saturated: false, failure: 'storage-unavailable' })
  await context.setOffline(true)
  expect(await read()).toEqual({ code: 'NETWORK_ERROR' })
  await context.setOffline(false)
  const headers = await writeContext(page, application.url)
  expect((await page.request.post('/api/v1/auth/logout', { headers })).status()).toBe(200)
  expect(await read()).toEqual({ code: 'AUTHENTICATION_REQUIRED' })
  expect(requests).toEqual(['GET', 'GET', 'GET', 'GET'])
  expect(replies).toEqual([
    { status: 401, cache: 'no-store', etag: undefined },
    { status: 200, cache: 'no-store', etag: undefined },
    { status: 401, cache: 'no-store', etag: undefined },
  ])
})
