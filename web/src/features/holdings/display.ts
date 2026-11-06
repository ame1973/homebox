import type { Item } from '@/lib/api/catalog'
import type { Unit } from '@/lib/api/holdings'
import type { Location } from '@/lib/api/locations'
import type { LocationTree } from '@/features/locations/tree'
import { locationPath } from '@/features/locations/tree'
import { messages } from '@/lib/messages'
import type { HoldingCommand } from './commands'

const text = messages.holdings
export function holdingPath(tree: LocationTree, id: string | null): readonly Location[] | undefined {
  return id === null ? [] : tree.byID.has(id) ? locationPath(tree, id) : undefined
}
export function sameHoldingPath(before: readonly Location[] | undefined, current: readonly Location[] | undefined): boolean {
  if (!before || !current) return before === current
  return before.length === current.length && before.every((location, index) =>
    (Object.keys(location) as (keyof Location)[]).every((key) => location[key] === current[index]![key]))
}
// Owner labels are review context, not a second Unit precondition. An unrelated
// Item-counter advance must not invalidate an unchanged independent Unit proof.
export function holdingCommandCurrent(command: HoldingCommand, item: Item | undefined, unit: Unit | undefined, tree: LocationTree | undefined): boolean {
  if (!item || !tree || (Object.keys(command.owner) as (keyof Item)[]).some((key) => !('subject' in command && key === 'revision') && command.owner[key] !== item[key])) return false
  if ('subject' in command && (!unit || (Object.keys(command.subject) as (keyof Unit)[]).some((key) => command.subject[key] !== unit[key]))) return false
  const paths = 'balances' in command ? [command.source, command.target] : command.kind === 'create-unit' ? [command.destination]
    : command.kind === 'move-unit' ? [command.location, command.destination] : [command.location]
  return paths.every((path) => sameHoldingPath(path, holdingPath(tree, path.at(-1)?.id ?? null)))
}
export function holdingValue(key: string, value: unknown): string {
  if (key === 'status' && typeof value === 'string' && Object.hasOwn(text.statuses, value)) return text.statuses[value as keyof typeof text.statuses]
  if (key === 'disposal_kind' && typeof value === 'string' && Object.hasOwn(text.disposalKinds, value)) return text.disposalKinds[value as keyof typeof text.disposalKinds]
  if (value === null) return key === 'location_id' ? text.unknownLocation : messages.catalog.notProvided
  return typeof value === 'string' ? JSON.stringify(value) : String(value)
}
export function baseQuantity(quantity: number, unit: string) {
  return `${new Intl.NumberFormat('zh-TW', { maximumFractionDigits: 0 }).format(quantity)} ${JSON.stringify(unit)}`
}
