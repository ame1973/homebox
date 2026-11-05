import { useId, useState } from 'react'
import { Controller, useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Field, FieldError, FieldGroup, FieldLabel, FieldSet } from '@/components/ui/field'
import { Textarea } from '@/components/ui/textarea'
import { validText } from '@/lib/api/client'
import type { Location } from '@/lib/api/locations'
import { messages } from '@/lib/messages'
import { LocationLeaveGuard } from './leave-guard'
import { ParentPicker } from './parent-picker'
import { subtreeIDs, type LocationTree } from './tree'
import type { LocationMutation } from './use-location-mutation'

const text = messages.locations
const control = 'min-h-12 h-auto whitespace-normal py-3 text-base! leading-normal transition-colors'
const actionSchema = z.object({ destination: z.string(), reason: z.string(), archive_children: z.boolean() }).superRefine((value, ctx) => {
  if (!validText(value.destination, 0, 128)) ctx.addIssue({ code: 'custom', path: ['destination'], message: text.parentError })
  if (!validText(value.reason, 0, 10000)) ctx.addIssue({ code: 'custom', path: ['reason'], message: text.descriptionError })
})
type ActionValues = z.infer<typeof actionSchema>

export function LocationActions({ location, tree, operation, online, canWrite }: { location: Location; tree: LocationTree; operation: LocationMutation; online: boolean; canWrite: boolean }) {
  const prefix = useId()
  const [panel, setPanel] = useState<{ kind: 'move' | 'archive'; subject: Location }>()
  const [noChanges, setNoChanges] = useState(false)
  const form = useForm<ActionValues>({ resolver: zodResolver(actionSchema), defaultValues: { destination: '', reason: '', archive_children: false }, mode: 'onBlur' })
  const locked = operation.phase === 'pending' || operation.phase === 'uncertain'
  const disabled = !online || !canWrite || locked
  const subject = panel?.subject
  const excluded = subtreeIDs(tree, subject?.id ?? location.id)
  const activeChildren = Array.from(excluded).filter((id) => id !== subject?.id && tree.byID.get(id)?.status === 'active').length
  const stale = subject !== undefined && subject.revision !== location.revision

  function start(kind: 'move' | 'archive') {
    if (disabled) return
    setPanel({ kind, subject: { ...location } })
    setNoChanges(false)
    form.reset({ destination: location.parent_id ?? '', reason: '', archive_children: false })
  }

  function submit(values: ActionValues) {
    if (!subject || !panel || disabled || stale) return
    setNoChanges(false)
    if (panel.kind === 'move') {
      if (values.destination === (subject.parent_id ?? '')) { setNoChanges(true); return }
      if (values.destination && (!tree.canReceive.get(values.destination) || excluded.has(values.destination))) {
        form.setError('destination', { message: text.parentError }, { shouldFocus: true }); return
      }
      operation.review({ kind: 'move', subject, body: { target_parent_id: values.destination || null } })
    } else {
      if (activeChildren > 0 && !values.archive_children) { form.setError('archive_children', { message: text.children }, { shouldFocus: true }); return }
      operation.review({ kind: 'archive', subject, body: { archive_children: values.archive_children, reason: values.reason } })
    }
  }

  return <div className="min-w-0 space-y-5">
    <div className="flex flex-wrap gap-2">
      <Button type="button" variant="outline" disabled={disabled} className={control} onClick={() => start('move')}>{text.move}</Button>
      {location.status === 'active' ? <Button type="button" variant="outline" disabled={disabled} className={control} onClick={() => start('archive')}>{text.archive}</Button>
        : <Button type="button" variant="outline" disabled={disabled || location.parent_id !== null && !tree.canReceive.get(location.parent_id)} className={control} onClick={() => operation.review({ kind: 'restore', subject: location })}>{text.restore}</Button>}
      <Button type="button" variant="destructive" disabled={disabled} className={control} onClick={() => operation.review({ kind: 'delete', subject: location })}>{text.delete}</Button>
    </div>
    {location.status === 'archived' && location.parent_id !== null && !tree.canReceive.get(location.parent_id) && <p className="text-sm leading-relaxed text-muted-foreground">{text.restoreHelp}</p>}
    {panel && <section className="min-w-0 space-y-5 rounded-lg border bg-card p-5" aria-labelledby={`${prefix}-title`}>
      <h2 id={`${prefix}-title`} className="text-xl font-semibold">{panel.kind === 'move' ? text.move : text.archive}</h2>
      <p className="text-base leading-relaxed text-muted-foreground">{panel.kind === 'move' ? text.moveHelp : text.archiveHelp}</p>
      <p className="text-sm text-muted-foreground">{text.originalVersion}{panel.subject.revision}</p>
      {stale && <Alert className="p-4"><AlertTitle>{text.changed}</AlertTitle><AlertDescription className="space-y-3 text-base"><p>{text.changedHelp}</p><p>{text.currentVersion}{location.revision}</p><Button type="button" variant="outline" disabled={locked} className={control} onClick={() => setPanel({ kind: panel.kind, subject: { ...location } })}>{text.keepEdits}</Button></AlertDescription></Alert>}
      <form noValidate onSubmit={form.handleSubmit(submit)} className="space-y-5">
        <FieldSet disabled={locked}>
          <FieldGroup>
            {panel.kind === 'move' ? <Controller name="destination" control={form.control} render={({ field, fieldState }) => <Field data-invalid={fieldState.invalid}>
              <FieldLabel htmlFor={`${prefix}-destination`} className="text-base">{text.destination}</FieldLabel>
              <ParentPicker id={`${prefix}-destination`} tree={tree} value={field.value} onChange={field.onChange} inputRef={field.ref} excluded={excluded} disabled={locked} invalid={fieldState.invalid} /><FieldError errors={[fieldState.error]} />
            </Field>} /> : <>
              <Controller name="archive_children" control={form.control} render={({ field, fieldState }) => <Field data-invalid={fieldState.invalid}>
                <FieldLabel htmlFor={`${prefix}-children`} className="flex min-h-12 cursor-pointer items-center gap-3 text-base"><input id={`${prefix}-children`} ref={field.ref} name={field.name} type="checkbox" checked={field.value} onChange={(event) => field.onChange(event.target.checked)} onBlur={field.onBlur} aria-invalid={fieldState.invalid} aria-describedby={`${prefix}-children-help`} className="size-5 shrink-0 accent-primary" />{text.archiveChildren}（{tree.descendantCount.get(location.id) ?? 0}）</FieldLabel>
                <p id={`${prefix}-children-help`} className="text-sm leading-relaxed text-muted-foreground">{text.archiveChildrenHelp}</p><FieldError errors={[fieldState.error]} />
              </Field>} />
              <Controller name="reason" control={form.control} render={({ field, fieldState }) => <Field data-invalid={fieldState.invalid}><FieldLabel htmlFor={`${prefix}-reason`} className="text-base">{text.reason}</FieldLabel><Textarea {...field} id={`${prefix}-reason`} rows={3} maxLength={20000} aria-invalid={fieldState.invalid} className="min-h-24 text-base md:text-base" /><FieldError errors={[fieldState.error]} /></Field>} />
            </>}
          </FieldGroup>
        </FieldSet>
        {noChanges && <p role="status">{text.noChanges}</p>}
        <div className="flex flex-wrap gap-2"><Button type="submit" disabled={disabled || stale} className={control}>{text.review}</Button><Button type="button" variant="outline" disabled={locked} className={control} onClick={() => setPanel(undefined)}>{text.cancel}</Button></div>
      </form>
    </section>}
    <LocationLeaveGuard when={!!panel && form.formState.isDirty || locked} canLeave={operation.canLeave} onLeave={operation.allowNavigation} onBlocked={operation.suspend} onStay={operation.reopen} />
  </div>
}
