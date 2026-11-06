import { useId, useState, type Ref } from 'react'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import type { Location } from '@/lib/api/locations'
import { messages } from '@/lib/messages'
import type { LocationTree } from '@/features/locations/tree'
import { MetadataError } from '@/features/catalog/editor'
import { HoldingPath } from './holding-fields'

const text = messages.holdings
export function HoldingLocationPicker({ label, value, tree, allowed, path, onChange, onBlur, inputRef, error, disabled, nullable = false }: {
  label: string; value: string; tree: LocationTree; allowed: ReadonlySet<string>; path: readonly Location[] | undefined
  onChange: (id: string) => void; onBlur?: () => void; inputRef?: Ref<HTMLSelectElement>; error?: { message?: string }; disabled: boolean; nullable?: boolean
}) {
  const prefix = useId(), [search, setSearch] = useState('')
  const term = search.trim().toLocaleLowerCase('zh-Hant')
  const available = !value || allowed.has(value) && tree.byID.has(value)
  const options = tree.order.filter((id) => allowed.has(id) && (id === value || !term || `${tree.byID.get(id)!.name}\n${tree.byID.get(id)!.code}\n${id}`.toLocaleLowerCase('zh-Hant').includes(term)))
  return <Field data-invalid={!!error} className="min-w-0">
    <FieldLabel htmlFor={`${prefix}-choice`} className="text-base">{label}{!nullable && <span aria-hidden="true">*</span>}</FieldLabel>
    <FieldLabel htmlFor={`${prefix}-search`} className="text-sm font-normal">{label}：{text.locationSearch}</FieldLabel>
    <Input id={`${prefix}-search`} value={search} onChange={(event) => setSearch(event.target.value)} disabled={disabled} autoComplete="off" className="min-h-12 text-base md:text-base" />
    <NativeSelect ref={inputRef} id={`${prefix}-choice`} value={value} disabled={disabled} onBlur={onBlur} onChange={(event) => onChange(event.target.value)} required={!nullable} aria-invalid={!!error || !available} aria-describedby={`${prefix}-help ${prefix}-error`} className="w-full [&_select]:min-h-12 [&_select]:text-base">
      <NativeSelectOption value="">{nullable ? text.unknownLocation : text.chooseLocation}</NativeSelectOption>
      {!available && <NativeSelectOption value={value}>{text.unavailable} ({JSON.stringify(value)})</NativeSelectOption>}
      {options.map((id) => { const location = tree.byID.get(id)!; return <NativeSelectOption key={id} value={id}>{'—'.repeat(Math.min(tree.depth.get(id) ?? 0, 4))}{location.name} ({location.code})</NativeSelectOption> })}
    </NativeSelect>
    <FieldDescription id={`${prefix}-help`} className="text-base">{nullable ? text.createLocationHelp : text.locationHelp}</FieldDescription>
    <div id={`${prefix}-error`}><MetadataError message={text.locationError} error={error ?? (!available ? { message: text.locationError } : undefined)} /></div>
    <HoldingPath path={path} label={`${label}：${text.selectedPath}`} />
  </Field>
}
