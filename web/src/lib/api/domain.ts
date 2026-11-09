import { z } from 'zod'
import { ApiError, requestData, validText, writeHeaders, type RequestSender, type WriteContext } from './client'

export type RevisionWriteContext = WriteContext & Readonly<{ expectedRevision: number }>
export type RootWriteContext = WriteContext & { readonly expectedRevision?: never }
export type CreateContext = WriteContext & { readonly expectedRevision?: number }

export const text = (minimum: number, maximum: number) => z.string().refine((value) => validText(value, minimum, maximum))
export const idSchema = text(1, 128)
export const revisionSchema = z.int().min(1)
export const pathIDSchema = idSchema.refine((id) => !['.', '..'].includes(id) && !['/', '\\', '\0'].some((character) => id.includes(character)))
// Server cursor limits count decoded UTF-8 bytes, not JavaScript characters.
// Check Unicode first so TextEncoder cannot silently repair a lone surrogate.
export const validCursor = (value: string, maximum = 2048) => validText(value, 1, maximum) && new TextEncoder().encode(value).byteLength <= maximum
export const cursorSchema = (maximum = 2048) => z.string().refine((value) => validCursor(value, maximum))
export const pageQuerySchema = z.strictObject({ cursor: cursorSchema().optional(), limit: z.int().min(1).max(100).optional() })
export const deleteAckSchema = z.strictObject({ ok: z.literal(true), operation_id: idSchema, revision: revisionSchema })

// Parse original canonical decimal text; never trim, round or coerce a display
// amount, grouped value, exponent, trailing newline or unsafe integer.
export function parseSafeCount(value: string, minimum: 0 | 1 = 0): number | undefined {
  if (typeof value !== 'string' || !/^(?:0|[1-9][0-9]{0,15})(?![\s\S])/.test(value)) return undefined
  const number = Number(value)
  return Number.isSafeInteger(number) && number >= minimum ? number : undefined
}

export function input<T>(schema: z.ZodType<T>, value: unknown): T {
  const result = schema.safeParse(value)
  if (!result.success) throw new ApiError('VALIDATION_ERROR')
  return result.data
}

export function revisionHeaders(context: CreateContext) {
  const revision = input(revisionSchema, context.expectedRevision)
  return { ...writeHeaders(context), 'If-Match': `"${revision}"` }
}

export function createHeaders(parentID: string | null | undefined, context: CreateContext) {
  if (parentID != null) return revisionHeaders(context)
  if (context.expectedRevision !== undefined) throw new ApiError('VALIDATION_ERROR')
  return writeHeaders(context)
}

// Snapshot the requested identity/proof before sending. An acknowledgement can
// be an old frozen reply; validation never rebases it or installs current state.
export function resourceRequest<T extends { id: string; revision: number }>(schema: z.ZodType<T>, signal: AbortSignal, send: RequestSender,
  { id, expectedRevision, creating = false }: Readonly<{ id?: string; expectedRevision?: number; creating?: boolean }>, timeoutMs = 30_000,
): Promise<T> {
  return requestData(signal, send, schema, timeoutMs, (resource, response) =>
    (id === undefined || resource.id === id) && response.headers.get('ETag') === `"${resource.revision}"` &&
    (creating ? resource.revision === 1 : expectedRevision === undefined || resource.revision === expectedRevision || resource.revision === expectedRevision + 1))
}
