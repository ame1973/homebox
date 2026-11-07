import createClient from 'openapi-fetch'
import type { $Write } from 'openapi-typescript-helpers'
import { z } from 'zod'
import { version as clientVersion } from '../../../package.json'
import type { components, paths } from './schema'

// helpers 0.1.0 misclassifies a literal-null property as readOnly because
// NonNullable<null> is never. Its public write marker preserves null in request
// types only; Writable unwraps it before accepting the unchanged wire command.
type PreserveNull<T> = T extends null ? $Write<null> : T extends object ? { [K in keyof T]: PreserveNull<T[K]> } : T
type RequestNulls<T> = { [K in keyof T]: K extends 'requestBody' ? PreserveNull<T[K]> : T[K] }
type ClientPaths = { [P in keyof paths]: { [M in keyof paths[P]]: RequestNulls<paths[P][M]> } }

export const apiClient = createClient<ClientPaths>({
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

// Match JSON Schema/Go code-point lengths without normalization or silent lone
// UTF-16 surrogate repair. The caller still owns semantic field validation.
export function validText(value: string, minimum: number, maximum: number): boolean {
  if (value.length > maximum * 2) return false
  let length = 0
  for (const character of value) {
    if (character.length === 1 && character.charCodeAt(0) >= 0xD800 && character.charCodeAt(0) <= 0xDFFF) return false
    length++
  }
  return length >= minimum && length <= maximum
}

const serverCodes = z.enum([
  'AUTHENTICATION_REQUIRED', 'CSRF_REJECTED', 'PASSWORD_CHANGE_REQUIRED', 'CREDENTIALS_REJECTED',
  'SETUP_CLOSED', 'IDEMPOTENCY_CONFLICT', 'VALIDATION_ERROR', 'SERVICE_UNAVAILABLE', 'ORIGIN_REJECTED',
  'RATE_LIMITED', 'CLIENT_UPGRADE_REQUIRED', 'INVALID_IDEMPOTENCY_KEY', 'INVALID_JSON', 'INVALID_BODY',
  'BODY_TOO_LARGE', 'UNSUPPORTED_MEDIA_TYPE', 'INVALID_AUTH_URL', 'METHOD_NOT_ALLOWED', 'NOT_FOUND',
  'INVALID_LOCATION_URL', 'INVALID_QUERY', 'INVALID_CURSOR', 'INVALID_REVISION', 'PRECONDITION_REQUIRED',
  'REVISION_CONFLICT', 'IDEMPOTENCY_EXPIRED', 'LOCATION_CODE_CONFLICT', 'LOCATION_CYCLE',
  'LOCATION_ARCHIVED', 'LOCATION_HAS_CHILDREN', 'LOCATION_IN_USE',
  'INVALID_TAXONOMY_URL', 'CATEGORY_CYCLE', 'CATEGORY_HAS_CHILDREN', 'TAG_NAME_CONFLICT',
  'INVALID_CATALOG_URL', 'ITEM_HAS_HISTORY', 'BARCODE_CONFLICT', 'ALIAS_CONFLICT',
  'INVALID_HOLDING_URL', 'TRACKING_MODE_CONFLICT', 'INSUFFICIENT_STOCK', 'QUANTITY_OVERFLOW',
  'UNIT_HAS_HISTORY', 'UNIT_DISPOSED', 'ASSET_LIFECYCLE_REQUIRED',
  'INVALID_PURCHASE_URL', 'PURCHASE_CURRENCY_LOCKED', 'PURCHASE_CURRENCY_CONFLICT', 'ASSET_COST_BASIS_CONFLICT', 'ASSET_TOTAL_OVERFLOW',
])
const errorSchema = z.object({ error: z.object({
  code: serverCodes, request_id: z.string().regex(/^[A-Za-z0-9_-]{1,128}$/).optional(),
}) })

type FailureCode = z.infer<typeof serverCodes> | 'UNKNOWN_ERROR' | 'INVALID_RESPONSE' | 'NETWORK_ERROR' | 'REQUEST_ABORTED' | 'REQUEST_TIMEOUT'

// Never retain arbitrary server messages/details, response bodies, or transport
// causes. Views localize allowlisted codes instead of rendering raw diagnostics.
export class ApiError extends Error {
  readonly code: FailureCode
  readonly status: number | undefined
  readonly retryAfterSeconds: number | undefined
  readonly requestId: string | undefined

  constructor(code: FailureCode, metadata: { status?: number; retryAfterSeconds?: number; requestId?: string } = {}) {
    super('HomeBox API request failed')
    this.name = 'ApiError'
    this.code = code
    this.status = metadata.status
    this.retryAfterSeconds = metadata.retryAfterSeconds
    this.requestId = metadata.requestId
  }
}

export type WriteContext = Readonly<{ csrfToken: string; idempotencyKey: string; signal: AbortSignal }>
export type RequestSender = (signal: AbortSignal) => Promise<{ data?: unknown; error?: unknown; response: Response }>

export function writeHeaders(context: WriteContext) {
  return { Origin: window.location.origin, 'X-CSRF-Token': context.csrfToken,
    'Idempotency-Key': context.idempotencyKey, 'X-HomeBox-Client-Version': clientVersion }
}

// One explicit request, never a token refresh/retry/queue. Optional invariants
// bind versioned domain responses to their requested resource and HTTP ETag.
export async function requestData<T>(signal: AbortSignal, send: RequestSender, schema: z.ZodType<T>, timeoutMs = 30_000, valid?: (data: T, response: Response) => boolean): Promise<T> {
  try {
    const { data, error, response } = await withRequestDeadline(signal, send, timeoutMs)
    if (signal.aborted) throw new ApiError('REQUEST_ABORTED')
    if (!response.ok) {
      const parsed = errorSchema.safeParse(error)
      const retry = response.headers.get('Retry-After')
      const seconds = retry !== null && /^\d{1,4}$/.test(retry) && Number(retry) <= 3600 ? Number(retry) : undefined
      throw new ApiError(parsed.success ? parsed.data.error.code : 'UNKNOWN_ERROR', {
        status: response.status, retryAfterSeconds: seconds,
        requestId: parsed.success ? parsed.data.error.request_id : undefined,
      })
    }
    const parsed = schema.safeParse(data)
    if (response.status !== 200 || !parsed.success || valid && !valid(parsed.data, response)) throw new ApiError('INVALID_RESPONSE', { status: response.status })
    return parsed.data
  } catch (error) {
    if (error instanceof ApiError) throw error
    if (error instanceof DOMException && error.name === 'AbortError') throw new ApiError('REQUEST_ABORTED')
    if (error instanceof DOMException && error.name === 'TimeoutError') throw new ApiError('REQUEST_TIMEOUT')
    if (error instanceof SyntaxError) throw new ApiError('INVALID_RESPONSE')
    throw new ApiError('NETWORK_ERROR')
  }
}
