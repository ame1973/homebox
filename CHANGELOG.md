# Changelog

## Unreleased

### Added

- MIT license, private-data ignore rules, and an English Conventional Commits contribution policy.
- Initial product and contribution documentation.
- Executable OpenAPI 3.1 and offline-projection contracts, with all 103 original operations preserved and workflow-completing endpoints added.
- Contract-level fixes for public login/setup, multipart uploads, required item creation fields, review validation, model integrity metadata, and restore reauthentication/dry-run tokens.
- Currency/unit-aware aggregate schemas, immutable correction references, and strict offline projections excluding unreviewed AI text.
- Preserved baseline/additive SQL migrations and locked development tools with automated contract and migration tests.
- Pinned pure-Go SQLite driver with WAL/FULL durability, per-connection foreign keys, capability checks, private storage permissions, and process-lifetime writer locking.
- Explicit checksum-verified forward migrations, audited legacy adoption, unchanged base-count mappings, and atomic rollback across all pending migrations.
- Verified pre-upgrade SQLite Online Backup snapshots with SHA-256 markers, integrity/FK checks, free-space gates, and self-contained rollback-journal output.
- Real Go integration, race-detector, and CGO-disabled tests for persistence and failure handling.
- A runnable database CLI for version information, explicit migration, integrity checks, and snapshot verification, with bounded mapping-file parsing and cancellation.
- English database operations documentation and reproducible CGO-free Linux amd64/arm64 build targets.

### Security

- Reject permissive or symlinked SQLite sidecars before opening a database, and reject unhashed sidecars or unsafe checksum markers when verifying a migration snapshot.

### Acceptance status

- No product release is claimed. Specification-level checks do not establish HTTP, PWA, or QNAP production readiness.
- Physical QNAP, iPhone/Android, trusted HTTPS/VPN, and actual off-host backup acceptance require operator-supplied environments.
