import { beforeEach, expect, it, vi } from 'vitest'
import * as api from '@/lib/api/taxonomy'
import { commandName, freezeTaxonomyCommand, sendTaxonomyCommand, type TaxonomyCommandInput } from './commands'

vi.mock('@/lib/api/taxonomy', () => ({ createCategory: vi.fn(), patchCategory: vi.fn(), deleteCategory: vi.fn(), createTag: vi.fn(), patchTag: vi.fn(), deleteTag: vi.fn() }))
const category: api.Category = { id: 'category', parent_id: null, name: '  工具 é  ', revision: 12 }
const tag: api.Tag = { id: 'tag', name: '  常用 é  ', revision: 9 }
const signal = new AbortController().signal
beforeEach(() => { vi.clearAllMocks() })

it('freezes original target/parent metadata and body under one new volatile review key', () => {
  const input = { kind: 'patch-category' as const, subject: { ...category }, body: { name: 'Reviewed' }, parent: { ...category, id: 'parent', name: 'Reviewed parent' } }
  const command = freezeTaxonomyCommand(input)
  input.subject.revision++
  input.parent.name = 'Later parent'
  input.body.name = 'Later edit'
  expect(command).toMatchObject({ subject: category, body: { name: 'Reviewed' }, parent: { name: 'Reviewed parent' } })
  expect(command.key).toMatch(/^[a-f0-9-]{36}$/)
  expect(Object.isFrozen(command)).toBe(true)
  expect(commandName(command)).toBe(category.name)
  expect(freezeTaxonomyCommand(input).key).not.toBe(command.key)
})

it('dispatches root/child category and tag creation with only the reviewed preconditions', async () => {
  const root = freezeTaxonomyCommand({ kind: 'create-category', body: { name: 'Root' } })
  await sendTaxonomyCommand(root, 'csrf', signal)
  expect(api.createCategory).toHaveBeenLastCalledWith({ name: 'Root', parent_id: null }, { csrfToken: 'csrf', signal, idempotencyKey: root.key })
  expect(commandName(root)).toBe('Root')
  const child = freezeTaxonomyCommand({ kind: 'create-category', body: { name: 'Child', parent_id: category.id }, parent: category })
  await sendTaxonomyCommand(child, 'csrf', signal)
  expect(api.createCategory).toHaveBeenLastCalledWith({ name: 'Child', parent_id: category.id }, { csrfToken: 'csrf', signal, idempotencyKey: child.key, expectedRevision: 12 })
  const createdTag = freezeTaxonomyCommand({ kind: 'create-tag', body: { name: tag.name } })
  await sendTaxonomyCommand(createdTag, 'csrf', signal)
  expect(api.createTag).toHaveBeenLastCalledWith({ name: tag.name }, { csrfToken: 'csrf', signal, idempotencyKey: createdTag.key })
  expect(commandName(createdTag)).toBe(tag.name)
})

it('rejects missing, mismatched, or unrelated category parent proofs rather than inventing one', async () => {
  for (const input of [
    { kind: 'create-category', body: { name: 'Child', parent_id: category.id } },
    { kind: 'create-category', body: { name: 'Child', parent_id: 'another' }, parent: category },
    { kind: 'create-category', body: { name: 'Root', parent_id: null }, parent: category },
  ] as const) {
    await expect(sendTaxonomyCommand(freezeTaxonomyCommand(input), 'csrf', signal)).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
  }
  expect(api.createCategory).not.toHaveBeenCalled()
})

it.each([
  { input: { kind: 'patch-category', subject: category, body: { parent_id: null } }, method: 'patchCategory', id: category.id, revision: 12, body: { parent_id: null } },
  { input: { kind: 'delete-category', subject: category }, method: 'deleteCategory', id: category.id, revision: 12 },
  { input: { kind: 'patch-tag', subject: tag, body: { name: 'Renamed' } }, method: 'patchTag', id: tag.id, revision: 9, body: { name: 'Renamed' } },
  { input: { kind: 'delete-tag', subject: tag }, method: 'deleteTag', id: tag.id, revision: 9 },
] as const)('dispatches $input.kind and explicit retries with the original identity/key/proof', async ({ input, method, id, revision, ...rest }) => {
  const command = freezeTaxonomyCommand(input as TaxonomyCommandInput)
  const context = { csrfToken: 'csrf', signal, idempotencyKey: command.key, expectedRevision: revision }
  await sendTaxonomyCommand(command, 'csrf', signal)
  if ('body' in rest) expect(api[method]).toHaveBeenCalledWith(id, rest.body, context)
  else expect(api[method]).toHaveBeenCalledWith(id, context)
  await sendTaxonomyCommand(command, 'fresh-csrf', signal)
  expect(api[method]).toHaveBeenLastCalledWith(...('body' in rest ? [id, rest.body, { ...context, csrfToken: 'fresh-csrf' }] : [id, { ...context, csrfToken: 'fresh-csrf' }]))
})
