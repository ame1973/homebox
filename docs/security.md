# Security

**This development build is not ready for deployment.** The HTTP interface currently exposes only static application assets and minimal liveness. Password primitives are implemented, but administrator HTTP authentication, CSRF enforcement, rate limiting, and the complete account lifecycle are not yet available. Keep the server bound to loopback.

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

## Data and transport boundaries

Private SQLite storage, sidecars, and migration snapshots have strict permissions and must not be placed in the public static-assets directory. Migration snapshots are not encrypted full backups; see [Database Operations](database.md). Future deployment requires trusted HTTPS on LAN/VPN; a VPN alone does not satisfy browser camera/PWA secure-context requirements.

Never put credentials, CSRF tokens, or provider secrets in browser persistent storage, URLs, QR labels, or logs. Browser offline data is disposable plaintext from the unlocked-device threat-model perspective, not a backup or remotely erasable vault.

Frontend runtime and development dependency audits are separate; current tooling advisories and audit commands are documented in [Web Client](../web/README.md). Do not replace a failed compatibility check with a forced dependency override.
