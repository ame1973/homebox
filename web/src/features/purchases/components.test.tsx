import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { expect, it } from 'vitest'
import type { Item } from '@/lib/api/catalog'
import type { Unit } from '@/lib/api/holdings'
import type { Purchase, PurchaseLine } from '@/lib/api/purchases'
import { ApiError } from '@/lib/api/client'
import { messages } from '@/lib/messages'
import { freezePurchaseCommand } from './commands'
import { purchaseCommandCurrent, purchaseInterpretation, samePurchaseDetail, sameRecord } from './display'
import { PurchaseError } from './purchase-error'
import { PurchaseFields, PurchaseItem, PurchaseLines } from './purchase-fields'
import { PurchaseList } from './purchase-list'

const text = messages.purchases
const purchase: Purchase = { id: 'purchase', revision: 4, merchant_name: '  é 商家 😀\n', purchased_at: '2026-11-07T10:00:00.123456789+08:00', currency: 'ZZZ', receipt_note: ' <script>private</script> ' }
const item: Item = { id: 'item', revision: 3, name: 'Owner', tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1, status: 'active', brand: null, model: null, category_id: null, description: '', notes: '' }
const unit: Unit = { id: 'unit', item_id: item.id, revision: 8, asset_code: 'AST-owned', serial_number: null, notes: '', location_id: null, status: 'disposed' }
const line: PurchaseLine = { id: 'line', revision: 9, purchase_id: purchase.id, item_id: item.id, item_unit_id: unit.id, quantity_base: 2, paid_minor: 0, note: ' <b>exact</b> ' }

it('shows exact retained instant, unknown currency, escaped literal text and current complete Item interpretation without guessing precision', () => {
  const view = render(<><PurchaseFields purchase={purchase} /><PurchaseItem item={item} /></>)
  expect(screen.getByText(JSON.stringify(purchase.purchased_at))).toBeVisible()
  expect(screen.getByText(JSON.stringify(purchase.receipt_note))).toBeVisible()
  expect(screen.getByText('"ZZZ"')).toBeVisible()
  expect(view.container.querySelector('script')).toBeNull()
  expect(screen.getByText(messages.catalog.individual)).toBeVisible()
  expect(screen.getByText(messages.holdings.measurement)).toBeVisible()
})
it('reads old bulk named-Unit allocations verbatim with independent versions and real safe links, never line edit/delete or a mixed total', () => {
  render(<MemoryRouter><PurchaseLines lines={[line, { ...line, id: 'null-line', item_unit_id: null, quantity_base: 1500, paid_minor: 120000 }]} currency={purchase.currency} navigationState={{}} /></MemoryRouter>)
  expect(screen.getByText(`${text.lineCount}2`)).toBeVisible()
  expect(screen.getAllByRole('link', { name: item.id })[0]).toHaveAttribute('href', '/items/item')
  expect(screen.getByRole('link', { name: unit.id })).toHaveAttribute('href', '/units/unit')
  expect(screen.getByText('120000 ZZZ')).toBeVisible()
  expect(screen.getByText('0 ZZZ')).toBeVisible()
  expect(screen.getByText('2', { exact: true })).toBeVisible()
  expect(screen.getByText(text.noUnit)).toBeVisible()
  expect(screen.queryByRole('button')).not.toBeInTheDocument()
})
it('does not repair retained path-unsafe owner IDs into links', () => {
  render(<MemoryRouter><PurchaseLines lines={[{ ...line, item_id: '/', item_unit_id: '/' }]} currency="JPY" navigationState={{}} /></MemoryRouter>)
  expect(screen.queryByRole('link')).not.toBeInTheDocument()
})
it('shows a real empty Line collection and unallocated header explanation', () => {
  render(<MemoryRouter><PurchaseLines lines={[]} currency="JPY" navigationState={{}} /></MemoryRouter>)
  expect(screen.getByRole('heading', { name: text.emptyLines })).toBeVisible()
})
it.each([true, false])('distinguishes complete associated headers from a global live page, associated=%s', (associated) => {
  const view = render(<MemoryRouter><PurchaseList purchases={[{ ...purchase, id: 'new' }, { ...purchase, id: '/' }]} associated={associated} navigationState={{}} /></MemoryRouter>)
  expect(screen.getByRole('link')).toHaveAttribute('href', '/purchases/new/view')
  expect(screen.getByRole('link').textContent).toBe(JSON.stringify(purchase.merchant_name))
  expect(screen.getByText(messages.holdings.unaddressable)).toBeVisible()
  expect(view.container).toHaveTextContent(`${associated ? text.associatedCount : messages.catalog.pageCount}2`)
})
it.each([true, false])('provides an actionable actual empty header state, associated=%s', (associated) => {
  render(<MemoryRouter><PurchaseList purchases={[]} associated={associated} navigationState={{}} /></MemoryRouter>)
  expect(screen.getByRole('heading', { name: associated ? text.emptyAssociated : text.empty })).toBeVisible()
})
it.each([
  ['PURCHASE_CURRENCY_LOCKED', text.currencyLocked], ['PURCHASE_CURRENCY_CONFLICT', text.currencyConflict], ['ASSET_COST_BASIS_CONFLICT', text.basisConflict], ['ASSET_LIFECYCLE_REQUIRED', text.assetRequired], ['TRACKING_MODE_CONFLICT', text.allocationError], ['INVALID_PURCHASE_URL', messages.locations.validation], ['INVALID_RESPONSE', text.invalidRead], ['INVALID_QUERY', text.invalidRead], ['INVALID_CURSOR', text.invalidRead], ['NOT_FOUND', text.missingHelp], ['REVISION_CONFLICT', messages.locations.conflict], ['RATE_LIMITED', messages.locations.rate], ['UNKNOWN_ERROR', messages.locations.busy],
] as const)('renders only static safe purchase error guidance for %s', (code, guidance) => {
  render(<PurchaseError error={new ApiError(code, { retryAfterSeconds: 7 })} />)
  expect(screen.getByRole('alert')).toHaveTextContent(guidance)
  expect(screen.getByRole('alert')).toHaveTextContent(`${messages.auth.seconds}7`)
})
it('uses safe non-API failure guidance without rendering arbitrary error messages', () => {
  render(<PurchaseError error={new Error('private source SQL financial detail')} />)
  expect(screen.getByRole('alert')).toHaveTextContent(messages.locations.busy)
  expect(screen.queryByText(/private source SQL/)).not.toBeInTheDocument()
})
it('compares complete flat source values rather than property order, and complete Lines rather than a fabricated total', () => {
  expect(sameRecord(undefined, undefined)).toBe(true)
  expect(sameRecord(purchase, undefined)).toBe(false)
  expect(sameRecord({ a: 1 }, { a: 1, b: 2 })).toBe(false)
  expect(sameRecord(purchase, Object.fromEntries(Object.entries(purchase).reverse()) as Purchase)).toBe(true)
  const detail = { purchase, lines: [line] }
  expect(samePurchaseDetail(detail, structuredClone(detail))).toBe(true)
  expect(samePurchaseDetail(detail, { purchase, lines: [] })).toBe(false)
  expect(samePurchaseDetail(detail, { purchase, lines: [{ ...line, note: 'changed' }] })).toBe(false)
})
it('rechecks original command contexts without adding a second Unit proof or disabling original-key reconciliation', () => {
  expect(purchaseCommandCurrent(freezePurchaseCommand({ kind: 'create-purchase', body: { merchant_name: 'merchant', purchased_at: purchase.purchased_at, currency: 'JPY' } }))).toBe(true)
  const detail = { purchase, lines: [line] }, patch = freezePurchaseCommand({ kind: 'patch-purchase', source: detail, body: { receipt_note: '' } })
  expect(purchaseCommandCurrent(patch, detail)).toBe(true)
  expect(purchaseCommandCurrent(patch)).toBe(false)
  expect(purchaseCommandCurrent(patch, { purchase: { ...purchase, revision: 5 }, lines: [line] })).toBe(false)
  const allocation = { kind: 'individual' as const, purchase, item, units: [unit] }
  const command = freezePurchaseCommand({ kind: 'create-line', purchase, item, unit, body: { item_id: item.id, item_revision: item.revision, item_unit_id: unit.id, quantity_base: 1, paid_minor: 0 } })
  expect(purchaseCommandCurrent(command, undefined, allocation)).toBe(true)
  expect(purchaseCommandCurrent(command)).toBe(false)
  expect(purchaseCommandCurrent(command, undefined, { ...allocation, item: { ...item, revision: 4 } })).toBe(false)
  expect(purchaseCommandCurrent(command, undefined, { ...allocation, units: [] })).toBe(false)
  expect(purchaseCommandCurrent(command, undefined, { ...allocation, units: [{ ...unit, notes: 'changed' }] })).toBe(false)
  const quantity = { ...item, tracking_mode: 'quantity' as const }
  expect(purchaseCommandCurrent(command, undefined, { kind: 'quantity', purchase, item: quantity })).toBe(false)
  const unallocated = freezePurchaseCommand({ kind: 'create-line', purchase, item: quantity, unit: null, body: { item_id: item.id, item_revision: item.revision, item_unit_id: null, quantity_base: 1500, paid_minor: 123 } })
  expect(purchaseCommandCurrent(unallocated, undefined, { kind: 'quantity', purchase, item: quantity })).toBe(true)
  expect(purchaseInterpretation(allocation)).not.toBe(purchaseInterpretation({ ...allocation, purchase: { ...purchase, currency: 'USD' } }))
})
