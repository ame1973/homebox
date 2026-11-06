import type { ChildProcess } from 'node:child_process'
import { rmSync } from 'node:fs'

type PriorFailure = Readonly<{ error: unknown }>
async function stopPrivateServer(child: ChildProcess | undefined): Promise<void> {
  if (!child?.pid) return
  if (child.exitCode !== null || child.signalCode !== null) {
    if (child.exitCode !== 0) throw new Error('Private auth server shutdown failed')
    return
  }
  const server = child
  await new Promise<void>((resolve, reject) => {
    let expired = false, settled = false
    const deadline = setTimeout(() => { expired = true; server.kill('SIGKILL') }, 10_000)
    function finish(error?: Error) {
      if (settled) return
      settled = true
      clearTimeout(deadline)
      server.removeListener('exit', onExit)
      server.removeListener('error', onError)
      if (error) reject(error); else resolve()
    }
    function onExit(code: number | null) {
      finish(expired ? new Error('Private auth server shutdown timed out') : code === 0 ? undefined : new Error('Private auth server shutdown failed'))
    }
    function onError(cause: Error) {
      finish(new Error(expired ? 'Private auth server shutdown timed out' : 'Private auth server shutdown failed', { cause }))
    }
    server.once('exit', onExit)
    server.once('error', onError)
    server.kill('SIGTERM')
    // A delivered signal is not process termination. Even after the deadline,
    // wait for actual exit/error before removing the private database directory.
  })
}

export async function cleanupPrivateApplication(child: ChildProcess | undefined, directory: string, prior?: PriorFailure): Promise<void> {
  const errors: unknown[] = []
  try { await stopPrivateServer(child) } catch (error) { errors.push(error) }
  try { rmSync(directory, { recursive: true, force: true }) } catch (error) { errors.push(error) }
  if (!errors.length) return
  if (prior) errors.unshift(prior.error)
  throw errors.length === 1 ? errors[0] : new AggregateError(errors, 'Private application cleanup failed')
}
