import { QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createQueryClient } from '@/lib/query'
import { messages } from '@/lib/messages'
import { fetchHealth } from '@/lib/api/client'
import ConnectionPage from './connection-page'

vi.mock('@/lib/api/client', () => ({ fetchHealth: vi.fn() }))
const client = createQueryClient()
const mock = vi.mocked(fetchHealth)
beforeEach(() => { mock.mockReset() })
afterEach(() => client.clear())
function show() { render(<QueryClientProvider client={client}><ConnectionPage /></QueryClientProvider>) }

it('shows loading feedback and disables duplicate requests', () => {
  mock.mockReturnValue(new Promise(() => {}))
  show()
  expect(screen.getByRole('status')).toHaveTextContent(messages.connection.pending)
  expect(screen.getByRole('button', { name: messages.connection.checking })).toBeDisabled()
})

it.each(['ok', 'degraded'] as const)('shows the real %s result and a response timestamp', async (status) => {
  mock.mockResolvedValue({ status })
  show()
  expect(await screen.findByText(messages.connection[status])).toBeVisible()
  expect(screen.getByText(messages.connection.checked)).toBeVisible()
  expect(screen.getByRole('button', { name: messages.connection.retry })).toBeEnabled()
})

it('recovers after a failed request through an explicit retry', async () => {
  mock.mockRejectedValueOnce(new Error('Unavailable')).mockResolvedValueOnce({ status: 'ok' })
  show()
  expect(await screen.findByText(messages.connection.error)).toBeVisible()
  expect(screen.getByText(messages.connection.noResult)).toBeVisible()
  await userEvent.click(screen.getByRole('button', { name: messages.connection.retry }))
  expect(await screen.findByText(messages.connection.ok)).toBeVisible()
  expect(mock).toHaveBeenCalledTimes(2)
})

it('does not keep presenting stale success after a failed recheck', async () => {
  mock.mockResolvedValueOnce({ status: 'ok' }).mockRejectedValueOnce(new Error('Disconnected'))
  show()
  await screen.findByText(messages.connection.ok)
  await userEvent.click(screen.getByRole('button', { name: messages.connection.retry }))
  expect(await screen.findByText(messages.connection.error)).toBeVisible()
  expect(screen.queryByText(messages.connection.ok)).not.toBeInTheDocument()
  expect(screen.getByText(messages.connection.checked)).toBeVisible()
})
