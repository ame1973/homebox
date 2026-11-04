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

## Security and data

Use trusted HTTPS over LAN/VPN. A VPN does not replace the secure context required by camera/PWA APIs. Do not expose NAS administration, the Docker socket, or unprotected data directories. Do not place SQLite on SMB/NFS. Browser storage can be evicted and is not a durable backup.

Local specifications, environment files, databases, private media, backups, keys, and internal development records are excluded from version control. Public builds and tests must not depend on the local specification pack.

## Development checks

Prerequisites: Linux (including WSL2), Go 1.26.2, Python 3.12, and [uv](https://docs.astral.sh/uv/). The race detector also needs a C compiler; the application SQLite driver itself does not require CGO. Python is a development-only tool, not a NAS runtime dependency. Exact dependencies and artifact hashes are recorded in `go.sum` and `uv.lock`.

```sh
uv sync --frozen
make check
make test-go-pure

# Run individual tests.
uv run --frozen python -m unittest tests.test_contracts.OpenAPIContractTests.test_entire_document_validates_as_openapi_31 -v
go -C server test -run TestAllPendingMigrationsRollbackWithTheirChecksumRows -v ./internal/store
```

The suite validates OpenAPI 3.1, all original API operation identities, offline projection equivalence, security/concurrency requirements, and negative payload cases. Real Go/SQLite integration tests cover safe connection replacement, process locking, fresh/legacy upgrades, checksum refusal, multi-migration rollback, explicit unit mappings, WAL-aware online snapshots, and corruption detection. See [the API reference](contracts/README.md) and [the offline protocol](contracts/sync.md).

The database core is implemented, but these checks do not establish HTTP handler, browser, camera, worker, or QNAP runtime coverage. Application startup/deployment instructions will accompany those tested implementations.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Use English Conventional Commits and English source comments. English documentation is canonical; Traditional Chinese translations will be provided after the English originals are complete. The default product interface is Traditional Chinese.

## License

[MIT](LICENSE). Third-party dependencies and generated shadcn components retain their respective licenses.
