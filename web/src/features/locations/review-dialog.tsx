import { MutationReviewDialog } from '@/components/mutation-review'
import { messages } from '@/lib/messages'
import { commandName, type LocationCommand } from './commands'
import { isKnownKind } from './form'
import { LocationError } from './location-error'
import type { LocationTree } from './tree'
import type { LocationMutation } from './use-location-mutation'

export { MutationNotice as LocationMutationNotice } from '@/components/mutation-review'
const text = messages.locations
const labels = { create: text.confirmCreate, patch: text.confirmSave, move: text.confirmMove, archive: text.confirmArchive, restore: text.confirmRestore, delete: text.confirmDelete }

function parentLabel(tree: LocationTree, id: string | null | undefined) {
  if (id == null) return text.root
  const location = tree.byID.get(id)
  return location ? `${location.name} (${location.code})` : id
}

function reviewRows(command: LocationCommand, tree: LocationTree) {
  const rows: { label: string; before?: string; after: string }[] = []
  const source = command.kind === 'create' ? undefined : command.subject
  const body = command.kind === 'create' || command.kind === 'patch' ? command.body : command.kind === 'move' ? { parent_id: command.body.target_parent_id } : command.kind === 'archive' ? { status: 'archived' } : command.kind === 'restore' ? { status: 'active' } : {}
  for (const [key, label] of [['name', text.name], ['code', text.code], ['kind', text.kind], ['parent_id', text.parentLabel], ['sort_order', text.sortOrder], ['description', text.description], ['status', text.status]] as const) {
    if (!(key in body)) continue
    const value = body[key as keyof typeof body]
    if (value === undefined) continue
    const display = (value: unknown) => key === 'parent_id' ? parentLabel(tree, value as string | null) : key === 'status' ? value === 'active' ? text.active : text.archived : key === 'kind' && typeof value === 'string' && isKnownKind(value) ? text.kinds[value] : String(value ?? '') || text.emptyValue
    rows.push({ label, before: source ? display(source[key]) : undefined, after: display(value) })
  }
  return rows
}

export function LocationReviewDialog({ operation, tree, online, onReadCurrent }: { operation: LocationMutation; tree: LocationTree; online: boolean; onReadCurrent: () => Promise<unknown> }) {
  const command = operation.command
  if (!command) return null
  const help = command.kind === 'move' ? text.moveHelp : command.kind === 'archive' ? text.archiveHelp : command.kind === 'restore' ? text.restoreHelp : command.kind === 'delete' ? text.deleteHelp : text.reviewHelp
  return <MutationReviewDialog operation={operation} title={text.reviewTitle} help={help} confirmLabel={labels[command.kind]} destructive={command.kind === 'delete'} online={online} onReadCurrent={onReadCurrent} error={<LocationError error={operation.error} />}>
    <p className="text-lg font-semibold [overflow-wrap:anywhere]">{commandName(command)}</p>
    {'subject' in command && <p className="text-base [overflow-wrap:anywhere]">{command.subject.code}</p>}
    {'subject' in command && <p className="text-sm text-muted-foreground">{text.originalVersion}{command.subject.revision}</p>}
    {command.kind === 'create' && command.parentRevision !== undefined && <p className="text-sm text-muted-foreground">{text.parentProof}{command.parentRevision}</p>}
    <dl className="space-y-4">
      {reviewRows(command, tree).map((row) => <div key={row.label} className="space-y-1 border-b pb-3">
        <dt className="text-base font-medium">{row.label}</dt>
        {row.before !== undefined && <dd className="whitespace-pre-wrap text-sm text-muted-foreground [overflow-wrap:anywhere]">{text.previous}：{row.before}</dd>}
        <dd className="whitespace-pre-wrap text-base [overflow-wrap:anywhere]">{text.proposed}：{row.after}</dd>
      </div>)}
    </dl>
    {command.kind === 'archive' && <p className="text-base leading-relaxed">{text.archiveChildren}：{command.body.archive_children ? text.yes : text.no}<br />{text.reason}：{command.body.reason || text.emptyValue}</p>}
  </MutationReviewDialog>
}
