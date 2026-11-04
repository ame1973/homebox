-- Authentication metadata is private and never enters offline projections.
ALTER TABLE admin_accounts ADD COLUMN revision INTEGER NOT NULL DEFAULT 1
  CHECK(revision BETWEEN 1 AND 9007199254740991);
ALTER TABLE admin_accounts ADD COLUMN must_change_password INTEGER NOT NULL DEFAULT 1
  CHECK(must_change_password IN (0,1));
-- Refuse ambiguous legacy multi-admin databases instead of silently deleting accounts.
CREATE UNIQUE INDEX idx_single_admin_account ON admin_accounts((1));

ALTER TABLE sessions ADD COLUMN last_seen_at TEXT NOT NULL DEFAULT '1970-01-01T00:00:00.000000000Z';
-- Reference sessions lack an idle-expiry history and cannot be safely continued.
UPDATE sessions SET last_seen_at=created_at,
  revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now'));
CREATE INDEX idx_sessions_admin ON sessions(admin_id);

CREATE TABLE auth_bootstrap (
  singleton INTEGER PRIMARY KEY CHECK(singleton=1),
  token_hash TEXT NOT NULL CHECK(length(token_hash)=64),
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL
) STRICT;
