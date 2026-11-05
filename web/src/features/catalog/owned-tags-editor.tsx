import { useId, useState } from 'react'
import { Link } from 'react-router'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Field, FieldDescription, FieldLabel, FieldLegend, FieldSet } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { DraftLeaveGuard } from '@/components/draft-leave-guard'
import { mutationControl as control } from '@/components/mutation-review'
import { itemTagsSchema } from '@/lib/api/catalog-links'
import { messages } from '@/lib/messages'
import type { Tag } from '@/lib/api/taxonomy'
import { retainTagEdits } from './owned-form'
import { tagChanges, type OwnedTags } from './owned-model'
import { TagSet } from './owned-review-dialog'
import type { OwnedMutation } from './use-owned-mutation'

const text = messages.owned
export function OwnedTagsEditor({ data, operation, online, canWrite }: { data: OwnedTags; operation: OwnedMutation; online: boolean; canWrite: boolean }) {
  const prefix = useId()
  const [base, setBase] = useState({ item: data.item, tags: data.tags })
  const [selected, setSelected] = useState(() => data.tags.map((tag) => tag.id))
  const [choices, setChoices] = useState(() => new Map(data.tags.map((tag) => [tag.id, tag])))
  const [search, setSearch] = useState('')
  const [noChanges, setNoChanges] = useState(false)
  const selectedIDs = new Set(selected)
  const changed = base.item.revision !== data.item.revision
  const unavailable = selected.some((id) => !data.availableTags.byID.has(id))
  const staleLabels = selected.some((id) => { const current = data.availableTags.byID.get(id); const original = choices.get(id); return !current || !original || current.revision !== original.revision || current.name !== original.name })
  const validSet = itemTagsSchema.safeParse({ tag_ids: selected }).success
  const locked = operation.phase === 'pending' || operation.phase === 'uncertain'
  const disabled = !online || !canWrite || locked
  const changes = tagChanges(base.tags.map((tag) => tag.id), selected)
  const dirty = !!changes.added.length || !!changes.removed.length
  const term = search.toLocaleLowerCase('zh-Hant')
  const options = [...data.availableTags.order, ...selected.filter((id) => !data.availableTags.byID.has(id)).map((id) => choices.get(id)!)]
  const visible = options.filter((tag) => !data.availableTags.byID.has(tag.id) || [tag.id, tag.name].some((value) => value.toLocaleLowerCase('zh-Hant').includes(term)))

  function adopt(keep: boolean) {
    if (disabled) return
    const ids = keep ? retainTagEdits(selected, base.tags.map((tag) => tag.id), data.tags.map((tag) => tag.id)) : data.tags.map((tag) => tag.id)
    setBase({ item: data.item, tags: data.tags })
    setSelected(ids)
    setChoices(new Map(ids.map((id) => [id, data.availableTags.byID.get(id) ?? choices.get(id)!])))
    operation.dismiss()
    setNoChanges(false)
  }
  function toggle(tag: Tag, checked: boolean) {
    if (locked) return
    setSelected((ids) => checked ? [...ids, tag.id] : ids.filter((id) => id !== tag.id))
    if (checked) setChoices((previous) => new Map(previous).set(tag.id, tag))
    setNoChanges(false)
  }
  function review() {
    if (disabled || changed || staleLabels || !validSet) return
    if (!dirty) { setNoChanges(true); return }
    operation.review({ kind: 'replace-tags', owner: base.item, before: base.tags, after: selected.map((id) => choices.get(id)!) })
  }
  return <div className="min-w-0 space-y-6">
    <p className="max-w-prose text-base leading-relaxed">{text.tagHelp}</p>
    <Button asChild variant="outline" className={control}><Link to="/settings/tags">{text.manageTags}</Link></Button>
    {changed && <Alert className="space-y-3 p-4"><AlertTitle>{text.changed}</AlertTitle><AlertDescription className="space-y-3 text-base leading-relaxed"><p>{text.changedHelp}</p><p>{text.ownerProof}{base.item.revision}；{text.currentVersion}{data.item.revision}</p><TagSet tags={data.tags} label={text.beforeTags} />
      <div className="flex flex-wrap gap-2"><Button type="button" variant="outline" disabled={disabled} className={control} onClick={() => adopt(true)}>{text.keepEdits}</Button><Button type="button" variant="outline" disabled={disabled} className={control} onClick={() => adopt(false)}>{text.discardEdits}</Button></div>
    </AlertDescription></Alert>}
    {staleLabels && <Alert className="space-y-3 p-4"><AlertTitle>{text.selectedLabelChanged}</AlertTitle><AlertDescription className="space-y-3 text-base"><p>{text.changedHelp}</p><Button type="button" variant="outline" disabled={disabled || unavailable} className={control} onClick={() => setChoices(new Map(selected.map((id) => [id, data.availableTags.byID.get(id)!])))}>{text.recheckTags}</Button></AlertDescription></Alert>}
    <Field><FieldLabel htmlFor={`${prefix}-search`} className="text-base">{text.search}</FieldLabel><Input id={`${prefix}-search`} type="search" value={search} onChange={(event) => setSearch(event.target.value)} className="h-auto min-h-12 py-3 text-base md:text-base" /><FieldDescription>{text.searchHelp}</FieldDescription></Field>
    <p role="status" className="text-base">{text.selectedTags}{selected.length}</p>
    {!validSet && <p role="alert" className="text-base leading-relaxed">{text.tagLimit}</p>}
    <FieldSet disabled={locked} className="min-w-0"><FieldLegend className="text-xl">{text.selectTags}</FieldLegend>
      {!options.length ? <p>{text.emptyTags}</p> : !visible.length ? <p role="status">{text.noMatches}</p> : <ul className="divide-y rounded-lg border bg-card">{visible.map((tag) => <li key={tag.id} className="min-w-0 p-4 [content-visibility:auto] [contain-intrinsic-size:auto_8rem]">
        <label className="flex min-h-12 min-w-0 cursor-pointer items-start gap-3 py-2"><input type="checkbox" checked={selectedIDs.has(tag.id)} onChange={(event) => toggle(tag, event.target.checked)} className="mt-1 size-5 shrink-0 accent-primary" />
          <span className="min-w-0 whitespace-pre-wrap text-base [overflow-wrap:anywhere]">{JSON.stringify(tag.name)}<span className="mt-1 block text-sm text-muted-foreground">{tag.id}；{text.version}：{tag.revision}</span>{!data.availableTags.byID.has(tag.id) && <span className="mt-2 block text-base">{text.unavailableTag}</span>}</span>
        </label>
      </li>)}</ul>}
    </FieldSet>
    <p className="text-sm text-muted-foreground">{text.ownerProof}{base.item.revision}</p>
    <div className="flex flex-wrap gap-2"><Button type="button" variant="outline" disabled={locked || !selected.length} className={control} onClick={() => { setSelected([]); setNoChanges(false) }}>{text.clearSelection}</Button><Button type="button" disabled={disabled || changed || staleLabels || !validSet} className={control} onClick={review}>{text.review}</Button></div>
    {noChanges && <p role="status">{text.noChanges}</p>}
    <DraftLeaveGuard labels={text} when={dirty || !!operation.command} canLeave={operation.canLeave} onLeave={operation.allowNavigation} onBlocked={operation.suspend} onStay={operation.reopen} />
  </div>
}
