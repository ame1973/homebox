import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { ApiError } from '@/lib/api/client'
import { messages } from '@/lib/messages'
import { catalogErrorGuidance } from '@/features/catalog/display'

export function AssetError({ error }: { error: unknown }) {
  const text = messages.assets
  const codes = { ASSET_CURRENCY_CONFLICT: text.currencyConflict, PURCHASE_CURRENCY_CONFLICT: text.currencyConflict, ASSET_COST_BASIS_CONFLICT: text.basisHelp,
    ASSET_TOTAL_OVERFLOW: text.totalOverflow, ASSET_HISTORY_LOCKED: text.historyLocked, ASSET_LIFECYCLE_REQUIRED: text.lifecycleRequired,
    INVALID_ASSET_URL: messages.locations.validation, INVALID_RESPONSE: text.invalidRead, INVALID_QUERY: text.invalidRead, INVALID_CURSOR: text.invalidRead, NOT_FOUND: text.missingHelp }
  const help = error instanceof ApiError && Object.hasOwn(codes, error.code) ? codes[error.code as keyof typeof codes] : catalogErrorGuidance(error)
  return <Alert role="alert" className="p-4"><AlertTitle>{text.failed}</AlertTitle><AlertDescription className="text-base leading-relaxed"><p>{help}</p>
    {error instanceof ApiError && error.retryAfterSeconds !== undefined && <p>{messages.auth.seconds}{error.retryAfterSeconds}</p>}
  </AlertDescription></Alert>
}
