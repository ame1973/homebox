import { messages } from '@/lib/messages'
import { ApiError } from '@/lib/api/client'
import type { Alias, Attribute, Barcode } from '@/lib/api/catalog-links'

export type OwnedRow = Barcode | Alias | Attribute
type OwnedFieldName = keyof typeof messages.owned.fields
export type OwnedFieldsValue = Partial<Record<OwnedFieldName, string>>
export function ownedFields(value: OwnedFieldsValue) {
  return (Object.keys(messages.owned.fields) as OwnedFieldName[]).flatMap((key) => value[key] === undefined ? [] : [{ key, label: messages.owned.fields[key], value: JSON.stringify(value[key]) }])
}

const text = messages.catalog
export function catalogErrorGuidance(error: unknown): string {
  const common = messages.locations
  if (!(error instanceof ApiError)) return common.busy
  switch (error.code) {
    case 'ITEM_HAS_HISTORY': return text.history
    case 'BARCODE_CONFLICT': return messages.owned.barcodeConflict
    case 'ALIAS_CONFLICT': return messages.owned.aliasConflict
    case 'VALIDATION_ERROR': case 'INVALID_REVISION': case 'INVALID_CATALOG_URL': case 'PRECONDITION_REQUIRED': return common.validation
    case 'REVISION_CONFLICT': return common.conflict
    case 'IDEMPOTENCY_CONFLICT': case 'IDEMPOTENCY_EXPIRED': return common.keyConflict
    case 'INVALID_RESPONSE': case 'INVALID_CURSOR': case 'INVALID_QUERY': return text.invalidRead
    case 'NOT_FOUND': return text.missingHelp
    case 'NETWORK_ERROR': case 'REQUEST_TIMEOUT': case 'REQUEST_ABORTED': return common.network
    case 'CLIENT_UPGRADE_REQUIRED': return common.upgrade
    case 'RATE_LIMITED': return common.rate
    case 'CSRF_REJECTED': case 'ORIGIN_REJECTED': case 'AUTHENTICATION_REQUIRED': case 'PASSWORD_CHANGE_REQUIRED': return common.forbidden
    default: return common.busy
  }
}
export function catalogDisplay(key: string, value: string | number | null): string {
  if (value === null) return text.notProvided
  if (value === '') return text.suppliedEmpty
  if (key === 'tracking_mode') return value === 'individual' ? text.individual : text.quantity
  if (key === 'status') return value === 'active' ? text.active : text.archived
  return String(value)
}
