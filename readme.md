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

See [Database Operations](docs/database.md) for private storage permissions, explicit legacy adoption, precision mappings, verified pre-migration snapshots, failure recovery, timeouts, and Linux amd64/arm64 builds. Local migration snapshots are sensitive, unencrypted database-only recovery files, not full encrypted off-host backups.

## Private setup credential (available now)

With the server stopped and the database already migrated, create a new private setup-token file:

```sh
./bin/homebox auth bootstrap --database ./data/homebox.sqlite --output ./data/bootstrap.token
```

The command prints only the absolute file path and expiry, not the secret. The output directory must be private, the new file is mode `0600`, and existing files/symlinks are never overwritten. The credential expires after 30 minutes and is disabled after successful administrator provisioning. The HTTP setup/login endpoints are implemented, but their React screens are still pending; this is not a production-ready product. See [HTTP Authentication and Transport](docs/http-authentication.md) and [Security](docs/security.md) for the current workflow, delivery, failure, and retry behavior.

## Offline account recovery (available now)

A local filesystem owner can replace a forgotten or unsupported legacy password while the server is stopped:

```sh
./bin/homebox auth reset-password --database ./data/homebox.sqlite \
  --output ./data/recovery-password.txt --confirm RESET_PASSWORD
```

The command requires an already provisioned account and explicit confirmation. It generates a random temporary password into a new mode-`0600` private file, prints only account/file/revision metadata, revokes all sessions, and requires password change after the next login. Existing files are never overwritten. Recovery is deliberately unavailable in normal serving-store mode and has no HTTP route. Read the file through a trusted local channel; do not paste secrets into logs or issues. The HTTP login/password-change endpoints are available; their React screens are still being implemented.

## Local web application (available now)

The React interface currently provides a real, read-only server connectivity check, retry/error/loading states, and system/light/dark appearance. It does not display invented inventory data or claim that private domain APIs are ready.

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

The suite validates OpenAPI 3.1, all original API operation identities, offline projection equivalence, security/concurrency requirements, and negative payload cases. Real Go/SQLite integration tests cover safe connection replacement, process locking, fresh/legacy upgrades, checksum refusal, multi-migration rollback, explicit unit mappings, WAL-aware online snapshots, and corruption detection. See [the API reference](contracts/README.md) and [the offline protocol](contracts/sync.md).

The database core, HTTP authentication, and initial connectivity interface are implemented. HTTP tests cover all seven authentication routes through actual TLS/cookie jars, strict input parsing, origin/proxy/CSRF/version/rate checks, mandatory password change, safe revoked-cookie acknowledgement replay, public liveness, static-file isolation, SPA routing, and shutdown. Frontend checks cover generated API types, runtime response validation, cancellation, theme persistence, mutation non-replay, and real-server browser accessibility/responsiveness. GitHub Actions configuration is provided, but no remote run is claimed. Private domain APIs, installable/offline PWA behavior, camera, worker, and QNAP runtime acceptance remain unfinished. The `serve` command requires a real frontend build and refuses a missing `index.html`; it never substitutes a fake application page.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Use English Conventional Commits and English source comments. English documentation is canonical; Traditional Chinese translations will be provided after the English originals are complete. The default product interface is Traditional Chinese.

## License

[MIT](LICENSE). Third-party dependencies and generated shadcn components retain their respective licenses.
