import { MemoryRouter } from 'react-router'
import { fireEvent, render, screen, within } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import { messages } from '@/lib/messages'
import { freezeOwnedCommand, type OwnedCommand, type OwnedCommandInput } from './owned-commands'
import { ownedFields } from './display'
import { OwnedFields } from './owned-fields'
import { OwnedList } from './owned-list'
import { OwnedReviewDialog } from './owned-review-dialog'
import type { OwnedMutation } from './use-owned-mutation'

const text = messages.owned
const item = { id: 'item', revision: 7, name: '  Owner é  ', brand: null, model: '', category_id: null, description: '', notes: '', tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1, status: 'active' } as const
const barcode = { id: 'barcode', item_id: item.id, revision: 2, symbology: 'UPC_A', raw_value: 'legacy invalid width', normalized_value: '000 retained' } as const
const alias = { id: 'alias', item_id: item.id, revision: 3, alias: 'Original alias' } as const
const attribute = { id: 'attribute', item_id: item.id, revision: 4, attribute_key: 'Original key', attribute_value: 'Original content' } as const
const tag = { id: 'tag', revision: 5, name: 'Original tag' } as const
function operation(command?: OwnedCommand): OwnedMutation {
  return { command, phase: 'review', open: true, error: undefined, confirmed: false, review: vi.fn(), dismiss: vi.fn(), confirm: vi.fn(async () => {}), canLeave: vi.fn(() => false), allowNavigation: vi.fn(), reopen: vi.fn(), suspend: vi.fn(), abort: vi.fn(), restoreFocus: vi.fn() }
}
it('renders exact escaped values without interpreting HTML, URLs, controls, normalization or identity as content fields', () => {
  const raw = '  00123\x00\x1d\r\n<script>https://evil.example</script> é  '
  render(<OwnedFields value={{ symbology: 'CODE_128', raw_value: raw, normalized_value: 'different retained value' }} />)
  expect(screen.getByText(JSON.stringify(raw), { normalizer: (value) => value })).toBeVisible()
  expect(screen.queryByRole('link')).not.toBeInTheDocument()
  expect(document.querySelector('script')).toBeNull()
  expect(ownedFields({ attribute_key: 'Repeated', attribute_value: '' })).toEqual([{ key: 'attribute_key', label: text.fields.attribute_key, value: '"Repeated"' }, { key: 'attribute_value', label: text.fields.attribute_value, value: '""' }])
  expect(ownedFields({})).toEqual([])
})
it('explains an unaddressable retained row without hiding its source or inventing an edit route', () => {
  render(<MemoryRouter><OwnedList itemID={item.id} kind="aliases" records={[{ ...alias, id: 'retained/identity' }]} navigationState={{ catalogList: '/items', catalogCursors: [] }} /></MemoryRouter>)
  expect(screen.getByText(text.unaddressable)).toBeVisible()
  expect(screen.getByText(/retained\/identity/)).toBeVisible()
  expect(screen.queryByRole('link')).not.toBeInTheDocument()
})
it('shows a real empty collection and its count rather than fabricated fixture rows', () => {
  render(<MemoryRouter><OwnedList itemID={item.id} kind="attributes" records={[]} navigationState={{ catalogList: '/items', catalogCursors: [] }} /></MemoryRouter>)
  expect(screen.getByRole('heading', { name: text.empty })).toBeVisible()
  expect(screen.getByText(`${text.count}0`)).toBeVisible()
  expect(screen.queryByRole('list')).not.toBeInTheDocument()
})
const cases: { input: OwnedCommandInput; confirm: string }[] = [
  { input: { kind: 'create-barcode', owner: item, body: { symbology: 'UPC_A', raw_value: '036000291452' } }, confirm: text.confirmCreate.barcodes },
  { input: { kind: 'patch-barcode', owner: item, subject: barcode, body: { symbology: 'UPC_E', raw_value: '04252614' } }, confirm: text.confirmSave },
  { input: { kind: 'delete-barcode', owner: item, subject: barcode }, confirm: text.confirmDelete.barcodes },
  { input: { kind: 'create-alias', owner: item, body: { alias: '  New é  ' } }, confirm: text.confirmCreate.aliases },
  { input: { kind: 'patch-alias', owner: item, subject: alias, body: { alias: '  New é  ' } }, confirm: text.confirmSave },
  { input: { kind: 'delete-alias', owner: item, subject: alias }, confirm: text.confirmDelete.aliases },
  { input: { kind: 'create-attribute', owner: item, body: { attribute_key: 'Key', attribute_value: '' } }, confirm: text.confirmCreate.attributes },
  { input: { kind: 'patch-attribute', owner: item, subject: attribute, body: { attribute_key: 'Changed key' } }, confirm: text.confirmSave },
  { input: { kind: 'delete-attribute', owner: item, subject: attribute }, confirm: text.confirmDelete.attributes },
  { input: { kind: 'replace-tags', owner: item, before: [tag], after: [{ ...tag, id: 'other', name: 'Reviewed new tag' }] }, confirm: text.confirmTags },
]
it.each(cases)('renders the complete original $input.kind context with the correct explicit action', async ({ input, confirm }) => {
  const active = operation(freezeOwnedCommand(input))
  render(<OwnedReviewDialog operation={active} online onReadCurrent={vi.fn(async () => {})} />)
  const dialog = await screen.findByRole('alertdialog')
  expect(dialog).toHaveTextContent(JSON.stringify(item.name).replace(/\s+/g, ' '))
  expect(within(dialog).getByRole('button', { name: confirm })).toBeEnabled()
  expect(within(dialog).getByRole('heading', { name: text.reviewTitle })).toHaveFocus()
  if ('subject' in input) expect(dialog).toHaveTextContent(`${text.rowProof}${input.subject.revision}`)
  else expect(dialog).toHaveTextContent(`${text.ownerProof}${item.revision}`)
  if (input.kind === 'delete-barcode') expect(dialog).toHaveTextContent(barcode.normalized_value)
  fireEvent.keyDown(dialog, { key: 'Escape' })
  expect(active.dismiss).toHaveBeenCalledOnce()
  expect(active.confirm).not.toHaveBeenCalled()
})
it('omits an absent command and reviews a full legacy Tag clear without a nested read-only scroller', () => {
  const view = render(<OwnedReviewDialog operation={operation()} online onReadCurrent={vi.fn(async () => {})} />)
  expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  const before = Array.from({ length: 205 }, (_, index) => ({ ...tag, id: `tag-${index}`, name: `Legacy${index}` }))
  view.rerender(<OwnedReviewDialog operation={operation(freezeOwnedCommand({ kind: 'replace-tags', owner: item, before, after: [] }))} online onReadCurrent={vi.fn(async () => {})} />)
  const dialog = screen.getByRole('alertdialog')
  expect(within(dialog).getByRole('heading', { name: `${text.beforeTags}（205）` })).toBeVisible()
  expect(within(dialog).getByRole('heading', { name: `${text.afterTags}（0）` })).toBeVisible()
  expect(dialog).toHaveTextContent('Legacy204')
  expect(dialog).toHaveTextContent(text.clearHelp)
  expect(dialog.querySelectorAll('[class*="overflow-y-auto"]')).toHaveLength(0)
  expect(within(dialog).getByRole('button', { name: text.confirmTags })).toHaveAttribute('data-variant', 'destructive')
})
