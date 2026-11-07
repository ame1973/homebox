import { useId, useRef } from 'react'
import { Controller, useForm } from 'react-hook-form'
import { Button } from '@/components/ui/button'
import { Field, FieldError, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { mutationControl as control } from '@/components/mutation-review'
import { itemPageQuerySchema, type ItemPageQuery } from '@/lib/api/catalog'
import { purchasePageQuerySchema } from '@/lib/api/purchases'
import { assetPageQuerySchema } from '@/lib/api/assets'
import type { LocationTree } from '@/features/locations/tree'
import type { CategoryTree } from '@/features/taxonomy/model'
import { messages } from '@/lib/messages'
import { CatalogHierarchyPicker } from './hierarchy-picker'

const text = messages.catalog
type Values = Record<'q' | 'from' | 'to' | 'currency' | 'merchant' | 'category_id' | 'location_id' | 'scope' | 'status' | 'limit', string>
export function CatalogFilters({ query, categories, locations, disabled, onApply, domain = 'catalog' }: {
  query: ItemPageQuery; categories: CategoryTree; locations: LocationTree; disabled: boolean; onApply: (query: ItemPageQuery) => void; domain?: 'catalog' | 'purchases' | 'assets'
}) {
  const prefix = useId()
  const advanced = useRef<HTMLDetailsElement>(null)
  const form = useForm<Values>({ defaultValues: { q: query.q ?? '', from: query.from ?? '', to: query.to ?? '', currency: query.currency ?? '', merchant: query.merchant ?? '',
    category_id: query.category_id ?? '', location_id: query.location_id ?? '', scope: query.scope ?? 'subtree', status: query.status ?? '', limit: String(query.limit ?? 50) } })
  function apply(values: Values) {
    if (disabled) return
    const candidate: Record<string, string | number> = Object.fromEntries(Object.entries(values).filter(([, value]) => value !== ''))
    candidate.limit = /^[1-9][0-9]{0,2}$/.test(values.limit) ? Number(values.limit) : Number.NaN
    const result = (domain === 'purchases' ? purchasePageQuerySchema : domain === 'assets' ? assetPageQuerySchema : itemPageQuerySchema).safeParse(candidate)
    form.clearErrors()
    if (!result.success) {
      if (advanced.current) advanced.current.open = true
      result.error.issues.forEach((issue, index) => form.setError((issue.path[0] ?? 'from') as keyof Values, { message: text.filterError }, { shouldFocus: index === 0 }))
      return
    }
    onApply(result.data)
  }
  const labels = { q: text.search, from: text.from, to: text.to, currency: text.currency, merchant: text.merchant, limit: text.pageSize }
  function field(name: keyof typeof labels) {
    return <Controller key={name} name={name} control={form.control} render={({ field, fieldState }) => <Field data-invalid={fieldState.invalid}>
      <FieldLabel htmlFor={`${prefix}-${name}`} className="text-base">{labels[name]}</FieldLabel>
      {name === 'merchant' ? <Textarea {...field} id={`${prefix}-${name}`} rows={2} maxLength={600} autoComplete="off" aria-invalid={fieldState.invalid} className="min-h-20 text-base md:text-base" /> : <Input {...field} id={`${prefix}-${name}`} type={name === 'from' || name === 'to' ? 'date' : name === 'q' ? 'search' : 'text'} inputMode={name === 'limit' ? 'numeric' : undefined} autoComplete="off" aria-invalid={fieldState.invalid} className="h-auto min-h-12 min-w-0 py-3 text-base md:text-base" />}
      <FieldError errors={[fieldState.error]} />
    </Field>} />
  }
  return <form noValidate onSubmit={(event) => { void form.handleSubmit(apply)(event) }} className="min-w-0 space-y-5 rounded-lg border p-5">
    {domain === 'catalog' && field('q')}
    <details ref={advanced}><summary className="min-h-12 cursor-pointer py-3 font-medium focus-visible:outline-2 focus-visible:outline-ring">{text.advanced}</summary>
      <div className="grid min-w-0 gap-5 py-4 md:grid-cols-2">
        {(['from', 'to', 'currency', 'merchant'] as const).map(field)}
        {(['category_id', 'location_id'] as const).map((name) => <Controller key={name} name={name} control={form.control} render={({ field, fieldState }) => <Field data-invalid={fieldState.invalid}>
          <FieldLabel htmlFor={`${prefix}-${name}`} className="text-base">{name === 'category_id' ? text.fields.category_id : text.location}</FieldLabel>
          <CatalogHierarchyPicker id={`${prefix}-${name}`} tree={name === 'category_id' ? categories : locations} value={field.value} onChange={field.onChange} inputRef={field.ref} empty={name === 'category_id' ? text.categoryAll : text.locationAll} invalid={fieldState.invalid} />
        </Field>} />)}
        {(['scope', 'status'] as const).map((name) => <Controller key={name} name={name} control={form.control} render={({ field, fieldState }) => <Field data-invalid={fieldState.invalid}>
          <FieldLabel htmlFor={`${prefix}-${name}`} className="text-base">{name === 'scope' ? text.scope : text.fields.status}</FieldLabel>
          <NativeSelect {...field} id={`${prefix}-${name}`} className="w-full min-w-0 [&_select]:min-h-12 [&_select]:text-base">
            {(name === 'scope' ? [['subtree', text.subtree], ['direct', text.direct]] : [['', text.allStatus], ['active', text.active], ['archived', text.archived]]).map(([value, label]) => <NativeSelectOption key={value} value={value}>{label}</NativeSelectOption>)}
          </NativeSelect><FieldError errors={[fieldState.error]} />
        </Field>} />)}
        {field('limit')}
      </div>
    </details>
    <p className="max-w-prose text-sm leading-relaxed text-muted-foreground">{domain === 'purchases' ? messages.purchases.filterHelp : domain === 'assets' ? messages.assets.filterHelp : text.filterHelp}</p>
    <div className="flex flex-wrap gap-2"><Button type="submit" variant="outline" disabled={disabled} className={control}>{text.apply}</Button><Button type="button" variant="outline" disabled={disabled} className={control} onClick={() => onApply({})}>{text.reset}</Button></div>
  </form>
}
