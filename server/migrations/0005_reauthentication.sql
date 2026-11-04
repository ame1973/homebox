-- Reauthentication grants are private, single-use capabilities, never offline data.
CREATE TABLE auth_reauth_grants (
  token_hash TEXT PRIMARY KEY CHECK(length(token_hash)=64),
  session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  admin_revision INTEGER NOT NULL CHECK(admin_revision BETWEEN 1 AND 9007199254740991),
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  consumed_at TEXT
) STRICT;
CREATE INDEX idx_reauth_session ON auth_reauth_grants(session_id);
CREATE INDEX idx_reauth_expiry ON auth_reauth_grants(expires_at);
