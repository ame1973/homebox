import { useId, useState } from 'react'
import { Controller, useForm, useWatch } from 'react-hook-form'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Field, FieldDescription, FieldGroup, FieldLabel, FieldLegend, FieldSet } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { DraftLeaveGuard } from '@/components/draft-leave-guard'
import { mutationControl as control } from '@/components/mutation-review'
import type { Item } from '@/lib/api/catalog'
import { messages } from '@/lib/messages'
import type { LocationTree } from '@/features/locations/tree'
import { MetadataError } from '@/features/catalog/editor'
import { baseQuantity, holdingPath, sameHoldingPath } from './display'
import { stockFormResolver } from './editor-validation'
import { stockBody, stockDefaults, type StockAction, type StockValues } from './form'
import { HoldingError } from './holding-error'
import { HoldingPath, Measurement } from './holding-fields'
import { HoldingLocationPicker } from './location-picker'
import type { ItemHoldings } from './model'
import type { HoldingMutation } from './use-holding-mutation'

const text = messages.holdings
type StockData = Extract<ItemHoldings, { kind: 'stock' }>
const interpretation = (item: Item) => JSON.stringify([item.tracking_mode, item.unit_code, item.base_unit, item.scale_factor])
function defaults(action: StockAction, data: StockData, prefill?: string): StockValues {
  const values = stockDefaults(action, data.stock.find((stock) => stock.location_id === prefill))
  if (prefill !== undefined) {
    if (action === 'consume' || action === 'transfer') values.source_location_id = prefill
    else values.target_location_id = prefill
  }
  return values
}
export function StockEditor({ action, data, currentItem, locations, prefill, operation, online, canWrite }: {
  action: StockAction; data: StockData; currentItem: Item; locations: LocationTree; prefill?: string; operation: HoldingMutation; online: boolean; canWrite: boolean
}) {
  const prefix = useId(), [base, setBase] = useState(data)
  const form = useForm<StockValues>({ resolver: stockFormResolver(action, base.item.id), defaultValues: defaults(action, base, prefill), mode: 'onBlur', reValidateMode: 'onBlur' })
  const sourceID = useWatch({ control: form.control, name: 'source_location_id' }), targetID = useWatch({ control: form.control, name: 'target_location_id' })
  const [sourcePath, setSourcePath] = useState(() => holdingPath(locations, form.getValues('source_location_id') || null))
  const [targetPath, setTargetPath] = useState(() => holdingPath(locations, form.getValues('target_location_id') || null))
  const [acknowledged, setAcknowledged] = useState<string>(), [localError, setLocalError] = useState<unknown>()
  const hasSource = action === 'consume' || action === 'transfer', hasTarget = action !== 'consume'
  const sourceFresh = holdingPath(locations, sourceID || null), targetFresh = holdingPath(locations, targetID || null)
  const sourceAllowed = new Set(base.stock.filter((stock) => stock.quantity_base > 0 && locations.byID.has(stock.location_id)).map((stock) => stock.location_id))
  const targetAllowed = new Set(locations.order.filter((id) => locations.canReceive.get(id) || action === 'adjust' && base.stock.some((stock) => stock.location_id === id)))
  const locked = operation.phase === 'pending' || operation.phase === 'uncertain'
  const changed = base.item.revision !== currentItem.revision || interpretation(base.item) !== interpretation(currentItem)
  const measurementChanged = interpretation(base.item) !== interpretation(currentItem)
  const stalePath = !sameHoldingPath(sourcePath, sourceFresh) || !sameHoldingPath(targetPath, targetFresh)
  const unavailable = sourceFresh === undefined || targetFresh === undefined || hasSource && sourceID !== '' && !sourceAllowed.has(sourceID) || hasTarget && targetID !== '' && !targetAllowed.has(targetID)
  const disabled = !online || !canWrite || locked || changed || stalePath || unavailable

  function recheckPaths() {
    if (!canWrite || locked || unavailable) return
    setSourcePath(sourceFresh); setTargetPath(targetFresh); setLocalError(undefined); operation.dismiss()
  }
  function adopt(keep: boolean) {
    if (!canWrite || locked || currentItem.tracking_mode !== 'quantity' || measurementChanged && acknowledged !== interpretation(currentItem)) return
    const fresh = defaults(action, data, prefill), values = keep ? form.getValues() : fresh
    setBase(data); setAcknowledged(undefined)
    setSourcePath(holdingPath(locations, values.source_location_id || null)); setTargetPath(holdingPath(locations, values.target_location_id || null))
    form.reset(fresh)
    for (const key of Object.keys(values) as (keyof StockValues)[]) if (values[key] !== fresh[key]) form.setValue(key, values[key], { shouldDirty: true, shouldValidate: true })
    operation.dismiss(); setLocalError(undefined)
  }
  function submit(values: StockValues) {
    if (disabled || !sourcePath || !targetPath) return
    setLocalError(undefined)
    try { operation.review({ ...stockBody(action, values, base.item.id), owner: base.item, balances: base.stock, source: sourcePath, target: targetPath }) }
    catch (error) { setLocalError(error) }
  }
  function location(kind: 'source_location_id' | 'target_location_id') {
    const isSource = kind === 'source_location_id', balance = base.stock.find((stock) => stock.location_id === (isSource ? sourceID : targetID))
    return <div className="min-w-0 space-y-3"><Controller name={kind} control={form.control} render={({ field, fieldState }) => <HoldingLocationPicker label={text.fields[kind]} value={field.value} inputRef={field.ref} onBlur={field.onBlur} onChange={(id) => { field.onChange(id); if (isSource) setSourcePath(holdingPath(locations, id || null)); else setTargetPath(holdingPath(locations, id || null)); setLocalError(undefined) }} tree={locations} allowed={isSource ? sourceAllowed : targetAllowed} path={isSource ? sourcePath : targetPath} error={fieldState.error} disabled={locked} />} />
      {(isSource ? sourceID : targetID) && <p className="text-base [overflow-wrap:anywhere]">{isSource ? text.sourceBefore : text.targetBefore}：{balance ? baseQuantity(balance.quantity_base, base.item.base_unit) : text.absentBalance}{balance && <span className="block text-sm text-muted-foreground">{text.stockProof}{balance.revision}</span>}</p>}
    </div>
  }
  function noteField(name: 'note' | 'reason') {
    const message = name === 'reason' ? text.reasonError : text.textError
    return <Controller name={name} control={form.control} render={({ field, fieldState }) => <Field data-invalid={fieldState.invalid}><FieldLabel htmlFor={`${prefix}-${name}`} className="text-base">{text.fields[name]}{name === 'reason' && <span aria-hidden="true">*</span>}</FieldLabel><Textarea {...field} id={`${prefix}-${name}`} rows={3} required={name === 'reason'} maxLength={20000} autoComplete="off" aria-invalid={fieldState.invalid} aria-describedby={`${prefix}-${name}-error`} className="min-h-24 text-base md:text-base" /><div id={`${prefix}-${name}-error`}><MetadataError message={message} error={fieldState.error} /></div></Field>} />
  }
  return <>
    {changed && <Alert className="mb-6 space-y-3 p-4"><AlertTitle>{text.changed}</AlertTitle><AlertDescription className="space-y-3 text-base leading-relaxed"><p>{text.changedHelp}</p><p>{text.ownerProof}{base.item.revision}；{text.currentVersion}{currentItem.revision}</p><Measurement item={currentItem} />
      {data.item.revision === currentItem.revision && <dl className="space-y-2">{([['source_location_id', text.sourceCurrent], ['target_location_id', text.targetCurrent]] as const).filter(([key]) => (key === 'source_location_id' ? hasSource : hasTarget) && form.getValues(key)).map(([key, label]) => { const balance = data.stock.find((stock) => stock.location_id === form.getValues(key)); return <div key={key}><dt>{label}</dt><dd className="[overflow-wrap:anywhere]">{balance ? baseQuantity(balance.quantity_base, currentItem.base_unit) : text.absentBalance}</dd></div> })}</dl>}
      {measurementChanged && <><p>{text.measurementChanged}</p><label className="flex min-h-12 cursor-pointer items-start gap-3 rounded-md border p-3"><input type="checkbox" checked={acknowledged === interpretation(currentItem)} disabled={!canWrite || locked} onChange={(event) => setAcknowledged(event.target.checked ? interpretation(currentItem) : undefined)} className="mt-1 size-5 shrink-0 accent-primary" /><span>{text.acknowledgeMeasurement}</span></label></>}
      <div className="flex flex-wrap gap-2"><Button type="button" variant="outline" disabled={!canWrite || locked || measurementChanged && acknowledged !== interpretation(currentItem)} className={control} onClick={() => adopt(true)}>{text.keepEdits}</Button><Button type="button" variant="outline" disabled={!canWrite || locked || measurementChanged && acknowledged !== interpretation(currentItem)} className={control} onClick={() => adopt(false)}>{text.discardEdits}</Button></div>
    </AlertDescription></Alert>}
    {(stalePath || unavailable) && <Alert className="mb-6 space-y-3 p-4"><AlertTitle>{unavailable ? text.unavailable : text.pathChanged}</AlertTitle><AlertDescription className="space-y-3 text-base">{hasSource && <HoldingPath path={sourceFresh} label={text.fields.source_location_id} />}{hasTarget && <HoldingPath path={targetFresh} label={text.fields.target_location_id} />}<Button type="button" variant="outline" disabled={!canWrite || locked || unavailable} className={control} onClick={recheckPaths}>{text.recheckPaths}</Button></AlertDescription></Alert>}
    <form noValidate aria-busy={operation.phase === 'pending'} onSubmit={form.handleSubmit(submit)} className="min-w-0 space-y-6">
      <FieldSet disabled={locked}><FieldLegend className="text-xl">{text.actions[action]}</FieldLegend><FieldDescription className="text-base">{action === 'adjust' ? text.adjustmentHelp : text.quantityHelp}</FieldDescription><FieldGroup>
        {hasSource && location('source_location_id')}{hasTarget && location('target_location_id')}
        <Controller name="quantity_base" control={form.control} render={({ field, fieldState }) => <Field data-invalid={fieldState.invalid}><FieldLabel htmlFor={`${prefix}-quantity`} className="text-base">{text.fields.quantity_base} ({JSON.stringify(base.item.base_unit)})<span aria-hidden="true">*</span></FieldLabel><Input {...field} id={`${prefix}-quantity`} required inputMode="numeric" autoComplete="off" spellCheck={false} maxLength={16} aria-invalid={fieldState.invalid} aria-describedby={`${prefix}-quantity-help ${prefix}-quantity-error`} className="min-h-12 text-base md:text-base" /><FieldDescription id={`${prefix}-quantity-help`} className="text-base">{text.quantityHelp}</FieldDescription><div id={`${prefix}-quantity-error`}><MetadataError message={action === 'adjust' ? text.absoluteError : text.quantityError} error={fieldState.error} /></div></Field>} />
        {action === 'adjust' && noteField('reason')}{noteField('note')}
      </FieldGroup></FieldSet>
      <Measurement item={base.item} /><p className="text-sm text-muted-foreground [overflow-wrap:anywhere]">{text.owner}：{JSON.stringify(base.item.name)} ({base.item.id})<br />{text.ownerProof}{base.item.revision}</p>
      {localError !== undefined && <HoldingError error={localError} />}<Button type="submit" disabled={disabled} className={`${control} w-full sm:w-auto`}>{text.review}</Button>
    </form>
    <DraftLeaveGuard labels={text} when={form.formState.isDirty || !!operation.command} canLeave={operation.canLeave} onLeave={operation.allowNavigation} onBlocked={operation.suspend} onStay={operation.reopen} />
  </>
}
