import { Link } from 'react-router'
import { messages } from '@/lib/messages'
import { locationRoute } from '@/lib/location-navigation'
import type { Location } from '@/lib/api/locations'
import { locationPath, type LocationTree } from './tree'

const text = messages.locations

export function LocationBreadcrumbs({ tree, location }: { tree: LocationTree; location: Location }) {
  const parents = tree.byID.has(location.id) ? locationPath(tree, location.id).slice(0, -1) : location.parent_id && tree.byID.has(location.parent_id) ? locationPath(tree, location.parent_id) : []
  const links = parents.map((parent) => <li key={parent.id}><Link className="inline-flex min-h-11 items-center rounded-sm py-2 underline underline-offset-4 [overflow-wrap:anywhere] focus-visible:outline-2 focus-visible:outline-ring" to={locationRoute(parent.id)}>{parent.name} ({parent.code})</Link></li>)
  return <nav aria-label={text.parentLabel} className="min-w-0 text-sm leading-relaxed">
    <ol className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1">
      <li><Link className="inline-flex min-h-11 items-center rounded-sm py-2 underline underline-offset-4 focus-visible:outline-2 focus-visible:outline-ring" to="/locations">{text.tree}</Link></li>
      {parents.length > 5 ? <li className="min-w-0"><details><summary className="min-h-11 cursor-pointer py-3 focus-visible:outline-2 focus-visible:outline-ring">{text.parentLabel}（{parents.length}）</summary><ol className="max-h-64 space-y-2 overflow-y-auto rounded-md border p-3">{links}</ol></details></li> : links}
      <li aria-current="page" className="min-w-0 py-2 [overflow-wrap:anywhere]">{location.name}</li>
    </ol>
  </nav>
}
