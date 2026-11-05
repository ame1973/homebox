# Changelog

## Unreleased

### Added

- Add responsive real-server location hierarchy/detail/editor/move/archive/restore/delete screens with complete graph validation, native parent selection, explicit before/after review, original parent/target proofs, dirty-navigation protection, safe protected deep links, and shared phone/tablet/desktop capabilities. Retain uncertain volatile requests for explicit original-key retry and fresh read reconciliation; never install historical acknowledgements as current state or replay writes on reconnect. Add isolated real-server workflows, conflict/committed-malformed-reply tests, light/dark accessibility, long paths and 200% text across five emulated layouts.

- Add a foreground-only location mutation controller with explicit review/confirmation, original-key/proof retention for uncertain outcomes, read-only cache invalidation, interruption handling, session-loss cleanup, and no automatic write replay. Controller tests cover concurrent confirmation, pre-send failures, conflicts, malformed replies, cancellation and late results; screen integration remains separate.

- Add complete iterative location-graph validation and traversal, exact original-text metadata validation, explicit dirty-field reconciliation, frozen command dispatch, and restricted location-only authentication return paths, with executable edge-case tests. These helpers do not by themselves enable location screens.

- Generate official Radix Nova NativeSelect and Textarea primitives for location forms, preserving upstream code/licensing and the existing exact dependency lockfile. Apply touch sizing and field semantics in feature composition rather than altering the generated controls.

- Add strict typed browser adapters for all seven location operations with root/child proof overloads, original-key/revision preservation, identity/strong-ETag/deletion/page validation, Unicode/safe-integer bounds, cancellation/deadlines, and no hidden token refresh, rebase, retry, queue, or persistence. Share the existing stateless error/Unicode/transport implementations with account adapters and retain compatibility exports.

- Connect seven real location HTTP operations under the existing fixed-origin/session/CSRF/version/rate boundary, with quoted revisions, no-store ETags, signed live-list pages, strict bounded Unicode JSON, exact nonrounding integer parsing, safe errors, and immutable acknowledgement replay. Verify real TLS/cookie-jar CRUD, subtree confirmation, stale proofs, revoked-after-middleware races, and negative transport/input cases; browser location screens remain pending.

- Add principal/epoch/page-size-scoped signed location-list continuations with a 15-minute lifetime, private-key and credential-purpose isolation, canonical encoding checks, active-session enforcement, and live-list rather than fake-snapshot semantics.

- Add the authoritative location service with Unicode/safe-integer bounds, parent/subtree aggregate revisions, cycle and archival/holding guards, immutable movements, logical deletion, and independent owner-media-link tombstones. Source changes, final-state current/frozen projections, catalog invalidation, immutable audit, and principal/epoch-scoped replies commit together; add statement/commit-failure, concurrency, deep-tree, retention, and replay tests. Location UI and complete sync remain separate unfinished work.
- Add migration 0007 for location metadata, independently retained current projections with deferred revision integrity, and append-only mutation audit; preserve original migration files and legacy quantities. Refine root location concurrency, pagination, nullable-field, and bounds contracts with generated-client fixtures and English rules documentation.

- Add responsive real-server setup/login/mandatory-password-change/logout pages with React Hook Form/Zod, official fields/dialogs, password-manager support, dirty/back/reload guards, secret clearing, safe session-cache transitions, uncertain-outcome reconciliation, and lazy-chunk recovery. Add isolated per-test Go/browser fixtures, light/dark accessibility, two-times text, and committed-but-malformed-response coverage; fix transient theme contrast and narrow enlarged menus without editing generated primitives.

- Pin React Hook Form/Zod resolver tooling and generate official Radix Nova Input/Field/Alert/AlertDialog primitives for the account UI, preserving the existing button implementation and third-party notice.

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
- A minimal same-origin HTTP foundation with public liveness, rooted static-file containment, safe SPA deep links, structured errors, security headers, and graceful shutdown. Remaining private domain APIs are tracked separately.
- An official Vite/React and shadcn/Radix/Tailwind frontend with real connectivity checks, cancellation/timeouts, runtime response validation, accessible system/light/dark themes, and no automatic mutation replay.
- Generated API types with drift/negative-type checks, frontend unit/coverage and build-budget tests, and real-Go-server Playwright/axe checks across five layouts.
- SHA-pinned GitHub Actions checks for the backend/frontend, frozen dependencies, dependency audits, and browser test artifacts. Remote CI execution and physical acceptance are not yet claimed.

### Fixed

- Admit composite item-tag/search projection identities by validating their original components, not a single-row ID length. Use canonical Unicode JSON pairs consistent with browser JSON.stringify and SQLite json_array, reject alternate spellings/invalid UTF and delimiter collisions, and verify a full-length link deletion commits its independent tombstone atomically.

- Refine taxonomy contracts with distinct category creation/partial-patch schemas, required nullable response parents, Unicode/identity/positive-safe-revision bounds, reviewed child/target proofs, Origin/body admission, and bounded signed live pages. Preserve all operation identities and version-one offline payloads; add negative schema/generated-type fixtures before transaction and HTTP integration.

- Handle the first location-review Escape synchronously after browser-Back modal handoff, before the UI library's effect-installed document listener is ready. Pending Escape interrupts local waiting without undoing a committed server write or generating a replacement request; add an actual commit/held-response/back/stay/leave browser regression.

- Keep imported location IDs matching `new` distinct from the static create route through a safe detail escape without rewriting stored identity. Reject inconsistent live list/detail revisions before enabling writes. Preserve dialog layout at enlarged text sizes with shrinkable grid composition, start long reviews at visible static content, and avoid low-contrast enabled-opacity transitions without editing generated UI primitives.

- Preserve optional request fields with server defaults in generated TypeScript using the official generator option, with minimal location/item create fixtures. Keep root location sort orders signed while constraining positive resource revisions independently of shared YAML anchors.

- Move OpenAPI offline payload definitions into reusable components so generated TypeScript does not incorrectly require a `$defs` data field. The wire format and standalone JSON Schema remain unchanged and equivalence is tested.

### Security

- Add stateless typed browser adapters for all seven auth endpoints with strict Zod response checks, explicit CSRF/idempotency context, package-version headers, safe error metadata, Unicode code-point bounds, cancel/timeout cleanup, and no persistent credentials or hidden refresh/retry/replay. Add 79 adapter cases and negative auth type fixtures, and verify the module in Chromium against real isolated Vite/Go processes.

- Expose the seven real authentication HTTP routes with fixed-origin/Host and trusted-transport enforcement, strict bounded UTF-8 JSON, HttpOnly/SameSite cookies, CSRF/version/idempotency checks, bounded per-peer/global rate limits, mandatory password change, and exact revoked-cookie acknowledgement replay. Add direct TLS and explicit proxy CIDR configuration, actual TLS/cookie-jar and CLI listener tests, parser fuzzing, and English operator documentation. Other domain routes remain pending.

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
