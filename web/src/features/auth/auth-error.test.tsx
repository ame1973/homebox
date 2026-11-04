import { render, screen } from '@testing-library/react'
import { expect, it } from 'vitest'
import { ApiError } from '@/lib/api/auth'
import { messages } from '@/lib/messages'
import { AuthError } from './auth-error'

const text = messages.auth
it.each([
  ['AUTHENTICATION_REQUIRED', text.credentials], ['CREDENTIALS_REJECTED', text.oldPasswordError],
  ['SETUP_CLOSED', text.setupClosed], ['CSRF_REJECTED', text.forbidden], ['ORIGIN_REJECTED', text.forbidden],
  ['CLIENT_UPGRADE_REQUIRED', text.upgrade], ['IDEMPOTENCY_CONFLICT', text.conflict],
  ['PASSWORD_CHANGE_REQUIRED', text.mandatoryHelp], ['VALIDATION_ERROR', text.validation],
  ['RATE_LIMITED', text.rate], ['NETWORK_ERROR', text.network], ['REQUEST_ABORTED', text.uncertain],
  ['REQUEST_TIMEOUT', text.uncertain], ['INVALID_RESPONSE', text.uncertain], ['SERVICE_UNAVAILABLE', text.busy],
] as const)('localizes %s without displaying server diagnostics', (code, message) => {
  render(<AuthError error={new ApiError(code, { retryAfterSeconds: 15 })} />)
  expect(screen.getByRole('alert')).toHaveTextContent(message)
  expect(screen.getByRole('alert')).toHaveTextContent(text.seconds + '15')
})

it('uses setup-token guidance and hides unexpected exception content', () => {
  const view = render(<AuthError setup error={new ApiError('AUTHENTICATION_REQUIRED')} />)
  expect(screen.getByRole('alert')).toHaveTextContent(text.bootstrapRejected)
  view.rerender(<AuthError error={new Error('private diagnostic content')} />)
  expect(screen.getByRole('alert')).toHaveTextContent(text.busy)
  expect(screen.getByRole('alert')).not.toHaveTextContent('private diagnostic content')
})
