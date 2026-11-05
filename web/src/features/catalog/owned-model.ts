import { ApiError } from '@/lib/api/client'
import { fetchItem, type Item } from '@/lib/api/catalog'
import { fetchItemAliasesPage, fetchItemAttributesPage, fetchItemBarcodesPage, fetchItemTagsPage, type Alias, type Attribute, type Barcode } from '@/lib/api/catalog-links'
import type { Tag } from '@/lib/api/taxonomy'
import type { CatalogOwnedKind } from '@/lib/catalog-navigation'
import { readAllMetadata } from '@/lib/metadata'
import { readTags, type TagIndex } from '@/features/taxonomy/model'

export type OwnedTags = Readonly<{ kind: 'tags'; item: Item; tags: readonly Tag[]; availableTags: TagIndex }>
export type OwnedMetadata = OwnedTags
  | Readonly<{ kind: 'barcodes'; item: Item; rows: readonly Barcode[] }>
  | Readonly<{ kind: 'aliases'; item: Item; rows: readonly Alias[] }>
  | Readonly<{ kind: 'attributes'; item: Item; rows: readonly Attribute[] }>

// Complete the selected live collection, not unrelated collections on every
// route. Bracket it with Item authority; never invent empty unqueried sets or
// treat these live pages as the frozen offline snapshot protocol.
export async function readOwnedMetadata(itemID: string, kind: CatalogOwnedKind, signal: AbortSignal): Promise<OwnedMetadata> {
  const before = await fetchItem(itemID, signal)
  const collection = kind === 'tags' ? await readAssignedTags(itemID, signal)
    : kind === 'barcodes' ? { kind, rows: await readAllMetadata((query, signal) => fetchItemBarcodesPage(itemID, query, signal), signal) }
      : kind === 'aliases' ? { kind, rows: await readAllMetadata((query, signal) => fetchItemAliasesPage(itemID, query, signal), signal) }
        : { kind, rows: await readAllMetadata((query, signal) => fetchItemAttributesPage(itemID, query, signal), signal) }
  const item = await fetchItem(itemID, signal)
  if (signal.aborted) throw new ApiError('REQUEST_ABORTED')
  if ((Object.keys(before) as (keyof Item)[]).some((key) => before[key] !== item[key])) throw new ApiError('INVALID_RESPONSE')
  return { ...collection, item }
}

async function readAssignedTags(itemID: string, signal: AbortSignal) {
  const [tags, availableTags] = await Promise.all([
    readAllMetadata((query, signal) => fetchItemTagsPage(itemID, query, signal), signal), readTags(signal),
  ])
  for (const tag of tags) {
    const available = availableTags.byID.get(tag.id)
    if (!available || available.revision !== tag.revision || available.name !== tag.name) throw new ApiError('INVALID_RESPONSE')
  }
  return { kind: 'tags' as const, tags, availableTags }
}

export function tagChanges(before: readonly string[], after: readonly string[]) {
  const oldIDs = new Set(before)
  const newIDs = new Set(after)
  return { added: after.filter((id) => !oldIDs.has(id)), removed: before.filter((id) => !newIDs.has(id)) }
}
