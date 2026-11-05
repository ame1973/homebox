import { beforeEach, expect, it, vi } from 'vitest'
import * as api from '@/lib/api/catalog-links'
import { freezeOwnedCommand, sendOwnedCommand, type OwnedCommandInput } from './owned-commands'

vi.mock('@/lib/api/catalog-links', () => ({ createBarcode: vi.fn(), patchBarcode: vi.fn(), deleteBarcode: vi.fn(), createAlias: vi.fn(), patchAlias: vi.fn(), deleteAlias: vi.fn(),
  createAttribute: vi.fn(), patchAttribute: vi.fn(), deleteAttribute: vi.fn(), replaceItemTags: vi.fn() }))
const item: import('@/lib/api/catalog').Item = { id: 'item', revision: 7, name: '  Owner é  ', brand: null, model: '', category_id: null, description: '', notes: '',
  tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1, status: 'active' } as const
const barcode = { id: 'barcode', item_id: item.id, revision: 2, symbology: 'UPC_A', raw_value: '036000291452', normalized_value: '036000291452' } as const
const alias: api.Alias = { id: 'alias', item_id: item.id, revision: 3, alias: '  Exact é  ' } as const
const attribute = { id: 'attribute', item_id: item.id, revision: 4, attribute_key: 'Key', attribute_value: 'Value' } as const
const tag: import('@/lib/api/taxonomy').Tag = { id: 'tag', revision: 5, name: '  Reviewed tag  ' } as const
const otherTag = { id: 'other-tag', revision: 6, name: 'Other' } as const
const signal = new AbortController().signal
beforeEach(() => { vi.clearAllMocks() })

it('copies all original owner/row/label/body context under one volatile key without credentials', () => {
  const proposal = { kind: 'patch-alias' as const, owner: { ...item }, subject: { ...alias }, body: { alias: 'Reviewed' } }
  const command = freezeOwnedCommand(proposal)
  proposal.owner.name = 'Later owner'
  proposal.owner.revision++
  proposal.subject.alias = 'Later subject'
  proposal.subject.revision++
  proposal.body.alias = 'Later draft'
  expect(command).toMatchObject({ owner: item, subject: alias, body: { alias: 'Reviewed' } })
  expect(Object.isFrozen(command)).toBe(true)
  expect(command.key).toMatch(/^[a-f0-9-]{36}$/)
  expect(freezeOwnedCommand(proposal).key).not.toBe(command.key)
  expect('csrfToken' in command).toBe(false)
  expect('signal' in command).toBe(false)
})

it.each([
  { input: { kind: 'create-barcode', owner: item, body: { symbology: 'UPC_E', raw_value: '04252614' } }, method: 'createBarcode' },
  { input: { kind: 'create-alias', owner: item, body: { alias: '  Alias é  ' } }, method: 'createAlias' },
  { input: { kind: 'create-attribute', owner: item, body: { attribute_key: 'Key', attribute_value: '' } }, method: 'createAttribute' },
] as const)('dispatches $input.kind with the original Item proof, not a row or metadata-label revision', async ({ input, method }) => {
  const command = freezeOwnedCommand(input)
  await sendOwnedCommand(command, 'csrf', signal)
  expect(api[method]).toHaveBeenCalledWith(item.id, input.body, { csrfToken: 'csrf', signal, idempotencyKey: command.key, expectedRevision: 7 })
})

it.each([
  { input: { kind: 'patch-barcode', owner: item, subject: barcode, body: { symbology: 'UPC_E', raw_value: '04252614' } }, method: 'patchBarcode' },
  { input: { kind: 'delete-barcode', owner: item, subject: barcode }, method: 'deleteBarcode' },
  { input: { kind: 'patch-alias', owner: item, subject: alias, body: { alias: 'ReviewedAlias' } }, method: 'patchAlias' },
  { input: { kind: 'delete-alias', owner: item, subject: alias }, method: 'deleteAlias' },
  { input: { kind: 'patch-attribute', owner: item, subject: attribute, body: { attribute_value: '' } }, method: 'patchAttribute' },
  { input: { kind: 'delete-attribute', owner: item, subject: attribute }, method: 'deleteAttribute' },
] as const)('dispatches/reconciles $input.kind only with the independent original subject/owner/key/proof', async ({ input, method }) => {
  const command = freezeOwnedCommand(input)
  const proof = { csrfToken: 'csrf', signal, idempotencyKey: command.key, itemID: item.id, expectedRevision: input.subject.revision }
  await sendOwnedCommand(command, 'csrf', signal)
  expect(api[method]).toHaveBeenCalledWith(...('body' in input ? [input.subject.id, input.body, proof] : [input.subject.id, proof]))
  await sendOwnedCommand(command, 'fresh-csrf', signal)
  expect(api[method]).toHaveBeenLastCalledWith(...('body' in input ? [input.subject.id, input.body, { ...proof, csrfToken: 'fresh-csrf' }] : [input.subject.id, { ...proof, csrfToken: 'fresh-csrf' }]))
})

it('freezes complete before/after tag labels and derives one original ordered ID body without Tag proofs', async () => {
  const proposal = { kind: 'replace-tags' as const, owner: { ...item }, before: [{ ...tag }], after: [{ ...otherTag }, { ...tag }] }
  const command = freezeOwnedCommand(proposal)
  proposal.before[0]!.name = 'Later old label'
  proposal.after.reverse()
  proposal.after[0]!.revision++
  proposal.owner.revision++
  await sendOwnedCommand(command, 'csrf', signal)
  expect(api.replaceItemTags).toHaveBeenCalledWith(item.id, { tag_ids: [otherTag.id, tag.id] }, { csrfToken: 'csrf', signal, idempotencyKey: command.key, expectedRevision: 7 })
  expect(command).toMatchObject({ before: [tag], after: [otherTag, tag] })
  await sendOwnedCommand(command, 'fresh-csrf', signal)
  expect(api.replaceItemTags).toHaveBeenLastCalledWith(item.id, { tag_ids: [otherTag.id, tag.id] }, expect.objectContaining({ expectedRevision: 7, idempotencyKey: command.key }))
})

it('allows a complete large legacy before-set while retaining an explicit empty clear command', async () => {
  const before = Array.from({ length: 205 }, (_, index) => ({ ...tag, id: `legacy-${index}`, name: `Legacy${index}` }))
  const command = freezeOwnedCommand({ kind: 'replace-tags', owner: item, before, after: [] })
  await sendOwnedCommand(command, 'csrf', signal)
  expect(command.kind === 'replace-tags' && command.before).toHaveLength(205)
  expect(api.replaceItemTags).toHaveBeenCalledWith(item.id, { tag_ids: [] }, expect.objectContaining({ expectedRevision: item.revision }))
})

it.each([
  { kind: 'patch-barcode', owner: { ...item, id: 'other' }, subject: barcode, body: { symbology: 'UPC_E', raw_value: '04252614' } },
  { kind: 'delete-barcode', owner: { ...item, id: 'other' }, subject: barcode },
  { kind: 'patch-alias', owner: { ...item, id: 'other' }, subject: alias, body: { alias: 'Alias' } },
  { kind: 'delete-alias', owner: { ...item, id: 'other' }, subject: alias },
  { kind: 'patch-attribute', owner: { ...item, id: 'other' }, subject: attribute, body: { attribute_value: '' } },
  { kind: 'delete-attribute', owner: { ...item, id: 'other' }, subject: attribute },
] as const)('rejects an inconsistent $kind owner context before any mutation', async (proposal) => {
  await expect(sendOwnedCommand(freezeOwnedCommand(proposal), 'csrf', signal)).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
  for (const method of Object.values(api)) expect(method).not.toHaveBeenCalled()
})

it.each([
  { before: [tag, tag], after: [] }, { before: [], after: [tag, tag] }, { before: [], after: [{ ...tag, name: '' }] },
  { before: [], after: Array.from({ length: 101 }, (_, index) => ({ ...tag, id: `tag-${index}`, name: `Tag${index}` })) },
])('rejects incomplete/duplicate/oversized reviewed Tag context without dispatch', async ({ before, after }) => {
  await expect(sendOwnedCommand(freezeOwnedCommand({ kind: 'replace-tags', owner: item, before, after }), 'csrf', signal)).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
  expect(api.replaceItemTags).not.toHaveBeenCalled()
})

// @ts-expect-error Owned creation must retain its original complete Item review context.
const missingOwner: OwnedCommandInput = { kind: 'create-alias', body: { alias: 'Unreviewed' } }
// @ts-expect-error An attribute DTO cannot become an alias subject merely by reusing its ID.
const wrongSubject: OwnedCommandInput = { kind: 'delete-alias', owner: item, subject: attribute }
// @ts-expect-error Empty attribute edits remain invalid at the command boundary.
const emptyPatch: OwnedCommandInput = { kind: 'patch-attribute', owner: item, subject: attribute, body: {} }
// @ts-expect-error Complete tag review needs original before/after label snapshots.
const missingMembership: OwnedCommandInput = { kind: 'replace-tags', owner: item, after: [tag] }
void [missingOwner, wrongSubject, emptyPatch, missingMembership]
