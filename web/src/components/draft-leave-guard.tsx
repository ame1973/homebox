import { useCallback, useEffect } from 'react'
import { useBeforeUnload, useBlocker } from 'react-router'
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog'
import { mutationControl } from '@/components/mutation-review'
import { messages } from '@/lib/messages'

export function DraftLeaveGuard({ when, canLeave, onLeave, onBlocked, onStay, labels = messages.locations }: {
  when: boolean; canLeave: () => boolean; onLeave: () => void; onBlocked: () => void; onStay: () => void
  labels?: { leaveTitle: string; leaveHelp: string; stay: string; leave: string }
}) {
  const block = useCallback(() => when && !canLeave(), [when, canLeave])
  const blocker = useBlocker(block)
  useBeforeUnload(useCallback((event) => {
    if (block()) { event.preventDefault(); event.returnValue = '' }
  }, [block]))
  useEffect(() => { if (blocker.state === 'blocked') onBlocked() }, [blocker.state, onBlocked])
  return <AlertDialog open={blocker.state === 'blocked'} onOpenChange={(open) => {
    if (!open && !canLeave() && blocker.state === 'blocked') { blocker.reset(); onStay() }
  }}>
    <AlertDialogContent className="max-h-[calc(100dvh-2rem)] w-[calc(100%_-_2rem)] max-w-lg animate-none! overflow-y-auto p-6">
      <AlertDialogHeader className="place-items-start text-left"><AlertDialogTitle className="text-xl">{labels.leaveTitle}</AlertDialogTitle><AlertDialogDescription className="text-base leading-relaxed">{labels.leaveHelp}</AlertDialogDescription></AlertDialogHeader>
      <AlertDialogFooter><AlertDialogCancel className={mutationControl}>{labels.stay}</AlertDialogCancel><AlertDialogAction className={mutationControl} onClick={() => { onLeave(); if (blocker.state === 'blocked') blocker.proceed() }}>{labels.leave}</AlertDialogAction></AlertDialogFooter>
    </AlertDialogContent>
  </AlertDialog>
}
