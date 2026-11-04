import { lazy, Suspense } from 'react'
import { Link, Route, Routes } from 'react-router'
import { Package } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { ThemeMenu } from '@/components/theme-menu'
import { messages } from '@/lib/messages'

const ConnectionPage = lazy(() => import('@/features/system/connection-page'))

export default function App() {
  return (
    <div className="safe-page mx-auto flex min-h-dvh max-w-5xl flex-col">
      <a href="#main" className="sr-only z-50 rounded-md bg-primary p-3 text-primary-foreground focus:not-sr-only focus:absolute focus:top-2">{messages.app.skip}</a>
      <header className="flex items-center justify-between gap-4 border-b py-4">
        <Link to="/" aria-label={messages.app.home} className="flex min-h-11 items-center gap-3 rounded-md text-xl font-semibold focus-visible:outline-2 focus-visible:outline-offset-4 focus-visible:outline-ring">
          <Package className="size-7 text-primary" aria-hidden="true" />HomeBox
        </Link>
        <ThemeMenu />
      </header>
      <main id="main" tabIndex={-1} className="flex-1 outline-none">
        <Suspense fallback={<p role="status" className="py-12">{messages.app.loading}</p>}>
          <Routes>
            <Route path="/" element={<ConnectionPage />} />
            <Route path="*" element={
              <section className="space-y-6 py-12">
                <h1 className="text-3xl font-semibold">{messages.app.notFound}</h1>
                <Button asChild className="min-h-11 text-base focus-visible:ring-ring"><Link to="/">{messages.app.returnHome}</Link></Button>
              </section>
            } />
          </Routes>
        </Suspense>
      </main>
      <footer className="border-t py-6 text-sm leading-relaxed text-muted-foreground">{messages.app.development}</footer>
    </div>
  )
}
