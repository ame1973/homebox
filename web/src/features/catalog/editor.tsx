import { useId, useState } from 'react'
import { Controller, useForm, useWatch } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel, FieldLegend, FieldSet } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { Textarea } from '@/components/ui/textarea'
import { DraftLeaveGuard } from '@/components/draft-leave-guard'
import { mutationControl as control } from '@/components/mutation-review'
import type { Item } from '@/lib/api/catalog'
import type { Category } from '@/lib/api/taxonomy'
import { messages } from '@/lib/messages'
import type { CategoryTree } from '@/features/taxonomy/model'
import { catalogDefaults, catalogFormSchema, catalogPatch, metadataFromValues, retainCatalogEdits, type CatalogValues } from './form'
import { CatalogHierarchyPicker } from './hierarchy-picker'
import { catalogDisplay } from './display'
import type { CatalogMutation } from './use-catalog-mutation'

const text = messages.catalog
const fieldErrors = { name: text.nameError, brand: text.brandError, model: text.modelError, description: text.textError, notes: text.textError,
  unit_code: text.unitError, base_unit: text.baseUnitError, scale_factor: text.scaleError }

// Reserve the localized message size so blur validation cannot move a pointer
// target between down/up, including wrapped errors at enlarged text sizes.
function MetadataError({ message, error }: { message: string; error?: { message?: string } }) {
  return <div className="grid text-sm font-normal"><span aria-hidden="true" data-error-space={message} className="invisible col-start-1 row-start-1 after:content-[attr(data-error-space)]" /><FieldError className="col-start-1 row-start-1" errors={[error]} /></div>
}

export function CatalogEditor({ mode, subject, categories, operation, online, canWrite }: {
  mode: 'create' | 'edit'; subject?: Item; categories: CategoryTree; operation: CatalogMutation; online: boolean; canWrite: boolean
}) {
  const prefix = useId()
  const [base, setBase] = useState(subject)
  const [choice, setChoice] = useState<Category | undefined>(() => subject?.category_id ? categories.byID.get(subject.category_id) : undefined)
  const [noChanges, setNoChanges] = useState(false)
  const form = useForm<CatalogValues>({ resolver: zodResolver(catalogFormSchema), defaultValues: catalogDefaults(base), mode: 'onBlur', reValidateMode: 'onBlur' })
  const categoryID = useWatch({ control: form.control, name: 'category_id' })
  const brand = useWatch({ control: form.control, name: 'brand_provided' })
  const model = useWatch({ control: form.control, name: 'model_provided' })
  const selected = categories.byID.get(categoryID)
  const unavailable = categoryID !== '' && !selected
  const staleCategory = categoryID !== '' && (!choice || choice.id !== categoryID || choice.revision !== selected?.revision)
  const locked = operation.phase === 'pending' || operation.phase === 'uncertain'
  const changed = base !== undefined && subject !== undefined && base.revision !== subject.revision

  function adopt(keep: boolean) {
    if (!base || !subject || locked) return
    const values = keep ? retainCatalogEdits(form.getValues(), base, subject) : catalogDefaults(subject)
    const fresh = catalogDefaults(subject)
    setBase(subject)
    setChoice(categories.byID.get(values.category_id))
    form.reset(fresh)
    for (const key of Object.keys(values) as (keyof CatalogValues)[]) if (values[key] !== fresh[key]) form.setValue(key, values[key], { shouldDirty: true, shouldValidate: true })
    operation.dismiss()
    setNoChanges(false)
  }
  function submit(values: CatalogValues) {
    if (!online || !canWrite || locked || changed) return
    setNoChanges(false)
    if (unavailable || staleCategory) { form.setError('category_id', { message: text.categoryError }, { shouldFocus: true }); return }
    if (mode === 'create') operation.review({ kind: 'create', body: metadataFromValues(values), category: choice ?? null })
    else if (base) {
      const body = catalogPatch(values, base)
      if (!body) { setNoChanges(true); return }
      operation.review({ kind: 'patch', subject: base, body, ...(body.category_id !== undefined ? { category: choice ?? null } : {}) })
    }
  }
  function field(name: 'name' | 'brand' | 'model' | 'description' | 'notes' | 'unit_code' | 'base_unit' | 'scale_factor') {
    return <Controller key={name} name={name} control={form.control} render={({ field, fieldState }) => <Field data-invalid={fieldState.invalid}>
      <FieldLabel htmlFor={`${prefix}-${name}`} className="text-base">{text.fields[name]}{name === 'name' && <span aria-hidden="true">*</span>}</FieldLabel>
      {name === 'description' || name === 'notes' ? <Textarea {...field} id={`${prefix}-${name}`} rows={4} maxLength={20000} aria-invalid={fieldState.invalid} className="min-h-28 text-base md:text-base" />
        : <Input {...field} id={`${prefix}-${name}`} required={name === 'name'} maxLength={name === 'scale_factor' ? 16 : name === 'unit_code' || name === 'base_unit' ? 64 : 600} inputMode={name === 'scale_factor' ? 'numeric' : undefined} autoComplete="off" spellCheck={false} aria-invalid={fieldState.invalid} className="h-auto min-h-12 py-3 text-base md:text-base" />}
      {name === 'name' && <FieldDescription>{text.nameHelp}</FieldDescription>}
      {(name === 'description' || name === 'notes') && <FieldDescription>{text.textHelp}</FieldDescription>}
      <MetadataError message={fieldErrors[name]} error={fieldState.error} />
    </Field>} />
  }
  return <>
    {changed && <Alert className="mb-6 space-y-3 p-4"><AlertTitle>{text.changed}</AlertTitle><AlertDescription className="space-y-3 text-base leading-relaxed"><p>{text.changedHelp}</p><p>{text.originalVersion}{base.revision}；{text.currentVersion}{subject.revision}</p>
      <dl className="space-y-2">{Object.entries(text.fields).map(([key, label]) => <div key={key} className="[overflow-wrap:anywhere]"><dt className="font-medium">{label}</dt><dd className="whitespace-pre-wrap">{catalogDisplay(key, subject[key as keyof typeof text.fields])}</dd></div>)}</dl>
      <div className="flex flex-wrap gap-2"><Button type="button" variant="outline" disabled={locked} className={control} onClick={() => adopt(true)}>{text.keepEdits}</Button><Button type="button" variant="outline" disabled={locked} className={control} onClick={() => adopt(false)}>{text.discardEdits}</Button></div>
    </AlertDescription></Alert>}
    <form noValidate aria-busy={operation.phase === 'pending'} onSubmit={form.handleSubmit(submit)} className="min-w-0 space-y-7">
      <FieldSet disabled={locked}><FieldLegend className="text-xl">{text.basics}</FieldLegend><FieldGroup>
        {field('name')}
        {(['brand_provided', 'model_provided'] as const).map((name) => <div key={name} className="min-w-0 space-y-3"><Controller name={name} control={form.control} render={({ field }) => <FieldLabel htmlFor={`${prefix}-${name}`} className="flex min-h-12 cursor-pointer items-center gap-3 text-base"><input id={`${prefix}-${name}`} ref={field.ref} name={field.name} type="checkbox" checked={field.value} onChange={(event) => field.onChange(event.target.checked)} onBlur={field.onBlur} className="size-5 shrink-0 accent-primary" />{name === 'brand_provided' ? text.providedBrand : text.providedModel}</FieldLabel>} />
          {(name === 'brand_provided' ? brand : model) && field(name === 'brand_provided' ? 'brand' : 'model')}
        </div>)}
        <p className="text-sm leading-relaxed text-muted-foreground">{text.nullableHelp}</p>
        <Controller name="category_id" control={form.control} render={({ field, fieldState }) => <Field data-invalid={fieldState.invalid}>
          <FieldLabel htmlFor={`${prefix}-category`} className="text-base">{text.fields.category_id}</FieldLabel>
          <CatalogHierarchyPicker id={`${prefix}-category`} tree={categories} value={field.value} onChange={(id) => { field.onChange(id); setChoice(categories.byID.get(id)) }} inputRef={field.ref} empty={text.categoryNone} disabled={locked} invalid={fieldState.invalid} />
          {staleCategory && <Alert className="p-4"><AlertDescription className="space-y-3 text-base"><p>{text.changedHelp}</p><Button type="button" variant="outline" disabled={!selected || locked} className={control} onClick={() => setChoice(selected)}>{text.reviewCategory}</Button></AlertDescription></Alert>}
          <MetadataError message={text.categoryError} error={fieldState.error} />
        </Field>} />
        {field('description')}{field('notes')}
      </FieldGroup></FieldSet>
      <FieldSet disabled={locked}><FieldLegend className="text-xl">{text.measurement}</FieldLegend><p className="text-base leading-relaxed text-muted-foreground">{text.measurementHelp}</p><FieldGroup>
        <Controller name="tracking_mode" control={form.control} render={({ field }) => <Field><FieldLabel htmlFor={`${prefix}-tracking`} className="text-base">{text.fields.tracking_mode}</FieldLabel><NativeSelect {...field} id={`${prefix}-tracking`} className="w-full [&_select]:min-h-12 [&_select]:text-base"><NativeSelectOption value="individual">{text.individual}</NativeSelectOption><NativeSelectOption value="quantity">{text.quantity}</NativeSelectOption></NativeSelect></Field>} />
        {field('unit_code')}{field('base_unit')}{field('scale_factor')}
      </FieldGroup></FieldSet>
      {base && <p className="text-sm text-muted-foreground [overflow-wrap:anywhere]">{text.identifier}：{base.id}<br />{text.originalVersion}{base.revision}</p>}
      {!online && <p role="status" className="text-base leading-relaxed">{text.offlineHelp}</p>}
      {noChanges && <p role="status">{text.noChanges}</p>}
      <Button type="submit" disabled={!online || !canWrite || locked || changed || unavailable || staleCategory} className={`${control} w-full sm:w-auto`}>{text.review}</Button>
    </form>
    <DraftLeaveGuard labels={text} when={form.formState.isDirty || locked} canLeave={operation.canLeave} onLeave={operation.allowNavigation} onBlocked={operation.suspend} onStay={operation.reopen} />
  </>
}
