import { MutationReviewDialog } from '@/components/mutation-review'
import { messages } from '@/lib/messages'
import { commandName, type CatalogCommand } from './commands'
import { CatalogError } from './catalog-error'
import { catalogDisplay } from './display'
import type { CatalogMutation } from './use-catalog-mutation'

const text = messages.catalog
const labels = { create: text.confirmCreate, patch: text.confirmSave, archive: text.confirmArchive, restore: text.confirmRestore, delete: text.confirmDelete }
function reviewRows(command: CatalogCommand) {
  const rows: { label: string; before?: string; after: string }[] = []
  if (command.kind === 'create' || command.kind === 'patch') {
    for (const key of Object.keys(text.fields) as (keyof typeof text.fields)[]) {
      const value = command.body[key]
      if (value === undefined) continue
      const after = key === 'category_id' && command.category ? `「${command.category.name}」 (${command.category.id})` : catalogDisplay(key, value)
      rows.push({ label: text.fields[key], ...(command.kind === 'patch' ? { before: catalogDisplay(key, command.subject[key]) } : {}), after })
    }
  } else if (command.kind === 'archive' || command.kind === 'restore') {
    rows.push({ label: text.fields.status, before: catalogDisplay('status', command.subject.status), after: command.kind === 'archive' ? text.archived : text.active })
    if (command.kind === 'archive' && command.body.reason !== undefined) rows.push({ label: text.reason, after: catalogDisplay('reason', command.body.reason) })
  }
  return rows
}
export function CatalogReviewDialog({ operation, online, onReadCurrent }: { operation: CatalogMutation; online: boolean; onReadCurrent: () => Promise<unknown> }) {
  const command = operation.command
  if (!command) return null
  const help = command.kind === 'delete' ? text.deleteHelp : command.kind === 'archive' ? text.archiveHelp : command.kind === 'restore' ? text.restoreHelp : text.reviewHelp
  return <MutationReviewDialog operation={operation} title={text.reviewTitle} help={help} confirmLabel={labels[command.kind]} destructive={command.kind === 'delete'} online={online} onReadCurrent={onReadCurrent} error={<CatalogError error={operation.error} />}>
    <p className="whitespace-pre-wrap text-lg font-semibold [overflow-wrap:anywhere]">「{commandName(command)}」</p>
    {'subject' in command && <p className="text-sm text-muted-foreground [overflow-wrap:anywhere]">{text.identifier}：{command.subject.id}<br />{text.originalVersion}{command.subject.revision}</p>}
    <dl className="space-y-4">{reviewRows(command).map((row) => <div key={row.label} className="space-y-1 border-b pb-3"><dt className="text-base font-medium">{row.label}</dt>
      {row.before !== undefined && <dd className="whitespace-pre-wrap text-sm text-muted-foreground [overflow-wrap:anywhere]">{text.previous}：{row.before}</dd>}
      <dd className="whitespace-pre-wrap text-base [overflow-wrap:anywhere]">{text.proposed}：{row.after}</dd>
    </div>)}</dl>
  </MutationReviewDialog>
}
