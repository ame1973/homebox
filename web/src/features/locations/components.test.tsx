import { MemoryRouter } from 'react-router'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/client'
import type { Location } from '@/lib/api/locations'
import { messages } from '@/lib/messages'
import { LocationBreadcrumbs } from './breadcrumbs'
import { freezeLocationCommand, type LocationCommandInput } from './commands'
import { LocationError } from './location-error'
import { ParentPicker } from './parent-picker'
import { LocationMutationNotice, LocationReviewDialog } from './review-dialog'
import { buildLocationTree } from './tree'
import { LocationTreeView } from './tree-view'
import type { LocationMutation } from './use-location-mutation'

const text = messages.locations
const root: Location = { id: 'room', revision: 3, name: '書房', code: 'ROOM-1', kind: 'room', description: '', status: 'active', sort_order: 0, parent_id: null }
const child: Location = { ...root, id: 'box', parent_id: 'room', name: '收納箱', code: 'BOX-1', kind: 'box', revision: 1 }
const archived: Location = { ...root, id: 'archive', name: '舊箱子', code: 'OLD-1', status: 'archived' }
function operation(input?: LocationCommandInput, overrides: Partial<LocationMutation> = {}): LocationMutation {
  return { command: input ? freezeLocationCommand(input) : undefined, phase: 'review', open: true, error: undefined, confirmed: false, review: vi.fn(), dismiss: vi.fn(), confirm: vi.fn().mockResolvedValue(undefined), canLeave: vi.fn(() => false), allowNavigation: vi.fn(), reopen: vi.fn(), suspend: vi.fn(), abort: vi.fn(), restoreFocus: vi.fn(), ...overrides }
}

it('expands and filters the real hierarchy without flattening its meaning or losing archived paths', async () => {
  const user = userEvent.setup()
  const grandchild = { ...child, id: 'inner', parent_id: 'box', name: '內層盒', code: 'INNER' }
  const tree = buildLocationTree([root, child, grandchild, archived])
  render(<MemoryRouter><LocationTreeView tree={tree} selectedID="box" /></MemoryRouter>)
  expect(screen.queryByRole('link', { name: /內層盒/ })).not.toBeInTheDocument()
  expect(screen.queryByText('舊箱子')).not.toBeInTheDocument()
  expect(screen.getByText('收納箱').closest('li')).toHaveClass('border-primary')
  await user.click(screen.getByRole('button', { name: `${text.expand} 收納箱` }))
  expect(screen.getByRole('link', { name: /內層盒/ })).toHaveTextContent(`${text.level} 3`)
  await user.click(screen.getByRole('button', { name: `${text.collapse} 書房` }))
  expect(screen.queryByText('收納箱')).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: text.expandAll }))
  expect(screen.getByText('內層盒')).toBeVisible()
  await user.click(screen.getByRole('button', { name: text.collapseAll }))
  expect(screen.queryByText('內層盒')).not.toBeInTheDocument()
  await user.type(screen.getByLabelText(text.search), 'INNER')
  expect(screen.getByText('內層盒')).toBeVisible()
  expect(screen.getByRole('button', { name: `${text.collapse} 書房` })).toBeDisabled()
  await user.clear(screen.getByLabelText(text.search))
  await user.click(screen.getByLabelText(text.showArchived))
  expect(screen.getByText('舊箱子')).toBeVisible()
  await user.type(screen.getByLabelText(text.search), 'missing')
  expect(screen.getByRole('status')).toHaveTextContent(text.noMatches)
})

it('uses a native searchable parent selection and never substitutes root for an unavailable value', async () => {
  const user = userEvent.setup()
  const changed = vi.fn()
  const ref = { current: null as HTMLSelectElement | null }
  const tree = buildLocationTree([root, child, archived, { ...child, id: 'archived-child', parent_id: 'archive' }])
  const view = render(<><label htmlFor="parent">{text.parent}</label><ParentPicker tree={tree} id="parent" value="room" onChange={changed} inputRef={ref} excluded={new Set(['box'])} /></>)
  expect(ref.current).toBe(screen.getByLabelText(text.parent))
  expect(screen.getAllByRole('option')).toHaveLength(2)
  await user.type(screen.getByLabelText(text.parentSearch), 'no matches')
  expect(screen.getByRole('option', { name: '書房 (ROOM-1)' })).toBeInTheDocument()
  await user.selectOptions(screen.getByLabelText(text.parent), '')
  expect(changed).toHaveBeenCalledWith('')
  view.rerender(<><label htmlFor="parent">{text.parent}</label><ParentPicker tree={tree} id="parent" value="archive" onChange={changed} /></>)
  expect(screen.getByRole('option', { name: text.parentUnavailable })).toBeDisabled()
  expect(screen.getByLabelText(text.parent)).toHaveValue('archive')
  await user.click(screen.getByText(`${text.parentLabel}（1）`))
  expect(screen.getByRole('list', { name: text.parentLabel })).toHaveTextContent('舊箱子')
  view.rerender(<><label htmlFor="parent">{text.parent}</label><ParentPicker tree={tree} id="parent" value="missing" onChange={changed} invalid disabled /></>)
  expect(screen.getByLabelText(text.parent)).toBeDisabled()
  expect(screen.getByLabelText(text.parent)).toHaveAttribute('aria-invalid', 'true')
})

it('renders long breadcrumbs as a fully available path rather than truncating ancestors', async () => {
  const user = userEvent.setup()
  const nodes = [root]
  for (let index = 1; index <= 8; index++) nodes.push({ ...root, id: `depth-${index}`, parent_id: nodes.at(-1)!.id, name: `第 ${index} 層` })
  const tree = buildLocationTree(nodes)
  const view = render(<MemoryRouter><LocationBreadcrumbs tree={tree} location={nodes.at(-1)!} /></MemoryRouter>)
  expect(screen.getByText(`${text.parentLabel}（8）`).closest('details')).not.toHaveAttribute('open')
  await user.click(screen.getByText(`${text.parentLabel}（8）`))
  expect(screen.getByText(`${text.parentLabel}（8）`).closest('details')).toHaveAttribute('open')
  expect(screen.getByRole('link', { name: '書房 (ROOM-1)' })).toHaveAttribute('href', '/locations/room')
  expect(screen.getByText('第 8 層')).toHaveAttribute('aria-current', 'page')
  view.rerender(<MemoryRouter><LocationBreadcrumbs tree={tree} location={{ ...child, id: 'not-in-list' }} /></MemoryRouter>)
  expect(screen.getByRole('link', { name: '書房 (ROOM-1)' })).toBeVisible()
  view.rerender(<MemoryRouter><LocationBreadcrumbs tree={tree} location={{ ...child, id: 'not-in-list', parent_id: 'missing' }} /></MemoryRouter>)
  expect(screen.getAllByRole('link')).toHaveLength(1)
})

it.each([
  ['VALIDATION_ERROR', 'validation'], ['INVALID_REVISION', 'validation'], ['PRECONDITION_REQUIRED', 'validation'], ['REVISION_CONFLICT', 'conflict'], ['LOCATION_CODE_CONFLICT', 'codeConflict'], ['LOCATION_CYCLE', 'cycle'], ['LOCATION_ARCHIVED', 'archivedDestination'], ['LOCATION_HAS_CHILDREN', 'children'], ['LOCATION_IN_USE', 'inUse'], ['IDEMPOTENCY_CONFLICT', 'keyConflict'], ['IDEMPOTENCY_EXPIRED', 'keyConflict'], ['INVALID_RESPONSE', 'invalidRead'], ['INVALID_CURSOR', 'invalidRead'], ['INVALID_QUERY', 'invalidRead'], ['NOT_FOUND', 'missingHelp'], ['NETWORK_ERROR', 'network'], ['REQUEST_TIMEOUT', 'network'], ['REQUEST_ABORTED', 'network'], ['CLIENT_UPGRADE_REQUIRED', 'upgrade'], ['RATE_LIMITED', 'rate'], ['CSRF_REJECTED', 'forbidden'], ['ORIGIN_REJECTED', 'forbidden'], ['AUTHENTICATION_REQUIRED', 'forbidden'], ['PASSWORD_CHANGE_REQUIRED', 'forbidden'], ['SERVICE_UNAVAILABLE', 'busy'],
] as const)('renders static guidance for %s without arbitrary server diagnostics', (code, key) => {
  render(<LocationError error={new ApiError(code, { retryAfterSeconds: 12 })} />)
  expect(screen.getByRole('alert')).toHaveTextContent(text[key])
  expect(screen.getByRole('alert')).toHaveTextContent(`${messages.auth.seconds}12`)
})

it('does not expose unknown exception messages or invent retry metadata', () => {
  render(<LocationError error={new Error('private SQL diagnostic')} />)
  expect(screen.getByRole('alert')).toHaveTextContent(text.busy)
  expect(screen.getByRole('alert')).not.toHaveTextContent('private SQL diagnostic')
  expect(screen.getByRole('alert')).not.toHaveTextContent(messages.auth.seconds)
})

it.each([
  { kind: 'create', body: { name: '新箱子', code: 'NEW', kind: 'box', parent_id: 'room', description: '', sort_order: -1 }, parentRevision: 3 },
  { kind: 'patch', subject: child, body: { name: '新箱子', code: 'NEW', kind: '密封', sort_order: -2, description: '新說明' } },
  { kind: 'move', subject: child, body: { target_parent_id: null } },
  { kind: 'archive', subject: child, body: { archive_children: true, reason: '整理' } },
  { kind: 'restore', subject: { ...child, status: 'archived' } },
  { kind: 'delete', subject: child },
] satisfies LocationCommandInput[])('reviews $kind with original proof and no implicit write', async (input) => {
  const user = userEvent.setup()
  const work = operation(input)
  render(<LocationReviewDialog operation={work} tree={buildLocationTree([root, child])} online onReadCurrent={vi.fn()} />)
  const dialog = screen.getByRole('alertdialog')
  expect(within(dialog).getByRole('heading', { name: text.reviewTitle })).toBeVisible()
  expect(work.confirm).not.toHaveBeenCalled()
  await waitFor(() => expect(within(dialog).getByRole('heading', { name: text.reviewTitle })).toHaveFocus())
  if (input.kind === 'create') {
    expect(dialog).toHaveTextContent(`${text.parentProof}3`)
    expect(dialog).toHaveTextContent('書房 (ROOM-1)')
    expect(dialog).toHaveTextContent(text.kinds.box)
    expect(dialog).toHaveTextContent(text.emptyValue)
  } else expect(dialog).toHaveTextContent(`${text.originalVersion}1`)
  if (input.kind === 'move') expect(dialog).toHaveTextContent(`${text.proposed}：${text.root}`)
  if (input.kind === 'archive') expect(dialog).toHaveTextContent(`${text.archiveChildren}：${text.yes}`)
  const labels = { create: text.confirmCreate, patch: text.confirmSave, move: text.confirmMove, archive: text.confirmArchive, restore: text.confirmRestore, delete: text.confirmDelete }
  await user.click(within(dialog).getByRole('button', { name: labels[input.kind] }))
  expect(work.confirm).toHaveBeenCalledTimes(1)
  await user.click(within(dialog).getByRole('button', { name: text.cancel }))
  expect(work.dismiss).toHaveBeenCalledTimes(1)
})

it('handles pending interruption, known rejection focus and uncertain explicit retry/read controls', async () => {
  const user = userEvent.setup()
  const tree = buildLocationTree([root, child])
  const work = operation({ kind: 'archive', subject: child, body: {} }, { phase: 'pending' })
  const read = vi.fn().mockResolvedValue(undefined)
  const view = render(<LocationReviewDialog operation={work} tree={tree} online onReadCurrent={read} />)
  expect(screen.getByRole('button', { name: text.pending })).toBeDisabled()
  fireEvent.keyDown(screen.getByRole('alertdialog'), { key: 'Escape' })
  expect(work.abort).toHaveBeenCalledTimes(1)
  await user.click(screen.getByRole('button', { name: text.stopWaiting }))
  expect(work.abort).toHaveBeenCalledTimes(2)
  expect(work.dismiss).not.toHaveBeenCalled()
  view.rerender(<LocationReviewDialog operation={{ ...work, phase: 'rejected', error: new ApiError('REVISION_CONFLICT') }} tree={tree} online onReadCurrent={read} />)
  expect(screen.getByRole('button', { name: text.confirmArchive })).toBeDisabled()
  expect(screen.getByRole('alert').parentElement).toHaveFocus()
  view.rerender(<LocationReviewDialog operation={{ ...work, phase: 'uncertain', error: new ApiError('INVALID_RESPONSE') }} tree={tree} online={false} onReadCurrent={read} />)
  expect(screen.getByRole('button', { name: text.retryOriginal })).toBeDisabled()
  expect(screen.getByText(work.command!.key)).toBeVisible()
  view.rerender(<LocationReviewDialog operation={{ ...work, phase: 'uncertain', error: new ApiError('INVALID_RESPONSE') }} tree={tree} online onReadCurrent={read} />)
  await user.click(screen.getByRole('button', { name: text.readCurrent }))
  expect(read).toHaveBeenCalledTimes(1)
  expect(work.confirm).not.toHaveBeenCalled()
  await user.click(screen.getByRole('button', { name: text.retryOriginal }))
  expect(work.confirm).toHaveBeenCalledTimes(1)
  view.rerender(<LocationReviewDialog operation={{ ...work, open: false }} tree={tree} online onReadCurrent={read} />)
  await waitFor(() => expect(work.restoreFocus).toHaveBeenCalled())
})

it('reviews a historical unknown parent without silently showing root and permits unsent network retries', async () => {
  const work = operation({ kind: 'move', subject: child, body: { target_parent_id: 'unavailable-id' } }, { phase: 'rejected', error: new ApiError('NETWORK_ERROR') })
  const view = render(<LocationReviewDialog operation={work} tree={buildLocationTree([root, child])} online onReadCurrent={vi.fn()} />)
  expect(screen.getByRole('alertdialog')).toHaveTextContent('unavailable-id')
  expect(screen.getByRole('button', { name: text.confirmMove })).toBeEnabled()
  await userEvent.setup().keyboard('{Escape}')
  expect(work.dismiss).toHaveBeenCalled()
  view.rerender(<LocationReviewDialog operation={{ ...work, command: undefined }} tree={buildLocationTree([])} online onReadCurrent={vi.fn()} />)
  expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
})

it('keeps the uncertain request accessible after dismissal and reports only confirmed success', async () => {
  const user = userEvent.setup()
  const work = operation(undefined, { phase: 'uncertain' })
  const read = vi.fn().mockResolvedValue(undefined)
  const view = render(<LocationMutationNotice operation={work} online onReadCurrent={read} />)
  await user.click(screen.getByRole('button', { name: text.openOriginal }))
  expect(work.reopen).toHaveBeenCalledOnce()
  await user.click(screen.getByRole('button', { name: text.readCurrent }))
  expect(read).toHaveBeenCalledOnce()
  view.rerender(<LocationMutationNotice operation={{ ...work, phase: 'idle', confirmed: false }} online onReadCurrent={read} />)
  expect(screen.queryByRole('status')).not.toBeInTheDocument()
  view.rerender(<LocationMutationNotice operation={{ ...work, phase: 'idle', confirmed: true }} online onReadCurrent={read} />)
  expect(screen.getByRole('status')).toHaveTextContent(text.done)
})
