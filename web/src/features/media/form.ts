import { z } from 'zod'
import { ApiError } from '@/lib/api/client'
import { input, parseSafeCount } from '@/lib/api/domain'
import { mediaLinkSchema, mediaLinkViewSchema, type MediaLink, type MediaLinkView } from '@/lib/api/media'
import { mediaLinkMetadataSchema, mediaLinkPatchSchema, type MediaLinkPatchInput } from '@/lib/api/media-writes'

const valuesSchema = z.strictObject({ is_cover: z.boolean(), sort_order: z.string(), description: z.string() })
const linkSchema = z.union([mediaLinkSchema, mediaLinkViewSchema])
export type MediaLinkValues = z.infer<typeof valuesSchema>
type ReviewedLink = MediaLink | MediaLinkView

function linkValues(link: ReviewedLink): MediaLinkValues {
  return { is_cover: link.is_cover, sort_order: String(link.sort_order), description: link.description }
}

export function mediaLinkDefaults(link?: ReviewedLink): MediaLinkValues {
  return link === undefined ? { is_cover: false, sort_order: '0', description: '' } : linkValues(input(linkSchema, link))
}

// Attachment makes the visible default choices explicit; raw text is preserved
// and the position must be canonical and exactly representable.
export function mediaLinkCreateBody(values: MediaLinkValues) {
  const checked = input(valuesSchema, values)
  return input(mediaLinkMetadataSchema, { ...checked, sort_order: parseSafeCount(checked.sort_order) ?? NaN })
}

export function mediaLinkPatch(values: MediaLinkValues, original: ReviewedLink): MediaLinkPatchInput | undefined {
  const checked = input(valuesSchema, values), before = linkValues(input(linkSchema, original))
  const patch = { ...(checked.is_cover !== before.is_cover ? { is_cover: checked.is_cover } : {}),
    ...(checked.sort_order !== before.sort_order ? { sort_order: parseSafeCount(checked.sort_order) ?? NaN } : {}),
    ...(checked.description !== before.description ? { description: checked.description } : {}) }
  return Object.keys(patch).length ? input(mediaLinkPatchSchema, patch) : undefined
}

// Only explicit adoption changes the baseline. Preserve edited raw strings,
// including invalid input, while retaining the same independent link identity.
export function retainMediaLinkEdits(values: MediaLinkValues, original: ReviewedLink, current: ReviewedLink): MediaLinkValues {
  const checked = input(valuesSchema, values), before = input(linkSchema, original), after = input(linkSchema, current)
  if (before.id !== after.id || before.media_id !== after.media_id || before.owner_type !== after.owner_type || before.owner_id !== after.owner_id) {
    throw new ApiError('VALIDATION_ERROR')
  }
  const previous = linkValues(before), next = linkValues(after)
  return { is_cover: checked.is_cover !== previous.is_cover ? checked.is_cover : next.is_cover,
    sort_order: checked.sort_order !== previous.sort_order ? checked.sort_order : next.sort_order,
    description: checked.description !== previous.description ? checked.description : next.description }
}
