import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as auth from '@/lib/api/auth'
import { ApiError } from '@/lib/api/client'
import * as holdings from '@/lib/api/holdings'
import * as locations from '@/lib/api/locations'
import { messages } from '@/lib/messages'
import * as qr from '@/lib/qr-label-client'
import { encodeQRLabel } from '@/lib/qr-label-encode'
import { makeQRLabelLink } from '@/lib/qr-label-link'
import { notifyPageSuspended } from '@/lib/session-lifecycle'
import { QRLabelPanel } from './qr-label-panel'

vi.mock('@/lib/api/auth', async (original) => ({ ...await original<typeof import('@/lib/api/auth')>(), fetchAdmin: vi.fn() }))
vi.mock('@/lib/api/holdings', async (original) => ({ ...await original<typeof import('@/lib/api/holdings')>(), fetchUnit: vi.fn() }))
vi.mock('@/lib/api/locations', async (original) => ({ ...await original<typeof import('@/lib/api/locations')>(), fetchLocation: vi.fn() }))
vi.mock('@/lib/qr-label-client', () => ({ prepareQRLabel: vi.fn() }))

const text = messages.qr
const admin: auth.Admin = { id: 'owner', username: 'owner', revision: 1, must_change_password: false }
const location: locations.Location = { id: 'room-1', revision: 3, name: 'Study', code: 'ROOM-1', kind: 'room', parent_id: null, sort_order: 0, description: '', status: 'active' }
const unit: holdings.Unit = { id: 'unit-1', item_id: 'item-1', revision: 2, location_id: 'room-1', asset_code: 'ASSET-1', serial_number: null, notes: '', status: 'stored' }
const lost = vi.fn()
const props = { kind: 'location' as const, id: location.id, revision: location.revision, online: true, checked: true, checking: false, invalid: false, onSessionLost: lost }

beforeEach(() => {
  vi.clearAllMocks()
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: true })
  vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
  vi.mocked(auth.fetchAdmin).mockResolvedValue(admin)
  vi.mocked(locations.fetchLocation).mockResolvedValue(location)
  vi.mocked(holdings.fetchUnit).mockResolvedValue(unit)
  vi.mocked(qr.prepareQRLabel).mockImplementation(async (kind, id, origin) => {
    const matrix = encodeQRLabel(kind, id, origin)
    return { kind: 'ready', link: makeQRLabelLink(kind, id, origin), size: matrix.size, modules: matrix.modules }
  })
})
afterEach(() => { vi.restoreAllMocks() })

async function generate() {
  await userEvent.setup().click(screen.getByRole('button', { name: text.create }))
}

describe('local QR label panel', () => {
  it('generates an exact same-origin Location label only after explicit session and source checks', async () => {
    render(<QRLabelPanel {...props} />)
    expect(auth.fetchAdmin).not.toHaveBeenCalled(); expect(locations.fetchLocation).not.toHaveBeenCalled(); expect(qr.prepareQRLabel).not.toHaveBeenCalled()
    await generate()
    expect(await screen.findByRole('img', { name: text.locationImage })).toBeVisible()
    expect(screen.getByText(`${window.location.origin}/l/room-1`)).toBeVisible()
    expect(screen.getByText('"ROOM-1"')).toBeVisible()
    expect(auth.fetchAdmin).toHaveBeenCalledTimes(2); expect(locations.fetchLocation).toHaveBeenCalledTimes(2)
    expect(qr.prepareQRLabel).toHaveBeenCalledWith('location', 'room-1', window.location.origin, expect.any(AbortSignal))
    const image = screen.getByRole('img', { name: text.locationImage })
    expect(image.querySelector('rect')).toHaveAttribute('fill', '#ffffff')
    expect(image.querySelector('path')).toHaveAttribute('fill', '#000000')
    expect(image.querySelector('path')?.getAttribute('d')).toMatch(/^M/)
    expect(holdings.fetchUnit).not.toHaveBeenCalled(); expect(lost).not.toHaveBeenCalled()
  })

  it('reads the actual Unit and does not use an Item or a Location as its identity', async () => {
    render(<QRLabelPanel {...props} kind="unit" id={unit.id} revision={unit.revision} />)
    await generate()
    expect(await screen.findByRole('img', { name: text.unitImage })).toBeVisible()
    expect(screen.getByText(`${window.location.origin}/u/unit-1`)).toBeVisible()
    expect(screen.getByText('"ASSET-1"')).toBeVisible()
    expect(holdings.fetchUnit).toHaveBeenCalledTimes(2); expect(locations.fetchLocation).not.toHaveBeenCalled()
  })

  it('rejects a changed source before or after encoding rather than rendering a stale label', async () => {
    const view = render(<QRLabelPanel {...props} />)
    vi.mocked(locations.fetchLocation).mockResolvedValueOnce({ ...location, revision: 4 })
    await generate()
    expect(await screen.findByRole('alert')).toHaveTextContent(text.changed)
    expect(qr.prepareQRLabel).not.toHaveBeenCalled(); expect(screen.queryByRole('img')).not.toBeInTheDocument()
    view.unmount()
    vi.mocked(locations.fetchLocation).mockReset().mockResolvedValueOnce(location).mockResolvedValueOnce({ ...location, code: 'CHANGED', revision: 4 })
    render(<QRLabelPanel {...props} />)
    await generate()
    expect(await screen.findByRole('alert')).toHaveTextContent(text.changed)
    expect(screen.queryByRole('img')).not.toBeInTheDocument()
  })

  it.each(['principal', 'same-revision code'] as const)('withholds inconsistent %s across the worker interval', async (changed) => {
    if (changed === 'principal') vi.mocked(auth.fetchAdmin).mockResolvedValueOnce(admin).mockResolvedValueOnce({ ...admin, id: 'other-owner' })
    else vi.mocked(locations.fetchLocation).mockResolvedValueOnce(location).mockResolvedValueOnce({ ...location, code: 'unexpected-code' })
    render(<QRLabelPanel {...props} />)
    await generate()
    expect(await screen.findByRole('alert')).toHaveTextContent(text.changed)
    expect(screen.queryByRole('img')).not.toBeInTheDocument()
  })

  it('withholds a failed worker result and does not expose a diagnostic', async () => {
    vi.mocked(qr.prepareQRLabel).mockResolvedValueOnce({ kind: 'failed' })
    render(<QRLabelPanel {...props} />)
    await generate()
    expect(await screen.findByRole('alert')).toHaveTextContent(text.error)
    expect(screen.queryByRole('img')).not.toBeInTheDocument(); expect(locations.fetchLocation).toHaveBeenCalledTimes(1)
  })

  it('withholds unsupported exact identities without altering them', async () => {
    render(<QRLabelPanel {...props} id="legacy+id" />)
    expect(screen.getByText(text.unsupported)).toBeVisible()
    expect(screen.queryByRole('button', { name: text.create })).not.toBeInTheDocument()
    expect(auth.fetchAdmin).not.toHaveBeenCalled(); expect(qr.prepareQRLabel).not.toHaveBeenCalled()
  })

  it('hides the label while parent reads are pending or inconsistent', async () => {
    const view = render(<QRLabelPanel {...props} />)
    await generate(); expect(await screen.findByRole('img')).toBeVisible()
    view.rerender(<QRLabelPanel {...props} checking />)
    expect(screen.queryByRole('img')).not.toBeInTheDocument()
    view.rerender(<QRLabelPanel {...props} invalid />)
    expect(screen.queryByRole('img')).not.toBeInTheDocument()
    view.rerender(<QRLabelPanel {...props} />)
    expect(screen.getByRole('img')).toBeVisible()
  })

  it('never starts or automatically resumes while offline', async () => {
    const view = render(<QRLabelPanel {...props} online={false} />)
    expect(screen.getByRole('button', { name: text.create })).toHaveAttribute('aria-disabled', 'true')
    await generate(); expect(auth.fetchAdmin).not.toHaveBeenCalled()
    view.rerender(<QRLabelPanel {...props} />)
    expect(auth.fetchAdmin).not.toHaveBeenCalled()
    await generate(); expect(await screen.findByRole('img')).toBeVisible()
    Object.defineProperty(navigator, 'onLine', { configurable: true, value: false })
    act(() => window.dispatchEvent(new Event('offline')))
    expect(screen.queryByRole('img')).not.toBeInTheDocument()
    Object.defineProperty(navigator, 'onLine', { configurable: true, value: true })
    act(() => window.dispatchEvent(new Event('online')))
    expect(screen.queryByRole('img')).not.toBeInTheDocument(); expect(auth.fetchAdmin).toHaveBeenCalledTimes(2)
  })

  it.each(['hidden', 'pagehide', 'session'] as const)('discards pending and displayed labels on %s', async (mode) => {
    let release: ((value: qr.QRLabelPreparation) => void) | undefined
    vi.mocked(qr.prepareQRLabel).mockImplementationOnce(() => new Promise((resolve) => { release = resolve }))
    render(<QRLabelPanel {...props} />)
    await generate(); await waitFor(() => expect(release).toBeDefined())
    const matrix = encodeQRLabel('location', location.id, window.location.origin)
    act(() => {
      if (mode === 'hidden') { vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden'); document.dispatchEvent(new Event('visibilitychange')) }
      if (mode === 'pagehide') window.dispatchEvent(new Event('pagehide'))
      if (mode === 'session') notifyPageSuspended()
      release?.({ kind: 'ready', link: makeQRLabelLink('location', location.id, window.location.origin), size: matrix.size, modules: matrix.modules })
    })
    await waitFor(() => expect(screen.queryByRole('img')).not.toBeInTheDocument())
    expect(locations.fetchLocation).toHaveBeenCalledTimes(1)
  })

  it('requires an explicit fresh action after actual page restoration', async () => {
    render(<QRLabelPanel {...props} />)
    await generate(); expect(await screen.findByRole('img')).toBeVisible()
    act(() => { window.dispatchEvent(new Event('pagehide')); window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true })) })
    expect(screen.queryByRole('img')).not.toBeInTheDocument()
    expect(screen.getByText(text.restore)).toBeVisible()
    expect(auth.fetchAdmin).toHaveBeenCalledTimes(2)
    await userEvent.setup().click(screen.getByRole('button', { name: text.continue }))
    expect(await screen.findByRole('img')).toBeVisible(); expect(auth.fetchAdmin).toHaveBeenCalledTimes(4)
  })

  it('shows restoration failures and allows an explicit retry', async () => {
    render(<QRLabelPanel {...props} />)
    act(() => { window.dispatchEvent(new Event('pagehide')); window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true })) })
    vi.mocked(auth.fetchAdmin).mockRejectedValueOnce(new ApiError('NETWORK_ERROR'))
    await userEvent.setup().click(screen.getByRole('button', { name: text.continue }))
    expect(await screen.findByRole('alert')).toHaveTextContent(text.error)
    expect(screen.getByText(text.restore)).toBeVisible(); expect(screen.queryByRole('img')).not.toBeInTheDocument()
    await userEvent.setup().click(screen.getByRole('button', { name: text.continue }))
    expect(await screen.findByRole('img')).toBeVisible()
  })

  it('aborts a restored-page request immediately when the page becomes hidden', async () => {
    let release: ((value: qr.QRLabelPreparation) => void) | undefined
    vi.mocked(qr.prepareQRLabel).mockImplementationOnce(() => new Promise((resolve) => { release = resolve }))
    render(<QRLabelPanel {...props} />)
    act(() => { window.dispatchEvent(new Event('pagehide')); window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true })) })
    await userEvent.setup().click(screen.getByRole('button', { name: text.continue }))
    await waitFor(() => expect(release).toBeDefined())
    const signal = vi.mocked(qr.prepareQRLabel).mock.calls[0]![3]
    act(() => { vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden'); document.dispatchEvent(new Event('visibilitychange')) })
    expect(signal.aborted).toBe(true)
    const matrix = encodeQRLabel('location', location.id, window.location.origin)
    await act(async () => { release?.({ kind: 'ready', link: makeQRLabelLink('location', location.id, window.location.origin), size: matrix.size, modules: matrix.modules }) })
    expect(screen.queryByRole('img')).not.toBeInTheDocument(); expect(locations.fetchLocation).toHaveBeenCalledTimes(1)
  })

  it('redirects session failures without exposing a QR or raw errors', async () => {
    vi.mocked(auth.fetchAdmin).mockRejectedValueOnce(new ApiError('AUTHENTICATION_REQUIRED', { status: 401 }))
    render(<QRLabelPanel {...props} />)
    await generate()
    await waitFor(() => expect(lost).toHaveBeenCalledWith(false))
    expect(screen.queryByRole('img')).not.toBeInTheDocument(); expect(qr.prepareQRLabel).not.toHaveBeenCalled()
  })

  it('rechecks authorization after the worker finishes', async () => {
    vi.mocked(auth.fetchAdmin).mockResolvedValueOnce(admin).mockRejectedValueOnce(new ApiError('AUTHENTICATION_REQUIRED', { status: 401 }))
    render(<QRLabelPanel {...props} />)
    await generate()
    await waitFor(() => expect(lost).toHaveBeenCalledWith(false))
    expect(qr.prepareQRLabel).toHaveBeenCalledTimes(1); expect(screen.queryByRole('img')).not.toBeInTheDocument()
  })

  it('handles mandatory password changes before reading the source', async () => {
    vi.mocked(auth.fetchAdmin).mockResolvedValueOnce({ ...admin, must_change_password: true })
    render(<QRLabelPanel {...props} />)
    await generate()
    await waitFor(() => expect(lost).toHaveBeenCalledWith(true))
    expect(locations.fetchLocation).not.toHaveBeenCalled(); expect(qr.prepareQRLabel).not.toHaveBeenCalled()
  })
})
