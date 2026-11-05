import { useCallback, useEffect, useState } from 'react'
import { Link, Navigate, useLocation, useNavigate, useParams, useSearchParams } from 'react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { LoaderCircle, Plus, RefreshCw, WifiOff } from 'lucide-react'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { ApiError } from '@/lib/api/client'
import { fetchAdmin } from '@/lib/api/auth'
import { fetchLocation } from '@/lib/api/locations'
import { locationReturnPath, locationRoute } from '@/lib/location-navigation'
import { messages } from '@/lib/messages'
import { useOnline } from '@/lib/use-online'
import { clearSessionQueries } from '@/features/auth/use-auth-action'
import { LocationActions } from './actions'
import { LocationBreadcrumbs } from './breadcrumbs'
import { LocationEditor } from './editor'
import { isKnownKind } from './form'
import { LocationError } from './location-error'
import { LocationMutationNotice, LocationReviewDialog } from './review-dialog'
import { readLocationTree } from './tree'
import { LocationTreeView } from './tree-view'
import { useLocationMutation } from './use-location-mutation'
import type { LocationCommand, LocationResult } from './commands'

const text = messages.locations
const control = 'min-h-12 h-auto whitespace-normal py-3 text-base! leading-normal transition-colors'
type Mode = 'list' | 'detail' | 'create' | 'edit'

export function LocationAlias() {
  const { id } = useParams()
  const path = id ? locationReturnPath(locationRoute(id)) : undefined
  return path ? <Navigate to={path} replace /> : <section className="space-y-4 py-10"><h1 className="text-3xl font-semibold">{text.missing}</h1><p>{text.missingHelp}</p><Link to="/locations" className="inline-flex min-h-12 items-center underline">{text.back}</Link></section>
}

export default function LocationsPage({ mode }: { mode: Mode }) {
  const route = useLocation()
  const navigate = useNavigate()
  const client = useQueryClient()
  const { id = '' } = useParams()
  const [search] = useSearchParams()
  const online = useOnline()
  const [transitioning, setTransitioning] = useState(false)
  const returnTo = locationReturnPath(route.pathname + route.search) ?? '/locations'
  const needRecord = mode === 'detail' || mode === 'edit'
  const validPath = !needRecord || locationReturnPath(locationRoute(id)) !== undefined
  const auth = useQuery({ queryKey: ['auth', 'me'], enabled: online, staleTime: 0, refetchOnMount: 'always', queryFn: async ({ signal }) => {
    try { return await fetchAdmin(signal) } catch (error) { if (error instanceof ApiError && error.status === 401) return null; throw error }
  } })
  const authorized = !!auth.data && !auth.data.must_change_password
  const treeQuery = useQuery({ queryKey: ['locations', 'tree'], enabled: online && authorized && validPath, staleTime: 0, refetchOnMount: 'always', queryFn: ({ signal }) => readLocationTree(signal) })
  const recordQuery = useQuery({ queryKey: ['locations', 'detail', id], enabled: online && authorized && needRecord && validPath, staleTime: 0, refetchOnMount: 'always', queryFn: ({ signal }) => fetchLocation(id, signal) })

  const sessionLost = useCallback((mandatory: boolean) => {
    setTransitioning(true)
    clearSessionQueries(client)
    if (!mandatory) client.setQueryData(['auth', 'me'], null)
    navigate(mandatory ? '/settings/account' : '/login', { replace: true, state: { ...(mandatory ? {} : { notice: 'expired' }), returnTo } })
  }, [client, navigate, returnTo])
  const confirmed = useCallback((command: LocationCommand, result: LocationResult) => {
    const target = command.kind === 'delete' ? '/locations' : command.kind === 'create' && 'id' in result ? locationRoute(result.id) : command.kind !== 'create' ? locationRoute(command.subject.id) : '/locations'
    navigate(target, { replace: true, state: { locationNotice: 'confirmed' } })
  }, [navigate])
  const operation = useLocationMutation(confirmed, sessionLost)
  const credentialFailure = [treeQuery.error, recordQuery.error].find((error) => error instanceof ApiError && (error.status === 401 || error.code === 'PASSWORD_CHANGE_REQUIRED'))
  const { allowNavigation } = operation
  useEffect(() => {
    if (credentialFailure instanceof ApiError) {
      allowNavigation()
      void client.cancelQueries({ queryKey: ['locations'] })
      client.removeQueries({ queryKey: ['locations'] })
    }
  }, [credentialFailure, allowNavigation, client])
  useEffect(() => {
    if (auth.data === null) {
      const privateQuery = (key: readonly unknown[]) => key[0] !== 'health' && key[0] !== 'auth'
      void client.cancelQueries({ predicate: (query) => privateQuery(query.queryKey) })
      client.removeQueries({ predicate: (query) => privateQuery(query.queryKey) })
    }
  }, [auth.data, client])

  async function readCurrent() {
    return Promise.all([auth.refetch(), treeQuery.refetch(), ...(needRecord ? [recordQuery.refetch()] : [])])
  }
  if (!transitioning && online && credentialFailure instanceof ApiError) return <Navigate to={credentialFailure.code === 'PASSWORD_CHANGE_REQUIRED' ? '/settings/account' : '/login'} replace state={{ ...(credentialFailure.code === 'PASSWORD_CHANGE_REQUIRED' ? {} : { notice: 'expired' }), returnTo }} />
  if (!transitioning && online && !auth.isFetching) {
    if (auth.data === null) return <Navigate to="/login" replace state={{ notice: 'expired', returnTo }} />
    if (auth.data?.must_change_password) return <Navigate to="/settings/account" replace state={{ returnTo }} />
  }
  const tree = treeQuery.data
  const record = recordQuery.data
  const pending = transitioning || online && (auth.isPending || !authorized && !auth.isError || authorized && (treeQuery.isPending || needRecord && recordQuery.isPending))
  const fatal = auth.isError && !auth.data ? auth.error : treeQuery.isError && !tree ? treeQuery.error : needRecord && recordQuery.isError && !record ? recordQuery.error : undefined
  const missing = !validPath || fatal instanceof ApiError && (fatal.status === 404 || fatal.code === 'VALIDATION_ERROR')
  const isReading = auth.isFetching || treeQuery.isFetching || needRecord && recordQuery.isFetching
  const inconsistentRecord = needRecord && record !== undefined && tree !== undefined && tree.byID.get(record.id)?.revision !== record.revision
  const canWrite = online && authorized && !transitioning && !isReading && !auth.isError && !treeQuery.isError && (!needRecord || !recordQuery.isError) && !inconsistentRecord
  const title = mode === 'list' ? text.title : mode === 'create' ? text.createTitle : mode === 'edit' ? text.editTitle : record?.name ?? text.detail
  const parent = mode === 'create' ? search.get('parent') ?? '' : ''
  const readError = auth.error ?? treeQuery.error ?? (needRecord ? recordQuery.error : undefined)

  return <div className="min-w-0 space-y-7 py-8 md:py-10">
    {record && tree && <LocationBreadcrumbs tree={tree} location={record} />}
    <header className="flex min-w-0 flex-wrap items-start justify-between gap-4">
      <div className="min-w-0 space-y-3"><h1 className="text-3xl leading-snug font-semibold [overflow-wrap:anywhere] md:text-4xl">{title}</h1>
        {mode === 'list' && <p className="max-w-prose text-base leading-relaxed text-muted-foreground">{text.intro}</p>}
        {record && mode !== 'list' && <p className="text-base text-muted-foreground [overflow-wrap:anywhere]">{record.code}</p>}
      </div>
      <div className="flex max-w-full flex-wrap gap-2">
        {mode === 'list' && (canWrite ? <Button asChild className={`${control} gap-2`}><Link to="/locations/new"><Plus aria-hidden="true" />{text.create}</Link></Button> : <Button disabled className={control}>{text.create}</Button>)}
        {mode !== 'list' && <Button asChild variant="outline" className={control}><Link to="/locations">{text.back}</Link></Button>}
        <Button type="button" variant="outline" disabled={!online || isReading || transitioning} className={`${control} gap-2`} onClick={() => { void readCurrent() }}><RefreshCw className={`size-4 ${isReading ? 'animate-spin motion-reduce:animate-none' : ''}`} aria-hidden="true" />{text.refresh}</Button>
      </div>
    </header>
    {!online && <Alert role="status" className="p-4"><WifiOff aria-hidden="true" /><AlertTitle>{text.offline}</AlertTitle><AlertDescription className="text-base leading-relaxed">{text.offlineHelp}</AlertDescription></Alert>}
    {route.state?.locationNotice === 'confirmed' && <Alert role="status" className="p-4"><AlertDescription className="text-base">{text.done}</AlertDescription></Alert>}
    <LocationMutationNotice operation={operation} online={online} onReadCurrent={readCurrent} />
    {missing ? <section className="space-y-4 py-8"><h2 className="text-xl font-semibold">{text.missing}</h2><p className="leading-relaxed text-muted-foreground">{text.missingHelp}</p></section>
      : pending && (!tree || needRecord && !record || transitioning) ? <p role="status" className="flex min-h-28 items-center gap-3 text-base"><LoaderCircle className="size-6 animate-spin motion-reduce:animate-none" aria-hidden="true" />{auth.isPending ? text.checking : text.loading}</p>
        : fatal ? <LocationError error={fatal} />
          : !tree ? <p role="status" className="leading-relaxed text-muted-foreground">{online ? text.loading : text.offlineHelp}</p>
            : <>
              {readError && <LocationError error={readError} />}
              {inconsistentRecord && <LocationError error={new ApiError('INVALID_RESPONSE')} />}
              {mode === 'list' ? tree.byID.size === 0 ? <section className="max-w-2xl space-y-4 rounded-lg border bg-card p-6"><h2 className="text-xl font-semibold">{text.empty}</h2><p className="text-base leading-relaxed text-muted-foreground">{text.emptyHelp}</p></section> : <LocationTreeView tree={tree} />
                : mode === 'create' || mode === 'edit' && record ? <Card className="max-w-3xl [--card-spacing:--spacing(6)]"><CardContent className="pt-6"><LocationEditor key={mode === 'create' ? 'create' : record?.id} mode={mode === 'create' ? 'create' : 'edit'} location={record} tree={tree} parentID={parent} operation={operation} online={online} canWrite={canWrite} /></CardContent></Card>
                  : record && <div className="grid min-w-0 items-start gap-7 lg:grid-cols-[minmax(0,2fr)_minmax(0,1fr)]">
                    <section className="min-w-0 space-y-6" aria-labelledby="location-detail-title">
                      <div className="flex flex-wrap items-center justify-between gap-3"><h2 id="location-detail-title" className="text-xl font-semibold">{text.detail}</h2>{canWrite ? <Button asChild className={control}><Link to={locationRoute(record.id, 'edit')}>{text.edit}</Link></Button> : <Button disabled className={control}>{text.edit}</Button>}</div>
                      <dl className="space-y-4 rounded-lg border bg-card p-5">
                        {[[text.status, record.status === 'active' ? text.active : text.archived], [text.kind, isKnownKind(record.kind) ? text.kinds[record.kind] : record.kind], [text.sortOrder, String(record.sort_order)], [text.version, String(record.revision)], [text.description, record.description || text.emptyValue]].map(([label, value]) => <div key={label} className="space-y-1"><dt className="text-sm text-muted-foreground">{label}</dt><dd className="whitespace-pre-wrap text-base leading-relaxed [overflow-wrap:anywhere]">{value}</dd></div>)}
                      </dl>
                      <LocationActions key={record.id} location={record} tree={tree} operation={operation} online={online} canWrite={canWrite} />
                    </section>
                    <section className="min-w-0 space-y-4 rounded-lg border p-5" aria-labelledby="sublocation-title"><h2 id="sublocation-title" className="text-xl font-semibold">{text.sublocations}</h2>
                      {canWrite && tree.canReceive.get(record.id) ? <Button asChild variant="outline" className={`${control} w-full`}><Link to={`/locations/new?parent=${encodeURIComponent(record.id)}`}>{text.createChild}</Link></Button> : <Button disabled variant="outline" className={`${control} w-full`}>{text.createChild}</Button>}
                      {(tree.children.get(record.id) ?? []).length === 0 ? <p className="leading-relaxed text-muted-foreground">{text.noChildren}</p> : <ul className="space-y-3">{(tree.children.get(record.id) ?? []).map((childID) => {
                        const child = tree.byID.get(childID)!
                        return <li key={childID}><Link to={locationRoute(childID)} className="block min-h-12 space-y-1 rounded-md py-2 underline-offset-4 hover:underline focus-visible:outline-2 focus-visible:outline-ring"><span className="block font-medium [overflow-wrap:anywhere]">{child.name}</span><span className="block text-sm text-muted-foreground [overflow-wrap:anywhere]">{child.code}{child.status === 'archived' ? ` (${text.archived})` : ''}</span></Link></li>
                      })}</ul>}
                    </section>
                  </div>}
              <p className="max-w-prose text-sm leading-relaxed text-muted-foreground">{text.future}</p>
              {treeQuery.dataUpdatedAt > 0 && <p className="text-sm text-muted-foreground">{text.readAt}：{new Intl.DateTimeFormat('zh-TW', { dateStyle: 'short', timeStyle: 'short' }).format(treeQuery.dataUpdatedAt)}</p>}
              <LocationReviewDialog operation={operation} tree={tree} online={online} onReadCurrent={readCurrent} />
            </>}
  </div>
}
