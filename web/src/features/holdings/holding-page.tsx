import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link, Navigate, useLocation, useNavigate, useParams } from 'react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { LoaderCircle, RefreshCw, WifiOff } from 'lucide-react'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { DraftLeaveGuard } from '@/components/draft-leave-guard'
import { MutationNotice, mutationControl as control } from '@/components/mutation-review'
import { fetchAdmin } from '@/lib/api/auth'
import { ApiError } from '@/lib/api/client'
import { pathIDSchema } from '@/lib/api/domain'
import { catalogNavigationState, catalogRoute } from '@/lib/catalog-navigation'
import { assetNavigationState, assetRoute } from '@/lib/asset-navigation'
import { holdingReturnPath, itemHoldingsRoute, stockPrefill, unitRoute } from '@/lib/holding-navigation'
import { messages } from '@/lib/messages'
import { useOnline } from '@/lib/use-online'
import { clearSessionQueries } from '@/features/auth/use-auth-action'
import { readLocationTree } from '@/features/locations/tree'
import { holdingCommandCurrent, holdingPath } from './display'
import type { StockAction } from './form'
import { HoldingError } from './holding-error'
import { Measurement, UnitFields } from './holding-fields'
import { HoldingList } from './holding-list'
import { HoldingReviewDialog } from './holding-review-dialog'
import { readItemHoldings, readUnitHolding, type ItemHoldings, type UnitHolding } from './model'
import { StockEditor } from './stock-editor'
import { UnitEditor } from './unit-editor'
import { useHoldingMutation } from './use-holding-mutation'

const text = messages.holdings
type View = ItemHoldings | (UnitHolding & { kind: 'unit' })
type Mode = 'item' | 'create-unit' | 'unit' | 'edit-unit' | 'move-unit' | 'status-unit' | 'stock'
export default function HoldingPage({ mode, action }: { mode: Mode; action?: StockAction }) {
  const route = useLocation(), navigate = useNavigate(), client = useQueryClient(), { id = '' } = useParams()
  const online = useOnline(), point = ['unit', 'edit-unit', 'move-unit', 'status-unit'].includes(mode)
  const [transitioning, setTransitioning] = useState(false), [remembered, setRemembered] = useState<View>()
  const validPath = holdingReturnPath(route.pathname + route.search) !== undefined
  const returnTo = validPath ? route.pathname + route.search : '/items'
  const navigationState = useMemo(() => ({ ...catalogNavigationState(route.state?.catalogList, route.state?.catalogCursors), ...(route.state?.assetList !== undefined ? assetNavigationState(route.state?.assetList, route.state?.assetCursors) : {}) }), [route.state?.catalogList, route.state?.catalogCursors, route.state?.assetList, route.state?.assetCursors])
  const auth = useQuery({ queryKey: ['auth', 'me'], enabled: online && !transitioning, staleTime: 0, refetchOnMount: 'always', queryFn: async ({ signal }) => {
    try { return await fetchAdmin(signal) } catch (error) { if (error instanceof ApiError && error.status === 401) return null; throw error }
  } })
  const authorized = !!auth.data && !auth.data.must_change_password
  const enabled = online && authorized && validPath && !transitioning
  const data = useQuery<View | null>({ queryKey: ['holdings', point ? 'unit' : 'item', id], enabled, staleTime: 0, refetchOnMount: 'always', queryFn: async ({ signal }) => {
    try { return point ? { kind: 'unit' as const, ...await readUnitHolding(id, signal) } : await readItemHoldings(id, signal) }
    catch (error) { if (error instanceof ApiError && error.status === 404) return null; throw error }
  } })
  const locations = useQuery({ queryKey: ['locations', 'tree'], enabled, staleTime: 0, refetchOnMount: 'always', queryFn: ({ signal }) => readLocationTree(signal) })
  const compatible = data.data !== undefined && data.data !== null && (point ? data.data.kind === 'unit' : mode === 'stock' ? data.data.kind === 'stock' : mode === 'create-unit' ? data.data.kind === 'units' : true)
  if (data.data && compatible && data.data !== remembered) setRemembered(data.data)
  const metadata = compatible ? data.data : remembered ?? data.data
  const item = data.data?.item ?? metadata?.item
  const unit = metadata?.kind === 'unit' ? metadata.unit : undefined
  const missing = data.data === null, wrongMode = !!data.data && !compatible
  const sessionLost = useCallback((mandatory: boolean) => {
    setTransitioning(true); clearSessionQueries(client)
    if (!mandatory) client.setQueryData(['auth', 'me'], null)
    navigate(mandatory ? '/settings/account' : '/login', { replace: true, state: { ...(mandatory ? {} : { notice: 'expired' }), ...navigationState, returnTo } })
  }, [client, navigate, returnTo, navigationState])
  const operation = useHoldingMutation((command, result) => {
    const destination = command.kind === 'create-unit' && 'id' in result ? unitRoute(result.id)
      : 'subject' in command && command.kind !== 'delete-unit' ? unitRoute(command.subject.id) : itemHoldingsRoute(command.owner.id)
    navigate(destination, { replace: true, state: { ...navigationState, holdingNotice: 'confirmed' } })
  }, sessionLost)
  const credentialFailure = [data.error, locations.error].find((error) => error instanceof ApiError && (error.status === 401 || error.code === 'PASSWORD_CHANGE_REQUIRED')) as ApiError | undefined
  const mandatory = !!auth.data?.must_change_password || credentialFailure?.code === 'PASSWORD_CHANGE_REQUIRED'
  const denied = mandatory || !auth.isFetching && auth.data === null || credentialFailure !== undefined
  const { allowNavigation } = operation
  useEffect(() => {
    if (!online || transitioning || !denied) return
    allowNavigation(); clearSessionQueries(client)
    if (!mandatory) client.setQueryData(['auth', 'me'], null)
    navigate(mandatory ? '/settings/account' : '/login', { replace: true, state: { ...(mandatory ? {} : { notice: 'expired' }), ...navigationState, returnTo } })
  }, [online, transitioning, denied, mandatory, allowNavigation, client, navigate, returnTo, navigationState])
  async function readCurrent() { return Promise.all([auth.refetch(), ...(authorized && validPath ? [data.refetch(), locations.refetch()] : [])]) }
  const reading = auth.isFetching || data.isFetching || locations.isFetching
  const editorReady = data.isFetchedAfterMount && locations.isFetchedAfterMount
  const initializing = online && authorized && validPath && !editorReady
  const pending = online && (auth.isPending || !authorized && !auth.isError || enabled && (data.isPending || locations.isPending))
  const readError = auth.error ?? data.error ?? locations.error
  const fatal = auth.isError && !auth.data ? auth.error : data.isError && !metadata ? data.error : locations.isError && !locations.data ? locations.error : undefined
  const permitted = online && authorized && !transitioning && !denied && !auth.isError
  const canWrite = permitted && validPath && !reading && !readError && !!data.data && !!locations.data && compatible
  const currentReview = !!operation.command && !reading && !readError && holdingCommandCurrent(operation.command, data.data?.item, data.data?.kind === 'unit' ? data.data.unit : undefined, locations.data)
  const formMode = mode !== 'item' && mode !== 'unit'
  const title = mode === 'item' ? text.nav : mode === 'create-unit' ? text.createUnit : mode === 'edit-unit' ? text.editUnit : mode === 'move-unit' ? text.moveUnit : mode === 'status-unit' ? text.statusUnit : mode === 'stock' && action ? text.actions[action] : text.unitDetail
  function removeUnit() {
    if (!canWrite || !unit || !item || !locations.data) return
    const path = holdingPath(locations.data, unit.location_id)
    if (path) operation.review({ kind: 'delete-unit', owner: item, subject: unit, location: path })
  }
  return <div className="min-w-0 space-y-7 py-8 md:py-10">
    <header className="flex min-w-0 flex-wrap items-start justify-between gap-4"><div className="min-w-0 space-y-3"><h1 className="text-3xl leading-snug font-semibold [overflow-wrap:anywhere] md:text-4xl">{title}</h1><p className="max-w-prose text-base leading-relaxed text-muted-foreground">{text.intro}</p></div><div className="flex max-w-full flex-wrap gap-2">
      <Button asChild variant="outline" className={control}><Link to={navigationState.catalogList} state={{ catalogCursors: navigationState.catalogCursors }}>{messages.catalog.back}</Link></Button>
      {item && <><Button asChild variant="outline" className={control}><Link to={catalogRoute(item.id)} state={navigationState}>{messages.catalog.backDetail}</Link></Button>{mode !== 'item' && <Button asChild variant="outline" className={control}><Link to={itemHoldingsRoute(item.id)} state={navigationState}>{text.back}</Link></Button>}</>}
      {point && mode !== 'unit' && unit && <Button asChild variant="outline" className={control}><Link to={unitRoute(unit.id)} state={navigationState}>{text.unitDetail}</Link></Button>}
      <Button type="button" variant="outline" disabled={!online || reading || transitioning || !validPath} className={`${control} gap-2`} onClick={() => { void readCurrent() }}><RefreshCw aria-hidden="true" className={`size-4 ${reading ? 'animate-spin motion-reduce:animate-none' : ''}`} />{text.refresh}</Button>
    </div></header>
    {!online && <Alert role="status" className="p-4"><WifiOff aria-hidden="true" /><AlertTitle>{text.offline}</AlertTitle><AlertDescription className="text-base">{text.offlineHelp}</AlertDescription></Alert>}
    {route.state?.holdingNotice === 'confirmed' && <Alert role="status" className="p-4"><AlertDescription className="text-base">{text.done}</AlertDescription></Alert>}
    <MutationNotice operation={operation} online={permitted} onReadCurrent={readCurrent} />
    {transitioning || denied || initializing || pending && (!metadata || !locations.data) ? <p role="status" className="flex min-h-28 items-center gap-3"><LoaderCircle aria-hidden="true" className="size-6 animate-spin motion-reduce:animate-none" />{auth.isPending || denied || transitioning ? text.checking : text.loading}</p>
      : !validPath ? <HoldingError error={new ApiError('INVALID_HOLDING_URL')} /> : fatal ? <HoldingError error={fatal} />
        : !metadata || !locations.data ? missing ? <section className="space-y-3"><h2 className="text-xl font-semibold">{text.missing}</h2><p>{text.missingHelp}</p></section> : <p role="status">{text.offlineHelp}</p>
          : <>
            {readError && <HoldingError error={readError} />}
            <section className="min-w-0 space-y-3"><h2 className="whitespace-pre-wrap text-xl font-semibold [overflow-wrap:anywhere]">{JSON.stringify(item!.name)}</h2><p className="text-sm text-muted-foreground [overflow-wrap:anywhere]">{text.owner}：{item!.id}；{text.version}：{item!.revision}</p>{metadata.kind === 'stock' && <Measurement item={metadata.item} />}</section>
            {(missing || wrongMode) && <Alert role="alert" className="p-4"><AlertTitle>{missing ? text.missing : text.trackingError}</AlertTitle><AlertDescription className="text-base">{missing ? text.missingHelp : text.wrongMode}</AlertDescription></Alert>}
            {mode === 'item' && metadata.kind !== 'unit' && <HoldingList data={metadata} locations={locations.data} navigationState={navigationState} canWrite={canWrite} />}
            {mode === 'unit' && unit && <section className="min-w-0 max-w-3xl space-y-6"><div className="flex flex-wrap gap-2">{(['edit', 'move', 'status'] as const).map((action) => canWrite && !(action === 'move' && unit.status === 'disposed') ? <Button key={action} asChild variant={action === 'edit' ? 'default' : 'outline'} className={control}><Link to={unitRoute(unit.id, action)} state={navigationState}>{action === 'edit' ? text.editUnit : action === 'move' ? text.moveUnit : text.statusUnit}</Link></Button> : <Button key={action} disabled variant="outline" className={control}>{action === 'edit' ? text.editUnit : action === 'move' ? text.moveUnit : text.statusUnit}</Button>)}</div>
              <Button asChild variant="outline" className={control}><Link to={assetRoute(unit.id)} state={navigationState}>{messages.assets.title}</Link></Button>
              <UnitFields unit={unit} locations={locations.data} /><p className="text-sm text-muted-foreground [overflow-wrap:anywhere]">{text.identifier}：{unit.id}<br />{text.unitProof}{unit.revision}</p>{unit.status === 'disposed' && <p>{text.disposedHelp}</p>}
              <section className="space-y-4 border-t pt-6"><p className="text-base leading-relaxed text-muted-foreground">{text.deleteHelp}</p><Button type="button" variant="destructive" disabled={!canWrite || operation.phase === 'pending' || operation.phase === 'uncertain' || holdingPath(locations.data, unit.location_id) === undefined} className={control} onClick={removeUnit}>{text.deleteUnit}</Button></section>
            </section>}
            {formMode && !editorReady && <p role="status">{text.offlineHelp}</p>}
            {mode === 'stock' && action && editorReady && metadata.kind === 'stock' && <Card className="max-w-3xl [--card-spacing:--spacing(6)]"><CardContent className="pt-6"><StockEditor action={action} data={metadata} currentItem={item!} locations={locations.data} prefill={stockPrefill(route.search)} operation={operation} online={online} canWrite={canWrite} /></CardContent></Card>}
            {mode === 'create-unit' && editorReady && metadata.kind === 'units' && <Card className="max-w-3xl [--card-spacing:--spacing(6)]"><CardContent className="pt-6"><UnitEditor mode="create" item={item!} locations={locations.data} operation={operation} online={online} canWrite={canWrite} /></CardContent></Card>}
            {point && formMode && editorReady && unit && <Card className="max-w-3xl [--card-spacing:--spacing(6)]"><CardContent className="pt-6"><UnitEditor mode={mode === 'edit-unit' ? 'edit' : mode === 'move-unit' ? 'move' : 'status'} item={item!} unit={unit} locations={locations.data} operation={operation} online={online} canWrite={canWrite} /></CardContent></Card>}
            <p className="max-w-prose text-sm leading-relaxed text-muted-foreground">{text.future}</p>
            {data.dataUpdatedAt > 0 && <p className="text-sm text-muted-foreground">{text.readAt}：{new Intl.DateTimeFormat('zh-TW', { dateStyle: 'short', timeStyle: 'short' }).format(Math.min(data.dataUpdatedAt, locations.dataUpdatedAt))}</p>}
          </>}
    {!formMode && <DraftLeaveGuard labels={text} when={!!operation.command} canLeave={operation.canLeave} onLeave={operation.allowNavigation} onBlocked={operation.suspend} onStay={operation.reopen} />}
    <HoldingReviewDialog operation={operation} online={permitted} current={currentReview} onReadCurrent={readCurrent} />
  </div>
}
export function UnitAlias() {
  const { id = '' } = useParams()
  return pathIDSchema.safeParse(id).success ? <Navigate to={unitRoute(id)} replace /> : <HoldingError error={new ApiError('INVALID_HOLDING_URL')} />
}
