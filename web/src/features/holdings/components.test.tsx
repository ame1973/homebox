import { MemoryRouter } from 'react-router'
import { fireEvent, render, screen, within } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/client'
import { messages } from '@/lib/messages'
import { buildLocationTree } from '@/features/locations/tree'
import { freezeHoldingCommand, type HoldingCommand, type HoldingCommandInput } from './commands'
import { baseQuantity, holdingCommandCurrent, holdingPath, holdingValue, sameHoldingPath } from './display'
import { HoldingError } from './holding-error'
import { HoldingPath, Measurement, UnitFields } from './holding-fields'
import { HoldingList } from './holding-list'
import { HoldingLocationPicker } from './location-picker'
import { HoldingReviewDialog } from './holding-review-dialog'
import type { HoldingMutation } from './use-holding-mutation'

const text = messages.holdings
const owner = { id: 'item', revision: 7, name: '  Owner é  ', brand: null, model: '', category_id: null, description: '', notes: '', tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1, status: 'active' } as const
const quantity = { ...owner, tracking_mode: 'quantity' as const, unit_code: 'kg', base_unit: 'g', scale_factor: 1000 }
const source = { id: 'source', revision: 3, name: '  Source é  ', code: 'SRC', kind: 'other', parent_id: null, sort_order: 0, description: '', status: 'active' } as const
const target = { ...source, id: 'target', name: 'Target', code: 'DST' }
const unit = { id: 'unit', item_id: owner.id, revision: 2, location_id: source.id, asset_code: '  AST\0é  ', serial_number: null, notes: 'Original', status: 'stored' } as const
const balance = { item_id: owner.id, location_id: source.id, revision: 2, quantity_base: 1500, unit_code: 'kg', base_unit: 'g', scale_factor: 1000 } as const
const tree = buildLocationTree([source, target])
const context = { owner, subject: unit, location: [source] }
const stockContext = { owner: quantity, balances: [balance], source: [], target: [] }
function operation(command?: HoldingCommand): HoldingMutation {
  return { command, phase: 'review', open: true, error: undefined, confirmed: false, review: vi.fn(), dismiss: vi.fn(), confirm: vi.fn(async () => {}), canLeave: vi.fn(() => false), allowNavigation: vi.fn(), reopen: vi.fn(), suspend: vi.fn(), abort: vi.fn(), restoreFocus: vi.fn() }
}
it('preserves literal fields/nullable states without interpreting HTML, controls or URLs', () => {
  const raw = '  <script>https://evil.example</script>\0\r\né  '
  render(<UnitFields unit={{ ...unit, asset_code: raw, notes: raw, serial_number: '' }} locations={tree} />)
  expect(screen.getAllByText(JSON.stringify(raw), { normalizer: (value) => value })).toHaveLength(2)
  expect(screen.getByText('""')).toBeVisible()
  expect(document.querySelector('script')).toBeNull()
  expect(screen.queryByRole('link')).not.toBeInTheDocument()
  expect(holdingValue('serial_number', null)).toBe(messages.catalog.notProvided)
  expect(holdingValue('location_id', null)).toBe(text.unknownLocation)
  expect(holdingValue('status', 'stored')).toBe(text.statuses.stored)
  expect(holdingValue('disposal_kind', 'sold')).toBe(text.disposalKinds.sold)
  expect(holdingValue('revision', 2)).toBe('2')
  expect(baseQuantity(1500, 'g')).toBe('1,500 "g"')
})
it('shows the exact measurement and unavailable/null/full paths using only the outer scroller', () => {
  const view = render(<><Measurement item={quantity} /><HoldingPath path={undefined} label="Missing" /><HoldingPath path={[]} label="Unknown" /><HoldingPath path={[source, { ...target, parent_id: source.id }]} label="Full" /></>)
  expect(screen.getByRole('heading', { name: text.measurement })).toBeVisible()
  expect(screen.getByText(`Missing：${text.unavailable}`)).toBeVisible()
  expect(screen.getByText(`Unknown：${text.unknownLocation}`)).toBeVisible()
  expect(screen.getByText(/Full："Target"/)).toBeVisible()
  expect(view.container.querySelectorAll('[class*="overflow-y-auto"]')).toHaveLength(0)
  expect(holdingPath(tree, null)).toEqual([])
  expect(holdingPath(tree, 'missing')).toBeUndefined()
  expect(holdingPath(tree, source.id)).toEqual([source])
  expect(sameHoldingPath(undefined, undefined)).toBe(true)
  expect(sameHoldingPath([], undefined)).toBe(false)
  expect(sameHoldingPath([], [source])).toBe(false)
  expect(sameHoldingPath([source], [{ ...source, revision: 4 }])).toBe(false)
  expect(sameHoldingPath([source], [{ ...source }])).toBe(true)
})
it('retains unavailable native selections, filters only actual allowed options and keeps full selected metadata visible', () => {
  const change = vi.fn(), blur = vi.fn()
  render(<HoldingLocationPicker label="Destination" value="missing" tree={tree} allowed={new Set([source.id])} path={undefined} onChange={change} onBlur={blur} error={{ message: text.locationError }} disabled={false} />)
  const select = screen.getByRole('combobox', { name: 'Destination' })
  expect(select).toHaveValue('missing')
  expect(select).toHaveAttribute('aria-invalid', 'true')
  expect(within(select).getByRole('option', { name: /original|原選/ })).toBeInTheDocument()
  fireEvent.change(screen.getByRole('textbox', { name: `Destination：${text.locationSearch}` }), { target: { value: 'no match' } })
  expect(within(select).queryByRole('option', { name: /Source/ })).not.toBeInTheDocument()
  fireEvent.change(screen.getByRole('textbox'), { target: { value: 'SRC' } })
  fireEvent.change(select, { target: { value: source.id } })
  fireEvent.blur(select)
  expect(change).toHaveBeenCalledWith(source.id)
  expect(blur).toHaveBeenCalled()
})
it('renders complete list counts, honest empty/missing matches and addressability without invented rows', () => {
  const view = render(<MemoryRouter><HoldingList data={{ kind: 'units', item: owner, units: [] }} locations={tree} canWrite navigationState={{}} /></MemoryRouter>)
  expect(screen.getByRole('heading', { name: text.emptyUnits })).toBeVisible()
  view.rerender(<MemoryRouter><HoldingList data={{ kind: 'units', item: owner, units: [{ ...unit, id: 'imported/identity' }] }} locations={tree} canWrite={false} navigationState={{}} /></MemoryRouter>)
  expect(screen.getByText(text.unaddressable)).toBeVisible()
  expect(screen.getByRole('button', { name: text.createUnit })).toBeDisabled()
  fireEvent.change(screen.getByRole('textbox', { name: text.search }), { target: { value: 'absent' } })
  expect(screen.getByRole('status')).toHaveTextContent(text.noMatches)
})
it('keeps zero/archived Stock visible and exposes only actual source/placement actions', () => {
  render(<MemoryRouter><HoldingList data={{ kind: 'stock', item: quantity, stock: [balance, { ...balance, location_id: target.id, quantity_base: 0 }] }} locations={buildLocationTree([source, { ...target, status: 'archived' }])} canWrite navigationState={{}} /></MemoryRouter>)
  expect(screen.getByText(`${text.stockCount}2`)).toBeVisible()
  expect(screen.getByText(text.retainedZero)).toBeVisible()
  expect(screen.getAllByRole('link', { name: text.actions.adjust })).toHaveLength(3)
  expect(screen.getByRole('button', { name: text.actions.receive })).toBeDisabled()
  expect(screen.getByRole('button', { name: text.actions.consume })).toBeDisabled()
  expect(screen.getByRole('button', { name: text.actions.transfer })).toBeDisabled()
})
const cases: { input: HoldingCommandInput; confirm: string }[] = [
  { input: { kind: 'create-unit', owner, destination: [], body: { location_id: null, serial_number: '', notes: '' } }, confirm: text.confirmCreate },
  { input: { ...context, kind: 'patch-unit', body: { serial_number: '', notes: 'Reviewed' } }, confirm: text.confirmSave },
  { input: { ...context, kind: 'move-unit', destination: [target], body: { target_location_id: target.id } }, confirm: text.confirmMove },
  { input: { ...context, kind: 'status-unit', body: { status: 'disposed', disposal_kind: 'recycled', note: 'Reviewed note' } }, confirm: text.confirmStatus },
  { input: { ...context, kind: 'delete-unit' }, confirm: text.confirmDelete },
  { input: { ...stockContext, kind: 'receive', target: [target], body: { item_id: owner.id, target_location_id: target.id, quantity_base: 1000 } }, confirm: text.confirms.receive },
  { input: { ...stockContext, kind: 'consume', source: [source], body: { item_id: owner.id, source_location_id: source.id, quantity_base: 1500, note: 'Exact note' } }, confirm: text.confirms.consume },
  { input: { ...stockContext, kind: 'transfer', source: [source], target: [target], body: { item_id: owner.id, source_location_id: source.id, target_location_id: target.id, quantity_base: 1000 } }, confirm: text.confirms.transfer },
  { input: { ...stockContext, kind: 'adjust', target: [source], body: { item_id: owner.id, target_location_id: source.id, quantity_base: 0, reason: 'Absolute count' } }, confirm: text.confirms.adjust },
]
it.each(cases)('reviews original $input.kind context with visible static focus and only its explicit action', async ({ input, confirm }) => {
  const command = freezeHoldingCommand(input), active = operation(command)
  render(<HoldingReviewDialog operation={active} online current onReadCurrent={vi.fn(async () => {})} />)
  const dialog = await screen.findByRole('alertdialog')
  expect(within(dialog).getByRole('heading', { name: text.reviewTitle })).toHaveFocus()
  expect(within(dialog).getByRole('button', { name: confirm })).toBeEnabled()
  expect(dialog).toHaveTextContent(command.key)
  expect(dialog).toHaveTextContent('"Owner é"'.replaceAll('"', ''))
  fireEvent.keyDown(dialog, { key: 'Escape' })
  expect(active.dismiss).toHaveBeenCalledOnce()
  expect(active.confirm).not.toHaveBeenCalled()
})
it('blocks a known stale review but preserves explicit original retry when current source disappeared', () => {
  const command = freezeHoldingCommand(cases[0]!.input), active = operation(command)
  const view = render(<HoldingReviewDialog operation={active} online current={false} onReadCurrent={vi.fn(async () => {})} />)
  expect(screen.getByText(text.reviewStale)).toBeVisible()
  expect(screen.getByRole('button', { name: text.confirmCreate })).toBeDisabled()
  view.rerender(<HoldingReviewDialog operation={{ ...active, phase: 'uncertain', error: new ApiError('INVALID_RESPONSE') }} online current={false} onReadCurrent={vi.fn(async () => {})} />)
  expect(screen.getByRole('button', { name: text.retryOriginal })).toBeEnabled()
  view.rerender(<HoldingReviewDialog operation={operation()} online current={false} onReadCurrent={vi.fn(async () => {})} />)
  expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
})
it('checks full original owner/Unit/path context without changing proofs or doing any reads', () => {
  const command = freezeHoldingCommand(cases[2]!.input)
  expect(holdingCommandCurrent(command, owner, unit, tree)).toBe(true)
  expect(holdingCommandCurrent(command, undefined, unit, tree)).toBe(false)
  expect(holdingCommandCurrent(command, owner, unit, undefined)).toBe(false)
  expect(holdingCommandCurrent(command, { ...owner, revision: 8 }, unit, tree)).toBe(true)
  expect(holdingCommandCurrent(freezeHoldingCommand(cases[0]!.input), { ...owner, revision: 8 }, undefined, tree)).toBe(false)
  expect(holdingCommandCurrent(freezeHoldingCommand(cases[5]!.input), { ...quantity, revision: 8 }, undefined, tree)).toBe(false)
  expect(holdingCommandCurrent(command, owner, undefined, tree)).toBe(false)
  expect(holdingCommandCurrent(command, owner, { ...unit, notes: 'Unversioned change' }, tree)).toBe(false)
  expect(holdingCommandCurrent(command, owner, unit, buildLocationTree([source, { ...target, name: 'Changed' }]))).toBe(false)
  expect(holdingCommandCurrent(freezeHoldingCommand(cases[0]!.input), owner, undefined, tree)).toBe(true)
  expect(holdingCommandCurrent(freezeHoldingCommand(cases[5]!.input), quantity, undefined, tree)).toBe(true)
})
it.each(['TRACKING_MODE_CONFLICT', 'INSUFFICIENT_STOCK', 'QUANTITY_OVERFLOW', 'LOCATION_ARCHIVED', 'UNIT_HAS_HISTORY', 'UNIT_DISPOSED', 'ASSET_LIFECYCLE_REQUIRED', 'INVALID_HOLDING_URL'] as const)('renders safe holding %s recovery without arbitrary diagnostics', (code) => {
  render(<HoldingError error={new ApiError(code, { status: 409, retryAfterSeconds: 3 })} />)
  expect(screen.getByRole('alert')).toHaveTextContent(text.failed)
  expect(screen.getByRole('alert')).toHaveTextContent(messages.auth.seconds)
  expect(screen.getByRole('alert')).not.toHaveTextContent('undefined')
})
