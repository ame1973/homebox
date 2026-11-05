import { useState, type Ref } from 'react'
import { Input } from '@/components/ui/input'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { messages } from '@/lib/messages'
import { hierarchyPath } from '@/lib/metadata'
import type { CategoryTree } from './model'

const text = messages.taxonomy
export function CategoryParentPicker({ tree, id, value, onChange, disabled, excluded, invalid, inputRef }: {
  tree: CategoryTree; id: string; value: string; onChange: (id: string) => void; disabled?: boolean
  excluded?: ReadonlySet<string>; invalid?: boolean; inputRef?: Ref<HTMLSelectElement>
}) {
  const [search, setSearch] = useState('')
  const term = search.trim().toLocaleLowerCase('zh-Hant')
  const available = tree.order.filter((nodeID) => !excluded?.has(nodeID))
  const options = available.filter((nodeID) => {
    const record = tree.byID.get(nodeID)!
    return nodeID === value || !term || `${record.name}\n${record.id}`.toLocaleLowerCase('zh-Hant').includes(term)
  })
  const unavailable = value !== '' && !available.includes(value)
  const path = value && tree.byID.has(value) ? hierarchyPath(tree, value) : []
  return <div className="min-w-0 space-y-3">
    <label htmlFor={`${id}-search`} className="block text-sm leading-relaxed text-muted-foreground">{text.parentSearch}</label>
    <Input id={`${id}-search`} type="search" value={search} onChange={(event) => setSearch(event.target.value)} disabled={disabled} autoComplete="off" className="h-auto min-h-12 py-3 text-base md:text-base" />
    <NativeSelect id={id} ref={inputRef} value={value} onChange={(event) => onChange(event.target.value)} disabled={disabled} aria-invalid={invalid} aria-describedby={`${id}-help`}
      className="w-full min-w-0 [&_select]:h-auto [&_select]:min-h-12 [&_select]:py-3 [&_select]:text-base">
      <NativeSelectOption value="">{text.root}</NativeSelectOption>
      {unavailable && <NativeSelectOption value={value} disabled>{text.parentUnavailable}</NativeSelectOption>}
      {options.map((nodeID) => {
        const record = tree.byID.get(nodeID)!
        return <NativeSelectOption key={nodeID} value={nodeID}>{`${'　'.repeat(Math.min(tree.depth.get(nodeID) ?? 0, 4))}「${record.name}」 (${record.id})`}</NativeSelectOption>
      })}
    </NativeSelect>
    <p id={`${id}-help`} className="text-sm leading-relaxed text-muted-foreground">{text.parentHelp}</p>
    {path.length > 0 && <details className="min-w-0 text-sm"><summary className="min-h-11 cursor-pointer py-3 text-muted-foreground focus-visible:outline-2 focus-visible:outline-ring">{text.parent}（{path.length}）</summary>
      <ol aria-label={text.parent} tabIndex={0} className="max-h-48 space-y-2 overflow-y-auto rounded-md border p-3 [overflow-wrap:anywhere] focus-visible:outline-2 focus-visible:outline-ring">
        {path.map((record) => <li key={record.id}>「{record.name}」 ({record.id})</li>)}
      </ol>
    </details>}
  </div>
}
