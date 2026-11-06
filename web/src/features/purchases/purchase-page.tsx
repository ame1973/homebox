import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link, useLocation, useNavigate, useParams } from 'react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { LoaderCircle, RefreshCw, WifiOff } from 'lucide-react'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { MutationNotice, mutationControl as control } from '@/components/mutation-review'
import { fetchAdmin } from '@/lib/api/auth'
import { fetchItemsPage } from '@/lib/api/catalog'
import { ApiError } from '@/lib/api/client'
import { pathIDSchema } from '@/lib/api/domain'
import { fetchPurchase, fetchPurchasesPage, type Purchase } from '@/lib/api/purchases'
import { catalogNavigationState, catalogQuery, catalogRoute } from '@/lib/catalog-navigation'
import { messages } from '@/lib/messages'
import { purchaseAllocationRoute, purchaseCursorTrail, purchaseListRoute, purchaseNavigationState, purchaseQuery, purchaseReturnPath, purchaseRoute, purchaseSelectionRoute } from '@/lib/purchase-navigation'
import { useOnline } from '@/lib/use-online'
import { clearSessionQueries } from '@/features/auth/use-auth-action'
import { CatalogFilters } from '@/features/catalog/filters'
import { catalogDisplay } from '@/features/catalog/display'
import { readLocationTree } from '@/features/locations/tree'
import { readCategoryTree } from '@/features/taxonomy/model'
import { AllocationEditor } from './allocation-editor'
import { purchaseCommandCurrent } from './display'
import { readItemPurchases, readPurchaseAllocation, readPurchaseDetail, type ItemPurchases, type PurchaseAllocationContext, type PurchaseDetail } from './model'
import { PurchaseEditor } from './purchase-editor'
import { PurchaseError } from './purchase-error'
import { PurchaseFields, PurchaseItem, PurchaseLines } from './purchase-fields'
import { PurchaseList } from './purchase-list'
import { PurchaseReviewDialog } from './purchase-review-dialog'
import { usePurchaseMutation } from './use-purchase-mutation'

const text = messages.purchases
export type PurchaseMode = 'list' | 'create' | 'detail' | 'edit' | 'select-item' | 'allocate' | 'item'
type View = (PurchaseDetail & { kind: 'detail' }) | (ItemPurchases & { kind: 'item' }) | (Readonly<{ purchase: Purchase }> & { kind: 'selection' }) | PurchaseAllocationContext
export default function PurchasePage({ mode }: { mode: PurchaseMode }) {
  const route = useLocation(), navigate = useNavigate(), client = useQueryClient(), { id = '', itemID = '' } = useParams()
  const online = useOnline(), [transitioning, setTransitioning] = useState(false), [remembered, setRemembered] = useState<View>()
  const selection = mode === 'select-item', filters = mode === 'list' || selection, point = !['list', 'create'].includes(mode)
  const formMode = mode === 'create' || mode === 'edit' || mode === 'allocate'
  const validPath = purchaseReturnPath(route.pathname + route.search) !== undefined
  const query = mode === 'list' ? purchaseQuery(route.search) : selection ? catalogQuery(route.search) : undefined
  const catalogState = useMemo(() => catalogNavigationState(route.state?.catalogList, route.state?.catalogCursors), [route.state?.catalogList, route.state?.catalogCursors])
  const purchaseState = useMemo(() => purchaseNavigationState(mode === 'list' || mode === 'item' ? route.pathname + route.search : route.state?.purchaseList, route.state?.purchaseCursors, mode === 'item' ? id : route.state?.purchaseItem), [mode, route.pathname, route.search, route.state?.purchaseList, route.state?.purchaseCursors, route.state?.purchaseItem, id])
  const navigationState = useMemo(() => ({ ...catalogState, ...purchaseState }), [catalogState, purchaseState])
  const trail = selection ? purchaseCursorTrail(route.state?.selectionCursors) : purchaseState.purchaseCursors
  const returnTo = validPath ? route.pathname + route.search : '/purchases'
  const auth = useQuery({ queryKey: ['auth', 'me'], enabled: online && !transitioning, staleTime: 0, refetchOnMount: 'always', queryFn: async ({ signal }) => {
    try { return await fetchAdmin(signal) } catch (error) { if (error instanceof ApiError && error.status === 401) return null; throw error }
  } })
  const authorized = !!auth.data && !auth.data.must_change_password, enabled = online && authorized && validPath && !transitioning
  const record = useQuery<View | null>({ queryKey: ['purchases', mode === 'item' ? 'item' : mode === 'allocate' ? 'allocation' : selection ? 'selection' : 'detail', id, ...(mode === 'allocate' ? [itemID] : [])], enabled: enabled && point, staleTime: 0, refetchOnMount: 'always', queryFn: async ({ signal }) => {
    try {
      if (mode === 'item') return { kind: 'item' as const, ...await readItemPurchases(id, signal) }
      if (mode === 'allocate') return await readPurchaseAllocation(id, itemID, signal)
      if (selection) return { kind: 'selection' as const, purchase: await fetchPurchase(id, signal) }
      return { kind: 'detail' as const, ...await readPurchaseDetail(id, signal) }
    } catch (error) { if (error instanceof ApiError && error.status === 404) return null; throw error }
  } })
  const categories = useQuery({ queryKey: ['taxonomy', 'categories'], enabled: enabled && filters, staleTime: 0, refetchOnMount: 'always', queryFn: async ({ signal }) => ({ kind: 'categories' as const, value: await readCategoryTree(signal) }) })
  const locations = useQuery({ queryKey: ['locations', 'tree'], enabled: enabled && filters, staleTime: 0, refetchOnMount: 'always', queryFn: ({ signal }) => readLocationTree(signal) })
  const list = useQuery({ queryKey: ['purchases', 'list', query], enabled: enabled && mode === 'list', staleTime: 0, refetchOnMount: 'always', queryFn: ({ signal }) => fetchPurchasesPage(query ?? {}, signal) })
  const items = useQuery({ queryKey: ['catalog', 'items', query], enabled: enabled && selection, staleTime: 0, refetchOnMount: 'always', queryFn: ({ signal }) => fetchItemsPage(query ?? {}, signal) })
  const current = point ? record.data : undefined
  if (current && record.isFetchedAfterMount && !record.isError && current !== remembered) setRemembered(current)
  const metadata = current ?? remembered
  const purchase = metadata && metadata.kind !== 'item' ? metadata.purchase : undefined
  const item = metadata && (metadata.kind === 'item' || metadata.kind === 'individual' || metadata.kind === 'quantity') ? metadata.item : undefined
  const sessionLost = useCallback((mandatory: boolean) => {
    setTransitioning(true); clearSessionQueries(client)
    if (!mandatory) client.setQueryData(['auth', 'me'], null)
    navigate(mandatory ? '/settings/account' : '/login', { replace: true, state: { ...(mandatory ? {} : { notice: 'expired' }), returnTo, ...navigationState, ...(selection ? { selectionCursors: purchaseCursorTrail(route.state?.selectionCursors) } : {}) } })
  }, [client, navigate, returnTo, navigationState, selection, route.state?.selectionCursors])
  const operation = usePurchaseMutation((command, result) => {
    const destination = command.kind === 'create-purchase' ? result.id : command.kind === 'patch-purchase' ? command.source.purchase.id : command.purchase.id
    navigate(purchaseRoute(destination), { replace: true, state: { ...navigationState, purchaseNotice: 'confirmed' } })
  }, sessionLost)
  const errors = [auth.error, ...(point ? [record.error] : []), ...(filters ? [categories.error, locations.error, selection ? items.error : list.error] : [])]
  const credentialFailure = errors.find((error) => error instanceof ApiError && (error.status === 401 || error.code === 'PASSWORD_CHANGE_REQUIRED')) as ApiError | undefined
  const mandatory = !!auth.data?.must_change_password || credentialFailure?.code === 'PASSWORD_CHANGE_REQUIRED'
  const denied = mandatory || !auth.isFetching && auth.data === null || credentialFailure !== undefined
  const { allowNavigation } = operation
  useEffect(() => {
    if (!online || transitioning || !denied) return
    allowNavigation(); clearSessionQueries(client)
    if (!mandatory) client.setQueryData(['auth', 'me'], null)
    navigate(mandatory ? '/settings/account' : '/login', { replace: true, state: { ...(mandatory ? {} : { notice: 'expired' }), returnTo, ...navigationState, ...(selection ? { selectionCursors: purchaseCursorTrail(route.state?.selectionCursors) } : {}) } })
  }, [online, transitioning, denied, mandatory, allowNavigation, client, navigate, returnTo, navigationState, selection, route.state?.selectionCursors])
  async function readCurrent() {
    return Promise.all([auth.refetch(), ...(authorized && validPath ? [...(point ? [record.refetch()] : []), ...(filters ? [categories.refetch(), locations.refetch(), selection ? items.refetch() : list.refetch()] : [])] : [])])
  }
  const reading = auth.isFetching || point && record.isFetching || filters && (categories.isFetching || locations.isFetching || (selection ? items.isFetching : list.isFetching))
  const readError = errors.find((error) => error != null)
  const initializing = formMode && point && !record.isFetchedAfterMount
  const pending = online && (auth.isPending || !authorized && !auth.isError || enabled && (point && record.isPending || filters && (categories.isPending || locations.isPending || (selection ? items.isPending : list.isPending))))
  const fatal = auth.isError && !auth.data ? auth.error : point && record.isError && !metadata ? record.error : filters && (!categories.data && categories.isError || !locations.data && locations.isError || selection && !items.data && items.isError || !selection && !list.data && list.isError) ? readError : undefined
  const permitted = online && authorized && !transitioning && !denied && !auth.isError
  const inconsistent = selection && !!categories.data && items.data?.data.some((item) => item.category_id !== null && !categories.data.value.byID.has(item.category_id))
  const canWrite = permitted && validPath && !reading && !readError && !inconsistent && (!point || !!current)
  const detail = metadata?.kind === 'detail' ? metadata : undefined
  const allocation = metadata?.kind === 'quantity' || metadata?.kind === 'individual' ? metadata : undefined
  const currentReview = !!operation.command && !reading && !readError && purchaseCommandCurrent(operation.command, current?.kind === 'detail' ? current : undefined, current?.kind === 'quantity' || current?.kind === 'individual' ? current : undefined)
  const pageData = selection ? items.data : list.data
  const invalidContinuation = pageData?.next_cursor != null && trail.includes(pageData.next_cursor)
  const readAt = Math.min(auth.dataUpdatedAt, ...(point ? [record.dataUpdatedAt] : []), ...(filters ? [categories.dataUpdatedAt, locations.dataUpdatedAt, selection ? items.dataUpdatedAt : list.dataUpdatedAt] : []))
  const title = mode === 'list' ? text.title : mode === 'create' ? text.create : mode === 'edit' ? text.edit : mode === 'item' ? text.itemPurchases : selection ? text.selectItem : mode === 'allocate' ? text.allocate : text.detail
  function page(cursor: string | null, cursors: (string | null)[]) {
    if (!query) return
    const { cursor: _previous, ...scope } = query
    const next = { ...scope, ...(cursor ? { cursor } : {}) }
    navigate(selection ? purchaseSelectionRoute(id, next) : purchaseListRoute(next), { state: { ...navigationState, ...(selection ? { selectionCursors: cursors } : { purchaseCursors: cursors }) } })
  }
  return <div className="min-w-0 space-y-7 py-8 md:py-10">
    <header className="flex min-w-0 flex-wrap items-start justify-between gap-4"><div className="min-w-0 space-y-3"><h1 className="text-3xl leading-snug font-semibold [overflow-wrap:anywhere] md:text-4xl">{title}</h1><p className="max-w-prose text-base leading-relaxed text-muted-foreground">{text.intro}</p></div><div className="flex max-w-full flex-wrap gap-2">
      {mode === 'list' || mode === 'item' ? canWrite && !invalidContinuation ? <Button asChild className={control}><Link to="/purchases/new" state={navigationState}>{text.create}</Link></Button> : <Button disabled className={control}>{text.create}</Button> : <Button asChild variant="outline" className={control}><Link to={purchaseState.purchaseList} state={navigationState}>{text.back}</Link></Button>}
      {mode === 'item' && <Button asChild variant="outline" className={control}><Link to="/purchases" state={navigationState}>{text.all}</Link></Button>}
      {purchase && mode !== 'detail' && <Button asChild variant="outline" className={control}><Link to={purchaseRoute(purchase.id)} state={navigationState}>{text.backDetail}</Link></Button>}
      {item && <><Button asChild variant="outline" className={control}><Link to={catalogRoute(item.id)} state={navigationState}>{messages.catalog.backDetail}</Link></Button><Button asChild variant="outline" className={control}><Link to={catalogState.catalogList} state={{ catalogCursors: catalogState.catalogCursors }}>{messages.catalog.back}</Link></Button></>}
      <Button type="button" variant="outline" disabled={!online || reading || transitioning || !validPath} className={`${control} gap-2`} onClick={() => { void readCurrent() }}><RefreshCw aria-hidden="true" className={`size-4 ${reading ? 'animate-spin motion-reduce:animate-none' : ''}`} />{text.refresh}</Button>
    </div></header>
    {!online && <Alert role="status" className="p-4"><WifiOff aria-hidden="true" /><AlertTitle>{text.offline}</AlertTitle><AlertDescription className="text-base">{text.offlineHelp}</AlertDescription></Alert>}
    {route.state?.purchaseNotice === 'confirmed' && <Alert role="status" className="p-4"><AlertDescription className="text-base">{text.done}</AlertDescription></Alert>}
    <MutationNotice operation={operation} online={permitted} onReadCurrent={readCurrent} />
    {transitioning || denied || initializing || pending && (point ? !metadata : filters ? !categories.data || !locations.data || !pageData : true) ? <p role="status" className="flex min-h-28 items-center gap-3"><LoaderCircle aria-hidden="true" className="size-6 animate-spin motion-reduce:animate-none" />{auth.isPending || denied || transitioning ? text.checking : text.loading}</p>
      : !validPath ? <><PurchaseError error={new ApiError('INVALID_PURCHASE_URL')} /><Button asChild variant="outline" className={control}><Link to="/purchases">{messages.catalog.firstPage}</Link></Button></> : fatal ? <PurchaseError error={fatal} />
        : point && !metadata ? current === null ? <section className="space-y-3"><h2 className="text-xl font-semibold">{text.missing}</h2><p>{text.missingHelp}</p></section> : <p role="status">{text.offlineHelp}</p> : <>
          {readError && <PurchaseError error={readError} />}{inconsistent && <PurchaseError error={new ApiError('INVALID_RESPONSE')} />}
          {point && current === null && metadata && <Alert role="alert" className="p-4"><AlertTitle>{text.missing}</AlertTitle><AlertDescription className="text-base">{text.missingHelp}</AlertDescription></Alert>}
          {filters && query && categories.data && locations.data && <>
            {selection && <p className="max-w-prose text-base">{text.selectionHelp}</p>}<CatalogFilters domain={selection ? 'catalog' : 'purchases'} query={query} categories={categories.data.value} locations={locations.data} disabled={!permitted || reading} onApply={(values) => navigate(selection ? purchaseSelectionRoute(id, values) : purchaseListRoute(values), { state: navigationState })} />
            {invalidContinuation ? <PurchaseError error={new ApiError('INVALID_RESPONSE')} /> : pageData && <>
              {selection && items.data ? <><p className="text-sm text-muted-foreground">{messages.catalog.pageCount}{items.data.data.length}</p>{!items.data.data.length ? <p role="status">{messages.catalog.empty}</p> : <ul className="divide-y rounded-lg border bg-card">{items.data.data.map((item) => <li key={item.id} className="min-w-0 space-y-3 p-5 [content-visibility:auto] [contain-intrinsic-size:auto_12rem]"><h2 className="whitespace-pre-wrap text-lg font-semibold [overflow-wrap:anywhere]">{JSON.stringify(item.name)}</h2><p className="text-base">{catalogDisplay('tracking_mode', item.tracking_mode)}；{catalogDisplay('status', item.status)}</p><p className="text-sm text-muted-foreground [overflow-wrap:anywhere]">{item.id}</p>{canWrite && pathIDSchema.safeParse(item.id).success ? <Button asChild variant="outline" className={control}><Link to={purchaseAllocationRoute(id, item.id)} state={navigationState}>{text.chooseItem}</Link></Button> : <Button disabled className={control}>{text.chooseItem}</Button>}</li>)}</ul>}</> : list.data && <PurchaseList purchases={list.data.data} navigationState={navigationState} />}
              <nav aria-label={selection ? text.selectionPagination : text.pagination} className="flex flex-wrap gap-2"><Button type="button" variant="outline" disabled={!permitted || reading || !trail.length} className={control} onClick={() => page(trail.at(-1) ?? null, trail.slice(0, -1))}>{messages.catalog.previousPage}</Button><Button type="button" variant="outline" disabled={!permitted || reading || !query.cursor} className={control} onClick={() => page(null, [])}>{messages.catalog.firstPage}</Button><Button type="button" variant="outline" disabled={!permitted || reading || !pageData.next_cursor} className={control} onClick={() => page(pageData.next_cursor, [...trail, query.cursor ?? null])}>{messages.catalog.nextPage}</Button></nav>
            </>}
          </>}
          {mode === 'item' && metadata?.kind === 'item' && <><PurchaseItem item={metadata.item} /><PurchaseList purchases={metadata.purchases} navigationState={navigationState} associated /></>}
          {mode === 'detail' && detail && <section className="min-w-0 space-y-6"><div className="flex flex-wrap gap-2">{canWrite ? <><Button asChild className={control}><Link to={purchaseState.purchaseItem ? purchaseAllocationRoute(detail.purchase.id, purchaseState.purchaseItem) : purchaseSelectionRoute(detail.purchase.id)} state={navigationState}>{text.allocate}</Link></Button><Button asChild variant="outline" className={control}><Link to={purchaseRoute(detail.purchase.id, 'edit')} state={navigationState}>{text.edit}</Link></Button></> : <><Button disabled className={control}>{text.allocate}</Button><Button disabled variant="outline" className={control}>{text.edit}</Button></>}</div><h2 className="text-xl font-semibold">{text.header}</h2><PurchaseFields purchase={detail.purchase} /><p className="text-sm text-muted-foreground [overflow-wrap:anywhere]">{text.purchaseID}：{detail.purchase.id}；{text.version}：{detail.purchase.revision}</p><PurchaseLines lines={detail.lines} currency={detail.purchase.currency} navigationState={navigationState} /></section>}
          {(mode === 'create' || mode === 'edit' && detail) && <Card className="max-w-3xl [--card-spacing:--spacing(6)]"><CardContent className="pt-6"><PurchaseEditor data={mode === 'edit' ? detail : undefined} operation={operation} online={online} canWrite={canWrite} /></CardContent></Card>}
          {mode === 'allocate' && allocation && <Card className="max-w-3xl [--card-spacing:--spacing(6)]"><CardContent className="pt-6"><AllocationEditor data={allocation} operation={operation} online={online} canWrite={canWrite} /></CardContent></Card>}
          <p className="max-w-prose text-sm leading-relaxed text-muted-foreground">{text.future}</p>
          {readAt > 0 && <p className="text-sm text-muted-foreground">{text.readAt}：{new Intl.DateTimeFormat('zh-TW', { dateStyle: 'short', timeStyle: 'short' }).format(readAt)}</p>}
        </>}
    <PurchaseReviewDialog operation={operation} online={permitted} current={currentReview} onReadCurrent={readCurrent} />
  </div>
}
