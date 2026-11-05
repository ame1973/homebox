import { z } from 'zod'
import { validText } from '@/lib/api/client'
import type { Category, Tag } from '@/lib/api/taxonomy'
import type { components } from '@/lib/api/schema'
import { messages } from '@/lib/messages'

const text = messages.taxonomy
export const taxonomyFormSchema = z.object({ name: z.string(), parent_id: z.string() }).superRefine((value, context) => {
  if (!validText(value.name, 1, 200)) context.addIssue({ code: 'custom', path: ['name'], message: text.nameError })
  if (!validText(value.parent_id, 0, 128)) context.addIssue({ code: 'custom', path: ['parent_id'], message: text.parentError })
})
export type TaxonomyValues = z.infer<typeof taxonomyFormSchema>

export function taxonomyDefaults(record?: Category | Tag): TaxonomyValues {
  return { name: record?.name ?? '', parent_id: record && 'parent_id' in record ? record.parent_id ?? '' : '' }
}

export function categoryPatch(values: TaxonomyValues, original: Category): components['schemas']['CategoryWrite'] | undefined {
  const patch: { name?: string; parent_id?: string | null } = {}
  if (values.name !== original.name) patch.name = values.name
  if (values.parent_id !== (original.parent_id ?? '')) patch.parent_id = values.parent_id || null
  return Object.keys(patch).length ? patch : undefined
}

// Explicit adoption keeps only fields actually changed by the person. Untouched
// fields adopt current source state; invalid in-progress text is never discarded.
export function retainTaxonomyEdits(values: TaxonomyValues, original: Category | Tag, current: Category | Tag): TaxonomyValues {
  const before = taxonomyDefaults(original)
  const next = taxonomyDefaults(current)
  return { name: values.name === before.name ? next.name : values.name,
    parent_id: values.parent_id === before.parent_id ? next.parent_id : values.parent_id }
}
