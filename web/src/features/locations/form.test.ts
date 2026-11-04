import { expect, it } from 'vitest'
import { ApiError } from '@/lib/api/client'
import type { Location } from '@/lib/api/locations'
import { isKnownKind, locationDefaults, locationFormSchema, metadataFromValues, metadataPatch, parseSortOrder, retainEdits } from './form'

const original: Location = { id: 'room', revision: 4, name: '原有房間', code: 'R-1', kind: 'room', parent_id: null, sort_order: 3, status: 'active', description: '說明' }

it.each(['', ' ', '+1', '01', '-01', '1.0', '1e2', '1,000', '9007199254740992', '-9007199254740992', '9'.repeat(50), '0x10', '１２', '1\n'])('rejects noncanonical or unsafe sort text %j before conversion', (value) => {
  expect(parseSortOrder(value)).toBeUndefined()
})
it.each(['0', '-0', '1', '-1', '9007199254740991', '-9007199254740991'])('parses the exact safe integer %s', (value) => {
  expect(parseSortOrder(value)).toBe(Number(value))
})

it('preserves metadata, Unicode and custom kinds without normalization or defaulted writes', () => {
  const values = locationDefaults({ ...original, kind: '密封容器', name: '  😀房間  ' })
  expect(values.kind).toBe('custom')
  expect(values.custom_kind).toBe('密封容器')
  expect(metadataFromValues(values)).toEqual({ name: '  😀房間  ', code: 'R-1', kind: '密封容器', sort_order: 3, description: '說明' })
  expect(locationDefaults()).toEqual({ name: '', code: '', kind: 'other', custom_kind: '', parent_id: '', sort_order: '0', description: '' })
  expect(isKnownKind('box')).toBe(true)
  expect(isKnownKind('密封容器')).toBe(false)
  expect(locationFormSchema.safeParse({ ...values, name: '😀'.repeat(200), code: '箱'.repeat(100), custom_kind: '層'.repeat(32), description: '😀'.repeat(10000) }).success).toBe(true)
})

it('reports each invalid logical field, including original invalid integer and Unicode text', () => {
  const result = locationFormSchema.safeParse({ ...locationDefaults(), kind: 'custom', custom_kind: 'x'.repeat(33), parent_id: '\ud800', sort_order: '9007199254740992', description: 'x'.repeat(10001) })
  expect(result.success).toBe(false)
  if (result.success) throw new Error('Invalid form unexpectedly accepted')
  expect(result.error.issues.map((issue) => issue.path[0])).toEqual(['name', 'code', 'custom_kind', 'parent_id', 'sort_order', 'description'])
  expect(() => metadataFromValues({ ...locationDefaults(original), sort_order: '1e2' })).toThrow(ApiError)
})

it('patches only changed metadata and never implicitly moves, restores or rebases', () => {
  expect(metadataPatch(locationDefaults(original), original)).toBeUndefined()
  expect(metadataPatch({ ...locationDefaults(original), name: '新房間', code: 'R-2', kind: 'custom', custom_kind: '密封', parent_id: 'other', sort_order: '-2', description: '新說明' }, original)).toEqual({ name: '新房間', code: 'R-2', kind: '密封', sort_order: -2, description: '新說明' })
  expect(metadataPatch({ ...locationDefaults(original), parent_id: 'other', sort_order: '3' }, original)).toBeUndefined()
})

it('retains only explicit edits while adopting concurrent untouched fields', () => {
  const latest: Location = { ...original, revision: 5, name: '別處重新命名', code: 'R-NEW', parent_id: 'new-parent', sort_order: 8, kind: 'cabinet', description: '別處說明' }
  const draft = { ...locationDefaults(original), description: '我的說明', sort_order: '1e', kind: 'custom' as const, custom_kind: '密封容器' }
  expect(retainEdits(draft, original, latest)).toEqual({ ...locationDefaults(latest), description: '我的說明', sort_order: '1e', kind: 'custom', custom_kind: '密封容器' })
  expect(retainEdits(locationDefaults(original), original, latest)).toEqual(locationDefaults(latest))
  expect(retainEdits({ ...locationDefaults(original), name: '我的名稱', code: 'MINE' }, original, latest)).toEqual({ ...locationDefaults(latest), name: '我的名稱', code: 'MINE' })
  const custom = { ...original, kind: 'custom-container' }
  expect(retainEdits({ ...locationDefaults(custom), kind: 'box' }, custom, latest).kind).toBe('box')
})
