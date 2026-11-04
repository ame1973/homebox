import { lazy, Suspense, useEffect, useRef } from 'react'
import { Link, Route, Routes, useLocation } from 'react-router'
import { Package, UserRound } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { ThemeMenu } from '@/components/theme-menu'
import { AppErrorBoundary } from '@/components/app-error-boundary'
import { messages } from '@/lib/messages'

const ConnectionPage = lazy(() => import('@/features/system/connection-page'))
const AccountPage = lazy(() => import('@/features/auth/account-page'))

export default function App() {
  const location = useLocation()
  const main = useRef<HTMLElement>(null)
  const previousPath = useRef(location.pathname)
  useEffect(() => {
    if (previousPath.current !== location.pathname) main.current?.focus()
    previousPath.current = location.pathname
  }, [location.pathname])
  return (
    <div className="safe-page mx-auto flex min-h-dvh max-w-5xl flex-col">
      <a href="#main" onClick={(event) => { event.preventDefault(); main.current?.focus() }} className="sr-only z-50 rounded-md bg-primary p-3 text-primary-foreground focus:not-sr-only focus:absolute focus:top-2">{messages.app.skip}</a>
      <header className="flex flex-wrap items-center justify-between gap-4 border-b py-4">
        <Link to="/" aria-label={messages.app.home} className="flex min-h-11 items-center gap-3 rounded-md text-xl font-semibold focus-visible:outline-2 focus-visible:outline-offset-4 focus-visible:outline-ring">
          <Package className="size-7 text-primary" aria-hidden="true" />HomeBox
        </Link>
        <nav aria-label={messages.app.account} className="flex max-w-full flex-wrap items-center gap-2">
          <Button variant="outline" asChild className="min-h-11 gap-2 text-base"><Link to="/login"><UserRound className="size-4" aria-hidden="true" />{messages.app.account}</Link></Button>
          <ThemeMenu />
        </nav>
      </header>
      <main id="main" ref={main} tabIndex={-1} className="flex-1 outline-none">
        <AppErrorBoundary key={location.pathname}>
          <Suspense fallback={<p role="status" className="py-12">{messages.app.loading}</p>}>
            <Routes>
              <Route path="/" element={<ConnectionPage />} />
              <Route path="/login" element={<AccountPage key={location.key} mode="login" />} />
              <Route path="/setup" element={<AccountPage key={location.key} mode="setup" />} />
              <Route path="/settings/account" element={<AccountPage key={location.key} mode="account" />} />
              <Route path="*" element={
                <section className="space-y-6 py-12">
                  <h1 className="text-3xl font-semibold">{messages.app.notFound}</h1>
                  <Button asChild className="min-h-11 text-base focus-visible:ring-ring"><Link to="/">{messages.app.returnHome}</Link></Button>
                </section>
              } />
            </Routes>
          </Suspense>
        </AppErrorBoundary>
      </main>
      <footer className="border-t py-6 text-sm leading-relaxed text-muted-foreground">{messages.app.development}</footer>
    </div>
  )
}
