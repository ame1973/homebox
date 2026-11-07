import { useId } from 'react'
import { useForm } from 'react-hook-form'
import { Button } from '@/components/ui/button'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Table, TableBody, TableCaption, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { mutationControl as control } from '@/components/mutation-review'
import { assetSummaryQuerySchema, type AssetSummaryQuery } from '@/lib/api/assets'
import { messages } from '@/lib/messages'
import { MetadataError } from '@/features/catalog/editor'
import { AssetEvidence } from './asset-fields'
import type { AssetDetail } from './model'

const text = messages.assets
export function AssetCalculation({ data, query, disabled, navigationState, onApply }: { data: AssetDetail; query: AssetSummaryQuery; disabled: boolean; navigationState: object; onApply: (query: AssetSummaryQuery) => void }) {
  const id = useId(), form = useForm({ defaultValues: { as_of: query.as_of ?? '' } }), summary = data.summary
  function apply(values: { as_of: string }) {
    if (disabled) return
    const parsed = assetSummaryQuerySchema.safeParse(values.as_of === '' ? {} : values)
    if (!parsed.success) { form.setError('as_of', { type: 'validate', message: text.instantError }, { shouldFocus: true }); return }
    form.clearErrors(); onApply(parsed.data)
  }
  const rows = Object.keys(text.summaryFields) as (keyof typeof text.summaryFields)[]
  const money = (name: typeof rows[number]) => name.endsWith('_minor') && !name.includes('per_')
  return <section className="min-w-0 space-y-6"><p className="max-w-prose text-base leading-relaxed">{text.calculationHelp}</p>
    <form noValidate onSubmit={form.handleSubmit(apply)} className="min-w-0 space-y-3 rounded-lg border p-5"><Field data-invalid={!!form.formState.errors.as_of}><FieldLabel htmlFor={id} className="text-base">{text.asOf}</FieldLabel><Input {...form.register('as_of')} id={id} maxLength={70} autoComplete="off" spellCheck={false} aria-invalid={!!form.formState.errors.as_of} aria-describedby={`${id}-help ${id}-error`} className="h-auto min-h-12 py-3 text-base md:text-base" /><FieldDescription id={`${id}-help`} className="text-base">{text.serverNow}。{text.instantHelp}</FieldDescription><div id={`${id}-error`}><MetadataError error={form.formState.errors.as_of} message={text.instantError} /></div></Field><Button type="submit" variant="outline" disabled={disabled} className={control}>{text.applyAsOf}</Button></form>
    <dl className="grid min-w-0 gap-4 sm:grid-cols-2">{[[text.fields.currency, summary.currency], [text.fields.cost_basis, text.bases[summary.cost_basis]], [text.fields.usage_unit, `${text.usageUnits[summary.usage_unit]} (${summary.usage_unit})`], [text.timezone, summary.timezone], [text.asOfResult, summary.as_of], [text.sourceRevision, summary.source_revision]].map(([label, value]) => <div key={label} className="min-w-0"><dt className="text-sm font-medium text-muted-foreground">{label}</dt><dd className="whitespace-pre-wrap text-base [overflow-wrap:anywhere]">{String(value)}</dd></div>)}</dl>
    <p className="text-base text-muted-foreground">{text.dayPolicy}</p>
    {/* Preserve table roles when narrow-screen CSS stacks each label above its value. */}
    <Table role="table" aria-colcount={2} className="table-fixed text-base max-sm:block"><TableCaption className="text-base max-sm:mb-4 max-sm:block">{text.summary}；{text.realized}／{text.projected}；{summary.currency}</TableCaption><TableHeader role="rowgroup" className="max-sm:sr-only"><TableRow role="row"><TableHead role="columnheader" scope="col" className="w-1/2 whitespace-normal! px-3 py-3">{text.metric}</TableHead><TableHead role="columnheader" scope="col" className="whitespace-normal! px-3 py-3">{text.value}</TableHead></TableRow></TableHeader>
      <TableBody role="rowgroup" className="max-sm:block">{rows.map((name) => <TableRow key={name} role="row" className="max-sm:block"><TableHead role="rowheader" scope="row" className="whitespace-normal! px-3 py-4 align-top text-base [overflow-wrap:anywhere] max-sm:block max-sm:h-auto max-sm:pb-2">{text.summaryFields[name]}<span className="mt-2 block text-sm font-normal text-muted-foreground">{name.startsWith('projected_') || name === 'estimated_residual_minor' ? text.projected : name === 'elapsed_days' || name === 'total_usage' ? '' : text.realized}</span></TableHead><TableCell role="cell" className="whitespace-normal! px-3 py-4 align-top tabular-nums [overflow-wrap:anywhere] max-sm:block max-sm:pt-0">{summary[name] === null ? name.includes('per_') ? text.nullRatio : text.unknownValue : String(summary[name])}<span className="mt-2 block text-sm text-muted-foreground">{money(name) ? `${summary.currency} ${text.minorUnit}` : name === 'elapsed_days' ? text.dayUnit : name === 'total_usage' ? `${text.usageUnits[summary.usage_unit]} (${summary.usage_unit})` : `${summary.currency} ${text.minorUnit} / ${name.includes('day') ? text.dayUnit : text.usageUnits[summary.usage_unit]}`}</span></TableCell></TableRow>)}</TableBody>
    </Table><AssetEvidence itemID={data.item.id} navigationState={navigationState} />
  </section>
}
