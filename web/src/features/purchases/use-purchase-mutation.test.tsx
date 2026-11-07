import type { ReactNode } from 'react'
import { QueryClientProvider } from '@tanstack/react-query'
import { act, renderHook } from '@testing-library/react'
import { beforeEach, expect, it, vi } from 'vitest'
import { fetchCSRF } from '@/lib/api/auth'
import { ApiError } from '@/lib/api/client'
import { createQueryClient } from '@/lib/query'
import { sendPurchaseCommand, type PurchaseCommandInput, type PurchaseResult } from './commands'
import { usePurchaseMutation } from './use-purchase-mutation'

vi.mock('@/lib/api/auth', () => ({ fetchCSRF: vi.fn() }))
vi.mock('./commands', async (original) => ({ ...await original<typeof import('./commands')>(), sendPurchaseCommand: vi.fn() }))
const item = { id: 'item', revision: 7, name: 'Original quantity', brand: null, model: '', category_id: null, description: '', notes: '', tracking_mode: 'quantity', unit_code: 'kg', base_unit: 'g', scale_factor: 1000, status: 'active' } as const
const purchase = { id: 'purchase', revision: 12, merchant_name: 'Reviewed merchant', purchased_at: '2026-11-06T00:00:00.000000001Z', currency: 'JPY', receipt_note: '' } as const
const proposal: PurchaseCommandInput = { kind: 'create-line', purchase, item, unit: null, body: { item_id: item.id, item_revision: 7, item_unit_id: null, quantity_base: 1500, paid_minor: 0 } }
const reply: PurchaseResult = { id: 'line', revision: 1, purchase_id: purchase.id, item_id: item.id, item_unit_id: null, quantity_base: 1500, paid_minor: 0, note: '' }
const keys = [['purchases', 'detail', purchase.id], ['purchases', 'allocation', purchase.id, item.id], ['purchases', 'item', item.id], ['purchases', 'list', { currency: 'JPY' }], ['catalog', 'item', item.id], ['catalog', 'items'], ['holdings', 'item', item.id], ['holdings', 'stock', { item_id: item.id }], ['assets', 'detail', 'unit'], ['assets', 'costs', 'unit']] as const
const unrelated = [['auth', 'me'], ['taxonomy', 'categories'], ['locations', 'tree']] as const
beforeEach(() => {
  vi.mocked(fetchCSRF).mockReset().mockResolvedValue('csrf')
  vi.mocked(sendPurchaseCommand).mockReset().mockResolvedValue(reply)
})
function fixture() {
  const client = createQueryClient(), onConfirmed = vi.fn(), onSessionLost = vi.fn()
  const hook = renderHook(() => usePurchaseMutation(onConfirmed, onSessionLost), { wrapper: ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider> })
  for (const key of [...keys, ...unrelated]) client.setQueryData(key, { revision: 99, current: true })
  return { ...hook, client, onConfirmed, onSessionLost, dispose: () => { hook.unmount(); client.clear() } }
}
it('requires explicit review and single-flight confirmation, invalidating dependent reads without installing an old Line', async () => {
  const work = fixture()
  act(() => { work.result.current.review(proposal) })
  const original = work.result.current.command
  expect(fetchCSRF).not.toHaveBeenCalled(); expect(sendPurchaseCommand).not.toHaveBeenCalled()
  let release: ((reply: PurchaseResult) => void) | undefined
  vi.mocked(sendPurchaseCommand).mockImplementation(() => new Promise((resolve) => { release = resolve }))
  let pending: Promise<void> | undefined
  await act(async () => { pending = work.result.current.confirm(); await work.result.current.confirm() })
  expect(sendPurchaseCommand).toHaveBeenCalledTimes(1)
  expect(work.client.getMutationCache().getAll()[0]?.options).toMatchObject({ networkMode: 'always', retry: false, gcTime: 0 })
  await act(async () => { release?.(reply); await pending })
  expect(work.onConfirmed).toHaveBeenCalledWith(original, reply)
  for (const key of keys) { expect(work.client.getQueryState(key)?.isInvalidated).toBe(true); expect(work.client.getQueryData(key)).toEqual({ revision: 99, current: true }) }
  for (const key of unrelated) expect(work.client.getQueryState(key)?.isInvalidated).toBe(false)
  expect(work.result.current.canLeave()).toBe(true)
  work.dispose()
})
it.each(['NETWORK_ERROR', 'REQUEST_TIMEOUT', 'INVALID_RESPONSE', 'REVISION_CONFLICT', 'PURCHASE_CURRENCY_LOCKED', 'PURCHASE_CURRENCY_CONFLICT', 'ASSET_COST_BASIS_CONFLICT', 'ASSET_LIFECYCLE_REQUIRED', 'ASSET_TOTAL_OVERFLOW', 'IDEMPOTENCY_EXPIRED'] as const)('retains exact original money, measurement, nullable intent and key after sent %s', async (code) => {
  const work = fixture()
  const uncertain = ['NETWORK_ERROR', 'REQUEST_TIMEOUT', 'INVALID_RESPONSE', 'IDEMPOTENCY_EXPIRED'].includes(code)
  vi.mocked(sendPurchaseCommand).mockRejectedValueOnce(new ApiError(code, { status: uncertain ? undefined : 409 }))
  act(() => { work.result.current.review(proposal) })
  const original = work.result.current.command
  await act(async () => { await work.result.current.confirm() })
  expect(work.result.current.phase).toBe(uncertain ? 'uncertain' : 'rejected')
  expect(work.result.current.command).toBe(original)
  for (const key of keys) expect(work.client.getQueryState(key)?.isInvalidated).toBe(true)
  work.client.setQueryData(['purchases', 'detail', purchase.id], { revision: 100, currency: 'USD' })
  work.client.setQueryData(['catalog', 'item', item.id], { revision: 100, base_unit: 'ml' })
  await act(async () => { window.dispatchEvent(new Event('online')) })
  expect(sendPurchaseCommand).toHaveBeenCalledTimes(1)
  expect(work.result.current.command).toMatchObject({ purchase, item, unit: null, body: { item_revision: 7, quantity_base: 1500, paid_minor: 0 } })
  expect(work.onConfirmed).not.toHaveBeenCalled()
  work.dispose()
})
it('preserves an uncertain original command after current reads disappear and reconciles only an explicit same-key confirmation', async () => {
  const work = fixture()
  vi.mocked(sendPurchaseCommand).mockRejectedValueOnce(new ApiError('INVALID_RESPONSE')).mockResolvedValueOnce(reply)
  act(() => { work.result.current.review(proposal) })
  const original = work.result.current.command
  await act(async () => { await work.result.current.confirm() })
  work.client.removeQueries({ queryKey: ['purchases'] }); work.client.removeQueries({ queryKey: ['catalog'] })
  act(() => { work.result.current.dismiss(); work.result.current.review({ kind: 'create-purchase', body: { merchant_name: 'Different', purchased_at: purchase.purchased_at, currency: 'USD' } }); window.dispatchEvent(new Event('online')) })
  expect(work.result.current.command).toBe(original); expect(sendPurchaseCommand).toHaveBeenCalledTimes(1)
  act(() => { work.result.current.reopen() })
  vi.mocked(fetchCSRF).mockResolvedValueOnce('fresh-csrf')
  await act(async () => { await work.result.current.confirm() })
  expect(sendPurchaseCommand).toHaveBeenNthCalledWith(1, original, 'csrf', expect.any(AbortSignal))
  expect(sendPurchaseCommand).toHaveBeenNthCalledWith(2, original, 'fresh-csrf', expect.any(AbortSignal))
  expect(work.onConfirmed).toHaveBeenCalledWith(original, reply)
  work.dispose()
})
it('does not dispatch, invalidate or replay when CSRF cannot be obtained', async () => {
  const work = fixture()
  vi.mocked(fetchCSRF).mockRejectedValueOnce(new ApiError('NETWORK_ERROR'))
  act(() => { work.result.current.review(proposal) })
  await act(async () => { await work.result.current.confirm() })
  expect(work.result.current.phase).toBe('rejected'); expect(sendPurchaseCommand).not.toHaveBeenCalled()
  for (const key of keys) expect(work.client.getQueryState(key)?.isInvalidated).toBe(false)
  work.dispose()
})
it('interrupts a sent financial write, discards late success and never resubmits on reconnect', async () => {
  const work = fixture()
  let release: ((reply: PurchaseResult) => void) | undefined, signal: AbortSignal | undefined
  vi.mocked(sendPurchaseCommand).mockImplementation((_command, _csrf, requestSignal) => { signal = requestSignal; return new Promise((resolve) => { release = resolve }) })
  act(() => { work.result.current.review(proposal) })
  let pending: Promise<void> | undefined
  await act(async () => { pending = work.result.current.confirm() })
  act(() => { window.dispatchEvent(new Event('offline')) })
  expect(signal?.aborted).toBe(true)
  await act(async () => { release?.(reply); await pending })
  expect(work.result.current.phase).toBe('uncertain')
  for (const key of keys) expect(work.client.getQueryState(key)?.isInvalidated).toBe(true)
  await act(async () => { window.dispatchEvent(new Event('online')) })
  expect(sendPurchaseCommand).toHaveBeenCalledTimes(1); expect(work.onConfirmed).not.toHaveBeenCalled()
  work.dispose()
})
it.each([false, true])('purges volatile financial context on session loss without forwarding amounts, mandatory=%s', async (mandatory) => {
  const work = fixture()
  vi.mocked(sendPurchaseCommand).mockRejectedValueOnce(new ApiError(mandatory ? 'PASSWORD_CHANGE_REQUIRED' : 'AUTHENTICATION_REQUIRED', { status: mandatory ? 403 : 401 }))
  act(() => { work.result.current.review(proposal) })
  await act(async () => { await work.result.current.confirm() })
  expect(work.result.current.command).toBeUndefined(); expect(work.result.current.open).toBe(false)
  expect(work.onSessionLost).toHaveBeenCalledWith(mandatory); expect(work.result.current.canLeave()).toBe(true)
  work.dispose()
})
