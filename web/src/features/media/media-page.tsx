import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link, useLocation, useNavigate } from 'react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { ImageOff, LoaderCircle, RefreshCw, WifiOff } from 'lucide-react'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { DraftLeaveGuard } from '@/components/draft-leave-guard'
import { MutationNotice, mutationControl as control } from '@/components/mutation-review'
import { fetchAdmin } from '@/lib/api/auth'
import { ApiError } from '@/lib/api/client'
import { pathIDSchema } from '@/lib/api/domain'
import { catalogNavigationState, catalogRoute } from '@/lib/catalog-navigation'
import { assetNavigationState } from '@/lib/asset-navigation'
import { unitRoute } from '@/lib/holding-navigation'
import { locationReturnPath, locationRoute } from '@/lib/location-navigation'
import { mediaAttachRoute, mediaEditRoute, mediaEditScope, mediaOwnerRoute, mediaOwnerScope, mediaReturnPath, type MediaRouteScope } from '@/lib/media-navigation'
import { messages } from '@/lib/messages'
import { useOnline } from '@/lib/use-online'
import { clearSessionQueries } from '@/features/auth/use-auth-action'
import { mediaScopesConsistent } from './commands'
import { MediaAttachEditor } from './media-attach-editor'
import { MediaError } from './media-error'
import { MediaLinkEditor } from './media-link-editor'
import { MediaReviewDialog } from './media-review-dialog'
import { readMediaMembership, readOwnerMedia, sameMediaLink, sameMediaMembership, sameMediaSource, sameOwnerMedia, type MediaMembership, type OwnerMedia } from './model'
import { useMediaMutation } from './use-media-mutation'

const text = messages.media
function back(scope: MediaRouteScope | undefined) {
  if (!scope || scope.kind === 'item' && !pathIDSchema.safeParse(scope.id).success || scope.kind === 'unit' && !pathIDSchema.safeParse(scope.id).success) {
    return { path: '/items', label: text.backItems }
  }
  if (scope.kind === 'location') {
    const path = pathIDSchema.safeParse(scope.id).success ? locationReturnPath(locationRoute(scope.id)) : undefined
    return path ? { path, label: text.backLocation } : { path: '/locations', label: text.backLocations }
  }
  return scope.kind === 'unit' ? { path: unitRoute(scope.id), label: text.backUnit } : { path: catalogRoute(scope.id), label: text.backItem }
}
function ownerName(source: OwnerMedia) {
  return source.owner.kind === 'unit' ? source.owner.value.asset_code : source.owner.value.name
}

export default function MediaPage({ mode }: { mode: 'list' | 'edit' | 'attach' }) {
  const route = useLocation(), navigate = useNavigate(), client = useQueryClient(), online = useOnline()
  const editing = mode === 'edit' ? mediaEditScope(route.search) : undefined
  const scope = mode === 'edit' ? editing && { kind: editing.kind, id: editing.id } : mediaOwnerScope(route.search)
  const returnTo = mediaReturnPath(route.pathname + route.search), validPath = !!scope && !!returnTo
  const catalogList = route.state?.catalogList, catalogCursors = route.state?.catalogCursors, assetList = route.state?.assetList, assetCursors = route.state?.assetCursors
  const navigationState = useMemo(() => ({ ...catalogNavigationState(catalogList, catalogCursors),
    ...(assetList !== undefined ? assetNavigationState(assetList, assetCursors) : {}) }), [catalogList, catalogCursors, assetList, assetCursors])
  const [transitioning, setTransitioning] = useState(false), [remembered, setRemembered] = useState<OwnerMedia>(), [initialEdit, setInitialEdit] = useState<OwnerMedia>(), [localError, setLocalError] = useState<unknown>(), [selectedMediaID, setSelectedMediaID] = useState<string>()
  const auth = useQuery({ queryKey: ['auth', 'me'], enabled: online && !transitioning, staleTime: 0, refetchOnMount: 'always', queryFn: async ({ signal }) => {
    try { return await fetchAdmin(signal) } catch (error) { if (error instanceof ApiError && error.status === 401) return null; throw error }
  } })
  const authorized = !!auth.data && !auth.data.must_change_password, enabled = online && authorized && validPath && !transitioning
  const data = useQuery<OwnerMedia | null>({ queryKey: ['media', 'owner', scope?.kind, scope?.id], enabled, staleTime: 0, refetchOnMount: 'always', queryFn: async ({ signal }) => {
    try { return await readOwnerMedia(scope!, signal) }
    catch (error) { if (error instanceof ApiError && error.code === 'NOT_FOUND' && (error.status === undefined || error.status === 404)) return null; throw error }
  } })
  const membership = useQuery<MediaMembership | null>({ queryKey: ['media', 'membership', selectedMediaID], enabled: enabled && mode === 'attach' && !!selectedMediaID,
    staleTime: 0, refetchOnMount: 'always', queryFn: async ({ signal }) => {
      try { return await readMediaMembership(selectedMediaID!, signal) }
      catch (error) { if (error instanceof ApiError && error.code === 'NOT_FOUND' && error.status === 404) return null; throw error }
    } })
  const current = data.isFetchedAfterMount && !data.isError ? data.data : undefined
  const currentMembership = membership.isFetchedAfterMount && !membership.isError ? membership.data : undefined
  if (current && current !== remembered) setRemembered(current)
  if (!initialEdit && current && editing && current.links.some((link) => link.id === editing.linkID)) setInitialEdit(current)
  const source = current ?? remembered, missing = current === null
  const missingLink = !!editing && !!current && !current.links.some((link) => link.id === editing.linkID)
  const sessionLost = useCallback((mandatory: boolean) => {
    setTransitioning(true); clearSessionQueries(client)
    if (!mandatory) client.setQueryData(['auth', 'me'], null)
    navigate(mandatory ? '/settings/account' : '/login', { replace: true, state: { ...(mandatory ? {} : { notice: 'expired' }), returnTo: returnTo ?? '/items', ...navigationState } })
  }, [client, navigate, returnTo, navigationState])
  const operation = useMediaMutation((command) => {
    setLocalError(undefined)
    if (command.kind === 'patch-link' || command.kind === 'attach') {
      const owner = command.kind === 'attach' ? command.owner.owner : command.source.owner
      navigate(mediaOwnerRoute({ kind: owner.kind, id: owner.value.id }), { replace: true, state: { ...navigationState, mediaNotice: 'confirmed' } })
    }
  }, sessionLost)
  const errors = [auth.error, data.error, membership.error], credentialFailure = errors.find((error) => error instanceof ApiError && (error.status === 401 || error.code === 'PASSWORD_CHANGE_REQUIRED')) as ApiError | undefined
  const mandatory = !!auth.data?.must_change_password || credentialFailure?.code === 'PASSWORD_CHANGE_REQUIRED'
  const denied = mandatory || !auth.isFetching && auth.data === null || credentialFailure !== undefined
  const { allowNavigation } = operation
  const pickerSessionLost = useCallback((mandatory: boolean) => { allowNavigation(); sessionLost(mandatory) }, [allowNavigation, sessionLost])
  useEffect(() => {
    if (!online || transitioning || !denied) return
    allowNavigation(); clearSessionQueries(client)
    if (!mandatory) client.setQueryData(['auth', 'me'], null)
    navigate(mandatory ? '/settings/account' : '/login', { replace: true, state: { ...(mandatory ? {} : { notice: 'expired' }), returnTo: returnTo ?? '/items', ...navigationState } })
  }, [online, transitioning, denied, mandatory, allowNavigation, client, navigate, returnTo, navigationState])
  async function readCurrent() {
    return Promise.all([auth.refetch(), ...(authorized && validPath ? [data.refetch(), ...(mode === 'attach' && selectedMediaID ? [membership.refetch()] : [])] : [])])
  }
  function lookupMedia(id: string) {
    if (id === selectedMediaID) void readCurrent()
    else { setSelectedMediaID(id); void data.refetch() }
  }
  const reading = auth.isFetching || data.isFetching || membership.isFetching, readError = auth.error ?? data.error ?? membership.error
  const initializing = online && authorized && validPath && !data.isFetchedAfterMount && !source
  const pending = online && (auth.isPending || !authorized && !auth.isError || enabled && data.isPending)
  const fatal = auth.isError && !auth.data ? auth.error : data.isError && !source ? data.error : undefined
  const permitted = online && authorized && !transitioning && !denied && !auth.isError
  const canLookup = permitted && validPath && !auth.isFetching && !data.isFetching && !data.isError && !!current
  const canWrite = permitted && validPath && !reading && !readError && !!current
  const command = operation.command
  const reviewCommand = command && command.kind !== 'attach' ? command : undefined
  const reviewed = reviewCommand?.source.links.find((link) => link.id === reviewCommand.linkID)
  const selected = current?.links.find((link) => link.id === reviewCommand?.linkID)
  const currentReview = !!(canWrite && current && reviewCommand && reviewed && selected && current.owner.kind === reviewCommand.source.owner.kind &&
    sameMediaSource(current.owner.value, reviewCommand.source.owner.value) && sameMediaLink(reviewed, selected))
  const attach = command?.kind === 'attach' ? command : undefined
  const currentAttach = !!(canWrite && current && currentMembership && attach && selectedMediaID === attach.membership.media.id &&
    sameOwnerMedia(current, attach.owner) && sameMediaMembership(currentMembership, attach.membership) && mediaScopesConsistent(current, currentMembership))
  function unlink(id: string) {
    if (!canWrite || !current) return
    try { setLocalError(undefined); operation.review({ kind: 'unlink', source: current, linkID: id }) } catch (error) { setLocalError(error) }
  }
  const destination = back(scope)
  const links = source ? [...source.links].sort((first, second) => first.sort_order - second.sort_order || (first.id < second.id ? -1 : first.id > second.id ? 1 : 0)) : []
  return <div className="min-w-0 space-y-7 py-8 md:py-10">
    <header className="flex min-w-0 flex-wrap items-start justify-between gap-4"><div className="min-w-0 space-y-3"><h1 className="text-3xl leading-snug font-semibold [overflow-wrap:anywhere] md:text-4xl">{mode === 'edit' ? text.editTitle : mode === 'attach' ? text.attachTitle : text.title}</h1><p className="max-w-prose text-base leading-relaxed text-muted-foreground">{mode === 'edit' ? text.editIntro : mode === 'attach' ? text.attachIntro : text.intro}</p></div>
      <div className="flex max-w-full flex-wrap gap-2"><Button asChild variant="outline" className={control}><Link to={mode !== 'list' && scope ? mediaOwnerRoute(scope) : destination.path === '/items' ? navigationState.catalogList : destination.path} state={navigationState}>{mode !== 'list' && scope ? text.backLinks : destination.label}</Link></Button>
        <Button type="button" variant="outline" disabled={!online || reading || transitioning || !validPath} className={`${control} gap-2`} onClick={() => { void readCurrent() }}><RefreshCw aria-hidden="true" className={`size-4 ${reading ? 'animate-spin motion-reduce:animate-none' : ''}`} />{text.refresh}</Button></div>
    </header>
    {!online && <Alert role="status" className="p-4"><WifiOff aria-hidden="true" /><AlertTitle>{text.offline}</AlertTitle><AlertDescription className="text-base">{text.offlineHelp}</AlertDescription></Alert>}
    {route.state?.mediaNotice === 'confirmed' && <Alert role="status" className="p-4"><AlertDescription className="text-base">{text.done}</AlertDescription></Alert>}
    <MutationNotice operation={operation} online={permitted} onReadCurrent={readCurrent} />
    {transitioning || denied || initializing || pending && !source ? <p role="status" className="flex min-h-28 items-center gap-3"><LoaderCircle aria-hidden="true" className="size-6 animate-spin motion-reduce:animate-none" />{auth.isPending || denied || transitioning ? text.checking : text.loading}</p>
      : !validPath ? <MediaError error={new ApiError('INVALID_MEDIA_URL')} /> : fatal ? <MediaError error={fatal} />
        : !source ? missing ? <section className="space-y-3"><h2 className="text-xl font-semibold">{text.missing}</h2><p>{text.missingHelp}</p></section> : <p role="status">{text.offlineHelp}</p>
          : <>
            {readError && <MediaError error={readError} />}{localError !== undefined && <MediaError error={localError} />}
            {missing && <Alert role="alert" className="p-4"><AlertTitle>{text.missing}</AlertTitle><AlertDescription className="text-base">{text.missingHelp}</AlertDescription></Alert>}
            {missingLink && <Alert role="alert" className="p-4"><AlertTitle>{text.linkMissing}</AlertTitle><AlertDescription className="text-base">{text.linkMissingHelp}</AlertDescription></Alert>}
            <section className="min-w-0 space-y-2"><p className="text-sm font-medium text-muted-foreground">{text.owner}：{text.ownerKinds[source.owner.kind]}</p><h2 className="whitespace-pre-wrap text-xl font-semibold [overflow-wrap:anywhere]">{ownerName(source)}</h2><p className="text-sm text-muted-foreground [overflow-wrap:anywhere]">{JSON.stringify(source.owner.value.id)}；{text.version}：{source.owner.value.revision}</p></section>
            {mode === 'edit' ? initialEdit && editing && <MediaLinkEditor initial={initialEdit} current={current} linkID={editing.linkID} operation={operation} online={online} canWrite={canWrite && !missingLink} />
              : mode === 'attach' ? <MediaAttachEditor current={current} membership={currentMembership} selectedID={selectedMediaID} onLookup={lookupMedia} onSessionLost={pickerSessionLost} operation={operation} online={online} canLookup={canLookup} canWrite={canWrite} loading={membership.isFetching} />
              : <div className="grid min-w-0 items-start gap-8 lg:grid-cols-[minmax(0,2fr)_minmax(15rem,1fr)]">
              <section className="min-w-0 space-y-4" aria-labelledby="media-list-title"><div className="flex flex-wrap items-baseline justify-between gap-2"><h2 id="media-list-title" className="text-xl font-semibold">{text.list}</h2><p className="text-sm text-muted-foreground">{text.total}{links.length}</p></div>
                {scope && canWrite && operation.phase !== 'pending' && operation.phase !== 'uncertain' ? <Button asChild variant="outline" className={control}><Link to={mediaAttachRoute(scope)} state={navigationState}>{text.attach}</Link></Button> : <Button type="button" variant="outline" disabled className={control}>{text.attach}</Button>}
                {links.length === 0 ? <div className="space-y-2 border-y py-6"><h3 className="text-lg font-semibold">{text.empty}</h3><p className="text-base text-muted-foreground">{text.emptyHelp}</p></div>
                  : <ul aria-label={text.list} className="divide-y border-y">{links.map((link) => {
                    const editable = !!scope && canWrite && operation.phase !== 'pending' && operation.phase !== 'uncertain'
                    return <li key={link.id} className="min-w-0 space-y-4 py-5 [content-visibility:auto] [contain-intrinsic-size:auto_18rem]"><div className="flex min-w-0 flex-wrap items-start justify-between gap-3"><div className="min-w-0 space-y-2"><p className="text-base font-medium">{link.media.verification === 'verified' ? text.verified : text.unverified}</p>{link.is_cover && <p className="inline-block rounded-md bg-muted px-2 py-1 text-sm font-medium">{text.cover}</p>}</div>
                      <div className="flex flex-wrap gap-2">{editable && scope ? <Button asChild variant="outline" className={control}><Link to={mediaEditRoute({ ...scope, linkID: link.id })} state={navigationState} aria-label={`${text.edit} ${JSON.stringify(link.id)}`}>{text.edit}</Link></Button> : <Button type="button" variant="outline" disabled aria-label={`${text.edit} ${JSON.stringify(link.id)}`} className={control}>{text.edit}</Button>}
                        <Button type="button" variant="outline" aria-label={`${text.remove} ${JSON.stringify(link.id)}`} disabled={!canWrite || operation.phase === 'pending' || operation.phase === 'uncertain'} className={control} onClick={() => unlink(link.id)}>{text.remove}</Button></div></div>
                      <dl className="grid min-w-0 gap-3 sm:grid-cols-2">{[[text.order, String(link.sort_order)], [text.description, JSON.stringify(link.description)], [text.linkID, JSON.stringify(link.id)], [text.mediaID, JSON.stringify(link.media_id)], [text.linkVersion, String(link.revision)], [text.mediaVersion, String(link.media.revision)]]
                        .map(([label, value]) => <div key={label} className="min-w-0 space-y-1"><dt className="text-sm text-muted-foreground">{label}</dt><dd className="whitespace-pre-wrap text-base [overflow-wrap:anywhere]">{value}</dd></div>)}</dl></li>
                  })}</ul>}
              </section>
              <aside className="min-w-0 space-y-3 border-l-4 border-primary pl-4" aria-labelledby="media-storage-title"><ImageOff aria-hidden="true" className="size-6 text-primary" /><h2 id="media-storage-title" className="text-lg font-semibold">{text.storageTitle}</h2><p className="text-base leading-relaxed text-muted-foreground">{text.storageUnavailable}</p><p className="text-sm leading-relaxed text-muted-foreground">{text.verificationHelp}</p></aside>
            </div>}
            {data.dataUpdatedAt > 0 && <p className="text-sm text-muted-foreground">{text.readAt}：{new Intl.DateTimeFormat('zh-TW', { dateStyle: 'short', timeStyle: 'short' }).format(data.dataUpdatedAt)}</p>}
          </>}
    {mode === 'list' && <DraftLeaveGuard labels={text} when={!!operation.command} canLeave={operation.canLeave} onLeave={operation.allowNavigation} onBlocked={operation.suspend} onStay={operation.reopen} />}
    <MediaReviewDialog operation={operation} online={permitted} current={attach ? currentAttach : currentReview} onReadCurrent={readCurrent} />
  </div>
}
