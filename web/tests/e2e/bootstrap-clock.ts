const bootstrapLifetimeMs = 30 * 60_000
const maxClockWaitMs = 10_000

// The CLI reports the expiry for its fixed 30-minute bootstrap lifetime. Wait
// only before the first test request; never alter server time or retry a setup.
export async function waitForBootstrapWindow(
  value: unknown,
  now: () => number = Date.now,
  wait: (milliseconds: number) => Promise<void> = (milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds)),
): Promise<boolean> {
  const expires = typeof value === 'string' && /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z$/.test(value) ? Date.parse(value) : NaN
  if (!Number.isSafeInteger(expires)) throw new Error('Invalid private bootstrap metadata')
  const issued = expires - bootstrapLifetimeMs
  const readClock = () => {
    const current = now()
    if (!Number.isSafeInteger(current)) throw new Error('Private bootstrap clock is unavailable')
    if (current >= expires) throw new Error('Private bootstrap expired before setup')
    return current
  }
  const current = readClock()
  // Date.parse truncates sub-millisecond precision, so wait through that entire
  // millisecond before relying on the parsed instant.
  const delay = issued - current + 1
  if (delay <= 0) return false
  if (delay > maxClockWaitMs) throw new Error('Private bootstrap clock is too far before issuance')
  await wait(delay)
  if (readClock() <= issued) throw new Error('Private bootstrap clock remains before issuance')
  return true
}
