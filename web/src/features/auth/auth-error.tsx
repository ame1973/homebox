import { TriangleAlert } from 'lucide-react'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { ApiError } from '@/lib/api/auth'
import { messages } from '@/lib/messages'

function authErrorMessage(error: unknown, setup = false): string {
  const text = messages.auth
  if (!(error instanceof ApiError)) return text.busy
  switch (error.code) {
    case 'AUTHENTICATION_REQUIRED': return setup ? text.bootstrapRejected : text.credentials
    case 'CREDENTIALS_REJECTED': return text.oldPasswordError
    case 'SETUP_CLOSED': return text.setupClosed
    case 'CSRF_REJECTED': case 'ORIGIN_REJECTED': return text.forbidden
    case 'CLIENT_UPGRADE_REQUIRED': return text.upgrade
    case 'IDEMPOTENCY_CONFLICT': return text.conflict
    case 'PASSWORD_CHANGE_REQUIRED': return text.mandatoryHelp
    case 'VALIDATION_ERROR': return text.validation
    case 'RATE_LIMITED': return text.rate
    case 'NETWORK_ERROR': return text.network
    case 'REQUEST_ABORTED': case 'REQUEST_TIMEOUT': case 'INVALID_RESPONSE': return text.uncertain
    default: return text.busy
  }
}

export function AuthError({ error, setup = false }: { error: unknown; setup?: boolean }) {
  return (
    <Alert variant="destructive" className="p-4 text-base">
      <TriangleAlert aria-hidden="true" />
      <AlertTitle>{messages.auth.failed}</AlertTitle>
      <AlertDescription className="text-base leading-relaxed">
        {authErrorMessage(error, setup)}
        {error instanceof ApiError && error.retryAfterSeconds !== undefined && <p>{messages.auth.seconds}{error.retryAfterSeconds}</p>}
      </AlertDescription>
    </Alert>
  )
}
