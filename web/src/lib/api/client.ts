import createClient from 'openapi-fetch'
import { z } from 'zod'
import type { components, paths } from './schema'

const client = createClient<paths>({
  baseUrl: new URL('/api/v1', window.location.origin).href,
  credentials: 'same-origin', cache: 'no-store', redirect: 'error',
})

const healthSchema = z.object({ status: z.enum(['ok', 'degraded']) })

export async function fetchHealth(signal: AbortSignal): Promise<components['schemas']['Health']> {
  const controller = new AbortController()
  const abort = () => controller.abort()
  if (signal.aborted) abort()
  else signal.addEventListener('abort', abort, { once: true })
  const timeout = setTimeout(abort, 8_000)
  try {
    const { data, response } = await client.GET('/health/live', { signal: controller.signal })
    if (!response.ok) throw new Error(`Health request failed (${response.status})`)
    return healthSchema.parse(data)
  } finally {
    clearTimeout(timeout)
    signal.removeEventListener('abort', abort)
  }
}
