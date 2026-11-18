import { Link } from 'react-router'
import { LoaderCircle, RefreshCw, WifiOff } from 'lucide-react'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { mutationControl as control } from '@/components/mutation-review'
import { fetchSyncStatus, syncProjectionKinds } from '@/lib/api/sync-status'
import { messages } from '@/lib/messages'
import { quoteExactText } from '@/lib/text-literal'
import { useReadOnlyStatus } from './use-read-only-status'

const text = messages.syncStatus
const statusKey = ['system', 'sync-status'] as const
const kindLabels: Record<(typeof syncProjectionKinds)[number], string> = text.kinds
const number = new Intl.NumberFormat('zh-TW')

export default function SyncStatusPage() {
  const { valid, online, blocked, transitioning, fetching, waiting, error, checking, status, refresh } =
    useReadOnlyStatus('/settings/sync-status', statusKey, fetchSyncStatus)
  const value = status.data
  const counts = value ? syncProjectionKinds.filter((kind) => value.counts[kind] !== undefined).map((kind) => [kind, kindLabels[kind]] as const) : []

  return <div className="min-w-0 space-y-7 py-8 md:py-10">
    <header className="flex min-w-0 flex-wrap items-start justify-between gap-4">
      <div className="min-w-0 space-y-3"><h1 className="text-3xl leading-snug font-semibold [overflow-wrap:anywhere] md:text-4xl">{text.title}</h1><p className="max-w-prose text-base leading-relaxed text-muted-foreground">{text.intro}</p></div>
      <div className="flex max-w-full flex-wrap gap-2"><Button asChild variant="outline" className={control}><Link to="/settings">{text.back}</Link></Button>
        <Button type="button" variant="outline" className={`${control} gap-2`} disabled={!valid || !online || blocked || transitioning || fetching} onClick={() => { void refresh() }}><RefreshCw aria-hidden="true" className={`size-4 ${fetching ? 'animate-spin motion-reduce:animate-none' : ''}`} />{text.refresh}</Button></div>
    </header>
    {!valid ? <Alert role="alert" className="p-4"><AlertDescription className="text-base">{text.invalidRoute}</AlertDescription></Alert>
      : blocked ? <Alert role="alert" className="p-4"><AlertDescription className="space-y-3 text-base"><p>{text.sessionCleared}</p><Button asChild variant="outline" className={control}><Link to="/login" state={{ notice: 'expired', returnTo: '/settings/sync-status' }}>{messages.scan.signIn}</Link></Button></AlertDescription></Alert>
        : !online ? <Alert role="status" className="p-4"><WifiOff aria-hidden="true" /><AlertTitle>{text.offline}</AlertTitle><AlertDescription className="text-base">{text.offlineHelp}</AlertDescription></Alert>
          : waiting && !error ? <p role="status" className="flex min-h-24 items-center gap-3 text-base"><LoaderCircle aria-hidden="true" className="size-6 animate-spin motion-reduce:animate-none" />{checking ? text.checking : text.loading}</p>
            : error ? <Alert role="alert" className="p-4"><AlertDescription className="text-base">{text.readError}</AlertDescription></Alert>
              : value && <div className="max-w-3xl space-y-5">
                <Card className="[--card-spacing:--spacing(6)]"><CardContent className="space-y-6 pt-6">
                  <section className="min-w-0 space-y-2"><h2 className="text-xl font-semibold">{text.bootstrap}</h2><p className="text-lg font-medium">{value.bootstrap_ready ? text.bootstrapReady : text.bootstrapUnavailable}</p><p className="text-base leading-relaxed">{text.bootstrapHelp}</p></section>
                  <section className="min-w-0 space-y-2 border-t pt-5"><h2 className="text-xl font-semibold">{text.retention}</h2><p className="text-lg font-medium">{value.retention_enabled ? text.retentionEnabled : text.retentionDisabled}</p><p className="text-base leading-relaxed">{text.retentionHelpBefore} {number.format(value.retention_days)} {text.retentionHelpAfter}</p></section>
                </CardContent></Card>
                <Card className="[--card-spacing:--spacing(6)]"><CardContent className="space-y-5 pt-6">
                  <section className="min-w-0 space-y-4"><h2 className="text-xl font-semibold">{text.positions}</h2><dl className="space-y-4">
                    <div className="min-w-0 space-y-1"><dt className="font-medium">{text.watermark}</dt><dd className="text-xl tabular-nums [overflow-wrap:anywhere]">{number.format(value.high_watermark)}</dd><dd className="text-sm leading-relaxed text-muted-foreground">{text.watermarkHelp}</dd></div>
                    <div className="min-w-0 space-y-1"><dt className="font-medium">{text.floor}</dt><dd className="text-xl tabular-nums [overflow-wrap:anywhere]">{number.format(value.min_retained_seq)}</dd><dd className="text-sm leading-relaxed text-muted-foreground">{text.floorHelp}</dd></div>
                    <div className="min-w-0 space-y-1"><dt className="font-medium">{text.lastChange}</dt><dd className="text-base [overflow-wrap:anywhere]">{value.last_change_at ? <time dateTime={value.last_change_at}>{value.last_change_at}</time> : text.noLastChange}</dd><dd className="text-sm leading-relaxed text-muted-foreground">{text.lastChangeHelp}</dd></div>
                  </dl></section>
                </CardContent></Card>
                <Card className="[--card-spacing:--spacing(6)]"><CardContent className="space-y-5 pt-6">
                  <section aria-labelledby="sync-counts-title" className="min-w-0 space-y-4"><h2 id="sync-counts-title" className="text-xl font-semibold">{text.counts}</h2><p className="text-base leading-relaxed">{text.countsHelp}</p>
                    {counts.length === 0 ? <p className="text-base">{text.noCounts}</p> : <dl className="divide-y rounded-md border">{counts.map(([kind, label]) => <div key={kind} className="flex min-w-0 flex-wrap items-baseline justify-between gap-x-4 gap-y-1 px-4 py-3"><dt className="min-w-0 font-medium [overflow-wrap:anywhere]">{label}</dt><dd className="min-w-0 tabular-nums [overflow-wrap:anywhere]">{number.format(value.counts[kind]!)}</dd></div>)}</dl>}
                  </section>
                </CardContent></Card>
                <Card className="[--card-spacing:--spacing(6)]"><CardContent className="space-y-5 pt-6">
                  <section className="min-w-0 space-y-4"><h2 className="text-xl font-semibold">{text.identity}</h2><dl className="space-y-3">
                    <div className="min-w-0 space-y-1"><dt className="font-medium">{text.epoch}</dt><dd className="text-base [overflow-wrap:anywhere]">{quoteExactText(value.dataset_epoch)}</dd></div>
                    <div className="min-w-0 space-y-1"><dt className="font-medium">{text.instance}</dt><dd className="text-base [overflow-wrap:anywhere]">{value.server_instance_id}</dd></div>
                    <div className="min-w-0 space-y-1"><dt className="font-medium">{text.projectionVersion}</dt><dd className="text-base tabular-nums">{number.format(value.projection_version)}</dd></div>
                  </dl></section>
                  {value.models && value.models.length > 0 && <section className="min-w-0 space-y-3 border-t pt-5"><h2 className="text-xl font-semibold">{text.models}</h2><p className="text-base leading-relaxed">{text.modelsHelp}</p><ul className="list-inside list-disc space-y-2">{value.models.map((model, index) => <li key={`${index}:${model}`} className="[overflow-wrap:anywhere]">{quoteExactText(model)}</li>)}</ul></section>}
                </CardContent></Card>
                <p className="max-w-prose text-base leading-relaxed text-muted-foreground">{text.readOnlyHelp}</p>
                {status.dataUpdatedAt > 0 && <p className="text-sm text-muted-foreground">{text.readAt}：<time dateTime={new Date(status.dataUpdatedAt).toISOString()}>{new Intl.DateTimeFormat('zh-TW', { dateStyle: 'short', timeStyle: 'short' }).format(status.dataUpdatedAt)}</time></p>}
              </div>}
  </div>
}
