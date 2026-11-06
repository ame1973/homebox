import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { ApiError } from '@/lib/api/client'
import { messages } from '@/lib/messages'
import { CatalogError } from '@/features/catalog/catalog-error'

const text = messages.holdings
export function HoldingError({ error }: { error: unknown }) {
  const codes = {
    TRACKING_MODE_CONFLICT: text.trackingError, INSUFFICIENT_STOCK: text.insufficient, QUANTITY_OVERFLOW: text.overflow,
    LOCATION_ARCHIVED: text.archivedError, UNIT_HAS_HISTORY: text.historyError, UNIT_DISPOSED: text.disposedError, ASSET_LIFECYCLE_REQUIRED: text.assetError,
    INVALID_HOLDING_URL: messages.locations.validation,
  }
  const help = error instanceof ApiError && Object.hasOwn(codes, error.code) ? codes[error.code as keyof typeof codes] : undefined
  if (!help) return <CatalogError error={error} />
  return <Alert role="alert" className="p-4"><AlertTitle>{text.failed}</AlertTitle><AlertDescription className="text-base leading-relaxed"><p>{help}</p>
    {error instanceof ApiError && error.retryAfterSeconds !== undefined && <p>{messages.auth.seconds}{error.retryAfterSeconds}</p>}
  </AlertDescription></Alert>
}
