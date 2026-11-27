import { Link } from 'react-router'
import { LoaderCircle, RefreshCw, WifiOff } from 'lucide-react'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { mutationControl as control } from '@/components/mutation-review'
import { fetchSystemStorage } from '@/lib/api/system-storage'
import { formatBytes } from '@/lib/format-bytes'
import { messages } from '@/lib/messages'
import { useReadOnlyStatus } from './use-read-only-status'

const text = messages.serverStorage
const statusKey = ['system', 'storage'] as const
const exact = new Intl.NumberFormat('zh-TW')

function Bytes({ label, value }: { label: string; value: number | null }) {
  return <div className="min-w-0 space-y-1"><dt className="font-medium">{label}</dt><dd className="text-base tabular-nums [overflow-wrap:anywhere]">
    {value === null ? text.unknown : <>{formatBytes(value)} <span className="text-sm text-muted-foreground">（{exact.format(value)} {text.bytes}）</span></>}
  </dd></div>
}

export default function ServerStoragePage() {
  const { valid, online, blocked, transitioning, fetching, waiting, error, checking, status, refresh } =
    useReadOnlyStatus('/settings/storage', statusKey, fetchSystemStorage)
  const value = status.data
  const disableRefresh = !valid || !online || blocked || transitioning || fetching

  return <div className="min-w-0 space-y-7 py-8 md:py-10">
    <header className="flex min-w-0 flex-wrap items-start justify-between gap-4">
      <div className="min-w-0 space-y-3"><h1 className="text-3xl leading-snug font-semibold [overflow-wrap:anywhere] md:text-4xl">{text.title}</h1><p className="max-w-prose text-base leading-relaxed text-muted-foreground">{text.intro}</p></div>
      <div className="flex max-w-full flex-wrap gap-2"><Button asChild variant="outline" className={control}><Link to="/settings">{text.back}</Link></Button>
        <Button type="button" variant="outline" className={`${control} gap-2 aria-disabled:cursor-not-allowed aria-disabled:opacity-50`} aria-disabled={disableRefresh}
          onClick={() => { if (!disableRefresh) void refresh() }}><RefreshCw aria-hidden="true" className={`size-4 ${fetching ? 'animate-spin motion-reduce:animate-none' : ''}`} />{text.refresh}</Button></div>
    </header>
    {!valid ? <Alert role="alert" className="p-4"><AlertDescription className="text-base">{text.invalidRoute}</AlertDescription></Alert>
      : blocked ? <Alert role="alert" className="p-4"><AlertDescription className="space-y-3 text-base"><p>{text.sessionCleared}</p><Button asChild variant="outline" className={control}><Link to="/login" state={{ notice: 'expired', returnTo: '/settings/storage' }}>{messages.scan.signIn}</Link></Button></AlertDescription></Alert>
        : !online ? <Alert role="status" className="p-4"><WifiOff aria-hidden="true" /><AlertTitle>{text.offline}</AlertTitle><AlertDescription className="text-base">{text.offlineHelp}</AlertDescription></Alert>
          : waiting && !error ? <p role="status" className="flex min-h-24 items-center gap-3 text-base"><LoaderCircle aria-hidden="true" className="size-6 animate-spin motion-reduce:animate-none" />{checking ? text.checking : text.loading}</p>
            : error ? <Alert role="alert" className="p-4"><AlertDescription className="text-base">{text.readError}</AlertDescription></Alert>
              : value && <div className="max-w-3xl space-y-5">
                <Card className="[--card-spacing:--spacing(6)] py-4! sm:py-6!"><CardContent className="space-y-6 px-[12px]! sm:px-6!">
                  <section className="min-w-0 space-y-4"><h2 className="text-xl font-semibold">{text.filesystem}</h2><p className="text-base leading-relaxed">{text.filesystemHelp}</p><dl className="space-y-4">
                    <Bytes label={text.database} value={value.database_bytes} /><Bytes label={text.free} value={value.free_bytes} /><Bytes label={text.total} value={value.database_filesystem_total_bytes} />
                  </dl></section>
                  <section className="min-w-0 space-y-4 border-t pt-5"><h2 className="text-xl font-semibold">{text.otherScopes}</h2><p className="text-base leading-relaxed">{text.otherHelp}</p><dl className="space-y-4">
                    <Bytes label={text.media} value={value.media_bytes} /><Bytes label={text.backup} value={value.backup_bytes} />
                  </dl></section>
                  <div className="min-w-0 space-y-1 border-t pt-5"><p className="font-medium">{text.measuredAt}</p><time dateTime={value.measured_at} className="text-base tabular-nums [overflow-wrap:anywhere]">{value.measured_at}</time></div>
                </CardContent></Card>
                <p className="max-w-prose text-base leading-relaxed text-muted-foreground">{text.readOnlyHelp}</p>
              </div>}
  </div>
}
