import { messages } from '@/lib/messages'

const text = messages.catalog
export function catalogDisplay(key: string, value: string | number | null): string {
  if (value === null) return text.notProvided
  if (value === '') return text.suppliedEmpty
  if (key === 'tracking_mode') return value === 'individual' ? text.individual : text.quantity
  if (key === 'status') return value === 'active' ? text.active : text.archived
  return String(value)
}
