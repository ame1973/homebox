import { useState } from 'react'
import { Link } from 'react-router'
import { ChevronDown, ChevronRight, MapPin } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { messages } from '@/lib/messages'
import { locationRoute } from '@/lib/location-navigation'
import { visibleLocations, type LocationTree } from './tree'

const text = messages.locations
const control = 'min-h-12 h-auto whitespace-normal py-3 text-base! leading-normal transition-colors'

export function LocationTreeView({ tree, selectedID }: { tree: LocationTree; selectedID?: string }) {
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(() => new Set(tree.children.get(null)))
  const [search, setSearch] = useState('')
  const [archived, setArchived] = useState(false)
  const rows = visibleLocations(tree, expanded, search, archived)
  const matching = new Set(visibleLocations(tree, new Set(tree.order), search, archived).map((location) => location.id))
  function toggle(id: string) {
    setExpanded((current) => { const next = new Set(current); if (next.has(id)) next.delete(id); else next.add(id); return next })
  }
  return <section aria-labelledby="location-tree-title" className="min-w-0 space-y-5">
    <div className="flex flex-wrap items-baseline justify-between gap-3"><h2 id="location-tree-title" className="text-xl font-semibold">{text.tree}</h2><p className="text-sm text-muted-foreground">{text.total}{tree.byID.size}</p></div>
    <div className="space-y-3">
      <label htmlFor="location-tree-search" className="block text-base font-medium">{text.search}</label>
      <Input id="location-tree-search" type="search" value={search} onChange={(event) => setSearch(event.target.value)} aria-describedby="location-tree-help" className="h-auto min-h-12 py-3 text-base md:text-base" />
      <p id="location-tree-help" className="text-sm leading-relaxed text-muted-foreground">{text.searchHelp}</p>
      <label className="flex min-h-12 cursor-pointer items-center gap-3 text-base"><input type="checkbox" checked={archived} onChange={(event) => setArchived(event.target.checked)} className="size-5 accent-primary" />{text.showArchived}</label>
      <div className="flex flex-wrap gap-2"><Button type="button" variant="outline" className={control} onClick={() => setExpanded(new Set(tree.order))}>{text.expandAll}</Button><Button type="button" variant="outline" className={control} onClick={() => setExpanded(new Set())}>{text.collapseAll}</Button></div>
    </div>
    {rows.length === 0 ? <p role="status" className="py-6 leading-relaxed text-muted-foreground">{text.noMatches}</p> : <ul aria-label={text.tree} className="space-y-2">
      {rows.map((location) => {
        const hasChildren = (tree.children.get(location.id) ?? []).some((id) => matching.has(id))
        const isExpanded = search.trim() !== '' || expanded.has(location.id)
        const parent = location.parent_id ? tree.byID.get(location.parent_id) : undefined
        const level = (tree.depth.get(location.id) ?? 0) + 1
        return <li key={location.id} style={{ marginInlineStart: Math.min(level - 1, 6) * 8 }}
          className={`min-w-0 rounded-lg border p-3 [contain-intrinsic-size:auto_6rem] [content-visibility:auto] ${selectedID === location.id ? 'border-primary bg-primary/5' : 'bg-card'}`}>
          <div className="flex min-w-0 items-start gap-2">
            {hasChildren ? <Button type="button" variant="ghost" className="min-h-12 min-w-12 shrink-0" aria-label={`${isExpanded ? text.collapse : text.expand} ${location.name}`} aria-expanded={isExpanded} disabled={search.trim() !== ''} onClick={() => toggle(location.id)}>
              {isExpanded ? <ChevronDown aria-hidden="true" /> : <ChevronRight aria-hidden="true" />}
            </Button> : <span className="flex min-h-12 min-w-12 shrink-0 items-center justify-center"><MapPin aria-hidden="true" className="size-5 text-muted-foreground" /></span>}
            <Link to={locationRoute(location.id)} className="min-h-12 min-w-0 flex-1 space-y-1 rounded-md py-2 focus-visible:outline-2 focus-visible:outline-ring">
              <span className="sr-only">{text.level} {level}，{text.parentLabel}：{parent?.name ?? text.root}。</span>
              <span className="block text-base font-semibold [overflow-wrap:anywhere]">{location.name}</span>
              <span className="block text-sm text-muted-foreground [overflow-wrap:anywhere]">{location.code}</span>
            </Link>
          </div>
          {location.status === 'archived' && <p className="pt-2 text-sm font-medium text-muted-foreground">{text.archived}</p>}
        </li>
      })}
    </ul>}
  </section>
}
