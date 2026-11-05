import { messages } from '@/lib/messages'
import type { Alias, Attribute, Barcode } from '@/lib/api/catalog-links'

export type OwnedRow = Barcode | Alias | Attribute
type OwnedFieldName = keyof typeof messages.owned.fields
export type OwnedFieldsValue = Partial<Record<OwnedFieldName, string>>
export function ownedFields(value: OwnedFieldsValue) {
  return (Object.keys(messages.owned.fields) as OwnedFieldName[]).flatMap((key) => value[key] === undefined ? [] : [{ key, label: messages.owned.fields[key], value: JSON.stringify(value[key]) }])
}

const text = messages.catalog
export function catalogDisplay(key: string, value: string | number | null): string {
  if (value === null) return text.notProvided
  if (value === '') return text.suppliedEmpty
  if (key === 'tracking_mode') return value === 'individual' ? text.individual : text.quantity
  if (key === 'status') return value === 'active' ? text.active : text.archived
  return String(value)
}
