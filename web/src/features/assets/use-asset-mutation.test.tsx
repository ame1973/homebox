import type { ReactNode } from 'react'
import { QueryClientProvider } from '@tanstack/react-query'
import { act, renderHook } from '@testing-library/react'
import { beforeEach, expect, it, vi } from 'vitest'
import { fetchCSRF } from '@/lib/api/auth'
import { ApiError } from '@/lib/api/client'
import { createQueryClient } from '@/lib/query'
import { sendAssetCommand, type AssetCommandInput, type AssetResult } from './commands'
import { useAssetMutation } from './use-asset-mutation'

vi.mock('@/lib/api/auth', () => ({ fetchCSRF: vi.fn() }))
vi.mock('./commands', async (original) => ({ ...await original<typeof import('./commands')>(), sendAssetCommand: vi.fn() }))
const item = { id: 'item', revision: 11, name: 'Original owner', tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1, brand: null, model: '', category_id: null, description: '', notes: '', status: 'active' } as const
const unit = { id: 'unit', item_id: item.id, revision: 13, asset_code: 'AST', status: 'stored', location_id: 'place', serial_number: null, notes: 'Original physical source' } as const
const asset = { item_unit_id: unit.id, revision: 7, cost_basis: 'manual', currency: 'JPY', manual_acquisition_minor: 100, in_service_at: null, retired_at: null, estimated_residual_minor: null, usage_unit: 'times' } as const
const body = { delta: 1, usage_unit: 'times', event_type: 'use', occurred_at: '2026-11-07T00:00:00.123456789Z' } as const
const source = { unit, item, asset }
const proposal: AssetCommandInput = { kind: 'record-usage', source: { ...source, kind: 'usage', usage: [] }, body }
const proposals: AssetCommandInput[] = [
  { kind: 'put-profile', source, body: { item_revision: 11, asset_revision: 7, cost_basis: 'manual', currency: 'JPY', manual_acquisition_minor: 100, in_service_at: null, retired_at: null, estimated_residual_minor: null, usage_unit: 'times' } }, proposal,
  { kind: 'record-cost', source: { ...source, kind: 'costs', costs: [] }, body: { amount_minor: 1, currency: 'JPY', kind: 'repair', direction: 'expense', occurred_at: body.occurred_at } },
  { kind: 'retire-asset', source: { ...source, disposal: null }, body: { unit_revision: 13, retired_at: body.occurred_at } },
  { kind: 'dispose-asset', source: { ...source, disposal: null }, body: { unit_revision: 13, kind: 'other', currency: 'JPY', realized_proceeds_minor: 0, disposed_at: body.occurred_at } },
]
const reply: AssetResult = { ...body, id: 'new-event', revision: 1, item_unit_id: unit.id, note: '', correction_of: null }
const keys = [['assets', 'profile', unit.id], ['assets', 'usage', unit.id], ['assets', 'costs', unit.id], ['assets', 'detail', unit.id], ['assets', 'list', { currency: 'JPY' }],
  ['catalog', 'item', item.id], ['catalog', 'items'], ['holdings', 'unit', unit.id], ['holdings', 'item', item.id], ['purchases', 'item', item.id], ['purchases', 'allocation', 'purchase', item.id]] as const
const location = ['locations', 'tree'] as const
const unrelated = [['auth', 'me'], ['taxonomy', 'categories'], ['taxonomy', 'tags']] as const
beforeEach(() => { vi.mocked(fetchCSRF).mockReset().mockResolvedValue('csrf'); vi.mocked(sendAssetCommand).mockReset().mockResolvedValue(reply) })
function fixture() {
  const client = createQueryClient(), onConfirmed = vi.fn(), onSessionLost = vi.fn()
  const hook = renderHook(() => useAssetMutation(onConfirmed, onSessionLost), { wrapper: ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider> })
  for (const key of [...keys, location, ...unrelated]) client.setQueryData(key, { revision: 99, current: true })
  return { ...hook, client, onConfirmed, onSessionLost, dispose: () => { hook.unmount(); client.clear() } }
}

it('requires review and one foreground confirmation without installing an old financial reply or touching unrelated scopes', async () => {
  const work = fixture()
  act(() => { work.result.current.review(proposal) })
  const original = work.result.current.command
  expect(fetchCSRF).not.toHaveBeenCalled(); expect(sendAssetCommand).not.toHaveBeenCalled()
  let release: ((result: AssetResult) => void) | undefined
  vi.mocked(sendAssetCommand).mockImplementation(() => new Promise((resolve) => { release = resolve }))
  let pending: Promise<void> | undefined
  await act(async () => { pending = work.result.current.confirm(); await work.result.current.confirm() })
  expect(sendAssetCommand).toHaveBeenCalledTimes(1)
  expect(work.client.getMutationCache().getAll()[0]?.options).toMatchObject({ retry: false, networkMode: 'always', gcTime: 0 })
  await act(async () => { release?.(reply); await pending })
  expect(work.onConfirmed).toHaveBeenCalledWith(original, reply)
  for (const key of keys) { expect(work.client.getQueryState(key)?.isInvalidated).toBe(true); expect(work.client.getQueryData(key)).toEqual({ revision: 99, current: true }) }
  for (const key of [location, ...unrelated]) expect(work.client.getQueryState(key)?.isInvalidated).toBe(false)
  expect(work.result.current.canLeave()).toBe(true); work.dispose()
})
it.each(proposals)('resolves sent-success and uncertain $kind invalidations from its frozen physical scope', async (candidate) => {
  for (const failed of [false, true]) {
    const work = fixture()
    if (failed) vi.mocked(sendAssetCommand).mockRejectedValueOnce(new ApiError('NETWORK_ERROR'))
    act(() => { work.result.current.review(candidate) })
    await act(async () => { await work.result.current.confirm() })
    for (const key of keys) expect(work.client.getQueryState(key)?.isInvalidated).toBe(true)
    expect(work.client.getQueryState(location)?.isInvalidated).toBe(candidate.kind === 'retire-asset' || candidate.kind === 'dispose-asset')
    for (const key of unrelated) expect(work.client.getQueryState(key)?.isInvalidated).toBe(false)
    work.dispose()
  }
})
it.each(['retire-asset', 'dispose-asset'] as const)('does not invalidate locations for retained nonphysical %s', async (kind) => {
  const work = fixture()
  const status = kind === 'retire-asset' ? 'retired' : 'disposed'
  const candidate = proposals.find((value) => value.kind === kind)!
  act(() => { work.result.current.review({ ...candidate, source: { ...source, unit: { ...unit, status }, disposal: null } } as AssetCommandInput) })
  await act(async () => { await work.result.current.confirm() })
  expect(work.client.getQueryState(location)?.isInvalidated).toBe(false)
  for (const key of keys) expect(work.client.getQueryState(key)?.isInvalidated).toBe(true)
  work.dispose()
})
it.each(['NETWORK_ERROR', 'REQUEST_TIMEOUT', 'INVALID_RESPONSE', 'REVISION_CONFLICT', 'ASSET_CURRENCY_CONFLICT', 'ASSET_TOTAL_OVERFLOW', 'ASSET_HISTORY_LOCKED', 'ASSET_LIFECYCLE_REQUIRED', 'UNIT_DISPOSED', 'IDEMPOTENCY_EXPIRED', 'IDEMPOTENCY_CONFLICT', 'SERVICE_UNAVAILABLE'] as const)('retains the complete original financial command after sent %s without reconnect replay', async (code) => {
  const work = fixture()
  const uncertain = ['NETWORK_ERROR', 'REQUEST_TIMEOUT', 'INVALID_RESPONSE', 'IDEMPOTENCY_EXPIRED', 'IDEMPOTENCY_CONFLICT', 'SERVICE_UNAVAILABLE'].includes(code)
  vi.mocked(sendAssetCommand).mockRejectedValueOnce(new ApiError(code, { status: code === 'SERVICE_UNAVAILABLE' ? 503 : uncertain ? undefined : 409 }))
  act(() => { work.result.current.review(proposal) })
  const original = work.result.current.command
  await act(async () => { await work.result.current.confirm() })
  expect(work.result.current.phase).toBe(uncertain ? 'uncertain' : 'rejected'); expect(work.result.current.command).toBe(original)
  work.client.setQueryData(['assets', 'profile', unit.id], { revision: 100, currency: 'USD', usage_unit: 'minutes' })
  work.client.setQueryData(['holdings', 'unit', unit.id], { revision: 100, status: 'disposed' })
  await act(async () => { window.dispatchEvent(new Event('online')) })
  expect(sendAssetCommand).toHaveBeenCalledTimes(1); expect(work.result.current.command).toMatchObject(proposal)
  expect(work.onConfirmed).not.toHaveBeenCalled(); work.dispose()
})
it('preserves uncertain original-key reconciliation after all current source caches disappear', async () => {
  const work = fixture()
  vi.mocked(sendAssetCommand).mockRejectedValueOnce(new ApiError('INVALID_RESPONSE')).mockResolvedValueOnce(reply)
  act(() => { work.result.current.review(proposal) })
  const original = work.result.current.command
  await act(async () => { await work.result.current.confirm() })
  for (const root of ['assets', 'catalog', 'holdings', 'purchases']) work.client.removeQueries({ queryKey: [root] })
  act(() => { work.result.current.dismiss(); work.result.current.review(proposals[0]!); window.dispatchEvent(new Event('online')) })
  expect(work.result.current.command).toBe(original); expect(sendAssetCommand).toHaveBeenCalledTimes(1)
  act(() => { work.result.current.reopen() })
  vi.mocked(fetchCSRF).mockResolvedValueOnce('fresh-csrf')
  await act(async () => { await work.result.current.confirm() })
  expect(sendAssetCommand).toHaveBeenNthCalledWith(1, original, 'csrf', expect.any(AbortSignal))
  expect(sendAssetCommand).toHaveBeenNthCalledWith(2, original, 'fresh-csrf', expect.any(AbortSignal))
  expect(work.onConfirmed).toHaveBeenCalledWith(original, reply); work.dispose()
})
it('does not dispatch or invalidate when the pre-send CSRF read fails', async () => {
  const work = fixture()
  vi.mocked(fetchCSRF).mockRejectedValueOnce(new ApiError('NETWORK_ERROR'))
  act(() => { work.result.current.review(proposal) })
  await act(async () => { await work.result.current.confirm() })
  expect(work.result.current.phase).toBe('rejected'); expect(sendAssetCommand).not.toHaveBeenCalled()
  for (const key of [...keys, location, ...unrelated]) expect(work.client.getQueryState(key)?.isInvalidated).toBe(false)
  work.dispose()
})
it('interrupts sent work and withholds late success rather than replaying on reconnect', async () => {
  const work = fixture()
  let release: ((value: AssetResult) => void) | undefined, signal: AbortSignal | undefined
  vi.mocked(sendAssetCommand).mockImplementation((_command, _csrf, requestSignal) => { signal = requestSignal; return new Promise((resolve) => { release = resolve }) })
  act(() => { work.result.current.review(proposal) })
  let pending: Promise<void> | undefined
  await act(async () => { pending = work.result.current.confirm() })
  act(() => { window.dispatchEvent(new Event('offline')) })
  expect(signal?.aborted).toBe(true)
  await act(async () => { release?.(reply); await pending })
  expect(work.result.current.phase).toBe('uncertain'); expect(work.onConfirmed).not.toHaveBeenCalled()
  await act(async () => { window.dispatchEvent(new Event('online')) })
  expect(sendAssetCommand).toHaveBeenCalledTimes(1); work.dispose()
})
it.each([false, true])('purges volatile command state on real session loss without forwarding financial bodies, mandatory=%s', async (mandatory) => {
  const work = fixture()
  vi.mocked(sendAssetCommand).mockRejectedValueOnce(new ApiError(mandatory ? 'PASSWORD_CHANGE_REQUIRED' : 'AUTHENTICATION_REQUIRED', { status: mandatory ? 403 : 401 }))
  act(() => { work.result.current.review(proposal) })
  await act(async () => { await work.result.current.confirm() })
  expect(work.result.current.command).toBeUndefined(); expect(work.result.current.open).toBe(false); expect(work.result.current.canLeave()).toBe(true)
  expect(work.onSessionLost).toHaveBeenCalledWith(mandatory); work.dispose()
})
