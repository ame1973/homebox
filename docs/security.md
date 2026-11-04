# Security

**This development build is not ready for deployment.** The HTTP interface currently exposes only static application assets and minimal liveness. Password primitives and transactional account/session storage are implemented, but administrator HTTP authentication, CSRF enforcement, rate limiting, password-change/reauthentication flows, and the complete account lifecycle are not yet available. Keep the server bound to loopback.

## Password storage

HomeBox uses the official Go `golang.org/x/crypto/argon2` implementation, pinned in `go.mod`/`go.sum`. Passwords are encoded as canonical Argon2id version 19 PHC strings with independent 128-bit random salts and 256-bit derived keys. Comparison uses `crypto/subtle` constant-time comparison. No password normalization or whitespace trimming is performed.

New passwords must contain 12–256 Unicode characters; invalid UTF-8 and oversized input are rejected before hashing. Verification does not assume that a previously stored hash uses the currently selected cost parameters.

The initial configuration is 64 MiB, three iterations, and one lane. Supported tuning bounds are 19–128 MiB, two–six iterations, and one–four lanes. These bounds are enforced on both newly selected parameters and stored PHC strings before allocating Argon2 memory. Unsupported algorithms/versions, noncanonical encodings, unexpected salt/key sizes, and excessive costs fail closed. This is not a generic importer for arbitrary external password hashes.

One process-wide KDF admission limit prevents simultaneous requests or accidentally duplicated service instances from multiplying the memory allocation. A busy caller receives an error rather than entering an unbounded password-work queue. The Argon2 library computation itself is not interruptible: cancellation discards its result after the bounded computation finishes. HTTP rate limits are still required in addition to this resource boundary.

Before choosing deployment costs, benchmark on the actual NAS. From the repository root:

```sh
go -C server test ./internal/password -run '^$' -bench BenchmarkDefaultHash -benchtime=3x -count=1
```

A development desktop measurement is not a QNAP performance guarantee. Public deployment tuning flags will be documented when the server authentication wiring is implemented; do not invent or assume available flags.

## Verification

```sh
go -C server test -race -count=1 -cover ./internal/password
CGO_ENABLED=0 go -C server test -count=1 ./internal/password
go -C server test ./internal/password -run '^$' -fuzz FuzzParsePHC -fuzztime=3s -parallel=2
```

Tests cover a fixed vector generated independently with the C reference implementation, random salt uniqueness, Unicode/whitespace behavior, malicious PHC costs/encodings, process-wide admission, cancellation, and the default configuration. The fixed fixture does not introduce a native library or CGO dependency into builds or tests.

## Account and session storage

The internal store service supports a one-time, 30-minute bootstrap credential. Issuing a replacement invalidates the previous credential; setup consumes it, creates exactly one administrator, and records the audit event in one transaction. Only a SHA-256 digest of the uniformly random 256-bit bootstrap secret is stored. The operator CLI/private-file delivery is not yet exposed; never copy a raw setup secret into logs or normal JSON output.

Login creates an independent 256-bit opaque session secret and stores only its SHA-256 digest. The account ID, revision, and password hash are rechecked inside the session-creation transaction after the expensive password verification has finished. A concurrent credential change cannot publish a stale session. Wrong usernames still perform verification against the sole account's hash, and failed attempts produce an audit event without retaining submitted credentials.

Session defaults are 30 minutes idle and 24 hours absolute lifetime. Trusted configuration may select an idle timeout of at least one minute, no longer than the absolute lifetime, and an absolute lifetime no longer than seven days. Activity updates never extend the absolute deadline. A timestamp at the expiry boundary is expired; requests with a clock earlier than recorded creation/activity fail closed. Session metadata survives restarts without storing raw cookies. Both session and bootstrap credentials are bound to the current dataset epoch: rotating it invalidates old credentials and frozen logout replies, so a future restore cannot resurrect an earlier session.

Logout commits session revocation, its audit event, and a frozen acknowledgement together. An exact session-scoped idempotency retry may retrieve that acknowledgement while the original lifetime bounds still hold, but a revoked token never authorizes another action. Other sessions remain independent. Tests also force a deferred foreign-key failure at COMMIT, verifying that no credential/acknowledgement escapes, all effects roll back, and the pooled connection is usable afterward.

New accounts have `must_change_password=true`. This is state for the forthcoming HTTP access gate, not a claim that password-change routes or private domain access are implemented. Authentication tables and security audit data never enter offline projection changes.

Schema migration 0003 adds revision/mandatory-change/idle metadata and the one-admin index; 0004 adds dataset-epoch binding and invalidates older unbound session/bootstrap credentials. Existing reference sessions are revoked because they have no certified idle history. Multiple legacy administrators cause an atomic upgrade refusal, preserving the original accounts and the verified pre-upgrade snapshot; no account is silently selected or deleted. Unsupported legacy password encodings are not silently downgraded or accepted.

## Data and transport boundaries

Private SQLite storage, sidecars, and migration snapshots have strict permissions and must not be placed in the public static-assets directory. Migration snapshots are not encrypted full backups; see [Database Operations](database.md). Future deployment requires trusted HTTPS on LAN/VPN; a VPN alone does not satisfy browser camera/PWA secure-context requirements.

Never put credentials, CSRF tokens, or provider secrets in browser persistent storage, URLs, QR labels, or logs. Browser offline data is disposable plaintext from the unlocked-device threat-model perspective, not a backup or remotely erasable vault.

Frontend runtime and development dependency audits are separate; current tooling advisories and audit commands are documented in [Web Client](../web/README.md). Do not replace a failed compatibility check with a forced dependency override.
