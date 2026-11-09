import { Alert, AlertDescription } from '@/components/ui/alert'
import { MutationReviewDialog } from '@/components/mutation-review'
import { messages } from '@/lib/messages'
import { UnitFields } from '@/features/holdings/holding-fields'
import { PurchaseError } from '@/features/purchases/purchase-error'
import { PurchaseFields, PurchaseItem } from '@/features/purchases/purchase-fields'
import { OriginalCorrectionUnitFields, PurchaseCorrectionLine, PurchaseCorrectionProofs } from './purchase-correction-fields'
import type { CorrectionMutation } from './use-correction-mutation'

const text = messages.purchaseCorrections, purchase = messages.purchases

export function PurchaseCorrectionReview({ operation, online, current, onReadCurrent }: { operation: CorrectionMutation; online: boolean; current: boolean; onReadCurrent: () => Promise<unknown> }) {
  const command = operation.command
  if (!command || command.kind !== 'correct-purchase') return null
  const { source, body } = command, replacement = body.replacement
  return <MutationReviewDialog operation={operation} title={text.reviewTitle} help={text.reviewHelp} confirmLabel={body.action === 'void' ? text.confirmVoid : text.confirmReplace} destructive={body.action === 'void'} online={online && (operation.phase === 'uncertain' || current)} onReadCurrent={onReadCurrent} error={<PurchaseError error={operation.error} />}>
    <p className="text-sm [overflow-wrap:anywhere]">{text.requestKey}：{command.key}</p><PurchaseCorrectionProofs source={source} includeTarget={replacement !== null} />
    {online && !current && operation.phase !== 'uncertain' && <Alert role="status" className="p-4"><AlertDescription className="text-base">{text.reviewStale}</AlertDescription></Alert>}
    <PurchaseFields purchase={source.purchase} /><PurchaseItem item={source.item} /><PurchaseCorrectionLine title={text.original} line={source.line} currency={source.purchase.currency} /><OriginalCorrectionUnitFields original={source.originalUnit} />
    {replacement === null ? <><p className="text-base">{text.voidHelp}</p>{source.replacement.kind !== 'none' && <p className="text-sm text-muted-foreground">{text.inactiveTarget}</p>}</> : <section className="min-w-0 space-y-4"><h2 className="text-lg font-semibold">{text.target}</h2>{source.replacement.kind !== 'none' && <PurchaseItem item={source.replacement.item} />}
      <dl className="grid min-w-0 gap-4 sm:grid-cols-2">{(['item_unit_id', 'quantity_base', 'paid_minor', 'note'] as const).map((key) => <div key={key} className="min-w-0 space-y-1"><dt className="text-sm font-medium text-muted-foreground">{purchase.fields[key]}</dt><dd className="whitespace-pre-wrap text-base tabular-nums [overflow-wrap:anywhere]">{key === 'item_unit_id' && replacement[key] === null ? purchase.noUnit : typeof replacement[key] === 'string' ? JSON.stringify(replacement[key]) : String(replacement[key] ?? '')}{key === 'quantity_base' && source.replacement.kind !== 'none' ? ` ${JSON.stringify(source.replacement.item.base_unit)}` : key === 'paid_minor' ? ` ${source.purchase.currency}` : ''}</dd></div>)}</dl>
      {command.unit && <><UnitFields unit={command.unit} /><p className="text-sm text-muted-foreground">{text.contextHelp}</p></>}
    </section>}
    <p className="whitespace-pre-wrap text-base [overflow-wrap:anywhere]">{text.reason}：{JSON.stringify(body.reason)}</p><p className="max-w-prose text-base">{text.voidHelp}</p>
  </MutationReviewDialog>
}
