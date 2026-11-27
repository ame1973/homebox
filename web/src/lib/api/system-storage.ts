import { z } from 'zod'
import { apiClient, requestData } from './client'
import { purchaseInstantSchema } from './purchases'
import type { components } from './schema'

export type SystemStorage = components['schemas']['StorageStatus']

const fields = new Set(['database_bytes', 'media_bytes', 'free_bytes', 'backup_bytes', 'database_filesystem_total_bytes', 'measured_at'])
// Check raw keys before Zod can discard a special own __proto__ property.
const knownKeys = (value: unknown) => typeof value === 'object' && value !== null && !Array.isArray(value) && Object.keys(value).every((key) => fields.has(key))
const bytes = z.int().min(0).max(Number.MAX_SAFE_INTEGER)
const schema: z.ZodType<SystemStorage> = z.unknown().refine(knownKeys).pipe(z.strictObject({
  database_bytes: bytes,
  media_bytes: bytes.nullable(),
  free_bytes: bytes,
  backup_bytes: bytes.nullable(),
  database_filesystem_total_bytes: bytes,
  measured_at: purchaseInstantSchema,
})).refine((value) => value.free_bytes <= value.database_filesystem_total_bytes)

// The observation is neither a reservation nor a complete storage inventory.
export async function fetchSystemStorage(signal: AbortSignal): Promise<SystemStorage> {
  return requestData(signal, (signal) => apiClient.GET('/system/storage', { signal }), schema, 8_000, (_value, response) =>
    !response.headers.has('ETag') && (response.headers.get('Cache-Control') ?? '').split(',').some((part) => part.trim().toLowerCase() === 'no-store'))
}
