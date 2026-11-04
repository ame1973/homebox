import { z } from 'zod'
import { apiClient, requestData, validText, writeHeaders, type WriteContext } from './client'
import type { components } from './schema'

// Preserve the existing account adapter boundary while sharing the actual
// transport/error/Unicode implementations with domain adapters.
export { ApiError, validText as validAuthText } from './client'
export type { WriteContext as AuthWriteContext } from './client'

type Schemas = components['schemas']
export type Admin = Schemas['Admin']

const text = (minimum: number, maximum: number) => z.string().refine((value) => validText(value, minimum, maximum))
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

export function fetchAdmin(signal: AbortSignal): Promise<Admin> {
  return requestData(signal, (signal) => apiClient.GET('/auth/me', { signal }), adminSchema, 8_000)
}

// CSRF and grants are returned to the caller, never cached or persisted here.
// Each write makes one request only: no hidden refresh, retry, queue, or replay.
export async function fetchCSRF(signal: AbortSignal): Promise<string> {
  const result = await requestData(signal, (signal) => apiClient.GET('/auth/csrf', { signal }), csrfSchema, 8_000)
  return result.token
}

export function setupAdmin(body: Schemas['AuthSetupInput'], signal: AbortSignal): Promise<Admin> {
  return requestData(signal, (signal) => apiClient.POST('/auth/setup', { signal, body, params: { header: { Origin: window.location.origin } } }), adminSchema)
}

export function login(body: Schemas['LoginInput'], signal: AbortSignal): Promise<Admin> {
  return requestData(signal, (signal) => apiClient.POST('/auth/login', { signal, body, params: { header: { Origin: window.location.origin } } }), adminSchema)
}

export function changePassword(body: Schemas['AuthPasswordChange'], context: WriteContext): Promise<Schemas['ActionAck']> {
  return requestData(context.signal, (signal) => apiClient.POST('/auth/password', { signal, body, params: { header: writeHeaders(context) } }), ackSchema)
}

export function reauthenticate(body: Schemas['LoginInput'], context: WriteContext): Promise<Schemas['ReauthResult']> {
  return requestData(context.signal, (signal) => apiClient.POST('/auth/reauth', { signal, body, params: { header: writeHeaders(context) } }), reauthSchema)
}

export function logout(context: WriteContext): Promise<Schemas['ActionAck']> {
  return requestData(context.signal, (signal) => apiClient.POST('/auth/logout', { signal, params: { header: writeHeaders(context) } }), ackSchema)
}
