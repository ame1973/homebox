import { beforeEach, expect, it, vi } from 'vitest'
import * as api from '@/lib/api/catalog'
import { commandName, freezeCatalogCommand, sendCatalogCommand, type CatalogCommandInput } from './commands'

vi.mock('@/lib/api/catalog', () => ({ createItem: vi.fn(), patchItem: vi.fn(), archiveItem: vi.fn(), deleteItem: vi.fn() }))
const item: api.Item = { id: 'item', revision: 7, name: '  物品 é  ', brand: null, model: '', category_id: 'category', description: '', notes: '',
  tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1, status: 'active' }
const category = { id: 'category', revision: 12, name: '  分類 é  ', parent_id: null }
const signal = new AbortController().signal
beforeEach(() => { vi.clearAllMocks() })

it('copies original metadata/category labels and body under one volatile review key', () => {
  const input = { kind: 'patch' as const, subject: { ...item }, body: { name: 'Reviewed', category_id: category.id }, category: { ...category } }
  const command = freezeCatalogCommand(input)
  input.subject.revision++
  input.category.name = 'Later label'
  input.body.name = 'Later draft'
  expect(command).toMatchObject({ subject: item, body: { name: 'Reviewed', category_id: category.id }, category })
  expect(Object.isFrozen(command)).toBe(true)
  expect(command.key).toMatch(/^[a-f0-9-]{36}$/)
  expect(commandName(command)).toBe(item.name)
  expect(freezeCatalogCommand(input).key).not.toBe(command.key)
  expect('csrfToken' in command).toBe(false)
  expect('session_token' in command).toBe(false)
})

it('dispatches minimal/classified creation without defaults, implicit inventory or category revision proofs', async () => {
  for (const input of [{ kind: 'create', body: { name: 'Name' } }, { kind: 'create', body: { name: 'Name', category_id: category.id }, category }] as const) {
    const command = freezeCatalogCommand(input)
    await sendCatalogCommand(command, 'csrf', signal)
    expect(api.createItem).toHaveBeenLastCalledWith(input.body, { csrfToken: 'csrf', signal, idempotencyKey: command.key })
    expect(commandName(command)).toBe('Name')
  }
  const body = { name: 'Measured', tracking_mode: 'quantity', unit_code: 'kg', base_unit: 'g', scale_factor: 1000, category_id: null } as const
  const measured = freezeCatalogCommand({ kind: 'create', body, category: null })
  await sendCatalogCommand(measured, 'csrf', signal)
  expect(api.createItem).toHaveBeenLastCalledWith(body, { csrfToken: 'csrf', signal, idempotencyKey: measured.key })
})

it('rejects missing/mismatched/unrelated reviewed category context without issuing requests', async () => {
  for (const input of [
    { kind: 'create', body: { name: 'Name', category_id: category.id } },
    { kind: 'create', body: { name: 'Name', category_id: 'another' }, category },
    { kind: 'create', body: { name: 'Name' }, category },
    { kind: 'create', body: { name: 'Name', category_id: null }, category },
    { kind: 'patch', subject: item, body: { category_id: category.id } },
    { kind: 'patch', subject: item, body: { category_id: 'another' }, category },
    { kind: 'patch', subject: item, body: { name: 'Name' }, category },
  ] as const) await expect(sendCatalogCommand(freezeCatalogCommand(input), 'csrf', signal)).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
  expect(api.createItem).not.toHaveBeenCalled()
  expect(api.patchItem).not.toHaveBeenCalled()
})

it.each([
  { input: { kind: 'patch', subject: item, body: { name: 'Exact partial' } }, method: 'patchItem', body: { name: 'Exact partial' } },
  { input: { kind: 'patch', subject: item, body: { category_id: null, brand: null, model: '' }, category: null }, method: 'patchItem', body: { category_id: null, brand: null, model: '' } },
  { input: { kind: 'patch', subject: item, body: { category_id: category.id }, category }, method: 'patchItem', body: { category_id: category.id } },
  { input: { kind: 'archive', subject: item, body: { reason: '  Keep history  ' } }, method: 'archiveItem', body: { reason: '  Keep history  ' } },
  { input: { kind: 'archive', subject: item, body: {} }, method: 'archiveItem', body: {} },
  { input: { kind: 'restore', subject: item }, method: 'patchItem', body: { status: 'active' } },
  { input: { kind: 'delete', subject: item }, method: 'deleteItem' },
] as const)('dispatches $input.kind using only the original item identity/key/revision', async ({ input, method, ...rest }) => {
  const command = freezeCatalogCommand(input as CatalogCommandInput)
  const context = { csrfToken: 'csrf', signal, idempotencyKey: command.key, expectedRevision: item.revision }
  await sendCatalogCommand(command, 'csrf', signal)
  expect(api[method]).toHaveBeenCalledWith(...('body' in rest ? [item.id, rest.body, context] : [item.id, context]))
  await sendCatalogCommand(command, 'fresh-csrf', signal)
  expect(api[method]).toHaveBeenLastCalledWith(...('body' in rest ? [item.id, rest.body, { ...context, csrfToken: 'fresh-csrf' }] : [item.id, { ...context, csrfToken: 'fresh-csrf' }]))
})

// @ts-expect-error Reviewed metadata patches require the original resource DTO.
const unprovenPatch: CatalogCommandInput = { kind: 'patch', body: { name: 'Unproven' } }
// @ts-expect-error A custom creation unit requires the complete explicit measurement tuple.
const incompleteMeasurement: CatalogCommandInput = { kind: 'create', body: { name: 'Incomplete', unit_code: 'kg' } }
// @ts-expect-error Archival requires an explicit body, even when its reason is omitted.
const missingArchiveBody: CatalogCommandInput = { kind: 'archive', subject: item }
// @ts-expect-error Catalog metadata cannot receive stock as an implicit side effect.
const implicitStock: CatalogCommandInput = { kind: 'create', body: { name: 'Not stock', quantity_base: 1 } }
void [unprovenPatch, incompleteMeasurement, missingArchiveBody, implicitStock]
