import { Alert, AlertDescription } from '@/components/ui/alert'
import { MutationReviewDialog } from '@/components/mutation-review'
import { messages } from '@/lib/messages'
import { UnitFields } from '@/features/holdings/holding-fields'
import { PurchaseError } from './purchase-error'
import { PurchaseItem } from './purchase-fields'
import type { PurchaseMutation } from './use-purchase-mutation'

const text = messages.purchases
export function PurchaseReviewDialog({ operation, online, current, onReadCurrent }: { operation: PurchaseMutation; online: boolean; current: boolean; onReadCurrent: () => Promise<unknown> }) {
  const command = operation.command
  if (!command) return null
  const allocation = command.kind === 'create-line'
  const purchase = command.kind === 'patch-purchase' ? command.source.purchase : allocation ? command.purchase : undefined
  return <MutationReviewDialog operation={operation} title={text.reviewTitle} help={allocation ? text.allocationReviewHelp : text.reviewHelp} confirmLabel={allocation ? text.confirmLine : command.kind === 'patch-purchase' ? text.confirmSave : text.confirmCreate} online={online && (operation.phase === 'uncertain' || current)} onReadCurrent={onReadCurrent} error={<PurchaseError error={operation.error} />}>
    <p className="text-sm [overflow-wrap:anywhere]">{text.requestKey}：{command.key}</p>
    {purchase && <p className="text-sm text-muted-foreground [overflow-wrap:anywhere]">{text.purchaseID}：{purchase.id}<br />{text.purchaseProof}{purchase.revision}</p>}
    {online && !current && operation.phase !== 'uncertain' && <Alert role="status" className="p-4"><AlertDescription className="text-base">{text.reviewStale}</AlertDescription></Alert>}
    {command.kind === 'create-line' ? <>
      <p className="whitespace-pre-wrap text-base [overflow-wrap:anywhere]">{text.fields.merchant_name}：{JSON.stringify(command.purchase.merchant_name)}<br />{text.fields.purchased_at}：{command.purchase.purchased_at}<br />{text.fields.currency}：{command.purchase.currency}</p>
      <PurchaseItem item={command.item} /><dl className="min-w-0 space-y-4">{(['item_unit_id', 'quantity_base', 'paid_minor', 'note'] as const).map((key) => <div key={key} className="min-w-0 space-y-2 border-b pb-3"><dt className="text-base font-medium">{text.fields[key]}</dt><dd className="whitespace-pre-wrap text-base [overflow-wrap:anywhere]">{key === 'item_unit_id' && command.body[key] === null ? text.noUnit : typeof command.body[key] === 'string' ? JSON.stringify(command.body[key]) : String(command.body[key] ?? '')}{key === 'quantity_base' ? ` ${JSON.stringify(command.item.base_unit)}` : key === 'paid_minor' ? ` ${command.purchase.currency}` : ''}</dd></div>)}</dl>
      {command.unit && <section className="min-w-0 space-y-3"><h3 className="text-base font-semibold">{text.unit}</h3><UnitFields unit={command.unit} /><p className="text-sm text-muted-foreground">{text.unitContext}</p></section>}
    </> : <dl className="min-w-0 space-y-4">{Object.entries(command.body).map(([name, value]) => <div key={name} className="min-w-0 space-y-2 border-b pb-3"><dt className="text-base font-medium">{text.fields[name as keyof typeof text.fields]}</dt>{purchase && <dd className="whitespace-pre-wrap text-sm text-muted-foreground [overflow-wrap:anywhere]">{text.previous}：{JSON.stringify(purchase[name as keyof typeof command.body])}</dd>}<dd className="whitespace-pre-wrap text-base [overflow-wrap:anywhere]">{text.proposed}：{JSON.stringify(value)}</dd></div>)}</dl>}
  </MutationReviewDialog>
}
