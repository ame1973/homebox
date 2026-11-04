-- A restored database must not resurrect a session or setup credential.
-- Epoch comparison is an authentication predicate, not an ON UPDATE CASCADE FK.
ALTER TABLE sessions ADD COLUMN dataset_epoch TEXT NOT NULL DEFAULT '';
ALTER TABLE auth_bootstrap ADD COLUMN dataset_epoch TEXT NOT NULL DEFAULT '';
-- Older credentials have no certified dataset binding and must be reissued.
UPDATE sessions SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now'));
DELETE FROM auth_bootstrap;
