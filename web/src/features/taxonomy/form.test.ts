import { expect, it } from 'vitest'
import { categoryPatch, retainTaxonomyEdits, taxonomyDefaults, taxonomyFormSchema } from './form'

const category = { id: 'category', name: 'Original', parent_id: 'parent', revision: 1 }
const tag = { id: 'tag', name: 'Original', revision: 1 }

it('validates original Unicode text and root-parent input without trimming or normalization', () => {
  for (const name of ['  名稱 é  ', ' ', '😀'.repeat(200)]) {
    expect(taxonomyFormSchema.parse({ name, parent_id: '' })).toEqual({ name, parent_id: '' })
  }
  expect(taxonomyFormSchema.parse({ name: 'Name', parent_id: '😀'.repeat(128) }).parent_id).toBe('😀'.repeat(128))
})

it.each([{ name: '', parent_id: '' }, { name: '\uD800', parent_id: '' }, { name: '😀'.repeat(201), parent_id: '' },
  { name: 'Name', parent_id: '\uD800' }, { name: 'Name', parent_id: '😀'.repeat(129) }])('rejects invalid field values %j', (value) => {
  expect(taxonomyFormSchema.safeParse(value).success).toBe(false)
})

it('defaults only absent metadata and keeps nullable parents separate from tag fields', () => {
  expect(taxonomyDefaults()).toEqual({ name: '', parent_id: '' })
  expect(taxonomyDefaults(category)).toEqual({ name: 'Original', parent_id: 'parent' })
  expect(taxonomyDefaults(tag)).toEqual({ name: 'Original', parent_id: '' })
  expect(taxonomyDefaults({ ...category, parent_id: null })).toEqual({ name: 'Original', parent_id: '' })
})

it('submits only changed category metadata and explicit null for root movement', () => {
  expect(categoryPatch(taxonomyDefaults(category), category)).toBeUndefined()
  expect(categoryPatch({ name: 'Edited', parent_id: 'parent' }, category)).toEqual({ name: 'Edited' })
  expect(categoryPatch({ name: 'Original', parent_id: '' }, category)).toEqual({ parent_id: null })
  expect(categoryPatch({ name: 'Edited', parent_id: 'next' }, category)).toEqual({ name: 'Edited', parent_id: 'next' })
  expect(categoryPatch({ name: 'Original', parent_id: '' }, { ...category, parent_id: null })).toBeUndefined()
})

it('explicitly adopts current untouched fields while retaining changed or invalid draft fields', () => {
  const current = { ...category, name: 'Concurrent', parent_id: 'new-parent', revision: 2 }
  expect(retainTaxonomyEdits({ name: 'Edited', parent_id: 'parent' }, category, current)).toEqual({ name: 'Edited', parent_id: 'new-parent' })
  expect(retainTaxonomyEdits({ name: 'Original', parent_id: '' }, category, current)).toEqual({ name: 'Concurrent', parent_id: '' })
  expect(retainTaxonomyEdits({ name: '', parent_id: 'parent' }, category, current)).toEqual({ name: '', parent_id: 'new-parent' })
  expect(retainTaxonomyEdits(taxonomyDefaults(category), category, current)).toEqual(taxonomyDefaults(current))
  expect(retainTaxonomyEdits({ name: 'Edited', parent_id: '' }, tag, { ...tag, name: 'Concurrent', revision: 2 })).toEqual({ name: 'Edited', parent_id: '' })
})
