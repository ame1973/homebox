import { useEffect, useState } from 'react'
import { Link, Navigate, useLocation, useNavigate } from 'react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { LoaderCircle, LogOut, ShieldCheck, WifiOff } from 'lucide-react'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader } from '@/components/ui/card'
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle, AlertDialogTrigger } from '@/components/ui/alert-dialog'
import * as api from '@/lib/api/auth'
import { messages } from '@/lib/messages'
import { useOnline } from '@/lib/use-online'
import { locationReturnPath } from '@/lib/location-navigation'
import { AuthError } from './auth-error'
import { CredentialsForm } from './credentials-form'
import { clearSessionQueries, useAuthAction, type AuthNotice, type AuthOutcome } from './use-auth-action'

const text = messages.auth
const authQueryKey = ['auth', 'me'] as const
const control = 'min-h-12 h-auto whitespace-normal py-3 text-base! leading-normal'
const notices: Record<AuthNotice, string> = { setup: text.setupDone, password: text.passwordDone, logout: text.logoutDone, expired: text.expired, uncertain: text.uncertain }

export default function AccountPage({ mode }: { mode: 'login' | 'setup' | 'account' }) {
  const client = useQueryClient()
  const navigate = useNavigate()
  const location = useLocation()
  const returnTo = locationReturnPath(location.state?.returnTo)
  const online = useOnline()
  const [logoutOpen, setLogoutOpen] = useState(false)
  const [transitioning, setTransitioning] = useState(false)
  const query = useQuery({
    queryKey: authQueryKey, enabled: online, staleTime: 0, refetchOnMount: 'always',
    queryFn: async ({ signal }) => {
      try { return await api.fetchAdmin(signal) }
      catch (error) { if (error instanceof api.ApiError && error.status === 401) return null; throw error }
    },
  })

  function sessionLost(notice: AuthNotice) {
    setTransitioning(true)
    clearSessionQueries(client)
    if (notice !== 'uncertain') client.setQueryData(authQueryKey, null)
    navigate('/login', { replace: true, state: { notice, ...(returnTo ? { returnTo } : {}) } })
  }

  function succeeded(result: AuthOutcome) {
    setTransitioning(true)
    clearSessionQueries(client)
    if (result.kind === 'login') {
      client.setQueryData(authQueryKey, result.admin)
      navigate(!result.admin.must_change_password && returnTo ? returnTo : '/settings/account', { replace: true, state: returnTo ? { returnTo } : undefined })
    } else {
      client.setQueryData(authQueryKey, null)
      navigate('/login', { replace: true, state: { notice: result.kind, ...(result.kind !== 'logout' && returnTo ? { returnTo } : {}) } })
    }
  }

  const operation = useAuthAction(succeeded, sessionLost)
  useEffect(() => {
    if (query.data === null) {
      const privateQuery = (key: readonly unknown[]) => key[0] !== 'health' && key[0] !== 'auth'
      void client.cancelQueries({ predicate: (query) => privateQuery(query.queryKey) })
      client.removeQueries({ predicate: (query) => privateQuery(query.queryKey) })
    }
  }, [client, query.data])

  const rawNotice: unknown = location.state?.notice
  const notice = typeof rawNotice === 'string' && Object.hasOwn(notices, rawNotice) ? notices[rawNotice as AuthNotice] : undefined
  // A read-driven redirect must not overwrite a mutation's explicit outcome notice.
  if (!transitioning && online && !query.isFetching && !query.isError && query.data !== undefined) {
    if (query.data && mode !== 'account') return <Navigate to={!query.data.must_change_password && returnTo ? returnTo : '/settings/account'} replace state={returnTo ? { returnTo } : location.state} />
    if (!query.data && mode === 'account') return <Navigate to="/login" replace state={location.state} />
  }

  const waitingForSession = transitioning || query.isPending || query.isFetching && (query.data ? mode !== 'account' : mode === 'account')
  const title = mode === 'login' ? text.loginTitle : mode === 'setup' ? text.setupTitle : text.accountTitle
  const intro = mode === 'login' ? text.loginIntro : mode === 'setup' ? text.setupIntro : text.accountIntro
  const heading = mode === 'login' ? text.loginHeading : mode === 'setup' ? text.setupHeading : text.passwordHeading
  return (
    <div className="grid items-start gap-8 py-8 md:grid-cols-2 md:gap-12 md:py-12">
      <section aria-labelledby="account-title" className="min-w-0 space-y-5 md:pt-4">
        <h1 id="account-title" className="text-3xl leading-snug font-semibold tracking-tight [overflow-wrap:anywhere] md:text-4xl">{title}</h1>
        <p className="max-w-prose text-base leading-relaxed text-muted-foreground">{intro}</p>
        <p className="flex items-start gap-3 text-sm leading-relaxed text-muted-foreground"><ShieldCheck className="mt-0.5 size-5 shrink-0" aria-hidden="true" />{text.privacy}</p>
        <Button variant="outline" asChild className={control}><Link to="/">{text.connection}</Link></Button>
        {query.data && mode === 'account' && <div className="space-y-4 rounded-lg border p-5">
          <p className="text-sm text-muted-foreground">{text.loggedIn}</p><p className="break-words text-lg font-semibold [overflow-wrap:anywhere]">{query.data.username}</p>
          <AlertDialog open={logoutOpen} onOpenChange={setLogoutOpen}>
            <AlertDialogTrigger asChild><Button variant="outline" disabled={operation.isPending} className={`${control} w-full gap-2`}><LogOut aria-hidden="true" />{operation.pendingKind === 'logout' ? text.logoutPending : text.logout}</Button></AlertDialogTrigger>
            <AlertDialogContent className="max-h-[calc(100dvh-2rem)] w-[calc(100%_-_2rem)] max-w-lg animate-none! overflow-y-auto p-6">
              <AlertDialogHeader className="place-items-start text-left"><AlertDialogTitle className="text-xl">{text.logoutTitle}</AlertDialogTitle><AlertDialogDescription className="text-base leading-relaxed">{text.logoutHelp}</AlertDialogDescription></AlertDialogHeader>
              <AlertDialogFooter><AlertDialogCancel className={control}>{text.cancel}</AlertDialogCancel><AlertDialogAction className={control} onClick={() => { void operation.run('logout', async (signal) => {
                const csrfToken = await api.fetchCSRF(signal)
                await api.logout({ csrfToken, idempotencyKey: crypto.randomUUID(), signal })
                return { kind: 'logout' }
              }) }}>{text.logout}</AlertDialogAction></AlertDialogFooter>
            </AlertDialogContent>
          </AlertDialog>
        </div>}
      </section>
      <Card className="min-w-0 [--card-spacing:--spacing(6)]">
        <CardHeader><h2 className="text-lg font-semibold">{heading}</h2></CardHeader>
        <CardContent className="space-y-6">
          {notice && <Alert role={rawNotice === 'uncertain' ? 'alert' : 'status'} className="p-4"><AlertDescription className="text-base leading-relaxed">{notice}</AlertDescription></Alert>}
          {!online ? <Alert className="p-4"><WifiOff aria-hidden="true" /><AlertTitle>{text.offline}</AlertTitle><AlertDescription className="text-base leading-relaxed">{text.offlineHelp}</AlertDescription></Alert>
            : waitingForSession ? <p role="status" className="flex min-h-24 items-center gap-3"><LoaderCircle className="size-6 animate-spin motion-reduce:animate-none" aria-hidden="true" />{text.checking}</p>
              : query.isError ? <div className="space-y-5"><AuthError error={query.error} /><Button className={`${control} w-full`} onClick={() => { void query.refetch() }}>{text.checkAgain}</Button></div>
                : <>
                  {query.data?.must_change_password && <Alert className="p-4"><AlertTitle>{text.mandatory}</AlertTitle><AlertDescription className="text-base leading-relaxed">{text.mandatoryHelp}</AlertDescription></Alert>}
                  <CredentialsForm mode={mode === 'account' ? 'password' : mode} operation={operation} username={mode === 'account' ? query.data?.username : undefined} />
                </>}
        </CardContent>
      </Card>
    </div>
  )
}
