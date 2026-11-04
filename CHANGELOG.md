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

- Add stateless typed browser adapters for all seven auth endpoints with strict Zod response checks, explicit CSRF/idempotency context, package-version headers, safe error metadata, Unicode code-point bounds, cancel/timeout cleanup, and no persistent credentials or hidden refresh/retry/replay. Add 79 adapter cases and negative auth type fixtures, and verify the module in Chromium against real isolated Vite/Go processes. Account screens remain pending.

- Expose the seven real authentication HTTP routes with fixed-origin/Host and trusted-transport enforcement, strict bounded UTF-8 JSON, HttpOnly/SameSite cookies, CSRF/version/idempotency checks, bounded per-peer/global rate limits, mandatory password change, and exact revoked-cookie acknowledgement replay. Add direct TLS and explicit proxy CIDR configuration, actual TLS/cookie-jar and CLI listener tests, parser fuzzing, and English operator documentation. React authentication screens and domain routes remain pending.

- Add session-bound, independently server-keyed CSRF issuance and constant-time verification, with active-session issuance, cross-session/capability isolation, and safe acknowledgement-replay tests.

- Add explicit offline-owner password recovery with typed confirmation, random private-file delivery, mandatory first-login rotation, all-session revocation, target revalidation, and atomic audit/rollback behavior. Normal serving stores reject it and no HTTP recovery endpoint is added.

- Fix reauthentication capability derivation to require independent server-only key material as well as the raw session credential. Cookie/key knowledge no longer reconstructs an issued capability. Migration 0006 invalidates the earlier auth protocol without deleting domain replies; adversarial and restart/missing-key tests cover the change.

- Add the offline `auth bootstrap` CLI with exclusive private-file delivery, metadata-only output, file-before-database durability ordering, no-overwrite/symlink/special-file refusal, rollback cleanup, and native CLI smoke coverage.

- Add short-lived, session/revision/epoch-bound single-use reauthentication grants with hash-only storage, deterministic safe replay, atomic private consumption, concurrent-use exclusion, and commit-failure rollback tests. Migration 0005 adds private grant storage.

- Add atomic password change with current-password verification, mandatory-change completion, all-session revocation, safe frozen acknowledgement replay, operation/body-bound auth keys, and keyed credential fingerprints.

- Add transactional one-time bootstrap, single-administrator enforcement, hashed opaque sessions, idle/absolute expiry, credential-proof revalidation, and session-scoped idempotent logout. Migration 0003 revokes uncertified reference sessions and refuses ambiguous multi-admin upgrades without deleting data; 0004 binds credentials to the dataset epoch to prevent resurrection after restore.

- Add bounded Argon2id PHC hashing/verification with unique salts, constant-time comparison, process-wide work admission, strict cost/encoding checks, Unicode password bounds, independent-vector tests, and parser fuzzing.

- Reject permissive or symlinked SQLite sidecars before opening a database, and reject unhashed sidecars or unsafe checksum markers when verifying a migration snapshot.

### Acceptance status

- No product release is claimed. Specification-level checks do not establish HTTP, PWA, or QNAP production readiness.
- Physical QNAP, iPhone/Android, trusted HTTPS/VPN, and actual off-host backup acceptance require operator-supplied environments.
