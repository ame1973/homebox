import type { ReactNode } from 'react'
import { createMemoryRouter } from 'react-router'
import { RouterProvider } from 'react-router/dom'
import { fireEvent, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/client'
import { messages } from '@/lib/messages'
import { buildCategoryTree } from '@/features/taxonomy/model'
import { CatalogActions } from './actions'
import { CatalogError } from './catalog-error'
import { freezeCatalogCommand, type CatalogCommand } from './commands'
import { catalogDisplay } from './display'
import { CatalogHierarchyPicker } from './hierarchy-picker'
import { CatalogReviewDialog } from './review-dialog'
import type { CatalogMutation } from './use-catalog-mutation'

const text = messages.catalog
const common = messages.locations
const item = { id: 'item', revision: 3, name: 'Item', brand: 'Brand', model: '', category_id: null, description: '', notes: '', tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1, status: 'active' } as const
const disposers: (() => void)[] = []
afterEach(() => { for (const dispose of disposers.splice(0)) dispose() })
function mount(node: ReactNode) {
  const router = createMemoryRouter([{ path: '*', element: node }])
  const view = render(<RouterProvider router={router} />)
  disposers.push(() => { view.unmount(); router.dispose() })
}
function operation(command?: CatalogCommand): CatalogMutation {
  return { command, phase: 'review', open: true, error: undefined, confirmed: false, review: vi.fn(), dismiss: vi.fn(), confirm: vi.fn(async () => {}),
    canLeave: vi.fn(() => false), allowNavigation: vi.fn(), reopen: vi.fn(), suspend: vi.fn(), abort: vi.fn(), restoreFocus: vi.fn() }
}

it.each([
  ['ITEM_HAS_HISTORY', text.history], ['VALIDATION_ERROR', common.validation], ['INVALID_REVISION', common.validation], ['INVALID_CATALOG_URL', common.validation], ['PRECONDITION_REQUIRED', common.validation],
  ['REVISION_CONFLICT', common.conflict], ['IDEMPOTENCY_CONFLICT', common.keyConflict], ['IDEMPOTENCY_EXPIRED', common.keyConflict],
  ['INVALID_RESPONSE', text.invalidRead], ['INVALID_CURSOR', text.invalidRead], ['INVALID_QUERY', text.invalidRead], ['NOT_FOUND', text.missingHelp],
  ['NETWORK_ERROR', common.network], ['REQUEST_TIMEOUT', common.network], ['REQUEST_ABORTED', common.network], ['CLIENT_UPGRADE_REQUIRED', common.upgrade], ['RATE_LIMITED', common.rate],
  ['CSRF_REJECTED', common.forbidden], ['ORIGIN_REJECTED', common.forbidden], ['AUTHENTICATION_REQUIRED', common.forbidden], ['PASSWORD_CHANGE_REQUIRED', common.forbidden], ['SERVICE_UNAVAILABLE', common.busy],
] as const)('localizes safe %s without displaying raw server diagnostics', (code, guidance) => {
  render(<CatalogError error={new ApiError(code, { retryAfterSeconds: 3 })} />)
  expect(screen.getByRole('alert')).toHaveTextContent(guidance)
  expect(screen.getByRole('alert')).toHaveTextContent(`${messages.auth.seconds}3`)
})
it('uses static fallback guidance for an unknown private error', () => {
  render(<CatalogError error={new Error('/private/database secret')} />)
  expect(screen.getByRole('alert')).toHaveTextContent(common.busy)
  expect(screen.getByRole('alert')).not.toHaveTextContent('/private')
})
it('displays null, supplied-empty, enums and exact integers without inventing inventory meaning', () => {
  expect(catalogDisplay('brand', null)).toBe(text.notProvided)
  expect(catalogDisplay('brand', '')).toBe(text.suppliedEmpty)
  expect(catalogDisplay('tracking_mode', 'individual')).toBe(text.individual)
  expect(catalogDisplay('tracking_mode', 'quantity')).toBe(text.quantity)
  expect(catalogDisplay('status', 'active')).toBe(text.active)
  expect(catalogDisplay('status', 'archived')).toBe(text.archived)
  expect(catalogDisplay('scale_factor', Number.MAX_SAFE_INTEGER)).toBe('9007199254740991')
})
it('searches native hierarchy options while retaining selected identity/full ancestry and unavailable selections', async () => {
  const tree = buildCategoryTree([{ id: 'root', name: 'Root', parent_id: null, revision: 1 }, { id: 'child', name: 'Child', parent_id: 'root', revision: 1 }])
  const change = vi.fn()
  const view = render(<CatalogHierarchyPicker tree={tree} id="category" value="child" onChange={change} empty={text.categoryNone} />)
  await userEvent.setup().type(screen.getByRole('searchbox'), 'unmatched')
  expect(screen.getByRole('option', { name: /Child/ })).toBeInTheDocument()
  expect(screen.queryByRole('option', { name: /Root/ })).not.toBeInTheDocument()
  await userEvent.setup().click(screen.getByText(`${text.selectedPath}（2）`))
  expect(screen.getByRole('list')).toHaveTextContent('Root')
  await userEvent.setup().selectOptions(screen.getByRole('combobox'), '')
  expect(change).toHaveBeenCalledWith('')
  view.rerender(<CatalogHierarchyPicker tree={tree} id="category" value="missing" onChange={change} empty={text.categoryNone} />)
  expect(screen.getByRole('option', { name: /missing/ })).toBeDisabled()
})
it('reviews a category reassignment and exact nullable changes under the original item proof', async () => {
  const command = freezeCatalogCommand({ kind: 'patch', subject: item, body: { category_id: 'category', brand: null, model: '' }, category: { id: 'category', name: 'Reviewed label', parent_id: null, revision: 7 } })
  render(<CatalogReviewDialog operation={operation(command)} online onReadCurrent={vi.fn(async () => {})} />)
  expect(await screen.findByRole('alertdialog')).toHaveTextContent('Reviewed label')
  expect(screen.getByRole('alertdialog')).toHaveTextContent(text.notProvided)
  expect(screen.getByRole('alertdialog')).toHaveTextContent(text.suppliedEmpty)
  expect(screen.getByRole('alertdialog')).toHaveTextContent(`${text.originalVersion}3`)
})
it('renders no review without a command and keeps archive reason omission distinct', () => {
  const view = render(<CatalogReviewDialog operation={operation()} online onReadCurrent={vi.fn(async () => {})} />)
  expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  view.rerender(<CatalogReviewDialog operation={operation(freezeCatalogCommand({ kind: 'archive', subject: item, body: {} }))} online onReadCurrent={vi.fn(async () => {})} />)
  expect(screen.getByRole('alertdialog')).toHaveTextContent(text.archiveHelp)
  expect(within(screen.getByRole('alertdialog')).queryByText(text.reason)).not.toBeInTheDocument()
})
it('validates a real archive reason, supports cancellation, and never opens an action while writes are locked', async () => {
  const active = { ...operation(), phase: 'idle' as const, open: false }
  mount(<CatalogActions item={item} operation={active} online canWrite />)
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: text.archive }))
  fireEvent.change(screen.getByLabelText(text.reason), { target: { value: '\uD800' } })
  await user.click(screen.getByRole('button', { name: text.review }))
  expect(await screen.findByText(text.textError)).toBeVisible()
  expect(active.review).not.toHaveBeenCalled()
  await user.click(screen.getByRole('button', { name: text.cancel }))
  expect(screen.queryByLabelText(text.reason)).not.toBeInTheDocument()
})
