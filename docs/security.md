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

The internal store service supports a one-time, 30-minute bootstrap credential. Issuing a replacement invalidates the previous credential; setup consumes it, creates exactly one administrator, and records the audit event in one transaction. Only a SHA-256 digest of the uniformly random 256-bit bootstrap secret is stored. The offline `homebox auth bootstrap --output PRIVATE_FILE` command delivers the secret to a new private file and prints only file/expiry metadata. It requires exclusive database ownership and never initializes or upgrades a database implicitly. Never copy a raw setup secret into logs or normal JSON output.

The file contains one base64url token line and must be read only through a trusted local channel. Its parent directory is private and its mode is `0600`; the command refuses existing files, symlinks, FIFOs, and directories. File and directory synchronization finish before the database activates the new hash. Delivery or commit failure preserves the earlier credential and attempts to remove only the new file. A crash before commit can leave an unactivated artifact; use a new private output path rather than overwriting it blindly. If printing metadata fails after commit, the already-synchronized file remains valid. Reissuing before provisioning intentionally invalidates the earlier token.

Login creates an independent 256-bit opaque session secret and stores only its SHA-256 digest. The account ID, revision, and password hash are rechecked inside the session-creation transaction after the expensive password verification has finished. A concurrent credential change cannot publish a stale session. Wrong usernames still perform verification against the sole account's hash, and failed attempts produce an audit event without retaining submitted credentials.

Session defaults are 30 minutes idle and 24 hours absolute lifetime. Trusted configuration may select an idle timeout of at least one minute, no longer than the absolute lifetime, and an absolute lifetime no longer than seven days. Activity updates never extend the absolute deadline. A timestamp at the expiry boundary is expired; requests with a clock earlier than recorded creation/activity fail closed. Session metadata survives restarts without storing raw cookies. Both session and bootstrap credentials are bound to the current dataset epoch: rotating it invalidates old credentials and frozen logout replies, so a future restore cannot resurrect an earlier session.

Logout commits session revocation, its audit event, and a frozen acknowledgement together. An exact session-scoped idempotency retry may retrieve that acknowledgement while the original lifetime bounds still hold, but a revoked token never authorizes another action. Other sessions remain independent. Tests also force a deferred foreign-key failure at COMMIT, verifying that no credential/acknowledgement escapes, all effects roll back, and the pooled connection is usable afterward.

The password-change store service verifies the current password and rejects reuse of the same password. It atomically writes the new hash, advances the account revision, clears mandatory-change state, revokes every session, and saves its audit event and idempotent acknowledgement. It does not issue a replacement session: the forthcoming HTTP flow must clear the cookie and require a fresh login. Expensive verification/hashing happens outside the write transaction, and the original account/session proof is checked again before commit.

Authentication mutation keys share a session-scoped namespace and bind both operation and request; changed payloads or operations conflict. Credential fingerprints use a domain-separated HMAC keyed by an independent 256-bit server secret, binding the raw session credential and typed request as input rather than persisting an unkeyed password digest. The server secret is generated with `crypto/rand` and stored as private SQLite key material, never in a browser, ordinary export, or offline projection. Cookie-only exposure lacks this independent secret, while a database-only copy lacks the raw session credential; simultaneous compromise of both remains outside that protection. An exact password-change retry with the original revoked cookie may recover only its frozen acknowledgement within the original lifetime bounds, never a new authenticated session.

New accounts have `must_change_password=true`. The password-change service clears this flag, but HTTP routing/access gates and the user-facing password-change flow are not implemented yet. Authentication tables and security audit data never enter offline projection changes.

Schema migration 0003 adds revision/mandatory-change/idle metadata and the one-admin index; 0004 adds dataset-epoch binding and invalidates older unbound session/bootstrap credentials. Existing reference sessions are revoked because they have no certified idle history. Multiple legacy administrators cause an atomic upgrade refusal, preserving the original accounts and the verified pre-upgrade snapshot; no account is silently selected or deleted. Unsupported legacy password encodings are not silently downgraded or accepted.

## Session-bound CSRF tokens

The internal CSRF service issues a domain-separated, server-keyed request token only after validating an active session, including sessions that must change their password. Tokens are stable for the session and distinct from both its cookie and reauthentication capabilities. They are not persisted in audit/idempotency data or browser storage. A different session or server key cannot validate the token.

MAC verification is deliberately separate from authorization: every mutation must still validate the session inside its own transaction. This allows an exact logout/password-change acknowledgement retry with the original cookie and CSRF token without authorizing a new action after revocation. A revoked, expired, or old-epoch session cannot obtain another CSRF response. HTTP Origin checks and enforcement are still pending.

## Reauthentication grants

The internal reauthentication service requires a fully initialized account, an active session, the correct username, and a fresh password proof. It creates a single-use capability valid for at most five minutes, further bounded by remaining session idle/absolute lifetime. Creation revalidates the account/session proof inside the transaction and atomically commits the grant hash, activity timestamp, audit event, and safe idempotency metadata.

Grant values are domain-separated HMAC outputs requiring that independent server secret as well as the raw session credential and request key. They cannot be computed from a cookie and idempotency key alone. This lets an exact retry reconstruct the original authenticated response without persisting the raw grant. The database stores only the grant hash, parent session, account revision, timestamps, and consumed state. Reconstruction does not create another row, renew expiry, or reset consumption; retrying an already used/expired grant does not make it usable again.

Grant consumption is private to the store and must share the authoritative SQL transaction with the privileged mutation or job authorization. There is deliberately no separate public check/consume endpoint. A failed transaction rolls consumption back; concurrent transactions can have only one winner. Logout, password rotation, revision mismatch, mandatory-change state, dataset-epoch rotation, expiry, or a different session prevents use. Knowing how to derive a token is not enough: a matching persisted, unused grant is required.

Migration 0005 adds private reauthentication storage and indexes. Migration 0006 retires the earlier cookie-only MAC protocol, revokes existing sessions, deletes old grants/authentication replies, and adds private server-key storage without deleting domain idempotency records. Missing key material with outstanding authentication state fails closed rather than silently regenerating a different key. HTTP reauthentication and privileged operation routes are still pending; these services do not by themselves expose a usable remote workflow.

## Offline owner recovery

`homebox auth reset-password --output PRIVATE_FILE --confirm RESET_PASSWORD` is an explicitly local operator command, not a remote authentication bypass. The server must be stopped so the CLI can obtain the process-lifetime database lock; only this command opens the store with offline recovery enabled. A normally opened serving store rejects the recovery method before creating any file. No HTTP route exposes it, and missing/wrong confirmation is rejected before database access.

Recovery requires an existing sole administrator. It generates a fresh random temporary password, hashes it without requiring the old/legacy encoding to verify, and rechecks the target account before committing. The new hash, revision advance, mandatory-change flag, all-session revocation, stale bootstrap cleanup, and recovery audit event commit together only after private-file synchronization. Delivery or commit failure preserves the earlier account/session state and removes the attempt's file when possible. Username and safe revision/file metadata are returned, but the temporary password is never printed or passed on the command line.

After a temporary-password login, reauthentication/privileged access remains blocked until the password-change flow completes. Prior sessions and grants cannot be used. The temporary password is not automatically time-expiring; change it promptly, then remove the now-obsolete private delivery file through the operator's normal secret-handling process. Recovery handles forgotten/unsupported password encodings, not corruption or loss of authentication server-key material; restore a verified protected database for that separate failure.

## Data and transport boundaries

Private SQLite storage, sidecars, and migration snapshots have strict permissions and must not be placed in the public static-assets directory. Migration snapshots are not encrypted full backups; see [Database Operations](database.md). Future deployment requires trusted HTTPS on LAN/VPN; a VPN alone does not satisfy browser camera/PWA secure-context requirements.

Never put credentials, CSRF tokens, or provider secrets in browser persistent storage, URLs, QR labels, or logs. Browser offline data is disposable plaintext from the unlocked-device threat-model perspective, not a backup or remotely erasable vault.

Frontend runtime and development dependency audits are separate; current tooling advisories and audit commands are documented in [Web Client](../web/README.md). Do not replace a failed compatibility check with a forced dependency override.
