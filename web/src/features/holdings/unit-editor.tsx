import { useId, useState } from 'react'
import { Controller, useForm, useWatch } from 'react-hook-form'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Field, FieldDescription, FieldGroup, FieldLabel, FieldLegend, FieldSet } from '@/components/ui/field'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { Textarea } from '@/components/ui/textarea'
import { DraftLeaveGuard } from '@/components/draft-leave-guard'
import { mutationControl as control } from '@/components/mutation-review'
import type { Item } from '@/lib/api/catalog'
import type { Unit } from '@/lib/api/holdings'
import { messages } from '@/lib/messages'
import type { LocationTree } from '@/features/locations/tree'
import { MetadataError } from '@/features/catalog/editor'
import { holdingPath, sameHoldingPath } from './display'
import { unitFormResolver, type UnitEditorMode, type UnitEditorValues } from './editor-validation'
import { retainUnitEdits, unitCreateBody, unitCreateDefaults, unitMetadataDefaults, unitMoveBody, unitPatch, unitStatusBody } from './form'
import { HoldingError } from './holding-error'
import { HoldingPath, UnitFields } from './holding-fields'
import { HoldingLocationPicker } from './location-picker'
import type { HoldingMutation } from './use-holding-mutation'

const text = messages.holdings
function defaults(unit?: Unit): UnitEditorValues {
  return { ...unitCreateDefaults(), ...unitMetadataDefaults(unit), status: unit?.status ?? 'stored', target_location_id: unit?.location_id ?? '', disposal_kind: '', note: '' }
}
export function UnitEditor({ mode, item, unit, locations, operation, online, canWrite }: {
  mode: UnitEditorMode; item: Item; unit?: Unit; locations: LocationTree; operation: HoldingMutation; online: boolean; canWrite: boolean
}) {
  const prefix = useId()
  const [baseItem, setBaseItem] = useState(item), [base, setBase] = useState(unit)
  const [sourcePath, setSourcePath] = useState(() => holdingPath(locations, unit?.location_id ?? null))
  const [choice, setChoice] = useState(() => holdingPath(locations, mode === 'move' ? unit?.location_id ?? null : null))
  const [noChanges, setNoChanges] = useState(false), [localError, setLocalError] = useState<unknown>()
  const form = useForm<UnitEditorValues>({ resolver: unitFormResolver(mode), defaultValues: defaults(base), mode: 'onBlur', reValidateMode: 'onBlur' })
  const provided = useWatch({ control: form.control, name: 'serial_provided' })
  const status = useWatch({ control: form.control, name: 'status' })
  const selected = useWatch({ control: form.control, name: mode === 'create' ? 'location_id' : 'target_location_id' })
  const currentSource = holdingPath(locations, base?.location_id ?? null)
  const currentChoice = holdingPath(locations, selected || null)
  const locked = operation.phase === 'pending' || operation.phase === 'uncertain'
  const changed = base ? base.revision !== unit?.revision : baseItem.revision !== item.revision
  const selecting = mode === 'create' || mode === 'move'
  const stalePath = !sameHoldingPath(sourcePath, currentSource) || selecting && !sameHoldingPath(choice, currentChoice)
  const unavailable = currentSource === undefined || selecting && selected !== '' && currentChoice === undefined
  const disabled = !online || !canWrite || locked || changed || stalePath || unavailable
  const allowed = new Set(locations.order.filter((id) => locations.canReceive.get(id) || mode === 'move' && id === base?.location_id))

  function recheckPaths() {
    if (!canWrite || locked || unavailable) return
    setSourcePath(currentSource); setChoice(currentChoice); setLocalError(undefined)
    operation.dismiss()
  }
  function adopt(keep: boolean) {
    if (!canWrite || locked || item.tracking_mode !== 'individual') return
    const fresh = defaults(unit), previous = form.getValues()
    let values = keep ? { ...previous } : fresh
    if (keep && base && unit) {
      values = { ...values, ...retainUnitEdits(previous, base, unit) }
      if (previous.target_location_id === (base.location_id ?? '')) values.target_location_id = fresh.target_location_id
      if (previous.status === base.status && previous.disposal_kind === '') { values.status = fresh.status; values.disposal_kind = fresh.disposal_kind }
    }
    setBase(unit); setBaseItem(item)
    setSourcePath(holdingPath(locations, unit?.location_id ?? null))
    setChoice(holdingPath(locations, (mode === 'create' ? values.location_id : values.target_location_id) || null))
    form.reset(fresh)
    for (const key of Object.keys(values) as (keyof UnitEditorValues)[]) if (values[key] !== fresh[key]) form.setValue(key, values[key], { shouldDirty: true, shouldValidate: true })
    operation.dismiss(); setNoChanges(false); setLocalError(undefined)
  }
  function submit(values: UnitEditorValues) {
    if (disabled || !sourcePath || selecting && !choice) return
    setNoChanges(false); setLocalError(undefined)
    const metadata = { serial_provided: values.serial_provided, serial_number: values.serial_number, notes: values.notes }
    try {
      if (mode === 'create') operation.review({ kind: 'create-unit', owner: baseItem, body: unitCreateBody({ ...metadata, location_id: values.location_id, status: values.status }), destination: choice! })
      else if (base) {
        const context = { owner: item, subject: base, location: sourcePath }
        if (mode === 'edit') { const body = unitPatch(metadata, base); if (body) operation.review({ ...context, kind: 'patch-unit', body }); else setNoChanges(true) }
        else if (mode === 'move') {
          if (values.target_location_id === base.location_id) setNoChanges(true)
          else operation.review({ ...context, kind: 'move-unit', body: unitMoveBody(values.target_location_id), destination: choice! })
        } else if (values.status === base.status && values.note === '' && base.status !== 'disposed') setNoChanges(true)
        else operation.review({ ...context, kind: 'status-unit', body: unitStatusBody({ status: values.status, disposal_kind: values.disposal_kind, note: values.note }) })
      }
    } catch (error) { setLocalError(error) }
  }
  function field(name: 'serial_number' | 'notes' | 'note') {
    const message = name === 'serial_number' ? text.serialError : text.textError
    return <Controller key={name} name={name} control={form.control} render={({ field, fieldState }) => <Field data-invalid={fieldState.invalid}>
      <FieldLabel htmlFor={`${prefix}-${name}`} className="text-base">{text.fields[name]}</FieldLabel>
      <Textarea {...field} id={`${prefix}-${name}`} rows={name === 'serial_number' ? 2 : 4} maxLength={20000} disabled={locked || name === 'serial_number' && !provided} autoComplete="off" spellCheck={false} aria-invalid={fieldState.invalid} aria-describedby={`${prefix}-${name}-error`} className="min-h-20 text-base md:text-base" />
      <FieldDescription className="text-base">{name === 'serial_number' ? text.serialHelp : text.textHelp}</FieldDescription>
      <div id={`${prefix}-${name}-error`}><MetadataError message={message} error={fieldState.error} /></div>
    </Field>} />
  }
  const title = mode === 'create' ? text.createUnit : mode === 'edit' ? text.editUnit : mode === 'move' ? text.moveUnit : text.statusUnit
  return <>
    {changed && <Alert className="mb-6 space-y-3 p-4"><AlertTitle>{text.changed}</AlertTitle><AlertDescription className="space-y-3 text-base leading-relaxed"><p>{text.changedHelp}</p><p>{text.ownerProof}{baseItem.revision}；{text.currentVersion}{item.revision}</p>{unit && <><p>{text.unitProof}{base?.revision}；{text.currentVersion}{unit.revision}</p><UnitFields unit={unit} /></>}
      <div className="flex flex-wrap gap-2"><Button type="button" variant="outline" disabled={!canWrite || locked || item.tracking_mode !== 'individual'} className={control} onClick={() => adopt(true)}>{text.keepEdits}</Button><Button type="button" variant="outline" disabled={!canWrite || locked || item.tracking_mode !== 'individual'} className={control} onClick={() => adopt(false)}>{text.discardEdits}</Button></div>
    </AlertDescription></Alert>}
    {(stalePath || unavailable) && <Alert className="mb-6 space-y-3 p-4"><AlertTitle>{unavailable ? text.unavailable : text.pathChanged}</AlertTitle><AlertDescription className="space-y-3 text-base"><HoldingPath path={currentSource} label={text.fields.location_id} />{selecting && <HoldingPath path={currentChoice} label={text.fields.target_location_id} />}<Button type="button" variant="outline" disabled={!canWrite || locked || unavailable} className={control} onClick={recheckPaths}>{text.recheckPaths}</Button></AlertDescription></Alert>}
    <form noValidate aria-busy={operation.phase === 'pending'} onSubmit={form.handleSubmit(submit)} className="min-w-0 space-y-6">
      <FieldSet disabled={locked}><FieldLegend className="text-xl">{title}</FieldLegend><FieldDescription className="text-base">{mode === 'create' ? text.createHelp : mode === 'edit' ? text.editHelp : mode === 'move' ? text.moveHelp : text.statusHelp}</FieldDescription><FieldGroup>
        {(mode === 'create' || mode === 'edit') && <><Controller name="serial_provided" control={form.control} render={({ field }) => <Field><FieldLabel className="flex min-h-12 cursor-pointer items-center gap-3 rounded-md border p-3 text-base"><input ref={field.ref} name={field.name} type="checkbox" checked={field.value} onChange={(event) => field.onChange(event.target.checked)} onBlur={field.onBlur} className="size-5 accent-primary" />{text.providedSerial}</FieldLabel></Field>} />{field('serial_number')}{field('notes')}</>}
        {selecting && <Controller name={mode === 'create' ? 'location_id' : 'target_location_id'} control={form.control} render={({ field, fieldState }) => <HoldingLocationPicker label={mode === 'create' ? text.fields.location_id : text.fields.target_location_id} value={field.value} inputRef={field.ref} onBlur={field.onBlur} onChange={(id) => { field.onChange(id); setChoice(holdingPath(locations, id || null)); setLocalError(undefined) }} tree={locations} allowed={allowed} path={choice} error={fieldState.error} disabled={locked} nullable={mode === 'create'} />} />}
        {(mode === 'create' || mode === 'status') && <Controller name="status" control={form.control} render={({ field, fieldState }) => <Field data-invalid={fieldState.invalid}><FieldLabel htmlFor={`${prefix}-status`} className="text-base">{text.fields.status}<span aria-hidden="true">*</span></FieldLabel><NativeSelect {...field} id={`${prefix}-status`} required aria-invalid={fieldState.invalid} aria-describedby={`${prefix}-status-error`} className="w-full [&_select]:min-h-12 [&_select]:text-base">{Object.entries(text.statuses).filter(([value]) => mode !== 'create' || !['retired', 'disposed'].includes(value)).map(([value, label]) => <NativeSelectOption key={value} value={value} disabled={base?.status === 'disposed' && value !== 'disposed'}>{label}</NativeSelectOption>)}</NativeSelect><div id={`${prefix}-status-error`}><MetadataError message={text.statusError} error={fieldState.error} /></div></Field>} />}
        {mode === 'status' && status === 'disposed' && <Controller name="disposal_kind" control={form.control} render={({ field, fieldState }) => <Field data-invalid={fieldState.invalid}><FieldLabel htmlFor={`${prefix}-kind`} className="text-base">{text.fields.disposal_kind}<span aria-hidden="true">*</span></FieldLabel><NativeSelect {...field} id={`${prefix}-kind`} required aria-invalid={fieldState.invalid} aria-describedby={`${prefix}-kind-error`} className="w-full [&_select]:min-h-12 [&_select]:text-base"><NativeSelectOption value="">{text.kindError}</NativeSelectOption>{Object.entries(text.disposalKinds).map(([value, label]) => <NativeSelectOption key={value} value={value}>{label}</NativeSelectOption>)}</NativeSelect><div id={`${prefix}-kind-error`}><MetadataError message={text.kindError} error={fieldState.error} /></div></Field>} />}
        {mode === 'status' && field('note')}
      </FieldGroup></FieldSet>
      {base?.status === 'disposed' && <p className="text-base leading-relaxed">{text.disposedHelp}</p>}
      <p className="text-sm text-muted-foreground [overflow-wrap:anywhere]">{text.owner}：{JSON.stringify((base ? item : baseItem).name)} ({baseItem.id})<br />{base ? `${text.unitProof}${base.revision}` : `${text.ownerProof}${baseItem.revision}`}</p>
      {base && <><UnitFields unit={base} /><HoldingPath path={sourcePath} label={text.fields.location_id} /></>}
      {noChanges && <p role="status">{text.noChanges}</p>}{localError !== undefined && <HoldingError error={localError} />}
      <Button type="submit" disabled={disabled} className={`${control} w-full sm:w-auto`}>{text.review}</Button>
    </form>
    <DraftLeaveGuard labels={text} when={form.formState.isDirty || !!operation.command} canLeave={operation.canLeave} onLeave={operation.allowNavigation} onBlocked={operation.suspend} onStay={operation.reopen} />
  </>
}
