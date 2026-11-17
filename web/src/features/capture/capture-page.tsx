import { useCallback, useEffect, useRef, useState } from 'react'
import { Link, useLocation, useNavigate } from 'react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { LoaderCircle, WifiOff } from 'lucide-react'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { mutationControl as control } from '@/components/mutation-review'
import { fetchAdmin } from '@/lib/api/auth'
import { ApiError } from '@/lib/api/client'
import { mediaOwnerRoute, mediaOwnerScope, mediaReturnPath } from '@/lib/media-navigation'
import { messages } from '@/lib/messages'
import { subscribeSessionCleared } from '@/lib/session-lifecycle'
import { quoteExactText } from '@/lib/text-literal'
import { useOnline } from '@/lib/use-online'
import { clearSessionQueries } from '@/features/auth/use-auth-action'
import { CatalogError } from '@/features/catalog/catalog-error'
import { CaptureWorkspace } from './capture-workspace'

const text = messages.capture

export default function CapturePage() {
  const route = useLocation(), navigate = useNavigate(), client = useQueryClient(), online = useOnline()
  const scope = mediaOwnerScope(route.search), returnTo = mediaReturnPath(route.pathname + route.search)
  const valid = !!scope && !!returnTo && !route.hash
  const [blocked, setBlocked] = useState(false), [transitioning, setTransitioning] = useState(false)
  const redirecting = useRef(false)
  const auth = useQuery({ queryKey: ['auth', 'me'], enabled: online && !blocked && !transitioning, staleTime: 0, refetchOnMount: 'always', queryFn: async ({ signal }) => {
    try { return await fetchAdmin(signal) } catch (cause) { if (cause instanceof ApiError && cause.status === 401) return null; throw cause }
  } })
  const knownSession = !!auth.data && !auth.data.must_change_password && auth.isFetchedAfterMount
  const mandatory = !!auth.data?.must_change_password || auth.error instanceof ApiError && auth.error.code === 'PASSWORD_CHANGE_REQUIRED'
  const denied = mandatory || !auth.isFetching && auth.data === null
  const active = valid && knownSession && !blocked && !transitioning && (!online || !auth.isFetching && !auth.isError)
  useEffect(() => subscribeSessionCleared(() => { setBlocked(true) }), [])
  const sessionLost = useCallback((requiresPassword: boolean) => {
    if (redirecting.current) return
    redirecting.current = true; setTransitioning(true)
    clearSessionQueries(client)
    if (!requiresPassword) client.setQueryData(['auth', 'me'], null)
    navigate(requiresPassword ? '/settings/account' : '/login', { replace: true, state: { ...(requiresPassword ? {} : { notice: 'expired' }), returnTo: returnTo ?? '/items' } })
  }, [client, navigate, returnTo])
  useEffect(() => {
    if (online && !blocked && !transitioning && denied) sessionLost(mandatory)
  }, [online, blocked, transitioning, denied, mandatory, sessionLost])
  const waiting = online && !blocked && (transitioning || denied || auth.isPending || !knownSession && !auth.isError)

  return <div className="min-w-0 space-y-7 py-8 md:py-10">
    <header className="flex min-w-0 flex-wrap items-start justify-between gap-4"><div className="min-w-0 space-y-3"><h1 className="text-3xl leading-snug font-semibold md:text-4xl">{text.title}</h1><p className="max-w-prose text-base leading-relaxed text-muted-foreground">{text.intro}</p></div>
      {scope && valid && <Button asChild variant="outline" className={control}><Link to={mediaOwnerRoute(scope)}>{text.back}</Link></Button>}
    </header>
    {!valid ? <Alert role="alert" className="p-4"><AlertDescription className="text-base">{text.invalidRoute}</AlertDescription></Alert>
      : blocked ? <Alert role="alert" className="p-4"><AlertDescription className="space-y-3 text-base"><p>{text.sessionCleared}</p><Button asChild variant="outline" className={control}><Link to="/login" state={{ notice: 'expired', returnTo }}>{messages.scan.signIn}</Link></Button></AlertDescription></Alert>
        : waiting ? <p role="status" className="flex min-h-24 items-center gap-3"><LoaderCircle aria-hidden="true" className="size-6 animate-spin motion-reduce:animate-none" />{text.checking}</p>
          : <>
            {!online && <Alert role="status" className="p-4"><WifiOff aria-hidden="true" /><AlertTitle>{text.offline}</AlertTitle><AlertDescription className="text-base">{text.offlineHelp}</AlertDescription></Alert>}
            {auth.isError && online && <div className="space-y-4"><CatalogError error={auth.error} /><Button type="button" variant="outline" className={control} onClick={() => { void auth.refetch() }}>{messages.auth.checkAgain}</Button></div>}
            {knownSession ? <>
              <Alert className="p-4"><AlertTitle>{text.unsavedTitle}</AlertTitle><AlertDescription className="text-base leading-relaxed">{text.unsavedHelp}</AlertDescription></Alert>
              <section className="min-w-0 space-y-1"><p className="text-sm font-medium text-muted-foreground">{text.owner}：{messages.media.ownerKinds[scope.kind]}</p><p className="whitespace-pre-wrap text-sm [overflow-wrap:anywhere]">{quoteExactText(scope.id)}</p><p className="text-sm leading-relaxed text-muted-foreground">{text.ownerHelp}</p></section>
              <CaptureWorkspace active={active} />
            </> : !online && <p role="status">{messages.scan.cameraSession}</p>}
          </>}
  </div>
}
