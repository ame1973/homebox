import type { ReactNode } from 'react'
import { QueryClientProvider } from '@tanstack/react-query'
import { act, renderHook } from '@testing-library/react'
import { beforeEach, expect, it, vi } from 'vitest'
import { fetchCSRF } from '@/lib/api/auth'
import { ApiError } from '@/lib/api/client'
import { createQueryClient } from '@/lib/query'
import { sendTaxonomyCommand, type TaxonomyCommandInput, type TaxonomyResult } from './commands'
import { useTaxonomyMutation } from './use-taxonomy-mutation'

vi.mock('@/lib/api/auth', () => ({ fetchCSRF: vi.fn() }))
vi.mock('./commands', async (original) => ({ ...await original<typeof import('./commands')>(), sendTaxonomyCommand: vi.fn() }))
const proposal: TaxonomyCommandInput = { kind: 'create-category', body: { name: 'Child', parent_id: 'parent' }, parent: { id: 'parent', parent_id: null, name: 'Parent', revision: 7 } }
const reply: TaxonomyResult = { id: 'child', name: 'Child', parent_id: 'parent', revision: 1 }
beforeEach(() => {
  vi.mocked(fetchCSRF).mockReset().mockResolvedValue('csrf')
  vi.mocked(sendTaxonomyCommand).mockReset().mockResolvedValue(reply)
})
function fixture() {
  const client = createQueryClient()
  const onConfirmed = vi.fn()
  const onSessionLost = vi.fn()
  const hook = renderHook(() => useTaxonomyMutation(onConfirmed, onSessionLost), { wrapper: ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider> })
  return { ...hook, client, onConfirmed, onSessionLost, dispose: () => { hook.unmount(); client.clear() } }
}

it('reviews without writing and confirms only once, invalidating dependent taxonomy/catalog reads rather than installing old replies', async () => {
  const work = fixture()
  const current = { revision: 8, name: 'Current' }
  work.client.setQueryData(['taxonomy', 'categories'], current)
  work.client.setQueryData(['taxonomy', 'tags'], current)
  work.client.setQueryData(['catalog', 'item', 'item'], current)
  work.client.setQueryData(['catalog', 'items', { q: 'Original' }], current)
  work.client.setQueryData(['holdings', 'item', 'item'], current)
  work.client.setQueryData(['purchases', 'list', { category_id: 'parent' }], current)
  work.client.setQueryData(['locations', 'tree'], current)
  act(() => { work.result.current.review(proposal) })
  expect(fetchCSRF).not.toHaveBeenCalled()
  const original = work.result.current.command
  let release: ((value: TaxonomyResult) => void) | undefined
  vi.mocked(sendTaxonomyCommand).mockImplementation(() => new Promise((resolve) => { release = resolve }))
  let pending: Promise<void> | undefined
  await act(async () => { pending = work.result.current.confirm(); await work.result.current.confirm() })
  expect(sendTaxonomyCommand).toHaveBeenCalledTimes(1)
  expect(work.client.getMutationCache().getAll()[0]?.options).toMatchObject({ retry: false, networkMode: 'always', gcTime: 0 })
  await act(async () => { release?.(reply); await pending })
  expect(work.onConfirmed).toHaveBeenCalledWith(original, reply)
  expect(work.result.current.confirmed).toBe(true)
  expect(work.result.current.canLeave()).toBe(true)
  expect(work.client.getQueryData(['taxonomy', 'categories'])).toBe(current)
  expect(work.client.getQueryState(['taxonomy', 'categories'])?.isInvalidated).toBe(true)
  expect(work.client.getQueryState(['taxonomy', 'tags'])?.isInvalidated).toBe(true)
  expect(work.client.getQueryState(['catalog', 'item', 'item'])?.isInvalidated).toBe(true)
  expect(work.client.getQueryData(['catalog', 'item', 'item'])).toBe(current)
  expect(work.client.getQueryState(['catalog', 'items', { q: 'Original' }])?.isInvalidated).toBe(true)
  expect(work.client.getQueryState(['holdings', 'item', 'item'])?.isInvalidated).toBe(true)
  expect(work.client.getQueryData(['holdings', 'item', 'item'])).toBe(current)
  expect(work.client.getQueryState(['purchases', 'list', { category_id: 'parent' }])?.isInvalidated).toBe(true)
  expect(work.client.getQueryData(['purchases', 'list', { category_id: 'parent' }])).toBe(current)
  expect(work.client.getQueryState(['locations', 'tree'])?.isInvalidated).toBe(false)
  work.dispose()
})

it('retains an uncertain taxonomy request through dismissal and explicit same-key retry without reconnect replay', async () => {
  const work = fixture()
  work.client.setQueryData(['taxonomy', 'categories'], 'current')
  work.client.setQueryData(['catalog', 'items'], 'current')
  work.client.setQueryData(['holdings', 'stock'], 'current')
  work.client.setQueryData(['purchases', 'allocation', 'purchase', 'item'], 'current')
  vi.mocked(sendTaxonomyCommand).mockRejectedValueOnce(new ApiError('NETWORK_ERROR'))
  act(() => { work.result.current.review(proposal) })
  const original = work.result.current.command
  await act(async () => { await work.result.current.confirm() })
  expect(work.result.current.phase).toBe('uncertain')
  expect(work.client.getQueryState(['taxonomy', 'categories'])?.isInvalidated).toBe(true)
  expect(work.client.getQueryState(['catalog', 'items'])?.isInvalidated).toBe(true)
  expect(work.client.getQueryState(['holdings', 'stock'])?.isInvalidated).toBe(true)
  expect(work.client.getQueryState(['purchases', 'allocation', 'purchase', 'item'])?.isInvalidated).toBe(true)
  expect(work.client.getQueryData(['purchases', 'allocation', 'purchase', 'item'])).toBe('current')
  act(() => { work.result.current.dismiss(); work.result.current.review({ kind: 'create-tag', body: { name: 'Must not replace' } }) })
  expect(work.result.current.command).toBe(original)
  expect(work.result.current.open).toBe(false)
  await act(async () => { window.dispatchEvent(new Event('online')); await Promise.resolve() })
  expect(sendTaxonomyCommand).toHaveBeenCalledTimes(1)
  act(() => { work.result.current.reopen() })
  await act(async () => { await work.result.current.confirm() })
  expect(sendTaxonomyCommand).toHaveBeenLastCalledWith(original, 'csrf', expect.any(AbortSignal))
  expect(work.onConfirmed).toHaveBeenCalledWith(original, reply)
  work.dispose()
})

it('keeps exact-name conflict editable, but a pre-send CSRF failure never becomes a sent uncertain write', async () => {
  const work = fixture()
  vi.mocked(sendTaxonomyCommand).mockRejectedValueOnce(new ApiError('TAG_NAME_CONFLICT', { status: 409 }))
  act(() => { work.result.current.review({ kind: 'create-tag', body: { name: 'Existing' } }) })
  await act(async () => { await work.result.current.confirm() })
  expect(work.result.current.phase).toBe('rejected')
  act(() => { work.result.current.dismiss(); work.result.current.review(proposal) })
  vi.mocked(fetchCSRF).mockRejectedValueOnce(new ApiError('NETWORK_ERROR'))
  await act(async () => { await work.result.current.confirm() })
  expect(work.result.current.phase).toBe('rejected')
  expect(sendTaxonomyCommand).toHaveBeenCalledTimes(1)
  work.dispose()
})

it.each([false, true])('clears proposals on taxonomy session loss, mandatory=%s', async (mandatory) => {
  const work = fixture()
  vi.mocked(sendTaxonomyCommand).mockRejectedValueOnce(new ApiError(mandatory ? 'PASSWORD_CHANGE_REQUIRED' : 'AUTHENTICATION_REQUIRED', { status: mandatory ? 403 : 401 }))
  act(() => { work.result.current.review(proposal) })
  await act(async () => { await work.result.current.confirm() })
  expect(work.onSessionLost).toHaveBeenCalledWith(mandatory)
  expect(work.result.current.command).toBeUndefined()
  expect(work.result.current.open).toBe(false)
  expect(work.result.current.canLeave()).toBe(true)
  work.dispose()
})
