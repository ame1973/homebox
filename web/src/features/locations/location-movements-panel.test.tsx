import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/client'
import * as api from '@/lib/api/locations'
import { messages } from '@/lib/messages'
import { notifyPageSuspended } from '@/lib/session-lifecycle'
import { quoteExactText } from '@/lib/text-literal'
import { LocationMovementsPanel } from './location-movements-panel'

vi.mock('@/lib/api/locations', async (original) => ({ ...await original<typeof import('@/lib/api/locations')>(), fetchLocationMovements: vi.fn() }))
const text = messages.locations
const event = { id: 'event\0‮a', location_id: 'room', occurred_at: 'legacy\r\n時間', from_parent_id: 'old/parent', to_parent_id: null }
function page(changes: Partial<api.LocationMovements> = {}, revision = 3): api.LocationMovementsRead {
  return { revision, value: { location_id: 'room', deleted: false, data: [], next_cursor: null, ...changes } }
}
const props = { id: 'room', revision: 3 as number | undefined, online: true, checked: true, checking: false, invalid: false, revalidate: vi.fn(), onSessionLost: vi.fn() }
const region = () => screen.getByRole('region', { name: text.movementsTitle })
const open = () => within(region()).getByRole('button', { name: text.movementsOpen })
const restart = () => within(region()).getByRole('button', { name: text.movementsRestart })
const next = () => within(region()).getByRole('button', { name: text.movementsNext })
const previous = () => within(region()).getByRole('button', { name: text.movementsPrevious })

beforeEach(() => {
  vi.clearAllMocks()
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: true })
  props.revalidate.mockResolvedValue(true)
  vi.mocked(api.fetchLocationMovements).mockReset().mockResolvedValue(page())
})
afterEach(() => { vi.restoreAllMocks() })
function mount(values = props) {
  const view = render(<><LocationMovementsPanel {...values} /><button type="button">其他位置</button></>)
  return { ...view, update: (changes: Partial<typeof props>) => view.rerender(<><LocationMovementsPanel {...props} {...changes} /><button type="button">其他位置</button></>) }
}

it('opens one explicit page and preserves exact historical text without claiming complete or chronological history', async () => {
  vi.mocked(api.fetchLocationMovements).mockResolvedValue(page({ data: [event] }))
  const user = userEvent.setup(); mount()
  expect(api.fetchLocationMovements).not.toHaveBeenCalled()
  expect(within(region()).getByText(text.movementsHelp)).toHaveTextContent('不是完整稽核紀錄')
  expect(within(region()).getByText(text.movementsHelp)).toHaveTextContent('不是時間先後順序')
  await user.click(open())
  expect(await within(region()).findByText(quoteExactText(event.id))).toBeVisible()
  expect(within(region()).getByText(quoteExactText(event.occurred_at))).toBeVisible()
  expect(within(region()).getByText(quoteExactText(event.from_parent_id))).toBeVisible()
  expect(within(region()).getByText(text.movementsRoot)).toBeVisible()
  expect(within(region()).getByText(text.movementsEnd)).toBeVisible()
  expect(api.fetchLocationMovements).toHaveBeenCalledWith('room', { limit: 20 }, expect.any(AbortSignal))
  expect(restart()).toHaveFocus()
})

it('shows a retained deleted owner when no current Location proof is available', async () => {
  vi.mocked(api.fetchLocationMovements).mockResolvedValue(page({ deleted: true, data: [event] }, 7))
  mount({ ...props, revision: undefined })
  await userEvent.setup().click(open())
  expect(await within(region()).findByText(text.movementsDeleted)).toBeVisible()
  expect(within(region()).getByText(quoteExactText(event.id))).toBeVisible()
})

it('pages explicitly, retains controls and distinguishes an empty continuation', async () => {
  vi.mocked(api.fetchLocationMovements).mockResolvedValueOnce(page({ data: [event], next_cursor: 'next' })).mockResolvedValueOnce(page())
  const user = userEvent.setup(); mount(); await user.click(open()); await within(region()).findByText(quoteExactText(event.id))
  const button = next(); await user.click(button)
  expect(await within(region()).findByText(text.movementsEmptyPage)).toBeVisible()
  expect(within(region()).queryByText(text.movementsEmpty)).not.toBeInTheDocument()
  expect(button).toHaveFocus(); expect(button).toHaveAttribute('aria-disabled', 'true')
  await user.click(previous()); expect(within(region()).getByText(quoteExactText(event.id))).toBeVisible()
  await user.click(next()); expect(within(region()).getByText(text.movementsEmptyPage)).toBeVisible()
  expect(api.fetchLocationMovements).toHaveBeenCalledTimes(2)
})

it.each(['duplicate', 'reversed', 'cycle', 'revision', 'deleted', 'invalid cursor'] as const)('withholds a %s page until explicit restart', async (kind) => {
  vi.mocked(api.fetchLocationMovements).mockResolvedValueOnce(page({ data: [event], next_cursor: 'one' }))
  if (kind === 'duplicate') vi.mocked(api.fetchLocationMovements).mockResolvedValueOnce(page({ data: [event] }))
  if (kind === 'reversed') vi.mocked(api.fetchLocationMovements).mockResolvedValueOnce(page({ data: [{ ...event, id: 'a' }] }))
  if (kind === 'cycle') vi.mocked(api.fetchLocationMovements).mockResolvedValueOnce(page({ next_cursor: 'one' }))
  if (kind === 'revision') vi.mocked(api.fetchLocationMovements).mockResolvedValueOnce(page({}, 4))
  if (kind === 'deleted') vi.mocked(api.fetchLocationMovements).mockResolvedValueOnce(page({ deleted: true }))
  if (kind === 'invalid cursor') vi.mocked(api.fetchLocationMovements).mockRejectedValueOnce(new ApiError('INVALID_CURSOR', { status: 400 }))
  vi.mocked(api.fetchLocationMovements).mockResolvedValueOnce(page())
  const user = userEvent.setup(); mount(); await user.click(open()); await within(region()).findByText(quoteExactText(event.id))
  await user.click(next())
  expect(await within(region()).findByText(kind === 'revision' || kind === 'invalid cursor' ? text.movementsChanged : text.movementsError)).toBeVisible()
  expect(within(region()).queryByText(quoteExactText(event.id))).not.toBeInTheDocument()
  await user.click(restart()); expect(await within(region()).findByText(text.movementsEmpty)).toBeVisible()
  expect(api.fetchLocationMovements).toHaveBeenCalledTimes(3)
})

it('hides deleted or changed current Location reads and does not silently retry after offline', async () => {
  vi.mocked(api.fetchLocationMovements).mockResolvedValue(page({ data: [event] }))
  const user = userEvent.setup(), view = mount(); await user.click(open()); await within(region()).findByText(quoteExactText(event.id))
  view.update({ revision: 4 }); expect(within(region()).getByText(text.movementsChanged)).toBeVisible()
  expect(within(region()).queryByText(quoteExactText(event.id))).not.toBeInTheDocument()
  view.update({ invalid: true }); expect(within(region()).getByText(text.movementsError)).toBeVisible()
  view.update({ invalid: false })
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: false }); fireEvent(window, new Event('offline')); view.update({ online: false })
  expect(within(region()).getByText(text.movementsOffline)).toBeVisible()
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: true }); fireEvent(window, new Event('online')); view.update({ online: true })
  expect(api.fetchLocationMovements).toHaveBeenCalledTimes(1)
  expect(within(region()).getByText(text.movementsOffline)).toBeVisible()
  await user.click(restart()); expect(await within(region()).findByText(quoteExactText(event.id))).toBeVisible()
})

it('withholds a deleted reply when the screen still holds a current Location snapshot', async () => {
  vi.mocked(api.fetchLocationMovements).mockResolvedValue(page({ deleted: true, data: [event] }))
  mount(); await userEvent.setup().click(open())
  expect(await within(region()).findByText(text.movementsChanged)).toBeVisible()
  expect(within(region()).queryByText(quoteExactText(event.id))).not.toBeInTheDocument()
})

it('does not steal focus when a delayed next page finishes after the user moves elsewhere', async () => {
  let release!: (value: api.LocationMovementsRead) => void
  vi.mocked(api.fetchLocationMovements).mockResolvedValueOnce(page({ data: [event], next_cursor: 'next' }))
    .mockImplementationOnce(() => new Promise((resolve) => { release = resolve }))
  const user = userEvent.setup(); mount(); await user.click(open()); await within(region()).findByText(quoteExactText(event.id))
  await user.click(next()); await waitFor(() => expect(release).toBeTypeOf('function'))
  expect(next()).toHaveFocus(); expect(next()).toHaveAttribute('aria-disabled', 'true')
  const other = screen.getByRole('button', { name: '其他位置' }); act(() => { other.focus() })
  await act(async () => { release(page()) })
  expect(await within(region()).findByText(text.movementsEmptyPage)).toBeVisible(); expect(other).toHaveFocus()
})

it('does not treat a missing retained owner or failed revalidation as an empty history', async () => {
  props.revalidate.mockResolvedValueOnce(false).mockResolvedValue(true)
  vi.mocked(api.fetchLocationMovements).mockRejectedValueOnce(new ApiError('NOT_FOUND', { status: 404 })).mockResolvedValueOnce(page())
  const user = userEvent.setup(); mount(); await user.click(open())
  expect(await within(region()).findByText(text.movementsError)).toBeVisible(); expect(api.fetchLocationMovements).not.toHaveBeenCalled()
  await user.click(restart()); expect(await within(region()).findByText(text.movementsError)).toBeVisible()
  expect(within(region()).queryByText(text.movementsEmpty)).not.toBeInTheDocument()
  await user.click(restart()); expect(await within(region()).findByText(text.movementsEmpty)).toBeVisible()
})

it('aborts and discards a delayed result on delivered session clear', async () => {
  let release!: (value: api.LocationMovementsRead) => void, signal!: AbortSignal
  vi.mocked(api.fetchLocationMovements).mockImplementation((_id, _query, nextSignal) => { signal = nextSignal; return new Promise((resolve) => { release = resolve }) })
  const user = userEvent.setup(); mount(); await user.click(open()); await waitFor(() => expect(release).toBeTypeOf('function'))
  act(() => { notifyPageSuspended() }); expect(signal.aborted).toBe(true)
  expect(within(region()).getByText(text.movementsCleared)).toBeVisible()
  await act(async () => { release(page({ data: [event] })) })
  expect(within(region()).queryByText(quoteExactText(event.id))).not.toBeInTheDocument()
  await user.click(within(region()).getByRole('button', { name: text.movementsLogin }))
  expect(props.onSessionLost).toHaveBeenCalledWith(false)
})

it.each([false, true])('redacts before pagehide and restores focus only if continuation is still focused (moved=%s)', async (moved) => {
  vi.mocked(api.fetchLocationMovements).mockResolvedValue(page({ data: [event] }))
  const user = userEvent.setup(); mount(); await user.click(open()); await within(region()).findByText(quoteExactText(event.id))
  act(() => { window.dispatchEvent(new PageTransitionEvent('pagehide', { persisted: true })); window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true })) })
  expect(within(region()).queryByText(quoteExactText(event.id))).not.toBeInTheDocument()
  let release!: (value: boolean) => void; props.revalidate.mockImplementationOnce(() => new Promise((resolve) => { release = resolve }))
  const resume = within(region()).getByRole('button', { name: text.movementsContinue }); await user.click(resume)
  expect(resume).toHaveFocus(); expect(resume).toHaveAttribute('aria-disabled', 'true')
  const other = screen.getByRole('button', { name: '其他位置' }); if (moved) act(() => { other.focus() })
  await act(async () => { release(true) })
  expect(await within(region()).findByText(quoteExactText(event.id))).toBeVisible()
  expect(moved ? other : within(region()).getByRole('heading', { name: text.movementsTitle })).toHaveFocus()
})

it.each([[401, 'AUTHENTICATION_REQUIRED', false], [403, 'PASSWORD_CHANGE_REQUIRED', true]] as const)('propagates session failure %s safely', async (status, code, mandatory) => {
  vi.mocked(api.fetchLocationMovements).mockRejectedValue(new ApiError(code, { status }))
  mount(); await userEvent.setup().click(open())
  await waitFor(() => expect(props.onSessionLost).toHaveBeenCalledWith(mandatory))
  expect(within(region()).queryByText(text.movementsEmpty)).not.toBeInTheDocument()
})
