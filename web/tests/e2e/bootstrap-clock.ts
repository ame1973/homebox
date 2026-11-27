const bootstrapLifetimeMs = 30 * 60_000
const maxClockWaitMs = 10_000

function bootstrapBounds(value: unknown) {
  const expires = typeof value === 'string' && /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z$/.test(value) ? Date.parse(value) : NaN
  if (!Number.isSafeInteger(expires)) throw new Error('Invalid private bootstrap metadata')
  return { issued: expires - bootstrapLifetimeMs, expires }
}

function readBootstrapClock(expires: number, now: () => number) {
  const current = now()
  if (!Number.isSafeInteger(current)) throw new Error('Private bootstrap clock is unavailable')
  if (current >= expires) throw new Error('Private bootstrap expired before setup')
  return current
}

// The CLI reports the expiry for its fixed 30-minute bootstrap lifetime. Wait
// only before a test request; never alter server time or retry a setup.
export async function waitForBootstrapWindow(
  value: unknown,
  now: () => number = Date.now,
  wait: (milliseconds: number) => Promise<void> = (milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds)),
): Promise<boolean> {
  const { issued, expires } = bootstrapBounds(value)
  const current = readBootstrapClock(expires, now)
  // Date.parse truncates sub-millisecond precision, so wait through that entire
  // millisecond before relying on the parsed instant.
  const delay = issued - current + 1
  if (delay <= 0) return false
  if (delay > maxClockWaitMs) throw new Error('Private bootstrap clock is too far before issuance')
  await wait(delay)
  if (readBootstrapClock(expires, now) <= issued) throw new Error('Private bootstrap clock remains before issuance')
  return true
}

// Recheck after the asynchronous wait and dispatch exactly once. A subsequent
// clock change can still occur while the request is in flight.
export async function runInBootstrapWindow<T>(
  value: unknown,
  request: (checkedAt: number) => Promise<T>,
  now: () => number = Date.now,
  wait?: (milliseconds: number) => Promise<void>,
): Promise<T> {
  const waited = await waitForBootstrapWindow(value, now, wait)
  const { issued, expires } = bootstrapBounds(value)
  const checkedAt = readBootstrapClock(expires, now)
  if (checkedAt <= issued) throw new Error('Private bootstrap clock changed before setup')
  if (waited) console.warn('Private bootstrap clock was before issuance; waited before the setup request')
  return request(checkedAt)
}
