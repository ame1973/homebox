import type { PurchaseLine } from '@/lib/api/purchases'
import { messages } from '@/lib/messages'
import { UnitFields } from '@/features/holdings/holding-fields'
import type { OriginalCorrectionUnit, PurchaseCorrectionSource } from '@/features/purchases/correction-model'

const text = messages.purchases, correction = messages.purchaseCorrections

export function PurchaseCorrectionProofs({ source, includeTarget = true }: { source: PurchaseCorrectionSource; includeTarget?: boolean }) {
  return <div className="min-w-0 space-y-2 text-sm text-muted-foreground [overflow-wrap:anywhere]"><p>{text.purchaseID}：{JSON.stringify(source.purchase.id)}<br />{text.purchaseProof}{source.purchase.revision}<br />{correction.lineProof}{source.line.revision}<br />{correction.oldItemProof}{source.item.revision}{includeTarget && source.replacement.kind !== 'none' && <><br />{correction.targetProof}{source.replacement.item.revision}</>}</p><p>{correction.contextHelp}</p></div>
}

export function OriginalCorrectionUnitFields({ original }: { original: OriginalCorrectionUnit }) {
  return <section className="min-w-0 space-y-3"><h3 className="text-lg font-semibold">{correction.originalUnit}</h3>{original.kind === 'resolved' ? <UnitFields unit={original.unit} /> : original.kind === 'unallocated' ? <p>{text.noUnit}</p> : <><p className="whitespace-pre-wrap [overflow-wrap:anywhere]">{JSON.stringify(original.id)}</p><p>{messages.holdings.unaddressable}</p></>}</section>
}

export function PurchaseCorrectionLine({ title, line, currency }: { title: string; line: PurchaseLine; currency: string }) {
  return <section className="min-w-0 space-y-3"><h3 className="text-lg font-semibold">{title}</h3><dl className="grid min-w-0 gap-4 sm:grid-cols-2">
    {([
      [text.lineID, JSON.stringify(line.id)], [text.lineVersion, String(line.revision)],
      [text.item, JSON.stringify(line.item_id)], [text.unit, line.item_unit_id === null ? text.noUnit : JSON.stringify(line.item_unit_id)],
      [text.fields.quantity_base, String(line.quantity_base)], [text.fields.paid_minor, `${line.paid_minor} ${currency}`],
      [text.fields.note, JSON.stringify(line.note)],
    ] as const).map(([label, value]) => <div key={label} className="min-w-0 space-y-1"><dt className="text-sm font-medium text-muted-foreground">{label}</dt><dd className="whitespace-pre-wrap text-base tabular-nums [overflow-wrap:anywhere]">{value}</dd></div>)}
  </dl></section>
}
