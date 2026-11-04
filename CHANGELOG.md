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
- A minimal same-origin HTTP foundation with public liveness, rooted static-file containment, safe SPA deep links, structured errors, security headers, and graceful shutdown. Private domain APIs are not yet implemented.
- An official Vite/React and shadcn/Radix/Tailwind frontend with real connectivity checks, cancellation/timeouts, runtime response validation, accessible system/light/dark themes, and no automatic mutation replay.
- Generated API types with drift/negative-type checks, frontend unit/coverage and build-budget tests, and real-Go-server Playwright/axe checks across five layouts.
- SHA-pinned GitHub Actions checks for the backend/frontend, frozen dependencies, dependency audits, and browser test artifacts. Remote CI execution and physical acceptance are not yet claimed.

### Fixed

- Move OpenAPI offline payload definitions into reusable components so generated TypeScript does not incorrectly require a `$defs` data field. The wire format and standalone JSON Schema remain unchanged and equivalence is tested.

### Security

- Add transactional one-time bootstrap, single-administrator enforcement, hashed opaque sessions, idle/absolute expiry, credential-proof revalidation, and session-scoped idempotent logout. Migration 0003 revokes uncertified reference sessions and refuses ambiguous multi-admin upgrades without deleting data; 0004 binds credentials to the dataset epoch to prevent resurrection after restore. These store services are not yet exposed as HTTP authentication.

- Add bounded Argon2id PHC hashing/verification with unique salts, constant-time comparison, process-wide work admission, strict cost/encoding checks, Unicode password bounds, independent-vector tests, and parser fuzzing. HTTP authentication is not yet wired.

- Reject permissive or symlinked SQLite sidecars before opening a database, and reject unhashed sidecars or unsafe checksum markers when verifying a migration snapshot.

### Acceptance status

- No product release is claimed. Specification-level checks do not establish HTTP, PWA, or QNAP production readiness.
- Physical QNAP, iPhone/Android, trusted HTTPS/VPN, and actual off-host backup acceptance require operator-supplied environments.
