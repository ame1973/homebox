import type { ReactNode } from 'react'
import { Link } from 'react-router'
import { LoaderCircle, RefreshCw, WifiOff } from 'lucide-react'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { mutationControl as control } from '@/components/mutation-review'
import { fetchSystemVersion } from '@/lib/api/system-version'
import { messages } from '@/lib/messages'
import { documentShellWorker } from '@/lib/shell-version'
import { quoteExactText } from '@/lib/text-literal'
import { version as clientVersion } from '../../../package.json'
import { useReadOnlyStatus } from './use-read-only-status'

const text = messages.versionStatus
const statusKey = ['system', 'version'] as const
const number = new Intl.NumberFormat('zh-TW')

function Field({ label, children }: { label: string; children: ReactNode }) {
  return <div className="min-w-0 space-y-1"><dt className="font-medium">{label}</dt><dd className="text-base [overflow-wrap:anywhere]">{children}</dd></div>
}

export default function VersionStatusPage() {
  const { valid, online, blocked, transitioning, fetching, waiting, error, checking, status, refresh } =
    useReadOnlyStatus('/settings/version', statusKey, fetchSystemVersion)
  const value = status.data
  const worker = documentShellWorker()
  const localBuild = worker?.slice('/pwa-worker-'.length, -'.js'.length)
  const accepted = value?.supported_write_client_versions.includes(clientVersion)
  const pageMatches = value && localBuild ? localBuild === value.web_build_id : undefined

  return <div className="min-w-0 space-y-7 py-8 md:py-10">
    <header className="flex min-w-0 flex-wrap items-start justify-between gap-4">
      <div className="min-w-0 space-y-3"><h1 className="text-3xl leading-snug font-semibold [overflow-wrap:anywhere] md:text-4xl">{text.title}</h1><p className="max-w-prose text-base leading-relaxed text-muted-foreground">{text.intro}</p></div>
      <div className="flex max-w-full flex-wrap gap-2"><Button asChild variant="outline" className={control}><Link to="/settings">{text.back}</Link></Button>
        <Button type="button" variant="outline" className={`${control} gap-2`} disabled={!valid || !online || blocked || transitioning || fetching} onClick={() => { void refresh() }}><RefreshCw aria-hidden="true" className={`size-4 ${fetching ? 'animate-spin motion-reduce:animate-none' : ''}`} />{text.refresh}</Button></div>
    </header>
    {!valid ? <Alert role="alert" className="p-4"><AlertDescription className="text-base">{text.invalidRoute}</AlertDescription></Alert>
      : blocked ? <Alert role="alert" className="p-4"><AlertDescription className="space-y-3 text-base"><p>{text.sessionCleared}</p><Button asChild variant="outline" className={control}><Link to="/login" state={{ notice: 'expired', returnTo: '/settings/version' }}>{messages.scan.signIn}</Link></Button></AlertDescription></Alert>
        : !online ? <Alert role="status" className="p-4"><WifiOff aria-hidden="true" /><AlertTitle>{text.offline}</AlertTitle><AlertDescription className="text-base">{text.offlineHelp}</AlertDescription></Alert>
          : waiting && !error ? <p role="status" className="flex min-h-24 items-center gap-3 text-base"><LoaderCircle aria-hidden="true" className="size-6 animate-spin motion-reduce:animate-none" />{checking ? text.checking : text.loading}</p>
            : error ? <Alert role="alert" className="p-4"><AlertDescription className="text-base">{text.readError}</AlertDescription></Alert>
              : value && <div className="max-w-3xl space-y-5">
                <Card className="[--card-spacing:--spacing(6)]"><CardContent className="space-y-6 pt-6">
                  <section className="min-w-0 space-y-3"><h2 className="text-xl font-semibold">{text.writeTitle}</h2><p className="text-lg font-medium">{accepted ? text.writeAccepted : text.writeRejected}</p><p className="text-base leading-relaxed">{text.writeHelp}</p>{(!accepted || pageMatches === false) && <p className="text-base leading-relaxed">{text.writeRejectedHelp}</p>}</section>
                  <section className="min-w-0 space-y-3 border-t pt-5"><h2 className="text-xl font-semibold">{text.pageTitle}</h2><p className="text-lg font-medium">{pageMatches === undefined ? text.pageUnknown : pageMatches ? text.pageMatches : text.pageDifferent}</p><p className="text-base leading-relaxed">{text.pageHelp}</p></section>
                </CardContent></Card>
                <Card className="[--card-spacing:--spacing(6)]"><CardContent className="space-y-5 pt-6">
                  <dl className="space-y-4"><Field label={text.localVersion}>{quoteExactText(clientVersion)}</Field><Field label={text.serverVersion}>{quoteExactText(value.web_version)}</Field>
                    <Field label={text.localBuild}>{localBuild ?? text.minimumUnknown}</Field><Field label={text.serverBuild}>{value.web_build_id}</Field>
                    <Field label={text.minimumClient}>{value.min_supported_client === null ? text.minimumUnknown : quoteExactText(value.min_supported_client)}</Field>
                    <Field label={text.minimumBuild}>{value.min_web_build_id === null ? text.minimumUnknown : quoteExactText(value.min_web_build_id)}</Field></dl>
                  <section className="min-w-0 space-y-3 border-t pt-5"><h2 className="text-xl font-semibold">{text.acceptedVersions}</h2><ul className="list-inside list-disc space-y-2">{value.supported_write_client_versions.map((entry) => <li key={entry} className="[overflow-wrap:anywhere]">{quoteExactText(entry)}</li>)}</ul></section>
                </CardContent></Card>
                <Card className="[--card-spacing:--spacing(6)]"><CardContent className="space-y-6 pt-6">
                  <section className="min-w-0 space-y-3"><h2 className="text-xl font-semibold">{text.models}</h2>
                    {value.model_index_versions === null ? <p className="text-base">{text.modelsUnknown}</p> : value.model_index_versions.length === 0 ? <p className="text-base">{text.modelsEmpty}</p> : <ul className="list-inside list-disc space-y-2">{value.model_index_versions.map((entry, index) => <li key={`${index}:${entry}`} className="[overflow-wrap:anywhere]">{quoteExactText(entry)}</li>)}</ul>}
                    <p className="text-base leading-relaxed">{text.modelsHelp}</p></section>
                  <section className="min-w-0 space-y-4 border-t pt-5"><h2 className="text-xl font-semibold">{text.dataset}</h2><dl className="space-y-4">
                    <Field label={text.epoch}>{quoteExactText(value.dataset_epoch)}</Field><Field label={text.instance}>{value.server_instance_id}</Field>
                    <Field label={text.apiVersion}>{quoteExactText(value.api_version)}</Field><Field label={text.schemaVersion}>{number.format(value.schema_version)}</Field><Field label={text.projectionVersion}>{number.format(value.projection_version)}</Field>
                  </dl></section>
                </CardContent></Card>
                <p className="max-w-prose text-base leading-relaxed text-muted-foreground">{text.readOnlyHelp}</p>
                {status.dataUpdatedAt > 0 && <p className="text-sm text-muted-foreground">{text.readAt}：<time dateTime={new Date(status.dataUpdatedAt).toISOString()}>{new Intl.DateTimeFormat('zh-TW', { dateStyle: 'short', timeStyle: 'short' }).format(status.dataUpdatedAt)}</time></p>}
              </div>}
  </div>
}
