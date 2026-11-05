# HTTP Authentication and Transport

**Development status:** all seven authentication API routes, their transport/security checks, the React account/location/taxonomy workflows, [seven location HTTP operations](locations.md), [eight category/tag HTTP operations](taxonomy.md), [six catalog HTTP operations and their typed stateless adapters](catalog.md) are implemented. Reviewed catalog screens, privileged reauthentication consumers, other inventory/domain routes, offline PWA, and production deployment are not complete. These commands document working server capabilities, not QNAP or physical-device acceptance.

## Start locally

Build the real frontend and binary, explicitly migrate the private database, then issue a bootstrap file while the server is stopped:

```sh
make setup-web build-web build
./bin/homebox db migrate --database ./data/homebox.sqlite
./bin/homebox auth bootstrap --database ./data/homebox.sqlite --output ./data/bootstrap.token
./bin/homebox serve --database ./data/homebox.sqlite --web-dir ./web/dist
```

Startup prints `listening`, `public_origin`, `version`, and `build_id`, never credentials. Open exactly the printed origin; `localhost` and `127.0.0.1` are different browser origins. The default is `http://127.0.0.1:8080`. Authentication over HTTP is limited to an actual loopback socket peer and a configured loopback IP or `localhost`, without proxy trust. Only this development mode omits the cookie's Secure attribute. Forged forwarding headers do not turn a LAN client into a loopback client.

Open `/setup` for first-time provisioning, `/login` to sign in, and `/settings/account` for password change or logout. These routes have the same capabilities on phone, tablet, and desktop. Read the bootstrap token only through a trusted private channel; never put it in a URL, shell history, issue, screenshot, or log. Its 30-minute expiry and no-overwrite delivery behavior are described in [Security](security.md). Setup creates the account but never pretends to sign it in.

## Direct HTTPS

Supply a certificate chain and private key, both in PEM format. The certificate must cover the browser hostname/IP and be trusted by the client. The example paths and internal DNS name below must be replaced with operator-provisioned values:

```sh
./bin/homebox serve --database ./data/homebox.sqlite --web-dir ./web/dist \
  --listen 127.0.0.1:8443 \
  --public-origin https://homebox.example.internal:8443 \
  --tls-cert /private/homebox/fullchain.pem --tls-key /private/homebox/key.pem
```

The listener supports TLS 1.2 or later. Both TLS flags are required together. For a non-loopback listener, an explicit `--public-origin` is mandatory; plaintext without an explicitly trusted proxy is refused. An HTTPS origin without either TLS certificates or explicit proxy trust is also refused. The origin is an absolute `https://host[:port]` with no credentials, path/trailing slash, query, or fragment. Use ASCII/punycode DNS names.

Keep private keys outside `web/dist` and Git with restrictive permissions. Certificate issuance, renewal, trusted internal DNS, firewall restrictions, and actual NAS deployment are operator responsibilities and remain separate acceptance gates. Do not disable browser/certificate verification to claim trusted HTTPS. A VPN alone does not provide the secure context needed by camera/PWA APIs.

## HTTPS reverse proxy

A trusted HTTPS proxy can terminate TLS while forwarding to a private HTTP listener:

```sh
./bin/homebox serve --database ./data/homebox.sqlite --web-dir ./web/dist \
  --listen 127.0.0.1:8080 --public-origin https://homebox.example.internal \
  --trusted-proxy 127.0.0.1/32
```

`--trusted-proxy` accepts an IP prefix and can be repeated. Use the narrowest actual proxy addresses, preferably `/32` or `/128`; wildcard `/0` trust is rejected. Do not trust an entire LAN, container network, VPN, or every caller just to make a request pass.

The proxy must:

- Serve the exact configured HTTPS origin with a client-trusted certificate.
- Preserve the original `Host` including a nondefault public port, and pass the browser's `Origin` unchanged.
- Overwrite client-supplied `X-Forwarded-Proto` with the single value `https`; never pass a list or untrusted value through.
- Rebuild a trustworthy `X-Forwarded-For` chain, rather than treating the caller's leftmost claim as authoritative.
- Restrict backend connectivity to the configured proxy through binding/firewall rules. Other local users must not be treated as untrusted while sharing a trusted loopback proxy address.
- Keep credential bodies, Cookie/Set-Cookie, CSRF, and reauthentication headers out of request/diagnostic logs.

Only a trusted socket peer can establish forwarded HTTPS or rate-limit identity. Client selection walks the chain from right to left until the nearest untrusted hop; the leftmost value is not blindly used. The chain is bounded to eight hops and 1 KiB. Without a forwarded client chain, requests share the proxy's rate budget. `X-Forwarded-Host` and `Forwarded` never override the configured origin. HSTS is emitted for actual TLS or verified trusted-proxy HTTPS, not an arbitrary incoming header.

These are verified application-side rules; no particular proxy/QNAP configuration or real certificate environment is claimed tested here.

## Authentication workflow

All paths below are relative to `/api/v1`. Every response is `Cache-Control: no-store`. No CORS access is enabled.

| Route | Required context | Successful result |
| --- | --- | --- |
| `POST /auth/setup` | Exact Origin and private one-time bootstrap token | Administrator metadata, `must_change_password=true`; no session cookie |
| `POST /auth/login` | Exact Origin and correct username/password | Administrator metadata and a fresh opaque session cookie |
| `GET /auth/me` | Active session cookie | Current administrator metadata |
| `GET /auth/csrf` | Active session cookie | Session-bound request token; keep only in memory |
| `POST /auth/password` | Origin, cookie, CSRF, supported version, idempotency key, old/new password | Frozen acknowledgement, all sessions revoked, cookie expired |
| `POST /auth/reauth` | Completed password change plus the write headers and a fresh username/password proof | Short-lived, single-use reauthentication capability |
| `POST /auth/logout` | Origin, cookie, CSRF, supported version, idempotency key; no body | Frozen acknowledgement and expired cookie |

Use the [OpenAPI contract](../contracts/openapi.yaml) for exact request and response schemas. Initial setup is not a login. Sign in after provisioning, obtain CSRF, choose a different password, then sign in again. A mandatory-change session can inspect its account/request token, change its password, or log out; it cannot access private domain data or obtain reauthentication grants. Core/domain routes that are not implemented return structured errors, never fake successful data.

The cookie is host-only `homebox_session`, HttpOnly, SameSite=Strict, `Path=/`, and Secure on HTTPS. Its absolute expiry is set from the committed session; server-side idle/absolute/epoch/revocation checks remain authoritative. The raw credential is delivered only in Set-Cookie, not JSON or browser storage. Credential-bearing requests use JSON bodies, never URL parameters. Authentication URLs must use exact unencoded paths and have no query string.

Reauthentication does not replace a session or authorize an arbitrary operation. A privileged transaction must consume the persisted session/revision/epoch-bound capability exactly once. No standalone HTTP check/consume or password-recovery endpoint exists. Owner recovery remains an [offline CLI operation](security.md#offline-owner-recovery).

## Browser form behavior

The account pages use React Hook Form/Zod and official shadcn/Radix fields/dialogs. Password-manager autocomplete is supported without copying cookie credentials into JavaScript. New-password lengths count Unicode code points, preserve whitespace/normalization, reject lone surrogates, and require matching confirmation. Secret visibility is explicit and resets on backgrounding.

Dirty in-app navigation opens a keyboard-accessible stay/clear-and-leave dialog; hard reload/close uses the browser's unsaved-change prompt. Secrets are not drafts: they clear on backgrounding, route exit, and completion. Pending actions reject duplicates and are aborted on backgrounding/offline/unmount. Abort or timeout after sending does **not** prove rollback. An uncertain result clears the previous session view and performs read-only reconciliation, never another credential mutation. If the server committed a password change but its reply was lost/malformed, the UI does not claim failure or success blindly; use the new password for an explicit fresh login.

Successful login/rotation/logout and expired/uncertain session transitions cancel and clear query/mutation state while preserving theme preferences. There is no shipped IndexedDB inventory replica or thumbnail cache yet, so this is not a claim of the later full offline-purge workflow or remote erasure of another disconnected device. Reconnection can refresh account reads but never submit a form automatically. Privileged reauthentication UI will be composed with its first real consumer; no decorative capability-minting button is exposed.

Lazy route-load failures show an explicit reload/recovery screen instead of a blank page. Phone/tablet/desktop share the routes and controls, with wrapping at 200% text size. Theme token changes avoid interpolating foreground/background pairs through unreadable intermediate contrast. Critical confirmation content appears fully opaque without a fade, so its text is readable from the first frame.

## Write protection and explicit retries

All auth POSTs require one exact configured `Origin`, even for anonymous login/setup. An absent, null, duplicate, different-scheme/host/port Origin is rejected. Host rebinding, cross-site/same-site fetch metadata, and untrusted proxy claims cannot authorize a write. Reads may omit Origin but must still match Host/transport/session and cannot present cross-origin metadata.

Protected writes additionally require:

- `X-CSRF-Token`: the token obtained for this active session, not a cookie/grant or another session's token.
- `X-HomeBox-Client-Version`: currently exactly `0.1.0` or `0.1.0-dev`; missing, duplicated, older, newer, or specification-pack versions receive `426 CLIENT_UPGRADE_REQUIRED`.
- `Idempotency-Key`: one 8–128-character key. Authentication keys bind this session, operation, and typed request; domain keys bind the administrator and dataset epoch, with operation/body/precondition binding and active completed authorization even on replay.

Do not automatically retry/resume paused writes. After a failed/uncertain response, foreground review and an explicit retry must preserve the original key/body/preconditions. A changed operation or request conflicts. Password change/logout can replay only their original acknowledgement with the original cookie and CSRF token, within the original server lifetime bounds; this does not reactivate the revoked session or fetch another token. Success clears the browser cookie and password change requires a fresh login. Reauthentication replay returns the same grant/deadline without extending expiry or resetting single use.

## Input and admission limits

Authentication JSON is unencoded `application/json` (optional UTF-8 charset), capped at 16 KiB before parsing. Field names are case-sensitive. Duplicate names, invalid UTF-8, lone escaped surrogates, malformed JSON, and trailing documents are rejected rather than silently repaired. Unknown/missing fields, nonstring/null values, and field bounds are validation errors. Password whitespace/Unicode is preserved. Logout accepts no body.

Process-local one-minute fixed windows apply before credential work:

| Request class | Per effective peer | Global |
| --- | --- | --- |
| Setup/login/password/reauth attempts | 5/minute | 30/minute |
| Other private API requests, including account/CSRF/logout | 60/minute | 600/minute |

Successful and failed admitted attempts count alike. `429 RATE_LIMITED` includes a bounded Retry-After in seconds. The peer map is capped at 1,024 identities; full active capacity fails closed and expired identities are reclaimed. Restarts reset these in-memory limits, and fixed-window boundaries can permit bursts; they are not a public-Internet abuse service. The independent process-wide Argon2 work gate also returns `503`/Retry-After when busy rather than queuing unbounded memory work. Raw forwarding/cookie claims do not create arbitrary rate-limit identities.

Malformed/forbidden requests never issue or clear cookies. Errors contain a fresh request ID and safe code/message/details, not submitted credentials, SQL errors, or private paths. Keep the app private on LAN/VPN and do not expose NAS administration, the Docker socket, or HomeBox directly to the Internet.

## Verification

```sh
go -C server test -race -count=1 ./internal/httpapi ./cmd/homebox
go -C server test ./internal/httpapi -run '^$' -fuzz FuzzAuthenticationJSON -fuzztime=3s -parallel=2
make check
make test-go-pure
```

Tests exercise actual TLS sockets with verified test certificates/cookie jars, all seven auth routes, six catalog operations, CLI TLS/proxy wiring, same-origin/CSRF/client-version/rate limits, mandatory password change, strict parsing, safe acknowledgement retries, and graceful lock release. The real-server browser suite additionally drives setup/login/rotation/logout, dirty navigation/focus, no offline replay, 200% text, lazy-chunk recovery, and a committed mutation whose response is deliberately malformed. Each account test has its own private server/database/bootstrap/rate state; credential traces/videos/automatic failure images are disabled. These automated checks are not physical iPhone/Android/QNAP/TLS/VPN acceptance or complete domain HTTP conformance.
