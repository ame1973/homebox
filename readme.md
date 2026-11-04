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

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Use English Conventional Commits and English source comments. English documentation is canonical; Traditional Chinese translations will be provided after the English originals are complete. The default product interface is Traditional Chinese.

## License

[MIT](LICENSE). Third-party dependencies and generated shadcn components retain their respective licenses.
