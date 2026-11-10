import type { ReactNode } from 'react'
import { QueryClientProvider } from '@tanstack/react-query'
import { act, renderHook } from '@testing-library/react'
import { beforeEach, expect, it, vi } from 'vitest'
import { fetchCSRF } from '@/lib/api/auth'
import { ApiError } from '@/lib/api/client'
import { createQueryClient } from '@/lib/query'
import { sendCorrectionCommand, type CorrectionCommandInput, type CorrectionResult } from './commands'
import { useCorrectionMutation } from './use-correction-mutation'

vi.mock('@/lib/api/auth', () => ({ fetchCSRF: vi.fn() }))
vi.mock('./commands', async (original) => ({ ...await original<typeof import('./commands')>(), sendCorrectionCommand: vi.fn() }))
const item = { id: 'item', revision: 7, name: 'Original', tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1, brand: null, model: '', category_id: null, description: '', notes: '', status: 'active' } as const
const unit = { id: 'unit', item_id: item.id, revision: 3, status: 'disposed', asset_code: 'Physical', location_id: null, serial_number: null, notes: '' } as const
const purchase = { id: 'purchase', revision: 12, merchant_name: 'Merchant', purchased_at: '2026-11-07T00:00:00.000000001Z', currency: 'JPY', receipt_note: '' } as const
const line = { id: 'line', revision: 4, purchase_id: purchase.id, item_id: item.id, item_unit_id: unit.id, quantity_base: 1, paid_minor: 0, note: '' } as const
const source = { purchase, line, item, lines: [line], corrections: [], originalUnit: { kind: 'resolved', unit }, replacement: { kind: 'none' } } as const
const asset = { item_unit_id: unit.id, revision: 8, cost_basis: 'manual', currency: 'JPY', manual_acquisition_minor: 100, in_service_at: null, retired_at: null, estimated_residual_minor: 20, usage_unit: 'times' } as const
const disposalData = { item_unit_id: unit.id, kind: 'sold', currency: 'JPY', realized_proceeds_minor: 0, disposed_at: purchase.purchased_at, note: '' } as const
const disposal = { item_unit_id: unit.id, revision: 9, original: disposalData, current: disposalData } as const
const proposals: CorrectionCommandInput[] = [
  { kind: 'correct-purchase', source, unit: null, body: { line_id: line.id, line_revision: line.revision, item_revision: item.revision, action: 'void', replacement: null, reason: 'Void attribution' } },
  { kind: 'correct-disposal', source: { unit, item, asset, disposal, corrections: [] }, body: { asset_revision: asset.revision, action: 'void', replacement: null, reason: 'Void financial decision' } },
]
const reply: CorrectionResult = { id: 'entry', revision: 1, purchase_id: purchase.id, action: 'void', reason: 'Void attribution', created_at: purchase.purchased_at, before: line, after: null }
const keys = [['purchases', 'detail', purchase.id], ['purchases', 'corrections', purchase.id], ['catalog', 'item', item.id], ['catalog', 'items'], ['holdings', 'unit', unit.id], ['holdings', 'item', item.id], ['assets', 'detail', unit.id], ['assets', 'costs', unit.id], ['media', 'owner', 'unit', unit.id], ['media', 'membership', 'photo']] as const
const unrelated = [['locations', 'tree'], ['auth', 'me'], ['taxonomy', 'categories']] as const
beforeEach(() => { vi.mocked(fetchCSRF).mockReset().mockResolvedValue('csrf'); vi.mocked(sendCorrectionCommand).mockReset().mockResolvedValue(reply) })
function fixture() {
  const client = createQueryClient(), onConfirmed = vi.fn(), onSessionLost = vi.fn()
  const hook = renderHook(() => useCorrectionMutation(onConfirmed, onSessionLost), { wrapper: ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider> })
  for (const key of [...keys, ...unrelated]) client.setQueryData(key, { revision: 99, current: true })
  return { ...hook, client, onConfirmed, onSessionLost, dispose: () => { hook.unmount(); client.clear() } }
}

it.each(proposals)('requires single-flight foreground confirmation for $kind and invalidates only financial dependencies', async (proposal) => {
  const work = fixture()
  act(() => { work.result.current.review(proposal) })
  const original = work.result.current.command
  expect(fetchCSRF).not.toHaveBeenCalled(); expect(sendCorrectionCommand).not.toHaveBeenCalled()
  let release: ((value: CorrectionResult) => void) | undefined
  vi.mocked(sendCorrectionCommand).mockImplementation(() => new Promise((resolve) => { release = resolve }))
  let pending: Promise<void> | undefined
  await act(async () => { pending = work.result.current.confirm(); await work.result.current.confirm() })
  expect(sendCorrectionCommand).toHaveBeenCalledTimes(1)
  expect(work.client.getMutationCache().getAll()[0]?.options).toMatchObject({ retry: false, networkMode: 'always', gcTime: 0 })
  await act(async () => { release?.(reply); await pending })
  expect(work.onConfirmed).toHaveBeenCalledWith(original, reply)
  for (const key of keys) { expect(work.client.getQueryState(key)?.isInvalidated).toBe(true); expect(work.client.getQueryData(key)).toEqual({ revision: 99, current: true }) }
  for (const key of unrelated) expect(work.client.getQueryState(key)?.isInvalidated).toBe(false)
  expect(work.result.current.canLeave()).toBe(true); work.dispose()
})
it.each(proposals)('retains an uncertain $kind original key after source removal until explicit reconciliation', async (proposal) => {
  const work = fixture()
  vi.mocked(sendCorrectionCommand).mockRejectedValueOnce(new ApiError('INVALID_RESPONSE')).mockResolvedValueOnce(reply)
  act(() => { work.result.current.review(proposal) })
  const original = work.result.current.command
  await act(async () => { await work.result.current.confirm() })
  expect(work.result.current.phase).toBe('uncertain')
  for (const root of ['assets', 'catalog', 'holdings', 'purchases']) work.client.removeQueries({ queryKey: [root] })
  act(() => { work.result.current.dismiss(); work.result.current.review(proposals[0]!); window.dispatchEvent(new Event('online')) })
  expect(work.result.current.command).toBe(original); expect(sendCorrectionCommand).toHaveBeenCalledTimes(1)
  act(() => { work.result.current.reopen() }); vi.mocked(fetchCSRF).mockResolvedValueOnce('fresh-csrf')
  await act(async () => { await work.result.current.confirm() })
  expect(sendCorrectionCommand).toHaveBeenNthCalledWith(1, original, 'csrf', expect.any(AbortSignal))
  expect(sendCorrectionCommand).toHaveBeenNthCalledWith(2, original, 'fresh-csrf', expect.any(AbortSignal))
  expect(work.onConfirmed).toHaveBeenCalledWith(original, reply); work.dispose()
})
it('withholds a late success on interruption and never replays on reconnect', async () => {
  const work = fixture()
  let release: ((value: CorrectionResult) => void) | undefined, signal: AbortSignal | undefined
  vi.mocked(sendCorrectionCommand).mockImplementation((_command, _csrf, requestSignal) => { signal = requestSignal; return new Promise((resolve) => { release = resolve }) })
  act(() => { work.result.current.review(proposals[0]!) })
  let pending: Promise<void> | undefined
  await act(async () => { pending = work.result.current.confirm() })
  act(() => { window.dispatchEvent(new Event('offline')) }); expect(signal?.aborted).toBe(true)
  await act(async () => { release?.(reply); await pending })
  expect(work.result.current.phase).toBe('uncertain'); expect(work.onConfirmed).not.toHaveBeenCalled()
  await act(async () => { window.dispatchEvent(new Event('online')) })
  expect(sendCorrectionCommand).toHaveBeenCalledTimes(1); work.dispose()
})
it('does not dispatch or invalidate when the CSRF read fails before sending', async () => {
  const work = fixture()
  vi.mocked(fetchCSRF).mockRejectedValueOnce(new ApiError('NETWORK_ERROR'))
  act(() => { work.result.current.review(proposals[0]!) })
  await act(async () => { await work.result.current.confirm() })
  expect(work.result.current.phase).toBe('rejected'); expect(sendCorrectionCommand).not.toHaveBeenCalled()
  for (const key of [...keys, ...unrelated]) expect(work.client.getQueryState(key)?.isInvalidated).toBe(false)
  work.dispose()
})
it.each([false, true])('purges volatile correction on session loss, mandatory=%s', async (mandatory) => {
  const work = fixture()
  vi.mocked(sendCorrectionCommand).mockRejectedValueOnce(new ApiError(mandatory ? 'PASSWORD_CHANGE_REQUIRED' : 'AUTHENTICATION_REQUIRED', { status: mandatory ? 403 : 401 }))
  act(() => { work.result.current.review(proposals[1]!) })
  await act(async () => { await work.result.current.confirm() })
  expect(work.result.current.command).toBeUndefined(); expect(work.result.current.open).toBe(false); expect(work.result.current.canLeave()).toBe(true)
  expect(work.onSessionLost).toHaveBeenCalledWith(mandatory); work.dispose()
})
