import { expect, it } from 'vitest'
import { ApiError } from './api/client'
import { freezeReviewedCommand, uncertainMutationFailure } from './reviewed-command'

it('copies and freezes nested domain bodies, proof objects and arrays without changing the draft', () => {
  const draft = { body: { quantity_base: 1, paid_minor: 0 }, source: { item: { revision: 7 }, history: [{ id: 'entry', current: null }] } }
  const command = freezeReviewedCommand(draft)
  for (const value of [command, command.body, command.source, command.source.item, command.source.history, command.source.history[0]]) expect(Object.isFrozen(value)).toBe(true)
  expect(Reflect.set(command.body, 'paid_minor', 99)).toBe(false)
  expect(Reflect.set(command.source.item, 'revision', 99)).toBe(false)
  expect(Reflect.set(command.source.history, '0', { id: 'other', current: null })).toBe(false)
  draft.body.quantity_base = 2; draft.source.item.revision = 8; draft.source.history[0]!.id = 'changed'
  expect(command).toMatchObject({ body: { quantity_base: 1, paid_minor: 0 }, source: { item: { revision: 7 }, history: [{ id: 'entry', current: null }] } })
  expect(Object.isFrozen(draft.body)).toBe(false); expect(command.key).toMatch(/^[0-9a-f-]{36}$/)
})
it('terminates on repeated or cyclic references rather than recursing', () => {
  const child: { name: string; self?: unknown } = { name: 'shared' }
  child.self = child
  const command = freezeReviewedCommand({ first: child, second: child })
  expect(command.first).toBe(command.second); expect(command.first.self).toBe(command.first)
  expect(Object.isFrozen(command.first)).toBe(true); expect(Reflect.set(command.first, 'name', 'changed')).toBe(false)
})
it('classifies sent uncertain outcomes without treating an ordinary conflict as uncertain', () => {
  expect(uncertainMutationFailure(new ApiError('NETWORK_ERROR'))).toBe(true)
  expect(uncertainMutationFailure(new ApiError('SERVICE_UNAVAILABLE', { status: 503 }))).toBe(true)
  expect(uncertainMutationFailure(new ApiError('REVISION_CONFLICT', { status: 409 }))).toBe(false)
  expect(uncertainMutationFailure(new ApiError('REVISION_CONFLICT'))).toBe(false)
})
