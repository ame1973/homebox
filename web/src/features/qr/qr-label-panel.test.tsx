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

  it('offers an isolated print and an SVG download, then revokes the volatile URL on session loss', async () => {
    const createDescriptor = Object.getOwnPropertyDescriptor(URL, 'createObjectURL'), revokeDescriptor = Object.getOwnPropertyDescriptor(URL, 'revokeObjectURL')
    const create = vi.fn((_blob: Blob) => 'blob:homebox-qr-test'), revoke = vi.fn()
    Object.defineProperty(URL, 'createObjectURL', { configurable: true, value: create })
    Object.defineProperty(URL, 'revokeObjectURL', { configurable: true, value: revoke })
    try {
      const view = render(<QRLabelPanel {...props} />)
      expect(screen.queryByRole('link', { name: text.download })).not.toBeInTheDocument()
      await generate(); expect(await screen.findByRole('img')).toBeVisible()
      const download = await screen.findByRole('link', { name: text.download })
      expect(download).toHaveAttribute('href', 'blob:homebox-qr-test')
      expect(download).toHaveAttribute('download', 'homebox-location-qr.svg')
      expect(create).toHaveBeenCalledTimes(1)
      const blob = create.mock.calls[0]![0] as Blob
      expect(blob.type).toBe('image/svg+xml')
      const svg = await new Promise<string>((resolve, reject) => {
        const reader = new FileReader(); reader.onerror = () => reject(reader.error); reader.onload = () => resolve(String(reader.result)); reader.readAsText(blob)
      })
      const document = new DOMParser().parseFromString(svg, 'image/svg+xml')
      expect(document.querySelectorAll('rect')).toHaveLength(1); expect(document.querySelectorAll('path')).toHaveLength(1)
      expect(document.querySelector('script, a, image, foreignObject')).toBeNull(); expect(svg).not.toContain('ROOM-1')
      const frame = view.container.querySelector('iframe')!
      expect(frame).toHaveAttribute('aria-hidden', 'true'); expect(frame).toHaveAttribute('tabindex', '-1')
      const printing = vi.fn(), child = frame.contentWindow!
      child.print = printing
      await userEvent.setup().click(screen.getByRole('button', { name: text.print }))
      expect(printing).toHaveBeenCalledTimes(1)
      expect(child.document.title).toBe('HomeBox QR')
      expect(child.document.body.textContent).toContain('"ROOM-1"'); expect(child.document.body.textContent).toContain('"room-1"')
      expect(child.document.querySelectorAll('svg')).toHaveLength(1); expect(child.document.querySelector('script, a, img, iframe')).toBeNull()
      await userEvent.setup().click(screen.getByRole('button', { name: text.print }))
      expect(printing).toHaveBeenCalledTimes(1)
      child.dispatchEvent(new Event('afterprint'))
      expect(child.document.body.childElementCount).toBe(0)
      await userEvent.setup().click(screen.getByRole('button', { name: text.print }))
      expect(printing).toHaveBeenCalledTimes(2)
      act(() => { notifyPageSuspended(); expect(child.document.body.childElementCount).toBe(0) })
      expect(revoke).toHaveBeenCalledWith('blob:homebox-qr-test')
      expect(view.container.querySelector('iframe')).toBeNull(); expect(screen.queryByRole('link', { name: text.download })).not.toBeInTheDocument()
      view.unmount()
    } finally {
      if (createDescriptor) Object.defineProperty(URL, 'createObjectURL', createDescriptor); else Reflect.deleteProperty(URL, 'createObjectURL')
      if (revokeDescriptor) Object.defineProperty(URL, 'revokeObjectURL', revokeDescriptor); else Reflect.deleteProperty(URL, 'revokeObjectURL')
    }
  })

  it('revokes an object URL created reentrantly during a delivered session-clear', async () => {
    const createDescriptor = Object.getOwnPropertyDescriptor(URL, 'createObjectURL'), revokeDescriptor = Object.getOwnPropertyDescriptor(URL, 'revokeObjectURL')
    const revoke = vi.fn()
    Object.defineProperty(URL, 'createObjectURL', { configurable: true, value: () => { notifyPageSuspended(); return 'blob:late-qr' } })
    Object.defineProperty(URL, 'revokeObjectURL', { configurable: true, value: revoke })
    try {
      const view = render(<QRLabelPanel {...props} />)
      await generate()
      await waitFor(() => expect(revoke).toHaveBeenCalledWith('blob:late-qr'))
      expect(screen.queryByRole('img')).not.toBeInTheDocument(); expect(screen.queryByRole('link', { name: text.download })).not.toBeInTheDocument()
      view.unmount()
    } finally {
      if (createDescriptor) Object.defineProperty(URL, 'createObjectURL', createDescriptor); else Reflect.deleteProperty(URL, 'createObjectURL')
      if (revokeDescriptor) Object.defineProperty(URL, 'revokeObjectURL', revokeDescriptor); else Reflect.deleteProperty(URL, 'revokeObjectURL')
    }
  })

  it('revokes the preceding SVG URL on regeneration and the new URL on unmount', async () => {
    const createDescriptor = Object.getOwnPropertyDescriptor(URL, 'createObjectURL'), revokeDescriptor = Object.getOwnPropertyDescriptor(URL, 'revokeObjectURL')
    const create = vi.fn().mockReturnValueOnce('blob:first-qr').mockReturnValueOnce('blob:second-qr'), revoke = vi.fn()
    Object.defineProperty(URL, 'createObjectURL', { configurable: true, value: create })
    Object.defineProperty(URL, 'revokeObjectURL', { configurable: true, value: revoke })
    try {
      const view = render(<QRLabelPanel {...props} />)
      await generate(); expect(await screen.findByRole('link', { name: text.download })).toHaveAttribute('href', 'blob:first-qr')
      await userEvent.setup().click(screen.getByRole('button', { name: text.again }))
      await waitFor(() => expect(screen.getByRole('link', { name: text.download })).toHaveAttribute('href', 'blob:second-qr'))
      expect(revoke).toHaveBeenCalledTimes(1); expect(revoke).toHaveBeenCalledWith('blob:first-qr')
      view.unmount(); expect(revoke).toHaveBeenCalledTimes(2); expect(revoke).toHaveBeenLastCalledWith('blob:second-qr')
    } finally {
      if (createDescriptor) Object.defineProperty(URL, 'createObjectURL', createDescriptor); else Reflect.deleteProperty(URL, 'createObjectURL')
      if (revokeDescriptor) Object.defineProperty(URL, 'revokeObjectURL', revokeDescriptor); else Reflect.deleteProperty(URL, 'revokeObjectURL')
    }
  })

  it('rejects reentrant print and download clicks after a local session-clear signal', async () => {
    const createDescriptor = Object.getOwnPropertyDescriptor(URL, 'createObjectURL'), revokeDescriptor = Object.getOwnPropertyDescriptor(URL, 'revokeObjectURL')
    Object.defineProperty(URL, 'createObjectURL', { configurable: true, value: () => 'blob:reentrant-qr' })
    Object.defineProperty(URL, 'revokeObjectURL', { configurable: true, value: vi.fn() })
    try {
      const view = render(<QRLabelPanel {...props} />)
      await generate()
      const button = await screen.findByRole('button', { name: text.print }), download = screen.getByRole('link', { name: text.download })
      const child = view.container.querySelector('iframe')!.contentWindow!, printing = vi.fn()
      child.print = printing
      let downloadAllowed = true
      const reenter = () => {
        button.click()
        downloadAllowed = download.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
      }
      window.addEventListener('homebox:session-cleared', reenter, { once: true })
      act(() => notifyPageSuspended())
      expect(printing).not.toHaveBeenCalled(); expect(downloadAllowed).toBe(false)
      expect(screen.queryByRole('img')).not.toBeInTheDocument(); view.unmount()
    } finally {
      if (createDescriptor) Object.defineProperty(URL, 'createObjectURL', createDescriptor); else Reflect.deleteProperty(URL, 'createObjectURL')
      if (revokeDescriptor) Object.defineProperty(URL, 'revokeObjectURL', revokeDescriptor); else Reflect.deleteProperty(URL, 'revokeObjectURL')
    }
  })

  it('omits an oversized Unit code from the printout without changing its identity', async () => {
    vi.mocked(holdings.fetchUnit).mockResolvedValue({ ...unit, asset_code: 'x'.repeat(300) })
    const view = render(<QRLabelPanel {...props} kind="unit" id={unit.id} revision={unit.revision} />)
    await generate(); expect(await screen.findByRole('img')).toBeVisible()
    const child = view.container.querySelector('iframe')!.contentWindow!
    child.print = vi.fn()
    await userEvent.setup().click(screen.getByRole('button', { name: text.print }))
    expect(child.document.body.textContent).toContain(text.printCodeOmitted)
    expect(child.document.body.textContent).toContain('"unit-1"'); expect(child.document.body.textContent).not.toContain('x'.repeat(300))
  })

  it('reports a print failure without removing the preview and allows a retry', async () => {
    const view = render(<QRLabelPanel {...props} />)
    await generate(); expect(await screen.findByRole('img')).toBeVisible()
    const child = view.container.querySelector('iframe')!.contentWindow!
    child.print = () => { throw new Error('private printer detail') }
    await userEvent.setup().click(screen.getByRole('button', { name: text.print }))
    expect(screen.getByRole('alert')).toHaveTextContent(text.printError); expect(child.document.body.childElementCount).toBe(0)
    expect(screen.getByRole('img')).toBeVisible()
    child.print = vi.fn()
    await userEvent.setup().click(screen.getByRole('button', { name: text.print }))
    expect(screen.queryByRole('alert')).not.toBeInTheDocument(); expect(child.print).toHaveBeenCalledTimes(1)
  })

  it('moves focus only from a disappearing print control during parent revalidation', async () => {
    const view = render(<QRLabelPanel {...props} />)
    await generate(); expect(await screen.findByRole('img')).toBeVisible()
    const frame = view.container.querySelector('iframe')!, child = frame.contentWindow!, printDocument = child.document
    child.print = vi.fn()
    await userEvent.setup().click(screen.getByRole('button', { name: text.print }))
    expect(printDocument.body.childElementCount).toBeGreaterThan(0)
    view.rerender(<QRLabelPanel {...props} checking />)
    expect(printDocument.body.childElementCount).toBe(0)
    expect(screen.getByRole('heading', { name: text.title })).toHaveFocus()
  })

  it('does not steal focus from another control when the print controls disappear', async () => {
    const view = render(<><QRLabelPanel {...props} /><button type="button">Outside action</button></>)
    await generate(); expect(await screen.findByRole('img')).toBeVisible()
    screen.getByRole('button', { name: text.print }).focus(); screen.getByRole('button', { name: 'Outside action' }).focus()
    view.rerender(<><QRLabelPanel {...props} checking /><button type="button">Outside action</button></>)
    expect(screen.getByRole('button', { name: 'Outside action' })).toHaveFocus()
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
