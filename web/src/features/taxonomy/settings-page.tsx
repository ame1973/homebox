import { useCallback, useEffect, useState } from 'react'
import { Link, NavLink, useLocation, useNavigate, useParams, useSearchParams } from 'react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { LoaderCircle, Plus, RefreshCw, WifiOff } from 'lucide-react'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { MutationNotice, mutationControl as control } from '@/components/mutation-review'
import { ApiError } from '@/lib/api/client'
import { fetchAdmin } from '@/lib/api/auth'
import type { Category, Tag } from '@/lib/api/taxonomy'
import { messages } from '@/lib/messages'
import { settingsReturnPath, taxonomyCreateRoute, taxonomyListRoute, type TaxonomyKind } from '@/lib/settings-navigation'
import { useOnline } from '@/lib/use-online'
import { clearSessionQueries } from '@/features/auth/use-auth-action'
import { version } from '../../../package.json'
import { TaxonomyEditor } from './editor'
import { TaxonomyList } from './list'
import { readCategoryTree, readTags } from './model'
import { TaxonomyReviewDialog } from './review-dialog'
import { TaxonomyError } from './taxonomy-error'
import { useTaxonomyMutation } from './use-taxonomy-mutation'
import type { TaxonomyCommand } from './commands'

const text = messages.taxonomy
const settings = messages.settings
type Mode = 'home' | 'list' | 'create' | 'edit'

function SettingsNavigation() {
  return <nav aria-label={settings.navigation} className="flex max-w-full flex-wrap gap-2">
    {[[settings.overview, '/settings'], [settings.categories, '/settings/categories'], [settings.tags, '/settings/tags'], [settings.account, '/settings/account']].map(([label, path]) =>
      <Button key={path} asChild variant="outline" className={`${control} aria-[current=page]:border-primary aria-[current=page]:bg-muted`}><NavLink to={path!} end={path === '/settings'}>{label}</NavLink></Button>)}
  </nav>
}

function SettingsOverview() {
  return <div className="max-w-3xl space-y-7"><ul className="divide-y rounded-lg border bg-card">
    {[[settings.categories, settings.categoryHelp, '/settings/categories'], [settings.tags, settings.tagHelp, '/settings/tags'], [settings.account, settings.accountHelp, '/settings/account']].map(([label, help, path]) =>
      <li key={path} className="p-5"><Link to={path!} className="block min-h-12 space-y-2 rounded-md py-2 focus-visible:outline-2 focus-visible:outline-ring"><span className="block text-xl font-semibold underline-offset-4 hover:underline">{label}</span><span className="block text-base leading-relaxed text-muted-foreground">{help}</span></Link></li>)}
  </ul><section className="space-y-3"><h2 className="text-xl font-semibold">{settings.appearance}</h2><p className="text-base leading-relaxed text-muted-foreground">{settings.appearanceHelp}</p><p className="text-sm text-muted-foreground">{settings.clientVersion}：{version}</p></section></div>
}

export default function SettingsPage({ kind = 'categories', mode }: { kind?: TaxonomyKind; mode: Mode }) {
  const route = useLocation()
  const navigate = useNavigate()
  const client = useQueryClient()
  const { id = '' } = useParams()
  const [search] = useSearchParams()
  const online = useOnline()
  const [transitioning, setTransitioning] = useState(false)
  const [remembered, setRemembered] = useState<Category | Tag>()
  const needData = mode !== 'home'
  const returnTo = settingsReturnPath(route.pathname + route.search) ?? (mode === 'home' ? '/settings' : taxonomyListRoute(kind))
  const validPath = settingsReturnPath(route.pathname + route.search) !== undefined
  const auth = useQuery({ queryKey: ['auth', 'me'], enabled: online && !transitioning, staleTime: 0, refetchOnMount: 'always', queryFn: async ({ signal }) => {
    try { return await fetchAdmin(signal) } catch (error) { if (error instanceof ApiError && error.status === 401) return null; throw error }
  } })
  const authorized = !!auth.data && !auth.data.must_change_password
  const data = useQuery({ queryKey: ['taxonomy', kind], enabled: online && authorized && needData && validPath && !transitioning, staleTime: 0, refetchOnMount: 'always', queryFn: async ({ signal }) =>
    kind === 'categories' ? { kind: 'categories' as const, value: await readCategoryTree(signal) } : { kind: 'tags' as const, value: await readTags(signal) } })
  const tree = data.data?.kind === 'categories' ? data.data.value : undefined
  const tags = data.data?.kind === 'tags' ? data.data.value : undefined
  const collection = tree ?? tags
  const current = mode === 'edit' ? collection?.byID.get(id) : undefined
  // Preserve volatile form context if a live read removes the target, including
  // a committed deletion with a lost reply. Never treat it as current authority.
  if (current && current !== remembered) setRemembered(current)
  const subject = current ?? remembered
  const removed = mode === 'edit' && collection !== undefined && current === undefined && remembered !== undefined

  const sessionLost = useCallback((mandatory: boolean) => {
    setTransitioning(true)
    clearSessionQueries(client)
    if (!mandatory) client.setQueryData(['auth', 'me'], null)
    navigate(mandatory ? '/settings/account' : '/login', { replace: true, state: { ...(mandatory ? {} : { notice: 'expired' }), returnTo } })
  }, [client, navigate, returnTo])
  const confirmed = useCallback((command: TaxonomyCommand) => {
    navigate(taxonomyListRoute(command.kind.endsWith('category') ? 'categories' : 'tags'), { replace: true, state: { taxonomyNotice: 'confirmed' } })
  }, [navigate])
  const operation = useTaxonomyMutation(confirmed, sessionLost)
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
  async function readCurrent() {
    return Promise.all([auth.refetch(), ...(needData ? [data.refetch()] : [])])
  }
  const isReading = auth.isFetching || needData && data.isFetching
  const pending = transitioning || denied || online && (auth.isPending || !authorized && !auth.isError || authorized && needData && validPath && data.isPending)
  const fatal = auth.isError && !auth.data ? auth.error : needData && data.isError && !collection ? data.error : undefined
  const missing = !validPath || mode === 'edit' && collection !== undefined && subject === undefined
  const canWrite = online && authorized && !transitioning && !denied && !isReading && !auth.isError && !data.isError && !!collection && !removed && validPath
  const createLabel = kind === 'categories' ? text.createCategory : text.createTag
  const title = mode === 'home' ? settings.title : mode === 'list' ? settings[kind] : mode === 'create' ? createLabel : kind === 'categories' ? text.editCategory : text.editTag
  const readError = auth.error ?? (needData ? data.error : undefined)
  const permitted = online && authorized && !transitioning && !denied && !auth.isError

  return <div className="min-w-0 space-y-7 py-8 md:py-10">
    <SettingsNavigation />
    <header className="flex min-w-0 flex-wrap items-start justify-between gap-4"><div className="min-w-0 space-y-3"><h1 className="text-3xl leading-snug font-semibold [overflow-wrap:anywhere] md:text-4xl">{title}</h1>
      <p className="max-w-prose text-base leading-relaxed text-muted-foreground">{mode === 'home' ? settings.intro : kind === 'categories' ? text.categoryIntro : text.tagIntro}</p>
    </div><div className="flex max-w-full flex-wrap gap-2">
      {mode === 'list' && (canWrite ? <Button asChild className={`${control} gap-2`}><Link to={taxonomyCreateRoute(kind)}><Plus aria-hidden="true" />{createLabel}</Link></Button> : <Button disabled className={control}>{createLabel}</Button>)}
      {(mode === 'create' || mode === 'edit') && <Button asChild variant="outline" className={control}><Link to={taxonomyListRoute(kind)}>{kind === 'categories' ? text.backCategories : text.backTags}</Link></Button>}
      <Button type="button" variant="outline" disabled={!online || isReading || transitioning} className={`${control} gap-2`} onClick={() => { void readCurrent() }}><RefreshCw className={`size-4 ${isReading ? 'animate-spin motion-reduce:animate-none' : ''}`} aria-hidden="true" />{text.refresh}</Button>
    </div></header>
    {!online && <Alert role="status" className="p-4"><WifiOff aria-hidden="true" /><AlertTitle>{text.offline}</AlertTitle><AlertDescription className="text-base leading-relaxed">{text.offlineHelp}</AlertDescription></Alert>}
    {route.state?.taxonomyNotice === 'confirmed' && <Alert role="status" className="p-4"><AlertDescription className="text-base">{text.done}</AlertDescription></Alert>}
    <MutationNotice operation={operation} online={permitted} onReadCurrent={readCurrent} />
    {transitioning || denied || pending && (mode === 'home' || !collection) ? <p role="status" className="flex min-h-28 items-center gap-3 text-base"><LoaderCircle className="size-6 animate-spin motion-reduce:animate-none" aria-hidden="true" />{auth.isPending || denied || transitioning ? text.checking : text.loading}</p>
      : fatal ? <TaxonomyError error={fatal} />
        : mode === 'home' ? <SettingsOverview />
          : missing ? <section className="space-y-4 py-8"><h2 className="text-xl font-semibold">{text.missing}</h2><p className="text-base leading-relaxed text-muted-foreground">{text.missingHelp}</p></section>
            : !collection ? <p role="status" className="text-base leading-relaxed text-muted-foreground">{online ? text.loading : text.offlineHelp}</p>
              : <>
                {readError && <TaxonomyError error={readError} />}
                {removed && <Alert role="alert" className="p-4"><AlertTitle>{text.missing}</AlertTitle><AlertDescription className="text-base leading-relaxed">{text.missingHelp}</AlertDescription></Alert>}
                {mode === 'list' ? collection.byID.size === 0 ? <section className="max-w-2xl space-y-4 rounded-lg border bg-card p-6"><h2 className="text-xl font-semibold">{kind === 'categories' ? text.emptyCategories : text.emptyTags}</h2><p className="text-base leading-relaxed text-muted-foreground">{text.emptyHelp}</p></section>
                  : <TaxonomyList kind={kind} tree={tree} tags={tags} canWrite={canWrite} />
                  : <Card className="max-w-3xl [--card-spacing:--spacing(6)]"><CardContent className="pt-6"><TaxonomyEditor kind={kind} mode={mode === 'create' ? 'create' : 'edit'} subject={subject} tree={tree} parentID={mode === 'create' ? search.get('parent') ?? '' : ''} operation={operation} online={online} canWrite={canWrite} /></CardContent></Card>}
                <p className="max-w-prose text-sm leading-relaxed text-muted-foreground">{text.future}</p>
                {data.dataUpdatedAt > 0 && <p className="text-sm text-muted-foreground">{text.readAt}：{new Intl.DateTimeFormat('zh-TW', { dateStyle: 'short', timeStyle: 'short' }).format(data.dataUpdatedAt)}</p>}
              </>}
    <TaxonomyReviewDialog operation={operation} online={permitted} onReadCurrent={readCurrent} />
  </div>
}
