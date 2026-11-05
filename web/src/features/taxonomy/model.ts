import { ApiError } from '@/lib/api/client'
import { fetchCategoriesPage, fetchTagsPage, type Category, type Tag } from '@/lib/api/taxonomy'
import { buildHierarchy, readAllMetadata, visibleHierarchy, type Hierarchy } from '@/lib/metadata'

export type CategoryTree = Hierarchy<Category>
export type TagIndex = Readonly<{ byID: ReadonlyMap<string, Tag>; order: readonly Tag[] }>

const collator = new Intl.Collator('zh-Hant', { sensitivity: 'variant' })
function compare(a: { id: string; name: string }, b: { id: string; name: string }) {
  return collator.compare(a.name, b.name) || collator.compare(a.id, b.id) || (a.id < b.id ? -1 : a.id > b.id ? 1 : 0)
}
function matches(record: { id: string; name: string }, term: string) {
  return !term || `${record.name}\n${record.id}`.toLocaleLowerCase('zh-Hant').includes(term)
}

export function buildCategoryTree(records: readonly Category[]): CategoryTree {
  return buildHierarchy(records, compare)
}

export async function readCategoryTree(signal: AbortSignal): Promise<CategoryTree> {
  return buildCategoryTree(await readAllMetadata(fetchCategoriesPage, signal))
}

export function visibleCategories(tree: CategoryTree, expanded: ReadonlySet<string>, search: string): Category[] {
  const term = search.trim().toLocaleLowerCase('zh-Hant')
  return visibleHierarchy(tree, expanded, (record) => matches(record, term), term !== '')
}

export function buildTags(records: readonly Tag[]): TagIndex {
  const byID = new Map<string, Tag>()
  const names = new Set<string>()
  for (const record of records) {
    if (byID.has(record.id) || names.has(record.name)) throw new ApiError('INVALID_RESPONSE')
    byID.set(record.id, record)
    names.add(record.name)
  }
  return { byID, order: Array.from(byID.values()).sort(compare) }
}

export async function readTags(signal: AbortSignal): Promise<TagIndex> {
  return buildTags(await readAllMetadata(fetchTagsPage, signal))
}

export function visibleTags(index: TagIndex, search: string): readonly Tag[] {
  const term = search.trim().toLocaleLowerCase('zh-Hant')
  return index.order.filter((record) => matches(record, term))
}
