import type { DisposalData } from '@/lib/api/financial-corrections'
import { messages } from '@/lib/messages'
import { assetValue } from '@/features/assets/display'
import type { DisposalCorrectionSource } from '@/features/assets/correction-model'

const text = messages.disposalCorrections, asset = messages.assets
export function DisposalCorrectionProofs({ source }: { source: DisposalCorrectionSource }) {
  return <div className="min-w-0 space-y-2 text-sm text-muted-foreground [overflow-wrap:anywhere]"><p>{asset.identifier}：{source.unit.id}<br />{text.disposalProof}{source.disposal.revision}<br />{text.assetProof}{source.asset.revision}</p><p>{text.contextHelp}</p></div>
}
export function DisposalDecision({ title, data }: { title: string; data: DisposalData | null }) {
  return <section className="min-w-0 space-y-3"><h3 className="text-lg font-semibold">{title}</h3>{data === null ? <p className="text-base">{text.voided}</p> : <dl className="grid min-w-0 gap-4 sm:grid-cols-2">{(['kind', 'currency', 'realized_proceeds_minor', 'disposed_at', 'note'] as const).map((name) => <div key={name} className={`min-w-0 space-y-1 ${name === 'note' ? 'sm:col-span-2' : ''}`}><dt className="text-sm font-medium text-muted-foreground">{name === 'kind' ? text.kind : asset.fields[name]}</dt><dd className="whitespace-pre-wrap text-base [overflow-wrap:anywhere]">{assetValue(name, data[name])}{name === 'realized_proceeds_minor' ? ` ${data.currency}` : ''}</dd></div>)}</dl>}</section>
}
export function DisposalCorrectionHistory({ source }: { source: DisposalCorrectionSource }) {
  const ordered = [...source.corrections].sort((a, b) => a.before.revision - b.before.revision)
  return <section className="min-w-0 space-y-4"><h2 className="text-xl font-semibold">{text.history}</h2><p className="text-sm text-muted-foreground">{text.historyCount}{ordered.length}</p>
    {!ordered.length ? <p>{text.noHistory}</p> : <ul className="divide-y rounded-lg border bg-card">{ordered.map((entry) => <li key={entry.id} className="min-w-0 p-5 [content-visibility:auto] [contain-intrinsic-size:auto_10rem]"><details className="min-w-0 space-y-4"><summary className="min-h-12 cursor-pointer rounded-sm py-3 text-base font-semibold [overflow-wrap:anywhere] focus-visible:outline-2 focus-visible:outline-ring">{entry.action === 'void' ? text.void : text.replace}；{text.afterVersion}：{entry.after.revision}</summary>
      <div className="min-w-0 space-y-5"><p className="text-sm text-muted-foreground [overflow-wrap:anywhere]">{text.entryID}：{JSON.stringify(entry.id)}<br />{text.entryCreated}：{entry.created_at}<br />{text.beforeVersion}：{entry.before.revision}；{text.afterVersion}：{entry.after.revision}</p>
        <p className="whitespace-pre-wrap [overflow-wrap:anywhere]">{text.entryReason}：{JSON.stringify(entry.reason)}</p><DisposalDecision title={text.before} data={entry.before.current} /><DisposalDecision title={text.after} data={entry.after.current} /></div>
    </details></li>)}</ul>}
  </section>
}
