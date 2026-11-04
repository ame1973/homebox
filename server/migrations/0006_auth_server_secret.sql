-- Reauthentication and credential fingerprints require an independent server secret.
-- Go creates the key with crypto/rand when authentication is first initialized.
CREATE TABLE auth_server_secret (
  singleton INTEGER PRIMARY KEY CHECK(singleton=1),
  key_bytes BLOB NOT NULL CHECK(length(key_bytes)=32),
  created_at TEXT NOT NULL
) STRICT;
-- Do not retain capabilities/replies created by the cookie-only derivation protocol.
UPDATE sessions SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now'));
DELETE FROM auth_reauth_grants;
DELETE FROM api_idempotency WHERE operation IN ('post_auth_logout','post_auth_password','post_auth_reauth');
