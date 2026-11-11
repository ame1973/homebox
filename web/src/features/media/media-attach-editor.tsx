import { useId, useState } from 'react'
import { Controller, useForm, type FieldErrors, type Resolver } from 'react-hook-form'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel, FieldLegend, FieldSet } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { Textarea } from '@/components/ui/textarea'
import { DraftLeaveGuard } from '@/components/draft-leave-guard'
import { mutationControl as control } from '@/components/mutation-review'
import { validText } from '@/lib/api/client'
import { idSchema, parseSafeCount } from '@/lib/api/domain'
import { messages } from '@/lib/messages'
import { MetadataError } from '@/features/catalog/editor'
import { mediaScopesConsistent } from './commands'
import { mediaLinkCreateBody, mediaLinkDefaults, type MediaLinkValues } from './form'
import { MediaError } from './media-error'
import { sameMediaMembership, sameOwnerMedia, type MediaMembership, type OwnerMedia } from './model'
import type { useMediaMutation } from './use-media-mutation'

const text = messages.media
const resolver: Resolver<MediaLinkValues> = (values) => {
  const errors: FieldErrors<MediaLinkValues> = {}
  if (parseSafeCount(values.sort_order) === undefined) errors.sort_order = { type: 'validate', message: text.attachOrderError }
  if (!validText(values.description, 0, 10000)) errors.description = { type: 'validate', message: text.descriptionError }
  return Object.keys(errors).length ? { values: {}, errors } : { values, errors: {} }
}

type Sources = Readonly<{ owner: OwnerMedia; membership: MediaMembership }>

// The list renders JSON string literals so invisible control characters survive
// copying; plain text is a separate explicit mode, never an inferred fallback.
function mediaID(value: string, format: 'json' | 'text'): string | undefined {
  try {
    const result = idSchema.safeParse(format === 'json' ? JSON.parse(value) : value)
    return result.success ? result.data : undefined
  } catch { return undefined }
}

export function MediaAttachEditor({ current, membership, selectedID, onLookup, operation, online, canLookup, canWrite, loading }: {
  current: OwnerMedia | null | undefined; membership: MediaMembership | null | undefined; selectedID: string | undefined
  onLookup: (id: string) => void; operation: ReturnType<typeof useMediaMutation>; online: boolean; canLookup: boolean; canWrite: boolean; loading: boolean
}) {
  const prefix = useId(), [input, setInput] = useState(''), [format, setFormat] = useState<'json' | 'text'>('json'), [invalidID, setInvalidID] = useState(false), [base, setBase] = useState<Sources>(), [error, setError] = useState<unknown>()
  const form = useForm<MediaLinkValues>({ resolver, defaultValues: mediaLinkDefaults(), mode: 'onBlur', reValidateMode: 'onBlur' })
  const locked = operation.phase === 'pending' || operation.phase === 'uncertain', id = mediaID(input, format)
  const selected = !!selectedID && id === selectedID && membership?.media.id === selectedID
  const consistent = !!(selected && current && membership && mediaScopesConsistent(current, membership))
  if (canWrite && consistent && current && membership && (!base || base.membership.media.id !== selectedID)) setBase({ owner: current, membership })
  const sameTarget = !!base && selected && base.membership.media.id === selectedID
  const changed = !!(base && sameTarget && current && membership && (!sameOwnerMedia(base.owner, current) || !sameMediaMembership(base.membership, membership)))

  // Lookup validation changes on click or input, never during blur.
  function lookup() {
    if (!online || !canLookup || locked || loading && id === selectedID) return
    if (id === undefined) { setInvalidID(true); return }
    setInvalidID(false); setError(undefined); onLookup(id)
  }
  function adopt() {
    if (!canWrite || locked || !sameTarget || !consistent || !current || !membership) return
    setBase({ owner: current, membership }); operation.dismiss(); setError(undefined)
  }
  function submit(values: MediaLinkValues) {
    if (!online || !canWrite || locked || !sameTarget || !consistent || changed || !base) return
    try {
      setError(undefined)
      operation.review({ kind: 'attach', owner: base.owner, membership: base.membership, body: mediaLinkCreateBody(values) })
    } catch (cause) { setError(cause) }
  }

  return <>
    <form noValidate aria-busy={operation.phase === 'pending'} onSubmit={form.handleSubmit(submit)} className="min-w-0 max-w-3xl space-y-6">
      <FieldSet disabled={locked} className="min-w-0"><FieldLegend className="text-xl">{text.attach}</FieldLegend><FieldGroup className="min-w-0">
        <Field><FieldLabel htmlFor={`${prefix}-format`} className="text-base">{text.mediaIDFormat}</FieldLabel><NativeSelect id={`${prefix}-format`} value={format} aria-describedby={`${prefix}-media-help`} onChange={(event) => { setFormat(event.target.value === 'text' ? 'text' : 'json'); setInvalidID(false) }} className="w-full [&_select]:h-auto [&_select]:min-h-12 [&_select]:py-3 [&_select]:text-base">
          <NativeSelectOption value="json">{text.mediaIDJSON}</NativeSelectOption><NativeSelectOption value="text">{text.mediaIDText}</NativeSelectOption></NativeSelect></Field>
        <Field data-invalid={invalidID}><FieldLabel htmlFor={`${prefix}-media`} className="text-base">{text.mediaID}</FieldLabel>
          <Textarea id={`${prefix}-media`} value={input} onChange={(event) => { setInput(event.target.value); setInvalidID(false) }} rows={2} maxLength={2048} autoComplete="off" spellCheck={false}
            aria-invalid={invalidID} aria-describedby={`${prefix}-media-help ${prefix}-media-error`} className="min-h-16 text-base md:text-base" />
          <FieldDescription id={`${prefix}-media-help`} className="text-base">{text.mediaLookupHelp}</FieldDescription><div id={`${prefix}-media-error`}><FieldError errors={invalidID ? [{ message: text.mediaIDError }] : []} /></div>
          <Button type="button" variant="outline" disabled={!online || !canLookup || locked || loading && id === selectedID} className={`${control} w-full sm:w-auto`} onClick={lookup}>{text.lookupMedia}</Button>
        </Field>
        {selectedID && id === selectedID && loading && <p role="status">{text.mediaLoading}</p>}
        {selected && membership && <section aria-labelledby={`${prefix}-selected`} className="min-w-0 space-y-3 border-y py-4">
          <h2 id={`${prefix}-selected`} className="text-lg font-semibold">{text.selectedMedia}</h2>
          <p className="font-medium">{membership.media.verification === 'verified' ? text.verified : text.unverified}</p><p className="text-sm leading-relaxed text-muted-foreground">{text.verificationHelp}</p>
          <dl className="grid min-w-0 gap-3 sm:grid-cols-2">{[[text.mediaID, JSON.stringify(membership.media.id)], [text.mediaVersion, String(membership.media.revision)],
            [text.mediaClaims, `${JSON.stringify(membership.media.mime_type)}；${membership.media.width} × ${membership.media.height}；${membership.media.bytes} 位元組`]]
            .map(([label, value]) => <div key={label} className="min-w-0 space-y-1"><dt className="text-sm text-muted-foreground">{label}</dt><dd className="whitespace-pre-wrap [overflow-wrap:anywhere]">{value}</dd></div>)}</dl>
          {current && <p>{text.ownerLinkCount}{current.links.length}</p>}<p>{text.membershipCount}{membership.links.length}</p>
          {consistent && current?.links.some((link) => link.media_id === membership.media.id) && <Alert className="p-4"><AlertDescription className="text-base">{text.alreadyLinked}</AlertDescription></Alert>}
          {membership.links.length > 0 && <details className="min-w-0"><summary className="min-h-12 cursor-pointer rounded-sm py-3 font-medium focus-visible:outline-2 focus-visible:outline-ring">{text.membershipList}</summary>
            <ul className="divide-y border-y">{membership.links.map((link) => <li key={link.id} className="space-y-1 py-3 [content-visibility:auto] [contain-intrinsic-size:auto_5rem]"><p className="[overflow-wrap:anywhere]">{text.ownerKinds[link.owner_type]}：{JSON.stringify(link.owner_id)}</p><p className="text-sm text-muted-foreground [overflow-wrap:anywhere]">{text.linkID}：{JSON.stringify(link.id)}{link.is_cover ? `；${text.cover}` : ''}</p></li>)}</ul>
          </details>}
        </section>}
        {selectedID && id === selectedID && membership === null && <Alert role="alert" className="p-4"><AlertTitle>{text.mediaMissing}</AlertTitle><AlertDescription className="text-base">{text.mediaMissingHelp}</AlertDescription></Alert>}
        {selected && current && !consistent && <Alert role="alert" className="p-4"><AlertTitle>{text.scopeMismatch}</AlertTitle><AlertDescription className="text-base">{text.invalidRead}</AlertDescription></Alert>}
        {changed && <Alert className="space-y-3 p-4"><AlertTitle>{text.attachChanged}</AlertTitle><AlertDescription className="space-y-3 text-base"><p>{text.attachChangedHelp}</p>
          <p>{text.ownerLinkCount}{current?.links.length}；{text.membershipCount}{membership?.links.length}</p>
          <Button type="button" variant="outline" disabled={!canWrite || locked || !consistent} className={control} onClick={adopt}>{text.adoptSources}</Button></AlertDescription></Alert>}
        <Controller name="is_cover" control={form.control} render={({ field }) => <div className="min-w-0 space-y-2"><FieldLabel htmlFor={`${prefix}-cover`} className="flex min-h-12 cursor-pointer items-center gap-3 text-base"><input id={`${prefix}-cover`} ref={field.ref} name={field.name} type="checkbox" checked={field.value} onChange={(event) => field.onChange(event.target.checked)} onBlur={field.onBlur} aria-describedby={`${prefix}-cover-help`} className="size-5 shrink-0 accent-primary" />{text.cover}</FieldLabel><FieldDescription id={`${prefix}-cover-help`} className="text-base">{text.coverHelp}</FieldDescription></div>} />
        <Controller name="sort_order" control={form.control} render={({ field, fieldState }) => <Field data-invalid={fieldState.invalid}><FieldLabel htmlFor={`${prefix}-order`} className="text-base">{text.order}</FieldLabel><Input {...field} id={`${prefix}-order`} inputMode="numeric" maxLength={32} autoComplete="off" spellCheck={false} aria-invalid={fieldState.invalid} aria-describedby={`${prefix}-order-help ${prefix}-order-error`} className="h-auto min-h-12 py-3 text-base md:text-base" /><FieldDescription id={`${prefix}-order-help`} className="text-base [overflow-wrap:anywhere]">{text.attachOrderHelp}</FieldDescription><div id={`${prefix}-order-error`}><MetadataError message={text.attachOrderError} error={fieldState.error} /></div></Field>} />
        <Controller name="description" control={form.control} render={({ field, fieldState }) => <Field data-invalid={fieldState.invalid}><FieldLabel htmlFor={`${prefix}-description`} className="text-base">{text.description}</FieldLabel><Textarea {...field} id={`${prefix}-description`} rows={4} maxLength={20000} autoComplete="off" aria-invalid={fieldState.invalid} aria-describedby={`${prefix}-description-help ${prefix}-description-error`} className="min-h-28 text-base md:text-base" /><FieldDescription id={`${prefix}-description-help`} className="text-base">{text.descriptionHelp}</FieldDescription><div id={`${prefix}-description-error`}><MetadataError message={text.descriptionError} error={fieldState.error} /></div></Field>} />
      </FieldGroup></FieldSet>
      {error !== undefined && <MediaError error={error} />}
      <Button type="submit" disabled={!online || !canWrite || locked || !sameTarget || !consistent || changed} className={`${control} w-full sm:w-auto`}>{text.reviewAttach}</Button>
    </form>
    <DraftLeaveGuard labels={text} when={input !== '' || format !== 'json' || form.formState.isDirty || !!operation.command} canLeave={operation.canLeave} onLeave={operation.allowNavigation} onBlocked={operation.suspend} onStay={operation.reopen} />
  </>
}
