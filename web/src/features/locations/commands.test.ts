import { beforeEach, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/client'
import * as api from '@/lib/api/locations'
import { commandName, freezeLocationCommand, sendLocationCommand, uncertainLocationFailure, type LocationCommandInput } from './commands'

vi.mock('@/lib/api/locations', () => ({ createLocation: vi.fn(), patchLocation: vi.fn(), moveLocation: vi.fn(), archiveLocation: vi.fn(), deleteLocation: vi.fn() }))
const subject: api.Location = { id: 'room', name: '房間', code: 'R1', revision: 12, kind: 'room', status: 'active', sort_order: 0, description: '', parent_id: null }
const signal = new AbortController().signal
beforeEach(() => { vi.clearAllMocks() })

it('snapshots a review before later input changes and generates a key only for a new review', () => {
  const input = { kind: 'patch' as const, subject: { ...subject }, body: { name: '新的名稱' } }
  const command = freezeLocationCommand(input)
  input.subject.revision = 13
  input.body.name = '修改中的名稱'
  expect(command).toMatchObject({ subject: { revision: 12 }, body: { name: '新的名稱' } })
  expect(command.key).toMatch(/^[a-f0-9-]{36}$/)
  expect(Object.isFrozen(command)).toBe(true)
  expect(commandName(command)).toBe('房間')
  expect(freezeLocationCommand(input).key).not.toBe(command.key)
})

it('sends root creation without an unrelated revision and child creation with the original parent proof', async () => {
  const root = freezeLocationCommand({ kind: 'create', body: { name: '房間', code: 'R1' } })
  await sendLocationCommand(root, 'csrf', signal)
  expect(api.createLocation).toHaveBeenLastCalledWith({ name: '房間', code: 'R1', parent_id: null }, { csrfToken: 'csrf', idempotencyKey: root.key, signal })
  expect(commandName(root)).toBe('房間')
  const child = freezeLocationCommand({ kind: 'create', parentRevision: 12, body: { name: '收納箱', code: 'B1', parent_id: 'room' } })
  await sendLocationCommand(child, 'csrf-2', signal)
  expect(api.createLocation).toHaveBeenLastCalledWith({ name: '收納箱', code: 'B1', parent_id: 'room' }, { csrfToken: 'csrf-2', idempotencyKey: child.key, expectedRevision: 12, signal })
  await expect(sendLocationCommand(freezeLocationCommand({ kind: 'create', body: { name: '收納箱', code: 'B1', parent_id: 'room' } }), 'csrf', signal)).rejects.toMatchObject({ code: 'VALIDATION_ERROR' })
  expect(api.createLocation).toHaveBeenCalledTimes(2)
})

it.each([
  { input: { kind: 'patch', subject, body: { description: '新說明' } }, method: 'patchLocation', body: { description: '新說明' } },
  { input: { kind: 'move', subject, body: { target_parent_id: null } }, method: 'moveLocation', body: { target_parent_id: null } },
  { input: { kind: 'archive', subject, body: { archive_children: true, reason: '整理' } }, method: 'archiveLocation', body: { archive_children: true, reason: '整理' } },
  { input: { kind: 'restore', subject }, method: 'patchLocation', body: { status: 'active' } },
  { input: { kind: 'delete', subject }, method: 'deleteLocation' },
] as const)('dispatches $input.kind with its immutable resource, original proof and key', async ({ input, method, ...rest }) => {
  const command = freezeLocationCommand(input as LocationCommandInput)
  await sendLocationCommand(command, 'csrf', signal)
  const context = { csrfToken: 'csrf', idempotencyKey: command.key, signal, expectedRevision: 12 }
  if ('body' in rest) expect(api[method]).toHaveBeenCalledWith('room', rest.body, context)
  else expect(api[method]).toHaveBeenCalledWith('room', context)
  await sendLocationCommand(command, 'fresh-csrf', signal)
  expect(api[method]).toHaveBeenLastCalledWith(...('body' in rest ? ['room', rest.body, { ...context, csrfToken: 'fresh-csrf' }] : ['room', { ...context, csrfToken: 'fresh-csrf' }]))
})

it('distinguishes uncertain sent outcomes from authoritative rejections without trusting messages', () => {
  for (const code of ['NETWORK_ERROR', 'REQUEST_TIMEOUT', 'REQUEST_ABORTED', 'INVALID_RESPONSE', 'UNKNOWN_ERROR', 'IDEMPOTENCY_CONFLICT', 'IDEMPOTENCY_EXPIRED'] as const) expect(uncertainLocationFailure(new ApiError(code))).toBe(true)
  expect(uncertainLocationFailure(new ApiError('SERVICE_UNAVAILABLE', { status: 503 }))).toBe(true)
  expect(uncertainLocationFailure(new ApiError('LOCATION_CODE_CONFLICT', { status: 409 }))).toBe(false)
  expect(uncertainLocationFailure(new ApiError('REVISION_CONFLICT', { status: 409 }))).toBe(false)
})
