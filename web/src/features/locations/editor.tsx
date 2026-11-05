import { useId, useState } from 'react'
import { Controller, useForm, useWatch } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel, FieldSet } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { Textarea } from '@/components/ui/textarea'
import type { Location } from '@/lib/api/locations'
import { messages } from '@/lib/messages'
import { knownKinds, locationDefaults, locationFormSchema, metadataFromValues, metadataPatch, retainEdits, type LocationValues } from './form'
import { LocationLeaveGuard } from './leave-guard'
import { ParentPicker } from './parent-picker'
import type { LocationTree } from './tree'
import type { LocationMutation } from './use-location-mutation'

const text = messages.locations
const control = 'min-h-12 h-auto whitespace-normal py-3 text-base! leading-normal transition-colors'

type ParentProof = { id: string; revision: number }

export function LocationEditor({ mode, location, tree, parentID = '', operation, online, canWrite }: {
  mode: 'create' | 'edit'; location?: Location; tree: LocationTree; parentID?: string; operation: LocationMutation; online: boolean; canWrite: boolean
}) {
  const prefix = useId()
  const [base, setBase] = useState(location)
  const [noChanges, setNoChanges] = useState(false)
  const [parentProof, setParentProof] = useState<ParentProof | undefined>(() => {
    const parent = tree.byID.get(parentID)
    return parent && tree.canReceive.get(parentID) ? { id: parent.id, revision: parent.revision } : undefined
  })
  const defaults = { ...locationDefaults(base), ...(mode === 'create' ? { parent_id: parentID } : {}) }
  const form = useForm<LocationValues>({ resolver: zodResolver(locationFormSchema), defaultValues: defaults, mode: 'onBlur', reValidateMode: 'onBlur' })
  const kind = useWatch({ control: form.control, name: 'kind' })
  const parent = useWatch({ control: form.control, name: 'parent_id' })
  const selectedParent = parent ? tree.byID.get(parent) : undefined
  const staleParent = mode === 'create' && parent !== '' && (!parentProof || parentProof.id !== parent || selectedParent?.revision !== parentProof.revision || !tree.canReceive.get(parent))
  const locked = operation.phase === 'pending' || operation.phase === 'uncertain'
  const changed = mode === 'edit' && base !== undefined && location !== undefined && base.revision !== location.revision

  function adopt(keep: boolean) {
    if (!base || !location || locked) return
    const values = keep ? retainEdits(form.getValues(), base, location) : locationDefaults(location)
    const fresh = locationDefaults(location)
    setBase(location)
    form.reset(fresh)
    for (const key of Object.keys(values) as (keyof LocationValues)[]) {
      if (values[key] !== fresh[key]) form.setValue(key, values[key], { shouldDirty: true, shouldValidate: true })
    }
    operation.dismiss()
    setNoChanges(false)
  }

  function submit(values: LocationValues) {
    setNoChanges(false)
    if (!online || locked || !canWrite) return
    if (mode === 'edit') {
      if (!base) return
      const body = metadataPatch(values, base)
      if (!body) { setNoChanges(true); return }
      operation.review({ kind: 'patch', subject: base, body })
    } else {
      if (staleParent) { form.setError('parent_id', { message: text.parentError }, { shouldFocus: true }); return }
      operation.review({ kind: 'create', body: { ...metadataFromValues(values), parent_id: values.parent_id || null },
        ...(parentProof && values.parent_id ? { parentRevision: parentProof.revision } : {}) })
    }
  }

  const fields = [
    { name: 'name' as const, label: text.name, help: text.nameHelp, max: 400, required: true },
    { name: 'code' as const, label: text.code, help: text.codeHelp, max: 200, required: true },
    { name: 'sort_order' as const, label: text.sortOrder, help: text.sortHelp, max: 17, required: true },
    { name: 'description' as const, label: text.description, help: text.descriptionHelp, max: 20000, required: false },
  ]
  return <>
    {changed && <Alert className="mb-6 space-y-3 p-4"><AlertTitle>{text.changed}</AlertTitle><AlertDescription className="space-y-3 text-base leading-relaxed">
      <p>{text.changedHelp}</p><p>{text.originalVersion}{base.revision}；{text.currentVersion}{location.revision}</p>
      <p className="[overflow-wrap:anywhere]">{location.name} ({location.code})</p>
      <div className="flex flex-wrap gap-2"><Button type="button" variant="outline" disabled={locked} className={control} onClick={() => adopt(true)}>{text.keepEdits}</Button><Button type="button" variant="outline" disabled={locked} className={control} onClick={() => adopt(false)}>{text.discardEdits}</Button></div>
    </AlertDescription></Alert>}
    <form noValidate aria-busy={operation.phase === 'pending'} onSubmit={form.handleSubmit(submit)} className="space-y-6">
      <FieldSet disabled={locked}>
        <FieldGroup>
          {fields.slice(0, 2).map((definition) => <Controller key={definition.name} name={definition.name} control={form.control} render={({ field, fieldState }) => {
            const id = `${prefix}-${definition.name}`
            return <Field data-invalid={fieldState.invalid}><FieldLabel htmlFor={id} className="text-base">{definition.label}<span aria-hidden="true">*</span></FieldLabel>
              <Input {...field} id={id} required maxLength={definition.max} autoComplete="off" autoCapitalize="none" spellCheck={false} aria-invalid={fieldState.invalid} aria-describedby={`${id}-help${fieldState.invalid ? ` ${id}-error` : ''}`} className="h-auto min-h-12 py-3 text-base md:text-base" />
              <FieldDescription id={`${id}-help`}>{definition.help}</FieldDescription><FieldError id={`${id}-error`} errors={[fieldState.error]} /></Field>
          }} />)}
          <Controller name="kind" control={form.control} render={({ field, fieldState }) => <Field data-invalid={fieldState.invalid}>
            <FieldLabel htmlFor={`${prefix}-kind`} className="text-base">{text.kind}<span aria-hidden="true">*</span></FieldLabel>
            <NativeSelect {...field} id={`${prefix}-kind`} aria-invalid={fieldState.invalid} aria-describedby={`${prefix}-kind-help`} className="w-full [&_select]:h-auto [&_select]:min-h-12 [&_select]:py-3 [&_select]:text-base">
              {knownKinds.map((kind) => <NativeSelectOption key={kind} value={kind}>{text.kinds[kind]}</NativeSelectOption>)}<NativeSelectOption value="custom">{text.kinds.custom}</NativeSelectOption>
            </NativeSelect><FieldDescription id={`${prefix}-kind-help`}>{text.kindHelp}</FieldDescription><FieldError errors={[fieldState.error]} />
          </Field>} />
          {kind === 'custom' && <Controller name="custom_kind" control={form.control} render={({ field, fieldState }) => <Field data-invalid={fieldState.invalid}>
            <FieldLabel htmlFor={`${prefix}-custom-kind`} className="text-base">{text.customKind}<span aria-hidden="true">*</span></FieldLabel>
            <Input {...field} id={`${prefix}-custom-kind`} required maxLength={64} aria-invalid={fieldState.invalid} className="h-auto min-h-12 py-3 text-base md:text-base" /><FieldError errors={[fieldState.error]} />
          </Field>} />}
          {mode === 'create' && <Controller name="parent_id" control={form.control} render={({ field, fieldState }) => <Field data-invalid={fieldState.invalid}>
            <FieldLabel htmlFor={`${prefix}-parent`} className="text-base">{text.parent}</FieldLabel>
            <ParentPicker tree={tree} id={`${prefix}-parent`} value={field.value} inputRef={field.ref} invalid={fieldState.invalid} disabled={locked} onChange={(id) => {
              field.onChange(id)
              const selected = tree.byID.get(id)
              setParentProof(selected && tree.canReceive.get(id) ? { id, revision: selected.revision } : undefined)
            }} />
            {parentProof && parent !== '' && <p className="text-sm text-muted-foreground">{text.parentProof}{parentProof.revision}</p>}
            {staleParent && <Alert className="p-4"><AlertDescription className="space-y-3 text-base"><p>{text.changedHelp}</p><Button type="button" variant="outline" disabled={!selectedParent || !tree.canReceive.get(parent)} className={control} onClick={() => { if (selectedParent) setParentProof({ id: selectedParent.id, revision: selectedParent.revision }) }}>{text.reviewParent}</Button></AlertDescription></Alert>}
            <FieldError errors={[fieldState.error]} />
          </Field>} />}
          {fields.slice(2).map((definition) => <Controller key={definition.name} name={definition.name} control={form.control} render={({ field, fieldState }) => {
            const id = `${prefix}-${definition.name}`
            return <Field data-invalid={fieldState.invalid}><FieldLabel htmlFor={id} className="text-base">{definition.label}{definition.required && <span aria-hidden="true">*</span>}</FieldLabel>
              {definition.name === 'description' ? <Textarea {...field} id={id} rows={5} maxLength={definition.max} aria-invalid={fieldState.invalid} aria-describedby={`${id}-help${fieldState.invalid ? ` ${id}-error` : ''}`} className="min-h-32 text-base md:text-base" /> : <Input {...field} id={id} required maxLength={definition.max} autoComplete="off" aria-invalid={fieldState.invalid} aria-describedby={`${id}-help${fieldState.invalid ? ` ${id}-error` : ''}`} className="h-auto min-h-12 py-3 text-base md:text-base" />}
              <FieldDescription id={`${id}-help`}>{definition.help}</FieldDescription><FieldError id={`${id}-error`} errors={[fieldState.error]} /></Field>
          }} />)}
        </FieldGroup>
      </FieldSet>
      {!online && <p role="status" className="text-base leading-relaxed text-muted-foreground">{text.offlineHelp}</p>}
      {noChanges && <p role="status" className="text-base">{text.noChanges}</p>}
      <Button type="submit" disabled={!online || !canWrite || locked || staleParent} className={`${control} w-full sm:w-auto`}>{text.review}</Button>
    </form>
    <LocationLeaveGuard when={form.formState.isDirty || locked} canLeave={operation.canLeave} onLeave={operation.allowNavigation} onBlocked={operation.suspend} onStay={operation.reopen} />
  </>
}
