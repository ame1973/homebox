import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router'
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeAll, beforeEach, expect, it, vi } from 'vitest'
import App from '@/App'
import * as auth from '@/lib/api/auth'
import { messages } from '@/lib/messages'
import { createQueryClient } from '@/lib/query'

vi.mock('@/features/scan/scan-page', () => ({ default: () => <p>Private scan content</p> }))
vi.mock('@/features/capture/capture-page', () => ({ default: () => <p>Private capture content</p> }))
vi.mock('@/features/catalog/catalog-page', () => ({ default: () => <p>Private Item draft and candidates</p> }))
vi.mock('@/lib/api/auth', async (original) => ({ ...await original<typeof import('@/lib/api/auth')>(), fetchAdmin: vi.fn() }))
const disposers: (() => void)[] = []
beforeAll(async () => { await Promise.all([import('@/features/scan/scan-page'), import('@/features/capture/capture-page'), import('@/features/catalog/catalog-page'), import('@/lib/settings-navigation')]) })
beforeEach(() => { vi.mocked(auth.fetchAdmin).mockReset().mockResolvedValue({ id: 'owner', username: 'owner', revision: 1, must_change_password: false }) })
afterEach(() => { for (const dispose of disposers.splice(0)) dispose() })

it('suspends ordinary Item creation and its private candidates before BFCache restoration', async () => {
  const client = createQueryClient()
  client.setQueryData(['private', 'cached'], 'private candidate')
  const view = render(<QueryClientProvider client={client}><MemoryRouter initialEntries={['/items/new']}><App /></MemoryRouter></QueryClientProvider>)
  disposers.push(() => { view.unmount(); client.clear() })
  expect(await screen.findByText('Private Item draft and candidates')).toBeVisible()
  act(() => { window.dispatchEvent(new PageTransitionEvent('pagehide', { persisted: true })) })
  expect(screen.getByRole('heading', { name: messages.catalog.restorePageTitle })).toBeVisible()
  expect(screen.getByText(messages.catalog.restorePageHelp)).toBeVisible()
  expect(screen.queryByText('Private Item draft and candidates')).not.toBeInTheDocument()
  expect(client.getQueryData(['private', 'cached'])).toBeUndefined()
  expect(auth.fetchAdmin).not.toHaveBeenCalled()
  act(() => { window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true })) })
  await waitFor(() => expect(auth.fetchAdmin).toHaveBeenCalledOnce())
  await userEvent.setup().click(await screen.findByRole('button', { name: messages.scan.restoreContinue }))
  expect(await screen.findByText('Private Item draft and candidates')).toBeVisible()
})

it('redacts a capture screen before BFCache and requires a fresh session before continuing', async () => {
  const client = createQueryClient()
  client.setQueryData(['private', 'cached'], 'private capture')
  const path = '/photos/capture?owner_type=item&owner_id=object'
  const view = render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[path]}><App /></MemoryRouter></QueryClientProvider>)
  disposers.push(() => { view.unmount(); client.clear() })
  expect(await screen.findByText('Private capture content')).toBeVisible()
  act(() => { window.dispatchEvent(new PageTransitionEvent('pagehide', { persisted: true })) })
  expect(screen.getByRole('heading', { name: messages.capture.restoreTitle })).toBeVisible()
  expect(screen.getByText(messages.capture.restoreHelp)).toBeVisible()
  expect(screen.queryByText('Private capture content')).not.toBeInTheDocument()
  expect(client.getQueryData(['private', 'cached'])).toBeUndefined()
  expect(auth.fetchAdmin).not.toHaveBeenCalled()
  act(() => { window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true })) })
  await waitFor(() => expect(auth.fetchAdmin).toHaveBeenCalledOnce())
  await userEvent.setup().click(await screen.findByRole('button', { name: messages.scan.restoreContinue }))
  expect(await screen.findByText('Private capture content')).toBeVisible()
})

it('does not resume a suspended scan screen on a non-persisted pageshow', async () => {
  const client = createQueryClient()
  client.setQueryData(['private', 'cached'], 'private cache')
  const view = render(<QueryClientProvider client={client}><MemoryRouter initialEntries={['/scan']}><App /></MemoryRouter></QueryClientProvider>)
  disposers.push(() => { view.unmount(); client.clear() })
  expect(await screen.findByText('Private scan content')).toBeVisible()
  act(() => { window.dispatchEvent(new PageTransitionEvent('pagehide', { persisted: true })) })
  expect(screen.getByText(messages.scan.restoreWaiting)).toBeVisible()
  expect(screen.queryByText('Private scan content')).not.toBeInTheDocument()
  expect(client.getQueryData(['private', 'cached'])).toBeUndefined()
  expect(auth.fetchAdmin).not.toHaveBeenCalled()
  act(() => { window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: false })) })
  expect(screen.getByText(messages.scan.restoreWaiting)).toBeVisible()
  expect(auth.fetchAdmin).not.toHaveBeenCalled()
  act(() => { window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true })) })
  await waitFor(() => expect(auth.fetchAdmin).toHaveBeenCalledOnce())
  await userEvent.setup().click(await screen.findByRole('button', { name: messages.scan.restoreContinue }))
  expect(await screen.findByText('Private scan content')).toBeVisible()
  expect(screen.getByRole('main')).toHaveFocus()
})
