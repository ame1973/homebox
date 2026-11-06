# HomeBox

A self-hosted, single-administrator household inventory and asset-management PWA for private LAN/VPN access.

> **Under development. Not ready for production use.** Specification revision v0.9.3 is not an application release. Deployment and usage instructions will be added alongside verified implementations; unimplemented commands are not presented as working features.

## Product scope

- Arbitrarily nested locations, individually tracked units and quantity stock, location/unit QR labels, and shared commercial barcodes.
- A feature-equivalent phone, tablet, and desktop PWA with real live camera scanning, multiple-photo capture, and image search.
- Reviewed OCR/FTS, an optional local AI worker, versioned models, and vector search.
- Purchases, asset lifecycle, per-day/per-use costs, and currency-separated analytics.
- A full offline read replica and explicitly reviewed local drafts: no automatic inventory or financial mutation replay.
- Secure authentication, transactions/idempotency/auditing, verified backup/restore, import/export, and QNAP deployment operations.

## Architecture

Go HTTP server + SQLite WAL/FTS5 + same-origin static React/TypeScript/Vite frontend. The UI uses shadcn/ui/Radix/Tailwind. Private database and media files live on a local filesystem. Production deployments do not require Node.js, and ordinary CRUD must continue with the optional AI worker stopped.

## Database CLI (available now)

The database-maintenance CLI is functional; it is not yet the complete HTTP/PWA application.

```sh
make build
./bin/homebox version
./bin/homebox db migrate --database ./data/homebox.sqlite
./bin/homebox db check --database ./data/homebox.sqlite
```

See [Database Operations](docs/database.md) for private storage permissions, explicit legacy adoption, precision mappings, verified pre-migration snapshots, failure recovery, timeouts, and Linux amd64/arm64 builds. Additive migrations retain independent location, taxonomy, catalog-link, physical Unit/Stock and purchase/allocation identities without silently rewriting historical strings, rescaling quantities or claiming full sync readiness. The [owned catalog services and fourteen HTTP operations](docs/catalog-links.md) enforce independent row identity, real barcode validation, complete tag-set replacement and scoped signed collection reads with atomic owner/search updates; typed owned browser adapters and responsive reviewed maintenance screens are available. Local migration snapshots are sensitive, unencrypted database-only recovery files, not full encrypted off-host backups.

## Private setup credential (available now)

With the server stopped and the database already migrated, create a new private setup-token file:

```sh
./bin/homebox auth bootstrap --database ./data/homebox.sqlite --output ./data/bootstrap.token
```

The command prints only the absolute file path and expiry, not the secret. The output directory must be private, the new file is mode `0600`, and existing files/symlinks are never overwritten. The credential expires after 30 minutes and is disabled after successful administrator provisioning. Start the server, open `/setup` on its printed origin, and enter the private code plus the initial account/password. Setup does not sign in automatically: use `/login`, complete the required password change at `/settings/account`, then sign in again with the new password. See [HTTP Authentication and Transport](docs/http-authentication.md) and [Security](docs/security.md) for delivery, failure, and explicit retry behavior. The wider product is still under development.

## Offline account recovery (available now)

A local filesystem owner can replace a forgotten or unsupported legacy password while the server is stopped:

```sh
./bin/homebox auth reset-password --database ./data/homebox.sqlite \
  --output ./data/recovery-password.txt --confirm RESET_PASSWORD
```

The command requires an already provisioned account and explicit confirmation. It generates a random temporary password into a new mode-`0600` private file, prints only account/file/revision metadata, revokes all sessions, and requires password change after the next login. Existing files are never overwritten. Recovery is deliberately unavailable in normal serving-store mode and has no HTTP route. Read the file through a trusted local channel; do not paste secrets into logs or issues. After restarting the server, sign in at `/login` with the temporary password, complete the forced password change, and sign in again. There is deliberately no browser password-reset endpoint.

## Local web application (available now)

The React interface provides real server connectivity, administrator setup/login/password-change/logout, and location management. Open `/locations` after completing the required initial password rotation to browse actual locations, create/edit metadata, move branches, archive/restore, or delete unused leaves. Every location write has an explicit review; conflicts and uncertain results never trigger automatic submission. The same routes and capabilities work on phone, tablet, and desktop, with system/light/dark appearance. See [Location Rules and Usage](docs/locations.md) for the workflow and its remaining contents/photo/history/QR limits. Open `/settings` to manage real category/tag hierarchies and exact names, with reviewed creation, partial edits, leaf-category deletion and tag detachment. See [Categories and Tags](docs/taxonomy.md) for original proofs, conflict adoption, safe retries and item-assignment workflows. Other inventory domains are still being built; no invented inventory data is displayed.

Open `/items` for real catalog metadata, complete server-side filters and signed pages. Create a name/classification/brand/model/measurement record, edit only changed fields, or explicitly archive, restore or delete an unused item. Every write requires review; null versus supplied-empty text and original versions remain explicit. This does not receive stock or create a physical unit/purchase. See [Catalog Item Rules](docs/catalog.md) for safe retries, history guards and the remaining photo-assisted/bulk/import workflows.

From item detail, open the real barcode, alias, attribute or Tag assignment screen. Add/edit/delete independent rows or explicitly replace the complete Tag set, with original Item-versus-row proofs, exact before/after reviews, manual conflict adoption and original-key unknown-result reconciliation. Larger legacy membership remains complete, not truncated to the new-set limit. See [Catalog Links and Tag Membership](docs/catalog-links.md) for routes and usage. These metadata screens do not include camera/scanning or implicitly receive inventory.

From item detail, open its real Unit list or location quantity balances. All devices share reviewed Unit creation/serial-note editing/movement/status/deletion and exact base-count receipt/consumption/transfer/absolute adjustment. Before/after leg counts, original Item-versus-Unit proofs, full selected paths and explicit conflict/measurement adoption stay visible; uncertain writes keep the original key even after source removal. Catalog filter/cursor returns survive validated login round-trips, but draft amounts and secrets do not. See [Individual Units and Quantity Stock](docs/holdings.md) for routes, archived/zero rules and the remaining asset/history/QR/offline limits.

From **Purchases** at `/purchases`, browse real filtered live pages, create an exact merchant/instant/currency header, correct descriptive fields or select a catalog Item and explicitly allocate whole integer minor-unit money and base quantity. Item detail also links its complete associated purchase list. Every financial write requires original-context review and confirmation; changed currency/measurement needs explicit adoption, zero-paid history locks currency, and uncertain replies retain only an explicitly retryable original command. Purchase entry never receives Stock or creates/moves Units. See [Purchases and Financial Allocations](docs/purchases.md) for all routes, exact input examples, safe return/reconciliation behavior and remaining asset/adjustment/media gates.

```sh
make setup-web build-web build
./bin/homebox db migrate --database ./data/homebox.sqlite
./bin/homebox serve --database ./data/homebox.sqlite --web-dir ./web/dist
```

Open exactly the `public_origin` printed at startup (by default `http://127.0.0.1:8080`). Development HTTP and insecure cookies are restricted to actual direct loopback peers. Keep this incomplete build on loopback; non-loopback configurations require an explicit HTTPS origin with direct TLS or an explicitly trusted proxy. See [HTTP Authentication and Transport](docs/http-authentication.md) for the implemented flags and API behavior. Production serving uses Go and built static assets, without Node.js. See [Web Client](web/README.md) for frontend development, browser testing, API generation, build budgets, and the current dependency-advisory status.

## Security and data

Use trusted HTTPS over LAN/VPN. See [Security](docs/security.md) for implemented password primitives, cost benchmarking, and the current authentication limits. A VPN does not replace the secure context required by camera/PWA APIs. Do not expose NAS administration, the Docker socket, or unprotected data directories. Do not place SQLite on SMB/NFS. Browser storage can be evicted and is not a durable backup.

Local specifications, environment files, databases, private media, backups, keys, and internal development records are excluded from version control. Public builds and tests must not depend on the local specification pack.

## Development checks

Prerequisites: Linux (including WSL2), Go 1.26.2, Node 24.14.1/npm 11.14.1, Python 3.12, and [uv](https://docs.astral.sh/uv/). The race detector also needs a C compiler; the application SQLite driver itself does not require CGO. Python and Node are development/build tools, not NAS runtime dependencies. Exact dependencies are recorded in `go.sum`, `uv.lock`, and `web/package-lock.json`.

```sh
uv sync --frozen
make setup-web
make check
make test-go-pure
make audit-web
make test-e2e  # Requires the Playwright Chromium browser.

# Run individual tests.
uv run --frozen python -m unittest tests.test_contracts.OpenAPIContractTests.test_entire_document_validates_as_openapi_31 -v
go -C server test -run TestAllPendingMigrationsRollbackWithTheirChecksumRows -v ./internal/store
```

The full Go race suite has an explicit 20-minute per-package deadline; it retains real Argon2 work, fresh all-package execution and every assertion rather than reducing password costs or filtering tests to fit the implicit Go timeout.

The suite validates OpenAPI 3.1, all original API operation identities, offline projection equivalence, security/concurrency requirements, and negative payload cases. Real Go/SQLite integration tests cover safe connection replacement, process locking, fresh/legacy upgrades, checksum refusal, multi-migration rollback, explicit unit mappings, WAL-aware online snapshots, and corruption detection. See [the API reference](contracts/README.md), [Catalog Item Rules](docs/catalog.md), and [the offline protocol](contracts/sync.md). The catalog rules describe implemented Go/SQLite transactions, complete signed/filter-scoped live pages, six item HTTP operations, typed stateless adapters and responsive reviewed metadata screens, bounded measurement and original aggregate proofs; owned-row and Tag maintenance is documented separately, and reviewed physical Unit/Stock screens are available; assisted creation and asset-backed lifecycle remain in development.

The database core, HTTP authentication, account/connection interface, authoritative location service, and responsive location-management screens are implemented. The [category/tag transaction and publication module, eight HTTP operations, typed browser adapters, and responsive settings screens](docs/taxonomy.md) are also implemented; item classification/Tag membership is available through the catalog screens, while asset-backed lifecycle and purchase/media cross-domain workflows remain in development. [Location Rules and Usage](docs/locations.md) explains the real workflows, seven HTTP operations, parent/subtree revisions, immutable history, archival/deletion guards, and atomic current/frozen projections; contents/summary/QR reads and the related content/photo/history UI remain pending. HTTP tests cover authentication, locations, categories/tags, catalog items, all twelve Unit/Stock operations and seven purchase operations through actual TLS/cookie jars, strict input parsing, origin/proxy/CSRF/version/rate checks, mandatory password change, safe acknowledgement replay, public liveness, static-file isolation, SPA routing, and shutdown. Catalog HTTP, typed adapters and reviewed metadata screens work without an AI worker; the remaining inventory/financial/media/assisted-creation domains are still being built. Frontend checks cover generated types, runtime validation, cancellation, theme persistence, reviewed mutations, conflicts, same-key uncertain-outcome reconciliation, protected deep links, and real-server browser accessibility/responsiveness. GitHub Actions configuration is provided, but no remote run is claimed. Remaining domain APIs, installable/offline PWA behavior, persistent drafts, camera, worker, analytics, full encrypted backups, production deployment, and QNAP runtime acceptance remain unfinished. The `serve` command requires a real frontend build and refuses a missing `index.html`; it never substitutes a fake application page.

See [Individual Units and Quantity Stock](docs/holdings.md) for independent Unit/Stock identities, original Item-versus-Unit proofs, exact integer base quantities and immutable movement rules. The root contracts/types and private retained loaders/materialization are tested. The Go `Inventory` service implements real receive/consume/transfer/absolute-adjust transactions, Unit creation/partial editing/movement/guarded status/deletion and authorized complete Unit/filtered Stock reads. Original Item-versus-Unit proofs, independent replies, retained zero balances, explicit non-asset disposal kinds, financial-reference guards and immutable movement/audit/current/frozen publication commit atomically. Unit deletion retains historical IDs and shared media rather than erasing events. These twelve HTTP operations are implemented under the same protected transport/admission boundary, with strict input, independent ETags and safe original-key reconciliation. All twelve typed stateless holding adapters are available, with exact Item-scoped Stock reads, strict response identity/ETag checks, caller-owned original proofs/keys/signals, and whole-reviewed-Item measurement validation for stock writes. Runtime/type tests and private actual-SDK/Go browser workflows cover signed pages, retained zeros, independent counters and explicit original-key reconciliation after genuine malformed/lost replies; no automatic retry, rebase, storage or reconnect replay is added. Complete bracketed holding reads, exact base-count/nullable forms, volatile original-proof commands and no-replay review control drive the responsive physical operating screens. Public history reads and asset-backed lifecycle producers remain unfinished. Neither a schema nor a projection backfill implies those complete workflows.

The [purchase API](docs/purchases.md) implements all seven existing header/allocation operations with exact safe money/base counts, explicit nullable Unit allocation, original Purchase-plus-Item interpretation proofs, independent Line ETags and complete filter-scoped live pages. Go services and strict same-origin HTTP handlers provide header detail/creation/immutable-context correction, unbacked allocation, filtered headers, distinct Item-associated headers and independent owned Lines. Writes atomically commit source/search/current/outbox/audit/original responses; reads use actual authority and scoped signed cursors, never cached financial source or frozen-snapshot claims. New money uses a pinned offline finite ISO minor-unit policy; retained currency text and zero-valued history are not repriced. All seven operations have verified-TLS/cookie-jar coverage; compiled-server/socket responses are separately validated against executable contracts. Generated types, retained loaders and migrations 0013/0014 are available. All seven stateless purchase browser adapters validate exact inputs/resources, complete pages, original whole Purchase/Item interpretations and independent Line ETags without storage, hidden refresh/rebase/retry or reconnect submission. Nanosecond/date/currency/money checks and private actual-SDK/Go browser workflows reject altered committed replies while retaining explicit original-key reconciliation. Complete bracketed selected-Purchase/Item reads, exact form models and volatile reviewed commands additionally preserve whole currency/measurement adoption, zero-paid currency locks and dependent read invalidation without installing historical results. Reviewed responsive purchase screens now use this state for complete lists/details, exact header creation/correction, real paged Item selection, explicit allocation and Item-associated operating views. All viewports preserve original dual proofs, raw minor/base integers, zero-paid currency locks, whole-context adoption and original-key reconciliation with dirty/Back/Escape/200%-text protection. Asset-backed financial integration, audited financial adjustments and receipt/media workflows remain unfinished; existing asset profiles cannot bypass their missing financial producer. These working backend/browser operations do not imply complete asset workflows or full-product acceptance.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Use English Conventional Commits and English source comments. English documentation is canonical; Traditional Chinese translations will be provided after the English originals are complete. The default product interface is Traditional Chinese.

## License

[MIT](LICENSE). Third-party dependencies and generated shadcn components retain their respective licenses.
