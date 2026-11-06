import { lazy, Suspense, useEffect, useRef } from 'react'
import { Link, NavLink, Route, Routes, useLocation } from 'react-router'
import { MapPin, Package, Settings, UserRound } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { ThemeMenu } from '@/components/theme-menu'
import { AppErrorBoundary } from '@/components/app-error-boundary'
import { messages } from '@/lib/messages'

const ConnectionPage = lazy(() => import('@/features/system/connection-page'))
const AccountPage = lazy(() => import('@/features/auth/account-page'))
const SettingsPage = lazy(() => import('@/features/taxonomy/settings-page'))
const CatalogPage = lazy(() => import('@/features/catalog/catalog-page'))
const OwnedPage = lazy(() => import('@/features/catalog/owned-page'))
const HoldingPage = lazy(() => import('@/features/holdings/holding-page'))
const UnitAlias = lazy(() => import('@/features/holdings/holding-page').then((module) => ({ default: module.UnitAlias })))
const LocationsPage = lazy(() => import('@/features/locations/locations-page'))
const LocationAlias = lazy(() => import('@/features/locations/locations-page').then((module) => ({ default: module.LocationAlias })))

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
        <nav aria-label={messages.app.navigation} className="flex max-w-full flex-wrap items-center gap-2">
          <Button variant="outline" asChild className="min-h-11 h-auto gap-2 whitespace-normal text-base aria-[current=page]:border-primary aria-[current=page]:bg-muted"><NavLink to="/locations"><MapPin className="size-4" aria-hidden="true" />{messages.locations.nav}</NavLink></Button>
          <Button variant="outline" asChild className="min-h-11 h-auto gap-2 whitespace-normal text-base aria-[current=page]:border-primary aria-[current=page]:bg-muted"><NavLink to="/items"><Package className="size-4" aria-hidden="true" />{messages.catalog.nav}</NavLink></Button>
          <Button variant="outline" asChild className="min-h-11 gap-2 text-base"><Link to="/login"><UserRound className="size-4" aria-hidden="true" />{messages.app.account}</Link></Button>
          <Button variant="outline" asChild className="min-h-11 h-auto gap-2 whitespace-normal text-base aria-[current=page]:border-primary aria-[current=page]:bg-muted"><NavLink to="/settings"><Settings className="size-4" aria-hidden="true" />{messages.settings.nav}</NavLink></Button>
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
              <Route path="/settings" element={<SettingsPage key={location.key} mode="home" />} />
              <Route path="/settings/categories" element={<SettingsPage key={location.key} kind="categories" mode="list" />} />
              <Route path="/settings/categories/new" element={<SettingsPage key={location.key} kind="categories" mode="create" />} />
              <Route path="/settings/categories/:id/edit" element={<SettingsPage key={location.key} kind="categories" mode="edit" />} />
              <Route path="/settings/tags" element={<SettingsPage key={location.key} kind="tags" mode="list" />} />
              <Route path="/settings/tags/new" element={<SettingsPage key={location.key} kind="tags" mode="create" />} />
              <Route path="/settings/tags/:id/edit" element={<SettingsPage key={location.key} kind="tags" mode="edit" />} />
              <Route path="/locations" element={<LocationsPage key={location.key} mode="list" />} />
              <Route path="/locations/new" element={<LocationsPage key={location.key} mode="create" />} />
              <Route path="/locations/:id/view" element={<LocationsPage key={location.key} mode="detail" />} />
              <Route path="/locations/:id" element={<LocationsPage key={location.key} mode="detail" />} />
              <Route path="/locations/:id/edit" element={<LocationsPage key={location.key} mode="edit" />} />
              <Route path="/items" element={<CatalogPage key={location.key} mode="list" />} />
              <Route path="/items/new" element={<CatalogPage key={location.key} mode="create" />} />
              <Route path="/items/:id/view" element={<CatalogPage key={location.key} mode="detail" />} />
              <Route path="/items/:id/edit" element={<CatalogPage key={location.key} mode="edit" />} />
              <Route path="/items/:id" element={<CatalogPage key={location.key} mode="detail" />} />
              {(['barcodes', 'aliases', 'attributes'] as const).map((kind) => <Route key={kind}>
                <Route path={`/items/:id/${kind}`} element={<OwnedPage key={location.key} kind={kind} mode="list" />} />
                <Route path={`/items/:id/${kind}/new`} element={<OwnedPage key={location.key} kind={kind} mode="create" />} />
                <Route path={`/items/:id/${kind}/:rowID/edit`} element={<OwnedPage key={location.key} kind={kind} mode="edit" />} />
              </Route>)}
              <Route path="/items/:id/tags" element={<OwnedPage key={location.key} kind="tags" mode="list" />} />
              <Route path="/items/:id/holdings" element={<HoldingPage key={location.key} mode="item" />} />
              <Route path="/items/:id/units/new" element={<HoldingPage key={location.key} mode="create-unit" />} />
              <Route path="/units/:id" element={<HoldingPage key={location.key} mode="unit" />} />
              <Route path="/units/:id/edit" element={<HoldingPage key={location.key} mode="edit-unit" />} />
              <Route path="/units/:id/move" element={<HoldingPage key={location.key} mode="move-unit" />} />
              <Route path="/units/:id/status" element={<HoldingPage key={location.key} mode="status-unit" />} />
              {(['receive', 'consume', 'transfer', 'adjust'] as const).map((action) => <Route key={action} path={`/items/:id/stock/${action}`} element={<HoldingPage key={location.key} mode="stock" action={action} />} />)}
              <Route path="/u/:id" element={<UnitAlias />} />
              <Route path="/l/:id" element={<LocationAlias />} />
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
