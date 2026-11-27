-- Existing snapshots remain unsealed. These tables record byte bindings, not
-- source authority or proof validity; no historical provenance is guessed.
CREATE TABLE sync_snapshot_signer (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1),
 key_proof BLOB NOT NULL CHECK(length(key_proof)=32)
) STRICT;

CREATE TRIGGER sync_snapshot_signer_no_update BEFORE UPDATE ON sync_snapshot_signer
 BEGIN SELECT RAISE(ABORT,'snapshot signer is immutable'); END;
CREATE TRIGGER sync_snapshot_signer_no_delete BEFORE DELETE ON sync_snapshot_signer
 BEGIN SELECT RAISE(ABORT,'snapshot signer is immutable'); END;
CREATE TRIGGER sync_snapshot_signer_no_replace BEFORE INSERT ON sync_snapshot_signer
 WHEN EXISTS(SELECT 1 FROM sync_snapshot_signer WHERE singleton=NEW.singleton)
 BEGIN SELECT RAISE(ABORT,'snapshot signer is immutable'); END;

CREATE TABLE sync_snapshot_row_proofs (
 snapshot_id TEXT NOT NULL,
 ordinal INTEGER NOT NULL CHECK(ordinal>=0),
 proof BLOB NOT NULL CHECK(length(proof)=32),
 PRIMARY KEY(snapshot_id,ordinal),
 FOREIGN KEY(snapshot_id,ordinal) REFERENCES sync_snapshot_rows(snapshot_id,ordinal) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;

CREATE TABLE sync_snapshot_seals (
 snapshot_id TEXT PRIMARY KEY NOT NULL REFERENCES sync_snapshots(id) ON DELETE CASCADE,
 total_rows INTEGER NOT NULL CHECK(total_rows>=0 AND total_rows<=9007199254740991),
 proof BLOB NOT NULL CHECK(length(proof)=32)
) STRICT, WITHOUT ROWID;

CREATE TRIGGER sync_snapshot_row_proofs_require_parent BEFORE INSERT ON sync_snapshot_row_proofs
 WHEN NOT EXISTS(SELECT 1 FROM sync_snapshot_rows WHERE snapshot_id=NEW.snapshot_id AND ordinal=NEW.ordinal)
 OR EXISTS(SELECT 1 FROM sync_snapshot_seals WHERE snapshot_id=NEW.snapshot_id)
 BEGIN SELECT RAISE(ABORT,'snapshot proof requires an unsealed row'); END;
CREATE TRIGGER sync_snapshot_row_proofs_no_update BEFORE UPDATE ON sync_snapshot_row_proofs
 BEGIN SELECT RAISE(ABORT,'snapshot row proofs are immutable'); END;
CREATE TRIGGER sync_snapshot_row_proofs_no_delete BEFORE DELETE ON sync_snapshot_row_proofs
 WHEN EXISTS(SELECT 1 FROM sync_snapshot_rows WHERE snapshot_id=OLD.snapshot_id AND ordinal=OLD.ordinal)
 BEGIN SELECT RAISE(ABORT,'snapshot proof requires its row'); END;
CREATE TRIGGER sync_snapshot_row_proofs_no_replace BEFORE INSERT ON sync_snapshot_row_proofs
 WHEN EXISTS(SELECT 1 FROM sync_snapshot_row_proofs WHERE snapshot_id=NEW.snapshot_id AND ordinal=NEW.ordinal)
 BEGIN SELECT RAISE(ABORT,'snapshot row proofs cannot be replaced'); END;

-- A count and a proof row for each ordinal prevent an accidentally partial
-- seal; the producer must still validate the exact bytes and approved source.
CREATE TRIGGER sync_snapshot_seals_require_complete_rows BEFORE INSERT ON sync_snapshot_seals
 WHEN NOT EXISTS(SELECT 1 FROM sync_snapshots WHERE id=NEW.snapshot_id AND status='ready')
 OR (SELECT COUNT(*) FROM sync_snapshot_rows WHERE snapshot_id=NEW.snapshot_id)<>NEW.total_rows
 OR (SELECT COUNT(*) FROM sync_snapshot_row_proofs WHERE snapshot_id=NEW.snapshot_id)<>NEW.total_rows
 OR (NEW.total_rows>0 AND ((SELECT MIN(ordinal) FROM sync_snapshot_rows WHERE snapshot_id=NEW.snapshot_id)<>0
  OR (SELECT MAX(ordinal) FROM sync_snapshot_rows WHERE snapshot_id=NEW.snapshot_id)<>NEW.total_rows-1))
 BEGIN SELECT RAISE(ABORT,'snapshot seal requires complete ordinal proofs'); END;
CREATE TRIGGER sync_snapshot_seals_no_update BEFORE UPDATE ON sync_snapshot_seals
 BEGIN SELECT RAISE(ABORT,'snapshot seals are immutable'); END;
CREATE TRIGGER sync_snapshot_seals_no_delete BEFORE DELETE ON sync_snapshot_seals
 WHEN EXISTS(SELECT 1 FROM sync_snapshots WHERE id=OLD.snapshot_id)
 BEGIN SELECT RAISE(ABORT,'snapshot seal requires its manifest'); END;
CREATE TRIGGER sync_snapshot_seals_no_replace BEFORE INSERT ON sync_snapshot_seals
 WHEN EXISTS(SELECT 1 FROM sync_snapshot_seals WHERE snapshot_id=NEW.snapshot_id)
 BEGIN SELECT RAISE(ABORT,'snapshot seals cannot be replaced'); END;

CREATE TRIGGER sync_snapshots_no_sealed_update BEFORE UPDATE ON sync_snapshots
 WHEN EXISTS(SELECT 1 FROM sync_snapshots s JOIN sync_snapshot_seals p ON p.snapshot_id=s.id
  WHERE s.id=OLD.id OR s.id=NEW.id OR s.rowid=NEW.rowid)
 BEGIN SELECT RAISE(ABORT,'sealed snapshots are immutable'); END;
-- In a BEFORE INSERT, an unspecified rowid is -1. If a sealed snapshot uses
-- that value, reject even automatic inserts rather than permit ambiguous REPLACE.
CREATE TRIGGER sync_snapshots_no_sealed_replace BEFORE INSERT ON sync_snapshots
 WHEN EXISTS(SELECT 1 FROM sync_snapshots s JOIN sync_snapshot_seals p ON p.snapshot_id=s.id
  WHERE s.id=NEW.id OR s.rowid=NEW.rowid)
 BEGIN SELECT RAISE(ABORT,'sealed snapshots cannot be replaced'); END;
CREATE TRIGGER sync_snapshot_rows_no_sealed_insert BEFORE INSERT ON sync_snapshot_rows
 WHEN EXISTS(SELECT 1 FROM sync_snapshot_seals WHERE snapshot_id=NEW.snapshot_id)
 BEGIN SELECT RAISE(ABORT,'sealed snapshot rows are immutable'); END;
CREATE TRIGGER sync_snapshot_rows_no_sealed_update BEFORE UPDATE ON sync_snapshot_rows
 WHEN EXISTS(SELECT 1 FROM sync_snapshot_seals WHERE snapshot_id=OLD.snapshot_id)
 OR EXISTS(SELECT 1 FROM sync_snapshot_seals WHERE snapshot_id=NEW.snapshot_id)
 BEGIN SELECT RAISE(ABORT,'sealed snapshot rows are immutable'); END;
CREATE TRIGGER sync_snapshot_rows_no_sealed_delete BEFORE DELETE ON sync_snapshot_rows
 WHEN EXISTS(SELECT 1 FROM sync_snapshots WHERE id=OLD.snapshot_id)
 AND EXISTS(SELECT 1 FROM sync_snapshot_seals WHERE snapshot_id=OLD.snapshot_id)
 BEGIN SELECT RAISE(ABORT,'sealed snapshot rows are immutable'); END;
