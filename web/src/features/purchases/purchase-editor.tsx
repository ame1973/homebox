import { useId, useState } from 'react'
import { Controller, useForm } from 'react-hook-form'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Field, FieldDescription, FieldGroup, FieldLabel, FieldLegend, FieldSet } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { DraftLeaveGuard } from '@/components/draft-leave-guard'
import { mutationControl as control } from '@/components/mutation-review'
import { messages } from '@/lib/messages'
import { MetadataError } from '@/features/catalog/editor'
import { samePurchaseDetail } from './display'
import { headerErrors, purchaseHeaderResolver } from './editor-validation'
import { purchaseCreateBody, purchaseDefaults, purchasePatch, retainPurchaseEdits, type PurchaseValues } from './form'
import type { PurchaseDetail } from './model'
import { PurchaseError } from './purchase-error'
import { PurchaseFields } from './purchase-fields'
import type { PurchaseMutation } from './use-purchase-mutation'

const text = messages.purchases
const helps = { merchant_name: text.merchantHelp, purchased_at: text.instantHelp, currency: text.currencyHelp, receipt_note: text.textHelp }
export function PurchaseEditor({ data, operation, online, canWrite }: { data?: PurchaseDetail; operation: PurchaseMutation; online: boolean; canWrite: boolean }) {
  const prefix = useId(), [base, setBase] = useState(data), [noChanges, setNoChanges] = useState(false), [localError, setLocalError] = useState<unknown>()
  const form = useForm<PurchaseValues>({ resolver: purchaseHeaderResolver, defaultValues: purchaseDefaults(base?.purchase), mode: 'onBlur', reValidateMode: 'onBlur' })
  const locked = operation.phase === 'pending' || operation.phase === 'uncertain'
  const changed = !!base && !!data && !samePurchaseDetail(base, data)
  function adopt(keep: boolean) {
    if (!base || !data || !canWrite || locked) return
    const fresh = purchaseDefaults(data.purchase), values = keep ? retainPurchaseEdits(form.getValues(), base.purchase, data.purchase) : fresh
    setBase(data); form.reset(fresh)
    for (const key of Object.keys(values) as (keyof PurchaseValues)[]) if (values[key] !== fresh[key]) form.setValue(key, values[key], { shouldDirty: true, shouldValidate: true })
    operation.dismiss(); setNoChanges(false); setLocalError(undefined)
  }
  function submit(values: PurchaseValues) {
    if (!online || !canWrite || locked || changed) return
    setNoChanges(false); setLocalError(undefined)
    if (base?.lines.length && values.currency !== base.purchase.currency) { form.setError('currency', { message: text.currencyLocked }, { shouldFocus: true }); return }
    try {
      if (!base) operation.review({ kind: 'create-purchase', body: purchaseCreateBody(values) })
      else { const body = purchasePatch(values, base.purchase); if (body) operation.review({ kind: 'patch-purchase', source: base, body }); else setNoChanges(true) }
    } catch (error) { setLocalError(error) }
  }
  return <>
    {changed && <Alert className="mb-6 space-y-3 p-4"><AlertTitle>{text.changed}</AlertTitle><AlertDescription className="space-y-3 text-base"><p>{text.changedHelp}</p><p>{text.purchaseProof}{base.purchase.revision}；{text.currentVersion}{data.purchase.revision}</p><PurchaseFields purchase={data.purchase} /><p>{text.lineCount}{data.lines.length}</p><div className="flex flex-wrap gap-2"><Button type="button" variant="outline" disabled={!canWrite || locked} className={control} onClick={() => adopt(true)}>{text.keepEdits}</Button><Button type="button" variant="outline" disabled={!canWrite || locked} className={control} onClick={() => adopt(false)}>{text.discardEdits}</Button></div></AlertDescription></Alert>}
    <form noValidate aria-busy={operation.phase === 'pending'} onSubmit={form.handleSubmit(submit)} className="min-w-0 space-y-6"><FieldSet disabled={locked}><FieldLegend className="text-xl">{base ? text.edit : text.create}</FieldLegend><FieldGroup>
      {(Object.keys(helps) as (keyof PurchaseValues)[]).map((name) => <Controller key={name} name={name} control={form.control} render={({ field, fieldState }) => <Field data-invalid={fieldState.invalid}><FieldLabel htmlFor={`${prefix}-${name}`} className="text-base">{text.fields[name]}{name !== 'receipt_note' && <span aria-hidden="true">*</span>}</FieldLabel>
        {name === 'receipt_note' || name === 'merchant_name' ? <Textarea {...field} id={`${prefix}-${name}`} rows={name === 'merchant_name' ? 2 : 4} required={name === 'merchant_name'} maxLength={name === 'merchant_name' ? 600 : 20000} autoComplete="off" aria-invalid={fieldState.invalid} aria-describedby={`${prefix}-${name}-help ${prefix}-${name}-error`} className="min-h-20 text-base md:text-base" /> : <Input {...field} id={`${prefix}-${name}`} required maxLength={name === 'purchased_at' ? 70 : 6} autoComplete="off" spellCheck={false} aria-invalid={fieldState.invalid} aria-describedby={`${prefix}-${name}-help ${prefix}-${name}-error`} className="h-auto min-h-12 py-3 text-base md:text-base" />}
        <FieldDescription id={`${prefix}-${name}-help`} className="text-base">{helps[name]}{name === 'currency' && !!base?.lines.length && <span className="mt-2 block">{text.currencyLocked}</span>}</FieldDescription><div id={`${prefix}-${name}-error`}><MetadataError message={name === 'currency' && base?.lines.length ? text.currencyLocked : headerErrors[name]} error={fieldState.error} /></div>
      </Field>} />)}
    </FieldGroup></FieldSet>
      {base && <p className="text-sm text-muted-foreground [overflow-wrap:anywhere]">{text.purchaseID}：{base.purchase.id}<br />{text.purchaseProof}{base.purchase.revision}</p>}
      {noChanges && <p role="status">{text.noChanges}</p>}{localError !== undefined && <PurchaseError error={localError} />}<Button type="submit" disabled={!online || !canWrite || locked || changed} className={`${control} w-full sm:w-auto`}>{text.review}</Button>
    </form><DraftLeaveGuard labels={text} when={form.formState.isDirty || !!operation.command} canLeave={operation.canLeave} onLeave={operation.allowNavigation} onBlocked={operation.suspend} onStay={operation.reopen} />
  </>
}
