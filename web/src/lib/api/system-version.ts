import { z } from 'zod'
import { apiClient, requestData } from './client'
import { text } from './domain'
import type { components } from './schema'

export type SystemVersion = components['schemas']['Version']

const fields = new Set(['api_version', 'projection_version', 'offline_projection_version', 'dataset_epoch', 'server_instance_id',
  'web_build_id', 'web_version', 'schema_version', 'min_supported_client', 'min_web_build_id', 'model_index_versions', 'supported_write_client_versions'])
// Check raw keys before Zod can discard a special own __proto__ property.
const knownKeys = (value: unknown) => typeof value === 'object' && value !== null && !Array.isArray(value) && Object.keys(value).every((key) => fields.has(key))
const schema: z.ZodType<SystemVersion> = z.unknown().refine(knownKeys).pipe(z.strictObject({
  api_version: text(1, 32),
  projection_version: z.literal(1),
  offline_projection_version: z.literal(1),
  dataset_epoch: text(1, 128),
  server_instance_id: z.string().regex(/^[0-9a-f]{32}(?![\s\S])/),
  web_build_id: z.string().regex(/^[0-9a-f]{24}(?![\s\S])/),
  web_version: text(1, 32),
  schema_version: z.int().min(1).max(Number.MAX_SAFE_INTEGER),
  min_supported_client: text(1, 32).nullable(),
  min_web_build_id: text(1, 128).nullable(),
  model_index_versions: z.array(text(1, 256)).max(500).nullable(),
  supported_write_client_versions: z.array(text(1, 32)).min(1).max(16),
})).refine((value) => new Set(value.supported_write_client_versions).size === value.supported_write_client_versions.length)

// This observation neither authorizes a write nor proves a model inventory,
// a complete deployment or an offline replica. The server still validates writes.
export async function fetchSystemVersion(signal: AbortSignal): Promise<SystemVersion> {
  return requestData(signal, (signal) => apiClient.GET('/system/version', { signal }), schema, 8_000, (_value, response) =>
    !response.headers.has('ETag') && (response.headers.get('Cache-Control') ?? '').split(',').some((part) => part.trim().toLowerCase() === 'no-store'))
}
