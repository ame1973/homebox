import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link, useLocation, useNavigate, useParams } from 'react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { LoaderCircle, RefreshCw, WifiOff } from 'lucide-react'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { MutationNotice, mutationControl as control } from '@/components/mutation-review'
import { fetchAdmin } from '@/lib/api/auth'
import { ApiError } from '@/lib/api/client'
import { assetNavigationState, assetReturnPath, assetRoute } from '@/lib/asset-navigation'
import { catalogNavigationState, catalogRoute } from '@/lib/catalog-navigation'
import { unitRoute } from '@/lib/holding-navigation'
import { messages } from '@/lib/messages'
import { useOnline } from '@/lib/use-online'
import { AssetError } from '@/features/assets/asset-error'
import { AssetProfileFields } from '@/features/assets/asset-fields'
import { readDisposalCorrection, type DisposalCorrectionSource } from '@/features/assets/correction-model'
import { clearSessionQueries } from '@/features/auth/use-auth-action'
import { UnitFields } from '@/features/holdings/holding-fields'
import { DisposalCorrectionEditor } from './disposal-editor'
import { DisposalCorrectionHistory, DisposalCorrectionProofs, DisposalDecision } from './disposal-fields'
import { sameDisposalCorrectionSource } from './disposal-editor-model'
import { DisposalCorrectionReview } from './disposal-review'
import { useCorrectionMutation } from './use-correction-mutation'

const text = messages.disposalCorrections, asset = messages.assets
export default function DisposalCorrectionPage() {
  const route = useLocation(), navigate = useNavigate(), client = useQueryClient(), { id = '' } = useParams()
  const online = useOnline(), [transitioning, setTransitioning] = useState(false), [remembered, setRemembered] = useState<DisposalCorrectionSource>()
  const validPath = assetReturnPath(route.pathname + route.search) !== undefined
  const catalogState = useMemo(() => catalogNavigationState(route.state?.catalogList, route.state?.catalogCursors), [route.state?.catalogList, route.state?.catalogCursors])
  const assetState = useMemo(() => assetNavigationState(route.state?.assetList, route.state?.assetCursors), [route.state?.assetList, route.state?.assetCursors])
  const navigationState = useMemo(() => ({ ...catalogState, ...assetState }), [catalogState, assetState])
  const returnTo = validPath ? route.pathname + route.search : '/assets'
  const auth = useQuery({ queryKey: ['auth', 'me'], enabled: online && !transitioning, staleTime: 0, refetchOnMount: 'always', queryFn: async ({ signal }) => {
    try { return await fetchAdmin(signal) } catch (error) { if (error instanceof ApiError && error.status === 401) return null; throw error }
  } })
  const authorized = !!auth.data && !auth.data.must_change_password, enabled = online && authorized && validPath && !transitioning
  const record = useQuery<DisposalCorrectionSource | null>({ queryKey: ['assets', 'disposal-corrections', id], enabled, staleTime: 0, refetchOnMount: 'always', queryFn: async ({ signal }) => {
    try { return await readDisposalCorrection(id, signal) } catch (error) { if (error instanceof ApiError && error.status === 404) return null; throw error }
  } })
  const current = record.data
  if (current && record.isFetchedAfterMount && !record.isError && current !== remembered) setRemembered(current)
  const source = current ?? remembered
  const sessionLost = useCallback((mandatory: boolean) => {
    setTransitioning(true); clearSessionQueries(client)
    if (!mandatory) client.setQueryData(['auth', 'me'], null)
    navigate(mandatory ? '/settings/account' : '/login', { replace: true, state: { ...(mandatory ? {} : { notice: 'expired' }), returnTo, ...navigationState } })
  }, [client, navigate, returnTo, navigationState])
  const operation = useCorrectionMutation(() => {
    navigate(assetRoute(id, 'disposal-corrections'), { replace: true, state: { ...navigationState, disposalCorrectionNotice: 'confirmed' } })
  }, sessionLost)
  const errors = [auth.error, record.error]
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
  async function readCurrent() { return Promise.all([auth.refetch(), ...(authorized && validPath ? [record.refetch()] : [])]) }
  const reading = auth.isFetching || record.isFetching, readError = errors.find((error) => error != null)
  const initializing = online && authorized && validPath && !record.isFetchedAfterMount
  const pending = online && (auth.isPending || !authorized && !auth.isError || enabled && record.isPending)
  const fatal = auth.isError && !auth.data ? auth.error : record.isError && !source ? record.error : undefined
  const permitted = online && authorized && !transitioning && !denied && !auth.isError
  const canWrite = permitted && validPath && !reading && !readError && !!current
  const currentReview = !!operation.command && operation.command.kind === 'correct-disposal' && !reading && !readError && sameDisposalCorrectionSource(operation.command.source, current)
  const readAt = Math.min(auth.dataUpdatedAt, record.dataUpdatedAt)
  return <div className="min-w-0 space-y-7 py-8 md:py-10"><header className="flex min-w-0 flex-wrap items-start justify-between gap-4"><div className="min-w-0 space-y-3"><h1 className="text-2xl leading-snug font-semibold [overflow-wrap:anywhere] sm:text-3xl md:text-4xl">{text.title}</h1><p className="max-w-prose text-base leading-relaxed text-muted-foreground">{text.intro}</p></div><div className="flex max-w-full flex-wrap gap-2">
    <Button asChild variant="outline" className={control}><Link to={assetState.assetList} state={navigationState}>{asset.back}</Link></Button>{validPath && <Button asChild variant="outline" className={control}><Link to={assetRoute(id)} state={navigationState}>{asset.backDetail}</Link></Button>}
    {source && <><Button asChild variant="outline" className={control}><Link to={unitRoute(source.unit.id)} state={navigationState}>{messages.holdings.unitDetail}</Link></Button><Button asChild variant="outline" className={control}><Link to={catalogRoute(source.item.id)} state={navigationState}>{messages.catalog.backDetail}</Link></Button></>}
    <Button type="button" variant="outline" disabled={!online || reading || transitioning || !validPath} className={`${control} gap-2`} onClick={() => { void readCurrent() }}><RefreshCw aria-hidden="true" className={`size-4 ${reading ? 'animate-spin motion-reduce:animate-none' : ''}`} />{asset.refresh}</Button>
  </div></header>
    {!online && <Alert role="status" className="p-4"><WifiOff aria-hidden="true" /><AlertTitle>{text.offline}</AlertTitle><AlertDescription className="text-base">{text.offlineHelp}</AlertDescription></Alert>}
    {route.state?.disposalCorrectionNotice === 'confirmed' && <Alert role="status" className="p-4"><AlertDescription className="text-base">{text.done}</AlertDescription></Alert>}
    <MutationNotice operation={operation} online={permitted} onReadCurrent={readCurrent} />
    {transitioning || denied || initializing || pending && !source ? <p role="status" className="flex min-h-28 items-center gap-3"><LoaderCircle aria-hidden="true" className="size-6 animate-spin motion-reduce:animate-none" />{auth.isPending || denied || transitioning ? asset.checking : asset.loading}</p>
      : !validPath ? <><AssetError error={new ApiError('INVALID_ASSET_URL')} /><Button asChild variant="outline" className={control}><Link to="/assets">{messages.catalog.firstPage}</Link></Button></> : fatal ? <AssetError error={fatal} />
        : !source ? current === null ? <section className="space-y-3"><h2 className="text-xl font-semibold">{text.missing}</h2><p>{text.missingHelp}</p></section> : <p role="status">{text.offlineHelp}</p> : <>
          {readError && <AssetError error={readError} />}{current === null && <Alert role="alert" className="p-4"><AlertTitle>{text.missing}</AlertTitle><AlertDescription className="text-base">{text.missingHelp}</AlertDescription></Alert>}
          <section className="min-w-0 space-y-5"><h2 className="whitespace-pre-wrap text-xl font-semibold [overflow-wrap:anywhere]">{JSON.stringify(source.item.name)}；{JSON.stringify(source.unit.asset_code)}</h2><DisposalCorrectionProofs source={source} /><AssetProfileFields asset={source.asset} /><UnitFields unit={source.unit} /><DisposalDecision title={text.original} data={source.disposal.original} /><DisposalDecision title={text.current} data={source.disposal.current} /></section>
          <DisposalCorrectionHistory source={source} />
          {record.isFetchedAfterMount ? <Card className="max-w-3xl [--card-spacing:--spacing(6)]"><CardContent className="pt-6"><DisposalCorrectionEditor data={source} operation={operation} online={online} canWrite={canWrite} /></CardContent></Card> : <p role="status">{text.offlineHelp}</p>}
          {readAt > 0 && <p className="text-sm text-muted-foreground">{text.readAt}：{new Intl.DateTimeFormat('zh-TW', { dateStyle: 'short', timeStyle: 'short' }).format(readAt)}</p>}
        </>}
    <DisposalCorrectionReview operation={operation} online={permitted} current={currentReview} onReadCurrent={readCurrent} />
  </div>
}
