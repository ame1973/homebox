import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link, NavLink, useLocation, useNavigate, useParams } from 'react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { LoaderCircle, RefreshCw, WifiOff } from 'lucide-react'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { MutationNotice, mutationControl as control } from '@/components/mutation-review'
import { fetchAdmin } from '@/lib/api/auth'
import { fetchAssetsPage } from '@/lib/api/assets'
import { ApiError } from '@/lib/api/client'
import { assetListRoute, assetNavigationState, assetQuery, assetReturnPath, assetRoute, assetSummaryQuery, assetSummaryRoute } from '@/lib/asset-navigation'
import { catalogNavigationState, catalogRoute } from '@/lib/catalog-navigation'
import { unitRoute } from '@/lib/holding-navigation'
import { messages } from '@/lib/messages'
import { useOnline } from '@/lib/use-online'
import { clearSessionQueries } from '@/features/auth/use-auth-action'
import { CatalogFilters } from '@/features/catalog/filters'
import { UnitFields } from '@/features/holdings/holding-fields'
import { readLocationTree } from '@/features/locations/tree'
import { readCategoryTree } from '@/features/taxonomy/model'
import { AssetCalculation } from './asset-calculation'
import { AssetEditor } from './asset-editor'
import { AssetError } from './asset-error'
import { AssetEvents, AssetEvidence, AssetList, AssetProfileFields, AssetProofs } from './asset-fields'
import { AssetReviewDialog } from './asset-review-dialog'
import { assetCommandCurrent } from './display'
import type { AssetEditorData } from './editor-model'
import { readAssetCosts, readAssetDetail, readAssetLifecycle, readAssetProfile, readAssetUsage, type AssetCostsSource, type AssetDetail, type AssetLifecycleSource, type AssetProfileSource, type AssetUsageSource } from './model'
import { useAssetMutation } from './use-asset-mutation'

const text = messages.assets
export type AssetPageMode = 'list' | 'detail' | 'edit' | 'usage' | 'usage-new' | 'costs' | 'costs-new' | 'retire' | 'dispose' | 'summary'
type View = Readonly<{ scope: 'profile'; source: AssetProfileSource }> | Readonly<{ scope: 'usage'; source: AssetUsageSource }> | Readonly<{ scope: 'costs'; source: AssetCostsSource }> | Readonly<{ scope: 'lifecycle'; source: AssetLifecycleSource }> | Readonly<{ scope: 'summary'; source: AssetDetail }>
export default function AssetPage({ mode }: { mode: AssetPageMode }) {
  const route = useLocation(), navigate = useNavigate(), client = useQueryClient(), { id = '' } = useParams()
  const online = useOnline(), point = mode !== 'list', formMode = ['edit', 'usage-new', 'costs-new', 'retire', 'dispose'].includes(mode)
  const [transitioning, setTransitioning] = useState(false), [remembered, setRemembered] = useState<View>()
  const validPath = assetReturnPath(route.pathname + route.search) !== undefined
  const query = mode === 'list' ? assetQuery(route.search) : undefined, calculation = mode === 'summary' ? assetSummaryQuery(route.search) : undefined
  const scope = mode.startsWith('usage') ? 'usage' : mode.startsWith('costs') ? 'costs' : mode === 'retire' || mode === 'dispose' ? 'lifecycle' : mode === 'summary' ? 'summary' : 'profile'
  const catalogState = useMemo(() => catalogNavigationState(route.state?.catalogList, route.state?.catalogCursors), [route.state?.catalogList, route.state?.catalogCursors])
  const assetState = useMemo(() => assetNavigationState(mode === 'list' ? route.pathname + route.search : route.state?.assetList, route.state?.assetCursors), [mode, route.pathname, route.search, route.state?.assetList, route.state?.assetCursors])
  const navigationState = useMemo(() => ({ ...catalogState, ...assetState }), [catalogState, assetState]), trail = assetState.assetCursors
  const returnTo = validPath ? route.pathname + route.search : '/assets'
  const auth = useQuery({ queryKey: ['auth', 'me'], enabled: online && !transitioning, staleTime: 0, refetchOnMount: 'always', queryFn: async ({ signal }) => {
    try { return await fetchAdmin(signal) } catch (error) { if (error instanceof ApiError && error.status === 401) return null; throw error }
  } })
  const authorized = !!auth.data && !auth.data.must_change_password, enabled = online && authorized && validPath && !transitioning
  const record = useQuery<View | null>({ queryKey: ['assets', scope, id, ...(mode === 'summary' ? [calculation] : [])], enabled: enabled && point, staleTime: 0, refetchOnMount: 'always', queryFn: async ({ signal }) => {
    try {
      if (scope === 'usage') return { scope, source: await readAssetUsage(id, signal) }
      if (scope === 'costs') return { scope, source: await readAssetCosts(id, signal) }
      if (scope === 'lifecycle') return { scope, source: await readAssetLifecycle(id, signal) }
      if (scope === 'summary') return { scope, source: await readAssetDetail(id, calculation ?? {}, signal) }
      return { scope: 'profile', source: await readAssetProfile(id, signal) }
    } catch (error) { if (error instanceof ApiError && error.status === 404) return null; throw error }
  } })
  const categories = useQuery({ queryKey: ['taxonomy', 'categories'], enabled: enabled && !point, staleTime: 0, refetchOnMount: 'always', queryFn: async ({ signal }) => ({ kind: 'categories' as const, value: await readCategoryTree(signal) }) })
  const locations = useQuery({ queryKey: ['locations', 'tree'], enabled: enabled && !point, staleTime: 0, refetchOnMount: 'always', queryFn: ({ signal }) => readLocationTree(signal) })
  const list = useQuery({ queryKey: ['assets', 'list', query], enabled: enabled && !point, staleTime: 0, refetchOnMount: 'always', queryFn: ({ signal }) => fetchAssetsPage(query ?? {}, signal) })
  const current = point ? record.data : undefined
  if (current && record.isFetchedAfterMount && !record.isError && current !== remembered) setRemembered(current)
  const metadata = current ?? remembered, source = metadata?.source
  const sessionLost = useCallback((mandatory: boolean) => {
    setTransitioning(true); clearSessionQueries(client)
    if (!mandatory) client.setQueryData(['auth', 'me'], null)
    navigate(mandatory ? '/settings/account' : '/login', { replace: true, state: { ...(mandatory ? {} : { notice: 'expired' }), returnTo, ...navigationState } })
  }, [client, navigate, returnTo, navigationState])
  const operation = useAssetMutation((command) => {
    navigate(assetRoute(command.source.unit.id, command.kind === 'record-usage' ? 'usage' : command.kind === 'record-cost' ? 'costs' : 'detail'), { replace: true, state: { ...navigationState, assetNotice: 'confirmed' } })
  }, sessionLost)
  const errors = [auth.error, ...(point ? [record.error] : [list.error, categories.error, locations.error])]
  const credentialFailure = errors.find((error) => error instanceof ApiError && (error.status === 401 || error.code === 'PASSWORD_CHANGE_REQUIRED')) as ApiError | undefined
  const mandatory = !!auth.data?.must_change_password || credentialFailure?.code === 'PASSWORD_CHANGE_REQUIRED'
  const denied = mandatory || !auth.isFetching && auth.data === null || credentialFailure !== undefined
  const { allowNavigation } = operation
  useEffect(() => {
    if (!online || transitioning || !denied) return
    allowNavigation(); clearSessionQueries(client)
    if (!mandatory) client.setQueryData(['auth', 'me'], null)
    navigate(mandatory ? '/settings/account' : '/login', { replace: true, state: { ...(mandatory ? {} : { notice: 'expired' }), returnTo, ...navigationState } })
  }, [online, transitioning, denied, mandatory, allowNavigation, client, navigate, returnTo, navigationState])
  async function readCurrent() { return Promise.all([auth.refetch(), ...(authorized && validPath ? point ? [record.refetch()] : [categories.refetch(), locations.refetch(), list.refetch()] : [])]) }
  const reading = auth.isFetching || (point ? record.isFetching : categories.isFetching || locations.isFetching || list.isFetching)
  const readError = errors.find((error) => error != null), initializing = online && authorized && validPath && formMode && !record.isFetchedAfterMount
  const pending = online && (auth.isPending || !authorized && !auth.isError || enabled && (point ? record.isPending : categories.isPending || locations.isPending || list.isPending))
  const fatal = auth.isError && !auth.data ? auth.error : point && record.isError && !metadata ? record.error : !point && (!categories.data && categories.isError || !locations.data && locations.isError || !list.data && list.isError) ? readError : undefined
  const permitted = online && authorized && !transitioning && !denied && !auth.isError
  const canWrite = permitted && validPath && !reading && !readError && (!point || !!current)
  const currentReview = !!operation.command && !reading && !readError && assetCommandCurrent(operation.command, current?.source)
  const invalidContinuation = list.data?.next_cursor != null && (trail.includes(list.data.next_cursor) || list.data.next_cursor === query?.cursor)
  const editor: AssetEditorData | undefined = mode === 'edit' && metadata?.scope === 'profile' ? { kind: 'put-profile', source: metadata.source }
    : mode === 'usage-new' && metadata?.scope === 'usage' ? { kind: 'record-usage', source: metadata.source } : mode === 'costs-new' && metadata?.scope === 'costs' ? { kind: 'record-cost', source: metadata.source }
      : mode === 'retire' && metadata?.scope === 'lifecycle' ? { kind: 'retire-asset', source: metadata.source } : mode === 'dispose' && metadata?.scope === 'lifecycle' ? { kind: 'dispose-asset', source: metadata.source } : undefined
  const titles = { list: text.title, detail: text.detail, edit: text.edit, usage: text.usage, 'usage-new': text.newUsage, costs: text.costs, 'costs-new': text.newCost, retire: text.retire, dispose: text.dispose, summary: text.summary }
  const readAt = Math.min(auth.dataUpdatedAt, ...(point ? [record.dataUpdatedAt] : [categories.dataUpdatedAt, locations.dataUpdatedAt, list.dataUpdatedAt]))
  function page(cursor: string | null, cursors: (string | null)[]) {
    if (!query) return
    const { cursor: _previous, ...scope } = query
    navigate(assetListRoute({ ...scope, ...(cursor ? { cursor } : {}) }), { state: { ...navigationState, assetCursors: cursors } })
  }
  return <div className="min-w-0 space-y-7 py-8 md:py-10"><header className="flex min-w-0 flex-wrap items-start justify-between gap-4"><div className="min-w-0 space-y-3"><h1 className="text-3xl leading-snug font-semibold [overflow-wrap:anywhere] md:text-4xl">{titles[mode]}</h1><p className="max-w-prose text-base leading-relaxed text-muted-foreground">{text.intro}</p></div><div className="flex max-w-full flex-wrap gap-2">
    {point && <Button asChild variant="outline" className={control}><Link to={assetState.assetList} state={navigationState}>{text.back}</Link></Button>}{point && validPath && mode !== 'detail' && <Button asChild variant="outline" className={control}><Link to={assetRoute(id)} state={navigationState}>{text.backDetail}</Link></Button>}
    {source && <><Button asChild variant="outline" className={control}><Link to={unitRoute(source.unit.id)} state={navigationState}>{messages.holdings.unitDetail}</Link></Button><Button asChild variant="outline" className={control}><Link to={catalogRoute(source.item.id)} state={navigationState}>{messages.catalog.backDetail}</Link></Button></>}
    <Button type="button" variant="outline" disabled={!online || reading || transitioning || !validPath} className={`${control} gap-2`} onClick={() => { void readCurrent() }}><RefreshCw aria-hidden="true" className={`size-4 ${reading ? 'animate-spin motion-reduce:animate-none' : ''}`} />{text.refresh}</Button>
  </div></header>
    {!online && <Alert role="status" className="p-4"><WifiOff aria-hidden="true" /><AlertTitle>{text.offline}</AlertTitle><AlertDescription className="text-base">{text.offlineHelp}</AlertDescription></Alert>}
    {route.state?.assetNotice === 'confirmed' && <Alert role="status" className="p-4"><AlertDescription className="text-base">{text.done}</AlertDescription></Alert>}
    <MutationNotice operation={operation} online={permitted} onReadCurrent={readCurrent} />
    {transitioning || denied || initializing || pending && (point ? !metadata : !list.data || !categories.data || !locations.data) ? <p role="status" className="flex min-h-28 items-center gap-3"><LoaderCircle aria-hidden="true" className="size-6 animate-spin motion-reduce:animate-none" />{auth.isPending || denied || transitioning ? text.checking : text.loading}</p>
      : !validPath ? <><AssetError error={new ApiError('INVALID_ASSET_URL')} /><Button asChild variant="outline" className={control}><Link to="/assets">{messages.catalog.firstPage}</Link></Button></> : fatal ? <AssetError error={fatal} />
        : point && !metadata ? current === null ? <section className="space-y-3"><h2 className="text-xl font-semibold">{text.missing}</h2><p>{text.missingHelp}</p></section> : <p role="status">{text.offlineHelp}</p> : <>
          {readError && <AssetError error={readError} />}{point && current === null && <Alert role="alert" className="p-4"><AlertTitle>{text.missing}</AlertTitle><AlertDescription className="text-base">{text.missingHelp}</AlertDescription></Alert>}
          {!point && query && categories.data && locations.data && <><CatalogFilters domain="assets" query={query} categories={categories.data.value} locations={locations.data} disabled={!permitted || reading} onApply={(values) => navigate(assetListRoute(values), { state: { ...navigationState, assetCursors: [] } })} />
            {invalidContinuation ? <AssetError error={new ApiError('INVALID_RESPONSE')} /> : list.data && <><AssetList assets={list.data.data} navigationState={navigationState} /><nav aria-label={text.pagination} className="flex flex-wrap gap-2"><Button type="button" variant="outline" disabled={!permitted || reading || !trail.length} className={control} onClick={() => page(trail.at(-1) ?? null, trail.slice(0, -1))}>{messages.catalog.previousPage}</Button><Button type="button" variant="outline" disabled={!permitted || reading || !query.cursor} className={control} onClick={() => page(null, [])}>{messages.catalog.firstPage}</Button><Button type="button" variant="outline" disabled={!permitted || reading || !list.data.next_cursor} className={control} onClick={() => page(list.data!.next_cursor, [...trail, query.cursor ?? null])}>{messages.catalog.nextPage}</Button></nav></>}
          </>}
          {source && <section className="min-w-0 space-y-4"><h2 className="whitespace-pre-wrap text-xl font-semibold [overflow-wrap:anywhere]">{JSON.stringify(source.item.name)}；{JSON.stringify(source.unit.asset_code)}</h2><AssetProofs source={source} />
            {source.asset !== null && <nav aria-label={text.navigation} className="flex max-w-full flex-wrap gap-2">{(['detail', 'usage', 'costs', 'summary'] as const).map((section) => <Button key={section} asChild variant="outline" className={`${control} aria-[current=page]:border-primary aria-[current=page]:bg-muted`}><NavLink end to={assetRoute(source.unit.id, section)} state={navigationState}>{section === 'detail' ? text.profile : text[section]}</NavLink></Button>)}</nav>}
          </section>}
          {mode === 'detail' && source && <section className="min-w-0 max-w-3xl space-y-6">{source.asset === null ? <><h2 className="text-xl font-semibold">{text.noProfile}</h2><p>{text.noProfileHelp}</p></> : <AssetProfileFields asset={source.asset} />}
            <div className="flex flex-wrap gap-2">{canWrite ? <Button asChild className={control}><Link to={assetRoute(id, 'edit')} state={navigationState}>{source.asset === null ? text.optIn : text.edit}</Link></Button> : <Button disabled className={control}>{source.asset === null ? text.optIn : text.edit}</Button>}
              {source.asset !== null && (['retire', 'dispose'] as const).map((action) => canWrite ? <Button key={action} asChild variant="outline" className={control}><Link to={assetRoute(id, action)} state={navigationState}>{text[action]}</Link></Button> : <Button key={action} disabled variant="outline" className={control}>{text[action]}</Button>)}
            </div><UnitFields unit={source.unit} /><AssetEvidence itemID={source.item.id} navigationState={navigationState} />
          </section>}
          {mode === 'usage' && metadata?.scope === 'usage' && <section className="space-y-5">{canWrite ? <Button asChild className={control}><Link to={assetRoute(id, 'usage/new')} state={navigationState}>{text.newUsage}</Link></Button> : <Button disabled className={control}>{text.newUsage}</Button>}<AssetEvents source={metadata.source} /></section>}
          {mode === 'costs' && metadata?.scope === 'costs' && <section className="space-y-5">{canWrite ? <Button asChild className={control}><Link to={assetRoute(id, 'costs/new')} state={navigationState}>{text.newCost}</Link></Button> : <Button disabled className={control}>{text.newCost}</Button>}<AssetEvents source={metadata.source} /></section>}
          {mode === 'summary' && metadata?.scope === 'summary' && calculation && <AssetCalculation data={metadata.source} query={calculation} navigationState={navigationState} disabled={!permitted || reading || !!readError} onApply={(values) => navigate(assetSummaryRoute(id, values), { state: navigationState })} />}
          {formMode && editor && (record.isFetchedAfterMount ? <Card className="max-w-3xl [--card-spacing:--spacing(6)]"><CardContent className="pt-6"><AssetEditor data={editor} operation={operation} online={online} canWrite={canWrite} /></CardContent></Card> : <p role="status">{text.offlineHelp}</p>)}
          <p className="max-w-prose text-sm leading-relaxed text-muted-foreground">{text.future}</p>{readAt > 0 && <p className="text-sm text-muted-foreground">{text.readAt}：{new Intl.DateTimeFormat('zh-TW', { dateStyle: 'short', timeStyle: 'short' }).format(readAt)}</p>}
        </>}
    <AssetReviewDialog operation={operation} online={permitted} current={currentReview} onReadCurrent={readCurrent} />
  </div>
}
