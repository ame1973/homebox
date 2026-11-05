import { ApiError } from '@/lib/api/client'
import { fetchItem, type Item } from '@/lib/api/catalog'
import { fetchItemAliasesPage, fetchItemAttributesPage, fetchItemBarcodesPage, fetchItemTagsPage, type Alias, type Attribute, type Barcode } from '@/lib/api/catalog-links'
import type { Tag } from '@/lib/api/taxonomy'
import { readAllMetadata } from '@/lib/metadata'
import { readTags, type TagIndex } from '@/features/taxonomy/model'

export type OwnedMetadata = Readonly<{
  item: Item
  barcodes: readonly Barcode[]
  aliases: readonly Alias[]
  attributes: readonly Attribute[]
  tags: readonly Tag[]
  availableTags: TagIndex
}>

// This is one complete live review read, not a frozen offline snapshot. Bracket
// every owned collection with the real Item aggregate and reject a changed read
// rather than silently reviewing a mixture or installing a newer command proof.
export async function readOwnedMetadata(itemID: string, signal: AbortSignal): Promise<OwnedMetadata> {
  const before = await fetchItem(itemID, signal)
  const [barcodes, aliases, attributes, tags, availableTags] = await Promise.all([
    readAllMetadata((query, signal) => fetchItemBarcodesPage(itemID, query, signal), signal),
    readAllMetadata((query, signal) => fetchItemAliasesPage(itemID, query, signal), signal),
    readAllMetadata((query, signal) => fetchItemAttributesPage(itemID, query, signal), signal),
    readAllMetadata((query, signal) => fetchItemTagsPage(itemID, query, signal), signal),
    readTags(signal),
  ])
  const item = await fetchItem(itemID, signal)
  if (signal.aborted) throw new ApiError('REQUEST_ABORTED')
  if ((Object.keys(before) as (keyof Item)[]).some((key) => before[key] !== item[key])) throw new ApiError('INVALID_RESPONSE')
  for (const tag of tags) {
    const available = availableTags.byID.get(tag.id)
    if (!available || available.revision !== tag.revision || available.name !== tag.name) throw new ApiError('INVALID_RESPONSE')
  }
  return { item, barcodes, aliases, attributes, tags, availableTags }
}

export function tagChanges(before: readonly string[], after: readonly string[]) {
  const oldIDs = new Set(before)
  const newIDs = new Set(after)
  return { added: after.filter((id) => !oldIDs.has(id)), removed: before.filter((id) => !newIDs.has(id)) }
}
