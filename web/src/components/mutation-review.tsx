import { useEffect, useRef, type ReactNode } from 'react'
import { LoaderCircle } from 'lucide-react'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { messages } from '@/lib/messages'
import type { useReviewedMutation } from '@/lib/use-reviewed-mutation'

export type ReviewOperation = Pick<ReturnType<typeof useReviewedMutation>, 'command' | 'phase' | 'open' | 'error' | 'confirmed' | 'dismiss' | 'confirm' | 'abort' | 'restoreFocus' | 'reopen'>
// Slot merges action classes after Button's cn call; keep wrapping explicit.
export const mutationControl = 'min-h-12 h-auto max-w-full whitespace-normal! py-3 text-base! leading-normal transition-colors'
const text = messages.mutations

// Feature composition owns the reviewed body/effects. This shared frame preserves
// first-Escape handling, static-content focus and original-request reconciliation.
export function MutationReviewDialog({ operation, title, help, confirmLabel, destructive = false, children, error, online, onReadCurrent }: {
  operation: ReviewOperation; title: string; help: string; confirmLabel: string; destructive?: boolean
  children: ReactNode; error: ReactNode; online: boolean; onReadCurrent: () => Promise<unknown>
}) {
  const titleRef = useRef<HTMLHeadingElement>(null)
  const errorRef = useRef<HTMLDivElement>(null)
  useEffect(() => { if (operation.error) errorRef.current?.focus() }, [operation.error])
  const command = operation.command
  if (!command) return null
  const pending = operation.phase === 'pending'
  const uncertain = operation.phase === 'uncertain'
  const retryable = !operation.error || uncertain || ['CSRF_REJECTED', 'RATE_LIMITED', 'NETWORK_ERROR', 'REQUEST_ABORTED', 'REQUEST_TIMEOUT', 'SERVICE_UNAVAILABLE'].includes(operation.error.code)
  return <AlertDialog open={operation.open} onOpenChange={(open) => { if (!open) operation.dismiss() }}>
    <AlertDialogContent onOpenAutoFocus={(event) => { event.preventDefault(); titleRef.current?.focus({ preventScroll: true }) }} onCloseAutoFocus={(event) => { event.preventDefault(); operation.restoreFocus() }} onEscapeKeyDown={(event) => event.preventDefault()}
      onKeyDownCapture={(event) => { if (event.key !== 'Escape') return; event.preventDefault(); event.stopPropagation(); if (pending) operation.abort(); else operation.dismiss() }}
      className="max-h-[calc(100dvh-2rem)] w-[calc(100%_-_2rem)] max-w-xl animate-none! overflow-y-auto p-6">
      <AlertDialogHeader className="place-items-start text-left"><AlertDialogTitle ref={titleRef} tabIndex={-1} className="rounded-sm text-xl [overflow-wrap:anywhere] focus-visible:outline-2 focus-visible:outline-ring">{uncertain ? text.uncertain : title}</AlertDialogTitle>
        <AlertDialogDescription className="text-base leading-relaxed">{uncertain ? text.uncertainHelp : help}</AlertDialogDescription>
      </AlertDialogHeader>
      {children}
      {operation.error && <div ref={errorRef} tabIndex={-1} className="rounded-md focus-visible:outline-2 focus-visible:outline-ring">{error}</div>}
      {uncertain && <div className="space-y-2"><p className="text-sm font-medium">{text.requestKey}</p><p className="text-sm [overflow-wrap:anywhere]">{command.key}</p><Button type="button" variant="outline" disabled={!online} className={mutationControl} onClick={() => { void onReadCurrent() }}>{text.readCurrent}</Button></div>}
      <AlertDialogFooter>
        <AlertDialogCancel className={mutationControl} onClick={(event) => { event.preventDefault(); if (pending) operation.abort(); else operation.dismiss() }}>{pending ? text.stopWaiting : text.cancel}</AlertDialogCancel>
        <AlertDialogAction disabled={pending || !online || !retryable} variant={destructive ? 'destructive' : 'default'} className={`${mutationControl} gap-2`} onClick={(event) => { event.preventDefault(); void operation.confirm() }}>
          {pending && <LoaderCircle className="animate-spin motion-reduce:animate-none" aria-hidden="true" />}{pending ? text.pending : uncertain ? text.retryOriginal : confirmLabel}
        </AlertDialogAction>
      </AlertDialogFooter>
      {pending && <p role="status" className="text-sm leading-relaxed text-muted-foreground">{text.stopHelp}</p>}
    </AlertDialogContent>
  </AlertDialog>
}

export function MutationNotice({ operation, online, onReadCurrent }: { operation: ReviewOperation; online: boolean; onReadCurrent: () => Promise<unknown> }) {
  if (operation.phase === 'uncertain') return <Alert role="alert" className="space-y-3 p-4"><AlertTitle>{text.uncertain}</AlertTitle><AlertDescription className="space-y-3 text-base leading-relaxed">
    <p>{text.uncertainHelp}</p><div className="flex flex-wrap gap-2"><Button type="button" variant="outline" className={mutationControl} onClick={operation.reopen}>{text.openOriginal}</Button><Button type="button" variant="outline" disabled={!online} className={mutationControl} onClick={() => { void onReadCurrent() }}>{text.readCurrent}</Button></div>
  </AlertDescription></Alert>
  return operation.confirmed ? <Alert role="status" className="p-4"><AlertDescription className="text-base">{text.done}</AlertDescription></Alert> : null
}
