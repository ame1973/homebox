import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expect, it, vi } from 'vitest'
import { createQueryClient } from '@/lib/query'
import { messages } from '@/lib/messages'
import App from './App'

vi.mock('@/lib/api/client', () => ({ fetchHealth: vi.fn().mockResolvedValue({ status: 'ok' }) }))

it('provides a working recovery route rather than a fake domain page', async () => {
  const client = createQueryClient()
  const view = render(<QueryClientProvider client={client}><MemoryRouter initialEntries={['/missing']}><App /></MemoryRouter></QueryClientProvider>)
  expect(await screen.findByRole('heading', { level: 1, name: messages.app.notFound })).toBeVisible()
  expect(screen.getByRole('link', { name: messages.app.skip })).toHaveAttribute('href', '#main')
  await userEvent.click(screen.getByRole('link', { name: messages.app.returnHome }))
  expect(await screen.findByRole('heading', { name: messages.connection.title })).toBeVisible()
  expect(await screen.findByText(messages.connection.ok)).toBeVisible()
  await userEvent.click(screen.getByRole('link', { name: messages.app.skip }))
  expect(screen.getByRole('main')).toHaveFocus()
  expect(window.location.hash).toBe('')
  view.unmount()
  client.clear()
})
