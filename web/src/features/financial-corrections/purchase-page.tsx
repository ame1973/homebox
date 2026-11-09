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
import { catalogNavigationState } from '@/lib/catalog-navigation'
import { messages } from '@/lib/messages'
import { purchaseCorrectionHistoryRoute, purchaseCorrectionLine, purchaseNavigationState, purchaseReturnPath, purchaseRoute } from '@/lib/purchase-navigation'
import { useOnline } from '@/lib/use-online'
import { clearSessionQueries } from '@/features/auth/use-auth-action'
import { readPurchaseCorrection, type PurchaseCorrectionSource } from '@/features/purchases/correction-model'
import { PurchaseError } from '@/features/purchases/purchase-error'
import { PurchaseCorrectionEditor } from './purchase-editor'
import { samePurchaseCorrectionSource } from './purchase-editor-model'
import { PurchaseCorrectionReview } from './purchase-review'
import { useCorrectionMutation } from './use-correction-mutation'

const text = messages.purchaseCorrections, purchase = messages.purchases

export default function PurchaseCorrectionPage() {
  const route = useLocation(), navigate = useNavigate(), client = useQueryClient(), { id = '' } = useParams()
  const online = useOnline(), [transitioning, setTransitioning] = useState(false), [targetID, setTargetID] = useState<string | null>(null), [remembered, setRemembered] = useState<PurchaseCorrectionSource>()
  const lineID = purchaseCorrectionLine(route.search), validPath = purchaseReturnPath(route.pathname + route.search) !== undefined && lineID !== undefined
  const catalogState = useMemo(() => catalogNavigationState(route.state?.catalogList, route.state?.catalogCursors), [route.state?.catalogList, route.state?.catalogCursors])
  const purchaseState = useMemo(() => purchaseNavigationState(route.state?.purchaseList, route.state?.purchaseCursors, route.state?.purchaseItem), [route.state?.purchaseList, route.state?.purchaseCursors, route.state?.purchaseItem])
  const navigationState = useMemo(() => ({ ...catalogState, ...purchaseState }), [catalogState, purchaseState])
  const returnTo = validPath ? route.pathname + route.search : '/purchases'
  const auth = useQuery({ queryKey: ['auth', 'me'], enabled: online && !transitioning, staleTime: 0, refetchOnMount: 'always', queryFn: async ({ signal }) => {
    try { return await fetchAdmin(signal) } catch (error) { if (error instanceof ApiError && error.status === 401) return null; throw error }
  } })
  const authorized = !!auth.data && !auth.data.must_change_password, enabled = online && authorized && validPath && !transitioning
  const record = useQuery<PurchaseCorrectionSource | null>({ queryKey: ['purchases', 'line-correction', id, lineID, targetID], enabled, staleTime: 0, refetchOnMount: 'always', queryFn: async ({ signal }) => {
    try { return await readPurchaseCorrection(id, lineID!, targetID, signal) } catch (error) { if (error instanceof ApiError && error.status === 404) return null; throw error }
  } })
  const current = record.isFetchedAfterMount && !record.isError ? record.data : undefined
  if (current && current !== remembered) setRemembered(current)
  const source = current ?? remembered
  const sessionLost = useCallback((mandatory: boolean) => {
    setTransitioning(true); clearSessionQueries(client)
    if (!mandatory) client.setQueryData(['auth', 'me'], null)
    navigate(mandatory ? '/settings/account' : '/login', { replace: true, state: { ...(mandatory ? {} : { notice: 'expired' }), returnTo, ...navigationState } })
  }, [client, navigate, returnTo, navigationState])
  const operation = useCorrectionMutation(() => {
    navigate(purchaseCorrectionHistoryRoute(id), { replace: true, state: { ...navigationState, purchaseCorrectionNotice: 'confirmed' } })
  }, sessionLost)
  const errors = [auth.error, record.error]
  const credentialFailure = errors.find((error) => error instanceof ApiError && (error.status === 401 || error.code === 'PASSWORD_CHANGE_REQUIRED')) as ApiError | undefined
  const mandatory = !!auth.data?.must_change_password || credentialFailure?.code === 'PASSWORD_CHANGE_REQUIRED'
  const denied = mandatory || !auth.isFetching && auth.data === null || credentialFailure !== undefined
  const { allowNavigation } = operation
  const pickerSessionLost = useCallback((mandatory: boolean) => { allowNavigation(); sessionLost(mandatory) }, [allowNavigation, sessionLost])
  useEffect(() => {
    if (!online || transitioning || !denied) return
    allowNavigation(); clearSessionQueries(client)
    if (!mandatory) client.setQueryData(['auth', 'me'], null)
    navigate(mandatory ? '/settings/account' : '/login', { replace: true, state: { ...(mandatory ? {} : { notice: 'expired' }), returnTo, ...navigationState } })
  }, [online, transitioning, denied, mandatory, allowNavigation, client, navigate, returnTo, navigationState])
  async function readCurrent() { return Promise.all([auth.refetch(), ...(authorized && validPath ? [record.refetch()] : [])]) }
  function selectTarget(next: string | null) { if (next === targetID) void record.refetch(); else setTargetID(next) }
  const reading = auth.isFetching || record.isFetching, readError = errors.find((error) => error != null)
  const initializing = online && authorized && validPath && !source && !record.isFetchedAfterMount
  const pending = online && (auth.isPending || !authorized && !auth.isError || enabled && record.isPending)
  const fatal = auth.isError && !auth.data ? auth.error : record.isError && !source ? record.error : undefined
  const permitted = online && authorized && !transitioning && !denied && !auth.isError
  const targetMatches = !!current && (current.replacement.kind === 'none' ? targetID === null : current.replacement.item.id === targetID)
  const canWrite = permitted && validPath && !reading && !readError && !!current && targetMatches
  const currentReview = !!operation.command && operation.command.kind === 'correct-purchase' && canWrite && samePurchaseCorrectionSource(operation.command.source, current)
  const readAt = Math.min(auth.dataUpdatedAt, record.dataUpdatedAt)
  return <div className="min-w-0 space-y-7 py-8 md:py-10"><header className="flex min-w-0 flex-wrap items-start justify-between gap-4"><div className="min-w-0 space-y-3"><h1 className="text-2xl leading-snug font-semibold [overflow-wrap:anywhere] sm:text-3xl md:text-4xl">{text.title}</h1><p className="max-w-prose text-base leading-relaxed text-muted-foreground">{text.intro}</p></div><div className="flex max-w-full flex-wrap gap-2">
    <Button asChild variant="outline" className={control}><Link to={purchaseState.purchaseList} state={navigationState}>{purchase.back}</Link></Button>{validPath && <><Button asChild variant="outline" className={control}><Link to={purchaseCorrectionHistoryRoute(id)} state={navigationState}>{text.backHistory}</Link></Button><Button asChild variant="outline" className={control}><Link to={purchaseRoute(id)} state={navigationState}>{purchase.backDetail}</Link></Button></>}
    <Button type="button" variant="outline" disabled={!online || reading || transitioning || !validPath} className={`${control} gap-2`} onClick={() => { void readCurrent() }}><RefreshCw aria-hidden="true" className={`size-4 ${reading ? 'animate-spin motion-reduce:animate-none' : ''}`} />{purchase.refresh}</Button>
  </div></header>
    {!online && <Alert role="status" className="p-4"><WifiOff aria-hidden="true" /><AlertTitle>{text.offline}</AlertTitle><AlertDescription className="text-base">{purchase.offlineHelp}</AlertDescription></Alert>}
    <MutationNotice operation={operation} online={permitted} onReadCurrent={readCurrent} />
    {transitioning || denied || initializing || pending && !source ? <p role="status" className="flex min-h-28 items-center gap-3"><LoaderCircle aria-hidden="true" className="size-6 animate-spin motion-reduce:animate-none" />{auth.isPending || denied || transitioning ? purchase.checking : purchase.loading}</p>
      : !validPath ? <><PurchaseError error={new ApiError('INVALID_PURCHASE_URL')} /><Button asChild variant="outline" className={control}><Link to="/purchases">{messages.catalog.firstPage}</Link></Button></> : fatal ? <PurchaseError error={fatal} />
        : !source ? current === null ? <section className="space-y-3"><h2 className="text-xl font-semibold">{text.missing}</h2><p>{text.missingHelp}</p></section> : <p role="status">{purchase.offlineHelp}</p> : <>
          {readError && <PurchaseError error={readError} />}{current === null && <Alert role="alert" className="p-4"><AlertTitle>{text.missing}</AlertTitle><AlertDescription className="text-base">{text.missingHelp}</AlertDescription></Alert>}
          <Card className="max-w-3xl [--card-spacing:--spacing(6)]"><CardContent className="pt-6"><PurchaseCorrectionEditor data={source} targetID={targetID} operation={operation} online={online} canWrite={canWrite} pickerEnabled={permitted && validPath} reading={reading} onSelectTarget={selectTarget} onSessionLost={pickerSessionLost} /></CardContent></Card>
          {readAt > 0 && <p className="text-sm text-muted-foreground">{purchase.readAt}：{new Intl.DateTimeFormat('zh-TW', { dateStyle: 'short', timeStyle: 'short' }).format(readAt)}</p>}
        </>}
    <PurchaseCorrectionReview operation={operation} online={permitted} current={currentReview} onReadCurrent={readCurrent} />
  </div>
}
