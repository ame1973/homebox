import type { ReactNode } from 'react'
import { QueryClientProvider } from '@tanstack/react-query'
import { act, renderHook } from '@testing-library/react'
import { beforeEach, expect, it, vi } from 'vitest'
import { fetchCSRF } from '@/lib/api/auth'
import { ApiError } from '@/lib/api/client'
import { createQueryClient } from '@/lib/query'
import { sendMediaCommand, type MediaCommandInput, type MediaResult } from './commands'
import { useMediaMutation } from './use-media-mutation'

vi.mock('@/lib/api/auth', () => ({ fetchCSRF: vi.fn() }))
vi.mock('./commands', async (original) => ({ ...await original<typeof import('./commands')>(), sendMediaCommand: vi.fn() }))
const item = { id: 'item', revision: 4, name: 'Owner', brand: null, model: '', category_id: null, description: '', notes: '', tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1, status: 'active' } as const
const image = { id: 'photo', revision: 7, sha256: 'a'.repeat(64), mime_type: 'image/heic', verification: 'unverified', width: 10, height: 20, bytes: 30, thumb_url: null, thumb_sha256: null } as const
const link = { id: 'link', revision: 2, media_id: image.id, owner_type: 'item', owner_id: item.id, is_cover: true, sort_order: -3, description: 'Original', media: image } as const
const source = { owner: { kind: 'item' as const, value: item }, links: [link] }
const proposal: MediaCommandInput = { kind: 'unlink', source, linkID: link.id }
const reply: MediaResult = { ok: true, operation_id: 'event', revision: 3 }
const keys = [['media', 'owner', 'item', item.id], ['media', 'membership', image.id], ['catalog', 'item', item.id], ['holdings', 'item', item.id], ['purchases', 'item', item.id], ['assets', 'list']] as const
const location = ['locations', 'tree'] as const
const unrelated = [['auth', 'me'], ['taxonomy', 'tags']] as const
beforeEach(() => { vi.mocked(fetchCSRF).mockReset().mockResolvedValue('csrf'); vi.mocked(sendMediaCommand).mockReset().mockResolvedValue(reply) })
function fixture() {
  const client = createQueryClient(), onConfirmed = vi.fn(), onSessionLost = vi.fn()
  const hook = renderHook(() => useMediaMutation(onConfirmed, onSessionLost), { wrapper: ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider> })
  for (const key of [...keys, location, ...unrelated]) client.setQueryData(key, { revision: 99, current: true })
  return { ...hook, client, onConfirmed, onSessionLost, dispose: () => { hook.unmount(); client.clear() } }
}

it('requires one explicit confirmation and invalidates reads without installing the frozen acknowledgement', async () => {
  const work = fixture()
  act(() => { work.result.current.review(proposal) })
  const original = work.result.current.command
  expect(fetchCSRF).not.toHaveBeenCalled(); expect(sendMediaCommand).not.toHaveBeenCalled()
  let release: ((value: MediaResult) => void) | undefined
  vi.mocked(sendMediaCommand).mockImplementation(() => new Promise((resolve) => { release = resolve }))
  let pending: Promise<void> | undefined
  await act(async () => { pending = work.result.current.confirm(); await work.result.current.confirm() })
  expect(sendMediaCommand).toHaveBeenCalledTimes(1)
  expect(work.client.getMutationCache().getAll()[0]?.options).toMatchObject({ retry: false, networkMode: 'always', gcTime: 0 })
  await act(async () => { release?.(reply); await pending })
  expect(work.onConfirmed).toHaveBeenCalledWith(original, reply); expect(work.result.current.canLeave()).toBe(true)
  for (const key of keys) { expect(work.client.getQueryState(key)?.isInvalidated).toBe(true); expect(work.client.getQueryData(key)).toEqual({ revision: 99, current: true }) }
  for (const key of [location, ...unrelated]) expect(work.client.getQueryState(key)?.isInvalidated).toBe(false)
  work.dispose()
})

it.each(['NETWORK_ERROR', 'REQUEST_TIMEOUT', 'INVALID_RESPONSE', 'REVISION_CONFLICT', 'MEDIA_COVER_CONFLICT', 'IDEMPOTENCY_EXPIRED', 'IDEMPOTENCY_CONFLICT', 'SERVICE_UNAVAILABLE'] as const)(
  'retains the reviewed command after %s and never replays on reconnect', async (code) => {
    const work = fixture(), uncertain = ['NETWORK_ERROR', 'REQUEST_TIMEOUT', 'INVALID_RESPONSE', 'IDEMPOTENCY_EXPIRED', 'IDEMPOTENCY_CONFLICT', 'SERVICE_UNAVAILABLE'].includes(code)
    vi.mocked(sendMediaCommand).mockRejectedValueOnce(new ApiError(code, { status: code === 'SERVICE_UNAVAILABLE' ? 503 : uncertain ? undefined : 409 }))
    act(() => { work.result.current.review(proposal) })
    const original = work.result.current.command
    await act(async () => { await work.result.current.confirm() })
    expect(work.result.current.phase).toBe(uncertain ? 'uncertain' : 'rejected'); expect(work.result.current.command).toBe(original)
    for (const key of keys) expect(work.client.getQueryState(key)?.isInvalidated).toBe(true)
    work.client.removeQueries({ queryKey: ['media'] })
    await act(async () => { window.dispatchEvent(new Event('online')) })
    expect(sendMediaCommand).toHaveBeenCalledTimes(1); expect(work.result.current.command).toMatchObject(proposal)
    expect(work.onConfirmed).not.toHaveBeenCalled(); work.dispose()
  },
)

it('keeps an uncertain unlink after source eviction for explicit original-key reconciliation only', async () => {
  const work = fixture()
  vi.mocked(sendMediaCommand).mockRejectedValueOnce(new ApiError('INVALID_RESPONSE')).mockResolvedValueOnce(reply)
  act(() => { work.result.current.review(proposal) })
  const original = work.result.current.command
  await act(async () => { await work.result.current.confirm() })
  for (const root of ['media', 'catalog', 'holdings', 'purchases', 'assets']) work.client.removeQueries({ queryKey: [root] })
  act(() => { work.result.current.dismiss(); work.result.current.review(proposal); window.dispatchEvent(new Event('online')) })
  expect(work.result.current.command).toBe(original); expect(sendMediaCommand).toHaveBeenCalledTimes(1)
  act(() => { work.result.current.reopen() })
  vi.mocked(fetchCSRF).mockResolvedValueOnce('fresh-csrf')
  await act(async () => { await work.result.current.confirm() })
  expect(sendMediaCommand).toHaveBeenNthCalledWith(1, original, 'csrf', expect.any(AbortSignal))
  expect(sendMediaCommand).toHaveBeenNthCalledWith(2, original, 'fresh-csrf', expect.any(AbortSignal))
  expect(work.onConfirmed).toHaveBeenCalledWith(original, reply); work.dispose()
})

it('invalidates location paths for a reviewed Unit at a location, not for unrelated item media', async () => {
  const work = fixture()
  const unit = { id: 'unit', item_id: item.id, revision: 3, location_id: 'place', asset_code: 'AST', status: 'stored', serial_number: null, notes: '' } as const
  const selected = { ...link, owner_type: 'unit' as const, owner_id: unit.id }
  act(() => { work.result.current.review({ kind: 'unlink', source: { owner: { kind: 'unit', value: unit }, links: [selected] }, linkID: selected.id }) })
  await act(async () => { await work.result.current.confirm() })
  expect(work.client.getQueryState(location)?.isInvalidated).toBe(true)
  for (const key of keys) expect(work.client.getQueryState(key)?.isInvalidated).toBe(true)
  for (const key of unrelated) expect(work.client.getQueryState(key)?.isInvalidated).toBe(false)
  work.dispose()
})

it('does not dispatch or invalidate when the initial CSRF read fails', async () => {
  const work = fixture()
  vi.mocked(fetchCSRF).mockRejectedValueOnce(new ApiError('NETWORK_ERROR'))
  act(() => { work.result.current.review(proposal) })
  await act(async () => { await work.result.current.confirm() })
  expect(work.result.current.phase).toBe('rejected'); expect(sendMediaCommand).not.toHaveBeenCalled()
  for (const key of [...keys, location, ...unrelated]) expect(work.client.getQueryState(key)?.isInvalidated).toBe(false)
  work.dispose()
})

it('interrupts a sent write when offline and does not accept late success or replay', async () => {
  const work = fixture()
  let release: ((value: MediaResult) => void) | undefined, signal: AbortSignal | undefined
  vi.mocked(sendMediaCommand).mockImplementation((_command, _csrf, requestSignal) => { signal = requestSignal; return new Promise((resolve) => { release = resolve }) })
  act(() => { work.result.current.review(proposal) })
  let pending: Promise<void> | undefined
  await act(async () => { pending = work.result.current.confirm() })
  act(() => { window.dispatchEvent(new Event('offline')) })
  expect(signal?.aborted).toBe(true)
  await act(async () => { release?.(reply); await pending })
  expect(work.result.current.phase).toBe('uncertain'); expect(work.onConfirmed).not.toHaveBeenCalled()
  await act(async () => { window.dispatchEvent(new Event('online')) })
  expect(sendMediaCommand).toHaveBeenCalledTimes(1); work.dispose()
})

it.each([false, true])('purges volatile review state on session loss, mandatory=%s', async (mandatory) => {
  const work = fixture()
  vi.mocked(sendMediaCommand).mockRejectedValueOnce(new ApiError(mandatory ? 'PASSWORD_CHANGE_REQUIRED' : 'AUTHENTICATION_REQUIRED', { status: mandatory ? 403 : 401 }))
  act(() => { work.result.current.review(proposal) })
  await act(async () => { await work.result.current.confirm() })
  expect(work.result.current.command).toBeUndefined(); expect(work.result.current.open).toBe(false); expect(work.result.current.canLeave()).toBe(true)
  expect(work.onSessionLost).toHaveBeenCalledWith(mandatory); work.dispose()
})
