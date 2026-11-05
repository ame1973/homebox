import { useState, type Ref } from 'react'
import { Input } from '@/components/ui/input'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { messages } from '@/lib/messages'
import { hierarchyPath, type Hierarchy } from '@/lib/metadata'

type Tree = Hierarchy<Readonly<{ id: string; name: string; parent_id: string | null }>>
const text = messages.catalog
export function CatalogHierarchyPicker({ tree, id, value, onChange, empty, disabled, invalid, inputRef }: {
  tree: Tree; id: string; value: string; onChange: (id: string) => void; empty: string; disabled?: boolean; invalid?: boolean; inputRef?: Ref<HTMLSelectElement>
}) {
  const [search, setSearch] = useState('')
  const term = search.trim().toLocaleLowerCase('zh-Hant')
  const options = tree.order.filter((key) => key === value || !term || `${tree.byID.get(key)!.name}\n${key}`.toLocaleLowerCase('zh-Hant').includes(term))
  const path = value && tree.byID.has(value) ? hierarchyPath(tree, value) : []
  return <div className="min-w-0 space-y-3">
    <label htmlFor={`${id}-search`} className="block text-sm text-muted-foreground">{text.pickerSearch}</label>
    <Input id={`${id}-search`} type="search" value={search} onChange={(event) => setSearch(event.target.value)} disabled={disabled} className="h-auto min-h-12 py-3 text-base md:text-base" />
    <NativeSelect id={id} ref={inputRef} value={value} onChange={(event) => onChange(event.target.value)} disabled={disabled} aria-invalid={invalid}
      className="w-full min-w-0 [&_select]:h-auto [&_select]:min-h-12 [&_select]:py-3 [&_select]:text-base">
      <NativeSelectOption value="">{empty}</NativeSelectOption>
      {value !== '' && !tree.byID.has(value) && <NativeSelectOption value={value} disabled>{text.unavailable} ({value})</NativeSelectOption>}
      {options.map((key) => <NativeSelectOption key={key} value={key}>{`${'　'.repeat(Math.min(tree.depth.get(key) ?? 0, 4))}「${tree.byID.get(key)!.name}」 (${key})`}</NativeSelectOption>)}
    </NativeSelect>
    {path.length > 0 && <details className="min-w-0 text-sm"><summary className="min-h-11 cursor-pointer py-3 text-muted-foreground focus-visible:outline-2 focus-visible:outline-ring">{text.selectedPath}（{path.length}）</summary>
      <ol className="space-y-2 rounded-md border p-3 [overflow-wrap:anywhere]">{path.map((record) => <li key={record.id}>「{record.name}」 ({record.id})</li>)}</ol>
    </details>}
  </div>
}
