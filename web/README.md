# HomeBox web client

This is the same-origin React/TypeScript application served by the Go executable. The current screen checks real server liveness; it does not simulate inventory or expose unimplemented management actions. A typed/runtime-validated authentication adapter is implemented and tested, but account screens, a service worker, the offline replica, and camera flows are not shipped yet.

## Install and run

Use Linux/WSL2, Node 24.14.1 (see `../.node-version`), npm 11.14.1, and the Go version in `../server/go.mod`. From the repository root:

```sh
make setup-web build-web build
./bin/homebox db migrate --database ./data/homebox.sqlite
./bin/homebox serve --database ./data/homebox.sqlite --web-dir ./web/dist
```

Open exactly the printed `public_origin` (normally `http://127.0.0.1:8080`). Keep this incomplete build on loopback. The backend authentication and direct TLS/explicit proxy features are implemented; the React account screens and production deployment remain pending. See [HTTP Authentication and Transport](../docs/http-authentication.md). `serve` requires a migrated database and a nonempty frontend build and does not silently migrate data. Stop it with Ctrl+C.

For hot reload, the Go origin must match the browser's Vite origin, and Vite must preserve that Host while forwarding `/api`:

```sh
# Terminal 1: migrated private database and built frontend already exist.
./bin/homebox serve --database ./data/homebox.sqlite --web-dir ./web/dist \
  --public-origin http://127.0.0.1:5173

# Terminal 2: open exactly http://127.0.0.1:5173, not localhost or a LAN address.
npm --prefix web run dev -- --host 127.0.0.1 --port 5173 --strictPort
```

The default Vite proxy targets `http://127.0.0.1:8080`; it does not change Host or enable proxy-header trust. Do not reuse an unrelated service if either port is occupied. For another local port pair, update the Vite proxy target and Go public origin together. The adapter was additionally smoke-tested in Chromium through Vite and the actual Go binary on isolated ephemeral ports. This manual integration smoke is not part of the current Playwright suite and is not authentication-UI acceptance. The production runtime needs only the Go executable and `web/dist`, not Node.js, npm, or Python. Linux amd64/arm64 binaries are built with `make build-linux`; hardware execution is a separate acceptance gate.

## Checks

```sh
make check-web                 # Unit tests/coverage, generated types, lint, build budgets
make audit-web                 # Registry-backed audits; requires network access
make test-e2e                  # Build and drive the actual Go server with Chromium
npm --prefix web run api:generate
npm --prefix web run api:check
npm --prefix web test -- --coverage.enabled=false src/lib/api/client.test.ts
npm --prefix web run test:e2e -- --project=phone
```

On a new test machine, install the Playwright Chromium version before E2E testing:

```sh
npm exec --prefix web -- playwright install chromium
```

Linux browser system libraries may also be required; the disposable CI runner uses `playwright install --with-deps chromium`. Do not substitute desktop emulation for real iPhone/Android or QNAP evidence.

E2E tests compile the real Go executable, create a private disposable database, migrate it, serve the production frontend on loopback port 4179, and clean up after graceful shutdown. An already-running service on that port is never reused. Coverage includes 320/390/768/1440 widths and landscape, light/dark axe checks, keyboard/menu/focus behavior, reduced motion, malformed/slow responses, offline failure and reconnect, and deep-link recovery. Adverse API responses are explicitly intercepted only in the corresponding tests. Normal connectivity tests use the actual API.

Unit coverage gates apply to application logic, excluding official generated primitives, declaration files, the one-line utility re-export, and the browser entrypoint (exercised through E2E). They do not establish full-product coverage. Build checks bound initial static-import JavaScript to 250 KiB gzip and individual lazy entry chunks to 150 KiB gzip; actual device latency still requires measurement.

## Source and state boundaries

- `components/ui` comes from the official shadcn CLI 4.23.0, Radix base, Nova preset. Its upstream MIT notice is retained. Compose these primitives in feature components; do not create another base-control framework. Theme menu targets are 48 px; primary controls are at least 44 px.
- Tailwind semantic tokens provide paired light/dark colors and local system/CJK fonts. There are no hosted fonts, CDN assets, analytics, or third-party requests in the application.
- React Router owns routes. TanStack Query owns temporary server-read state; Zustand stores only the theme preference. Neither is an authoritative inventory store.
- The shared generated OpenAPI client uses same-origin credentials/no-store and rejects redirects. Reads have an eight-second deadline; auth writes have 30 seconds. Pre-canceled requests are not sent, late canceled results are discarded, and all timers/listeners are cleaned up. Cancel/timeout after a write was sent does not prove the server rolled it back: treat the result as uncertain and reconcile explicitly, never silently replay it.
- The seven auth adapters validate success payloads with strict Zod schemas. They accept CSRF/idempotency/signal explicitly, send the package's application version, never read cookies or retain tokens in storage, and never secretly refresh/retry/replay a write. CSRF/grants remain the caller's memory-only responsibility. Errors retain only allowlisted codes, status, bounded Retry-After, and safe request IDs, not arbitrary server messages/details/transport causes. `validAuthText` counts Unicode code points and rejects lone surrogates without trimming/normalizing credentials.
- Queries can recheck after reconnect. Mutations explicitly use `networkMode: always` and no retry so an offline write cannot be paused and silently replayed. This is tested. Future writes still require explicit confirmation and authoritative server validation.
- Generated `src/lib/api/schema.d.ts` is committed and checked for drift. Compile fixtures deliberately use `skipLibCheck: false`. JSON Schema constraints such as integer bounds and conditional OCR visibility still require runtime validation; TypeScript alone cannot enforce them.
- UI strings use stable keys in `src/lib/messages.ts`; the default language is Traditional Chinese. Canonical documentation remains English.

## Dependency audit status

The lockfile is exact and installation disables lifecycle scripts. As of 2026-11-05, `npm audit --omit=dev` reports zero vulnerabilities. The full audit reports two low and four moderate findings in development tooling chains: deepmerge via shadcn/registry ([GHSA-gcqx-w48m-w2h4](https://github.com/advisories/GHSA-gcqx-w48m-w2h4)), and minimatch via ts-morph ([GHSA-g3hv-75wg-m249](https://github.com/advisories/GHSA-g3hv-75wg-m249)). These are current registry classifications, not a claim that the form-tool installation fixed earlier advisories. They are not application-runtime dependencies, but remain unresolved development-tool exposure.

Use the generators only with trusted local contracts and the official shadcn registry, not untrusted downloaded configuration. CI fails on any runtime advisory and on high/critical full-tree advisories; lower developer-tool findings remain visible. Do not use `npm audit fix --force` or incompatible global overrides: the suggested downgrades change supported APIs, and Redocly's callable minimatch API is not compatible with minimatch 11's named-export API. Recheck upstream compatible fixes before release.
