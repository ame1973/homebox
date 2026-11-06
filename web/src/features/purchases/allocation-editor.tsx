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
import { purchaseInterpretation, sameRecord } from './display'
import { lineErrors, purchaseLineResolver } from './editor-validation'
import { adoptPurchaseAllocation, purchaseLineDefaults, purchaseLineProposal, type PurchaseLineValues } from './form'
import type { PurchaseAllocationContext } from './model'
import { PurchaseError } from './purchase-error'
import { PurchaseFields, PurchaseItem } from './purchase-fields'
import type { PurchaseMutation } from './use-purchase-mutation'

const text = messages.purchases
export function AllocationEditor({ data, operation, online, canWrite }: { data: PurchaseAllocationContext; operation: PurchaseMutation; online: boolean; canWrite: boolean }) {
  const prefix = useId(), [base, setBase] = useState(data), [acknowledged, setAcknowledged] = useState<string>(), [localError, setLocalError] = useState<unknown>()
  const form = useForm<PurchaseLineValues>({ resolver: purchaseLineResolver(base), defaultValues: purchaseLineDefaults(), mode: 'onBlur', reValidateMode: 'onBlur' })
  const allocation = useWatch({ control: form.control, name: 'allocation' }), selected = useWatch({ control: form.control, name: 'item_unit_id' })
  const unit = base.kind === 'individual' ? base.units.find((unit) => unit.id === selected) : undefined
  const currentUnit = data.kind === 'individual' ? data.units.find((unit) => unit.id === selected) : undefined
  const locked = operation.phase === 'pending' || operation.phase === 'uncertain'
  const changed = !sameRecord(base.purchase, data.purchase) || !sameRecord(base.item, data.item) || allocation === 'unit' && !sameRecord(unit, currentUnit)
  const interpretationChanged = purchaseInterpretation(base) !== purchaseInterpretation(data)
  function adopt(keep: boolean) {
    if (!canWrite || locked || interpretationChanged && acknowledged !== purchaseInterpretation(data)) return
    const fresh = purchaseLineDefaults(), proposal = adoptPurchaseAllocation(keep ? form.getValues() : fresh, base, data)
    setBase(proposal.source); setAcknowledged(undefined); form.reset(fresh)
    for (const key of Object.keys(proposal.values) as (keyof PurchaseLineValues)[]) if (proposal.values[key] !== fresh[key]) form.setValue(key, proposal.values[key], { shouldDirty: true, shouldValidate: true })
    operation.dismiss(); setLocalError(undefined)
  }
  function submit(values: PurchaseLineValues) {
    if (!online || !canWrite || locked || changed) return
    setLocalError(undefined)
    try { operation.review({ kind: 'create-line', ...purchaseLineProposal(values, base) }) } catch (error) { setLocalError(error) }
  }
  return <>
    {changed && <Alert className="mb-6 space-y-3 p-4"><AlertTitle>{text.changed}</AlertTitle><AlertDescription className="space-y-3 text-base"><p>{text.changedHelp}</p><p>{text.purchaseProof}{base.purchase.revision}；{text.currentVersion}{data.purchase.revision}</p><PurchaseFields purchase={data.purchase} /><PurchaseItem item={data.item} />{allocation === 'unit' && (currentUnit ? <UnitFields unit={currentUnit} /> : <p>{text.unavailableUnit}</p>)}
      {interpretationChanged && <><p>{text.interpretationChanged}</p><label className="flex min-h-12 cursor-pointer items-start gap-3 rounded-md border p-3"><input type="checkbox" checked={acknowledged === purchaseInterpretation(data)} disabled={!canWrite || locked} onChange={(event) => setAcknowledged(event.target.checked ? purchaseInterpretation(data) : undefined)} className="mt-1 size-5 shrink-0 accent-primary" /><span>{text.acknowledge}</span></label></>}
      <div className="flex flex-wrap gap-2"><Button type="button" variant="outline" disabled={!canWrite || locked || interpretationChanged && acknowledged !== purchaseInterpretation(data)} className={control} onClick={() => adopt(true)}>{text.keepEdits}</Button><Button type="button" variant="outline" disabled={!canWrite || locked || interpretationChanged && acknowledged !== purchaseInterpretation(data)} className={control} onClick={() => adopt(false)}>{text.discardEdits}</Button></div>
    </AlertDescription></Alert>}
    <form noValidate aria-busy={operation.phase === 'pending'} onSubmit={form.handleSubmit(submit)} className="min-w-0 space-y-6"><PurchaseFields purchase={base.purchase} /><PurchaseItem item={base.item} /><p className="text-sm text-muted-foreground [overflow-wrap:anywhere]">{text.purchaseProof}{base.purchase.revision}</p>
      <FieldSet disabled={locked}><FieldLegend className="text-xl">{text.allocate}</FieldLegend><FieldDescription className="text-base">{text.allocationReviewHelp}</FieldDescription><FieldGroup>
        <Controller name="allocation" control={form.control} render={({ field, fieldState }) => <Field data-invalid={fieldState.invalid}><FieldLabel htmlFor={`${prefix}-allocation`} className="text-base">{text.fields.allocation}<span aria-hidden="true">*</span></FieldLabel><NativeSelect {...field} id={`${prefix}-allocation`} required aria-invalid={fieldState.invalid} aria-describedby={`${prefix}-allocation-help ${prefix}-allocation-error`} className="w-full [&_select]:min-h-12 [&_select]:text-base"><NativeSelectOption value="">{text.chooseAllocation}</NativeSelectOption><NativeSelectOption value="none">{text.noUnit}</NativeSelectOption><NativeSelectOption value="unit" disabled={base.kind !== 'individual'}>{text.namedUnit}</NativeSelectOption></NativeSelect><FieldDescription id={`${prefix}-allocation-help`} className="text-base">{text.allocationHelp}</FieldDescription><div id={`${prefix}-allocation-error`}><MetadataError message={lineErrors.allocation} error={fieldState.error} /></div></Field>} />
        {/* Keep registration order stable so first-error focus follows the visible form. */}
        <Controller name="item_unit_id" control={form.control} render={({ field, fieldState }) => allocation === 'unit' ? <Field data-invalid={fieldState.invalid}><FieldLabel htmlFor={`${prefix}-unit`} className="text-base">{text.fields.item_unit_id}<span aria-hidden="true">*</span></FieldLabel><NativeSelect {...field} id={`${prefix}-unit`} required aria-invalid={fieldState.invalid} aria-describedby={`${prefix}-unit-error`} className="w-full [&_select]:min-h-12 [&_select]:text-base"><NativeSelectOption value="">{text.chooseUnit}</NativeSelectOption>{selected && !unit && <NativeSelectOption value={selected}>{text.unavailableUnit} ({selected})</NativeSelectOption>}{base.kind === 'individual' && base.units.map((unit) => <NativeSelectOption key={unit.id} value={unit.id}>{JSON.stringify(unit.asset_code)} ({unit.id})</NativeSelectOption>)}</NativeSelect><div id={`${prefix}-unit-error`}><MetadataError message={lineErrors.item_unit_id} error={fieldState.error} /></div></Field> : <></>} />{allocation === 'unit' && unit && <section className="min-w-0 space-y-3"><UnitFields unit={unit} /><p className="text-sm text-muted-foreground">{text.unitContext}</p></section>}
        {(['quantity_base', 'paid_minor', 'note'] as const).map((name) => <Controller key={name} name={name} control={form.control} render={({ field, fieldState }) => <Field data-invalid={fieldState.invalid}><FieldLabel htmlFor={`${prefix}-${name}`} className="text-base">{text.fields[name]}{name === 'quantity_base' ? ` (${JSON.stringify(base.item.base_unit)})` : name === 'paid_minor' ? ` (${base.purchase.currency})` : ''}{name !== 'note' && <span aria-hidden="true">*</span>}</FieldLabel>
          {name === 'note' ? <Textarea {...field} id={`${prefix}-${name}`} rows={4} maxLength={20000} autoComplete="off" aria-invalid={fieldState.invalid} aria-describedby={`${prefix}-${name}-help ${prefix}-${name}-error`} className="min-h-24 text-base md:text-base" /> : <Input {...field} id={`${prefix}-${name}`} required inputMode="numeric" maxLength={32} autoComplete="off" spellCheck={false} aria-invalid={fieldState.invalid} aria-describedby={`${prefix}-${name}-help ${prefix}-${name}-error`} className="h-auto min-h-12 py-3 text-base md:text-base" />}
          <FieldDescription id={`${prefix}-${name}-help`} className="text-base">{name === 'note' ? text.textHelp : name === 'quantity_base' ? text.quantityHelp : text.moneyHelp}</FieldDescription><div id={`${prefix}-${name}-error`}><MetadataError message={lineErrors[name]} error={fieldState.error} /></div>
        </Field>} />)}
      </FieldGroup></FieldSet>{localError !== undefined && <PurchaseError error={localError} />}<Button type="submit" disabled={!online || !canWrite || locked || changed} className={`${control} w-full sm:w-auto`}>{text.review}</Button>
    </form><DraftLeaveGuard labels={text} when={form.formState.isDirty || !!operation.command} canLeave={operation.canLeave} onLeave={operation.allowNavigation} onBlocked={operation.suspend} onStay={operation.reopen} />
  </>
}
