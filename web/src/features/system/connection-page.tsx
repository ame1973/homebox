import { useQuery } from '@tanstack/react-query'
import { CircleCheck, LoaderCircle, RefreshCw, ShieldCheck, TriangleAlert, WifiOff } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardFooter, CardHeader } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { fetchHealth } from '@/lib/api/client'
import { messages } from '@/lib/messages'

const text = messages.connection

export default function ConnectionPage() {
  const query = useQuery({ queryKey: ['health', 'live'], queryFn: ({ signal }) => fetchHealth(signal) })
  const state = query.isFetching ? 'pending' : query.isError ? 'error' : query.data?.status ?? 'pending'
  const Icon = { pending: LoaderCircle, error: WifiOff, degraded: TriangleAlert, ok: CircleCheck }[state]
  const tone = { pending: 'text-muted-foreground', error: 'text-destructive', degraded: 'text-warning', ok: 'text-success' }[state]
  const help = state === 'ok' ? text.okHelp : state === 'degraded' ? text.degradedHelp : text.errorHelp
  return (
    <div className="grid items-start gap-8 py-8 md:grid-cols-2 md:gap-12 md:py-16">
      <section aria-labelledby="connection-title" className="space-y-5 md:pt-4">
        <h1 id="connection-title" className="text-3xl leading-snug font-semibold tracking-tight md:text-4xl">{text.title}</h1>
        <p className="max-w-prose text-base leading-relaxed text-muted-foreground">{text.introduction}</p>
        <p className="flex max-w-prose items-start gap-3 text-sm leading-relaxed text-muted-foreground">
          <ShieldCheck className="mt-0.5 size-5 shrink-0" aria-hidden="true" />{text.privacy}
        </p>
      </section>
      <Card className="min-h-80 [--card-spacing:--spacing(6)]">
        <CardHeader><h2 className="text-base font-medium">{text.heading}</h2></CardHeader>
        <CardContent className="flex-1 space-y-5">
          <div role="status" aria-live="polite" aria-atomic="true" className="space-y-3">
            <p className={`flex items-center gap-3 text-lg font-semibold ${tone}`}>
              <Icon className={`size-6 shrink-0 ${state === 'pending' ? 'animate-spin motion-reduce:animate-none' : ''}`} aria-hidden="true" />{text[state]}
            </p>
            {state === 'pending' ? (
              <div aria-hidden="true" className="space-y-2 py-1"><Skeleton className="h-4 w-full" /><Skeleton className="h-4 w-3/4" /></div>
            ) : <p className="text-base leading-relaxed text-muted-foreground">{help}</p>}
          </div>
          <Button className="min-h-11 w-full gap-2 text-base focus-visible:ring-ring" disabled={query.isFetching} onClick={() => { void query.refetch() }}>
            <RefreshCw aria-hidden="true" />{query.isFetching ? text.checking : text.retry}
          </Button>
        </CardContent>
        <CardFooter className="text-sm leading-relaxed text-muted-foreground">
          {query.dataUpdatedAt > 0 ? (
            <p>{text.checked}<br /><time dateTime={new Date(query.dataUpdatedAt).toISOString()} className="tabular-nums">{new Date(query.dataUpdatedAt).toLocaleString('zh-TW')}</time></p>
          ) : <p>{text.noResult}</p>}
        </CardFooter>
      </Card>
    </div>
  )
}
