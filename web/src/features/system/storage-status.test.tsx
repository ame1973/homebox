import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { messages } from '@/lib/messages'
import StorageStatus from './storage-status'

const text = messages.deviceStorage
let original: PropertyDescriptor | undefined

beforeEach(() => { original = Object.getOwnPropertyDescriptor(navigator, 'storage') })
afterEach(() => {
  if (original) Object.defineProperty(navigator, 'storage', original)
  else Reflect.deleteProperty(navigator, 'storage')
})

function storage(value: unknown) {
  Object.defineProperty(navigator, 'storage', { configurable: true, value })
}

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((done) => { resolve = done })
  return { promise, resolve }
}

it('reports origin-wide estimates and requests persistence only after an explicit action', async () => {
  const estimate = vi.fn().mockResolvedValue({ usage: 1024 ** 2, quota: 2 * 1024 ** 3 })
  const persisted = vi.fn().mockResolvedValue(false)
  const persist = vi.fn().mockResolvedValue(true)
  storage({ estimate, persisted, persist })
  render(<StorageStatus />)
  const region = screen.getByRole('region', { name: text.title })
  expect(await within(region).findByText('1 MiB')).toBeVisible()
  expect(within(region).getByText('2 GiB')).toBeVisible()
  expect(within(region).getByText(text.notPersistent)).toBeVisible()
  expect(within(region).getByText(text.notBackup)).toBeVisible()
  expect(persist).not.toHaveBeenCalled()
  const request = within(region).getByRole('button', { name: text.request })
  await userEvent.setup().click(request)
  expect(persist).toHaveBeenCalledTimes(1)
  expect(await within(region).findByText(text.persistent)).toBeVisible()
  expect(request).toHaveAttribute('aria-disabled', 'true')
  expect(request).toHaveFocus()
  await userEvent.setup().click(request)
  expect(persist).toHaveBeenCalledTimes(1)
  persisted.mockResolvedValueOnce(true)
  await userEvent.setup().click(within(region).getByRole('button', { name: text.refresh }))
  await waitFor(() => expect(estimate).toHaveBeenCalledTimes(2))
  expect(persist).toHaveBeenCalledTimes(1)
})

it('reports unsupported and invalid estimates as unknown, never displaying browser errors', async () => {
  storage(undefined)
  const view = render(<StorageStatus />)
  const region = screen.getByRole('region', { name: text.title })
  await waitFor(() => expect(within(region).getAllByText(text.unknown)).toHaveLength(3))
  expect(within(region).getByRole('button', { name: text.request })).toHaveAttribute('aria-disabled', 'true')
  view.unmount()

  storage({ estimate: vi.fn().mockResolvedValue({ usage: -1, quota: Infinity }),
    persisted: vi.fn().mockRejectedValue(new Error('private browser detail')),
    persist: vi.fn().mockRejectedValue(new Error('private browser detail')) })
  render(<StorageStatus />)
  const next = screen.getByRole('region', { name: text.title })
  await waitFor(() => expect(within(next).getAllByText(text.unknown)).toHaveLength(3))
  await userEvent.setup().click(within(next).getByRole('button', { name: text.request }))
  expect(await within(next).findByText(text.requestFailed)).toBeVisible()
  expect(next).not.toHaveTextContent('private browser detail')
})

it('clears the old persistence observation when the browser request fails', async () => {
  storage({ estimate: vi.fn().mockResolvedValue({ usage: 1024, quota: 2048 }), persisted: vi.fn().mockResolvedValue(false),
    persist: vi.fn().mockRejectedValue(new Error('private browser detail')) })
  render(<StorageStatus />)
  const region = screen.getByRole('region', { name: text.title })
  await within(region).findByText(text.notPersistent)
  await userEvent.setup().click(within(region).getByRole('button', { name: text.request }))
  expect(await within(region).findByText(text.requestFailed)).toBeVisible()
  expect(within(region).queryByText(text.notPersistent)).not.toBeInTheDocument()
  expect(within(region).getByText(text.unknown)).toBeVisible()
  expect(region).not.toHaveTextContent('private browser detail')
})

it('shows a browser refusal without retrying and permits an explicit later request', async () => {
  const persist = vi.fn().mockResolvedValueOnce(false).mockResolvedValueOnce(true)
  storage({ estimate: vi.fn().mockResolvedValue({}), persisted: vi.fn().mockResolvedValue(false), persist })
  render(<StorageStatus />)
  const region = screen.getByRole('region', { name: text.title })
  await within(region).findByText(text.notPersistent)
  const user = userEvent.setup()
  await user.click(within(region).getByRole('button', { name: text.request }))
  expect(await within(region).findByText(text.requestDenied)).toBeVisible()
  expect(persist).toHaveBeenCalledTimes(1)
  await user.click(within(region).getByRole('button', { name: text.request }))
  expect(await within(region).findByText(text.persistent)).toBeVisible()
  expect(persist).toHaveBeenCalledTimes(2)
})

it('times out a stalled estimate and ignores its late result after a fresh read', async () => {
  vi.useFakeTimers()
  const pending = deferred<StorageEstimate>()
  const estimate = vi.fn().mockReturnValueOnce(pending.promise).mockResolvedValueOnce({ usage: 4096, quota: 8192 })
  storage({ estimate, persisted: vi.fn().mockResolvedValue(false), persist: vi.fn().mockResolvedValue(false) })
  render(<StorageStatus />)
  const region = screen.getByRole('region', { name: text.title })
  expect(within(region).getByText(text.loading)).toBeVisible()
  await act(async () => { await vi.advanceTimersByTimeAsync(8_000) })
  expect(within(region).getAllByText(text.unknown)).toHaveLength(2)
  await act(async () => { fireEvent.click(within(region).getByRole('button', { name: text.refresh })) })
  expect(within(region).getByText('4 KiB')).toBeVisible()
  expect(within(region).getByText('8 KiB')).toBeVisible()
  await act(async () => { pending.resolve({ usage: 1048576, quota: 1048576 }); await pending.promise })
  expect(within(region).getByText('4 KiB')).toBeVisible()
  expect(within(region).queryByText('1 MiB')).not.toBeInTheDocument()
})

it('does not issue duplicate requests and ignores a result after unmount', async () => {
  const pending = deferred<boolean>(), persist = vi.fn().mockReturnValue(pending.promise)
  storage({ estimate: vi.fn().mockResolvedValue({ usage: 0, quota: 0 }), persisted: vi.fn().mockResolvedValue(false), persist })
  const view = render(<StorageStatus />)
  const region = screen.getByRole('region', { name: text.title })
  await within(region).findByText(text.notPersistent)
  const user = userEvent.setup()
  await user.click(within(region).getByRole('button', { name: text.request }))
  expect(within(region).getByRole('button', { name: text.request })).toHaveAttribute('aria-disabled', 'true')
  expect(within(region).getByRole('button', { name: text.refresh })).toHaveAttribute('aria-disabled', 'true')
  expect(persist).toHaveBeenCalledTimes(1)
  view.unmount()
  await act(async () => { pending.resolve(true); await pending.promise })
  expect(persist).toHaveBeenCalledTimes(1)
})
