import { useCallback, useEffect } from 'react'
import { useBeforeUnload, useBlocker } from 'react-router'
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog'
import { messages } from '@/lib/messages'

const text = messages.locations
const control = 'min-h-12 h-auto whitespace-normal py-3 text-base! leading-normal transition-colors'

export function LocationLeaveGuard({ when, canLeave, onLeave, onBlocked, onStay }: {
  when: boolean; canLeave: () => boolean; onLeave: () => void; onBlocked: () => void; onStay: () => void
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
      <AlertDialogHeader className="place-items-start text-left"><AlertDialogTitle className="text-xl">{text.leaveTitle}</AlertDialogTitle><AlertDialogDescription className="text-base leading-relaxed">{text.leaveHelp}</AlertDialogDescription></AlertDialogHeader>
      <AlertDialogFooter><AlertDialogCancel className={control}>{text.stay}</AlertDialogCancel><AlertDialogAction className={control} onClick={() => { onLeave(); if (blocker.state === 'blocked') blocker.proceed() }}>{text.leave}</AlertDialogAction></AlertDialogFooter>
    </AlertDialogContent>
  </AlertDialog>
}
