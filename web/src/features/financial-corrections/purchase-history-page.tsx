import { useEffect, useMemo, useState } from 'react'
import { Link, useLocation, useNavigate, useParams } from 'react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { LoaderCircle, RefreshCw, WifiOff } from 'lucide-react'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { mutationControl as control } from '@/components/mutation-review'
import { fetchAdmin } from '@/lib/api/auth'
import { ApiError } from '@/lib/api/client'
import type { PurchaseLine } from '@/lib/api/purchases'
import { catalogNavigationState } from '@/lib/catalog-navigation'
import { messages } from '@/lib/messages'
import { purchaseNavigationState, purchaseReturnPath, purchaseRoute } from '@/lib/purchase-navigation'
import { useOnline } from '@/lib/use-online'
import { clearSessionQueries } from '@/features/auth/use-auth-action'
import { readPurchaseCorrectionHistory, type PurchaseCorrectionHistory } from '@/features/purchases/correction-model'
import { PurchaseError } from '@/features/purchases/purchase-error'
import { PurchaseFields, PurchaseLines } from '@/features/purchases/purchase-fields'

const text = messages.purchaseCorrectionHistory, purchaseText = messages.purchases

function LineDecision({ title, line, currency }: { title: string; line: PurchaseLine; currency: string }) {
  return <section className="min-w-0 space-y-3"><h3 className="text-lg font-semibold">{title}</h3><dl className="grid min-w-0 gap-4 sm:grid-cols-2">
    {([
      [purchaseText.lineID, JSON.stringify(line.id)], [purchaseText.lineVersion, String(line.revision)],
      [purchaseText.item, JSON.stringify(line.item_id)], [purchaseText.unit, line.item_unit_id === null ? purchaseText.noUnit : JSON.stringify(line.item_unit_id)],
      [purchaseText.fields.quantity_base, String(line.quantity_base)], [purchaseText.fields.paid_minor, `${line.paid_minor} ${currency}`],
      [purchaseText.fields.note, JSON.stringify(line.note)],
    ] as const).map(([label, value]) => <div key={label} className="min-w-0 space-y-1"><dt className="text-sm font-medium text-muted-foreground">{label}</dt><dd className="whitespace-pre-wrap text-base tabular-nums [overflow-wrap:anywhere]">{value}</dd></div>)}
  </dl></section>
}

function History({ source }: { source: PurchaseCorrectionHistory }) {
  return <section className="min-w-0 space-y-4"><h2 className="text-xl font-semibold">{text.history}</h2><p className="text-sm text-muted-foreground">{text.historyCount}{source.corrections.length}</p><p className="max-w-prose text-base">{text.order}</p>
    {!source.corrections.length ? <p>{text.noHistory}</p> : <ul className="divide-y rounded-lg border bg-card">{source.corrections.map((entry) => <li key={entry.id} className="min-w-0 p-3 [content-visibility:auto] [contain-intrinsic-size:auto_10rem] sm:p-5">
      <details className="min-w-0 space-y-4"><summary className="min-h-12 cursor-pointer rounded-sm py-3 text-base font-semibold [overflow-wrap:anywhere] focus-visible:outline-2 focus-visible:outline-ring">{entry.action === 'void' ? text.void : text.replace}；{purchaseText.lineID}：{JSON.stringify(entry.before.id)}</summary>
        <div className="min-w-0 space-y-5"><p className="text-sm text-muted-foreground [overflow-wrap:anywhere]">{text.entryID}：{JSON.stringify(entry.id)}<br />{text.entryCreated}：{entry.created_at}</p>
          <p className="whitespace-pre-wrap [overflow-wrap:anywhere]">{text.entryReason}：{JSON.stringify(entry.reason)}</p><LineDecision title={text.before} line={entry.before} currency={source.purchase.currency} />
          {entry.after === null ? <section className="space-y-3"><h3 className="text-lg font-semibold">{text.after}</h3><p>{text.voided}</p></section> : <LineDecision title={text.after} line={entry.after} currency={source.purchase.currency} />}
        </div>
      </details>
    </li>)}</ul>}
  </section>
}

export default function PurchaseCorrectionHistoryPage() {
  const route = useLocation(), navigate = useNavigate(), client = useQueryClient(), { id = '' } = useParams()
  const online = useOnline(), [remembered, setRemembered] = useState<PurchaseCorrectionHistory>()
  const validPath = purchaseReturnPath(route.pathname + route.search) !== undefined
  const catalogState = useMemo(() => catalogNavigationState(route.state?.catalogList, route.state?.catalogCursors), [route.state?.catalogList, route.state?.catalogCursors])
  const purchaseState = useMemo(() => purchaseNavigationState(route.state?.purchaseList, route.state?.purchaseCursors, route.state?.purchaseItem), [route.state?.purchaseList, route.state?.purchaseCursors, route.state?.purchaseItem])
  const navigationState = useMemo(() => ({ ...catalogState, ...purchaseState }), [catalogState, purchaseState])
  const returnTo = validPath ? route.pathname + route.search : '/purchases'
  const auth = useQuery({ queryKey: ['auth', 'me'], enabled: online, staleTime: 0, refetchOnMount: 'always', queryFn: async ({ signal }) => {
    try { return await fetchAdmin(signal) } catch (error) { if (error instanceof ApiError && error.status === 401) return null; throw error }
  } })
  const authorized = !!auth.data && !auth.data.must_change_password, enabled = online && authorized && validPath
  const record = useQuery<PurchaseCorrectionHistory | null>({ queryKey: ['purchases', 'line-corrections', id], enabled, staleTime: 0, refetchOnMount: 'always', queryFn: async ({ signal }) => {
    try { return await readPurchaseCorrectionHistory(id, signal) } catch (error) { if (error instanceof ApiError && error.status === 404) return null; throw error }
  } })
  const current = record.data
  if (current && record.isFetchedAfterMount && !record.isError && current !== remembered) setRemembered(current)
  const source = current ?? remembered
  const errors = [auth.error, record.error]
  const credentialFailure = errors.find((error) => error instanceof ApiError && (error.status === 401 || error.code === 'PASSWORD_CHANGE_REQUIRED')) as ApiError | undefined
  const mandatory = !!auth.data?.must_change_password || credentialFailure?.code === 'PASSWORD_CHANGE_REQUIRED'
  const denied = mandatory || !auth.isFetching && auth.data === null || credentialFailure !== undefined
  useEffect(() => {
    if (!online || !denied) return
    clearSessionQueries(client)
    if (!mandatory) client.setQueryData(['auth', 'me'], null)
    navigate(mandatory ? '/settings/account' : '/login', { replace: true, state: { ...(mandatory ? {} : { notice: 'expired' }), returnTo, ...navigationState } })
  }, [online, denied, mandatory, client, navigate, returnTo, navigationState])
  async function readCurrent() { return Promise.all([auth.refetch(), ...(authorized && validPath ? [record.refetch()] : [])]) }
  const reading = auth.isFetching || record.isFetching, readError = errors.find((error) => error != null)
  const initializing = online && authorized && validPath && !record.isFetchedAfterMount
  const pending = online && (auth.isPending || !authorized && !auth.isError || enabled && record.isPending)
  const fatal = auth.isError && !auth.data ? auth.error : record.isError && !source ? record.error : undefined
  const stale = !online || reading || !!readError || current === null
  const readAt = Math.min(auth.dataUpdatedAt, record.dataUpdatedAt)
  return <div className="min-w-0 space-y-7 py-8 md:py-10"><header className="flex min-w-0 flex-wrap items-start justify-between gap-4"><div className="min-w-0 space-y-3"><h1 className="text-2xl leading-snug font-semibold [overflow-wrap:anywhere] sm:text-3xl md:text-4xl">{text.title}</h1><p className="max-w-prose text-base leading-relaxed text-muted-foreground">{text.intro}</p></div><div className="flex max-w-full flex-wrap gap-2">
    <Button asChild variant="outline" className={control}><Link to={purchaseState.purchaseList} state={navigationState}>{purchaseText.back}</Link></Button>{validPath && <Button asChild variant="outline" className={control}><Link to={purchaseRoute(id)} state={navigationState}>{purchaseText.backDetail}</Link></Button>}
    <Button type="button" variant="outline" disabled={!online || reading || !validPath} className={`${control} gap-2`} onClick={() => { void readCurrent() }}><RefreshCw aria-hidden="true" className={`size-4 ${reading ? 'animate-spin motion-reduce:animate-none' : ''}`} />{purchaseText.refresh}</Button>
  </div></header>
    {!online && <Alert role="status" className="p-4"><WifiOff aria-hidden="true" /><AlertTitle>{text.offline}</AlertTitle><AlertDescription className="text-base">{purchaseText.offlineHelp}</AlertDescription></Alert>}
    {denied || initializing || pending && !source ? <p role="status" className="flex min-h-28 items-center gap-3"><LoaderCircle aria-hidden="true" className="size-6 animate-spin motion-reduce:animate-none" />{auth.isPending || denied ? purchaseText.checking : purchaseText.loading}</p>
      : !validPath ? <><PurchaseError error={new ApiError('INVALID_PURCHASE_URL')} /><Button asChild variant="outline" className={control}><Link to="/purchases">{messages.catalog.firstPage}</Link></Button></> : fatal ? <PurchaseError error={fatal} />
        : !source ? current === null ? <section className="space-y-3"><h2 className="text-xl font-semibold">{text.missing}</h2><p>{text.missingHelp}</p></section> : <p role="status">{purchaseText.offlineHelp}</p> : <>
          {readError && <PurchaseError error={readError} />}{stale && <Alert role="status" className="p-4"><AlertDescription className="text-base">{text.stale}</AlertDescription></Alert>}
          <section className="min-w-0 space-y-5"><h2 className="text-xl font-semibold">{purchaseText.header}</h2><PurchaseFields purchase={source.purchase} /><p className="text-sm text-muted-foreground [overflow-wrap:anywhere]">{purchaseText.purchaseID}：{JSON.stringify(source.purchase.id)}；{purchaseText.version}：{source.purchase.revision}</p></section>
          <PurchaseLines lines={source.lines} currency={source.purchase.currency} navigationState={navigationState} /><History source={source} /><p className="max-w-prose text-sm leading-relaxed text-muted-foreground">{text.readOnly}</p>
          {readAt > 0 && <p className="text-sm text-muted-foreground">{purchaseText.readAt}：{new Intl.DateTimeFormat('zh-TW', { dateStyle: 'short', timeStyle: 'short' }).format(readAt)}</p>}
        </>}
  </div>
}
