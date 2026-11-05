import { fetchLocationsPage, type Location } from '@/lib/api/locations'
import { buildHierarchy, hierarchyPath, readAllMetadata, visibleHierarchy, type Hierarchy } from '@/lib/metadata'

export { subtreeIDs } from '@/lib/metadata'
export type LocationTree = Hierarchy<Location> & Readonly<{ canReceive: ReadonlyMap<string, boolean> }>

const collator = new Intl.Collator('zh-Hant', { sensitivity: 'variant' })

export async function readLocationTree(signal: AbortSignal): Promise<LocationTree> {
  return buildLocationTree(await readAllMetadata(fetchLocationsPage, signal))
}

export function buildLocationTree(records: readonly Location[]): LocationTree {
  const tree = buildHierarchy(records, (a, b) => {
    if (a.sort_order !== b.sort_order) return a.sort_order < b.sort_order ? -1 : 1
    return collator.compare(a.name, b.name) || collator.compare(a.code, b.code) || collator.compare(a.id, b.id)
  })
  const canReceive = new Map<string, boolean>()
  for (const id of tree.order) {
    const record = tree.byID.get(id)!
    canReceive.set(id, record.status === 'active' && (record.parent_id === null || canReceive.get(record.parent_id) === true))
  }
  return { ...tree, canReceive }
}

export function locationPath(tree: LocationTree, id: string | null): Location[] {
  return hierarchyPath(tree, id)
}

export function visibleLocations(tree: LocationTree, expanded: ReadonlySet<string>, search: string, archived: boolean): Location[] {
  const term = search.trim().toLocaleLowerCase('zh-Hant')
  return visibleHierarchy(tree, expanded, (record) =>
    (archived || record.status === 'active') && (!term || `${record.name}\n${record.code}`.toLocaleLowerCase('zh-Hant').includes(term)), term !== '')
}
