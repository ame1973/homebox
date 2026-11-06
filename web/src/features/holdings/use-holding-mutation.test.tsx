import type { ReactNode } from 'react'
import { QueryClientProvider } from '@tanstack/react-query'
import { act, renderHook } from '@testing-library/react'
import { beforeEach, expect, it, vi } from 'vitest'
import { fetchCSRF } from '@/lib/api/auth'
import { ApiError } from '@/lib/api/client'
import { createQueryClient } from '@/lib/query'
import { sendHoldingCommand, type HoldingCommandInput, type HoldingResult } from './commands'
import { useHoldingMutation } from './use-holding-mutation'

vi.mock('@/lib/api/auth', () => ({ fetchCSRF: vi.fn() }))
vi.mock('./commands', async (original) => ({ ...await original<typeof import('./commands')>(), sendHoldingCommand: vi.fn() }))
const owner = { id: 'item', revision: 7, name: 'Reviewed quantity', brand: null, model: '', category_id: null, description: '', notes: '', tracking_mode: 'quantity', unit_code: 'kg', base_unit: 'g', scale_factor: 1000, status: 'active' } as const
const source = { id: 'source', revision: 3, name: 'Source', code: 'SRC', kind: 'other', parent_id: null, sort_order: 0, description: '', status: 'active' } as const
const target = { ...source, id: 'target', name: 'Target', code: 'DST' }
const balance = { item_id: owner.id, location_id: source.id, revision: 2, quantity_base: 1500, unit_code: 'kg', base_unit: 'g', scale_factor: 1000 } as const
const proposal: HoldingCommandInput = { kind: 'transfer', owner, balances: [balance], source: [source], target: [target], body: { item_id: owner.id, source_location_id: source.id, target_location_id: target.id, quantity_base: 1500 } }
const reply: HoldingResult = { ...balance, location_id: target.id, revision: 1 }
const keys = [['holdings', 'item', owner.id], ['holdings', 'unit', 'unit'], ['holdings', 'stock', {}], ['catalog', 'item', owner.id], ['catalog', 'items'], ['locations', 'tree']] as const
beforeEach(() => {
  vi.mocked(fetchCSRF).mockReset().mockResolvedValue('csrf')
  vi.mocked(sendHoldingCommand).mockReset().mockResolvedValue(reply)
})
function fixture() {
  const client = createQueryClient(), onConfirmed = vi.fn(), onSessionLost = vi.fn()
  const hook = renderHook(() => useHoldingMutation(onConfirmed, onSessionLost), { wrapper: ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider> })
  for (const key of [...keys, ['auth', 'me'], ['taxonomy', 'categories']]) client.setQueryData(key, { revision: 99, current: true })
  return { ...hook, client, onConfirmed, onSessionLost, dispose: () => { hook.unmount(); client.clear() } }
}

it('requires review and one confirmation, invalidating only dependent reads without installing an old independent Stock', async () => {
  const work = fixture()
  act(() => { work.result.current.review(proposal) })
  const original = work.result.current.command
  expect(fetchCSRF).not.toHaveBeenCalled()
  expect(sendHoldingCommand).not.toHaveBeenCalled()
  let release: ((reply: HoldingResult) => void) | undefined
  vi.mocked(sendHoldingCommand).mockImplementation(() => new Promise((resolve) => { release = resolve }))
  let pending: Promise<void> | undefined
  await act(async () => { pending = work.result.current.confirm(); await work.result.current.confirm() })
  expect(sendHoldingCommand).toHaveBeenCalledTimes(1)
  expect(work.client.getMutationCache().getAll()[0]?.options).toMatchObject({ networkMode: 'always', retry: false, gcTime: 0 })
  await act(async () => { release?.(reply); await pending })
  expect(work.onConfirmed).toHaveBeenCalledWith(original, reply)
  for (const key of keys) {
    expect(work.client.getQueryState(key)?.isInvalidated).toBe(true)
    expect(work.client.getQueryData(key)).toEqual({ revision: 99, current: true })
  }
  expect(work.client.getQueryState(['auth', 'me'])?.isInvalidated).toBe(false)
  expect(work.client.getQueryState(['taxonomy', 'categories'])?.isInvalidated).toBe(false)
  expect(work.result.current.canLeave()).toBe(true)
  work.dispose()
})
it.each(['NETWORK_ERROR', 'REQUEST_TIMEOUT', 'INVALID_RESPONSE', 'REVISION_CONFLICT', 'INSUFFICIENT_STOCK', 'QUANTITY_OVERFLOW', 'LOCATION_ARCHIVED', 'IDEMPOTENCY_EXPIRED'] as const)('retains original Stock interpretation/key/counts after sent %s without rebase/replay', async (code) => {
  const work = fixture()
  const uncertain = ['NETWORK_ERROR', 'REQUEST_TIMEOUT', 'INVALID_RESPONSE', 'IDEMPOTENCY_EXPIRED'].includes(code)
  vi.mocked(sendHoldingCommand).mockRejectedValueOnce(new ApiError(code, { status: uncertain ? undefined : 409 }))
  act(() => { work.result.current.review(proposal) })
  const original = work.result.current.command
  await act(async () => { await work.result.current.confirm() })
  expect(work.result.current.phase).toBe(uncertain ? 'uncertain' : 'rejected')
  expect(work.result.current.command).toBe(original)
  for (const key of keys) expect(work.client.getQueryState(key)?.isInvalidated).toBe(true)
  work.client.setQueryData(['holdings', 'item', owner.id], { revision: 100, source_quantity_base: 0 })
  await act(async () => { window.dispatchEvent(new Event('online')) })
  expect(sendHoldingCommand).toHaveBeenCalledTimes(1)
  expect(work.result.current.command).toMatchObject({ owner, balances: [balance], body: { quantity_base: 1500 } })
  expect(work.onConfirmed).not.toHaveBeenCalled()
  work.dispose()
})
it('retains a genuinely removed Unit deletion for only explicit identical-key reconciliation', async () => {
  const work = fixture()
  const unit = { id: 'unit', item_id: owner.id, revision: 2, location_id: source.id, asset_code: 'Retained AST', serial_number: null, notes: '', status: 'stored' } as const
  const deletion: HoldingCommandInput = { kind: 'delete-unit', owner: { ...owner, tracking_mode: 'individual' }, subject: unit, location: [source] }
  const ack = { ok: true, operation_id: 'event', revision: 3 } as const
  vi.mocked(sendHoldingCommand).mockRejectedValueOnce(new ApiError('INVALID_RESPONSE')).mockResolvedValueOnce(ack)
  act(() => { work.result.current.review(deletion) })
  const original = work.result.current.command
  await act(async () => { await work.result.current.confirm() })
  work.client.removeQueries({ queryKey: ['holdings'] })
  act(() => { work.result.current.dismiss(); work.result.current.review(proposal); window.dispatchEvent(new Event('online')) })
  expect(work.result.current.command).toBe(original)
  expect(sendHoldingCommand).toHaveBeenCalledTimes(1)
  act(() => { work.result.current.reopen() })
  vi.mocked(fetchCSRF).mockResolvedValueOnce('fresh-csrf')
  await act(async () => { await work.result.current.confirm() })
  expect(sendHoldingCommand).toHaveBeenNthCalledWith(1, original, 'csrf', expect.any(AbortSignal))
  expect(sendHoldingCommand).toHaveBeenNthCalledWith(2, original, 'fresh-csrf', expect.any(AbortSignal))
  expect(work.onConfirmed).toHaveBeenCalledWith(original, ack)
  work.dispose()
})
it('does not dispatch or invalidate when the request token cannot be obtained', async () => {
  const work = fixture()
  vi.mocked(fetchCSRF).mockRejectedValueOnce(new ApiError('NETWORK_ERROR'))
  act(() => { work.result.current.review(proposal) })
  await act(async () => { await work.result.current.confirm() })
  expect(work.result.current.phase).toBe('rejected')
  expect(sendHoldingCommand).not.toHaveBeenCalled()
  for (const key of keys) expect(work.client.getQueryState(key)?.isInvalidated).toBe(false)
  work.dispose()
})
it('interrupts a sent holding write without accepting late success or replaying on reconnect', async () => {
  const work = fixture()
  let release: ((reply: HoldingResult) => void) | undefined
  let signal: AbortSignal | undefined
  vi.mocked(sendHoldingCommand).mockImplementation((_command, _csrf, requestSignal) => { signal = requestSignal; return new Promise((resolve) => { release = resolve }) })
  act(() => { work.result.current.review(proposal) })
  let pending: Promise<void> | undefined
  await act(async () => { pending = work.result.current.confirm() })
  act(() => { window.dispatchEvent(new Event('offline')) })
  expect(signal?.aborted).toBe(true)
  await act(async () => { release?.(reply); await pending })
  expect(work.result.current.phase).toBe('uncertain')
  for (const key of keys) expect(work.client.getQueryState(key)?.isInvalidated).toBe(true)
  await act(async () => { window.dispatchEvent(new Event('online')) })
  expect(sendHoldingCommand).toHaveBeenCalledTimes(1)
  expect(work.onConfirmed).not.toHaveBeenCalled()
  work.dispose()
})
it.each([false, true])('purges volatile holding review context after session loss, mandatory=%s', async (mandatory) => {
  const work = fixture()
  vi.mocked(sendHoldingCommand).mockRejectedValueOnce(new ApiError(mandatory ? 'PASSWORD_CHANGE_REQUIRED' : 'AUTHENTICATION_REQUIRED', { status: mandatory ? 403 : 401 }))
  act(() => { work.result.current.review(proposal) })
  await act(async () => { await work.result.current.confirm() })
  expect(work.result.current.command).toBeUndefined()
  expect(work.result.current.open).toBe(false)
  expect(work.onSessionLost).toHaveBeenCalledWith(mandatory)
  expect(work.result.current.canLeave()).toBe(true)
  work.dispose()
})
