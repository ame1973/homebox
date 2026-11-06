import type { ReactNode } from 'react'
import { QueryClientProvider } from '@tanstack/react-query'
import { act, renderHook } from '@testing-library/react'
import { beforeEach, expect, it, vi } from 'vitest'
import { fetchCSRF } from '@/lib/api/auth'
import { ApiError } from '@/lib/api/client'
import { createQueryClient } from '@/lib/query'
import { sendLocationCommand, type LocationCommandInput, type LocationResult } from './commands'
import { useLocationMutation } from './use-location-mutation'

vi.mock('@/lib/api/auth', () => ({ fetchCSRF: vi.fn() }))
vi.mock('./commands', async (original) => ({ ...await original<typeof import('./commands')>(), sendLocationCommand: vi.fn() }))
const proposal: LocationCommandInput = { kind: 'create', body: { name: '房間', code: 'R1' } }
const reply: LocationResult = { id: 'room', name: '房間', code: 'R1', kind: 'other', parent_id: null, sort_order: 0, description: '', status: 'active', revision: 1 }

beforeEach(() => {
  vi.mocked(fetchCSRF).mockReset().mockResolvedValue('csrf')
  vi.mocked(sendLocationCommand).mockReset().mockResolvedValue(reply)
})
function fixture() {
  const client = createQueryClient()
  const onConfirmed = vi.fn()
  const onSessionLost = vi.fn()
  const hook = renderHook(() => useLocationMutation(onConfirmed, onSessionLost), { wrapper: ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider> })
  return { ...hook, client, onConfirmed, onSessionLost, dispose: () => { hook.unmount(); client.clear() } }
}

it('reviews without writing, restores the trigger and dismisses an unsent request', () => {
  const work = fixture()
  const trigger = document.createElement('button')
  document.body.append(trigger)
  trigger.focus()
  act(() => { work.result.current.review(proposal) })
  expect(work.result.current.phase).toBe('review')
  expect(work.result.current.open).toBe(true)
  expect(work.result.current.canLeave()).toBe(false)
  expect(fetchCSRF).not.toHaveBeenCalled()
  const focus = document.createElement('input')
  document.body.append(focus)
  focus.focus()
  work.result.current.restoreFocus()
  expect(trigger).toHaveFocus()
  act(() => { work.result.current.suspend() })
  focus.focus()
  work.result.current.restoreFocus()
  expect(focus).toHaveFocus()
  act(() => { work.result.current.reopen() })
  expect(work.result.current.open).toBe(true)
  act(() => { work.result.current.dismiss() })
  expect(work.result.current.command).toBeUndefined()
  expect(work.result.current.phase).toBe('idle')
  act(() => { work.result.current.reopen() })
  expect(work.result.current.open).toBe(false)
  trigger.remove()
  focus.remove()
  work.dispose()
})

it('admits only one active confirmation and invalidates reads without installing a frozen reply', async () => {
  const work = fixture()
  let finish: ((result: LocationResult) => void) | undefined
  vi.mocked(sendLocationCommand).mockImplementation(() => new Promise((resolve) => { finish = resolve }))
  const current = { ...reply, revision: 8, name: '目前資料' }
  work.client.setQueryData(['locations', 'detail', 'room'], current)
  act(() => { work.result.current.review(proposal) })
  const original = work.result.current.command
  let pending: Promise<void> | undefined
  await act(async () => {
    pending = work.result.current.confirm()
    await work.result.current.confirm()
    work.result.current.review({ kind: 'create', body: { name: '另一個', code: 'R2' } })
  })
  expect(sendLocationCommand).toHaveBeenCalledTimes(1)
  expect(work.result.current.command).toBe(original)
  expect(work.result.current.phase).toBe('pending')
  expect(work.client.getMutationCache().getAll()[0]?.options).toMatchObject({ retry: false, networkMode: 'always', gcTime: 0 })
  await act(async () => { finish?.(reply); await pending })
  expect(work.onConfirmed).toHaveBeenCalledWith(original, reply)
  expect(work.result.current.canLeave()).toBe(true)
  expect(work.result.current.confirmed).toBe(true)
  expect(work.result.current.command).toBeUndefined()
  expect(work.result.current.open).toBe(false)
  expect(work.client.getQueryData(['locations', 'detail', 'room'])).toEqual(current)
  expect(work.client.getQueryState(['locations', 'detail', 'room'])?.isInvalidated).toBe(true)
  await act(async () => { await work.result.current.confirm() })
  expect(sendLocationCommand).toHaveBeenCalledTimes(1)
  work.dispose()
})

it.each([undefined, 'REVISION_CONFLICT', 'NETWORK_ERROR'] as const)('invalidates location-dependent catalog/holding scopes after sent %s without replacing current data', async (code) => {
  const work = fixture()
  const keys = [['locations', 'tree'], ['catalog', 'items', { location_id: 'room', scope: 'subtree' }], ['holdings', 'stock', { location_id: 'room' }]] as const
  const current = { revision: 99, current: true }
  for (const key of [...keys, ['auth', 'me'], ['taxonomy', 'tags']]) work.client.setQueryData(key, current)
  if (code) vi.mocked(sendLocationCommand).mockRejectedValueOnce(new ApiError(code, { status: code === 'REVISION_CONFLICT' ? 409 : undefined }))
  act(() => { work.result.current.review(proposal) })
  await act(async () => { await work.result.current.confirm() })
  for (const key of keys) {
    expect(work.client.getQueryState(key)?.isInvalidated).toBe(true)
    expect(work.client.getQueryData(key)).toEqual(current)
  }
  expect(work.client.getQueryState(['auth', 'me'])?.isInvalidated).toBe(false)
  expect(work.client.getQueryState(['taxonomy', 'tags'])?.isInvalidated).toBe(false)
  await act(async () => { window.dispatchEvent(new Event('online')) })
  expect(sendLocationCommand).toHaveBeenCalledTimes(1)
  expect(work.result.current.phase).toBe(code === 'NETWORK_ERROR' ? 'uncertain' : code ? 'rejected' : 'idle')
  work.dispose()
})

it('keeps a known conflict editable but never changes the rejected request by itself', async () => {
  const work = fixture()
  vi.mocked(sendLocationCommand).mockRejectedValue(new ApiError('REVISION_CONFLICT', { status: 409 }))
  act(() => { work.result.current.review(proposal) })
  const original = work.result.current.command
  await act(async () => { await work.result.current.confirm() })
  expect(work.result.current.phase).toBe('rejected')
  expect(work.result.current.command).toBe(original)
  expect(work.result.current.error?.code).toBe('REVISION_CONFLICT')
  act(() => { work.result.current.dismiss() })
  expect(work.result.current.command).toBeUndefined()
  act(() => { work.result.current.review(proposal) })
  expect(work.result.current.command?.key).not.toBe(original?.key)
  work.dispose()
})

it('preserves an uncertain original through reads, dismiss/reopen and reconnect until an explicit retry', async () => {
  const work = fixture()
  vi.mocked(sendLocationCommand).mockRejectedValueOnce(new ApiError('INVALID_RESPONSE', { status: 200 })).mockResolvedValueOnce(reply)
  act(() => { work.result.current.review(proposal) })
  const original = work.result.current.command
  await act(async () => { await work.result.current.confirm() })
  expect(work.result.current.phase).toBe('uncertain')
  act(() => {
    work.result.current.dismiss()
    work.result.current.review({ kind: 'create', body: { name: '不能取代原請求', code: 'NEW' } })
    window.dispatchEvent(new Event('offline'))
    window.dispatchEvent(new Event('online'))
  })
  expect(work.result.current.command).toBe(original)
  expect(work.result.current.open).toBe(false)
  expect(sendLocationCommand).toHaveBeenCalledTimes(1)
  act(() => { work.result.current.reopen() })
  expect(work.result.current.open).toBe(true)
  vi.mocked(fetchCSRF).mockResolvedValueOnce('fresh-csrf')
  await act(async () => { await work.result.current.confirm() })
  expect(sendLocationCommand).toHaveBeenNthCalledWith(1, original, 'csrf', expect.any(AbortSignal))
  expect(sendLocationCommand).toHaveBeenNthCalledWith(2, original, 'fresh-csrf', expect.any(AbortSignal))
  expect(work.onConfirmed).toHaveBeenCalledTimes(1)
  work.dispose()
})

it('does not call a pre-send CSRF failure an uncertain domain commit', async () => {
  const work = fixture()
  vi.mocked(fetchCSRF).mockRejectedValueOnce(new Error('private transport diagnostic'))
  act(() => { work.result.current.review(proposal) })
  await act(async () => { await work.result.current.confirm() })
  expect(work.result.current.phase).toBe('rejected')
  expect(work.result.current.error?.code).toBe('UNKNOWN_ERROR')
  expect(String(work.result.current.error)).not.toContain('private transport diagnostic')
  expect(sendLocationCommand).not.toHaveBeenCalled()
  work.dispose()
})

it.each(['offline', 'visibilitychange', 'dismiss', 'abort', 'leave'] as const)('interrupts a sent request on %s without accepting a late success or replaying it', async (event) => {
  const work = fixture()
  let finish: ((result: LocationResult) => void) | undefined
  let requestSignal: AbortSignal | undefined
  vi.mocked(sendLocationCommand).mockImplementation((_command, _csrf, signal) => { requestSignal = signal; return new Promise((resolve) => { finish = resolve }) })
  act(() => { work.result.current.review(proposal) })
  let pending: Promise<void> | undefined
  await act(async () => { pending = work.result.current.confirm() })
  act(() => {
    if (event === 'visibilitychange') { vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden'); document.dispatchEvent(new Event(event)) }
    else if (event === 'offline') window.dispatchEvent(new Event(event))
    else if (event === 'dismiss') work.result.current.dismiss()
    else if (event === 'leave') work.result.current.allowNavigation()
    else work.result.current.abort()
  })
  expect(requestSignal?.aborted).toBe(true)
  await act(async () => { finish?.(reply); await pending })
  expect(work.result.current.phase).toBe('uncertain')
  expect(work.result.current.error?.code).toBe('REQUEST_ABORTED')
  expect(work.onConfirmed).not.toHaveBeenCalled()
  expect(sendLocationCommand).toHaveBeenCalledTimes(1)
  work.dispose()
})

it('aborts before domain send and ignores both late success and late failure after unmount', async () => {
  const work = fixture()
  let csrf: ((token: string) => void) | undefined
  vi.mocked(fetchCSRF).mockImplementationOnce(() => new Promise((resolve) => { csrf = resolve }))
  act(() => { work.result.current.review(proposal) })
  let pending: Promise<void> | undefined
  await act(async () => { pending = work.result.current.confirm() })
  act(() => { work.result.current.abort() })
  await act(async () => { csrf?.('csrf'); await pending })
  expect(work.result.current.phase).toBe('rejected')
  expect(sendLocationCommand).not.toHaveBeenCalled()
  work.dispose()
  for (const reject of [false, true]) {
    const next = fixture()
    let finish: (() => void) | undefined
    let signal: AbortSignal | undefined
    vi.mocked(sendLocationCommand).mockImplementationOnce((_command, _csrf, current) => { signal = current; return new Promise((resolve, fail) => { finish = () => reject ? fail(new ApiError('NETWORK_ERROR')) : resolve(reply) }) })
    act(() => { next.result.current.review(proposal) })
    await act(async () => { pending = next.result.current.confirm() })
    next.unmount()
    expect(signal?.aborted).toBe(true)
    await act(async () => { finish?.(); await pending })
    expect(next.onConfirmed).not.toHaveBeenCalled()
    expect(next.onSessionLost).not.toHaveBeenCalled()
    next.client.clear()
  }
})

it.each([new ApiError('AUTHENTICATION_REQUIRED', { status: 401 }), new ApiError('PASSWORD_CHANGE_REQUIRED', { status: 403 })])('purges the proposal and exits for $code instead of permitting credential replay', async (error) => {
  const work = fixture()
  vi.mocked(sendLocationCommand).mockRejectedValueOnce(error)
  act(() => { work.result.current.review(proposal) })
  await act(async () => { await work.result.current.confirm() })
  expect(work.result.current.command).toBeUndefined()
  expect(work.result.current.phase).toBe('idle')
  expect(work.result.current.open).toBe(false)
  expect(work.result.current.canLeave()).toBe(true)
  expect(work.onSessionLost).toHaveBeenCalledWith(error.code === 'PASSWORD_CHANGE_REQUIRED')
  work.dispose()
})

it('restores safe fallback focus when a trigger is disabled or removed', () => {
  const work = fixture()
  const main = document.createElement('main')
  main.id = 'main'
  main.tabIndex = -1
  const trigger = document.createElement('button')
  document.body.append(main, trigger)
  trigger.focus()
  act(() => { work.result.current.review(proposal) })
  trigger.disabled = true
  work.result.current.restoreFocus()
  expect(main).toHaveFocus()
  trigger.remove()
  work.result.current.restoreFocus()
  expect(main).toHaveFocus()
  main.remove()
  work.dispose()
})
