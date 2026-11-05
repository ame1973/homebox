import { useEffect, useRef } from 'react'
import { LoaderCircle } from 'lucide-react'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { messages } from '@/lib/messages'
import { commandName, type LocationCommand } from './commands'
import { isKnownKind } from './form'
import { LocationError } from './location-error'
import type { LocationTree } from './tree'
import type { LocationMutation } from './use-location-mutation'

const text = messages.locations
const control = 'min-h-12 h-auto whitespace-normal py-3 text-base! leading-normal transition-colors'
const labels = { create: text.confirmCreate, patch: text.confirmSave, move: text.confirmMove, archive: text.confirmArchive, restore: text.confirmRestore, delete: text.confirmDelete }

function parentLabel(tree: LocationTree, id: string | null | undefined) {
  if (id == null) return text.root
  const location = tree.byID.get(id)
  return location ? `${location.name} (${location.code})` : id
}

function reviewRows(command: LocationCommand, tree: LocationTree) {
  const rows: { label: string; before?: string; after: string }[] = []
  const source = command.kind === 'create' ? undefined : command.subject
  const body = command.kind === 'create' || command.kind === 'patch' ? command.body : command.kind === 'move' ? { parent_id: command.body.target_parent_id } : command.kind === 'archive' ? { status: 'archived' } : command.kind === 'restore' ? { status: 'active' } : {}
  for (const [key, label] of [['name', text.name], ['code', text.code], ['kind', text.kind], ['parent_id', text.parentLabel], ['sort_order', text.sortOrder], ['description', text.description], ['status', text.status]] as const) {
    if (!(key in body)) continue
    const value = body[key as keyof typeof body]
    if (value === undefined) continue
    const display = (value: unknown) => key === 'parent_id' ? parentLabel(tree, value as string | null) : key === 'status' ? value === 'active' ? text.active : text.archived : key === 'kind' && typeof value === 'string' && isKnownKind(value) ? text.kinds[value] : String(value ?? '') || text.emptyValue
    rows.push({ label, before: source ? display(source[key]) : undefined, after: display(value) })
  }
  return rows
}

export function LocationReviewDialog({ operation, tree, online, onReadCurrent }: { operation: LocationMutation; tree: LocationTree; online: boolean; onReadCurrent: () => Promise<unknown> }) {
  // Start long reviews at visible content, not an offscreen footer action.
  const titleRef = useRef<HTMLHeadingElement>(null)
  const errorRef = useRef<HTMLDivElement>(null)
  useEffect(() => { if (operation.error) errorRef.current?.focus() }, [operation.error])
  const command = operation.command
  if (!command) return null
  const pending = operation.phase === 'pending'
  const uncertain = operation.phase === 'uncertain'
  const retryable = !operation.error || uncertain || ['CSRF_REJECTED', 'RATE_LIMITED', 'NETWORK_ERROR', 'REQUEST_ABORTED', 'REQUEST_TIMEOUT', 'SERVICE_UNAVAILABLE'].includes(operation.error.code)
  return <AlertDialog open={operation.open} onOpenChange={(open) => { if (!open) operation.dismiss() }}>
    <AlertDialogContent onOpenAutoFocus={(event) => { event.preventDefault(); titleRef.current?.focus({ preventScroll: true }) }} onCloseAutoFocus={(event) => { event.preventDefault(); operation.restoreFocus() }} onEscapeKeyDown={(event) => { if (pending) { event.preventDefault(); operation.abort() } }}
      className="max-h-[calc(100dvh-2rem)] w-[calc(100%_-_2rem)] max-w-xl animate-none! overflow-y-auto p-6">
      <AlertDialogHeader className="place-items-start text-left"><AlertDialogTitle ref={titleRef} tabIndex={-1} className="rounded-sm text-xl [overflow-wrap:anywhere] focus-visible:outline-2 focus-visible:outline-ring">{uncertain ? text.uncertain : text.reviewTitle}</AlertDialogTitle>
        <AlertDialogDescription className="text-base leading-relaxed">{uncertain ? text.uncertainHelp : command.kind === 'move' ? text.moveHelp : command.kind === 'archive' ? text.archiveHelp : command.kind === 'restore' ? text.restoreHelp : command.kind === 'delete' ? text.deleteHelp : text.reviewHelp}</AlertDialogDescription>
      </AlertDialogHeader>
      <p className="text-lg font-semibold [overflow-wrap:anywhere]">{commandName(command)}</p>
      {'subject' in command && <p className="text-base [overflow-wrap:anywhere]">{command.subject.code}</p>}
      {'subject' in command && <p className="text-sm text-muted-foreground">{text.originalVersion}{command.subject.revision}</p>}
      {command.kind === 'create' && command.parentRevision !== undefined && <p className="text-sm text-muted-foreground">{text.parentProof}{command.parentRevision}</p>}
      <dl className="space-y-4">
        {reviewRows(command, tree).map((row) => <div key={row.label} className="space-y-1 border-b pb-3">
          <dt className="text-base font-medium">{row.label}</dt>
          {row.before !== undefined && <dd className="max-h-32 overflow-y-auto whitespace-pre-wrap text-sm text-muted-foreground [overflow-wrap:anywhere]">{text.previous}：{row.before}</dd>}
          <dd className="max-h-40 overflow-y-auto whitespace-pre-wrap text-base [overflow-wrap:anywhere]">{text.proposed}：{row.after}</dd>
        </div>)}
      </dl>
      {command.kind === 'archive' && <p className="text-base leading-relaxed">{text.archiveChildren}：{command.body.archive_children ? text.yes : text.no}<br />{text.reason}：{command.body.reason || text.emptyValue}</p>}
      {operation.error && <div ref={errorRef} tabIndex={-1} className="rounded-md focus-visible:outline-2 focus-visible:outline-ring"><LocationError error={operation.error} /></div>}
      {uncertain && <div className="space-y-2"><p className="text-sm font-medium">{text.requestKey}</p><p className="text-sm [overflow-wrap:anywhere]">{command.key}</p><Button type="button" variant="outline" disabled={!online} className={control} onClick={() => { void onReadCurrent() }}>{text.readCurrent}</Button></div>}
      <AlertDialogFooter>
        <AlertDialogCancel className={control} onClick={(event) => { event.preventDefault(); if (pending) operation.abort(); else operation.dismiss() }}>{pending ? text.stopWaiting : text.cancel}</AlertDialogCancel>
        <AlertDialogAction disabled={pending || !online || !retryable} variant={command.kind === 'delete' ? 'destructive' : 'default'} className={`${control} gap-2`} onClick={(event) => { event.preventDefault(); void operation.confirm() }}>
          {pending && <LoaderCircle className="animate-spin motion-reduce:animate-none" aria-hidden="true" />}{pending ? text.pending : uncertain ? text.retryOriginal : labels[command.kind]}
        </AlertDialogAction>
      </AlertDialogFooter>
      {pending && <p role="status" className="text-sm leading-relaxed text-muted-foreground">{text.stopHelp}</p>}
    </AlertDialogContent>
  </AlertDialog>
}

export function LocationMutationNotice({ operation, online, onReadCurrent }: { operation: LocationMutation; online: boolean; onReadCurrent: () => Promise<unknown> }) {
  if (operation.phase === 'uncertain') return <Alert role="alert" className="space-y-3 p-4"><AlertTitle>{text.uncertain}</AlertTitle><AlertDescription className="space-y-3 text-base leading-relaxed">
    <p>{text.uncertainHelp}</p><div className="flex flex-wrap gap-2"><Button type="button" variant="outline" className={control} onClick={operation.reopen}>{text.openOriginal}</Button><Button type="button" variant="outline" disabled={!online} className={control} onClick={() => { void onReadCurrent() }}>{text.readCurrent}</Button></div>
  </AlertDescription></Alert>
  return operation.confirmed ? <Alert role="status" className="p-4"><AlertDescription className="text-base">{text.done}</AlertDescription></Alert> : null
}
