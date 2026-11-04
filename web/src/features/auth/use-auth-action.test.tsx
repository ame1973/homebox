import { QueryClientProvider } from '@tanstack/react-query'
import { act, renderHook } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import type { ReactNode } from 'react'
import { ApiError } from '@/lib/api/auth'
import { createQueryClient } from '@/lib/query'
import { useAuthAction, type AuthOutcome } from './use-auth-action'

function fixture() {
  const client = createQueryClient()
  const onSuccess = vi.fn()
  const onSessionLost = vi.fn()
  const hook = renderHook(() => useAuthAction(onSuccess, onSessionLost), { wrapper: ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider> })
  return { ...hook, client, onSuccess, onSessionLost }
}

it('admits one active action and ignores duplicate attempts', async () => {
  const { result, unmount, client, onSuccess } = fixture()
  let finish: ((result: AuthOutcome) => void) | undefined
  const work = vi.fn(() => new Promise<AuthOutcome>((resolve) => { finish = resolve }))
  let pending: Promise<void> | undefined
  await act(async () => {
    pending = result.current.run('setup', work)
    await result.current.run('setup', work)
  })
  expect(work).toHaveBeenCalledTimes(1)
  expect(result.current.canLeave()).toBe(false)
  await act(async () => { finish?.({ kind: 'setup' }); await pending })
  expect(onSuccess).toHaveBeenCalledWith({ kind: 'setup' })
  expect(result.current.canLeave()).toBe(true)
  expect(result.current.pendingKind).toBeUndefined()
  unmount()
  client.clear()
})

it('aborts and purges private memory on unmount without applying a late success', async () => {
  const { result, unmount, client, onSuccess, onSessionLost } = fixture()
  let finish: ((result: AuthOutcome) => void) | undefined
  let requestSignal: AbortSignal | undefined
  let pending: Promise<void> | undefined
  client.setQueryData(['items', 'private'], { private: true })
  await act(async () => { pending = result.current.run('login', (signal) => {
    requestSignal = signal
    return new Promise((resolve) => { finish = resolve })
  }) })
  unmount()
  expect(requestSignal?.aborted).toBe(true)
  expect(client.getQueryCache().getAll()).toHaveLength(0)
  await act(async () => { finish?.({ kind: 'login', admin: { id: 'admin', revision: 1, username: 'owner', must_change_password: true } }); await pending })
  expect(onSuccess).not.toHaveBeenCalled()
  expect(onSessionLost).not.toHaveBeenCalled()
  client.clear()
})

it('redirects expired protected actions but preserves safe failed-password errors', async () => {
  const { result, unmount, client, onSessionLost } = fixture()
  await act(async () => { await result.current.run('password', async () => { throw new ApiError('AUTHENTICATION_REQUIRED', { status: 401 }) }) })
  expect(onSessionLost).toHaveBeenCalledWith('expired')
  unmount()
  client.clear()
  const next = fixture()
  await act(async () => { await next.result.current.run('password', async () => { throw new ApiError('CREDENTIALS_REJECTED', { status: 403 }) }) })
  expect(next.result.current.error?.code).toBe('CREDENTIALS_REJECTED')
  expect(next.onSessionLost).not.toHaveBeenCalled()
  next.unmount()
  next.client.clear()
})

it('discards unknown exception details and aborts when the network disappears', async () => {
  const { result, unmount, client, onSessionLost } = fixture()
  await act(async () => { await result.current.run('login', async () => { throw new Error('private diagnostic') }) })
  expect(result.current.error?.code).toBe('UNKNOWN_ERROR')
  expect(String(result.current.error)).not.toContain('private diagnostic')
  let finish: ((result: AuthOutcome) => void) | undefined
  let signal: AbortSignal | undefined
  let pending: Promise<void> | undefined
  await act(async () => { pending = result.current.run('setup', (nextSignal) => { signal = nextSignal; return new Promise((resolve) => { finish = resolve }) }) })
  act(() => { window.dispatchEvent(new Event('offline')) })
  expect(signal?.aborted).toBe(true)
  await act(async () => { finish?.({ kind: 'setup' }); await pending })
  expect(onSessionLost).toHaveBeenCalledWith('uncertain')
  unmount()
  client.clear()
})
