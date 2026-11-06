import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { ApiError } from '@/lib/api/client'
import { messages } from '@/lib/messages'
import { catalogErrorGuidance } from './display'

const text = messages.catalog
export function CatalogError({ error }: { error: unknown }) {
  return <Alert role="alert" className="p-4"><AlertTitle>{text.failed}</AlertTitle><AlertDescription className="text-base leading-relaxed"><p>{catalogErrorGuidance(error)}</p>
    {error instanceof ApiError && error.retryAfterSeconds !== undefined && <p>{messages.auth.seconds}{error.retryAfterSeconds}</p>}
  </AlertDescription></Alert>
}
