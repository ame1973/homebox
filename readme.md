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

## Security and data

Use trusted HTTPS over LAN/VPN. A VPN does not replace the secure context required by camera/PWA APIs. Do not expose NAS administration, the Docker socket, or unprotected data directories. Do not place SQLite on SMB/NFS. Browser storage can be evicted and is not a durable backup.

Local specifications, environment files, databases, private media, backups, keys, and internal development records are excluded from version control. Public builds and tests must not depend on the local specification pack.

## Development checks

Prerequisites for the current contract suite: Python 3.12 and [uv](https://docs.astral.sh/uv/). Python is a development-only tool, not a NAS runtime dependency. Exact dependencies and artifact hashes are recorded in `uv.lock`.

```sh
uv sync --frozen
make test

# Run one test.
uv run --frozen python -m unittest tests.test_contracts.OpenAPIContractTests.test_entire_document_validates_as_openapi_31 -v
```

The suite validates OpenAPI 3.1, all original API operation identities, offline projection equivalence, security/concurrency requirements, negative payload cases, and fresh/legacy SQLite migration invariants. See [the API reference](contracts/README.md) and [the offline protocol](contracts/sync.md).

These checks do not establish HTTP handler, browser, camera, worker, or QNAP runtime coverage. Application startup/deployment instructions will accompany those tested implementations.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Use English Conventional Commits and English source comments. English documentation is canonical; Traditional Chinese translations will be provided after the English originals are complete. The default product interface is Traditional Chinese.

## License

[MIT](LICENSE). Third-party dependencies and generated shadcn components retain their respective licenses.
