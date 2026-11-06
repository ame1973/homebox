import { Alert, AlertDescription } from '@/components/ui/alert'
import { MutationReviewDialog } from '@/components/mutation-review'
import { messages } from '@/lib/messages'
import { stockPreview } from './commands'
import { baseQuantity, holdingValue } from './display'
import { HoldingError } from './holding-error'
import { HoldingPath, Measurement, UnitFields } from './holding-fields'
import type { HoldingMutation } from './use-holding-mutation'

const text = messages.holdings
export function HoldingReviewDialog({ operation, online, current, onReadCurrent }: { operation: HoldingMutation; online: boolean; current: boolean; onReadCurrent: () => Promise<unknown> }) {
  const command = operation.command
  if (!command) return null
  const stock = 'balances' in command ? stockPreview(command) : undefined
  const destructive = command.kind === 'delete-unit' || command.kind === 'consume' || command.kind === 'status-unit' && command.body.status === 'disposed' || !!stock?.target?.before && stock.target.quantity_base < stock.target.before.quantity_base
  const confirmLabel = command.kind === 'create-unit' ? text.confirmCreate : command.kind === 'patch-unit' ? text.confirmSave : command.kind === 'move-unit' ? text.confirmMove : command.kind === 'status-unit' ? text.confirmStatus : command.kind === 'delete-unit' ? text.confirmDelete : text.confirms[command.kind]
  const help = command.kind === 'delete-unit' ? text.deleteHelp : command.kind === 'status-unit' ? text.statusHelp : text.reviewHelp
  return <MutationReviewDialog operation={operation} title={text.reviewTitle} help={help} confirmLabel={confirmLabel} destructive={destructive} online={online && (operation.phase === 'uncertain' || current)} onReadCurrent={onReadCurrent} error={<HoldingError error={operation.error} />}>
    <div className="min-w-0 space-y-2"><p className="whitespace-pre-wrap text-lg font-semibold [overflow-wrap:anywhere]">{text.owner}：{JSON.stringify(command.owner.name)}</p><p className="text-sm text-muted-foreground [overflow-wrap:anywhere]">{command.owner.id}<br />{'subject' in command ? `${text.unitProof}${command.subject.revision}` : `${text.ownerProof}${command.owner.revision}`}</p><p className="text-sm [overflow-wrap:anywhere]">{text.requestKey}：{command.key}</p></div>
    {online && !current && operation.phase !== 'uncertain' && <Alert role="status" className="p-4"><AlertDescription className="text-base">{text.reviewStale}</AlertDescription></Alert>}
    {'balances' in command && stock ? <>
      <Measurement item={command.owner} /><p className="text-base font-medium">{text.actions[command.kind]}；{text.fields.quantity_base}：{baseQuantity(command.body.quantity_base, command.owner.base_unit)}</p>
      {([['source', stock.source, command.source, text.sourceBefore, text.sourceAfter], ['target', stock.target, command.target, text.targetBefore, text.targetAfter]] as const).map(([key, change, path, before, after]) => change && <section key={key} className="min-w-0 space-y-3 border-t pt-4"><HoldingPath path={path} label={key === 'source' ? text.fields.source_location_id : text.fields.target_location_id} /><dl className="min-w-0 space-y-3"><div><dt className="text-sm text-muted-foreground">{before}</dt><dd className="text-base [overflow-wrap:anywhere]">{change.before ? baseQuantity(change.before.quantity_base, command.owner.base_unit) : text.absentBalance}</dd></div><div><dt className="text-sm text-muted-foreground">{after}</dt><dd className="text-lg font-semibold [overflow-wrap:anywhere]">{baseQuantity(change.quantity_base, command.owner.base_unit)}</dd></div></dl>{change.before && <p className="text-sm text-muted-foreground">{text.stockProof}{change.before.revision}</p>}</section>)}
      {'reason' in command.body && <div className="min-w-0 space-y-2"><h3 className="text-base font-semibold">{text.fields.reason}</h3><p className="whitespace-pre-wrap text-base [overflow-wrap:anywhere]">{JSON.stringify(command.body.reason)}</p></div>}
      {command.body.note !== undefined && <div className="min-w-0 space-y-2"><h3 className="text-base font-semibold">{text.fields.note}</h3><p className="whitespace-pre-wrap text-base [overflow-wrap:anywhere]">{JSON.stringify(command.body.note)}</p></div>}
    </> : <>
      {'body' in command ? <dl className="min-w-0 space-y-4">{Object.entries(command.body).map(([key, value]) => <div key={key} className="min-w-0 space-y-2 border-b pb-3"><dt className="text-base font-medium">{text.fields[key as keyof typeof text.fields]}</dt>{'subject' in command && Object.hasOwn(command.subject, key) && <dd className="whitespace-pre-wrap text-sm text-muted-foreground [overflow-wrap:anywhere]">{text.previous}：{holdingValue(key, command.subject[key as keyof typeof command.subject])}</dd>}<dd className="whitespace-pre-wrap text-base [overflow-wrap:anywhere]">{text.proposed}：{holdingValue(key, value)}</dd></div>)}</dl>
        : 'subject' in command && <UnitFields unit={command.subject} />}
      {'location' in command && <HoldingPath path={command.location} label={text.fields.location_id} />}{'destination' in command && <HoldingPath path={command.destination} label={text.fields.target_location_id} />}
      {command.kind === 'status-unit' && command.subject.status === 'disposed' && <p className="text-base leading-relaxed">{text.disposedHelp}</p>}
    </>}
  </MutationReviewDialog>
}
