import { z } from 'zod'
import { apiClient, requestData } from './client'
import type { components } from './schema'

export type MediaRecoveryStatus = components['schemas']['MediaRecoveryStatus']

const statusSchema: z.ZodType<MediaRecoveryStatus> = z.strictObject({
  phase: z.enum(['unavailable', 'queued', 'running', 'idle', 'blocked', 'stopped']),
  completed: z.int().min(0).max(Number.MAX_SAFE_INTEGER),
  saturated: z.boolean(),
  failure: z.enum(['', 'staging-unproven', 'staging-limit', 'object-unproven', 'storage-unavailable', 'inconsistent-state', 'recovery-failed']),
}).refine((status) => {
  if (status.saturated && status.completed !== Number.MAX_SAFE_INTEGER) return false
  if (status.phase === 'unavailable') return status.failure === 'storage-unavailable' && status.completed === 0 && !status.saturated
  if (status.phase === 'blocked') return status.failure !== ''
  if (status.phase === 'stopped') return true
  return status.failure === ''
})

// One live, non-versioned snapshot; it is neither a file proof nor permission to retry.
export async function fetchMediaRecoveryStatus(signal: AbortSignal): Promise<MediaRecoveryStatus> {
  return requestData(signal, (signal) => apiClient.GET('/system/media-recovery', { signal }), statusSchema, 8_000,
    (_status, response) => !response.headers.has('ETag'))
}
