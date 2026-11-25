-- New installations and upgrades begin with no historical search proofs.
-- A proof is only a stored byte string; reviewed-source authority and its MAC
-- must be established separately by a future transactional producer/reader.
CREATE TABLE sync_search_proofs (
 seq INTEGER PRIMARY KEY NOT NULL REFERENCES sync_changes(seq) ON DELETE CASCADE,
 proof BLOB NOT NULL CHECK(length(proof)=32)
) STRICT, WITHOUT ROWID;

CREATE TRIGGER sync_search_proofs_require_upsert BEFORE INSERT ON sync_search_proofs
 WHEN NOT EXISTS(SELECT 1 FROM sync_changes WHERE seq=NEW.seq AND entity_type='search_doc' AND operation='upsert')
 BEGIN SELECT RAISE(ABORT,'search proof requires a retained search upsert'); END;
CREATE TRIGGER sync_search_proofs_no_update BEFORE UPDATE ON sync_search_proofs
 BEGIN SELECT RAISE(ABORT,'search proofs are immutable'); END;
CREATE TRIGGER sync_search_proofs_no_delete BEFORE DELETE ON sync_search_proofs
 WHEN EXISTS(SELECT 1 FROM sync_changes WHERE seq=OLD.seq)
 BEGIN SELECT RAISE(ABORT,'search proof requires its retained change'); END;
CREATE TRIGGER sync_search_proofs_no_replace BEFORE INSERT ON sync_search_proofs
 WHEN EXISTS(SELECT 1 FROM sync_search_proofs WHERE seq=NEW.seq)
 BEGIN SELECT RAISE(ABORT,'search proofs cannot be replaced'); END;
