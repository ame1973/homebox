import { useCallback, useEffect, useState } from 'react'
import { Link, NavLink, useLocation, useNavigate, useParams } from 'react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { LoaderCircle, Plus, RefreshCw, WifiOff } from 'lucide-react'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { MutationNotice, mutationControl as control } from '@/components/mutation-review'
import { fetchAdmin } from '@/lib/api/auth'
import { ApiError } from '@/lib/api/client'
import { catalogNavigationState, catalogOwnedCreateRoute, catalogOwnedKinds, catalogOwnedRoute, catalogReturnPath, catalogRoute, type CatalogOwnedKind } from '@/lib/catalog-navigation'
import { messages } from '@/lib/messages'
import { useOnline } from '@/lib/use-online'
import { clearSessionQueries } from '@/features/auth/use-auth-action'
import { CatalogError } from './catalog-error'
import { OwnedList } from './owned-list'
import { readOwnedMetadata, type OwnedMetadata } from './owned-model'
import { OwnedReviewDialog } from './owned-review-dialog'
import { OwnedRowEditor } from './owned-row-editor'
import { OwnedTagsEditor } from './owned-tags-editor'
import type { OwnedRow } from './display'
import { useOwnedMutation } from './use-owned-mutation'

const text = messages.owned
export default function OwnedPage({ kind, mode }: { kind: CatalogOwnedKind; mode: 'list' | 'create' | 'edit' }) {
  const route = useLocation()
  const navigate = useNavigate()
  const client = useQueryClient()
  const { id = '', rowID = '' } = useParams()
  const online = useOnline()
  const [transitioning, setTransitioning] = useState(false)
  const [remembered, setRemembered] = useState<OwnedMetadata>()
  const [rememberedRow, setRememberedRow] = useState<OwnedRow>()
  const validPath = catalogReturnPath(route.pathname + route.search) !== undefined
  const returnTo = validPath ? route.pathname + route.search : '/items'
  const navigationState = catalogNavigationState(route.state?.catalogList, route.state?.catalogCursors)
  const auth = useQuery({ queryKey: ['auth', 'me'], enabled: online && !transitioning, staleTime: 0, refetchOnMount: 'always', queryFn: async ({ signal }) => {
    try { return await fetchAdmin(signal) } catch (error) { if (error instanceof ApiError && error.status === 401) return null; throw error }
  } })
  const authorized = !!auth.data && !auth.data.must_change_password
  // This complete live review has its own catalog cache shape. Do not replace
  // taxonomy's wrapped indexes or the independent Item resource read with it.
  const data = useQuery({ queryKey: ['catalog', 'owned', id, kind], enabled: online && authorized && validPath && !transitioning, staleTime: 0, refetchOnMount: 'always', queryFn: async ({ signal }) => {
    try { return await readOwnedMetadata(id, kind, signal) } catch (error) { if (error instanceof ApiError && error.status === 404) return null; throw error }
  } })
  if (data.data && data.data !== remembered) setRemembered(data.data)
  const currentRow = mode === 'edit' && data.data && data.data.kind !== 'tags' ? data.data.rows.find((row) => row.id === rowID) : undefined
  if (currentRow && currentRow !== rememberedRow) setRememberedRow(currentRow)
  const metadata = data.data ?? remembered
  const subject = currentRow ?? rememberedRow
  const missingRow = mode === 'edit' && data.data !== undefined && !currentRow
  const missingOwner = data.data === null
  const sessionLost = useCallback((mandatory: boolean) => {
    setTransitioning(true)
    clearSessionQueries(client)
    if (!mandatory) client.setQueryData(['auth', 'me'], null)
    navigate(mandatory ? '/settings/account' : '/login', { replace: true, state: { ...(mandatory ? {} : { notice: 'expired' }), returnTo } })
  }, [client, navigate, returnTo])
  const operation = useOwnedMutation(() => {
    navigate(catalogOwnedRoute(id, kind), { replace: true, state: { ...navigationState, ownedNotice: 'confirmed' } })
  }, sessionLost)
  const credentialFailure = data.error instanceof ApiError && (data.error.status === 401 || data.error.code === 'PASSWORD_CHANGE_REQUIRED') ? data.error : undefined
  const mandatory = !!auth.data?.must_change_password || credentialFailure?.code === 'PASSWORD_CHANGE_REQUIRED'
  const denied = mandatory || !auth.isFetching && auth.data === null || credentialFailure !== undefined
  const { allowNavigation } = operation
  useEffect(() => {
    if (!online || transitioning || !denied) return
    allowNavigation()
    clearSessionQueries(client)
    if (!mandatory) client.setQueryData(['auth', 'me'], null)
    navigate(mandatory ? '/settings/account' : '/login', { replace: true, state: { ...(mandatory ? {} : { notice: 'expired' }), returnTo } })
  }, [online, transitioning, denied, mandatory, allowNavigation, client, navigate, returnTo])
  async function readCurrent() { return Promise.all([auth.refetch(), ...(authorized && validPath ? [data.refetch()] : [])]) }
  const reading = auth.isFetching || data.isFetching
  const pending = online && (auth.isPending || !authorized && !auth.isError || authorized && validPath && data.isPending)
  // A newly mounted editor must not adopt an older cached baseline while its
  // first current read is still running. Later refetches keep mounted drafts.
  const initializing = online && authorized && validPath && !data.isFetchedAfterMount
  const readError = auth.error ?? data.error
  const fatal = auth.isError && !auth.data ? auth.error : data.isError && !metadata ? data.error : undefined
  const permitted = online && authorized && !transitioning && !denied && !auth.isError
  const canWrite = permitted && validPath && !reading && !readError && !!data.data && !missingRow
  const title = kind === 'tags' || mode === 'list' ? text.collections[kind] : mode === 'create' ? text.create[kind] : text.edit[kind]

  return <div className="min-w-0 space-y-7 py-8 md:py-10">
    <header className="flex min-w-0 flex-wrap items-start justify-between gap-4"><div className="min-w-0 space-y-3"><h1 className="text-3xl leading-snug font-semibold [overflow-wrap:anywhere] md:text-4xl">{title}</h1><p className="max-w-prose text-base leading-relaxed text-muted-foreground">{text.intro}</p></div><div className="flex max-w-full flex-wrap gap-2">
      <Button asChild variant="outline" className={control}><Link to={navigationState.catalogList} state={{ catalogCursors: navigationState.catalogCursors }}>{messages.catalog.back}</Link></Button>
      {metadata && <Button asChild variant="outline" className={control}><Link to={catalogRoute(metadata.item.id)} state={navigationState}>{messages.catalog.backDetail}</Link></Button>}
      {metadata && kind !== 'tags' && mode !== 'list' && <Button asChild variant="outline" className={control}><Link to={catalogOwnedRoute(metadata.item.id, kind)} state={navigationState}>{text.back}</Link></Button>}
      <Button type="button" variant="outline" disabled={!online || reading || transitioning || !validPath} className={`${control} gap-2`} onClick={() => { void readCurrent() }}><RefreshCw aria-hidden="true" className={`size-4 ${reading ? 'animate-spin motion-reduce:animate-none' : ''}`} />{text.refresh}</Button>
    </div></header>
    {!online && <Alert role="status" className="p-4"><WifiOff aria-hidden="true" /><AlertTitle>{text.offline}</AlertTitle><AlertDescription className="text-base">{text.offlineHelp}</AlertDescription></Alert>}
    {route.state?.ownedNotice === 'confirmed' && <Alert role="status" className="p-4"><AlertDescription className="text-base">{text.done}</AlertDescription></Alert>}
    <MutationNotice operation={operation} online={permitted} onReadCurrent={readCurrent} />
    {transitioning || denied || initializing || pending && !metadata ? <p role="status" className="flex min-h-28 items-center gap-3"><LoaderCircle aria-hidden="true" className="size-6 animate-spin motion-reduce:animate-none" />{auth.isPending || denied || transitioning ? messages.catalog.checking : text.loading}</p>
      : !validPath ? <CatalogError error={new ApiError('INVALID_QUERY')} />
        : fatal ? <CatalogError error={fatal} />
          : !metadata ? missingOwner ? <section className="space-y-3"><h2 className="text-xl font-semibold">{messages.catalog.missing}</h2><p>{messages.catalog.missingHelp}</p></section> : <p role="status">{text.offlineHelp}</p>
            : <>
              {readError && <CatalogError error={readError} />}
              <section className="min-w-0 space-y-3"><h2 className="whitespace-pre-wrap text-xl font-semibold [overflow-wrap:anywhere]">{JSON.stringify(metadata.item.name)}</h2><p className="text-sm text-muted-foreground [overflow-wrap:anywhere]">{text.owner}：{metadata.item.id}；{text.version}：{metadata.item.revision}</p></section>
              <nav aria-label={text.navigation} className="flex max-w-full flex-wrap gap-2">{catalogOwnedKinds.map((collection) => <Button key={collection} asChild variant="outline" className={`${control} aria-[current=page]:border-primary aria-[current=page]:bg-muted`}><NavLink to={catalogOwnedRoute(metadata.item.id, collection)} state={navigationState}>{text.collections[collection]}</NavLink></Button>)}</nav>
              {(missingOwner || missingRow) && <Alert role="alert" className="p-4"><AlertTitle>{missingOwner ? messages.catalog.missing : text.missing}</AlertTitle><AlertDescription className="text-base">{text.missingHelp}</AlertDescription></Alert>}
              {metadata.kind === 'tags' ? <Card className="max-w-3xl [--card-spacing:--spacing(6)]"><CardContent className="pt-6"><OwnedTagsEditor data={metadata} operation={operation} online={online} canWrite={canWrite} /></CardContent></Card>
                : mode === 'list' ? <section className="max-w-3xl space-y-5">{canWrite ? <Button asChild className={`${control} gap-2`}><Link to={catalogOwnedCreateRoute(id, metadata.kind)} state={navigationState}><Plus aria-hidden="true" />{text.create[metadata.kind]}</Link></Button> : <Button disabled className={control}>{text.create[metadata.kind]}</Button>}<OwnedList itemID={id} kind={metadata.kind} records={metadata.rows} navigationState={navigationState} /></section>
                  : missingRow && !subject ? <p>{text.missingHelp}</p> : <Card className="max-w-3xl [--card-spacing:--spacing(6)]"><CardContent className="pt-6"><OwnedRowEditor kind={metadata.kind} owner={metadata.item} subject={subject} operation={operation} online={online} canWrite={canWrite} /></CardContent></Card>}
              {data.dataUpdatedAt > 0 && <p className="text-sm text-muted-foreground">{text.readAt}：{new Intl.DateTimeFormat('zh-TW', { dateStyle: 'short', timeStyle: 'short' }).format(data.dataUpdatedAt)}</p>}
            </>}
    <OwnedReviewDialog operation={operation} online={permitted} onReadCurrent={readCurrent} />
  </div>
}
