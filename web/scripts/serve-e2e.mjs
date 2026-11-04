import assert from 'node:assert/strict'
import { execFileSync, spawn } from 'node:child_process'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

const port = Number(process.argv[2])
assert(Number.isInteger(port) && port > 1024 && port <= 65535, 'Supply an unprivileged TCP port')
const directory = mkdtempSync(join(tmpdir(), 'homebox-e2e-'))
const binary = join(directory, 'homebox')
const database = join(directory, 'data', 'homebox.sqlite')
const cleanup = () => rmSync(directory, { recursive: true, force: true })
try {
  execFileSync('go', ['-C', fileURLToPath(new URL('../../server', import.meta.url)), 'build', '-mod=readonly', '-trimpath', '-o', binary, './cmd/homebox'], { stdio: 'inherit', env: { ...process.env, CGO_ENABLED: '0' } })
  execFileSync(binary, ['db', 'migrate', '--database', database], { stdio: 'inherit' })
  const child = spawn(binary, ['serve', '--listen', `127.0.0.1:${port}`, '--database', database, '--web-dir', fileURLToPath(new URL('../dist', import.meta.url))], { stdio: 'inherit' })
  for (const signal of ['SIGTERM', 'SIGINT']) process.once(signal, () => child.kill('SIGTERM'))
  child.once('error', (error) => { console.error(error); cleanup(); process.exitCode = 1 })
  child.once('exit', (code) => { cleanup(); process.exitCode = code ?? 1 })
} catch (error) {
  cleanup()
  throw error
}
