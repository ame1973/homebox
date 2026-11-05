import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { ApiError } from '@/lib/api/client'
import { messages } from '@/lib/messages'

const text = messages.taxonomy
const common = messages.locations
function guidance(error: unknown): string {
  if (!(error instanceof ApiError)) return common.busy
  switch (error.code) {
    case 'CATEGORY_CYCLE': return text.cycle
    case 'CATEGORY_HAS_CHILDREN': return text.children
    case 'TAG_NAME_CONFLICT': return text.tagConflict
    case 'VALIDATION_ERROR': case 'INVALID_REVISION': case 'INVALID_TAXONOMY_URL': case 'PRECONDITION_REQUIRED': return common.validation
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

export function TaxonomyError({ error }: { error: unknown }) {
  return <Alert role="alert" className="p-4"><AlertTitle>{text.failed}</AlertTitle><AlertDescription className="text-base leading-relaxed"><p>{guidance(error)}</p>
    {error instanceof ApiError && error.retryAfterSeconds !== undefined && <p>{messages.auth.seconds}{error.retryAfterSeconds}</p>}
  </AlertDescription></Alert>
}
