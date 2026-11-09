import { QueryClientProvider } from '@tanstack/react-query'
import { createMemoryRouter } from 'react-router'
import { RouterProvider } from 'react-router/dom'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeAll, beforeEach, expect, it, vi } from 'vitest'
import App from '@/App'
import * as auth from '@/lib/api/auth'
import { ApiError } from '@/lib/api/client'
import * as finance from '@/lib/api/financial-corrections'
import type { PurchaseLine } from '@/lib/api/purchases'
import { messages } from '@/lib/messages'
import { createQueryClient } from '@/lib/query'
import * as models from '@/features/purchases/correction-model'

vi.mock('@/lib/api/auth', async (original) => ({ ...await original<typeof import('@/lib/api/auth')>(), fetchAdmin: vi.fn(), fetchCSRF: vi.fn() }))
vi.mock('@/features/purchases/correction-model', async (original) => ({ ...await original<typeof import('@/features/purchases/correction-model')>(), readPurchaseCorrectionHistory: vi.fn() }))
vi.mock('@/lib/api/financial-corrections', async (original) => ({ ...await original<typeof import('@/lib/api/financial-corrections')>(), correctPurchaseLine: vi.fn() }))
const text = messages.purchaseCorrectionHistory, purchaseText = messages.purchases
const owner: auth.Admin = { id: 'owner', revision: 2, username: 'owner', must_change_password: false }
const old: PurchaseLine = { id: 'line/legacy', revision: 7, purchase_id: 'purchase', item_id: 'item', item_unit_id: 'legacy/unit', quantity_base: 3, paid_minor: 0, note: 'Old allocation\n' }
const replacement = { ...old, id: 'replacement', revision: 1 as const, item_id: 'other', item_unit_id: null, quantity_base: 5, paid_minor: 250, note: 'New allocation' } satisfies PurchaseLine
const original: models.PurchaseCorrectionHistory = {
  purchase: { id: 'purchase', revision: 12, merchant_name: 'Exact merchant', purchased_at: '2026-11-07T10:00:00.123456789+08:00', currency: 'JPY', receipt_note: '' },
  lines: [replacement], corrections: [{ id: 'entry-z', revision: 1, purchase_id: 'purchase', action: 'replace', reason: 'Exact reason\n', created_at: '2026-11-07T02:00:00.123456789Z', before: old, after: replacement }],
}
let administrator: auth.Admin | null, source: models.PurchaseCorrectionHistory, missing: boolean
const disposers: (() => void)[] = []
beforeAll(async () => { await Promise.all([import('./purchase-history-page'), import('@/features/purchases/purchase-page'), import('@/features/auth/account-page')]) })
beforeEach(() => {
  vi.clearAllMocks(); administrator = { ...owner }; source = structuredClone(original); missing = false
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: true }); vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
  vi.mocked(auth.fetchAdmin).mockReset().mockImplementation(async () => { if (!administrator) throw new ApiError('AUTHENTICATION_REQUIRED', { status: 401 }); return administrator })
  vi.mocked(auth.fetchCSRF).mockReset(); vi.mocked(finance.correctPurchaseLine).mockReset()
  vi.mocked(models.readPurchaseCorrectionHistory).mockReset().mockImplementation(async () => { if (missing) throw new ApiError('NOT_FOUND', { status: 404 }); return structuredClone(source) })
})
afterEach(() => { for (const dispose of disposers.splice(0)) dispose() })
function mount(path = '/purchases/purchase/line-corrections', state?: unknown, prepare?: (client: ReturnType<typeof createQueryClient>) => void) {
  const client = createQueryClient(); prepare?.(client)
  const [pathname, search = ''] = path.split('?'), router = createMemoryRouter([{ path: '*', element: <App /> }], { initialEntries: [{ pathname, search: search ? `?${search}` : '', state }] })
  const view = render(<QueryClientProvider client={client}><RouterProvider router={router} /></QueryClientProvider>)
  disposers.push(() => { view.unmount(); router.dispose(); client.clear() }); return { ...view, client, router }
}

it('shows actual current Lines and retained before/after decisions without fabricating a write', async () => {
  mount('/purchases/purchase/line-corrections', { purchaseList: '/purchases?currency=JPY', purchaseCursors: [null] })
  expect(await screen.findByText(`${text.historyCount}1`)).toBeVisible(); expect(screen.getByText(`${purchaseText.lineCount}1`)).toBeVisible()
  expect(screen.getByRole('link', { name: purchaseText.back })).toHaveAttribute('href', '/purchases?currency=JPY')
  expect(screen.getByRole('link', { name: purchaseText.backDetail })).toHaveAttribute('href', '/purchases/purchase')
  expect(screen.getByText(text.order)).toBeVisible(); expect(screen.getByText(text.readOnly)).toBeVisible()
  const history = screen.getByRole('heading', { name: text.history }).closest('section')!
  await userEvent.setup().click(within(history).getByText(new RegExp(text.replace)))
  const before = within(history).getByRole('heading', { name: text.before }).closest('section')!
  expect(within(before).getByText(JSON.stringify(old.id))).toBeVisible(); expect(within(before).getByText(JSON.stringify(old.item_unit_id))).toBeVisible()
  expect(within(history).getByText('0 JPY')).toBeVisible(); expect(within(history).getByText('250 JPY')).toBeVisible()
  expect(within(history).getByText(/Exact reason/)).toBeVisible(); expect(within(history).getByText(/123456789Z/)).toBeVisible()
  expect(auth.fetchCSRF).not.toHaveBeenCalled(); expect(finance.correctPurchaseLine).not.toHaveBeenCalled()
})
it('distinguishes no current Lines after a void from no retained financial history', async () => {
  source = { ...source, lines: [], corrections: [{ id: 'entry', revision: 1, purchase_id: 'purchase', action: 'void', reason: 'Explicit void', created_at: '2026-11-07T02:00:00Z', before: old, after: null }] }
  mount(); expect(await screen.findByRole('heading', { name: purchaseText.emptyLines })).toBeVisible(); expect(screen.getByText(`${text.historyCount}1`)).toBeVisible()
  expect(screen.queryByText(text.noHistory)).not.toBeInTheDocument(); await userEvent.setup().click(screen.getByText(new RegExp(text.void)))
  expect(screen.getByText(text.voided)).toBeVisible(); expect(within(screen.getByRole('heading', { name: text.before }).closest('section')!).getByText(JSON.stringify(old.id))).toBeVisible()
})
it('shows an actual empty history without inferring a new allocation', async () => {
  source = { ...source, lines: [], corrections: [] }; mount()
  expect(await screen.findByText(text.noHistory)).toBeVisible(); expect(screen.getByText(`${text.historyCount}0`)).toBeVisible(); expect(finance.correctPurchaseLine).not.toHaveBeenCalled()
})
it('withholds a failed complete read and allows an explicit fresh read', async () => {
  vi.mocked(models.readPurchaseCorrectionHistory).mockRejectedValueOnce(new ApiError('INVALID_RESPONSE'))
  mount(); expect(await screen.findByText(purchaseText.invalidRead)).toBeVisible(); expect(screen.queryByText(text.noHistory)).not.toBeInTheDocument()
  await userEvent.setup().click(screen.getByRole('button', { name: purchaseText.refresh })); expect(await screen.findByText(`${text.historyCount}1`)).toBeVisible()
})
it('retains and labels an earlier complete read when the current read fails or returns 404', async () => {
  mount(); await screen.findByText(`${text.historyCount}1`)
  vi.mocked(models.readPurchaseCorrectionHistory).mockRejectedValueOnce(new ApiError('NETWORK_ERROR'))
  await userEvent.setup().click(screen.getByRole('button', { name: purchaseText.refresh })); expect(await screen.findByText(text.stale)).toBeVisible(); expect(screen.getByText(`${text.historyCount}1`)).toBeVisible()
  missing = true; await userEvent.setup().click(screen.getByRole('button', { name: purchaseText.refresh })); await waitFor(() => expect(models.readPurchaseCorrectionHistory).toHaveBeenCalledTimes(3))
  expect(screen.getByText(text.stale)).toBeVisible(); expect(screen.getByText(`${text.historyCount}1`)).toBeVisible(); expect(screen.queryByText(text.noHistory)).not.toBeInTheDocument()
})
it('waits for the first fresh complete read rather than publishing a cached history as current', async () => {
  let release: ((value: models.PurchaseCorrectionHistory) => void) | undefined
  vi.mocked(models.readPurchaseCorrectionHistory).mockImplementationOnce(() => new Promise((resolve) => { release = resolve }))
  mount('/purchases/purchase/line-corrections', undefined, (client) => client.setQueryData(['purchases', 'line-corrections', 'purchase'], { ...original, corrections: [] }))
  await waitFor(() => expect(models.readPurchaseCorrectionHistory).toHaveBeenCalledTimes(1)); expect(screen.queryByText(text.noHistory)).not.toBeInTheDocument()
  await act(async () => { release?.(structuredClone(original)) }); expect(await screen.findByText(`${text.historyCount}1`)).toBeVisible()
})
it('renders every selected mocked history entry without assuming ID order is chronology', async () => {
  source = { ...source, lines: [], corrections: Array.from({ length: 205 }, (_, index) => ({ id: `entry-${204 - index}`, revision: 1 as const, purchase_id: 'purchase', action: 'void' as const, reason: `Reason ${index}`, created_at: '2026-11-07T02:00:00Z', before: { ...old, id: `line-${index}` }, after: null })) }
  mount(); expect(await screen.findByText(`${text.historyCount}205`)).toBeVisible(); const history = screen.getByRole('heading', { name: text.history }).closest('section')!
  expect(history.querySelectorAll('summary')).toHaveLength(205); expect(within(history).getByText(text.order)).toBeVisible()
})
it('does not claim a missing source has no history and rejects secret-bearing query data', async () => {
  missing = true; const first = mount(); expect(await screen.findByRole('heading', { name: text.missing })).toBeVisible(); expect(screen.queryByText(text.noHistory)).not.toBeInTheDocument()
  first.unmount(); first.router.dispose(); first.client.clear(); disposers.pop(); vi.mocked(models.readPurchaseCorrectionHistory).mockClear()
  mount('/purchases/purchase/line-corrections?reason=private'); expect(await screen.findByText(messages.locations.validation)).toBeVisible(); expect(models.readPurchaseCorrectionHistory).not.toHaveBeenCalled()
})
it('retains a labeled earlier read offline and does not send financial writes on reconnect', async () => {
  mount(); await screen.findByText(`${text.historyCount}1`)
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: false }); act(() => { window.dispatchEvent(new Event('offline')) })
  expect(await screen.findByText(text.offline)).toBeVisible(); expect(screen.getByText(text.stale)).toBeVisible(); expect(screen.getByText(`${text.historyCount}1`)).toBeVisible()
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: true }); act(() => { window.dispatchEvent(new Event('online')) })
  await waitFor(() => expect(screen.queryByText(text.offline)).not.toBeInTheDocument()); expect(finance.correctPurchaseLine).not.toHaveBeenCalled(); expect(auth.fetchCSRF).not.toHaveBeenCalled()
})
it.each([false, true])('purges private history when authorization fails or password change is required, mandatory=%s', async (mandatory) => {
  if (mandatory) administrator = { ...owner, must_change_password: true }
  else vi.mocked(models.readPurchaseCorrectionHistory).mockRejectedValueOnce(new ApiError('AUTHENTICATION_REQUIRED', { status: 401 }))
  const work = mount('/purchases/purchase/line-corrections', { purchaseList: '/purchases?currency=JPY' })
  await waitFor(() => expect(work.router.state.location.pathname).toBe(mandatory ? '/settings/account' : '/login'))
  expect(work.router.state.location.state).toMatchObject({ returnTo: '/purchases/purchase/line-corrections', purchaseList: '/purchases?currency=JPY' })
  expect(work.client.getQueryData(['purchases', 'line-corrections', 'purchase'])).toBeUndefined(); expect(finance.correctPurchaseLine).not.toHaveBeenCalled()
})
it('keeps only validated return scope when a session is absent', async () => {
  administrator = null; const work = mount('/purchases/purchase/line-corrections', { purchaseList: '/purchases?currency=JPY', purchaseCursors: [null], reason: 'private', key: 'private' })
  await waitFor(() => expect(work.router.state.location.pathname).toBe('/login'))
  expect(work.router.state.location.state).toMatchObject({ returnTo: '/purchases/purchase/line-corrections', purchaseList: '/purchases?currency=JPY', purchaseCursors: [null] })
  expect(work.router.state.location.state).not.toHaveProperty('reason'); expect(work.router.state.location.state).not.toHaveProperty('key')
})
