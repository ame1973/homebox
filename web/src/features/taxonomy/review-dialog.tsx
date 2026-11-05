import { MutationReviewDialog } from '@/components/mutation-review'
import type { Category } from '@/lib/api/taxonomy'
import { messages } from '@/lib/messages'
import { commandName, type TaxonomyCommand } from './commands'
import { TaxonomyError } from './taxonomy-error'
import type { TaxonomyMutation } from './use-taxonomy-mutation'

const text = messages.taxonomy
const labels = { 'create-category': text.confirmCreateCategory, 'patch-category': text.confirmSave, 'delete-category': text.confirmDeleteCategory,
  'create-tag': text.confirmCreateTag, 'patch-tag': text.confirmSave, 'delete-tag': text.confirmDeleteTag }
function parentLabel(id: string | null | undefined, snapshot?: Category | null) {
  if (id == null) return text.root
  return snapshot?.id === id ? `「${snapshot.name}」 (${id})` : id
}
function reviewRows(command: TaxonomyCommand) {
  const rows: { label: string; before?: string; after: string }[] = []
  if (!('body' in command)) return rows
  const source = 'subject' in command ? command.subject : undefined
  if (command.body.name !== undefined) rows.push({ label: command.kind.endsWith('category') ? text.categoryName : text.tagName, before: source?.name, after: command.body.name })
  if (command.kind === 'create-category' || command.kind === 'patch-category' && command.body.parent_id !== undefined) {
    rows.push({ label: text.parent,
      ...(command.kind === 'patch-category' ? { before: parentLabel(command.subject.parent_id, command.previousParent) } : {}),
      after: parentLabel(command.body.parent_id, command.parent) })
  }
  return rows
}

export function TaxonomyReviewDialog({ operation, online, onReadCurrent }: { operation: TaxonomyMutation; online: boolean; onReadCurrent: () => Promise<unknown> }) {
  const command = operation.command
  if (!command) return null
  const deleting = command.kind === 'delete-category' || command.kind === 'delete-tag'
  const help = command.kind === 'delete-category' ? text.deleteCategoryHelp : command.kind === 'delete-tag' ? text.deleteTagHelp : text.reviewHelp
  return <MutationReviewDialog operation={operation} title={text.reviewTitle} help={help} confirmLabel={labels[command.kind]} destructive={deleting} online={online} onReadCurrent={onReadCurrent} error={<TaxonomyError error={operation.error} />}>
    <p className="whitespace-pre-wrap text-lg font-semibold [overflow-wrap:anywhere]">「{commandName(command)}」</p>
    {'subject' in command && <p className="text-sm text-muted-foreground [overflow-wrap:anywhere]">{text.identifier}：{command.subject.id}<br />{text.originalVersion}{command.subject.revision}</p>}
    {command.kind === 'create-category' && command.parent && <p className="text-sm text-muted-foreground">{text.parentProof}{command.parent.revision}</p>}
    <dl className="space-y-4">{reviewRows(command).map((row) => <div key={row.label} className="space-y-1 border-b pb-3"><dt className="text-base font-medium">{row.label}</dt>
      {row.before !== undefined && <dd className="whitespace-pre-wrap text-sm text-muted-foreground [overflow-wrap:anywhere]">{text.previous}：{row.before}</dd>}
      <dd className="whitespace-pre-wrap text-base [overflow-wrap:anywhere]">{text.proposed}：{row.after}</dd>
    </div>)}</dl>
  </MutationReviewDialog>
}
