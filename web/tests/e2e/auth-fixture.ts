import { test as base } from '@playwright/test'
import { execFileSync, spawn } from 'node:child_process'
import { mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { cleanupPrivateApplication } from './private-application.ts'

type Application = { url: string; bootstrapToken: string }

// Private per-test stores avoid sharing a setup token/account/rate window across
// viewports or bypassing the production limiter with a test-only HTTP endpoint.
export const test = base.extend<{ application: Application; applicationDatabaseFixture: { prepare: (database: string) => void } | undefined }, { homeboxBinary: string; applicationWebDirectory: string }>({
  applicationDatabaseFixture: [undefined, { option: true }],
  applicationWebDirectory: [fileURLToPath(new URL('../../dist', import.meta.url)), { scope: 'worker' }],
  homeboxBinary: [async ({ browserName }, provideFixture) => {
    const directory = mkdtempSync(join(tmpdir(), `homebox-auth-${browserName}-binary-`))
    const binary = join(directory, 'homebox')
    try {
      execFileSync('go', ['-C', fileURLToPath(new URL('../../../server', import.meta.url)), 'build', '-mod=readonly', '-trimpath', '-o', binary, './cmd/homebox'], { env: { ...process.env, CGO_ENABLED: '0' }, stdio: 'pipe' })
      await provideFixture(binary)
    } finally { rmSync(directory, { recursive: true, force: true }) }
  }, { scope: 'worker' }],
  application: async ({ homeboxBinary, applicationWebDirectory, applicationDatabaseFixture }, provideFixture) => {
    const directory = mkdtempSync(join(tmpdir(), 'homebox-auth-browser-'))
    const database = join(directory, 'homebox.sqlite')
    const tokenFile = join(directory, 'bootstrap.token')
    let child: ReturnType<typeof spawn> | undefined
    let failure: { error: unknown } | undefined
    try {
      execFileSync(homeboxBinary, ['db', 'migrate', '--database', database], { stdio: 'pipe' })
      if (applicationDatabaseFixture) {
        // Offline legacy data setup only: no server is running, and the callback
        // must close its connection before the real CLI/server opens this file.
        applicationDatabaseFixture.prepare(database)
        execFileSync(homeboxBinary, ['db', 'check', '--database', database], { stdio: 'pipe' })
      }
      execFileSync(homeboxBinary, ['auth', 'bootstrap', '--database', database, '--output', tokenFile], { stdio: 'pipe' })
      child = spawn(homeboxBinary, ['serve', '--listen', '127.0.0.1:0', '--database', database, '--web-dir', applicationWebDirectory], { stdio: ['ignore', 'pipe', 'pipe'] })
      const server = child
      server.stderr?.resume()
      const ready = await new Promise<{ public_origin: string }>((resolve, reject) => {
        let output = ''
        const timeout = setTimeout(() => reject(new Error('Private auth server startup timed out')), 10_000)
        server.once('error', (error) => { clearTimeout(timeout); reject(error) })
        server.once('exit', () => { clearTimeout(timeout); reject(new Error('Private auth server exited before startup')) })
        server.stdout?.on('data', (data: Buffer) => {
          output += data.toString()
          if (output.includes('\n')) {
            clearTimeout(timeout)
            try { resolve(JSON.parse(output.split('\n')[0] ?? '')) } catch (error) { reject(error) }
          }
        })
      })
      await provideFixture({ url: ready.public_origin, bootstrapToken: readFileSync(tokenFile, 'utf8').trim() })
    } catch (error) {
      failure = { error }
      throw error
    } finally {
      await cleanupPrivateApplication(child, directory, failure)
    }
  },
  baseURL: async ({ application }, provideFixture) => { await provideFixture(application.url) },
})

export { expect } from '@playwright/test'
