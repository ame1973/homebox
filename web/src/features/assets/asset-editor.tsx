import { useId, useState } from 'react'
import { Controller, useForm, useWatch } from 'react-hook-form'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Field, FieldDescription, FieldGroup, FieldLabel, FieldLegend, FieldSet } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { Textarea } from '@/components/ui/textarea'
import { DraftLeaveGuard } from '@/components/draft-leave-guard'
import { mutationControl as control } from '@/components/mutation-review'
import { messages } from '@/lib/messages'
import { MetadataError } from '@/features/catalog/editor'
import { UnitFields } from '@/features/holdings/holding-fields'
import { AssetError } from './asset-error'
import { AssetDisposalFields, AssetProfileFields, AssetProofs } from './asset-fields'
import { assetInterpretation, sameAssetSource } from './display'
import { assetEditorDefaults, assetEditorProposal, assetEditorResolver, assetFieldVisible, editorFields, retainAssetEditorEdits, type AssetEditorData, type AssetEditorValues } from './editor-model'
import type { AssetMutation } from './use-asset-mutation'

const text = messages.assets
export function AssetEditor({ data, operation, online, canWrite }: { data: AssetEditorData; operation: AssetMutation; online: boolean; canWrite: boolean }) {
  const prefix = useId(), [base, setBase] = useState(data), [acknowledged, setAcknowledged] = useState<string>(), [localError, setLocalError] = useState<unknown>()
  const form = useForm<AssetEditorValues>({ resolver: assetEditorResolver(base), defaultValues: assetEditorDefaults(base), mode: 'onBlur', reValidateMode: 'onBlur' })
  const values = useWatch({ control: form.control }), locked = operation.phase === 'pending' || operation.phase === 'uncertain'
  const changed = base.kind !== data.kind || !sameAssetSource(base.source, data.source)
  const interpretationChanged = assetInterpretation(base.source) !== assetInterpretation(data.source)
  function adopt(keep: boolean) {
    if (!canWrite || locked || interpretationChanged && acknowledged !== assetInterpretation(data.source)) return
    const fresh = assetEditorDefaults(data), next = keep ? retainAssetEditorEdits(form.getValues(), base, data) : fresh
    setBase(data); setAcknowledged(undefined); form.reset(fresh)
    for (const [name, value] of Object.entries(next)) if (value !== fresh[name]) form.setValue(name, value, { shouldDirty: true, shouldValidate: true })
    operation.dismiss(); setLocalError(undefined)
  }
  function submit(values: AssetEditorValues) {
    if (!online || !canWrite || locked || changed) return
    setLocalError(undefined)
    try { operation.review(assetEditorProposal(base, values)) } catch (error) { setLocalError(error) }
  }
  function options(name: string): Readonly<Record<string, string>> | undefined {
    if (name.endsWith('_decision')) return { unknown: text.unknown, value: text.supplied }
    if (name === 'cost_basis') return text.bases
    if (name === 'usage_unit' && base.kind === 'put-profile') return text.usageUnits
    if (name === 'event_type') return text.eventTypes
    if (name === 'direction') return text.directions
    if (name === 'kind') return base.kind === 'dispose-asset' ? text.disposalKinds : text.costKinds
    if (name === 'correction_of') {
      const rows = base.kind === 'record-usage' ? base.source.usage : base.kind === 'record-cost' ? base.source.costs : []
      return Object.fromEntries(rows.map((event) => [event.id, `${'delta' in event ? `${event.delta} ${event.usage_unit}` : `${event.amount_minor} ${event.currency}`} — ${event.id}`]))
    }
  }
  function help(name: string) {
    if (name === 'note') return text.textHelp
    if (name === 'cost_basis') return text.basisHelp
    if (name === 'currency') return text.currencyHelp
    if (name === 'correction_of') return text.correctionHelp
    if (name === 'delta' || name === 'usage_unit' || name === 'event_type') return text.usageHelp
    if (name === 'retired_at' || name === 'retirement_decision') return `${text.instantHelp} ${text.retirementHelp}`
    if (name.endsWith('_at') || name === 'service_decision') return text.instantHelp
    if (name === 'kind' && base.kind === 'dispose-asset' || name === 'realized_proceeds_minor') return text.disposalHelp
    return text.moneyHelp
  }
  return <>
    {changed && <Alert className="mb-6 space-y-3 p-4"><AlertTitle>{text.changed}</AlertTitle><AlertDescription className="space-y-3 text-base"><p>{text.changedHelp}</p><AssetProofs source={data.source} /><UnitFields unit={data.source.unit} />{data.source.asset && <AssetProfileFields asset={data.source.asset} />}{(data.kind === 'retire-asset' || data.kind === 'dispose-asset') && <AssetDisposalFields disposal={data.source.disposal} />}
      {interpretationChanged && <><p>{text.interpretationChanged}</p><label className="flex min-h-12 cursor-pointer items-start gap-3 rounded-md border p-3"><input type="checkbox" checked={acknowledged === assetInterpretation(data.source)} disabled={!canWrite || locked} onChange={(event) => setAcknowledged(event.target.checked ? assetInterpretation(data.source) : undefined)} className="mt-1 size-5 shrink-0 accent-primary" /><span>{text.acknowledge}</span></label></>}
      <div className="flex flex-wrap gap-2"><Button type="button" variant="outline" disabled={!canWrite || locked || interpretationChanged && acknowledged !== assetInterpretation(data.source)} className={control} onClick={() => adopt(true)}>{text.keepEdits}</Button><Button type="button" variant="outline" disabled={!canWrite || locked || interpretationChanged && acknowledged !== assetInterpretation(data.source)} className={control} onClick={() => adopt(false)}>{text.discardEdits}</Button></div>
    </AlertDescription></Alert>}
    <form noValidate aria-busy={operation.phase === 'pending'} onSubmit={form.handleSubmit(submit)} className="min-w-0 space-y-6"><AssetProofs source={base.source} />{(base.kind === 'retire-asset' || base.kind === 'dispose-asset') && <AssetDisposalFields disposal={base.source.disposal} />}<FieldSet disabled={locked}><FieldLegend className="text-xl">{text.confirms[base.kind]}</FieldLegend><FieldDescription className="text-base">{text.reviewHelp}</FieldDescription>
      {base.kind === 'record-usage' && <Button type="button" variant="outline" className={control} onClick={() => { form.setValue('event_type', 'use', { shouldDirty: true, shouldValidate: true }); form.setValue('delta', '1', { shouldDirty: true, shouldValidate: true }) }}>{text.quickUse}</Button>}
      {(base.kind === 'record-cost' || base.kind === 'record-usage' || base.kind === 'dispose-asset' || base.kind === 'retire-asset') && <p className="max-w-prose text-base text-muted-foreground">{text.immutable}</p>}
      <FieldGroup>{editorFields[base.kind].map((name) => <Controller key={name} name={name} control={form.control} render={({ field, fieldState }) => {
        if (!assetFieldVisible(base.kind, name, values)) return <></>
        const choices = options(name), readOnly = base.kind !== 'put-profile' && (name === 'currency' || name === 'usage_unit') || base.kind === 'put-profile' && base.source.asset !== null && (name === 'retirement_decision' || name === 'retired_at')
        return <Field data-invalid={fieldState.invalid}><FieldLabel htmlFor={`${prefix}-${name}`} className="text-base">{name === 'kind' && base.kind === 'dispose-asset' ? text.dispose : text.fields[name]}{name !== 'note' && <span aria-hidden="true">*</span>}</FieldLabel>
          {choices ? <NativeSelect {...field} id={`${prefix}-${name}`} required disabled={readOnly} aria-readonly={readOnly || undefined} aria-invalid={fieldState.invalid} aria-describedby={`${prefix}-${name}-help ${prefix}-${name}-error`} className="w-full min-w-0 [&_select]:min-h-12 [&_select]:text-base"><NativeSelectOption value="">{text.choose}</NativeSelectOption>{field.value && !Object.hasOwn(choices, field.value) && <NativeSelectOption value={field.value}>{text.unavailableTarget} ({field.value})</NativeSelectOption>}{Object.entries(choices).map(([value, label]) => <NativeSelectOption key={value} value={value}>{label}</NativeSelectOption>)}</NativeSelect> : name === 'note' ? <Textarea {...field} id={`${prefix}-${name}`} rows={4} maxLength={20000} autoComplete="off" aria-invalid={fieldState.invalid} aria-describedby={`${prefix}-${name}-help ${prefix}-${name}-error`} className="min-h-24 text-base md:text-base" /> : <Input {...field} id={`${prefix}-${name}`} required readOnly={readOnly} inputMode={name.endsWith('_minor') ? 'numeric' : name === 'delta' ? 'text' : undefined} maxLength={name.endsWith('_at') ? 70 : 32} autoComplete="off" spellCheck={false} aria-invalid={fieldState.invalid} aria-describedby={`${prefix}-${name}-help ${prefix}-${name}-error`} className="h-auto min-h-12 py-3 text-base md:text-base" />}
          {name.endsWith('_at') && !readOnly && <Button type="button" variant="outline" aria-label={`${text.now}：${text.fields[name]}`} className={control} onClick={() => form.setValue(name, new Date().toISOString(), { shouldDirty: true, shouldValidate: true })}>{text.now}</Button>}
          <FieldDescription id={`${prefix}-${name}-help`} className="text-base">{help(name)}</FieldDescription><div id={`${prefix}-${name}-error`}><MetadataError message={fieldState.error?.message ?? text.choiceError} error={fieldState.error} /></div>
        </Field>
      }} />)}</FieldGroup>
    </FieldSet>{localError !== undefined && <AssetError error={localError} />}<Button type="submit" disabled={!online || !canWrite || locked || changed} className={`${control} w-full sm:w-auto`}>{text.review}</Button></form>
    <DraftLeaveGuard labels={text} when={form.formState.isDirty || !!operation.command} canLeave={operation.canLeave} onLeave={operation.allowNavigation} onBlocked={operation.suspend} onStay={operation.reopen} />
  </>
}
