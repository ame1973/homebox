import assert from 'node:assert/strict'
import { spawn, type ChildProcess } from 'node:child_process'
import { once } from 'node:events'
import { existsSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test, type TestContext } from 'node:test'
import { cleanupPrivateApplication } from './e2e/private-application.ts'

function directory(t: TestContext) {
  const path = mkdtempSync(join(tmpdir(), 'homebox-cleanup-test-'))
  t.after(() => rmSync(path, { recursive: true, force: true }))
  mkdirSync(join(path, 'nested'))
  // File sentinels exercise deletion, not real credentials or database acceptance.
  for (const name of ['bootstrap.token', 'homebox.sqlite', 'homebox.sqlite-wal', 'homebox.sqlite-shm', 'nested/private-file']) writeFileSync(join(path, name), 'private fixture sentinel', { mode: 0o600 })
  return path
}
function track(t: TestContext, child: ChildProcess) {
  t.after(async () => {
    if (child.pid && child.exitCode === null && child.signalCode === null) {
      const exited = once(child, 'exit')
      child.kill('SIGKILL')
      await exited
    }
  })
  return child
}
async function running(t: TestContext, onTerm: string) {
  const child = track(t, spawn(process.execPath, ['-e', `process.on('SIGTERM', () => { ${onTerm} }); process.send('ready'); setInterval(() => {}, 1000)`], { stdio: ['ignore', 'ignore', 'ignore', 'ipc'] }))
  assert.equal((await once(child, 'message'))[0], 'ready')
  return child
}
async function alreadyExited(t: TestContext, code: number) {
  const child = track(t, spawn(process.execPath, ['-e', `process.exit(${code})`], { stdio: 'ignore' }))
  await once(child, 'exit')
  return child
}

test('removes all private files when startup failed before spawning', async (t) => {
  const path = directory(t)
  await cleanupPrivateApplication(undefined, path)
  assert.equal(existsSync(path), false)
})
test('waits for actual graceful process exit before successful private cleanup', async (t) => {
  const path = directory(t), child = await running(t, 'process.exit(0)')
  await cleanupPrivateApplication(child, path)
  assert.equal(child.exitCode, 0)
  assert.equal(existsSync(path), false)
  assert.equal(child.listenerCount('exit'), 0)
  assert.equal(child.listenerCount('error'), 0)
})
test('removes files and rejects a running server that exits unsuccessfully', async (t) => {
  const path = directory(t), child = await running(t, 'process.exit(7)')
  await assert.rejects(cleanupPrivateApplication(child, path), { message: 'Private auth server shutdown failed' })
  assert.equal(child.exitCode, 7)
  assert.equal(existsSync(path), false)
})
test('cleans an already successful process without sending another signal', async (t) => {
  const path = directory(t), child = await alreadyExited(t, 0)
  await cleanupPrivateApplication(child, path)
  assert.equal(child.killed, false)
  assert.equal(existsSync(path), false)
})
test('does not silently accept a server that had already failed', async (t) => {
  const path = directory(t), child = await alreadyExited(t, 3)
  await assert.rejects(cleanupPrivateApplication(child, path), { message: 'Private auth server shutdown failed' })
  assert.equal(child.killed, false)
  assert.equal(existsSync(path), false)
})
test('recognizes an already signal-terminated server instead of waiting for a second exit', async (t) => {
  const path = directory(t), child = await running(t, '')
  const exited = once(child, 'exit'); child.kill('SIGKILL'); await exited
  assert.equal(child.exitCode, null)
  assert.equal(child.signalCode, 'SIGKILL')
  await assert.rejects(cleanupPrivateApplication(child, path), { message: 'Private auth server shutdown failed' })
  assert.equal(existsSync(path), false)
})
test('retains a spawn failure while removing the private startup directory', async (t) => {
  const path = directory(t), child = track(t, spawn(join(path, 'missing-executable'), [], { stdio: 'ignore' }))
  const [original] = await once(child, 'error')
  assert.equal(child.pid, undefined)
  await assert.rejects(async () => {
    try { throw original } finally { await cleanupPrivateApplication(child, path, { error: original }) }
  }, (error) => error === original)
  assert.equal(existsSync(path), false)
})
test('retains both original startup and cleanup failures without masking either', async (t) => {
  const path = directory(t), child = await running(t, 'process.exit(9)'), original = new Error('Original startup failure')
  await assert.rejects(async () => {
    try { throw original } finally { await cleanupPrivateApplication(child, path, { error: original }) }
  }, (error) => error instanceof AggregateError && error.errors[0] === original && error.errors[1].message === 'Private auth server shutdown failed')
  assert.equal(existsSync(path), false)
})
test('reports a filesystem cleanup failure together with process and original failures', async (t) => {
  const path = directory(t), child = await alreadyExited(t, 6), original = { error: 'Original non-Error failure' }
  await assert.rejects(cleanupPrivateApplication(child, `${path}\0invalid`, original), (error) => error instanceof AggregateError && error.errors.length === 3 && error.errors[0] === original.error && error.errors[1].message === 'Private auth server shutdown failed' && error.errors[2].code === 'ERR_INVALID_ARG_VALUE')
  assert.equal(existsSync(path), true)
})
test('surfaces a lone filesystem failure without wrapping or hiding it', async (t) => {
  const path = directory(t)
  await assert.rejects(cleanupPrivateApplication(undefined, `${path}\0invalid`), { code: 'ERR_INVALID_ARG_VALUE' })
})
test('removes its lifecycle listeners and retains a process error as a failure cause', async (t) => {
  const path = directory(t), child = await running(t, 'setTimeout(() => process.exit(0), 100)'), cause = new Error('Fixture signal error')
  const cleanup = cleanupPrivateApplication(child, path)
  child.emit('error', cause)
  await assert.rejects(cleanup, (error) => error instanceof Error && error.message === 'Private auth server shutdown failed' && error.cause === cause)
  assert.equal(existsSync(path), false)
  assert.equal(child.listenerCount('exit'), 0)
  assert.equal(child.listenerCount('error'), 0)
})
test('keeps the ten-second deadline, force-stops an unresponsive server, waits for exit and still removes all private files', { timeout: 15_000 }, async (t) => {
  const path = directory(t), child = await running(t, '')
  await assert.rejects(cleanupPrivateApplication(child, path), { message: 'Private auth server shutdown timed out' })
  assert.equal(child.signalCode, 'SIGKILL')
  assert.equal(child.exitCode, null)
  assert.equal(existsSync(path), false)
  assert.equal(child.listenerCount('exit'), 0)
  assert.equal(child.listenerCount('error'), 0)
})
