import { z } from 'zod'
import { validAuthText } from '@/lib/api/auth'
import { messages } from '@/lib/messages'

export type AuthMode = 'login' | 'setup' | 'password'
export const emptyCredentials = { username: '', password: '', newPassword: '', confirmation: '', bootstrapToken: '' }
export type CredentialValues = typeof emptyCredentials

export function credentialSchema(mode: AuthMode) {
  const text = messages.auth
  return z.strictObject({ username: z.string(), password: z.string(), newPassword: z.string(), confirmation: z.string(), bootstrapToken: z.string() }).superRefine((value, context) => {
    const issue = (path: keyof CredentialValues, message: string) => context.addIssue({ code: 'custom', path: [path], message })
    if (mode !== 'password' && !validAuthText(value.username, 1, 64)) issue('username', text.usernameBounds)
    if (!validAuthText(value.password, mode === 'setup' ? 12 : 1, 256)) issue('password', mode === 'setup' ? text.newPasswordBounds : text.passwordBounds)
    if (mode === 'setup' && !/^[A-Za-z0-9_-]{43}$/.test(value.bootstrapToken)) issue('bootstrapToken', text.bootstrapBounds)
    if (mode === 'password') {
      if (!validAuthText(value.newPassword, 12, 256)) issue('newPassword', text.newPasswordBounds)
      if (value.newPassword === value.password) issue('newPassword', text.passwordSame)
    }
    if (mode !== 'login' && value.confirmation !== (mode === 'setup' ? value.password : value.newPassword)) issue('confirmation', text.confirmationError)
  })
}
