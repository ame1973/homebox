import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react'
import { Link, useLocation, useNavigate } from 'react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { LoaderCircle, Search, WifiOff } from 'lucide-react'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { Textarea } from '@/components/ui/textarea'
import { mutationControl as control } from '@/components/mutation-review'
import { fetchAdmin } from '@/lib/api/auth'
import { type BarcodeSearchQuery } from '@/lib/api/barcode-search'
import type { Item } from '@/lib/api/catalog'
import { symbologySchema } from '@/lib/api/catalog-links'
import { ApiError, validText } from '@/lib/api/client'
import { pathIDSchema } from '@/lib/api/domain'
import { catalogRoute } from '@/lib/catalog-navigation'
import { messages } from '@/lib/messages'
import { barcodeJSONInputMax, startBarcodeHandoff } from '@/lib/scan-handoff'
import { subscribeSessionCleared } from '@/lib/session-lifecycle'
import { quoteExactText } from '@/lib/text-literal'
import { useOnline } from '@/lib/use-online'
import { clearSessionQueries } from '@/features/auth/use-auth-action'
import { CatalogError } from '@/features/catalog/catalog-error'
import { catalogDisplay } from '@/features/catalog/display'
import { readBarcodeChoices } from './barcode-model'

const text = messages.scan, catalog = messages.catalog

type Scope = Readonly<{ value: string; symbology?: BarcodeSearchQuery['symbology']; cursor?: string; sequence: number }>
type VisitedPage = Readonly<{ cursor?: string; ids: readonly string[] }>

function itemValue(key: keyof typeof catalog.fields, item: Item): string {
  const value = item[key]
  if (key === 'status' || key === 'tracking_mode') return catalogDisplay(key, value)
  return typeof value === 'string' ? quoteExactText(value) : JSON.stringify(value)
}

function inputValue(raw: string, format: 'plain' | 'json'): string | undefined {
  try {
    const value: unknown = format === 'json' ? JSON.parse(raw) : raw
    return typeof value === 'string' && validText(value, 1, 512) ? value : undefined
  } catch { return undefined }
}

export default function ScanPage() {
  const location = useLocation(), navigate = useNavigate(), client = useQueryClient(), online = useOnline()
  const validRoute = !location.search && !location.hash
  const [raw, setRaw] = useState(''), [format, setFormat] = useState<'plain' | 'json'>('plain'), [symbology, setSymbology] = useState('')
  const [scope, setScope] = useState<Scope>(), [trail, setTrail] = useState<VisitedPage[]>([]), [error, setError] = useState(false)
  const [blocked, setBlocked] = useState(false), [transitioning, setTransitioning] = useState(false)
  const sequence = useRef(0), redirecting = useRef(false), inputRef = useRef<HTMLTextAreaElement>(null)
  const auth = useQuery({ queryKey: ['auth', 'me'], enabled: online && !transitioning && !blocked, staleTime: 0, refetchOnMount: 'always', queryFn: async ({ signal }) => {
    try { return await fetchAdmin(signal) } catch (cause) { if (cause instanceof ApiError && cause.status === 401) return null; throw cause }
  } })
  // A transient auth recheck hides results but must not re-enable a failed
  // lookup: TanStack Query would otherwise retry it without another action.
  const knownSession = !!auth.data && !auth.data.must_change_password && auth.isFetchedAfterMount
  const queryEnabled = online && knownSession && validRoute && !blocked && !transitioning
  const enabled = queryEnabled && !auth.isFetching && !auth.isError
  const page = useQuery({ queryKey: ['catalog', 'barcode-choices', scope], enabled: queryEnabled && !!scope,
    gcTime: 0, staleTime: Infinity, refetchOnMount: false, refetchOnReconnect: false, refetchOnWindowFocus: false,
    queryFn: ({ signal }) => readBarcodeChoices({ value: scope!.value, ...(scope!.symbology ? { symbology: scope!.symbology } : {}), ...(scope!.cursor ? { cursor: scope!.cursor } : {}) }, signal) })
  const credentialFailure = page.error instanceof ApiError && (page.error.status === 401 || page.error.code === 'PASSWORD_CHANGE_REQUIRED') ? page.error : undefined
  const mandatory = !!auth.data?.must_change_password || credentialFailure?.code === 'PASSWORD_CHANGE_REQUIRED'
  const denied = mandatory || !auth.isFetching && auth.data === null || credentialFailure !== undefined

  useEffect(() => subscribeSessionCleared(() => {
    setBlocked(true); setRaw(''); setFormat('plain'); setSymbology(''); setScope(undefined); setTrail([]); setError(false)
    void client.cancelQueries({ queryKey: ['catalog', 'barcode-choices'] })
    client.removeQueries({ queryKey: ['catalog', 'barcode-choices'] })
  }), [client])
  useEffect(() => {
    const offline = () => { setScope(undefined); setTrail([]) }
    window.addEventListener('offline', offline)
    return () => window.removeEventListener('offline', offline)
  }, [])
  const sessionLost = useCallback((requiresPassword: boolean) => {
    if (redirecting.current) return
    redirecting.current = true
    setTransitioning(true)
    clearSessionQueries(client)
    if (!requiresPassword) client.setQueryData(['auth', 'me'], null)
    navigate(requiresPassword ? '/settings/account' : '/login', { replace: true, state: { ...(requiresPassword ? {} : { notice: 'expired' }), returnTo: '/scan' } })
  }, [client, navigate])
  useEffect(() => {
    if (!online || transitioning || blocked || !denied) return
    sessionLost(mandatory)
  }, [online, transitioning, blocked, denied, mandatory, sessionLost])

  function clearResults() {
    void client.cancelQueries({ queryKey: ['catalog', 'barcode-choices'] })
    setScope(undefined); setTrail([]); setError(false)
  }
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!enabled || page.isFetching) return
    const value = inputValue(raw, format), parsed = symbology === '' ? undefined : symbologySchema.safeParse(symbology)
    if (value === undefined || parsed && !parsed.success) { setError(true); inputRef.current?.focus(); return }
    setError(false); setTrail([])
    setScope({ value, ...(parsed?.success ? { symbology: parsed.data } : {}), sequence: ++sequence.current })
  }
  const current = scope && page.isFetchedAfterMount && !page.isError ? page.data : undefined
  const seen = new Set(trail.flatMap((entry) => entry.ids))
  const invalid = !!current && (current.matches.some(({ barcode }) => seen.has(barcode.barcode_id)) ||
    current.next_cursor !== null && (current.next_cursor === scope?.cursor || trail.some((entry) => entry.cursor === current.next_cursor)))
  const canPage = enabled && !!scope && !page.isFetching
  const canChoose = canPage && !!current && !invalid && !page.isError
  function first() { if (canPage && scope) { setTrail([]); setScope({ ...scope, cursor: undefined, sequence: ++sequence.current }) } }
  function previous() {
    if (!canPage || !scope || !trail.length) return
    setScope({ ...scope, cursor: trail.at(-1)?.cursor, sequence: ++sequence.current }); setTrail(trail.slice(0, -1))
  }
  function next() {
    if (!canChoose || !scope || !current?.next_cursor) return
    setTrail([...trail, { cursor: scope.cursor, ids: current.matches.map(({ barcode }) => barcode.barcode_id) }])
    setScope({ ...scope, cursor: current.next_cursor, sequence: ++sequence.current })
  }
  function createItem() {
    if (!canChoose || !scope || scope.cursor || !current || current.matches.length !== 0) return
    const token = startBarcodeHandoff({ rawValue: scope.value, ...(scope.symbology ? { symbology: scope.symbology } : {}) })
    navigate('/items/new', { state: { scanToken: token } })
  }
  const waiting = online && !blocked && (transitioning || denied || auth.isPending || auth.isFetching || !auth.isFetchedAfterMount && !auth.isError)

  return <div className="min-w-0 space-y-7 py-8 md:py-10">
    <header className="min-w-0 space-y-3"><h1 className="text-3xl leading-snug font-semibold md:text-4xl">{text.title}</h1><p className="max-w-prose text-base leading-relaxed text-muted-foreground">{text.intro}</p></header>
    <Alert className="p-4"><AlertDescription className="text-base leading-relaxed">{text.cameraUnavailable}</AlertDescription></Alert>
    {!validRoute ? <Alert role="alert" className="p-4"><AlertDescription className="space-y-3 text-base"><p>{text.invalidRoute}</p><Button asChild variant="outline" className={control}><Link to="/scan">{text.title}</Link></Button></AlertDescription></Alert>
      : blocked ? <Alert role="alert" className="p-4"><AlertTitle>{text.sessionCleared}</AlertTitle><AlertDescription className="space-y-3 text-base"><p>{text.sessionClearedHelp}</p><Button asChild variant="outline" className={control}><Link to="/login" state={{ returnTo: '/scan', notice: 'expired' }}>{text.signIn}</Link></Button></AlertDescription></Alert>
        : waiting ? <p role="status" className="flex min-h-24 items-center gap-3"><LoaderCircle className="size-6 animate-spin motion-reduce:animate-none" aria-hidden="true" />{text.checking}</p>
          : auth.isError && online ? <div className="space-y-4"><CatalogError error={auth.error} /><Button type="button" variant="outline" className={control} onClick={() => { void auth.refetch() }}>{messages.auth.checkAgain}</Button></div>
            : <>
              {!online && <Alert role="status" className="p-4"><WifiOff aria-hidden="true" /><AlertTitle>{text.offline}</AlertTitle><AlertDescription className="text-base">{text.offlineHelp}</AlertDescription></Alert>}
              <form noValidate onSubmit={submit} className="min-w-0 max-w-3xl space-y-5 rounded-lg border p-5 sm:p-6">
                <div className="space-y-2"><label htmlFor="scan-format" className="block text-base font-medium">{text.inputFormat}</label><NativeSelect id="scan-format" value={format} onChange={(event) => { setFormat(event.target.value === 'json' ? 'json' : 'plain'); clearResults() }} className="w-full [&_select]:min-h-12 [&_select]:text-base"><NativeSelectOption value="plain">{text.plain}</NativeSelectOption><NativeSelectOption value="json">{text.json}</NativeSelectOption></NativeSelect><p className="text-sm leading-relaxed text-muted-foreground">{format === 'json' ? text.jsonHelp : text.inputHelp}</p></div>
                <div className="min-w-0 space-y-2"><label htmlFor="scan-value" className="block text-base font-medium">{text.input}</label><Textarea ref={inputRef} id="scan-value" value={raw} onChange={(event) => { setRaw(event.target.value); clearResults() }} rows={2} maxLength={format === 'json' ? barcodeJSONInputMax : 1024} autoComplete="off" spellCheck={false} aria-invalid={error} aria-describedby={error ? 'scan-value-error' : undefined} className="min-h-20 text-base md:text-base" />{error && <p id="scan-value-error" role="alert" className="text-sm text-destructive">{text.invalid}</p>}</div>
                <div className="space-y-2"><label htmlFor="scan-symbology" className="block text-base font-medium">{text.symbology}</label><NativeSelect id="scan-symbology" value={symbology} onChange={(event) => { setSymbology(event.target.value); clearResults() }} className="w-full [&_select]:min-h-12 [&_select]:text-base"><NativeSelectOption value="">{text.allFormats}</NativeSelectOption>{Object.entries(messages.owned.symbologies).map(([value, label]) => <NativeSelectOption key={value} value={value}>{label}</NativeSelectOption>)}</NativeSelect></div>
                <Button type="submit" disabled={!enabled || page.isFetching} className={`${control} w-full gap-2 sm:w-auto`}><Search aria-hidden="true" />{text.search}</Button>
              </form>
              {scope && enabled && <section aria-labelledby="barcode-results" className="min-w-0 max-w-3xl space-y-5" aria-busy={page.isFetching}>
                <h2 id="barcode-results" className="text-2xl font-semibold">{text.results}</h2><p className="text-base leading-relaxed text-muted-foreground">{text.resultsHelp}</p>
                <p className="whitespace-pre-wrap text-sm [overflow-wrap:anywhere]">{text.matchedValue}：{quoteExactText(scope.value)}<br />{text.matchedFormat}：{scope.symbology ? messages.owned.symbologies[scope.symbology] : text.allFormats}</p>
                {current && !invalid && !page.isFetching && <p role="status" className="text-sm text-muted-foreground">{catalog.pageCount}{current.matches.length}</p>}
                {page.isFetching || page.isPending ? <p role="status" className="flex items-center gap-3"><LoaderCircle className="size-5 animate-spin motion-reduce:animate-none" aria-hidden="true" />{text.searching}</p>
                  : page.error ? <CatalogError error={page.error} /> : invalid ? <Alert role="alert" className="p-4"><AlertTitle>{text.failed}</AlertTitle><AlertDescription className="text-base">{text.changed}</AlertDescription></Alert>
                    : current && (current.matches.length === 0 ? scope.cursor ? <section className="space-y-3 rounded-lg border p-5"><h3 className="text-xl font-semibold">{text.emptyPage}</h3><p>{text.emptyPageHelp}</p></section>
                      : <section className="space-y-3 rounded-lg border p-5"><h3 className="text-xl font-semibold">{text.empty}</h3><p>{text.emptyHelp}</p><p className="text-sm text-muted-foreground">{text.createHelp}</p><Button type="button" variant="outline" disabled={!canChoose} className={control} onClick={createItem}>{text.create}</Button></section>
                      : <ul aria-label={text.results} className="divide-y rounded-lg border">{current.matches.map(({ barcode, item }) => {
                        const path = pathIDSchema.safeParse(item.id).success && !item.id.includes('%2F') ? catalogRoute(item.id) : undefined
                        return <li key={barcode.barcode_id} className="min-w-0 space-y-3 p-5"><h3 className="whitespace-pre-wrap text-lg font-semibold [overflow-wrap:anywhere]">{quoteExactText(item.name)}</h3>
                          <p className="text-sm leading-relaxed text-muted-foreground [overflow-wrap:anywhere]">{catalog.fields.status}：{catalogDisplay('status', item.status)}；{catalog.fields.tracking_mode}：{catalogDisplay('tracking_mode', item.tracking_mode)}<br />{text.symbology}：{messages.owned.symbologies[barcode.symbology]}<br />{text.itemID}：{quoteExactText(item.id)}<br />{text.barcodeID}：{quoteExactText(barcode.barcode_id)}</p>
                          <details className="min-w-0"><summary className="min-h-11 cursor-pointer rounded-sm py-3 font-medium focus-visible:outline-2 focus-visible:outline-ring">{text.itemDetails}</summary><dl className="grid min-w-0 gap-3 sm:grid-cols-2">{Object.entries(catalog.fields).map(([key, label]) => <div key={key} className="min-w-0"><dt className="text-sm text-muted-foreground">{label}</dt><dd className="whitespace-pre-wrap [overflow-wrap:anywhere]">{itemValue(key as keyof typeof catalog.fields, item)}</dd></div>)}</dl></details>
                          {path ? <Button asChild variant="outline" className={control}><Link to={path} aria-label={`${text.openItem}：${quoteExactText(item.name)} ${quoteExactText(item.id)}`}>{text.openItem}</Link></Button> : <p className="text-sm leading-relaxed">{text.unavailableItem}</p>}
                        </li>
                      })}</ul>)}
                <nav aria-label={text.pagination} className="flex flex-wrap gap-2"><Button type="button" variant="outline" disabled={!canPage || !trail.length} className={control} onClick={previous}>{catalog.previousPage}</Button><Button type="button" variant="outline" disabled={!canPage || !scope.cursor} className={control} onClick={first}>{text.reset}</Button><Button type="button" variant="outline" disabled={!canChoose || !current?.next_cursor} className={control} onClick={next}>{catalog.nextPage}</Button><Button type="button" variant="outline" disabled={!canPage} className={control} onClick={() => setScope({ ...scope, sequence: ++sequence.current })}>{text.refresh}</Button></nav>
              </section>}
            </>}
  </div>
}
