import { z } from 'zod'
import { apiClient, requestData } from './client'
import type { components } from './schema'

export type ConnectivityStatus = components['schemas']['ConnectivityStatus']

// Zod can discard an own __proto__ key, so inspect the original response first.
const statusSchema: z.ZodType<ConnectivityStatus> = z.unknown().refine((value) =>
  typeof value === 'object' && value !== null && !Array.isArray(value) &&
  Object.keys(value).length === 1 && Object.keys(value)[0] === 'status',
).pipe(z.strictObject({ status: z.enum(['ok', 'degraded']) }))

// One passive reachability read, not a guarantee that any later operation can succeed.
export async function probeConnectivity(signal: AbortSignal): Promise<ConnectivityStatus> {
  return requestData(signal, (signal) => apiClient.GET('/system/connectivity', { signal }), statusSchema, 8_000,
    (_status, response) => !response.headers.has('ETag'))
}
