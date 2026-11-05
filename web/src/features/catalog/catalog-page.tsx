import { useCallback, useEffect, useState } from 'react'
import { Link, useLocation, useNavigate, useParams } from 'react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { LoaderCircle, Plus, RefreshCw, WifiOff } from 'lucide-react'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { MutationNotice, mutationControl as control } from '@/components/mutation-review'
import { fetchAdmin } from '@/lib/api/auth'
import { fetchItem, fetchItemsPage, type Item } from '@/lib/api/catalog'
import { ApiError, validText } from '@/lib/api/client'
import { catalogListRoute, catalogQuery, catalogReturnPath, catalogRoute } from '@/lib/catalog-navigation'
import { messages } from '@/lib/messages'
import { useOnline } from '@/lib/use-online'
import { clearSessionQueries } from '@/features/auth/use-auth-action'
import { readCategoryTree } from '@/features/taxonomy/model'
import { readLocationTree } from '@/features/locations/tree'
import { CatalogActions } from './actions'
import { CatalogError } from './catalog-error'
import { CatalogEditor } from './editor'
import { CatalogFilters } from './filters'
import { CatalogReviewDialog } from './review-dialog'
import { catalogDisplay } from './display'
import { useCatalogMutation } from './use-catalog-mutation'
import type { CatalogCommand, CatalogResult } from './commands'

const text = messages.catalog
type Mode = 'list' | 'detail' | 'create' | 'edit'
export default function CatalogPage({ mode }: { mode: Mode }) {
  const route = useLocation()
  const navigate = useNavigate()
  const client = useQueryClient()
  const { id = '' } = useParams()
  const online = useOnline()
  const [transitioning, setTransitioning] = useState(false)
  const [remembered, setRemembered] = useState<Item>()
  const validPath = catalogReturnPath(route.pathname + route.search) !== undefined
  const query = mode === 'list' ? catalogQuery(route.search) : undefined
  const needRecord = mode === 'edit' || mode === 'detail'
  const candidate: unknown = mode === 'list' ? route.pathname + route.search : route.state?.catalogList
  const listReturn = typeof candidate === 'string' && candidate.split('?')[0] === '/items' && catalogReturnPath(candidate) ? candidate : '/items'
  // ponytail: visited cursor history grows per page; use bidirectional server
  // continuations if unusually deep navigation reaches browser history limits.
  const rawTrail: unknown = route.state?.catalogCursors
  const trail: (string | null)[] = Array.isArray(rawTrail) && rawTrail.every((cursor) => cursor === null || typeof cursor === 'string' && validText(cursor, 1, 2048)) ? rawTrail : []
  const navigationState = { catalogList: listReturn, catalogCursors: trail }
  const returnTo = validPath ? route.pathname + route.search : '/items'
  const auth = useQuery({ queryKey: ['auth', 'me'], enabled: online && !transitioning, staleTime: 0, refetchOnMount: 'always', queryFn: async ({ signal }) => {
    try { return await fetchAdmin(signal) } catch (error) { if (error instanceof ApiError && error.status === 401) return null; throw error }
  } })
  const authorized = !!auth.data && !auth.data.must_change_password
  const enabled = online && authorized && validPath && !transitioning
  // Share the existing taxonomy/location cache shapes, not a parallel replica.
  const categories = useQuery({ queryKey: ['taxonomy', 'categories'], enabled, staleTime: 0, refetchOnMount: 'always', queryFn: async ({ signal }) => ({ kind: 'categories' as const, value: await readCategoryTree(signal) }) })
  const locations = useQuery({ queryKey: ['locations', 'tree'], enabled: enabled && mode === 'list', staleTime: 0, refetchOnMount: 'always', queryFn: ({ signal }) => readLocationTree(signal) })
  const record = useQuery({ queryKey: ['catalog', 'item', id], enabled: enabled && needRecord, staleTime: 0, refetchOnMount: 'always', queryFn: async ({ signal }) => {
    try { return await fetchItem(id, signal) } catch (error) { if (error instanceof ApiError && error.status === 404) return null; throw error }
  } })
  const list = useQuery({ queryKey: ['catalog', 'items', query], enabled: enabled && mode === 'list', staleTime: 0, refetchOnMount: 'always', queryFn: ({ signal }) => fetchItemsPage(query ?? {}, signal) })
  const tree = categories.data?.value
  const current = needRecord ? record.data : undefined
  if (current && current !== remembered) setRemembered(current)
  const subject = current ?? remembered
  const removed = needRecord && current === null && remembered !== undefined
  const sessionLost = useCallback((mandatory: boolean) => {
    setTransitioning(true)
    clearSessionQueries(client)
    if (!mandatory) client.setQueryData(['auth', 'me'], null)
    navigate(mandatory ? '/settings/account' : '/login', { replace: true, state: { ...(mandatory ? {} : { notice: 'expired' }), returnTo } })
  }, [client, navigate, returnTo])
  function confirmed(command: CatalogCommand, result: CatalogResult) {
    const target = command.kind === 'delete' ? listReturn : command.kind === 'create' && 'id' in result ? catalogRoute(result.id) : command.kind !== 'create' ? catalogRoute(command.subject.id) : listReturn
    navigate(target, { replace: true, state: { catalogNotice: 'confirmed', catalogList: listReturn, catalogCursors: trail } })
  }
  const operation = useCatalogMutation(confirmed, sessionLost)
  const errors = [categories.error, ...(mode === 'list' ? [locations.error, list.error] : needRecord ? [record.error] : [])]
  const credentialFailure = errors.find((error) => error instanceof ApiError && (error.status === 401 || error.code === 'PASSWORD_CHANGE_REQUIRED'))
  const mandatory = !!auth.data?.must_change_password || credentialFailure instanceof ApiError && credentialFailure.code === 'PASSWORD_CHANGE_REQUIRED'
  const denied = mandatory || !auth.isFetching && auth.data === null || credentialFailure !== undefined
  const { allowNavigation } = operation
  useEffect(() => {
    if (!online || transitioning || !denied) return
    allowNavigation()
    clearSessionQueries(client)
    if (!mandatory) client.setQueryData(['auth', 'me'], null)
    navigate(mandatory ? '/settings/account' : '/login', { replace: true, state: { ...(mandatory ? {} : { notice: 'expired' }), returnTo } })
  }, [online, transitioning, denied, mandatory, allowNavigation, client, navigate, returnTo])
  async function readCurrent() {
    return Promise.all([auth.refetch(), ...(validPath ? [categories.refetch(), ...(mode === 'list' ? [locations.refetch(), list.refetch()] : needRecord ? [record.refetch()] : [])] : [])])
  }
  const reading = auth.isFetching || categories.isFetching || mode === 'list' && (locations.isFetching || list.isFetching) || needRecord && record.isFetching
  const pending = transitioning || denied || online && (auth.isPending || !authorized && !auth.isError || enabled && (categories.isPending || mode === 'list' && (locations.isPending || list.isPending) || needRecord && record.isPending))
  const readError = auth.error ?? categories.error ?? (mode === 'list' ? locations.error ?? list.error : needRecord ? record.error : undefined)
  const fatal = auth.isError && !auth.data ? auth.error : categories.isError && !tree ? categories.error : mode === 'list' && (locations.isError && !locations.data || list.isError && !list.data) ? readError : needRecord && record.isError && !subject ? record.error : undefined
  const inconsistent = !!tree && (subject?.category_id != null && !tree.byID.has(subject.category_id) || mode === 'list' && list.data?.data.some((item) => item.category_id !== null && !tree.byID.has(item.category_id)))
  const permitted = online && authorized && !transitioning && !denied && !auth.isError
  const canWrite = permitted && validPath && !reading && !readError && !!tree && !inconsistent && !removed && (!needRecord || !!current)
  const title = mode === 'list' ? text.title : mode === 'create' ? text.createTitle : mode === 'edit' ? text.editTitle : subject?.name ?? text.detail
  const invalidContinuation = list.data?.next_cursor != null && trail.includes(list.data.next_cursor)
  const readAt = Math.min(categories.dataUpdatedAt, ...(mode === 'list' ? [locations.dataUpdatedAt, list.dataUpdatedAt] : needRecord ? [record.dataUpdatedAt] : []))
  function page(cursor: string | null, cursors: (string | null)[]) {
    if (!query) return
    const { cursor: _previous, ...scope } = query
    navigate(catalogListRoute({ ...scope, ...(cursor ? { cursor } : {}) }), { state: { catalogCursors: cursors } })
  }
  return <div className="min-w-0 space-y-7 py-8 md:py-10">
    <header className="flex min-w-0 flex-wrap items-start justify-between gap-4"><div className="min-w-0 space-y-3"><h1 className="text-3xl leading-snug font-semibold [overflow-wrap:anywhere] md:text-4xl">{title}</h1><p className="max-w-prose text-base leading-relaxed text-muted-foreground">{text.intro}</p></div>
      <div className="flex max-w-full flex-wrap gap-2">
        {mode === 'list' ? canWrite && !invalidContinuation ? <Button asChild className={`${control} gap-2`}><Link to="/items/new" state={navigationState}><Plus aria-hidden="true" />{text.create}</Link></Button> : <Button disabled className={control}>{text.create}</Button>
          : <Button asChild variant="outline" className={control}><Link to={listReturn} state={{ catalogCursors: trail }}>{text.back}</Link></Button>}
        {mode === 'edit' && subject && <Button asChild variant="outline" className={control}><Link to={catalogRoute(subject.id)} state={navigationState}>{text.backDetail}</Link></Button>}
        <Button type="button" variant="outline" disabled={!online || reading || transitioning || !validPath} className={`${control} gap-2`} onClick={() => { void readCurrent() }}><RefreshCw aria-hidden="true" className={`size-4 ${reading ? 'animate-spin motion-reduce:animate-none' : ''}`} />{text.refresh}</Button>
      </div>
    </header>
    {!online && <Alert role="status" className="p-4"><WifiOff aria-hidden="true" /><AlertTitle>{text.offline}</AlertTitle><AlertDescription className="text-base">{text.offlineHelp}</AlertDescription></Alert>}
    {route.state?.catalogNotice === 'confirmed' && <Alert role="status" className="p-4"><AlertDescription className="text-base">{text.done}</AlertDescription></Alert>}
    <MutationNotice operation={operation} online={permitted} onReadCurrent={readCurrent} />
    {transitioning || denied || pending && (!tree || needRecord && !subject || mode === 'list' && (!locations.data || !list.data)) ? <p role="status" className="flex min-h-28 items-center gap-3"><LoaderCircle aria-hidden="true" className="size-6 animate-spin motion-reduce:animate-none" />{auth.isPending || denied || transitioning ? text.checking : text.loading}</p>
      : !validPath ? <><CatalogError error={new ApiError('INVALID_QUERY')} /><Button asChild variant="outline" className={control}><Link to="/items">{text.firstPage}</Link></Button></>
        : fatal ? <CatalogError error={fatal} />
          : needRecord && current === null && !subject ? <section className="space-y-3"><h2 className="text-xl font-semibold">{text.missing}</h2><p>{text.missingHelp}</p></section>
            : !tree ? <p role="status">{text.offlineHelp}</p>
              : <>
                {readError && <CatalogError error={readError} />}
                {inconsistent && <CatalogError error={new ApiError('INVALID_RESPONSE')} />}
                {removed && <Alert role="alert" className="p-4"><AlertTitle>{text.missing}</AlertTitle><AlertDescription className="text-base">{text.missingHelp}</AlertDescription></Alert>}
                {mode === 'list' && query && locations.data && <>
                  <CatalogFilters query={query} categories={tree} locations={locations.data} disabled={!permitted || reading} onApply={(values) => navigate(catalogListRoute(values))} />
                  {invalidContinuation ? <CatalogError error={new ApiError('INVALID_RESPONSE')} /> : list.data && <>
                    <p className="text-sm text-muted-foreground">{text.pageCount}{list.data.data.length}</p>
                    {list.data.data.length === 0 ? <section className="space-y-3 rounded-lg border p-6"><h2 className="text-xl font-semibold">{text.empty}</h2><p className="text-base leading-relaxed text-muted-foreground">{text.emptyHelp}</p></section>
                      : <ul className="divide-y rounded-lg border bg-card">{list.data.data.map((item) => <li key={item.id} className="min-w-0 p-5 [content-visibility:auto] [contain-intrinsic-size:auto_9rem]">
                        <Link to={catalogRoute(item.id)} state={navigationState} className="block min-h-12 rounded-sm py-2 focus-visible:outline-2 focus-visible:outline-ring"><span className="block whitespace-pre-wrap text-lg font-semibold underline-offset-4 hover:underline [overflow-wrap:anywhere]">{item.name}</span><span className="mt-2 block text-base text-muted-foreground [overflow-wrap:anywhere]">{catalogDisplay('tracking_mode', item.tracking_mode)}；{catalogDisplay('status', item.status)}<br />{text.identifier}：{item.id}</span></Link>
                      </li>)}</ul>}
                    <nav aria-label={text.pagination} className="flex flex-wrap gap-2"><Button type="button" variant="outline" disabled={!permitted || reading || !trail.length} className={control} onClick={() => page(trail.at(-1) ?? null, trail.slice(0, -1))}>{text.previousPage}</Button><Button type="button" variant="outline" disabled={!permitted || reading || !query.cursor} className={control} onClick={() => page(null, [])}>{text.firstPage}</Button><Button type="button" variant="outline" disabled={!permitted || reading || !list.data.next_cursor} className={control} onClick={() => page(list.data!.next_cursor, [...trail, query.cursor ?? null])}>{text.nextPage}</Button></nav>
                  </>}
                </>}
                {(mode === 'create' || mode === 'edit' && subject) && <Card className="max-w-3xl [--card-spacing:--spacing(6)]"><CardContent className="pt-6"><CatalogEditor mode={mode === 'create' ? 'create' : 'edit'} subject={subject} categories={tree} operation={operation} online={online} canWrite={canWrite} /></CardContent></Card>}
                {mode === 'detail' && subject && <section className="min-w-0 max-w-3xl space-y-6"><div className="flex flex-wrap items-center justify-between gap-3"><h2 className="text-xl font-semibold">{text.detail}</h2>{canWrite ? <Button asChild className={control}><Link to={catalogRoute(subject.id, 'edit')} state={navigationState}>{text.edit}</Link></Button> : <Button disabled className={control}>{text.edit}</Button>}</div>
                  <dl className="grid min-w-0 gap-5 sm:grid-cols-2">{Object.entries(text.fields).map(([key, label]) => <div key={key} className="min-w-0 space-y-2"><dt className="text-sm font-medium text-muted-foreground">{label}</dt><dd className="whitespace-pre-wrap text-base [overflow-wrap:anywhere]">{key === 'category_id' && subject.category_id && tree.byID.has(subject.category_id) ? `「${tree.byID.get(subject.category_id)!.name}」 (${subject.category_id})` : catalogDisplay(key, subject[key as keyof typeof text.fields])}</dd></div>)}</dl>
                  <p className="text-sm text-muted-foreground [overflow-wrap:anywhere]">{text.identifier}：{subject.id}<br />{text.version}：{subject.revision}</p><CatalogActions item={subject} operation={operation} online={online} canWrite={canWrite} />
                </section>}
                <p className="max-w-prose text-sm leading-relaxed text-muted-foreground">{text.future}</p>
                {readAt > 0 && <p className="text-sm text-muted-foreground">{text.readAt}：{new Intl.DateTimeFormat('zh-TW', { dateStyle: 'short', timeStyle: 'short' }).format(readAt)}</p>}
              </>}
    <CatalogReviewDialog operation={operation} online={permitted} onReadCurrent={readCurrent} />
  </div>
}
