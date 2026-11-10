import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { ApiError } from '@/lib/api/client'
import { messages } from '@/lib/messages'
import { CatalogError } from '@/features/catalog/catalog-error'

const text = messages.media
export function MediaError({ error }: { error: unknown }) {
  const codes = { MEDIA_COVER_CONFLICT: text.coverConflict, INVALID_MEDIA_URL: messages.locations.validation,
    NOT_FOUND: text.missingHelp, INVALID_RESPONSE: text.invalidRead, INVALID_CURSOR: text.invalidRead }
  const help = error instanceof ApiError && Object.hasOwn(codes, error.code) ? codes[error.code as keyof typeof codes] : undefined
  if (!help) return <CatalogError error={error} />
  return <Alert role="alert" className="p-4"><AlertTitle>{text.failed}</AlertTitle><AlertDescription className="text-base leading-relaxed"><p>{help}</p>
    {error instanceof ApiError && error.retryAfterSeconds !== undefined && <p>{messages.auth.seconds}{error.retryAfterSeconds}</p>}
  </AlertDescription></Alert>
}
