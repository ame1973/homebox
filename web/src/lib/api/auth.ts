import { z } from 'zod'
import { version as clientVersion } from '../../../package.json'
import { apiClient, withRequestDeadline } from './client'
import type { components } from './schema'

type Schemas = components['schemas']
export type Admin = Schemas['Admin']

// Match JSON Schema/Go code-point lengths without normalizing credentials or
// silently accepting lone UTF-16 surrogates that JSON would escape.
export function validAuthText(value: string, minimum: number, maximum: number): boolean {
  if (value.length > maximum * 2) return false
  let length = 0
  for (const character of value) {
    if (character.length === 1 && character.charCodeAt(0) >= 0xD800 && character.charCodeAt(0) <= 0xDFFF) return false
    length++
  }
  return length >= minimum && length <= maximum
}

const text = (minimum: number, maximum: number) => z.string().refine((value) => validAuthText(value, minimum, maximum))
const revision = z.int().min(1)
const token = z.string().regex(/^[A-Za-z0-9_-]{43}$/)
const adminSchema: z.ZodType<Admin> = z.strictObject({
  id: text(1, 128), revision, username: text(1, 64), must_change_password: z.boolean(),
})
const ackSchema: z.ZodType<Schemas['ActionAck']> = z.strictObject({ ok: z.literal(true), operation_id: text(1, 128), revision: revision.optional() })
const csrfSchema = z.strictObject({ token })
const reauthSchema: z.ZodType<Schemas['ReauthResult']> = z.strictObject({
  admin: adminSchema, reauth_token: token, expires_at: z.iso.datetime({ offset: true }),
})
const serverCodes = z.enum([
  'AUTHENTICATION_REQUIRED', 'CSRF_REJECTED', 'PASSWORD_CHANGE_REQUIRED', 'CREDENTIALS_REJECTED',
  'SETUP_CLOSED', 'IDEMPOTENCY_CONFLICT', 'VALIDATION_ERROR', 'SERVICE_UNAVAILABLE', 'ORIGIN_REJECTED',
  'RATE_LIMITED', 'CLIENT_UPGRADE_REQUIRED', 'INVALID_IDEMPOTENCY_KEY', 'INVALID_JSON', 'INVALID_BODY',
  'BODY_TOO_LARGE', 'UNSUPPORTED_MEDIA_TYPE', 'INVALID_AUTH_URL', 'METHOD_NOT_ALLOWED', 'NOT_FOUND',
])
const errorSchema = z.object({ error: z.object({
  code: serverCodes, request_id: z.string().regex(/^[A-Za-z0-9_-]{1,128}$/).optional(),
}) })

type FailureCode = z.infer<typeof serverCodes> | 'UNKNOWN_ERROR' | 'INVALID_RESPONSE' | 'NETWORK_ERROR' | 'REQUEST_ABORTED' | 'REQUEST_TIMEOUT'

// Do not retain arbitrary server messages/details, response bodies, or fetch causes.
// Views map these safe codes to localized guidance, never render raw diagnostics.
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

export type AuthWriteContext = Readonly<{ csrfToken: string; idempotencyKey: string; signal: AbortSignal }>

function writeHeaders(context: AuthWriteContext) {
  return { Origin: window.location.origin, 'X-CSRF-Token': context.csrfToken,
    'Idempotency-Key': context.idempotencyKey, 'X-HomeBox-Client-Version': clientVersion }
}

async function authRequest<T>(signal: AbortSignal, send: (signal: AbortSignal) => Promise<{ data?: unknown; error?: unknown; response: Response }>, schema: z.ZodType<T>, timeoutMs = 30_000): Promise<T> {
  try {
    const { data, error, response } = await withRequestDeadline(signal, send, timeoutMs)
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
    if (response.status !== 200 || !parsed.success) throw new ApiError('INVALID_RESPONSE', { status: response.status })
    return parsed.data
  } catch (error) {
    if (error instanceof ApiError) throw error
    if (error instanceof DOMException && error.name === 'AbortError') throw new ApiError('REQUEST_ABORTED')
    if (error instanceof DOMException && error.name === 'TimeoutError') throw new ApiError('REQUEST_TIMEOUT')
    if (error instanceof SyntaxError) throw new ApiError('INVALID_RESPONSE')
    throw new ApiError('NETWORK_ERROR')
  }
}

export function fetchAdmin(signal: AbortSignal): Promise<Admin> {
  return authRequest(signal, (signal) => apiClient.GET('/auth/me', { signal }), adminSchema, 8_000)
}

// CSRF and grants are returned to the caller, never cached or persisted here.
// Each write makes one request only: no hidden refresh, retry, queue, or replay.
export async function fetchCSRF(signal: AbortSignal): Promise<string> {
  const result = await authRequest(signal, (signal) => apiClient.GET('/auth/csrf', { signal }), csrfSchema, 8_000)
  return result.token
}

export function setupAdmin(body: Schemas['AuthSetupInput'], signal: AbortSignal): Promise<Admin> {
  return authRequest(signal, (signal) => apiClient.POST('/auth/setup', { signal, body, params: { header: { Origin: window.location.origin } } }), adminSchema)
}

export function login(body: Schemas['LoginInput'], signal: AbortSignal): Promise<Admin> {
  return authRequest(signal, (signal) => apiClient.POST('/auth/login', { signal, body, params: { header: { Origin: window.location.origin } } }), adminSchema)
}

export function changePassword(body: Schemas['AuthPasswordChange'], context: AuthWriteContext): Promise<Schemas['ActionAck']> {
  return authRequest(context.signal, (signal) => apiClient.POST('/auth/password', { signal, body, params: { header: writeHeaders(context) } }), ackSchema)
}

export function reauthenticate(body: Schemas['LoginInput'], context: AuthWriteContext): Promise<Schemas['ReauthResult']> {
  return authRequest(context.signal, (signal) => apiClient.POST('/auth/reauth', { signal, body, params: { header: writeHeaders(context) } }), reauthSchema)
}

export function logout(context: AuthWriteContext): Promise<Schemas['ActionAck']> {
  return authRequest(context.signal, (signal) => apiClient.POST('/auth/logout', { signal, params: { header: writeHeaders(context) } }), ackSchema)
}
