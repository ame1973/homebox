import { z } from 'zod'
import { itemCreateSchema, type Item } from '@/lib/api/catalog'
import { input, parseSafeCount } from '@/lib/api/domain'
import type { components } from '@/lib/api/schema'
import { messages } from '@/lib/messages'

const text = messages.catalog
const fields = z.strictObject({
  name: z.string(), brand_provided: z.boolean(), brand: z.string(), model_provided: z.boolean(), model: z.string(),
  category_id: z.string(), description: z.string(), notes: z.string(), tracking_mode: z.enum(['individual', 'quantity']),
  unit_code: z.string(), base_unit: z.string(), scale_factor: z.string(),
})
export type CatalogValues = z.infer<typeof fields>

// Parse the original decimal text, never round a floating/exponent/grouped value.
export function parseScaleFactor(value: string): number | undefined {
  return parseSafeCount(value, 1)
}

function desiredMetadata(value: CatalogValues) {
  return { name: value.name, brand: value.brand_provided ? value.brand : null, model: value.model_provided ? value.model : null,
    category_id: value.category_id || null, description: value.description, notes: value.notes, tracking_mode: value.tracking_mode,
    unit_code: value.unit_code, base_unit: value.base_unit, scale_factor: parseScaleFactor(value.scale_factor) ?? 0 }
}

export const catalogFormSchema = fields.superRefine((value, context) => {
  const result = itemCreateSchema.safeParse(desiredMetadata(value))
  if (result.success) return
  const errors = { name: text.nameError, brand: text.brandError, model: text.modelError, category_id: text.categoryError,
    description: text.textError, notes: text.textError, unit_code: text.unitError, base_unit: text.baseUnitError, scale_factor: text.scaleError }
  for (const error of result.error.issues) {
    const key = error.path[0] as keyof typeof errors
    context.addIssue({ code: 'custom', path: [key], message: errors[key] })
  }
})

export function catalogDefaults(item?: Item): CatalogValues {
  return { name: item?.name ?? '', brand_provided: item?.brand != null, brand: item?.brand ?? '', model_provided: item?.model != null, model: item?.model ?? '',
    category_id: item?.category_id ?? '', description: item?.description ?? '', notes: item?.notes ?? '', tracking_mode: item?.tracking_mode ?? 'individual',
    unit_code: item?.unit_code ?? 'pcs', base_unit: item?.base_unit ?? 'pcs', scale_factor: String(item?.scale_factor ?? 1) }
}

export function metadataFromValues(values: CatalogValues) {
  return desiredMetadata(input(catalogFormSchema, values))
}

export function catalogPatch(values: CatalogValues, original: Item): components['schemas']['ItemWrite'] | undefined {
  const desired = metadataFromValues(values)
  const patch: components['schemas']['ItemWrite'] = {}
  for (const key of Object.keys(desired) as (keyof typeof desired)[]) if (desired[key] !== original[key]) Object.assign(patch, { [key]: desired[key] })
  return Object.keys(patch).length ? patch : undefined
}

// Adoption is explicit. Nullable text is one logical field, and tracking plus
// measurement stays one interpretation: never combine half of a concurrent tuple
// with half of a local edit. Invalid in-progress text remains available to review.
export function retainCatalogEdits(values: CatalogValues, original: Item, current: Item): CatalogValues {
  const before = catalogDefaults(original)
  const next = catalogDefaults(current)
  for (const key of ['name', 'category_id', 'description', 'notes'] as const) if (values[key] !== before[key]) next[key] = values[key]
  for (const key of ['brand', 'model'] as const) {
    const provided = key === 'brand' ? 'brand_provided' : 'model_provided'
    if (values[provided] !== before[provided] || values[provided] && values[key] !== before[key]) { next[provided] = values[provided]; next[key] = values[key] }
  }
  const interpretation = ['tracking_mode', 'unit_code', 'base_unit', 'scale_factor'] as const
  if (interpretation.some((key) => values[key] !== before[key])) {
    next.tracking_mode = values.tracking_mode
    next.unit_code = values.unit_code
    next.base_unit = values.base_unit
    next.scale_factor = values.scale_factor
  }
  return next
}
