import { useId, useState } from 'react'
import { Controller, useForm, useWatch } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { DraftLeaveGuard } from '@/components/draft-leave-guard'
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel, FieldSet } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { mutationControl as control } from '@/components/mutation-review'
import type { Category, Tag } from '@/lib/api/taxonomy'
import { messages } from '@/lib/messages'
import { subtreeIDs } from '@/lib/metadata'
import type { TaxonomyKind } from '@/lib/settings-navigation'
import { categoryPatch, retainTaxonomyEdits, taxonomyDefaults, taxonomyFormSchema, type TaxonomyValues } from './form'
import { CategoryParentPicker } from './parent-picker'
import type { CategoryTree } from './model'
import type { TaxonomyMutation } from './use-taxonomy-mutation'

const text = messages.taxonomy
export function TaxonomyEditor({ kind, mode, subject, tree, parentID = '', operation, online, canWrite }: {
  kind: TaxonomyKind; mode: 'create' | 'edit'; subject?: Category | Tag; tree?: CategoryTree; parentID?: string
  operation: TaxonomyMutation; online: boolean; canWrite: boolean
}) {
  const prefix = useId()
  const [base, setBase] = useState(subject)
  const [noChanges, setNoChanges] = useState(false)
  const [parentProof, setParentProof] = useState<Category | undefined>(() => tree?.byID.get(parentID))
  const defaults = { ...taxonomyDefaults(base), ...(mode === 'create' && kind === 'categories' ? { parent_id: parentID } : {}) }
  const form = useForm<TaxonomyValues>({ resolver: zodResolver(taxonomyFormSchema), defaultValues: defaults, mode: 'onBlur', reValidateMode: 'onBlur' })
  const parent = useWatch({ control: form.control, name: 'parent_id' })
  const selectedParent = tree?.byID.get(parent)
  const excluded = base && tree ? subtreeIDs(tree, base.id) : undefined
  const unavailableParent = kind === 'categories' && parent !== '' && (!selectedParent || excluded?.has(parent))
  const staleParent = kind === 'categories' && mode === 'create' && parent !== '' && (!parentProof || parentProof.id !== parent || selectedParent?.revision !== parentProof.revision)
  const locked = operation.phase === 'pending' || operation.phase === 'uncertain'
  const changed = base !== undefined && subject !== undefined && base.revision !== subject.revision
  const hasChildren = base !== undefined && (tree?.descendantCount.get(base.id) ?? 0) > 0

  function adopt(keep: boolean) {
    if (!base || !subject || locked) return
    const values = keep ? retainTaxonomyEdits(form.getValues(), base, subject) : taxonomyDefaults(subject)
    const fresh = taxonomyDefaults(subject)
    setBase(subject)
    form.reset(fresh)
    for (const key of ['name', 'parent_id'] as const) if (values[key] !== fresh[key]) form.setValue(key, values[key], { shouldDirty: true, shouldValidate: true })
    operation.dismiss()
    setNoChanges(false)
  }

  function submit(values: TaxonomyValues) {
    setNoChanges(false)
    if (!online || !canWrite || locked || changed) return
    if (unavailableParent || staleParent) { form.setError('parent_id', { message: text.parentError }, { shouldFocus: true }); return }
    if (kind === 'categories') {
      if (mode === 'create') {
        operation.review({ kind: 'create-category', body: { name: values.name, parent_id: values.parent_id || null }, ...(parentProof && values.parent_id ? { parent: parentProof } : {}) })
      } else if (base && 'parent_id' in base) {
        const body = categoryPatch(values, base)
        if (!body) { setNoChanges(true); return }
        operation.review({ kind: 'patch-category', subject: base, body, parent: selectedParent ?? null, previousParent: base.parent_id ? tree?.byID.get(base.parent_id) : null })
      }
    } else if (mode === 'create') operation.review({ kind: 'create-tag', body: { name: values.name } })
    else if (base) {
      if (values.name === base.name) { setNoChanges(true); return }
      operation.review({ kind: 'patch-tag', subject: base, body: { name: values.name } })
    }
  }

  function remove() {
    if (!base || !online || !canWrite || locked || changed || hasChildren) return
    if (kind === 'categories' && 'parent_id' in base) operation.review({ kind: 'delete-category', subject: base })
    else if (kind === 'tags') operation.review({ kind: 'delete-tag', subject: base })
  }

  return <>
    {changed && <Alert className="mb-6 space-y-3 p-4"><AlertTitle>{text.changed}</AlertTitle><AlertDescription className="space-y-3 text-base leading-relaxed"><p>{text.changedHelp}</p>
      <p>{text.originalVersion}{base.revision}；{text.currentVersion}{subject.revision}</p><p className="whitespace-pre-wrap [overflow-wrap:anywhere]">「{subject.name}」 ({subject.id})</p>
      {'parent_id' in subject && <p className="[overflow-wrap:anywhere]">{text.parent}：{subject.parent_id ?? text.root}</p>}
      <div className="flex flex-wrap gap-2"><Button type="button" variant="outline" disabled={locked} className={control} onClick={() => adopt(true)}>{text.keepEdits}</Button><Button type="button" variant="outline" disabled={locked} className={control} onClick={() => adopt(false)}>{text.discardEdits}</Button></div>
    </AlertDescription></Alert>}
    <form noValidate aria-busy={operation.phase === 'pending'} onSubmit={form.handleSubmit(submit)} className="space-y-6">
      <FieldSet disabled={locked}><FieldGroup>
        <Controller name="name" control={form.control} render={({ field, fieldState }) => <Field data-invalid={fieldState.invalid}>
          <FieldLabel htmlFor={`${prefix}-name`} className="text-base">{kind === 'categories' ? text.categoryName : text.tagName}<span aria-hidden="true">*</span></FieldLabel>
          <Input {...field} id={`${prefix}-name`} required maxLength={400} autoComplete="off" spellCheck={false} aria-invalid={fieldState.invalid} aria-describedby={`${prefix}-name-help${fieldState.invalid ? ` ${prefix}-name-error` : ''}`} className="h-auto min-h-12 py-3 text-base md:text-base" />
          <FieldDescription id={`${prefix}-name-help`}>{kind === 'categories' ? text.categoryNameHelp : text.tagNameHelp}</FieldDescription><FieldError id={`${prefix}-name-error`} errors={[fieldState.error]} />
        </Field>} />
        {kind === 'categories' && tree && <Controller name="parent_id" control={form.control} render={({ field, fieldState }) => <Field data-invalid={fieldState.invalid}>
          <FieldLabel htmlFor={`${prefix}-parent`} className="text-base">{text.parent}</FieldLabel>
          <CategoryParentPicker tree={tree} id={`${prefix}-parent`} value={field.value} excluded={excluded} inputRef={field.ref} invalid={fieldState.invalid} disabled={locked} onChange={(id) => { field.onChange(id); setParentProof(tree.byID.get(id)) }} />
          {mode === 'create' && parentProof && parent !== '' && <p className="text-sm text-muted-foreground">{text.parentProof}{parentProof.revision}</p>}
          {staleParent && <Alert className="p-4"><AlertDescription className="space-y-3 text-base"><p>{text.changedHelp}</p><Button type="button" variant="outline" disabled={!selectedParent} className={control} onClick={() => { if (selectedParent) setParentProof(selectedParent) }}>{text.reviewParent}</Button></AlertDescription></Alert>}
          <FieldError errors={[fieldState.error]} />
        </Field>} />}
      </FieldGroup></FieldSet>
      {base && <p className="text-sm text-muted-foreground [overflow-wrap:anywhere]">{text.identifier}：{base.id}<br />{text.originalVersion}{base.revision}</p>}
      {!online && <p role="status" className="text-base leading-relaxed text-muted-foreground">{text.offlineHelp}</p>}
      {noChanges && <p role="status" className="text-base">{text.noChanges}</p>}
      <Button type="submit" disabled={!online || !canWrite || locked || changed || unavailableParent || staleParent} className={`${control} w-full sm:w-auto`}>{text.review}</Button>
    </form>
    {mode === 'edit' && base && <section className="mt-8 space-y-4 border-t pt-6"><p className="text-base leading-relaxed text-muted-foreground">{kind === 'categories' ? text.deleteCategoryHelp : text.deleteTagHelp}</p>
      {hasChildren && <p role="status" className="text-base leading-relaxed">{text.children}</p>}
      <Button type="button" variant="destructive" disabled={!online || !canWrite || locked || changed || hasChildren} className={control} onClick={remove}>{kind === 'categories' ? text.deleteCategory : text.deleteTag}</Button>
    </section>}
    <DraftLeaveGuard labels={text} when={form.formState.isDirty || locked} canLeave={operation.canLeave} onLeave={operation.allowNavigation} onBlocked={operation.suspend} onStay={operation.reopen} />
  </>
}
