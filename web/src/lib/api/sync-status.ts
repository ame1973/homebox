import { z } from 'zod'
import { apiClient, requestData } from './client'
import { text } from './domain'
import { purchaseInstantSchema } from './purchases'
import type { components } from './schema'

export type SyncStatus = components['schemas']['SyncStatus']

export const syncProjectionKinds = ['location', 'item', 'unit', 'stock', 'barcode', 'media', 'media_link', 'category', 'tag', 'item_tag', 'search_doc',
  'purchase', 'purchase_line', 'asset', 'usage', 'cost', 'disposal', 'alias', 'attribute', 'location_event', 'inventory_event', 'ai_review'] as const
const kinds = z.enum(syncProjectionKinds)
const nonnegative = z.int().min(0).max(Number.MAX_SAFE_INTEGER)
const positive = z.int().min(1).max(Number.MAX_SAFE_INTEGER)
const statusFields = new Set(['dataset_epoch', 'server_instance_id', 'projection_version', 'min_retained_seq', 'high_watermark', 'counts',
  'counts_scope', 'last_change_at', 'retention_days', 'bootstrap_ready', 'retention_enabled', 'models'])
const kindNames = new Set<string>(kinds.options)
// Zod drops an own __proto__ property while parsing objects. Check raw keys first
// so a non-contract reply cannot be silently repaired into a valid diagnostic.
const onlyKnownKeys = (value: unknown, allowed: ReadonlySet<string>) =>
  typeof value === 'object' && value !== null && !Array.isArray(value) && Object.keys(value).every((key) => allowed.has(key))
const countsSchema = z.unknown().refine((value) => onlyKnownKeys(value, kindNames)).pipe(z.partialRecord(kinds, positive))
const statusSchema: z.ZodType<SyncStatus> = z.unknown().refine((value) => onlyKnownKeys(value, statusFields)).pipe(z.strictObject({
  dataset_epoch: text(1, 128),
  server_instance_id: z.string().regex(/^[0-9a-f]{32}(?![\s\S])/),
  projection_version: z.literal(1),
  min_retained_seq: nonnegative,
  high_watermark: nonnegative,
  counts: countsSchema,
  counts_scope: z.literal('materialized_projections'),
  last_change_at: purchaseInstantSchema.nullable(),
  retention_days: positive,
  bootstrap_ready: z.boolean(),
  retention_enabled: z.boolean(),
  models: z.array(text(1, 256)).max(500).optional(),
})).refine((status) => status.min_retained_seq <= status.high_watermark &&
  (status.last_change_at === null || status.min_retained_seq < status.high_watermark))

// A diagnostic snapshot is not an offline replica or permission to bootstrap.
export async function fetchSyncStatus(signal: AbortSignal): Promise<SyncStatus> {
  return requestData(signal, (signal) => apiClient.GET('/sync/status', { signal }), statusSchema, 8_000,
    (_status, response) => !response.headers.has('ETag'))
}
