import { createQuerySerializer } from 'openapi-fetch'
import { z } from 'zod'
import { symbologySchema } from './catalog-links'
import { apiClient, requestData, validText } from './client'
import { cursorSchema, idSchema, input, pageQuerySchema, text } from './domain'
import { validLivePage } from './purchases'
import type { components, operations } from './schema'

export type BarcodeMatch = components['schemas']['BarcodeMatch']
export type BarcodeMatches = components['schemas']['BarcodeMatches']
export type BarcodeSearchQuery = operations['get_search_barcodes']['parameters']['query']

const serializeQuery = createQuerySerializer<BarcodeSearchQuery>()
const querySchema: z.ZodType<BarcodeSearchQuery> = pageQuerySchema.extend({ value: text(1, 512), symbology: symbologySchema.optional() })
  .refine((query) => Object.values(query).every((value) => typeof value !== 'string' || validText(value, 0, value.length)) &&
    serializeQuery(query).length <= 16_384)
const matchSchema: z.ZodType<BarcodeMatch> = z.strictObject({
  item_id: idSchema, barcode_id: idSchema, symbology: symbologySchema, normalized_value: text(1, 512),
})
const matchesSchema: z.ZodType<BarcodeMatches> = z.strictObject({ matches: z.array(matchSchema).max(100), next_cursor: cursorSchema().nullable() })
  .refine((page) => new Set(page.matches.map((row) => row.barcode_id)).size === page.matches.length)

// A product barcode may match many Items. One live page is not a Unit proof.
export async function fetchBarcodeMatches(query: BarcodeSearchQuery, signal: AbortSignal): Promise<BarcodeMatches> {
  const values = input(querySchema, query)
  return requestData(signal, (signal) => apiClient.GET('/search/barcodes', { signal, params: { query: values } }), matchesSchema, 8_000,
    (page, response) => validLivePage({ data: page.matches, next_cursor: page.next_cursor }, response, values) && page.matches.every((row) =>
      row.normalized_value === values.value && (values.symbology === undefined || row.symbology === values.symbology)))
}
