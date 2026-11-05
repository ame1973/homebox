import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { ApiError } from '@/lib/api/client'
import { messages } from '@/lib/messages'

const text = messages.locations

function guidance(error: unknown): string {
  if (!(error instanceof ApiError)) return text.busy
  switch (error.code) {
    case 'VALIDATION_ERROR': case 'INVALID_REVISION': case 'PRECONDITION_REQUIRED': return text.validation
    case 'REVISION_CONFLICT': return text.conflict
    case 'LOCATION_CODE_CONFLICT': return text.codeConflict
    case 'LOCATION_CYCLE': return text.cycle
    case 'LOCATION_ARCHIVED': return text.archivedDestination
    case 'LOCATION_HAS_CHILDREN': return text.children
    case 'LOCATION_IN_USE': return text.inUse
    case 'IDEMPOTENCY_CONFLICT': case 'IDEMPOTENCY_EXPIRED': return text.keyConflict
    case 'INVALID_RESPONSE': case 'INVALID_CURSOR': case 'INVALID_QUERY': return text.invalidRead
    case 'NOT_FOUND': return text.missingHelp
    case 'NETWORK_ERROR': case 'REQUEST_TIMEOUT': case 'REQUEST_ABORTED': return text.network
    case 'CLIENT_UPGRADE_REQUIRED': return text.upgrade
    case 'RATE_LIMITED': return text.rate
    case 'CSRF_REJECTED': case 'ORIGIN_REJECTED': case 'AUTHENTICATION_REQUIRED': case 'PASSWORD_CHANGE_REQUIRED': return text.forbidden
    default: return text.busy
  }
}

export function LocationError({ error }: { error: unknown }) {
  return <Alert role="alert" className="p-4"><AlertTitle>{text.failed}</AlertTitle><AlertDescription className="text-base leading-relaxed">
    <p>{guidance(error)}</p>
    {error instanceof ApiError && error.retryAfterSeconds !== undefined && <p>{messages.auth.seconds}{error.retryAfterSeconds}</p>}
  </AlertDescription></Alert>
}
