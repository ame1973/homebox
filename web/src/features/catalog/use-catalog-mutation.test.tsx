import type { ReactNode } from 'react'
import { QueryClientProvider } from '@tanstack/react-query'
import { act, renderHook } from '@testing-library/react'
import { beforeEach, expect, it, vi } from 'vitest'
import { fetchCSRF } from '@/lib/api/auth'
import { ApiError } from '@/lib/api/client'
import { createQueryClient } from '@/lib/query'
import { sendCatalogCommand, type CatalogCommandInput, type CatalogResult } from './commands'
import { useCatalogMutation } from './use-catalog-mutation'

vi.mock('@/lib/api/auth', () => ({ fetchCSRF: vi.fn() }))
vi.mock('./commands', async (original) => ({ ...await original<typeof import('./commands')>(), sendCatalogCommand: vi.fn() }))
const item = { id: 'item', revision: 7, name: 'Original', brand: null, model: '', category_id: null, description: '', notes: '',
  tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1, status: 'active' } as const
const proposal: CatalogCommandInput = { kind: 'patch', subject: item, body: { name: 'Reviewed' } }
const reply: CatalogResult = { ...item, name: 'Reviewed', revision: 8 }
const keys = [['catalog', 'items'], ['catalog', 'item', item.id], ['taxonomy', 'categories'], ['taxonomy', 'tags'], ['holdings', 'item', item.id], ['purchases', 'item', item.id], ['purchases', 'allocation', 'purchase', item.id]] as const
beforeEach(() => {
  vi.mocked(fetchCSRF).mockReset().mockResolvedValue('csrf')
  vi.mocked(sendCatalogCommand).mockReset().mockResolvedValue(reply)
})
function fixture() {
  const client = createQueryClient()
  const onConfirmed = vi.fn()
  const onSessionLost = vi.fn()
  const hook = renderHook(() => useCatalogMutation(onConfirmed, onSessionLost), { wrapper: ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider> })
  for (const key of keys) client.setQueryData(key, { ...item, revision: 9, name: 'Newer current source' })
  return { ...hook, client, onConfirmed, onSessionLost, dispose: () => { hook.unmount(); client.clear() } }
}

it('reviews without writing and confirms once, invalidating all dependent read scopes without installing an old reply', async () => {
  const work = fixture()
  work.client.setQueryData(['locations', 'tree'], 'unrelated')
  work.client.setQueryData(['auth', 'me'], 'unrelated')
  act(() => { work.result.current.review(proposal) })
  expect(fetchCSRF).not.toHaveBeenCalled()
  expect(sendCatalogCommand).not.toHaveBeenCalled()
  const original = work.result.current.command
  let release: ((value: CatalogResult) => void) | undefined
  vi.mocked(sendCatalogCommand).mockImplementation(() => new Promise((resolve) => { release = resolve }))
  let pending: Promise<void> | undefined
  await act(async () => { pending = work.result.current.confirm(); await work.result.current.confirm() })
  expect(sendCatalogCommand).toHaveBeenCalledTimes(1)
  expect(work.client.getMutationCache().getAll()[0]?.options).toMatchObject({ retry: false, networkMode: 'always', gcTime: 0 })
  await act(async () => { release?.(reply); await pending })
  expect(work.onConfirmed).toHaveBeenCalledWith(original, reply)
  expect(work.result.current.confirmed).toBe(true)
  expect(work.result.current.canLeave()).toBe(true)
  for (const key of keys) {
    expect(work.client.getQueryState(key)?.isInvalidated).toBe(true)
    expect(work.client.getQueryData(key)).toMatchObject({ revision: 9, name: 'Newer current source' })
  }
  expect(work.client.getQueryState(['locations', 'tree'])?.isInvalidated).toBe(false)
  expect(work.client.getQueryState(['auth', 'me'])?.isInvalidated).toBe(false)
  work.dispose()
})

it.each(['NETWORK_ERROR', 'INVALID_RESPONSE', 'ITEM_HAS_HISTORY'] as const)('rechecks classification/item reads after sent %s while retaining the reviewed command', async (code) => {
  const work = fixture()
  vi.mocked(sendCatalogCommand).mockRejectedValueOnce(new ApiError(code, { status: code === 'ITEM_HAS_HISTORY' ? 409 : 200 }))
  act(() => { work.result.current.review(proposal) })
  const original = work.result.current.command
  await act(async () => { await work.result.current.confirm() })
  expect(work.result.current.phase).toBe(code === 'ITEM_HAS_HISTORY' ? 'rejected' : 'uncertain')
  expect(work.result.current.command).toBe(original)
  for (const key of keys) expect(work.client.getQueryState(key)?.isInvalidated).toBe(true)
  await act(async () => { window.dispatchEvent(new Event('online')) })
  expect(sendCatalogCommand).toHaveBeenCalledTimes(1)
  expect(work.onConfirmed).not.toHaveBeenCalled()
  work.dispose()
})

it('retains original deletion context after current reads remove the target, retrying only explicitly with the same command', async () => {
  const work = fixture()
  const acknowledgement = { ok: true, operation_id: 'event', revision: 8 } as const
  vi.mocked(sendCatalogCommand).mockRejectedValueOnce(new ApiError('INVALID_RESPONSE')).mockResolvedValueOnce(acknowledgement)
  act(() => { work.result.current.review({ kind: 'delete', subject: item }) })
  const original = work.result.current.command
  await act(async () => { await work.result.current.confirm() })
  work.client.removeQueries({ queryKey: ['catalog', 'item', item.id] })
  act(() => {
    work.result.current.dismiss()
    work.result.current.review({ kind: 'create', body: { name: 'Must not replace the original' } })
    window.dispatchEvent(new Event('offline'))
    window.dispatchEvent(new Event('online'))
  })
  expect(work.result.current.command).toBe(original)
  expect(sendCatalogCommand).toHaveBeenCalledTimes(1)
  expect(work.result.current.open).toBe(false)
  act(() => { work.result.current.reopen() })
  vi.mocked(fetchCSRF).mockResolvedValueOnce('fresh-csrf')
  await act(async () => { await work.result.current.confirm() })
  expect(sendCatalogCommand).toHaveBeenNthCalledWith(1, original, 'csrf', expect.any(AbortSignal))
  expect(sendCatalogCommand).toHaveBeenNthCalledWith(2, original, 'fresh-csrf', expect.any(AbortSignal))
  expect(work.onConfirmed).toHaveBeenCalledWith(original, acknowledgement)
  work.dispose()
})

it('keeps a pre-send CSRF failure rejected without invalidating or dispatching dependent domain reads', async () => {
  const work = fixture()
  vi.mocked(fetchCSRF).mockRejectedValueOnce(new ApiError('NETWORK_ERROR'))
  act(() => { work.result.current.review(proposal) })
  await act(async () => { await work.result.current.confirm() })
  expect(work.result.current.phase).toBe('rejected')
  for (const key of keys) expect(work.client.getQueryState(key)?.isInvalidated).toBe(false)
  expect(sendCatalogCommand).not.toHaveBeenCalled()
  work.dispose()
})

it('interrupts a sent request without accepting a late commit reply or replaying on reconnect', async () => {
  const work = fixture()
  let release: ((value: CatalogResult) => void) | undefined
  let requestSignal: AbortSignal | undefined
  vi.mocked(sendCatalogCommand).mockImplementation((_command, _csrf, signal) => { requestSignal = signal; return new Promise((resolve) => { release = resolve }) })
  act(() => { work.result.current.review(proposal) })
  let pending: Promise<void> | undefined
  await act(async () => { pending = work.result.current.confirm() })
  act(() => { window.dispatchEvent(new Event('offline')) })
  expect(requestSignal?.aborted).toBe(true)
  await act(async () => { release?.(reply); await pending })
  expect(work.result.current.phase).toBe('uncertain')
  for (const key of keys) expect(work.client.getQueryState(key)?.isInvalidated).toBe(true)
  await act(async () => { window.dispatchEvent(new Event('online')) })
  expect(sendCatalogCommand).toHaveBeenCalledTimes(1)
  expect(work.onConfirmed).not.toHaveBeenCalled()
  work.dispose()
})

it.each([false, true])('clears catalog command context on session loss, mandatory=%s', async (mandatory) => {
  const work = fixture()
  vi.mocked(sendCatalogCommand).mockRejectedValueOnce(new ApiError(mandatory ? 'PASSWORD_CHANGE_REQUIRED' : 'AUTHENTICATION_REQUIRED', { status: mandatory ? 403 : 401 }))
  act(() => { work.result.current.review(proposal) })
  await act(async () => { await work.result.current.confirm() })
  expect(work.result.current.command).toBeUndefined()
  expect(work.result.current.open).toBe(false)
  expect(work.result.current.canLeave()).toBe(true)
  expect(work.onSessionLost).toHaveBeenCalledWith(mandatory)
  work.dispose()
})
