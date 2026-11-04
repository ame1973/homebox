# HomeBox web client

This is the same-origin React/TypeScript application served by the Go executable. The current screen checks real server liveness; it does not simulate inventory or expose unimplemented management actions. No service worker, offline replica, authentication, or camera flow is shipped yet.

## Install and run

Use Linux/WSL2, Node 24.14.1 (see `../.node-version`), npm 11.14.1, and the Go version in `../server/go.mod`. From the repository root:

```sh
make setup-web build-web build
./bin/homebox db migrate --database ./data/homebox.sqlite
./bin/homebox serve --database ./data/homebox.sqlite --web-dir ./web/dist
```

Open `http://127.0.0.1:8080`. Keep this development build on loopback; authentication and production HTTPS deployment are not implemented yet. `serve` requires a migrated database and a nonempty frontend build and does not silently migrate data. Stop it with Ctrl+C.

For hot reload, keep that server running and run `npm --prefix web run dev` in another terminal. Vite binds to loopback and proxies `/api` to the Go server. The production runtime needs only the Go executable and `web/dist`, not Node.js, npm, or Python. Linux amd64/arm64 binaries are built with `make build-linux`; hardware execution is a separate acceptance gate.

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
- The health adapter uses generated OpenAPI types, validates the actual response with Zod, uses same-origin credentials/no-store, rejects redirects, and honors cancellation and an eight-second deadline.
- Queries can recheck after reconnect. Mutations explicitly use `networkMode: always` and no retry so an offline write cannot be paused and silently replayed. This is tested. Future writes still require explicit confirmation and authoritative server validation.
- Generated `src/lib/api/schema.d.ts` is committed and checked for drift. Compile fixtures deliberately use `skipLibCheck: false`. JSON Schema constraints such as integer bounds and conditional OCR visibility still require runtime validation; TypeScript alone cannot enforce them.
- UI strings use stable keys in `src/lib/messages.ts`; the default language is Traditional Chinese. Canonical documentation remains English.

## Dependency audit status

The lockfile is exact and installation disables lifecycle scripts. As of 2026-11-04, `npm audit --omit=dev` reports zero vulnerabilities. The full audit reports two low and six moderate findings in development tooling chains: deepmerge via shadcn/registry ([GHSA-gcqx-w48m-w2h4](https://github.com/advisories/GHSA-gcqx-w48m-w2h4)), and minimatch via Redocly/openapi-typescript and ts-morph ([GHSA-g3hv-75wg-m249](https://github.com/advisories/GHSA-g3hv-75wg-m249)). They are not application-runtime dependencies, but remain unresolved development-tool exposure.

Use the generators only with trusted local contracts and the official shadcn registry, not untrusted downloaded configuration. CI fails on any runtime advisory and on high/critical full-tree advisories; lower developer-tool findings remain visible. Do not use `npm audit fix --force` or incompatible global overrides: the suggested downgrades change supported APIs, and Redocly's callable minimatch API is not compatible with minimatch 11's named-export API. Recheck upstream compatible fixes before release.
