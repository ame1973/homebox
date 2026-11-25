import { MemoryRouter } from 'react-router'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/client'
import * as api from '@/lib/api/locations'
import { messages } from '@/lib/messages'
import { notifyPageSuspended } from '@/lib/session-lifecycle'
import { LocationContentsPanel } from './location-contents-panel'

vi.mock('@/lib/api/locations', async (original) => ({ ...await original<typeof import('@/lib/api/locations')>(), fetchLocationContents: vi.fn() }))
const text = messages.locations
const unit = { id: 'unit\0/😀', item_id: 'item', location_id: 'room', asset_code: 'ASSET‮A', status: 'disposed' as const, serial_number: null, notes: '保留備註', revision: 1 }
const stock = { item_id: 'item/legacy', location_id: 'room', quantity_base: 0, unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1, revision: 2 }
const child = { id: 'new', parent_id: 'room', name: '箱子', code: 'BOX', kind: 'box', status: 'active' as const, sort_order: 0, description: '', revision: 1 }
function page(changes: Partial<api.LocationContents> = {}, revision = 3): api.LocationContentsRead {
  return { revision, value: { location_id: 'room', scope: 'subtree', units: [], stock: [], sub_locations: [], next_cursor: null, ...changes } }
}
const props = { id: 'room', revision: 3, online: true, checked: true, checking: false, invalid: false, revalidate: vi.fn(), onSessionLost: vi.fn() }
const region = () => screen.getByRole('region', { name: text.contentsTitle })
const open = () => within(region()).getByRole('button', { name: text.contentsOpen })
const restart = () => within(region()).getByRole('button', { name: text.contentsRestart })
const next = () => within(region()).getByRole('button', { name: text.contentsNext })
const previous = () => within(region()).getByRole('button', { name: text.contentsPrevious })
const revalidate = props.revalidate

beforeEach(() => {
  vi.clearAllMocks()
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: true })
  revalidate.mockResolvedValue(true)
  vi.mocked(api.fetchLocationContents).mockReset().mockResolvedValue(page())
})
afterEach(() => { vi.restoreAllMocks() })
function mount(values = props) {
  const view = render(<MemoryRouter><LocationContentsPanel {...values} /><button type="button">其他位置</button></MemoryRouter>)
  return { ...view, update: (changes: Partial<typeof props>) => view.rerender(<MemoryRouter><LocationContentsPanel {...props} {...changes} /><button type="button">其他位置</button></MemoryRouter>) }
}

it('opens one real scoped page explicitly and preserves retained records and unsafe identities', async () => {
  vi.mocked(api.fetchLocationContents).mockResolvedValue(page({ units: [unit], stock: [stock], sub_locations: [child] }))
  mount()
  expect(api.fetchLocationContents).not.toHaveBeenCalled()
  await userEvent.setup().click(open())
  expect(await within(region()).findByText(text.contentsUnits)).toBeVisible()
  expect(api.fetchLocationContents).toHaveBeenCalledWith('room', { scope: 'subtree', limit: 20 }, expect.any(AbortSignal))
  expect(within(region()).getByText(/unit\\u0000\/😀/)).toBeVisible()
  expect(within(region()).getByText(/ASSET\\u202eA/)).toBeVisible()
  expect(within(region()).getByText(messages.holdings.fields.status, { exact: true })).toBeVisible()
  expect(within(region()).getByText(text.status, { exact: true })).toBeVisible()
  expect(within(region()).getByText(messages.holdings.statuses.disposed)).toBeVisible()
  expect(within(region()).getByText(text.contentsZero)).toBeVisible()
  expect(within(region()).getByText(/item\/legacy/)).toBeVisible()
  expect(within(region()).getByRole('link', { name: `${text.contentsLocationLink}："new"` })).toHaveAttribute('href', '/locations/new/view')
  expect(within(region()).queryByRole('link', { name: new RegExp(`^${text.contentsUnitLink}`) })).not.toBeInTheDocument()
  expect(within(region()).getByText(text.contentsEnd)).toBeVisible()
  expect(within(region()).getByText(/不代表實際所在位置/)).toBeVisible()
})

it('gives each repeated detail action an accessible exact identity', async () => {
  vi.mocked(api.fetchLocationContents).mockResolvedValue(page({
    units: [{ ...unit, id: 'unit-1', item_id: 'item-1' }, { ...unit, id: 'unit-2', item_id: 'item-2' }],
    stock: [{ ...stock, item_id: 'item-3' }], sub_locations: [{ ...child, id: 'location-1' }],
  }))
  const user = userEvent.setup(); mount(); await user.click(open())
  expect(await within(region()).findByRole('link', { name: `${text.contentsUnitLink}："unit-1"` })).toHaveAttribute('href', '/units/unit-1')
  expect(within(region()).getByRole('link', { name: `${text.contentsUnitLink}："unit-2"` })).toHaveAttribute('href', '/units/unit-2')
  expect(within(region()).getByRole('link', { name: `${text.contentsItemLink}："item-3"` })).toHaveAttribute('href', '/items/item-3')
  expect(within(region()).getByRole('link', { name: `${text.contentsLocationLink}："location-1"` })).toHaveAttribute('href', '/locations/location-1')
})

it('withholds ambiguous router paths and a Unit whose owning Item cannot be addressed', async () => {
  vi.mocked(api.fetchLocationContents).mockResolvedValue(page({
    units: [{ ...unit, id: 'literal%2Funit' }, { ...unit, id: 'safe-unit', item_id: 'owner/legacy' }],
    stock: [{ ...stock, item_id: 'literal%2Fitem' }],
    sub_locations: [{ ...child, id: 'tree' }, { ...child, id: 'literal%2Flocation' }],
  }))
  const user = userEvent.setup(); mount(); await user.click(open())
  await within(region()).findByText('"literal%2Funit"')
  expect(within(region()).queryByRole('link', { name: new RegExp(`^${text.contentsUnitLink}`) })).not.toBeInTheDocument()
  expect(within(region()).queryByRole('link', { name: new RegExp(`^${text.contentsLocationLink}`) })).not.toBeInTheDocument()
  expect(within(region()).getAllByRole('link', { name: new RegExp(`^${text.contentsItemLink}`) })).toHaveLength(1)
  expect(within(region()).getByText('"owner/legacy"')).toBeVisible()
  expect(within(region()).getByText('"tree"')).toBeVisible()
})

it('switches scope explicitly and clears prior pages without inferring unseen results', async () => {
  vi.mocked(api.fetchLocationContents).mockResolvedValueOnce(page({ units: [unit], next_cursor: 'next' }))
    .mockResolvedValueOnce(page({ scope: 'direct' }))
  const user = userEvent.setup(); mount(); await user.click(open())
  expect(await within(region()).findByText(text.contentsMore)).toBeVisible()
  await user.click(within(region()).getByRole('radio', { name: text.contentsDirect }))
  expect(await within(region()).findByText(text.contentsEmpty)).toBeVisible()
  expect(within(region()).queryByText(text.contentsUnits)).not.toBeInTheDocument()
  expect(api.fetchLocationContents).toHaveBeenLastCalledWith('room', { scope: 'direct', limit: 20 }, expect.any(AbortSignal))
})

it('pages explicitly, retains focus, and distinguishes an empty continuation from an empty collection', async () => {
  vi.mocked(api.fetchLocationContents).mockResolvedValueOnce(page({ units: [unit], next_cursor: 'next' })).mockResolvedValueOnce(page())
  const user = userEvent.setup(); mount(); await user.click(open()); await within(region()).findByText(text.contentsUnits)
  const button = next(); await user.click(button)
  expect(await within(region()).findByText(text.contentsEmptyPage)).toBeVisible()
  expect(within(region()).queryByText(text.contentsEmpty)).not.toBeInTheDocument()
  expect(button).toHaveFocus(); expect(button).toHaveAttribute('aria-disabled', 'true')
  await user.click(previous()); expect(within(region()).getByText(text.contentsUnits)).toBeVisible()
  await user.click(next()); expect(within(region()).getByText(text.contentsEmptyPage)).toBeVisible()
  expect(api.fetchLocationContents).toHaveBeenCalledTimes(2)
})

it.each(['duplicate', 'cycle', 'revision', 'invalid cursor'] as const)('withholds a %s page until explicit restart', async (kind) => {
  vi.mocked(api.fetchLocationContents).mockResolvedValueOnce(page({ units: [unit], next_cursor: 'one' }))
  if (kind === 'duplicate') vi.mocked(api.fetchLocationContents).mockResolvedValueOnce(page({ units: [unit] }))
  if (kind === 'cycle') vi.mocked(api.fetchLocationContents).mockResolvedValueOnce(page({ next_cursor: 'one' }))
  if (kind === 'revision') vi.mocked(api.fetchLocationContents).mockResolvedValueOnce(page({}, 4))
  if (kind === 'invalid cursor') vi.mocked(api.fetchLocationContents).mockRejectedValueOnce(new ApiError('INVALID_CURSOR', { status: 400 }))
  vi.mocked(api.fetchLocationContents).mockResolvedValueOnce(page())
  const user = userEvent.setup(); mount(); await user.click(open()); await within(region()).findByText(text.contentsUnits)
  await user.click(next())
  expect(await within(region()).findByText(kind === 'revision' || kind === 'invalid cursor' ? text.contentsChanged : text.contentsError)).toBeVisible()
  expect(within(region()).queryByText(text.contentsUnits)).not.toBeInTheDocument()
  await user.click(restart()); expect(await within(region()).findByText(text.contentsEmpty)).toBeVisible()
  expect(api.fetchLocationContents).toHaveBeenCalledTimes(3)
})

it('hides changed Location revisions, failed parent reads and offline data without automatic retry', async () => {
  vi.mocked(api.fetchLocationContents).mockResolvedValue(page({ units: [unit] }))
  const user = userEvent.setup(), view = mount(); await user.click(open()); await within(region()).findByText(text.contentsUnits)
  view.update({ revision: 4 }); expect(within(region()).getByText(text.contentsChanged)).toBeVisible()
  expect(within(region()).queryByText(text.contentsUnits)).not.toBeInTheDocument()
  view.update({ invalid: true }); expect(within(region()).getByText(text.contentsError)).toBeVisible()
  view.update({ invalid: false });
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: false }); fireEvent(window, new Event('offline')); view.update({ online: false })
  expect(within(region()).getByText(text.contentsOffline)).toBeVisible()
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: true }); fireEvent(window, new Event('online')); view.update({ online: true })
  expect(api.fetchLocationContents).toHaveBeenCalledTimes(1)
  expect(within(region()).getByText(text.contentsOffline)).toBeVisible()
  await user.click(restart()); expect(await within(region()).findByText(text.contentsUnits)).toBeVisible()
  expect(api.fetchLocationContents).toHaveBeenCalledTimes(2)
})

it('does not retry a failed read during parent revalidation and allows explicit recovery', async () => {
  vi.mocked(api.fetchLocationContents).mockRejectedValueOnce(new ApiError('NETWORK_ERROR')).mockResolvedValueOnce(page())
  const user = userEvent.setup(), view = mount(); await user.click(open())
  expect(await within(region()).findByText(text.contentsError)).toBeVisible()
  view.update({ checking: true }); view.update({ checking: false })
  expect(api.fetchLocationContents).toHaveBeenCalledTimes(1)
  await user.click(restart()); expect(await within(region()).findByText(text.contentsEmpty)).toBeVisible()
})

it('does not steal focus when a delayed next page finishes after the user moves elsewhere', async () => {
  let release!: (value: api.LocationContentsRead) => void
  vi.mocked(api.fetchLocationContents).mockResolvedValueOnce(page({ units: [unit], next_cursor: 'next' }))
    .mockImplementationOnce(() => new Promise((resolve) => { release = resolve }))
  const user = userEvent.setup(); mount(); await user.click(open()); await within(region()).findByText(text.contentsUnits)
  await user.click(next()); await waitFor(() => expect(release).toBeTypeOf('function'))
  expect(next()).toHaveFocus(); expect(next()).toHaveAttribute('aria-disabled', 'true')
  const other = screen.getByRole('button', { name: '其他位置' }); act(() => { other.focus() })
  await act(async () => { release(page()) })
  expect(await within(region()).findByText(text.contentsEmptyPage)).toBeVisible()
  expect(other).toHaveFocus()
})

it('aborts and discards a delayed result on delivered session clear', async () => {
  let release!: (value: api.LocationContentsRead) => void, signal!: AbortSignal
  vi.mocked(api.fetchLocationContents).mockImplementation((_id, _query, nextSignal) => { signal = nextSignal; return new Promise((resolve) => { release = resolve }) })
  const user = userEvent.setup(); mount(); await user.click(open()); await waitFor(() => expect(release).toBeTypeOf('function'))
  act(() => { notifyPageSuspended() }); expect(signal.aborted).toBe(true)
  expect(within(region()).getByText(text.contentsCleared)).toBeVisible()
  await act(async () => { release(page({ units: [unit] })) })
  expect(within(region()).queryByText(text.contentsUnits)).not.toBeInTheDocument()
  await user.click(within(region()).getByRole('button', { name: text.contentsLogin }))
  expect(props.onSessionLost).toHaveBeenCalledWith(false)
})

it.each([false, true])('redacts before pagehide and restores focus only if continuation is still focused (moved=%s)', async (moved) => {
  vi.mocked(api.fetchLocationContents).mockResolvedValue(page({ units: [unit] }))
  const user = userEvent.setup(); mount(); await user.click(open()); await within(region()).findByText(text.contentsUnits)
  act(() => { window.dispatchEvent(new PageTransitionEvent('pagehide', { persisted: true })) })
  expect(within(region()).queryByText(text.contentsUnits)).not.toBeInTheDocument()
  act(() => { window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true })) })
  expect(within(region()).getByText(text.contentsRestore)).toBeVisible()
  let release!: (value: boolean) => void
  revalidate.mockImplementationOnce(() => new Promise((resolve) => { release = resolve }))
  const resume = within(region()).getByRole('button', { name: text.contentsContinue }); await user.click(resume)
  expect(resume).toHaveFocus(); expect(resume).toHaveAttribute('aria-disabled', 'true')
  fireEvent.click(resume); expect(revalidate).toHaveBeenCalledTimes(2)
  const other = screen.getByRole('button', { name: '其他位置' }); if (moved) act(() => { other.focus() })
  await act(async () => { release(true) })
  expect(await within(region()).findByText(text.contentsUnits)).toBeVisible()
  expect(moved ? other : within(region()).getByRole('heading', { name: text.contentsTitle })).toHaveFocus()
})

it('discards an in-flight page before pagehide and does not install it after restoration', async () => {
  let release!: (value: api.LocationContentsRead) => void, signal!: AbortSignal
  vi.mocked(api.fetchLocationContents).mockImplementationOnce((_id, _query, nextSignal) => { signal = nextSignal; return new Promise((resolve) => { release = resolve }) }).mockResolvedValueOnce(page())
  const user = userEvent.setup(); mount(); await user.click(open()); await waitFor(() => expect(release).toBeTypeOf('function'))
  act(() => { window.dispatchEvent(new PageTransitionEvent('pagehide', { persisted: true })); window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true })) })
  expect(signal.aborted).toBe(true)
  await act(async () => { release(page({ units: [unit] })) })
  expect(within(region()).getByText(text.contentsRestore)).toBeVisible()
  expect(within(region()).queryByText(text.contentsUnits)).not.toBeInTheDocument()
  expect(api.fetchLocationContents).toHaveBeenCalledTimes(1)
  await user.click(within(region()).getByRole('button', { name: text.contentsContinue }))
  expect(await within(region()).findByText(text.contentsEmpty)).toBeVisible()
  expect(api.fetchLocationContents).toHaveBeenCalledTimes(2)
})

it('rechecks a failed parent read before requesting a replacement page', async () => {
  revalidate.mockResolvedValueOnce(false).mockResolvedValueOnce(true)
  const user = userEvent.setup(); mount(); await user.click(open())
  expect(await within(region()).findByText(text.contentsError)).toBeVisible()
  expect(api.fetchLocationContents).not.toHaveBeenCalled()
  await user.click(restart()); expect(await within(region()).findByText(text.contentsEmpty)).toBeVisible()
  expect(api.fetchLocationContents).toHaveBeenCalledTimes(1)
})

it('ignores a resumed read if session clear arrives during revalidation', async () => {
  vi.mocked(api.fetchLocationContents).mockResolvedValue(page({ units: [unit] }))
  const user = userEvent.setup(); mount(); await user.click(open()); await within(region()).findByText(text.contentsUnits)
  act(() => { window.dispatchEvent(new PageTransitionEvent('pagehide', { persisted: true })); window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true })) })
  let release!: (value: boolean) => void; revalidate.mockImplementationOnce(() => new Promise((resolve) => { release = resolve }))
  await user.click(within(region()).getByRole('button', { name: text.contentsContinue }))
  act(() => { notifyPageSuspended() }); await act(async () => { release(true) })
  expect(within(region()).getByText(text.contentsCleared)).toBeVisible()
  expect(api.fetchLocationContents).toHaveBeenCalledTimes(1)
})

it.each([[401, 'AUTHENTICATION_REQUIRED', false], [403, 'PASSWORD_CHANGE_REQUIRED', true]] as const)('propagates session failure %s safely', async (status, code, mandatory) => {
  vi.mocked(api.fetchLocationContents).mockRejectedValue(new ApiError(code, { status }))
  mount(); await userEvent.setup().click(open())
  await waitFor(() => expect(props.onSessionLost).toHaveBeenCalledWith(mandatory))
  expect(within(region()).queryByText(text.contentsEmpty)).not.toBeInTheDocument()
})
