import { useId, useState } from 'react'
import { Controller, useForm, type FieldErrors, type Resolver } from 'react-hook-form'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Field, FieldDescription, FieldGroup, FieldLabel, FieldLegend, FieldSet } from '@/components/ui/field'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { Textarea } from '@/components/ui/textarea'
import { DraftLeaveGuard } from '@/components/draft-leave-guard'
import { mutationControl as control } from '@/components/mutation-review'
import type { Item } from '@/lib/api/catalog'
import { aliasWriteSchema, attributeWriteSchema, barcodeWriteSchema } from '@/lib/api/catalog-links'
import type { CatalogRowKind } from '@/lib/catalog-navigation'
import { messages } from '@/lib/messages'
import { MetadataError } from './editor'
import { aliasBody, aliasDefaults, aliasPatch, attributeBody, attributeDefaults, attributePatch, barcodeBody, barcodeDefaults, barcodePatch, retainAliasEdits, retainAttributeEdits, retainBarcodeEdits } from './owned-form'
import { OwnedFields } from './owned-fields'
import type { OwnedRow } from './display'
import type { OwnedMutation } from './use-owned-mutation'

const text = messages.owned
type Values = { symbology: string; raw_value: string; alias: string; attribute_key: string; attribute_value: string }
const errors = { symbology: text.symbologyError, raw_value: text.barcodeError, alias: text.aliasError, attribute_key: text.attributeKeyError, attribute_value: text.attributeValueError }
function defaults(subject?: OwnedRow): Values {
  return { ...barcodeDefaults(subject && 'symbology' in subject ? subject : undefined), ...aliasDefaults(subject && 'alias' in subject ? subject : undefined), ...attributeDefaults(subject && 'attribute_key' in subject ? subject : undefined) }
}
function barcodeValues(values: Values) { return { symbology: values.symbology, raw_value: values.raw_value } }
function aliasValues(values: Values) { return { alias: values.alias } }
function attributeValues(values: Values) { return { attribute_key: values.attribute_key, attribute_value: values.attribute_value } }

// The UI keeps one flat form; authoritative field/pair validation still comes
// from the same SDK schemas, with only localized error placement composed here.
function resolver(kind: CatalogRowKind): Resolver<Values> {
  return (values) => {
    const result = kind === 'barcodes' ? barcodeWriteSchema.safeParse(barcodeValues(values)) : kind === 'aliases' ? aliasWriteSchema.safeParse(aliasValues(values)) : attributeWriteSchema.safeParse(attributeValues(values))
    if (result.success) return { values, errors: {} }
    const fieldErrors: FieldErrors<Values> = {}
    for (const issue of result.error.issues) {
      const key = typeof issue.path[0] === 'string' && Object.hasOwn(errors, issue.path[0]) ? issue.path[0] as keyof Values : kind === 'barcodes' ? 'raw_value' : kind === 'aliases' ? 'alias' : 'attribute_key'
      fieldErrors[key] = { type: 'validate', message: errors[key] }
    }
    return { values: {}, errors: fieldErrors }
  }
}

export function OwnedRowEditor({ kind, owner, subject, operation, online, canWrite }: {
  kind: CatalogRowKind; owner: Item; subject?: OwnedRow; operation: OwnedMutation; online: boolean; canWrite: boolean
}) {
  const prefix = useId()
  const [base, setBase] = useState(subject)
  const [baseOwner, setBaseOwner] = useState(owner)
  const [noChanges, setNoChanges] = useState(false)
  const form = useForm<Values>({ resolver: resolver(kind), defaultValues: defaults(base), mode: 'onBlur', reValidateMode: 'onBlur' })
  const locked = operation.phase === 'pending' || operation.phase === 'uncertain'
  // A changed Item is a creation conflict, not a second proof for row writes.
  const changed = base ? !!subject && base.revision !== subject.revision : baseOwner.revision !== owner.revision
  const disabled = !online || !canWrite || locked || changed

  function adopt(keep: boolean) {
    if (!canWrite || locked) return
    const fresh = defaults(subject)
    let values = keep ? form.getValues() : fresh
    if (keep && base && subject) {
      if ('symbology' in base && 'symbology' in subject) values = { ...fresh, ...retainBarcodeEdits(barcodeValues(values), base, subject) }
      else if ('alias' in base && 'alias' in subject) values = { ...fresh, ...retainAliasEdits(aliasValues(values), base, subject) }
      else if ('attribute_key' in base && 'attribute_key' in subject) values = { ...fresh, ...retainAttributeEdits(attributeValues(values), base, subject) }
    }
    setBase(subject)
    setBaseOwner(owner)
    form.reset(fresh)
    for (const key of Object.keys(values) as (keyof Values)[]) if (values[key] !== fresh[key]) form.setValue(key, values[key], { shouldDirty: true, shouldValidate: true })
    operation.dismiss()
    setNoChanges(false)
  }
  function submit(values: Values) {
    if (disabled) return
    setNoChanges(false)
    if (kind === 'barcodes') {
      if (!base) operation.review({ kind: 'create-barcode', owner: baseOwner, body: barcodeBody(barcodeValues(values)) })
      else if ('symbology' in base) {
        const body = barcodePatch(barcodeValues(values), base)
        if (body) operation.review({ kind: 'patch-barcode', owner, subject: base, body }); else setNoChanges(true)
      }
    } else if (kind === 'aliases') {
      if (!base) operation.review({ kind: 'create-alias', owner: baseOwner, body: aliasBody(aliasValues(values)) })
      else if ('alias' in base) {
        const body = aliasPatch(aliasValues(values), base)
        if (body) operation.review({ kind: 'patch-alias', owner, subject: base, body }); else setNoChanges(true)
      }
    } else if (!base) operation.review({ kind: 'create-attribute', owner: baseOwner, body: attributeBody(attributeValues(values)) })
    else if ('attribute_key' in base) {
      const body = attributePatch(attributeValues(values), base)
      if (body) operation.review({ kind: 'patch-attribute', owner, subject: base, body }); else setNoChanges(true)
    }
  }
  function remove() {
    if (!base || disabled) return
    if ('symbology' in base) operation.review({ kind: 'delete-barcode', owner, subject: base })
    else if ('alias' in base) operation.review({ kind: 'delete-alias', owner, subject: base })
    else operation.review({ kind: 'delete-attribute', owner, subject: base })
  }
  function field(name: Exclude<keyof Values, 'symbology'>) {
    return <Controller key={name} name={name} control={form.control} render={({ field, fieldState }) => <Field data-invalid={fieldState.invalid}>
      <FieldLabel htmlFor={`${prefix}-${name}`} className="text-base">{text.fields[name]}{name !== 'attribute_value' && <span aria-hidden="true">*</span>}</FieldLabel>
      <Textarea {...field} id={`${prefix}-${name}`} required={name !== 'attribute_value'} rows={name === 'attribute_value' ? 4 : 2} maxLength={name === 'attribute_value' ? 20000 : name === 'attribute_key' ? 200 : name === 'alias' ? 600 : 1024} autoComplete="off" spellCheck={false} aria-invalid={fieldState.invalid} aria-describedby={`${prefix}-${name}-error`} className="min-h-20 text-base md:text-base" />
      <div id={`${prefix}-${name}-error`}><MetadataError message={errors[name]} error={fieldState.error} /></div>
    </Field>} />
  }
  return <>
    {changed && <Alert className="mb-6 space-y-3 p-4"><AlertTitle>{text.changed}</AlertTitle><AlertDescription className="space-y-3 text-base leading-relaxed"><p>{text.changedHelp}</p><p>{base ? text.rowProof : text.ownerProof}{base?.revision ?? baseOwner.revision}；{text.currentVersion}{base ? subject!.revision : owner.revision}</p>
      {base && subject ? <OwnedFields value={subject} /> : <p>{text.ownerChangedHelp}</p>}
      <div className="flex flex-wrap gap-2"><Button type="button" variant="outline" disabled={!canWrite || locked} className={control} onClick={() => adopt(true)}>{text.keepEdits}</Button><Button type="button" variant="outline" disabled={!canWrite || locked} className={control} onClick={() => adopt(false)}>{text.discardEdits}</Button></div>
    </AlertDescription></Alert>}
    <form noValidate aria-busy={operation.phase === 'pending'} onSubmit={form.handleSubmit(submit)} className="min-w-0 space-y-6">
      <FieldSet disabled={locked}><FieldLegend className="text-xl">{base ? text.edit[kind] : text.create[kind]}</FieldLegend><FieldDescription className="text-base">{kind === 'barcodes' ? text.barcodeHelp : kind === 'aliases' ? text.aliasHelp : text.attributeHelp}</FieldDescription><FieldGroup>
        {kind === 'barcodes' ? <><Controller name="symbology" control={form.control} render={({ field, fieldState }) => <Field data-invalid={fieldState.invalid}>
          <FieldLabel htmlFor={`${prefix}-symbology`} className="text-base">{text.fields.symbology}<span aria-hidden="true">*</span></FieldLabel>
          <NativeSelect {...field} id={`${prefix}-symbology`} required aria-invalid={fieldState.invalid} aria-describedby={`${prefix}-symbology-error`} className="w-full [&_select]:min-h-12 [&_select]:text-base"><NativeSelectOption value="">{text.chooseSymbology}</NativeSelectOption>{Object.entries(text.symbologies).map(([value, label]) => <NativeSelectOption key={value} value={value}>{label}</NativeSelectOption>)}</NativeSelect>
          <div id={`${prefix}-symbology-error`}><MetadataError message={errors.symbology} error={fieldState.error} /></div>
        </Field>} />{field('raw_value')}</> : kind === 'aliases' ? field('alias') : <>{field('attribute_key')}{field('attribute_value')}</>}
      </FieldGroup></FieldSet>
      <p className="text-sm leading-relaxed text-muted-foreground">{text.literalHelp}</p>
      <p className="text-sm text-muted-foreground [overflow-wrap:anywhere]">{base ? `${text.identifier}：${base.id}` : baseOwner.id}<br />{base ? `${text.rowProof}${base.revision}` : `${text.ownerProof}${baseOwner.revision}`}</p>
      {base && <OwnedFields value={base} />}
      {noChanges && <p role="status">{text.noChanges}</p>}
      <Button type="submit" disabled={disabled} className={`${control} w-full sm:w-auto`}>{text.review}</Button>
    </form>
    {base && <section className="mt-8 min-w-0 space-y-4 border-t pt-6"><p className="text-base leading-relaxed text-muted-foreground">{text.deleteHelp}</p><Button type="button" variant="destructive" disabled={disabled} className={control} onClick={remove}>{text.remove[kind]}</Button></section>}
    <DraftLeaveGuard labels={text} when={form.formState.isDirty || !!operation.command} canLeave={operation.canLeave} onLeave={operation.allowNavigation} onBlocked={operation.suspend} onStay={operation.reopen} />
  </>
}
