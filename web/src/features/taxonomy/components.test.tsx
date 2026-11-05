import { MemoryRouter } from 'react-router'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/client'
import { messages } from '@/lib/messages'
import { freezeTaxonomyCommand, type TaxonomyCommandInput } from './commands'
import { TaxonomyList } from './list'
import { buildCategoryTree, buildTags } from './model'
import { CategoryParentPicker } from './parent-picker'
import { TaxonomyReviewDialog } from './review-dialog'
import { TaxonomyError } from './taxonomy-error'
import type { TaxonomyMutation } from './use-taxonomy-mutation'

const text = messages.taxonomy
const root = { id: 'parent', parent_id: null, name: 'Parent', revision: 7 }
const child = { id: 'child', parent_id: 'parent', name: 'Child', revision: 1 }
const tag = { id: 'tag', name: 'Tag', revision: 2 }
function operation(input?: TaxonomyCommandInput, overrides: Partial<TaxonomyMutation> = {}): TaxonomyMutation {
  return { command: input ? freezeTaxonomyCommand(input) : undefined, phase: 'review', open: true, error: undefined, confirmed: false, review: vi.fn(), dismiss: vi.fn(), confirm: vi.fn().mockResolvedValue(undefined), canLeave: vi.fn(() => false), allowNavigation: vi.fn(), reopen: vi.fn(), suspend: vi.fn(), abort: vi.fn(), restoreFocus: vi.fn(), ...overrides }
}

it('expands, collapses and searches categories while keeping paths, identities and imported edit routes intact', async () => {
  const user = userEvent.setup()
  const tree = buildCategoryTree([root, child, { ...child, id: 'new', parent_id: 'child', name: 'Inner' }])
  render(<MemoryRouter><TaxonomyList kind="categories" tree={tree} canWrite /></MemoryRouter>)
  expect(screen.queryByRole('link', { name: '「Inner」' })).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: `${text.expand}：Child (child)` }))
  expect(screen.getByRole('link', { name: '「Inner」' })).toHaveAttribute('href', '/settings/categories/new/edit')
  await user.click(screen.getByRole('button', { name: `${text.collapse}：Parent (parent)` }))
  expect(screen.queryByRole('link', { name: '「Child」' })).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: text.expandAll }))
  expect(screen.getByRole('link', { name: '「Inner」' })).toBeVisible()
  await user.click(screen.getByRole('button', { name: text.collapseAll }))
  await user.type(screen.getByRole('searchbox', { name: text.searchCategories }), 'Inner')
  expect(screen.getByRole('link', { name: '「Parent」' })).toBeVisible()
  expect(screen.getByRole('link', { name: '「Inner」' })).toBeVisible()
  await user.clear(screen.getByRole('searchbox', { name: text.searchCategories }))
  await user.type(screen.getByRole('searchbox', { name: text.searchCategories }), 'missing')
  expect(screen.getByRole('status')).toHaveTextContent(text.noMatches)
})

it('searches real tag names/IDs and keeps unsupported imported paths read-only', async () => {
  const user = userEvent.setup()
  const tags = buildTags([tag, { ...tag, id: 'tree', name: 'Other' }, { ...tag, id: 'unsupported/path', name: 'Imported' }])
  render(<MemoryRouter><TaxonomyList kind="tags" tags={tags} canWrite={false} /></MemoryRouter>)
  expect(screen.queryByRole('button', { name: text.expandAll })).not.toBeInTheDocument()
  expect(screen.getByRole('link', { name: '「Other」' })).toHaveAttribute('href', '/settings/tags/tree/edit')
  expect(screen.getByText('「Imported」')).toBeVisible()
  expect(screen.queryByRole('link', { name: /編輯標籤：Imported/ })).not.toBeInTheDocument()
  await user.type(screen.getByRole('searchbox', { name: text.searchTags }), 'Other')
  expect(screen.queryByRole('link', { name: '「Tag」' })).not.toBeInTheDocument()
  expect(screen.getByRole('link', { name: '「Other」' })).toBeVisible()
})

it('uses a native searchable parent selector and retains unavailable selections rather than substituting root', async () => {
  const user = userEvent.setup()
  const changed = vi.fn()
  const ref = { current: null as HTMLSelectElement | null }
  const tree = buildCategoryTree([root, child])
  const view = render(<><label htmlFor="parent">{text.parent}</label><CategoryParentPicker tree={tree} id="parent" value="parent" onChange={changed} inputRef={ref} excluded={new Set(['child'])} /></>)
  expect(ref.current).toBe(screen.getByRole('combobox', { name: text.parent }))
  expect(screen.getAllByRole('option')).toHaveLength(2)
  await user.type(screen.getByRole('searchbox', { name: text.parentSearch }), 'not found')
  expect(screen.getByRole('option', { name: '「Parent」 (parent)' })).toBeInTheDocument()
  await user.selectOptions(screen.getByRole('combobox'), '')
  expect(changed).toHaveBeenCalledWith('')
  view.rerender(<><label htmlFor="parent">{text.parent}</label><CategoryParentPicker tree={tree} id="parent" value="child" onChange={changed} excluded={new Set(['child'])} /></>)
  expect(screen.getByRole('option', { name: text.parentUnavailable })).toBeDisabled()
  expect(screen.getByRole('combobox')).toHaveValue('child')
  await user.click(screen.getByText(`${text.parent}（2）`))
  expect(screen.getByRole('list', { name: text.parent })).toHaveTextContent('Parent')
  view.rerender(<><label htmlFor="parent">{text.parent}</label><CategoryParentPicker tree={tree} id="parent" value="missing" onChange={changed} disabled invalid /></>)
  expect(screen.getByRole('combobox')).toBeDisabled()
  expect(screen.getByRole('combobox')).toHaveAttribute('aria-invalid', 'true')
})

it.each([
  { kind: 'create-category', body: { name: 'Created', parent_id: 'parent' }, parent: root },
  { kind: 'patch-category', subject: child, body: { name: 'Renamed', parent_id: null }, previousParent: root },
  { kind: 'delete-category', subject: child },
  { kind: 'create-tag', body: { name: 'Created' } },
  { kind: 'patch-tag', subject: tag, body: { name: 'Renamed' } },
  { kind: 'delete-tag', subject: tag },
] satisfies TaxonomyCommandInput[])('reviews $kind with frozen labels/proofs and no implicit write', async (input) => {
  const user = userEvent.setup()
  const work = operation(input)
  render(<TaxonomyReviewDialog operation={work} online onReadCurrent={vi.fn()} />)
  const dialog = screen.getByRole('alertdialog')
  await waitFor(() => expect(within(dialog).getByRole('heading', { name: text.reviewTitle })).toHaveFocus())
  expect(work.confirm).not.toHaveBeenCalled()
  if (input.kind === 'create-category') expect(dialog).toHaveTextContent(`${text.parentProof}7`)
  if (input.kind !== 'create-category' && input.kind !== 'create-tag') expect(dialog).toHaveTextContent(`${text.originalVersion}${input.subject.revision}`)
  if (input.kind === 'patch-category') expect(dialog).toHaveTextContent(`${text.proposed}：${text.root}`)
  if (input.kind === 'delete-category') expect(dialog).toHaveTextContent(text.deleteCategoryHelp)
  if (input.kind === 'delete-tag') expect(dialog).toHaveTextContent(text.deleteTagHelp)
  const labels = { 'create-category': text.confirmCreateCategory, 'patch-category': text.confirmSave, 'delete-category': text.confirmDeleteCategory, 'create-tag': text.confirmCreateTag, 'patch-tag': text.confirmSave, 'delete-tag': text.confirmDeleteTag }
  await user.click(within(dialog).getByRole('button', { name: labels[input.kind] }))
  expect(work.confirm).toHaveBeenCalledOnce()
})

it('keeps original parent display metadata, historical unknown IDs and same-key uncertainty controls', async () => {
  const parent = { ...root }
  const work = operation({ kind: 'patch-category', subject: child, body: { parent_id: 'unknown-parent' }, previousParent: parent }, { phase: 'uncertain', error: new ApiError('INVALID_RESPONSE') })
  parent.name = 'Concurrent label'
  const read = vi.fn().mockResolvedValue(undefined)
  const view = render(<TaxonomyReviewDialog operation={work} online onReadCurrent={read} />)
  expect(screen.getByRole('alertdialog')).toHaveTextContent('「Parent」 (parent)')
  expect(screen.getByRole('alertdialog')).toHaveTextContent('unknown-parent')
  expect(screen.getByText(work.command!.key)).toBeVisible()
  await userEvent.setup().click(screen.getByRole('button', { name: text.readCurrent }))
  expect(read).toHaveBeenCalledOnce()
  expect(work.confirm).not.toHaveBeenCalled()
  view.rerender(<TaxonomyReviewDialog operation={{ ...work, phase: 'pending', error: undefined }} online onReadCurrent={read} />)
  fireEvent.keyDown(screen.getByRole('alertdialog'), { key: 'Escape' })
  expect(work.abort).toHaveBeenCalledOnce()
  view.rerender(<TaxonomyReviewDialog operation={{ ...work, command: undefined }} online onReadCurrent={read} />)
  expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
})

it.each(['CATEGORY_CYCLE', 'CATEGORY_HAS_CHILDREN', 'TAG_NAME_CONFLICT', 'INVALID_TAXONOMY_URL', 'VALIDATION_ERROR', 'INVALID_REVISION', 'PRECONDITION_REQUIRED',
  'REVISION_CONFLICT', 'IDEMPOTENCY_CONFLICT', 'IDEMPOTENCY_EXPIRED', 'INVALID_RESPONSE', 'INVALID_CURSOR', 'INVALID_QUERY', 'NOT_FOUND',
  'NETWORK_ERROR', 'REQUEST_TIMEOUT', 'REQUEST_ABORTED', 'CLIENT_UPGRADE_REQUIRED', 'RATE_LIMITED', 'CSRF_REJECTED', 'ORIGIN_REJECTED',
  'AUTHENTICATION_REQUIRED', 'PASSWORD_CHANGE_REQUIRED', 'SERVICE_UNAVAILABLE'] as const)('localizes safe %s guidance without diagnostics', (code) => {
  render(<TaxonomyError error={new ApiError(code, { retryAfterSeconds: 12 })} />)
  expect(screen.getByRole('alert')).toHaveTextContent(text.failed)
  expect(screen.getByRole('alert')).toHaveTextContent(`${messages.auth.seconds}12`)
})

it('does not expose arbitrary exception messages or invent retry metadata', () => {
  render(<TaxonomyError error={new Error('private SQL diagnostic')} />)
  expect(screen.getByRole('alert')).toHaveTextContent(messages.locations.busy)
  expect(screen.getByRole('alert')).not.toHaveTextContent('private SQL diagnostic')
  expect(screen.getByRole('alert')).not.toHaveTextContent(messages.auth.seconds)
})
