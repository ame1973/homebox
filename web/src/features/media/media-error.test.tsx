import { render, screen } from '@testing-library/react'
import { expect, it } from 'vitest'
import { ApiError } from '@/lib/api/client'
import { messages } from '@/lib/messages'
import { MediaError } from './media-error'

it.each([
  ['MEDIA_COVER_CONFLICT', messages.media.coverConflict], ['INVALID_MEDIA_URL', messages.locations.validation],
  ['NOT_FOUND', messages.media.missingHelp], ['INVALID_RESPONSE', messages.media.invalidRead], ['INVALID_CURSOR', messages.media.invalidRead],
] as const)('renders static safe guidance for %s without exposing error text', (code, guidance) => {
  const error = new ApiError(code, { retryAfterSeconds: 12 })
  error.message = 'Private arbitrary server detail'
  render(<MediaError error={error} />)
  expect(screen.getByRole('alert')).toHaveTextContent(guidance)
  expect(screen.getByText(`${messages.auth.seconds}12`)).toBeVisible()
  expect(screen.queryByText('Private arbitrary server detail')).not.toBeInTheDocument()
})

it('uses shared safe guidance for other errors', () => {
  render(<MediaError error={new ApiError('NETWORK_ERROR')} />)
  expect(screen.getByRole('alert')).toHaveTextContent(messages.locations.network)
})
