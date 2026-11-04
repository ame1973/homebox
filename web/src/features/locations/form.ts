import { z } from 'zod'
import { ApiError, validText } from '@/lib/api/client'
import { messages } from '@/lib/messages'
import type { Location } from '@/lib/api/locations'
import type { components } from '@/lib/api/schema'

export const knownKinds = ['room', 'cabinet', 'shelf', 'box', 'other'] as const
export type KnownKind = typeof knownKinds[number]
const text = messages.locations

export function isKnownKind(value: string): value is KnownKind {
  return knownKinds.some((kind) => kind === value)
}

// Validate the original integer text before converting to Number. Decimal,
// exponent, grouping, or unsafe integer input must never be silently rounded.
export function parseSortOrder(value: string): number | undefined {
  if (!/^-?(0|[1-9][0-9]{0,15})$/.test(value)) return undefined
  const result = Number(value)
  return Number.isSafeInteger(result) ? result : undefined
}

export const locationFormSchema = z.object({
  name: z.string(), code: z.string(), kind: z.enum([...knownKinds, 'custom']), custom_kind: z.string(),
  parent_id: z.string(), sort_order: z.string(), description: z.string(),
}).superRefine((value, ctx) => {
  const issue = (path: keyof typeof value, message: string) => ctx.addIssue({ code: 'custom', path: [path], message })
  if (!validText(value.name, 1, 200)) issue('name', text.nameError)
  if (!validText(value.code, 1, 100)) issue('code', text.codeError)
  if (value.kind === 'custom' && !validText(value.custom_kind, 1, 32)) issue('custom_kind', text.kindError)
  if (!validText(value.parent_id, 0, 128)) issue('parent_id', text.parentError)
  if (parseSortOrder(value.sort_order) === undefined) issue('sort_order', text.sortError)
  if (!validText(value.description, 0, 10000)) issue('description', text.descriptionError)
})

export type LocationValues = z.infer<typeof locationFormSchema>

export function locationDefaults(location?: Location): LocationValues {
  const kind = location?.kind ?? 'other'
  return { name: location?.name ?? '', code: location?.code ?? '', kind: isKnownKind(kind) ? kind : 'custom',
    custom_kind: isKnownKind(kind) ? '' : kind, parent_id: location?.parent_id ?? '',
    sort_order: String(location?.sort_order ?? 0), description: location?.description ?? '' }
}

export function metadataFromValues(values: LocationValues) {
  const sort_order = parseSortOrder(values.sort_order)
  if (sort_order === undefined) throw new ApiError('VALIDATION_ERROR')
  return { name: values.name, code: values.code, kind: values.kind === 'custom' ? values.custom_kind : values.kind,
    sort_order, description: values.description }
}

export function metadataPatch(values: LocationValues, base: Location): components['schemas']['LocationWrite'] | undefined {
  const desired = metadataFromValues(values)
  const patch: { name?: string; code?: string; kind?: string; sort_order?: number; description?: string } = {}
  for (const key of ['name', 'code', 'kind', 'description'] as const) if (desired[key] !== base[key]) patch[key] = desired[key]
  if (desired.sort_order !== base.sort_order) patch.sort_order = desired.sort_order
  return Object.keys(patch).length ? patch : undefined
}

// Explicit rebase keeps only user-changed logical fields. A concurrent rename
// must not be overwritten just because an untouched old name stayed in a form.
// Preserve invalid in-progress text for field validation, not a guessed value.
export function retainEdits(values: LocationValues, original: Location, latest: Location): LocationValues {
  const old = locationDefaults(original)
  const next = locationDefaults(latest)
  for (const key of ['name', 'code', 'description'] as const) if (values[key] !== old[key]) next[key] = values[key]
  if (parseSortOrder(values.sort_order) !== original.sort_order) next.sort_order = values.sort_order
  const desiredKind = values.kind === 'custom' ? values.custom_kind : values.kind
  if (desiredKind !== original.kind) {
    next.kind = values.kind
    next.custom_kind = values.custom_kind
  }
  return next
}
