import { useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import { flushSync } from 'react-dom'
import { Link, useLocation } from 'react-router'
import { useQueryClient } from '@tanstack/react-query'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { mutationControl as control } from '@/components/mutation-review'
import { catalogReturnPath } from '@/lib/catalog-navigation'
import { messages } from '@/lib/messages'
import { subscribeSessionCleared } from '@/lib/session-lifecycle'

export function SessionClearBoundary({ children }: { children: ReactNode }) {
  const client = useQueryClient(), route = useLocation()
  const [cleared, setCleared] = useState(false)
  const latched = useRef(false)
  useLayoutEffect(() => subscribeSessionCleared((source) => {
    if (latched.current) return
    latched.current = true
    void client.cancelQueries()
    client.clear()
    // A remote browser event can otherwise leave a reviewed portal visible
    // until React's next commit. Local signals can originate in a React effect.
    if (source === 'remote') flushSync(() => setCleared(true))
    else setCleared(true)
  }), [client])
  if (cleared) {
    const returnTo = catalogReturnPath(route.pathname + route.search) ?? '/items'
    const notice = typeof route.state?.scanToken === 'string' && route.state.scanToken.length === 36 ? 'scanHandoffLost' : 'expired'
    const text = messages.owned
    return <div className="max-w-3xl py-8"><Alert role="alert" className="space-y-3 p-4"><AlertTitle>{text.sessionCleared}</AlertTitle><AlertDescription className="space-y-3 text-base leading-relaxed"><p>{text.sessionClearedHelp}</p><p>{text.sessionClearedUncertain}</p><Button asChild variant="outline" className={control}><Link to="/login" state={{ notice, returnTo }}>{messages.scan.signIn}</Link></Button></AlertDescription></Alert></div>
  }
  return children
}
