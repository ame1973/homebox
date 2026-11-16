import { z } from 'zod'
import { apiClient, writeHeaders } from './client'
import { input, resourceRequest, type RootWriteContext } from './domain'
import { mediaSchema } from './media'
import type { operations } from './schema'

export type MediaUploadResult = operations['post_media']['responses'][200]['content']['application/json']

const maxUploadBytes = 32 * 1024 * 1024
const maxSanitizedBytes = 64_000_000 * 8 + 16 * 1024 * 1024
const imageMIME = z.enum(['image/jpeg', 'image/png', 'image/webp'])
const uploadFileSchema = z.instanceof(Blob).refine((file) => file.size >= 1 && file.size <= maxUploadBytes && imageMIME.safeParse(file.type).success)
const uploadResultSchema: z.ZodType<MediaUploadResult> = mediaSchema.and(z.object({ verification: z.literal('verified'), mime_type: imageMIME }))
  .refine((media) => media.width <= 16_384 && media.height <= 16_384 && media.width * media.height <= 64_000_000 && media.bytes <= maxSanitizedBytes)

// The Blob remains caller-owned. This adapter sends once and never inspects,
// persists, retries or attaches its bytes; the server must validate and sanitize.
export async function uploadMedia(rawFile: Blob, context: RootWriteContext): Promise<MediaUploadResult> {
  const file = input(uploadFileSchema, rawFile)
  input(z.undefined(), context.expectedRevision)
  return resourceRequest(uploadResultSchema, context.signal, (signal) => apiClient.POST('/media', {
    signal, body: { file }, params: { header: writeHeaders(context) },
    bodySerializer(body) {
      const form = new FormData()
      form.append('file', body.file, 'media')
      return form
    },
  }), {})
}
