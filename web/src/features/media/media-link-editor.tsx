import { useId, useState, type ReactNode } from 'react'
import { Controller, useForm, type FieldErrors, type Resolver } from 'react-hook-form'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Field, FieldDescription, FieldGroup, FieldLabel, FieldLegend, FieldSet } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { DraftLeaveGuard } from '@/components/draft-leave-guard'
import { mutationControl as control } from '@/components/mutation-review'
import { validText } from '@/lib/api/client'
import { parseSafeCount } from '@/lib/api/domain'
import type { MediaLinkView } from '@/lib/api/media'
import { messages } from '@/lib/messages'
import { MetadataError } from '@/features/catalog/editor'
import { mediaLinkDefaults, mediaLinkPatch, retainMediaLinkEdits, type MediaLinkValues } from './form'
import { MediaError } from './media-error'
import { sameMediaLink, sameMediaSource, type OwnerMedia } from './model'
import type { useMediaMutation } from './use-media-mutation'

const text = messages.media
function resolver(link: MediaLinkView): Resolver<MediaLinkValues> {
  return (values) => {
    const errors: FieldErrors<MediaLinkValues> = {}
    if (values.sort_order !== String(link.sort_order) && parseSafeCount(values.sort_order) === undefined) errors.sort_order = { type: 'validate', message: text.orderError }
    if (!validText(values.description, 0, 10000)) errors.description = { type: 'validate', message: text.descriptionError }
    return Object.keys(errors).length ? { values: {}, errors } : { values, errors: {} }
  }
}
function sameIdentity(first: MediaLinkView, second: MediaLinkView) {
  return first.id === second.id && first.media_id === second.media_id && first.owner_type === second.owner_type && first.owner_id === second.owner_id
}
function details(link: MediaLinkView): ReactNode {
  return <dl className="space-y-2">{[[text.cover, link.is_cover ? text.yes : text.no], [text.order, String(link.sort_order)], [text.description, JSON.stringify(link.description)]]
    .map(([label, value]) => <div key={label} className="min-w-0 [overflow-wrap:anywhere]"><dt className="font-medium">{label}</dt><dd className="whitespace-pre-wrap">{value}</dd></div>)}</dl>
}

export function MediaLinkEditor({ initial, current, linkID, operation, online, canWrite }: {
  initial: OwnerMedia; current: OwnerMedia | null | undefined; linkID: string
  operation: ReturnType<typeof useMediaMutation>; online: boolean; canWrite: boolean
}) {
  const prefix = useId(), [base, setBase] = useState(initial), [noChanges, setNoChanges] = useState(false), [error, setError] = useState<unknown>()
  const original = base.links.find((link) => link.id === linkID)!
  const latest = current?.links.find((link) => link.id === linkID)
  const same = !!current && !!latest && current.owner.kind === base.owner.kind && sameIdentity(original, latest)
  const changed = !!current && (!same || !sameMediaSource(base.owner.value, current.owner.value) || !sameMediaLink(original, latest!))
  const locked = operation.phase === 'pending' || operation.phase === 'uncertain'
  const form = useForm<MediaLinkValues>({ resolver: resolver(original), defaultValues: mediaLinkDefaults(original), mode: 'onBlur', reValidateMode: 'onBlur' })

  function adopt(keep: boolean) {
    if (!canWrite || locked || !same || !latest || !current) return
    try {
      const fresh = mediaLinkDefaults(latest), values = keep ? retainMediaLinkEdits(form.getValues(), original, latest) : fresh
      setBase(current); form.reset(fresh)
      for (const key of Object.keys(values) as (keyof MediaLinkValues)[]) if (values[key] !== fresh[key]) form.setValue(key, values[key], { shouldDirty: true, shouldValidate: true })
      operation.dismiss(); setNoChanges(false); setError(undefined)
    } catch (cause) { setError(cause) }
  }
  function submit(values: MediaLinkValues) {
    if (!online || !canWrite || locked || changed || !same) return
    try {
      setError(undefined); setNoChanges(false)
      const body = mediaLinkPatch(values, original)
      if (!body) { setNoChanges(true); return }
      operation.review({ kind: 'patch-link', source: base, linkID, body })
    } catch (cause) { setError(cause) }
  }

  return <>
    {changed && <Alert className="mb-6 space-y-3 p-4"><AlertTitle>{text.changed}</AlertTitle><AlertDescription className="space-y-3 text-base leading-relaxed"><p>{text.changedHelp}</p>
      {same && latest ? <><p>{text.originalVersion}{original.revision}；{text.currentVersion}{latest.revision}</p>{details(latest)}
        <div className="flex flex-wrap gap-2"><Button type="button" variant="outline" disabled={!canWrite || locked} className={control} onClick={() => adopt(true)}>{text.keepEdits}</Button><Button type="button" variant="outline" disabled={!canWrite || locked} className={control} onClick={() => adopt(false)}>{text.discardEdits}</Button></div></>
        : <p>{text.linkMissingHelp}</p>}
    </AlertDescription></Alert>}
    <form noValidate aria-busy={operation.phase === 'pending'} onSubmit={form.handleSubmit(submit)} className="min-w-0 max-w-3xl space-y-6">
      <FieldSet disabled={locked} className="min-w-0"><FieldLegend className="text-xl">{text.edit}</FieldLegend><FieldDescription className="text-base">{text.editHelp}</FieldDescription><FieldGroup className="min-w-0">
        <Controller name="is_cover" control={form.control} render={({ field }) => <div className="min-w-0 space-y-2"><FieldLabel htmlFor={`${prefix}-cover`} className="flex min-h-12 cursor-pointer items-center gap-3 text-base"><input id={`${prefix}-cover`} ref={field.ref} name={field.name} type="checkbox" checked={field.value} onChange={(event) => field.onChange(event.target.checked)} onBlur={field.onBlur} aria-describedby={`${prefix}-cover-help`} className="size-5 shrink-0 accent-primary" />{text.cover}</FieldLabel><FieldDescription id={`${prefix}-cover-help`} className="text-base">{text.coverHelp}</FieldDescription></div>} />
        <Controller name="sort_order" control={form.control} render={({ field, fieldState }) => <Field data-invalid={fieldState.invalid}><FieldLabel htmlFor={`${prefix}-order`} className="text-base">{text.order}</FieldLabel><Input {...field} id={`${prefix}-order`} inputMode="numeric" maxLength={32} autoComplete="off" spellCheck={false} aria-invalid={fieldState.invalid} aria-describedby={`${prefix}-order-help ${prefix}-order-error`} className="h-auto min-h-12 py-3 text-base md:text-base" /><FieldDescription id={`${prefix}-order-help`} className="text-base [overflow-wrap:anywhere]">{text.orderHelp}</FieldDescription><div id={`${prefix}-order-error`}><MetadataError message={text.orderError} error={fieldState.error} /></div></Field>} />
        <Controller name="description" control={form.control} render={({ field, fieldState }) => <Field data-invalid={fieldState.invalid}><FieldLabel htmlFor={`${prefix}-description`} className="text-base">{text.description}</FieldLabel><Textarea {...field} id={`${prefix}-description`} rows={4} maxLength={20000} autoComplete="off" aria-invalid={fieldState.invalid} aria-describedby={`${prefix}-description-help ${prefix}-description-error`} className="min-h-28 text-base md:text-base" /><FieldDescription id={`${prefix}-description-help`} className="text-base">{text.descriptionHelp}</FieldDescription><div id={`${prefix}-description-error`}><MetadataError message={text.descriptionError} error={fieldState.error} /></div></Field>} />
      </FieldGroup></FieldSet>
      <p className="text-sm text-muted-foreground [overflow-wrap:anywhere]">{text.linkID}：{JSON.stringify(original.id)}<br />{text.linkVersion}{original.revision}<br />{text.mediaID}：{JSON.stringify(original.media_id)}</p>
      {noChanges && <p role="status">{text.noChanges}</p>}{error !== undefined && <MediaError error={error} />}
      <Button type="submit" disabled={!online || !canWrite || locked || changed || !same} className={`${control} w-full sm:w-auto`}>{text.reviewEdit}</Button>
    </form>
    <DraftLeaveGuard labels={text} when={form.formState.isDirty || !!operation.command} canLeave={operation.canLeave} onLeave={operation.allowNavigation} onBlocked={operation.suspend} onStay={operation.reopen} />
  </>
}
