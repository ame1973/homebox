import { useId, useState } from 'react'
import { Controller, useForm } from 'react-hook-form'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Field, FieldError, FieldLabel } from '@/components/ui/field'
import { Textarea } from '@/components/ui/textarea'
import { DraftLeaveGuard } from '@/components/draft-leave-guard'
import { mutationControl as control } from '@/components/mutation-review'
import type { Item } from '@/lib/api/catalog'
import { validText } from '@/lib/api/client'
import { messages } from '@/lib/messages'
import type { CatalogMutation } from './use-catalog-mutation'

const text = messages.catalog
export function CatalogActions({ item, operation, online, canWrite }: { item: Item; operation: CatalogMutation; online: boolean; canWrite: boolean }) {
  const prefix = useId()
  const [subject, setSubject] = useState<Item>()
  const form = useForm<{ reason: string }>({ defaultValues: { reason: '' }, mode: 'onBlur' })
  const locked = operation.phase === 'pending' || operation.phase === 'uncertain'
  const disabled = !online || !canWrite || locked
  const stale = subject !== undefined && subject.revision !== item.revision
  function archive(values: { reason: string }) {
    if (!subject || disabled || stale) return
    if (!validText(values.reason, 0, 10000)) { form.setError('reason', { message: text.textError }, { shouldFocus: true }); return }
    operation.review({ kind: 'archive', subject, body: { reason: values.reason } })
  }
  return <div className="min-w-0 space-y-5 border-t pt-6">
    <div className="flex flex-wrap gap-2">
      {item.status === 'active' ? <Button type="button" variant="outline" disabled={disabled} className={control} onClick={() => { setSubject({ ...item }); form.reset({ reason: '' }) }}>{text.archive}</Button>
        : <Button type="button" variant="outline" disabled={disabled} className={control} onClick={() => operation.review({ kind: 'restore', subject: item })}>{text.restore}</Button>}
      <Button type="button" variant="destructive" disabled={disabled} className={control} onClick={() => operation.review({ kind: 'delete', subject: item })}>{text.delete}</Button>
    </div>
    <p className="max-w-prose text-sm leading-relaxed text-muted-foreground">{text.deleteHelp}</p>
    {subject && <section className="min-w-0 space-y-4 rounded-lg border p-5" aria-labelledby={`${prefix}-title`}>
      <h2 id={`${prefix}-title`} className="text-xl font-semibold">{text.archive}</h2><p className="text-base leading-relaxed">{text.archiveHelp}</p>
      <p className="text-sm text-muted-foreground">{text.originalVersion}{subject.revision}</p>
      {stale && <Alert className="p-4"><AlertTitle>{text.changed}</AlertTitle><AlertDescription className="space-y-3 text-base"><p>{text.changedHelp}</p><p>{text.currentVersion}{item.revision}</p><Button type="button" variant="outline" disabled={locked} className={control} onClick={() => setSubject({ ...item })}>{text.keepEdits}</Button></AlertDescription></Alert>}
      <form noValidate onSubmit={form.handleSubmit(archive)} className="space-y-4">
        <Controller name="reason" control={form.control} render={({ field, fieldState }) => <Field data-invalid={fieldState.invalid}><FieldLabel htmlFor={`${prefix}-reason`} className="text-base">{text.reason}</FieldLabel>
          <Textarea {...field} id={`${prefix}-reason`} disabled={locked} rows={3} maxLength={20000} aria-invalid={fieldState.invalid} className="min-h-24 text-base md:text-base" /><FieldError errors={[fieldState.error]} />
        </Field>} />
        <div className="flex flex-wrap gap-2"><Button type="submit" disabled={disabled || stale} className={control}>{text.review}</Button><Button type="button" variant="outline" disabled={locked} className={control} onClick={() => setSubject(undefined)}>{text.cancel}</Button></div>
      </form>
    </section>}
    <DraftLeaveGuard labels={text} when={!!subject && form.formState.isDirty || locked} canLeave={operation.canLeave} onLeave={operation.allowNavigation} onBlocked={operation.suspend} onStay={operation.reopen} />
  </div>
}
