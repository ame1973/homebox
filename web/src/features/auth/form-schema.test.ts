import { describe, expect, it } from 'vitest'
import { credentialSchema, emptyCredentials } from './form-schema'
import { messages } from '@/lib/messages'

const password = 'a valid temporary password'
const setup = { ...emptyCredentials, username: 'owner', password, confirmation: password, bootstrapToken: 'A'.repeat(43) }

describe('credential form contracts', () => {
  it('supports legacy login lengths without trimming or normalizing', () => {
    const value = { ...emptyCredentials, username: ' owner ', password: ' ' }
    expect(credentialSchema('login').parse(value)).toEqual(value)
  })

  it('validates setup and a different confirmed replacement password', () => {
    expect(credentialSchema('setup').parse(setup)).toEqual(setup)
    const value = { ...emptyCredentials, password, newPassword: 'a different private password', confirmation: 'a different private password' }
    expect(credentialSchema('password').parse(value)).toEqual(value)
  })

  it.each([
    ['username', '', messages.auth.usernameBounds],
    ['username', 'a'.repeat(65), messages.auth.usernameBounds],
    ['password', '😀'.repeat(6), messages.auth.newPasswordBounds],
    ['password', '\uD800', messages.auth.newPasswordBounds],
    ['bootstrapToken', 'short', messages.auth.bootstrapBounds],
    ['confirmation', 'not matching', messages.auth.confirmationError],
  ] as const)('places the setup %s error beside the field', (field, value, message) => {
    const result = credentialSchema('setup').safeParse({ ...setup, [field]: value })
    expect(result.success).toBe(false)
    if (!result.success) expect(result.error.issues).toContainEqual(expect.objectContaining({ path: [field], message }))
  })

  it('rejects a missing current proof, reused/weak replacement, and mismatched confirmation', () => {
    const base = { ...emptyCredentials, password, newPassword: password, confirmation: 'different' }
    const result = credentialSchema('password').safeParse(base)
    expect(result.success).toBe(false)
    if (!result.success) {
      expect(result.error.issues).toContainEqual(expect.objectContaining({ path: ['newPassword'], message: messages.auth.passwordSame }))
      expect(result.error.issues).toContainEqual(expect.objectContaining({ path: ['confirmation'] }))
    }
    expect(credentialSchema('password').safeParse({ ...base, password: '', newPassword: 'short' }).success).toBe(false)
    expect(credentialSchema('login').safeParse({ ...emptyCredentials, username: 'owner' }).success).toBe(false)
  })

  it('counts full code points and never silently accepts extra form fields', () => {
    const value = { ...setup, password: '😀'.repeat(12), confirmation: '😀'.repeat(12) }
    expect(credentialSchema('setup').parse(value)).toEqual(value)
    expect(credentialSchema('setup').safeParse({ ...value, rememberPassword: true }).success).toBe(false)
  })
})
