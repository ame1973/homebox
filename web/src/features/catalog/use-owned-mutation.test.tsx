import type { ReactNode } from 'react'
import { QueryClientProvider } from '@tanstack/react-query'
import { act, renderHook } from '@testing-library/react'
import { beforeEach, expect, it, vi } from 'vitest'
import { fetchCSRF } from '@/lib/api/auth'
import { ApiError } from '@/lib/api/client'
import { createQueryClient } from '@/lib/query'
import { sendOwnedCommand, type OwnedCommandInput, type OwnedResult } from './owned-commands'
import { useOwnedMutation } from './use-owned-mutation'

vi.mock('@/lib/api/auth', () => ({ fetchCSRF: vi.fn() }))
vi.mock('./owned-commands', async (original) => ({ ...await original<typeof import('./owned-commands')>(), sendOwnedCommand: vi.fn() }))
const owner = { id: 'item', revision: 7, name: 'Owner', brand: null, model: '', category_id: null, description: '', notes: '',
  tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1, status: 'active' } as const
const subject = { id: 'alias', item_id: owner.id, revision: 2, alias: 'Original' } as const
const proposal: OwnedCommandInput = { kind: 'patch-alias', owner, subject, body: { alias: 'Reviewed' } }
const reply: OwnedResult = { ...subject, alias: 'Reviewed', revision: 3 }
const keys = [['catalog', 'items'], ['catalog', 'item', owner.id], ['catalog', 'owned', owner.id], ['taxonomy', 'tags'], ['holdings', 'item', owner.id], ['purchases', 'item', owner.id], ['purchases', 'allocation', 'purchase', owner.id], ['assets', 'profile', 'unit'], ['assets', 'usage', 'unit'], ['media', 'owner', 'item', owner.id], ['media', 'membership', 'photo']] as const
beforeEach(() => {
  vi.mocked(fetchCSRF).mockReset().mockResolvedValue('csrf')
  vi.mocked(sendOwnedCommand).mockReset().mockResolvedValue(reply)
})
function fixture() {
  const client = createQueryClient()
  const onConfirmed = vi.fn()
  const onSessionLost = vi.fn()
  const hook = renderHook(() => useOwnedMutation(onConfirmed, onSessionLost), { wrapper: ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider> })
  for (const key of keys) client.setQueryData(key, { revision: 99, current: true })
  client.setQueryData(['locations', 'tree'], 'unrelated')
  client.setQueryData(['auth', 'me'], 'unrelated')
  return { ...hook, client, onConfirmed, onSessionLost, dispose: () => { hook.unmount(); client.clear() } }
}

it('requires review and one foreground confirmation, invalidating only dependent reads without installing old replies', async () => {
  const work = fixture()
  act(() => { work.result.current.review(proposal) })
  const original = work.result.current.command
  expect(fetchCSRF).not.toHaveBeenCalled()
  expect(sendOwnedCommand).not.toHaveBeenCalled()
  let release: ((value: OwnedResult) => void) | undefined
  vi.mocked(sendOwnedCommand).mockImplementation(() => new Promise((resolve) => { release = resolve }))
  let pending: Promise<void> | undefined
  await act(async () => { pending = work.result.current.confirm(); await work.result.current.confirm() })
  expect(sendOwnedCommand).toHaveBeenCalledTimes(1)
  expect(work.client.getMutationCache().getAll()[0]?.options).toMatchObject({ retry: false, networkMode: 'always', gcTime: 0 })
  await act(async () => { release?.(reply); await pending })
  expect(work.onConfirmed).toHaveBeenCalledWith(original, reply)
  for (const key of keys) {
    expect(work.client.getQueryState(key)?.isInvalidated).toBe(true)
    expect(work.client.getQueryData(key)).toEqual({ revision: 99, current: true })
  }
  expect(work.client.getQueryState(['locations', 'tree'])?.isInvalidated).toBe(false)
  expect(work.client.getQueryState(['auth', 'me'])?.isInvalidated).toBe(false)
  expect(work.result.current.canLeave()).toBe(true)
  work.dispose()
})

it.each(['INVALID_RESPONSE', 'BARCODE_CONFLICT', 'ALIAS_CONFLICT', 'REVISION_CONFLICT'] as const)('retains the original command after sent %s, never replaying on reconnect', async (code) => {
  const work = fixture()
  vi.mocked(sendOwnedCommand).mockRejectedValueOnce(new ApiError(code, { status: code === 'INVALID_RESPONSE' ? 200 : 409 }))
  act(() => { work.result.current.review(proposal) })
  const original = work.result.current.command
  await act(async () => { await work.result.current.confirm() })
  expect(work.result.current.phase).toBe(code === 'INVALID_RESPONSE' ? 'uncertain' : 'rejected')
  expect(work.result.current.command).toBe(original)
  for (const key of keys) expect(work.client.getQueryState(key)?.isInvalidated).toBe(true)
  await act(async () => { window.dispatchEvent(new Event('online')) })
  expect(sendOwnedCommand).toHaveBeenCalledTimes(1)
  expect(work.onConfirmed).not.toHaveBeenCalled()
  work.dispose()
})

it('preserves original row/owner context after current reads remove a genuinely committed deleted target', async () => {
  const work = fixture()
  const ack = { ok: true, operation_id: 'event', revision: 3 } as const
  vi.mocked(sendOwnedCommand).mockRejectedValueOnce(new ApiError('INVALID_RESPONSE')).mockResolvedValueOnce(ack)
  act(() => { work.result.current.review({ kind: 'delete-alias', owner, subject }) })
  const original = work.result.current.command
  await act(async () => { await work.result.current.confirm() })
  work.client.removeQueries({ queryKey: ['catalog', 'owned', owner.id] })
  act(() => {
    work.result.current.dismiss()
    work.result.current.review({ kind: 'create-alias', owner, body: { alias: 'Must not replace original' } })
    window.dispatchEvent(new Event('online'))
  })
  expect(work.result.current.command).toBe(original)
  expect(sendOwnedCommand).toHaveBeenCalledTimes(1)
  act(() => { work.result.current.reopen() })
  vi.mocked(fetchCSRF).mockResolvedValueOnce('fresh-csrf')
  await act(async () => { await work.result.current.confirm() })
  expect(sendOwnedCommand).toHaveBeenNthCalledWith(1, original, 'csrf', expect.any(AbortSignal))
  expect(sendOwnedCommand).toHaveBeenNthCalledWith(2, original, 'fresh-csrf', expect.any(AbortSignal))
  expect(work.onConfirmed).toHaveBeenCalledWith(original, ack)
  work.dispose()
})

it('does not dispatch or invalidate after a pre-send request-token failure', async () => {
  const work = fixture()
  vi.mocked(fetchCSRF).mockRejectedValueOnce(new ApiError('NETWORK_ERROR'))
  act(() => { work.result.current.review(proposal) })
  await act(async () => { await work.result.current.confirm() })
  expect(work.result.current.phase).toBe('rejected')
  expect(sendOwnedCommand).not.toHaveBeenCalled()
  for (const key of keys) expect(work.client.getQueryState(key)?.isInvalidated).toBe(false)
  work.dispose()
})

it('aborts a sent write without accepting a late reply or creating a new reconnect mutation', async () => {
  const work = fixture()
  let release: ((value: OwnedResult) => void) | undefined
  let requestSignal: AbortSignal | undefined
  vi.mocked(sendOwnedCommand).mockImplementation((_command, _csrf, signal) => { requestSignal = signal; return new Promise((resolve) => { release = resolve }) })
  act(() => { work.result.current.review(proposal) })
  let pending: Promise<void> | undefined
  await act(async () => { pending = work.result.current.confirm() })
  act(() => { window.dispatchEvent(new Event('offline')) })
  expect(requestSignal?.aborted).toBe(true)
  await act(async () => { release?.(reply); await pending })
  expect(work.result.current.phase).toBe('uncertain')
  for (const key of keys) expect(work.client.getQueryState(key)?.isInvalidated).toBe(true)
  await act(async () => { window.dispatchEvent(new Event('online')) })
  expect(sendOwnedCommand).toHaveBeenCalledTimes(1)
  expect(work.onConfirmed).not.toHaveBeenCalled()
  work.dispose()
})

it.each([false, true])('purges volatile owned context after session loss, mandatory=%s', async (mandatory) => {
  const work = fixture()
  vi.mocked(sendOwnedCommand).mockRejectedValueOnce(new ApiError(mandatory ? 'PASSWORD_CHANGE_REQUIRED' : 'AUTHENTICATION_REQUIRED', { status: mandatory ? 403 : 401 }))
  act(() => { work.result.current.review(proposal) })
  await act(async () => { await work.result.current.confirm() })
  expect(work.result.current.command).toBeUndefined()
  expect(work.onSessionLost).toHaveBeenCalledWith(mandatory)
  expect(work.result.current.open).toBe(false)
  expect(work.result.current.canLeave()).toBe(true)
  work.dispose()
})
