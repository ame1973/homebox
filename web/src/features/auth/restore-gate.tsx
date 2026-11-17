import { useEffect, useRef } from 'react'
import { Link } from 'react-router'
import { useQuery } from '@tanstack/react-query'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { ThemeMenu } from '@/components/theme-menu'
import { messages } from '@/lib/messages'

const text = messages.scan
const control = 'min-h-12 h-auto max-w-full whitespace-normal py-3 text-base leading-normal'

export function RestoreGate({ kind = 'scan', restored, generation, returnPath, onLeave }: {
  kind?: 'scan' | 'item' | 'capture'; restored: boolean; generation: number; returnPath: string; onLeave: () => void
}) {
  const heading = useRef<HTMLHeadingElement>(null)
  const auth = useQuery({ queryKey: ['auth', 'bfcache-check', generation, returnPath], enabled: restored,
    queryFn: async ({ signal }) => {
      const [{ fetchAdmin, ApiError }, { applicationReturnPath }] = await Promise.all([import('@/lib/api/auth'), import('@/lib/settings-navigation')])
      const returnTo = applicationReturnPath(returnPath) ?? '/items'
      try {
        const admin = await fetchAdmin(signal)
        return { kind: admin.must_change_password ? 'mandatory' as const : 'active' as const, returnTo }
      } catch (error) {
        if (error instanceof ApiError && error.code === 'PASSWORD_CHANGE_REQUIRED') return { kind: 'mandatory' as const, returnTo }
        if (error instanceof ApiError && error.status === 401) return { kind: 'expired' as const, returnTo }
        throw error
      }
    },
    staleTime: Infinity, retry: false, refetchOnWindowFocus: false, refetchOnReconnect: false })
  useEffect(() => { heading.current?.focus() }, [])
  const mandatory = auth.data?.kind === 'mandatory'
  const expired = auth.data?.kind === 'expired'
  const returnTo = auth.data?.returnTo ?? '/items'
  const scan = kind === 'scan'
  const title = scan ? text.restoreTitle : kind === 'capture' ? messages.capture.restoreTitle : messages.catalog.restorePageTitle
  const help = scan ? text.restoreHelp : kind === 'capture' ? messages.capture.restoreHelp : messages.catalog.restorePageHelp
  const scanNotice = scan ? { notice: 'scanHandoffLost' } : {}
  return <main id="main" className="safe-page mx-auto min-h-dvh max-w-3xl space-y-6 py-8">
    <div className="flex justify-end"><ThemeMenu /></div>
    <h1 ref={heading} tabIndex={-1} className="rounded-sm text-3xl font-semibold focus-visible:outline-2 focus-visible:outline-ring">{title}</h1>
    <Alert className="p-4"><AlertDescription className="text-base leading-relaxed">{help}</AlertDescription></Alert>
    {!restored || auth.isPending || auth.isFetching ? <p role="status">{restored ? messages.auth.checking : text.restoreWaiting}</p>
      : mandatory ? <div className="space-y-4"><Alert role="alert" className="p-4"><AlertTitle>{messages.auth.mandatory}</AlertTitle><AlertDescription className="text-base">{messages.auth.mandatoryHelp}</AlertDescription></Alert><Button asChild className={control}><Link to="/settings/account" state={{ ...scanNotice, returnTo }} onClick={onLeave}>{text.restorePassword}</Link></Button></div>
        : expired ? <div className="space-y-4"><Alert role="alert" className="p-4"><AlertDescription className="text-base">{messages.auth.expired}</AlertDescription></Alert><Button asChild className={control}><Link to="/login" state={{ notice: scan ? 'scanHandoffLost' : 'expired', returnTo }} onClick={onLeave}>{text.signIn}</Link></Button></div>
          : auth.isError ? <div className="space-y-4"><Alert role="alert" className="p-4"><AlertDescription className="text-base">{text.restoreFailed}</AlertDescription></Alert><div className="flex flex-wrap gap-2"><Button type="button" variant="outline" className={control} onClick={() => { void auth.refetch() }}>{messages.auth.checkAgain}</Button><Button asChild variant="outline" className={control}><Link to="/login" state={{ ...scanNotice, returnTo }} onClick={onLeave}>{text.signIn}</Link></Button></div></div>
            : <div className="space-y-4"><p role="status">{text.restoreReady}</p><Button type="button" className={control} onClick={onLeave}>{text.restoreContinue}</Button></div>}
  </main>
}
