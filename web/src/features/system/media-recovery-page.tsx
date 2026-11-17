import { useCallback, useEffect, useRef, useState } from 'react'
import { Link, useLocation, useNavigate } from 'react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { LoaderCircle, RefreshCw, WifiOff } from 'lucide-react'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { mutationControl as control } from '@/components/mutation-review'
import { fetchAdmin } from '@/lib/api/auth'
import { ApiError } from '@/lib/api/client'
import { fetchMediaRecoveryStatus } from '@/lib/api/media-recovery'
import { messages } from '@/lib/messages'
import { subscribeSessionCleared } from '@/lib/session-lifecycle'
import { settingsReturnPath } from '@/lib/settings-navigation'
import { useOnline } from '@/lib/use-online'
import { clearSessionQueries } from '@/features/auth/use-auth-action'

const text = messages.mediaRecovery
const statusKey = ['system', 'media-recovery'] as const

export default function MediaRecoveryPage() {
  const route = useLocation(), navigate = useNavigate(), client = useQueryClient(), online = useOnline()
  const valid = settingsReturnPath(route.pathname + route.search) === '/settings/media-recovery' && !route.hash
  const [blocked, setBlocked] = useState(false), [transitioning, setTransitioning] = useState(false)
  const generation = useRef(0), mounted = useRef(false), redirecting = useRef(false)
  const auth = useQuery({ queryKey: ['auth', 'me'], enabled: online && valid && !blocked && !transitioning, staleTime: 0, refetchOnMount: 'always', queryFn: async ({ signal }) => {
    try { return await fetchAdmin(signal) } catch (cause) { if (cause instanceof ApiError && cause.status === 401) return null; throw cause }
  } })
  const checked = !!auth.data && !auth.data.must_change_password && auth.isFetchedAfterMount
  const status = useQuery({ queryKey: statusKey, enabled: online && valid && checked && !blocked && !transitioning,
    queryFn: ({ signal }) => fetchMediaRecoveryStatus(signal), staleTime: 0, gcTime: 0, refetchOnMount: 'always', refetchOnWindowFocus: false })
  const credentialFailure = status.error instanceof ApiError && (status.error.status === 401 || status.error.code === 'PASSWORD_CHANGE_REQUIRED') ? status.error : undefined
  const mandatory = !!auth.data?.must_change_password || auth.error instanceof ApiError && auth.error.code === 'PASSWORD_CHANGE_REQUIRED' || credentialFailure?.code === 'PASSWORD_CHANGE_REQUIRED'
  const denied = mandatory || !auth.isFetching && auth.data === null || credentialFailure !== undefined
  useEffect(() => {
    mounted.current = true
    const invalidate = () => { generation.current++ }
    const unsubscribe = subscribeSessionCleared(() => {
      invalidate()
      setBlocked(true)
      void client.cancelQueries({ queryKey: statusKey })
      client.removeQueries({ queryKey: statusKey })
    })
    return () => { mounted.current = false; invalidate(); unsubscribe() }
  }, [client])
  const sessionLost = useCallback((requiresPassword: boolean) => {
    if (redirecting.current) return
    redirecting.current = true; setTransitioning(true)
    clearSessionQueries(client)
    if (!requiresPassword) client.setQueryData(['auth', 'me'], null)
    navigate(requiresPassword ? '/settings/account' : '/login', { replace: true, state: { ...(requiresPassword ? {} : { notice: 'expired' }), returnTo: '/settings/media-recovery' } })
  }, [client, navigate])
  useEffect(() => {
    if (online && valid && !blocked && !transitioning && denied) sessionLost(mandatory)
  }, [online, valid, blocked, transitioning, denied, mandatory, sessionLost])
  async function refresh() {
    const current = generation.current
    const result = await auth.refetch()
    if (mounted.current && generation.current === current && navigator.onLine && result.data && !result.data.must_change_password && !result.isError) await status.refetch()
  }
  const fetching = auth.isFetching || status.isFetching
  const waiting = transitioning || denied || auth.isPending || !checked && !auth.isError || status.isPending || fetching
  const error = auth.isError || status.isError
  const value = status.data
  const description = value?.phase === 'unavailable' ? text.unavailableHelp : value?.phase === 'blocked' ? text.blockedHelp : value ? text.phaseHelp[value.phase] : undefined
  const failure = value?.failure && value.phase !== 'unavailable' ? text.failures[value.failure] : undefined

  return <div className="min-w-0 space-y-7 py-8 md:py-10">
    <header className="flex min-w-0 flex-wrap items-start justify-between gap-4">
      <div className="min-w-0 space-y-3"><h1 className="text-3xl leading-snug font-semibold [overflow-wrap:anywhere] md:text-4xl">{text.title}</h1><p className="max-w-prose text-base leading-relaxed text-muted-foreground">{text.intro}</p></div>
      <div className="flex max-w-full flex-wrap gap-2"><Button asChild variant="outline" className={control}><Link to="/settings">{text.back}</Link></Button>
        <Button type="button" variant="outline" className={`${control} gap-2`} disabled={!valid || !online || blocked || transitioning || fetching} onClick={() => { void refresh() }}><RefreshCw aria-hidden="true" className={`size-4 ${fetching ? 'animate-spin motion-reduce:animate-none' : ''}`} />{text.refresh}</Button></div>
    </header>
    {!valid ? <Alert role="alert" className="p-4"><AlertDescription className="text-base">{text.invalidRoute}</AlertDescription></Alert>
      : blocked ? <Alert role="alert" className="p-4"><AlertDescription className="space-y-3 text-base"><p>{text.sessionCleared}</p><Button asChild variant="outline" className={control}><Link to="/login" state={{ notice: 'expired', returnTo: '/settings/media-recovery' }}>{messages.scan.signIn}</Link></Button></AlertDescription></Alert>
        : !online ? <Alert role="status" className="p-4"><WifiOff aria-hidden="true" /><AlertTitle>{text.offline}</AlertTitle><AlertDescription className="text-base">{text.offlineHelp}</AlertDescription></Alert>
          : waiting && !error ? <p role="status" className="flex min-h-24 items-center gap-3 text-base"><LoaderCircle aria-hidden="true" className="size-6 animate-spin motion-reduce:animate-none" />{auth.isFetching || !checked || denied ? text.checking : text.loading}</p>
            : error ? <Alert role="alert" className="p-4"><AlertDescription className="text-base">{text.readError}</AlertDescription></Alert>
              : value && <div className="max-w-3xl space-y-5">
                <Card className="[--card-spacing:--spacing(6)]"><CardContent className="space-y-5 pt-6">
                  <section aria-live="polite" aria-atomic="true" className="min-w-0 space-y-3"><h2 className="text-xl font-semibold [overflow-wrap:anywhere]">{text.phases[value.phase]}</h2><p className="text-base leading-relaxed">{description}</p>{failure && <p className="text-base leading-relaxed">{failure}</p>}</section>
                  {value.phase !== 'unavailable' && <dl className="space-y-2 border-t pt-5"><dt className="text-base font-medium">{text.count}</dt><dd className="text-2xl font-semibold tabular-nums [overflow-wrap:anywhere]">{value.saturated ? `${text.atLeast} ` : ''}{new Intl.NumberFormat('zh-TW').format(value.completed)}</dd><dd className="max-w-prose text-sm leading-relaxed text-muted-foreground">{text.countHelp}</dd></dl>}
                </CardContent></Card>
                <p className="max-w-prose text-base leading-relaxed text-muted-foreground">{text.readOnlyHelp}</p>
                {status.dataUpdatedAt > 0 && <p className="text-sm text-muted-foreground">{text.readAt}：<time dateTime={new Date(status.dataUpdatedAt).toISOString()}>{new Intl.DateTimeFormat('zh-TW', { dateStyle: 'short', timeStyle: 'short' }).format(status.dataUpdatedAt)}</time></p>}
              </div>}
  </div>
}
