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
import { AssetError } from '@/features/assets/asset-error'
import { AssetProfileFields } from '@/features/assets/asset-fields'
import { adoptDisposalCorrection, disposalCorrectionDefaults, disposalCorrectionProposal, type DisposalCorrectionValues } from '@/features/assets/correction-form'
import type { DisposalCorrectionSource } from '@/features/assets/correction-model'
import { assetInterpretation } from '@/features/assets/display'
import { MetadataError } from '@/features/catalog/editor'
import { UnitFields } from '@/features/holdings/holding-fields'
import { DisposalCorrectionProofs, DisposalDecision } from './disposal-fields'
import { disposalCorrectionResolver, sameDisposalCorrectionSource } from './disposal-editor-model'
import type { CorrectionMutation } from './use-correction-mutation'

const text = messages.disposalCorrections, asset = messages.assets

export function DisposalCorrectionEditor({ data, operation, online, canWrite }: { data: DisposalCorrectionSource; operation: CorrectionMutation; online: boolean; canWrite: boolean }) {
  const prefix = useId(), [base, setBase] = useState(data), [acknowledged, setAcknowledged] = useState<string>(), [localError, setLocalError] = useState<unknown>()
  const form = useForm<DisposalCorrectionValues>({ resolver: disposalCorrectionResolver(base), defaultValues: disposalCorrectionDefaults(base), mode: 'onBlur', reValidateMode: 'onBlur' })
  const action = useWatch({ control: form.control, name: 'action' }), locked = operation.phase === 'pending' || operation.phase === 'uncertain'
  const changed = !sameDisposalCorrectionSource(base, data), interpretation = assetInterpretation(data), interpretationChanged = assetInterpretation(base) !== interpretation
  function adopt(keep: boolean) {
    if (!canWrite || locked || interpretationChanged && acknowledged !== interpretation) return
    try {
      const fresh = disposalCorrectionDefaults(data), next = adoptDisposalCorrection(keep ? form.getValues() : fresh, base, data)
      setBase(next.source); setAcknowledged(undefined); form.reset(fresh)
      for (const name of Object.keys(next.values) as (keyof DisposalCorrectionValues)[]) if (next.values[name] !== fresh[name]) form.setValue(name, next.values[name], { shouldDirty: true, shouldValidate: true })
      operation.dismiss(); setLocalError(undefined)
    } catch (error) { setLocalError(error) }
  }
  function submit(values: DisposalCorrectionValues) {
    if (!online || !canWrite || locked || changed) return
    setLocalError(undefined)
    try { operation.review({ kind: 'correct-disposal', ...disposalCorrectionProposal(values, base) }) } catch (error) { setLocalError(error) }
  }
  return <>
    {changed && <Alert className="mb-6 space-y-3 p-4"><AlertTitle>{text.changed}</AlertTitle><AlertDescription className="space-y-3 text-base"><p>{text.changedHelp}</p>
      <h3 className="text-lg font-semibold">{text.previous}</h3><p className="whitespace-pre-wrap [overflow-wrap:anywhere]">{JSON.stringify(base.item.name)}；{JSON.stringify(base.unit.asset_code)}</p><DisposalCorrectionProofs source={base} /><AssetProfileFields asset={base.asset} /><UnitFields unit={base.unit} /><DisposalDecision title={text.before} data={base.disposal.current} />
      <h3 className="text-lg font-semibold">{text.currentVersion}{data.disposal.revision}</h3><p className="whitespace-pre-wrap [overflow-wrap:anywhere]">{JSON.stringify(data.item.name)}；{JSON.stringify(data.unit.asset_code)}</p><DisposalCorrectionProofs source={data} /><AssetProfileFields asset={data.asset} /><UnitFields unit={data.unit} /><DisposalDecision title={text.current} data={data.disposal.current} />
      {interpretationChanged && <><p>{text.interpretationChanged}</p><label className="flex min-h-12 cursor-pointer items-start gap-3 rounded-md border p-3"><input type="checkbox" checked={acknowledged === interpretation} disabled={!canWrite || locked} onChange={(event) => setAcknowledged(event.target.checked ? interpretation : undefined)} className="mt-1 size-5 shrink-0 accent-primary" /><span>{text.acknowledge}</span></label></>}
      <div className="flex flex-wrap gap-2"><Button type="button" variant="outline" disabled={!canWrite || locked || interpretationChanged && acknowledged !== interpretation} className={control} onClick={() => adopt(true)}>{text.keepEdits}</Button><Button type="button" variant="outline" disabled={!canWrite || locked || interpretationChanged && acknowledged !== interpretation} className={control} onClick={() => adopt(false)}>{text.discardEdits}</Button></div>
    </AlertDescription></Alert>}
    <form noValidate aria-busy={operation.phase === 'pending'} onSubmit={form.handleSubmit(submit)} className="min-w-0 space-y-6"><DisposalCorrectionProofs source={base} /><FieldSet disabled={locked}><FieldLegend className="text-xl">{text.action}</FieldLegend><FieldDescription className="text-base">{text.voidHelp}</FieldDescription><FieldGroup>
      <Controller name="action" control={form.control} render={({ field, fieldState }) => <Field data-invalid={fieldState.invalid}><FieldLabel htmlFor={`${prefix}-action`} className="text-base">{text.action}<span aria-hidden="true">*</span></FieldLabel><NativeSelect {...field} id={`${prefix}-action`} required aria-invalid={fieldState.invalid} aria-describedby={`${prefix}-action-error`} className="w-full [&_select]:min-h-12 [&_select]:text-base"><NativeSelectOption value="">{text.choose}</NativeSelectOption><NativeSelectOption value="void" disabled={base.disposal.current === null}>{text.void}</NativeSelectOption><NativeSelectOption value="replace">{text.replace}</NativeSelectOption></NativeSelect><div id={`${prefix}-action-error`}><MetadataError message={text.actionError} error={fieldState.error} /></div></Field>} />
      <Controller name="reason" control={form.control} render={({ field, fieldState }) => <Field data-invalid={fieldState.invalid}><FieldLabel htmlFor={`${prefix}-reason`} className="text-base">{text.reason}<span aria-hidden="true">*</span></FieldLabel><Textarea {...field} id={`${prefix}-reason`} required rows={3} maxLength={20000} autoComplete="off" aria-invalid={fieldState.invalid} aria-describedby={`${prefix}-reason-help ${prefix}-reason-error`} className="min-h-24 text-base md:text-base" /><FieldDescription id={`${prefix}-reason-help`} className="text-base">{text.reasonHelp}</FieldDescription><div id={`${prefix}-reason-error`}><MetadataError message={text.reasonError} error={fieldState.error} /></div></Field>} />
      {/* Keep controllers registered in visible order even when replacement fields are hidden. */}
      <Controller name="kind" control={form.control} render={({ field, fieldState }) => action === 'replace' ? <Field data-invalid={fieldState.invalid}><FieldLabel htmlFor={`${prefix}-kind`} className="text-base">{text.kind}<span aria-hidden="true">*</span></FieldLabel><NativeSelect {...field} id={`${prefix}-kind`} required aria-invalid={fieldState.invalid} aria-describedby={`${prefix}-kind-help ${prefix}-kind-error`} className="w-full [&_select]:min-h-12 [&_select]:text-base"><NativeSelectOption value="">{asset.choose}</NativeSelectOption>{Object.entries(asset.disposalKinds).map(([value, label]) => <NativeSelectOption key={value} value={value}>{label}</NativeSelectOption>)}</NativeSelect><FieldDescription id={`${prefix}-kind-help`} className="text-base">{text.replaceHelp}</FieldDescription><div id={`${prefix}-kind-error`}><MetadataError message={asset.choiceError} error={fieldState.error} /></div></Field> : <></>} />
      <Controller name="currency" control={form.control} render={({ field, fieldState }) => action === 'replace' ? <Field data-invalid={fieldState.invalid}><FieldLabel htmlFor={`${prefix}-currency`} className="text-base">{text.currency}</FieldLabel><Input {...field} id={`${prefix}-currency`} readOnly aria-invalid={fieldState.invalid} aria-describedby={`${prefix}-currency-help ${prefix}-currency-error`} className="h-auto min-h-12 py-3 text-base md:text-base" /><FieldDescription id={`${prefix}-currency-help`} className="text-base">{asset.currencyHelp}</FieldDescription><div id={`${prefix}-currency-error`}><MetadataError message={asset.currencyHelp} error={fieldState.error} /></div></Field> : <></>} />
      <Controller name="realized_proceeds_minor" control={form.control} render={({ field, fieldState }) => action === 'replace' ? <Field data-invalid={fieldState.invalid}><FieldLabel htmlFor={`${prefix}-proceeds`} className="text-base">{text.proceeds}<span aria-hidden="true">*</span></FieldLabel><Input {...field} id={`${prefix}-proceeds`} required inputMode="numeric" maxLength={32} autoComplete="off" spellCheck={false} aria-invalid={fieldState.invalid} aria-describedby={`${prefix}-proceeds-help ${prefix}-proceeds-error`} className="h-auto min-h-12 py-3 text-base md:text-base" /><FieldDescription id={`${prefix}-proceeds-help`} className="text-base">{asset.moneyHelp}</FieldDescription><div id={`${prefix}-proceeds-error`}><MetadataError message={`${asset.moneyError} ${asset.nonSaleError}`} error={fieldState.error} /></div></Field> : <></>} />
      <Controller name="disposed_at" control={form.control} render={({ field, fieldState }) => action === 'replace' ? <Field data-invalid={fieldState.invalid}><FieldLabel htmlFor={`${prefix}-date`} className="text-base">{text.disposedAt}<span aria-hidden="true">*</span></FieldLabel><Input {...field} id={`${prefix}-date`} required maxLength={70} autoComplete="off" spellCheck={false} aria-invalid={fieldState.invalid} aria-describedby={`${prefix}-date-help ${prefix}-date-error`} className="h-auto min-h-12 py-3 text-base md:text-base" /><FieldDescription id={`${prefix}-date-help`} className="text-base">{asset.instantHelp}</FieldDescription><div id={`${prefix}-date-error`}><MetadataError message={asset.instantError} error={fieldState.error} /></div></Field> : <></>} />
      <Controller name="note" control={form.control} render={({ field, fieldState }) => action === 'replace' ? <Field data-invalid={fieldState.invalid}><FieldLabel htmlFor={`${prefix}-note`} className="text-base">{text.note}</FieldLabel><Textarea {...field} id={`${prefix}-note`} rows={4} maxLength={20000} autoComplete="off" aria-invalid={fieldState.invalid} aria-describedby={`${prefix}-note-help ${prefix}-note-error`} className="min-h-24 text-base md:text-base" /><FieldDescription id={`${prefix}-note-help`} className="text-base">{asset.textHelp}</FieldDescription><div id={`${prefix}-note-error`}><MetadataError message={asset.textHelp} error={fieldState.error} /></div></Field> : <></>} />
    </FieldGroup></FieldSet>{localError !== undefined && <AssetError error={localError} />}<Button type="submit" disabled={!online || !canWrite || locked || changed} className={`${control} w-full sm:w-auto`}>{text.review}</Button></form>
    <DraftLeaveGuard labels={text} when={form.formState.isDirty || !!operation.command} canLeave={operation.canLeave} onLeave={operation.allowNavigation} onBlocked={operation.suspend} onStay={operation.reopen} />
  </>
}
