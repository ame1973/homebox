import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/client'
import * as api from '@/lib/api/locations'
import { messages } from '@/lib/messages'
import { notifyPageSuspended } from '@/lib/session-lifecycle'
import { quoteExactText } from '@/lib/text-literal'
import { LocationMovementLookup } from './location-movement-lookup'

vi.mock('@/lib/api/locations', async (original) => ({ ...await original<typeof import('@/lib/api/locations')>(), fetchLocationMovements: vi.fn() }))
const text = messages.locations, legacy = 'deleted/location\0\r\n😀'
const result = (id: string): api.LocationMovementsRead => ({ revision: 5, value: { location_id: id, deleted: true, data: [{ id: 'event', location_id: id, occurred_at: 'legacy time', from_parent_id: null, to_parent_id: null }], next_cursor: null } })
const props = { online: true, checked: true, checking: false, invalid: false, revalidate: vi.fn(), onSessionLost: vi.fn() }
const lookup = () => screen.getByRole('region', { name: text.movementLookupTitle })
const field = () => within(lookup()).getByRole('textbox', { name: text.movementLookupID })
const choose = () => within(lookup()).getByRole('button', { name: text.movementLookupChoose })
const panel = () => screen.getByRole('region', { name: text.movementsTitle })

beforeEach(() => {
  vi.clearAllMocks()
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: true })
  props.revalidate.mockResolvedValue(true)
  vi.mocked(api.fetchLocationMovements).mockReset().mockImplementation(async (id) => result(id))
})
afterEach(() => { vi.restoreAllMocks() })
function mount(values = props) { return render(<><LocationMovementLookup {...values} /><button type="button">其他位置</button></>) }

it('explicitly selects an exact retained ID and reads a deleted owner without a current Location proof', async () => {
  const user = userEvent.setup(); mount()
  await user.click(within(lookup()).getByRole('radio', { name: text.movementLookupJSON }))
  fireEvent.change(field(), { target: { value: quoteExactText(legacy) } })
  await user.click(choose())
  expect(api.fetchLocationMovements).not.toHaveBeenCalled()
  expect(within(lookup()).getByText(text.movementLookupSelected)).toBeVisible()
  await user.click(within(panel()).getByRole('button', { name: text.movementsOpen }))
  expect(await within(panel()).findByText(text.movementsDeleted)).toBeVisible()
  expect(within(panel()).getByText(`${text.movementsOwner}：${quoteExactText(legacy)}`)).toBeVisible()
  expect(api.fetchLocationMovements).toHaveBeenCalledWith(legacy, { limit: 20 }, expect.any(AbortSignal))
})

it('preserves plain input exactly and never interprets a missing or malformed ID as an unfiltered read', async () => {
  const user = userEvent.setup(); mount()
  for (const invalid of ['', 'x'.repeat(129), '\ud800']) {
    fireEvent.change(field(), { target: { value: invalid } }); await user.click(choose())
    expect(within(lookup()).getByText(text.movementLookupInvalid)).toBeVisible()
    expect(api.fetchLocationMovements).not.toHaveBeenCalled()
  }
  fireEvent.change(field(), { target: { value: '  room +  ' } }); await user.click(choose())
  await user.click(within(panel()).getByRole('button', { name: text.movementsOpen }))
  await waitFor(() => expect(api.fetchLocationMovements).toHaveBeenCalledWith('  room +  ', { limit: 20 }, expect.any(AbortSignal)))
})

it('accepts 128 astral IDs in JSON escape form and rejects non-string or unpaired-surrogate JSON', async () => {
  const user = userEvent.setup(); mount(); await user.click(within(lookup()).getByRole('radio', { name: text.movementLookupJSON }))
  for (const invalid of ['null', '0', '{}', '"\\ud800"', `"${'x'.repeat(129)}"`]) {
    fireEvent.change(field(), { target: { value: invalid } }); await user.click(choose())
    expect(within(lookup()).getByText(text.movementLookupInvalid)).toBeVisible()
    expect(api.fetchLocationMovements).not.toHaveBeenCalled()
  }
  const escaped = `"${'\\ud83d\\ude00'.repeat(128)}"`
  expect(escaped).toHaveLength(1538)
  fireEvent.change(field(), { target: { value: escaped } }); await user.click(choose())
  await user.click(within(panel()).getByRole('button', { name: text.movementsOpen }))
  await waitFor(() => expect(api.fetchLocationMovements).toHaveBeenCalledWith('😀'.repeat(128), { limit: 20 }, expect.any(AbortSignal)))
})

it('does not change an invalid JSON draft or retain the previous owner while it is edited', async () => {
  const user = userEvent.setup(); mount()
  fireEvent.change(field(), { target: { value: 'previous' } }); await user.click(choose())
  await user.click(within(panel()).getByRole('button', { name: text.movementsOpen }))
  await within(panel()).findByText(text.movementsDeleted)
  await user.click(within(lookup()).getByRole('radio', { name: text.movementLookupJSON }))
  fireEvent.change(field(), { target: { value: '"unfinished' } })
  expect(screen.queryByRole('region', { name: text.movementsTitle })).not.toBeInTheDocument()
  await user.click(choose()); expect(within(lookup()).getByText(text.movementLookupInvalid)).toBeVisible()
  expect(field()).toHaveValue('"unfinished'); expect(api.fetchLocationMovements).toHaveBeenCalledTimes(1)
})

it('aborts an in-flight owner read when the input changes and discards the late result', async () => {
  let release!: (value: api.LocationMovementsRead) => void, signal!: AbortSignal
  vi.mocked(api.fetchLocationMovements).mockImplementation((_id, _query, nextSignal) => { signal = nextSignal; return new Promise((resolve) => { release = resolve }) })
  const user = userEvent.setup(); mount(); fireEvent.change(field(), { target: { value: 'previous' } }); await user.click(choose())
  await user.click(within(panel()).getByRole('button', { name: text.movementsOpen })); await waitFor(() => expect(release).toBeTypeOf('function'))
  fireEvent.change(field(), { target: { value: 'next' } }); expect(signal.aborted).toBe(true)
  await act(async () => { release(result('previous')) })
  expect(screen.queryByRole('region', { name: text.movementsTitle })).not.toBeInTheDocument()
  expect(field()).toHaveValue('next')
})

it('clears input and selected owner on delivered session-clear without sending another read', async () => {
  const user = userEvent.setup(); mount(); fireEvent.change(field(), { target: { value: 'private-id' } }); await user.click(choose())
  await user.click(within(panel()).getByRole('button', { name: text.movementsOpen })); await within(panel()).findByText(text.movementsDeleted)
  act(() => { notifyPageSuspended() })
  expect(within(lookup()).getByText(text.movementLookupCleared)).toBeVisible()
  expect(within(lookup()).queryByRole('textbox')).not.toBeInTheDocument()
  expect(screen.queryByRole('region', { name: text.movementsTitle })).not.toBeInTheDocument()
  await user.click(within(lookup()).getByRole('button', { name: text.movementsLogin }))
  expect(props.onSessionLost).toHaveBeenCalledWith(false); expect(api.fetchLocationMovements).toHaveBeenCalledTimes(1)
})

it.each([false, true])('redacts lookup before pagehide and restores focus only when appropriate (moved=%s)', async (moved) => {
  const user = userEvent.setup(); mount(); fireEvent.change(field(), { target: { value: 'private-id' } }); await user.click(choose())
  await user.click(within(panel()).getByRole('button', { name: text.movementsOpen })); await within(panel()).findByText(text.movementsDeleted)
  act(() => { window.dispatchEvent(new PageTransitionEvent('pagehide', { persisted: true })); window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true })) })
  expect(within(lookup()).queryByRole('textbox')).not.toBeInTheDocument(); expect(screen.queryByRole('region', { name: text.movementsTitle })).not.toBeInTheDocument()
  let release!: (value: boolean) => void; props.revalidate.mockImplementationOnce(() => new Promise((resolve) => { release = resolve }))
  const resume = within(lookup()).getByRole('button', { name: text.movementLookupContinue }); await user.click(resume)
  const other = screen.getByRole('button', { name: '其他位置' }); if (moved) act(() => { other.focus() })
  await act(async () => { release(true) })
  expect(field()).toHaveValue(''); expect(api.fetchLocationMovements).toHaveBeenCalledTimes(1)
  expect(moved ? other : within(lookup()).getByRole('heading', { name: text.movementLookupTitle })).toHaveFocus()
})

it('allows an explicit fresh session check after a previous authentication read fails', async () => {
  const user = userEvent.setup(); mount({ ...props, invalid: true })
  act(() => { window.dispatchEvent(new PageTransitionEvent('pagehide', { persisted: true })); window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true })) })
  const resume = within(lookup()).getByRole('button', { name: text.movementLookupContinue })
  expect(resume).toHaveAttribute('aria-disabled', 'false')
  await user.click(resume)
  expect(props.revalidate).toHaveBeenCalledTimes(1)
  expect(field()).toHaveValue('')
})

it('does not restore the form if a session-clear signal arrives during revalidation', async () => {
  const user = userEvent.setup(); mount()
  act(() => { window.dispatchEvent(new PageTransitionEvent('pagehide', { persisted: true })); window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true })) })
  let release!: (value: boolean) => void; props.revalidate.mockImplementationOnce(() => new Promise((resolve) => { release = resolve }))
  await user.click(within(lookup()).getByRole('button', { name: text.movementLookupContinue }))
  act(() => { notifyPageSuspended() }); await act(async () => { release(true) })
  expect(within(lookup()).getByText(text.movementLookupCleared)).toBeVisible()
  expect(within(lookup()).queryByRole('textbox')).not.toBeInTheDocument(); expect(api.fetchLocationMovements).not.toHaveBeenCalled()
})

it('does not confuse a missing owner with an empty retained history', async () => {
  vi.mocked(api.fetchLocationMovements).mockRejectedValue(new ApiError('NOT_FOUND', { status: 404 }))
  const user = userEvent.setup(); mount(); fireEvent.change(field(), { target: { value: 'missing' } }); await user.click(choose())
  await user.click(within(panel()).getByRole('button', { name: text.movementsOpen }))
  expect(await within(panel()).findByText(text.movementsError)).toBeVisible()
  expect(within(panel()).queryByText(text.movementsEmpty)).not.toBeInTheDocument()
  expect(field()).toHaveValue('missing')
})
