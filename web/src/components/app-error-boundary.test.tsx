import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { expect, it, vi } from 'vitest'
import { messages } from '@/lib/messages'
import { AppErrorBoundary } from './app-error-boundary'

it('renders children until an error and never exposes private exception text', () => {
  const view = render(<MemoryRouter><AppErrorBoundary><p>Working screen</p></AppErrorBoundary></MemoryRouter>)
  expect(screen.getByText('Working screen')).toBeVisible()
  view.unmount()
  vi.spyOn(console, 'error').mockImplementation(() => {})
  function Broken(): never { throw new Error('private chunk diagnostic') }
  render(<MemoryRouter><AppErrorBoundary><Broken /></AppErrorBoundary></MemoryRouter>)
  expect(screen.getByRole('heading', { name: messages.app.loadError })).toBeVisible()
  expect(screen.getByRole('button', { name: messages.app.reload })).toBeVisible()
  expect(screen.getByRole('link', { name: messages.app.returnHome })).toHaveAttribute('href', '/')
  expect(screen.queryByText('private chunk diagnostic')).not.toBeInTheDocument()
})
