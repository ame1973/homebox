import { useId, useState } from 'react'
import { Link } from 'react-router'
import { Button } from '@/components/ui/button'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { mutationControl as control } from '@/components/mutation-review'
import { catalogOwnedEditRoute, type CatalogRowKind, type catalogNavigationState } from '@/lib/catalog-navigation'
import { pathIDSchema } from '@/lib/api/domain'
import { messages } from '@/lib/messages'
import { OwnedFields } from './owned-fields'
import { ownedFields, type OwnedRow } from './display'

const text = messages.owned
export function OwnedList({ itemID, kind, records, navigationState }: {
  itemID: string; kind: CatalogRowKind; records: readonly OwnedRow[]; navigationState: ReturnType<typeof catalogNavigationState>
}) {
  const id = useId()
  const [search, setSearch] = useState('')
  const term = search.toLocaleLowerCase('zh-Hant')
  const visible = records.filter((row) => [row.id, ...ownedFields(row).map((field) => field.value)].some((value) => value.toLocaleLowerCase('zh-Hant').includes(term)))
  return <section className="min-w-0 space-y-5">
    <Field><FieldLabel htmlFor={id} className="text-base">{text.search}</FieldLabel><Input id={id} type="search" value={search} onChange={(event) => setSearch(event.target.value)} className="h-auto min-h-12 py-3 text-base md:text-base" /><FieldDescription>{text.searchHelp}</FieldDescription></Field>
    <p className="text-sm text-muted-foreground">{text.count}{records.length}</p>
    {!records.length ? <div className="space-y-3 rounded-lg border p-6"><h2 className="text-xl font-semibold">{text.empty}</h2><p className="text-base text-muted-foreground">{text.emptyHelp}</p></div>
      : !visible.length ? <p role="status">{text.noMatches}</p> : <ul className="divide-y rounded-lg border bg-card">{visible.map((record) => <li key={record.id} className="min-w-0 space-y-4 p-5 [content-visibility:auto] [contain-intrinsic-size:auto_16rem]">
        <OwnedFields value={record} />
        <p className="text-sm text-muted-foreground [overflow-wrap:anywhere]">{text.identifier}：{record.id}<br />{text.rowVersion}：{record.revision}</p>
        {pathIDSchema.safeParse(record.id).success ? <Button asChild variant="outline" className={control}><Link to={catalogOwnedEditRoute(itemID, kind, record.id)} state={navigationState} aria-label={`${text.edit[kind]}：${record.id}`}>{text.edit[kind]}</Link></Button> : <p className="text-base leading-relaxed text-muted-foreground">{text.unaddressable}</p>}
      </li>)}</ul>}
  </section>
}
