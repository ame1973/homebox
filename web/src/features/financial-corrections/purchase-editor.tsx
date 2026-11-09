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
import { adoptPurchaseCorrection, purchaseCorrectionDefaults, purchaseCorrectionProposal, type PurchaseCorrectionValues } from '@/features/purchases/correction-form'
import type { PurchaseCorrectionSource } from '@/features/purchases/correction-model'
import { lineErrors } from '@/features/purchases/editor-validation'
import { PurchaseError } from '@/features/purchases/purchase-error'
import { PurchaseFields, PurchaseItem } from '@/features/purchases/purchase-fields'
import { OriginalCorrectionUnitFields, PurchaseCorrectionLine, PurchaseCorrectionProofs } from './purchase-correction-fields'
import { correctionInterpretation, purchaseCorrectionResolver, samePurchaseCorrectionSource } from './purchase-editor-model'
import { ReplacementItemPicker } from './replacement-item-picker'
import type { CorrectionMutation } from './use-correction-mutation'

const text = messages.purchaseCorrections, purchase = messages.purchases

export function PurchaseCorrectionEditor({ data, targetID, operation, online, canWrite, pickerEnabled, reading, onSelectTarget, onSessionLost }: {
  data: PurchaseCorrectionSource; targetID: string | null; operation: CorrectionMutation; online: boolean; canWrite: boolean; pickerEnabled: boolean; reading: boolean
  onSelectTarget: (id: string | null) => void; onSessionLost: (mandatory: boolean) => void
}) {
  const prefix = useId(), [base, setBase] = useState(data), [acknowledged, setAcknowledged] = useState<string>(), [localError, setLocalError] = useState<unknown>()
  const form = useForm<PurchaseCorrectionValues>({ resolver: purchaseCorrectionResolver(base), defaultValues: purchaseCorrectionDefaults(), mode: 'onBlur', reValidateMode: 'onBlur' })
  const action = useWatch({ control: form.control, name: 'action' }), allocation = useWatch({ control: form.control, name: 'allocation' }), selected = useWatch({ control: form.control, name: 'item_unit_id' })
  const unit = base.replacement.kind === 'individual' ? base.replacement.units.find((candidate) => candidate.id === selected) : undefined
  const locked = operation.phase === 'pending' || operation.phase === 'uncertain'
  const targetMatches = data.replacement.kind === 'none' ? targetID === null : data.replacement.item.id === targetID
  const changed = targetMatches && !samePurchaseCorrectionSource(base, data), interpretation = correctionInterpretation(data), interpretationChanged = correctionInterpretation(base) !== interpretation
  function selectTarget(id: string | null) {
    if (locked || !pickerEnabled) return
    form.setValue('replacement_item_id', id ?? '', { shouldDirty: true }); setAcknowledged(undefined); onSelectTarget(id)
  }
  function adopt(keep: boolean) {
    if (!canWrite || locked || !changed || interpretationChanged && acknowledged !== interpretation) return
    try {
      const fresh = { ...purchaseCorrectionDefaults(), replacement_item_id: data.replacement.kind === 'none' ? '' : data.replacement.item.id }
      const next = adoptPurchaseCorrection(keep ? form.getValues() : fresh, base, data)
      setBase(next.source); setAcknowledged(undefined); form.reset(fresh)
      for (const key of Object.keys(next.values) as (keyof PurchaseCorrectionValues)[]) if (next.values[key] !== fresh[key]) form.setValue(key, next.values[key], { shouldDirty: true, shouldValidate: true })
      operation.dismiss(); setLocalError(undefined)
    } catch (error) { setLocalError(error) }
  }
  function submit(values: PurchaseCorrectionValues) {
    if (!online || !canWrite || locked || changed || !targetMatches) return
    setLocalError(undefined)
    try { operation.review({ kind: 'correct-purchase', ...purchaseCorrectionProposal(values, base) }) } catch (error) { setLocalError(error) }
  }
  return <>
    {changed && <Alert className="mb-6 space-y-3 p-4"><AlertTitle>{text.changed}</AlertTitle><AlertDescription className="space-y-4 text-base"><p>{text.changedHelp}</p>
      <h3 className="text-lg font-semibold">{purchase.previous}</h3><PurchaseCorrectionProofs source={base} includeTarget={action === 'replace'} /><PurchaseFields purchase={base.purchase} /><PurchaseItem item={base.item} /><PurchaseCorrectionLine title={text.original} line={base.line} currency={base.purchase.currency} /><OriginalCorrectionUnitFields original={base.originalUnit} />{action === 'replace' && base.replacement.kind !== 'none' && <PurchaseItem item={base.replacement.item} />}
      <h3 className="text-lg font-semibold">{purchase.currentVersion}{data.purchase.revision}</h3><PurchaseCorrectionProofs source={data} includeTarget={action === 'replace'} /><PurchaseFields purchase={data.purchase} /><PurchaseItem item={data.item} /><PurchaseCorrectionLine title={text.original} line={data.line} currency={data.purchase.currency} /><OriginalCorrectionUnitFields original={data.originalUnit} />{action === 'replace' && data.replacement.kind !== 'none' && <PurchaseItem item={data.replacement.item} />}
      {interpretationChanged && <><p>{text.interpretationChanged}</p><label className="flex min-h-12 cursor-pointer items-start gap-3 rounded-md border p-3"><input type="checkbox" checked={acknowledged === interpretation} disabled={!canWrite || locked} onChange={(event) => setAcknowledged(event.target.checked ? interpretation : undefined)} className="mt-1 size-5 shrink-0 accent-primary" /><span>{text.acknowledge}</span></label></>}
      <div className="flex flex-wrap gap-2"><Button type="button" variant="outline" disabled={!canWrite || locked || interpretationChanged && acknowledged !== interpretation} className={control} onClick={() => adopt(true)}>{text.keepEdits}</Button><Button type="button" variant="outline" disabled={!canWrite || locked || interpretationChanged && acknowledged !== interpretation} className={control} onClick={() => adopt(false)}>{text.discardEdits}</Button></div>
    </AlertDescription></Alert>}
    {!targetMatches && <Alert role="status" className="mb-6 p-4"><AlertDescription className="text-base">{reading ? text.targetLoading : text.targetUnavailable}</AlertDescription></Alert>}
    <div className="mb-6"><ReplacementItemPicker enabled={pickerEnabled} locked={locked} selected={targetID} onSelect={selectTarget} onSessionLost={onSessionLost} /></div>
    <form noValidate aria-busy={operation.phase === 'pending'} onSubmit={form.handleSubmit(submit)} className="min-w-0 space-y-6"><PurchaseCorrectionProofs source={base} includeTarget={action === 'replace'} /><PurchaseFields purchase={base.purchase} /><PurchaseItem item={base.item} /><PurchaseCorrectionLine title={text.original} line={base.line} currency={base.purchase.currency} /><OriginalCorrectionUnitFields original={base.originalUnit} />
      <FieldSet disabled={locked}><FieldLegend className="text-xl">{text.action}</FieldLegend><FieldDescription className="text-base">{text.voidHelp}</FieldDescription><FieldGroup>
        <Controller name="action" control={form.control} render={({ field, fieldState }) => <Field data-invalid={fieldState.invalid}><FieldLabel htmlFor={`${prefix}-action`} className="text-base">{text.action}<span aria-hidden="true">*</span></FieldLabel><NativeSelect {...field} id={`${prefix}-action`} required aria-invalid={fieldState.invalid} aria-describedby={`${prefix}-action-error`} className="w-full [&_select]:min-h-12 [&_select]:text-base"><NativeSelectOption value="">{text.choose}</NativeSelectOption><NativeSelectOption value="void">{text.void}</NativeSelectOption><NativeSelectOption value="replace">{text.replace}</NativeSelectOption></NativeSelect><div id={`${prefix}-action-error`}><MetadataError message={text.actionError} error={fieldState.error} /></div></Field>} />
        <Controller name="reason" control={form.control} render={({ field, fieldState }) => <Field data-invalid={fieldState.invalid}><FieldLabel htmlFor={`${prefix}-reason`} className="text-base">{text.reason}<span aria-hidden="true">*</span></FieldLabel><Textarea {...field} id={`${prefix}-reason`} required rows={3} maxLength={20000} autoComplete="off" aria-invalid={fieldState.invalid} aria-describedby={`${prefix}-reason-help ${prefix}-reason-error`} className="min-h-24 text-base md:text-base" /><FieldDescription id={`${prefix}-reason-help`} className="text-base">{text.reasonHelp}</FieldDescription><div id={`${prefix}-reason-error`}><MetadataError message={text.reasonError} error={fieldState.error} /></div></Field>} />
        {/* Keep conditional controllers registered in visible order for first-error focus. */}
        <Controller name="replacement_item_id" control={form.control} render={({ field, fieldState }) => action === 'replace' ? <Field data-invalid={fieldState.invalid}><FieldLabel htmlFor={`${prefix}-target`} className="text-base">{text.targetID}<span aria-hidden="true">*</span></FieldLabel><Input {...field} id={`${prefix}-target`} readOnly required aria-invalid={fieldState.invalid} aria-describedby={`${prefix}-target-help ${prefix}-target-error`} className="h-auto min-h-12 py-3 text-base md:text-base" /><FieldDescription id={`${prefix}-target-help`} className="text-base">{text.targetHelp}</FieldDescription><div id={`${prefix}-target-error`}><MetadataError message={text.targetError} error={fieldState.error} /></div></Field> : <></>} />
        {action === 'replace' && base.replacement.kind !== 'none' && <><p className="text-base">{text.replaceHelp}</p><PurchaseItem item={base.replacement.item} /></>}
        <Controller name="allocation" control={form.control} render={({ field, fieldState }) => action === 'replace' && base.replacement.kind !== 'none' ? <Field data-invalid={fieldState.invalid}><FieldLabel htmlFor={`${prefix}-allocation`} className="text-base">{purchase.fields.allocation}<span aria-hidden="true">*</span></FieldLabel><NativeSelect {...field} id={`${prefix}-allocation`} required aria-invalid={fieldState.invalid} aria-describedby={`${prefix}-allocation-help ${prefix}-allocation-error`} className="w-full [&_select]:min-h-12 [&_select]:text-base"><NativeSelectOption value="">{purchase.chooseAllocation}</NativeSelectOption><NativeSelectOption value="none">{purchase.noUnit}</NativeSelectOption><NativeSelectOption value="unit" disabled={base.replacement.kind !== 'individual'}>{purchase.namedUnit}</NativeSelectOption></NativeSelect><FieldDescription id={`${prefix}-allocation-help`} className="text-base">{purchase.allocationHelp}</FieldDescription><div id={`${prefix}-allocation-error`}><MetadataError message={lineErrors.allocation} error={fieldState.error} /></div></Field> : <></>} />
        <Controller name="item_unit_id" control={form.control} render={({ field, fieldState }) => action === 'replace' && allocation === 'unit' && base.replacement.kind !== 'none' ? <Field data-invalid={fieldState.invalid}><FieldLabel htmlFor={`${prefix}-unit`} className="text-base">{purchase.fields.item_unit_id}<span aria-hidden="true">*</span></FieldLabel><NativeSelect {...field} id={`${prefix}-unit`} required aria-invalid={fieldState.invalid} aria-describedby={`${prefix}-unit-error`} className="w-full [&_select]:min-h-12 [&_select]:text-base"><NativeSelectOption value="">{purchase.chooseUnit}</NativeSelectOption>{selected && !unit && <NativeSelectOption value={selected}>{purchase.unavailableUnit} ({selected})</NativeSelectOption>}{base.replacement.kind === 'individual' && base.replacement.units.map((candidate) => <NativeSelectOption key={candidate.id} value={candidate.id}>{JSON.stringify(candidate.asset_code)} ({candidate.id})</NativeSelectOption>)}</NativeSelect><div id={`${prefix}-unit-error`}><MetadataError message={lineErrors.item_unit_id} error={fieldState.error} /></div></Field> : <></>} />
        {action === 'replace' && allocation === 'unit' && unit && <section className="min-w-0 space-y-3"><UnitFields unit={unit} /><p className="text-sm text-muted-foreground">{text.contextHelp}</p></section>}
        {(['quantity_base', 'paid_minor', 'note'] as const).map((name) => <Controller key={name} name={name} control={form.control} render={({ field, fieldState }) => action === 'replace' && base.replacement.kind !== 'none' ? <Field data-invalid={fieldState.invalid}><FieldLabel htmlFor={`${prefix}-${name}`} className="text-base">{purchase.fields[name]}{name === 'quantity_base' ? ` (${JSON.stringify(base.replacement.item.base_unit)})` : name === 'paid_minor' ? ` (${base.purchase.currency})` : ''}{name !== 'note' && <span aria-hidden="true">*</span>}</FieldLabel>
          {name === 'note' ? <Textarea {...field} id={`${prefix}-${name}`} rows={4} maxLength={20000} autoComplete="off" aria-invalid={fieldState.invalid} aria-describedby={`${prefix}-${name}-help ${prefix}-${name}-error`} className="min-h-24 text-base md:text-base" /> : <Input {...field} id={`${prefix}-${name}`} required inputMode="numeric" maxLength={32} autoComplete="off" spellCheck={false} aria-invalid={fieldState.invalid} aria-describedby={`${prefix}-${name}-help ${prefix}-${name}-error`} className="h-auto min-h-12 py-3 text-base md:text-base" />}
          <FieldDescription id={`${prefix}-${name}-help`} className="text-base">{name === 'note' ? purchase.textHelp : name === 'quantity_base' ? purchase.quantityHelp : purchase.moneyHelp}</FieldDescription><div id={`${prefix}-${name}-error`}><MetadataError message={lineErrors[name]} error={fieldState.error} /></div>
        </Field> : <></>} />)}
      </FieldGroup></FieldSet>{localError !== undefined && <PurchaseError error={localError} />}<Button type="submit" disabled={!online || !canWrite || locked || changed || !targetMatches} className={`${control} w-full sm:w-auto`}>{text.review}</Button>
    </form><DraftLeaveGuard labels={text} when={form.formState.isDirty || !!operation.command} canLeave={operation.canLeave} onLeave={operation.allowNavigation} onBlocked={operation.suspend} onStay={operation.reopen} />
  </>
}
