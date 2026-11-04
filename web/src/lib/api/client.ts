import createClient from 'openapi-fetch'
import { z } from 'zod'
import type { components, paths } from './schema'

export const apiClient = createClient<paths>({
  baseUrl: new URL('/api/v1', window.location.origin).href,
  credentials: 'same-origin', cache: 'no-store', redirect: 'error',
})

const healthSchema = z.object({ status: z.enum(['ok', 'degraded']) })

export async function withRequestDeadline<T>(signal: AbortSignal, send: (signal: AbortSignal) => Promise<T>, timeoutMs = 8_000): Promise<T> {
  if (signal.aborted) throw new DOMException('Aborted', 'AbortError')
  const controller = new AbortController()
  const abort = () => controller.abort()
  signal.addEventListener('abort', abort, { once: true })
  let timedOut = false
  const timeout = setTimeout(() => { timedOut = true; abort() }, timeoutMs)
  try {
    const result = await send(controller.signal)
    if (controller.signal.aborted) throw new DOMException('Aborted', 'AbortError')
    return result
  } catch (error) {
    if (signal.aborted) throw new DOMException('Aborted', 'AbortError')
    if (timedOut) throw new DOMException('Request timed out', 'TimeoutError')
    throw error
  } finally {
    clearTimeout(timeout)
    signal.removeEventListener('abort', abort)
  }
}

export async function fetchHealth(signal: AbortSignal): Promise<components['schemas']['Health']> {
  const { data, response } = await withRequestDeadline(signal, (signal) => apiClient.GET('/health/live', { signal }))
  if (!response.ok) throw new Error(`Health request failed (${response.status})`)
  return healthSchema.parse(data)
}
