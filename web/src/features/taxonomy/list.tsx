import { useId, useState } from 'react'
import { Link } from 'react-router'
import { ChevronDown, ChevronRight, Plus } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { mutationControl as control } from '@/components/mutation-review'
import { messages } from '@/lib/messages'
import { settingsReturnPath, taxonomyCreateRoute, taxonomyEditRoute, type TaxonomyKind } from '@/lib/settings-navigation'
import { visibleCategories, visibleTags, type CategoryTree, type TagIndex } from './model'

const text = messages.taxonomy
export function TaxonomyList({ kind, tree, tags, canWrite }: { kind: TaxonomyKind; tree?: CategoryTree; tags?: TagIndex; canWrite: boolean }) {
  const prefix = useId()
  const [search, setSearch] = useState('')
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(() => new Set(tree?.children.get(null) ?? []))
  const rows = kind === 'categories' && tree ? visibleCategories(tree, expanded, search) : tags ? visibleTags(tags, search) : []
  const searching = search.trim() !== ''
  const visible = new Set(rows.map((record) => record.id))
  return <section className="min-w-0 space-y-5" aria-label={kind === 'categories' ? messages.settings.categories : messages.settings.tags}>
    <div className="max-w-xl space-y-2"><label htmlFor={`${prefix}-search`} className="block text-base font-medium">{kind === 'categories' ? text.searchCategories : text.searchTags}</label>
      <Input id={`${prefix}-search`} type="search" value={search} onChange={(event) => setSearch(event.target.value)} autoComplete="off" aria-describedby={`${prefix}-help`} className="h-auto min-h-12 py-3 text-base md:text-base" />
      <p id={`${prefix}-help`} className="text-sm leading-relaxed text-muted-foreground">{text.searchHelp}</p>
    </div>
    {tree && <div className="flex flex-wrap gap-2"><Button type="button" variant="outline" disabled={searching} className={control} onClick={() => setExpanded(new Set(tree.order))}>{text.expandAll}</Button><Button type="button" variant="outline" disabled={searching} className={control} onClick={() => setExpanded(new Set())}>{text.collapseAll}</Button></div>}
    {rows.length === 0 ? <p role="status" className="py-5 text-base">{text.noMatches}</p> : <ul className="min-w-0 divide-y rounded-lg border bg-card">
      {rows.map((record) => {
        const depth = tree?.depth.get(record.id) ?? 0
        const children = tree?.children.get(record.id) ?? []
        const opened = searching ? children.some((id) => visible.has(id)) : expanded.has(record.id)
        const edit = settingsReturnPath(taxonomyEditRoute(kind, record.id))
        return <li key={record.id} className="min-w-0 p-4" style={{ contentVisibility: 'auto', containIntrinsicSize: 'auto 120px' }}>
          <div className="flex min-w-0 items-start gap-2" style={{ paddingInlineStart: `${Math.min(depth, 4) * 0.6}rem` }}>
            {tree && (children.length > 0 ? <Button type="button" variant="ghost" disabled={searching} aria-expanded={opened} aria-label={`${opened ? text.collapse : text.expand}：${record.name} (${record.id})`} className="size-12 shrink-0 transition-colors" onClick={() => setExpanded((old) => { const next = new Set(old); if (next.has(record.id)) next.delete(record.id); else next.add(record.id); return next })}>
              {opened ? <ChevronDown aria-hidden="true" /> : <ChevronRight aria-hidden="true" />}
            </Button> : <span aria-hidden="true" className="w-12 shrink-0" />)}
            <div className="min-w-0 flex-1 space-y-2">
              {edit ? <Link to={edit} className="block min-h-12 rounded-md py-2 text-lg font-semibold whitespace-pre-wrap [overflow-wrap:anywhere] hover:underline focus-visible:outline-2 focus-visible:outline-ring">「{record.name}」</Link> : <p className="py-2 text-lg font-semibold whitespace-pre-wrap [overflow-wrap:anywhere]">「{record.name}」</p>}
              <p className="text-sm leading-relaxed text-muted-foreground [overflow-wrap:anywhere]">{text.identifier}：{record.id}{tree && <span className="sr-only">；{text.level} {depth + 1}</span>}</p>
              <div className="flex max-w-full flex-wrap gap-2">
                {edit && <Button asChild variant="outline" className={control}><Link to={edit} aria-label={`${kind === 'categories' ? text.editCategory : text.editTag}：${record.name} (${record.id})`}>{kind === 'categories' ? text.editCategory : text.editTag}</Link></Button>}
                {tree && canWrite && edit && <Button asChild variant="outline" className={`${control} gap-2`}><Link to={taxonomyCreateRoute('categories', record.id)} aria-label={`${text.createChild}：${record.name} (${record.id})`}><Plus className="size-4" aria-hidden="true" />{text.createChild}</Link></Button>}
              </div>
            </div>
          </div>
        </li>
      })}
    </ul>}
  </section>
}
