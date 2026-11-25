-- This independent key dependency starts empty; no retained search change or
-- legacy proof is assigned a signer during migration.
CREATE TABLE sync_search_signer (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1),
 key_proof BLOB NOT NULL CHECK(length(key_proof)=32)
) STRICT;

CREATE TRIGGER sync_search_signer_no_update BEFORE UPDATE ON sync_search_signer
 BEGIN SELECT RAISE(ABORT,'search signer is immutable'); END;
CREATE TRIGGER sync_search_signer_no_delete BEFORE DELETE ON sync_search_signer
 BEGIN SELECT RAISE(ABORT,'search signer is immutable'); END;
CREATE TRIGGER sync_search_signer_no_replace BEFORE INSERT ON sync_search_signer
 WHEN EXISTS(SELECT 1 FROM sync_search_signer WHERE singleton=NEW.singleton)
 BEGIN SELECT RAISE(ABORT,'search signer is immutable'); END;
