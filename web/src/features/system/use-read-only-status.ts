import { useCallback, useEffect, useRef, useState } from 'react'
import { useLocation, useNavigate } from 'react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { fetchAdmin } from '@/lib/api/auth'
import { ApiError } from '@/lib/api/client'
import { subscribeSessionCleared } from '@/lib/session-lifecycle'
import { settingsReturnPath } from '@/lib/settings-navigation'
import { useOnline } from '@/lib/use-online'
import { clearSessionQueries } from '@/features/auth/use-auth-action'

export function useReadOnlyStatus<T>(returnTo: string, statusKey: readonly string[], read: (signal: AbortSignal) => Promise<T>) {
  const route = useLocation(), navigate = useNavigate(), client = useQueryClient(), online = useOnline()
  const valid = settingsReturnPath(route.pathname + route.search) === returnTo && !route.hash
  const [blocked, setBlocked] = useState(false), [transitioning, setTransitioning] = useState(false)
  const generation = useRef(0), mounted = useRef(false), redirecting = useRef(false)
  const auth = useQuery({ queryKey: ['auth', 'me'], enabled: online && valid && !blocked && !transitioning, staleTime: 0, refetchOnMount: 'always', queryFn: async ({ signal }) => {
    try { return await fetchAdmin(signal) } catch (cause) { if (cause instanceof ApiError && cause.status === 401) return null; throw cause }
  } })
  const checked = !!auth.data && !auth.data.must_change_password && auth.isFetchedAfterMount
  const status = useQuery({ queryKey: statusKey, enabled: online && valid && checked && !blocked && !transitioning,
    queryFn: ({ signal }) => read(signal), staleTime: 0, gcTime: 0, refetchOnMount: 'always', refetchOnWindowFocus: false })
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
  }, [client, statusKey])
  const sessionLost = useCallback((requiresPassword: boolean) => {
    if (redirecting.current) return
    redirecting.current = true; setTransitioning(true)
    clearSessionQueries(client)
    if (!requiresPassword) client.setQueryData(['auth', 'me'], null)
    navigate(requiresPassword ? '/settings/account' : '/login', { replace: true, state: { ...(requiresPassword ? {} : { notice: 'expired' }), returnTo } })
  }, [client, navigate, returnTo])
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
  return { valid, online, blocked, transitioning, fetching, waiting, error: auth.isError || status.isError,
    checking: auth.isFetching || !checked || denied, status, refresh }
}
