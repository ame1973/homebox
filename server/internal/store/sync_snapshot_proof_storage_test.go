package store

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"homebox/migrations"
)

func snapshotProofRootTwentyEightFixture(t *testing.T) (string, *Store) {
	t.Helper()
	path, old := syncProofRootTwentySevenFixture(t)
	all, err := migrations.Load()
	if err != nil {
		t.Fatal(err)
	}
	migration := all[27]
	execSQL(t, old.db, migration.SQL)
	execSQL(t, old.db, "INSERT INTO schema_migrations(version,name,applied_at) VALUES(?,?,'2026')", migration.Version, migration.Filename)
	execSQL(t, old.db, "INSERT INTO schema_migration_checksums VALUES(?,?,?)", migration.Version, migration.Filename, migration.Checksum)
	return path, old
}

func TestSyncSnapshotProofStorageMigrationPreservesUnsealedHistory(t *testing.T) {
	path, old := snapshotProofRootTwentyEightFixture(t)
	payload := `{"private":"retained-unreviewed-fixture"}`
	seedSyncSearchIndexSnapshot(t, old, projectionPair("item", "legacy"), payload, 7)
	before, projections := domainState(t, old), currentProjectionState(t, old)
	epoch := scalar[string](t, old.db, "SELECT dataset_epoch FROM sync_state")
	if err := old.db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(t.Context(), Config{Path: path, Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Report().From != 28 || s.Report().To < 29 || s.Report().BackupPath == "" || scalar[string](t, s.db, "SELECT dataset_epoch FROM sync_state") != epoch {
		t.Fatal("snapshot proof migration changed the dataset or omitted its recovery snapshot")
	}
	requireDomainState(t, s, before)
	if !reflect.DeepEqual(projections, currentProjectionState(t, s)) ||
		scalar[string](t, s.db, "SELECT payload_json FROM sync_snapshot_rows WHERE snapshot_id='index-snapshot'") != payload ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_snapshot_seals") != 0 ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_snapshot_row_proofs") != 0 ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_snapshot_signer") != 0 {
		t.Fatal("migration changed old frozen bytes or guessed their provenance")
	}
	if err = VerifySnapshot(t.Context(), s.Report().BackupPath); err != nil {
		t.Fatal("the pre-migration recovery snapshot did not verify", err)
	}
}

func TestSyncSnapshotProofStorageMigrationRejectsLateInjection(t *testing.T) {
	for _, test := range []struct{ name, statement string }{
		{"signer", `INSERT INTO sync_snapshot_signer VALUES(1,zeroblob(32))`},
		{"row proof", `INSERT INTO sync_snapshot_row_proofs VALUES('index-snapshot',0,zeroblob(32))`},
		{"seal", `INSERT INTO sync_snapshot_row_proofs VALUES('index-snapshot',0,zeroblob(32)); INSERT INTO sync_snapshot_seals VALUES('index-snapshot',1,zeroblob(32))`},
	} {
		t.Run(test.name, func(t *testing.T) {
			path, old := snapshotProofRootTwentyEightFixture(t)
			seedSyncSearchIndexSnapshot(t, old, projectionPair("item", "legacy"), `{"unreviewed":"fixture"}`, 7)
			execSQL(t, old.db, `CREATE TRIGGER inject_snapshot_proof AFTER INSERT ON schema_migration_checksums WHEN NEW.version=29 BEGIN `+test.statement+`; END`)
			before := domainState(t, old)
			ddl, err := schemaDescription(t.Context(), old.db)
			if err != nil {
				t.Fatal(err)
			}
			if err = old.db.Close(); err != nil {
				t.Fatal(err)
			}
			if s, err := Open(t.Context(), Config{Path: path, Migrate: true}); err == nil {
				_ = s.Close()
				t.Fatal("migration committed injected snapshot proof state")
			} else if !strings.Contains(err.Error(), "verified recovery snapshot retained") {
				t.Fatal("failed migration did not retain its recovery snapshot", err)
			}
			unchanged := rawDatabase(t, path)
			requireDomainState(t, &Store{db: unchanged}, before)
			actual, err := schemaDescription(t.Context(), unchanged)
			if err != nil || !reflect.DeepEqual(actual, ddl) || scalar[int](t, unchanged, "SELECT MAX(version) FROM schema_migrations") != 28 ||
				scalar[int](t, unchanged, "SELECT COUNT(*) FROM sqlite_schema WHERE name IN ('sync_snapshot_seals','sync_snapshot_row_proofs','sync_snapshot_signer')") != 0 {
				t.Fatal("failed migration leaked proof schema or ledger state", err)
			}
			backups, err := filepath.Glob(filepath.Join(filepath.Dir(path), "migration-backups", "*.sqlite"))
			if err != nil || len(backups) != 1 || VerifySnapshot(t.Context(), backups[0]) != nil {
				t.Fatal("failed migration did not preserve a verified snapshot", err)
			}
		})
	}
}

func TestSyncSnapshotProofStorageSealsOnlyCompleteRowsAndFreezesThem(t *testing.T) {
	s, _, _, _, cursor, _ := snapshotMetadataFixture(t)
	addSnapshotMetadataRow(t, s, cursor.SnapshotID, 0, "location", "one", `{}`)
	addSnapshotMetadataRow(t, s, cursor.SnapshotID, 1, "location", "two", `{}`)
	proof := bytes.Repeat([]byte{0x5a}, 32)
	for _, attempt := range []struct {
		statement string
		arguments []any
	}{
		{"INSERT INTO sync_snapshot_row_proofs VALUES(?,0,zeroblob(32))", []any{"missing"}},
		{"INSERT INTO sync_snapshot_row_proofs VALUES(?,0,zeroblob(31))", []any{cursor.SnapshotID}},
		{"INSERT INTO sync_snapshot_row_proofs VALUES(?,0,'not a blob')", []any{cursor.SnapshotID}},
		{"INSERT INTO sync_snapshot_row_proofs VALUES(?,-1,zeroblob(32))", []any{cursor.SnapshotID}},
		{"INSERT INTO sync_snapshot_seals VALUES(?,0,zeroblob(32))", []any{"missing"}},
		{"INSERT INTO sync_snapshot_seals VALUES(?,2,zeroblob(31))", []any{cursor.SnapshotID}},
		{"INSERT INTO sync_snapshot_seals VALUES(?,2,'not a blob')", []any{cursor.SnapshotID}},
		{"INSERT INTO sync_snapshot_seals VALUES(?,2,zeroblob(32))", []any{cursor.SnapshotID}},
		{"INSERT INTO sync_snapshot_seals VALUES(?,-1,zeroblob(32))", []any{cursor.SnapshotID}},
		{"INSERT INTO sync_snapshot_seals VALUES(?,9007199254740992,zeroblob(32))", []any{cursor.SnapshotID}},
	} {
		if _, err := s.db.ExecContext(t.Context(), attempt.statement, attempt.arguments...); err == nil {
			t.Fatal("invalid or incomplete snapshot proof state was accepted", attempt.statement)
		}
	}
	execSQL(t, s.db, "INSERT INTO sync_snapshot_row_proofs VALUES(?,0,?)", cursor.SnapshotID, proof)
	if _, err := s.db.ExecContext(t.Context(), "INSERT INTO sync_snapshot_seals VALUES(?,2,zeroblob(32))", cursor.SnapshotID); err == nil {
		t.Fatal("a snapshot was sealed without every row proof")
	}
	execSQL(t, s.db, "INSERT INTO sync_snapshot_row_proofs VALUES(?,1,?)", cursor.SnapshotID, proof)
	if _, err := s.db.ExecContext(t.Context(), "INSERT INTO sync_snapshot_seals VALUES(?,1,zeroblob(32))", cursor.SnapshotID); err == nil {
		t.Fatal("a snapshot was sealed with an incorrect total")
	}
	execSQL(t, s.db, "INSERT INTO sync_snapshot_seals VALUES(?,2,?)", cursor.SnapshotID, proof)
	execSQL(t, s.db, "PRAGMA recursive_triggers=OFF")
	defer execSQL(t, s.db, "PRAGMA recursive_triggers=ON")
	for _, attempt := range []struct {
		statement string
		arguments []any
	}{
		{"UPDATE sync_snapshots SET high_watermark=1 WHERE id=?", []any{cursor.SnapshotID}},
		{"UPDATE sync_snapshots SET status='expired' WHERE id=?", []any{cursor.SnapshotID}},
		{`INSERT OR REPLACE INTO sync_snapshots(id,server_instance_id,dataset_epoch,projection_version,high_watermark,created_at,expires_at,status)
 SELECT id,server_instance_id,dataset_epoch,projection_version,high_watermark,created_at,expires_at,status FROM sync_snapshots WHERE id=?`, []any{cursor.SnapshotID}},
		{`INSERT OR REPLACE INTO sync_snapshots(rowid,id,server_instance_id,dataset_epoch,projection_version,high_watermark,created_at,expires_at,status)
 SELECT rowid,'replacement',server_instance_id,dataset_epoch,projection_version,high_watermark,created_at,expires_at,status FROM sync_snapshots WHERE id=?`, []any{cursor.SnapshotID}},
		{"UPDATE sync_snapshot_rows SET payload_json='{\"changed\":1}' WHERE snapshot_id=? AND ordinal=0", []any{cursor.SnapshotID}},
		{"DELETE FROM sync_snapshot_rows WHERE snapshot_id=? AND ordinal=0", []any{cursor.SnapshotID}},
		{"INSERT INTO sync_snapshot_rows VALUES(?,2,'location','three',1,'{}')", []any{cursor.SnapshotID}},
		{"INSERT OR REPLACE INTO sync_snapshot_rows VALUES(?,0,'location','one',1,'{}')", []any{cursor.SnapshotID}},
		{"UPDATE sync_snapshot_row_proofs SET proof=zeroblob(32) WHERE snapshot_id=? AND ordinal=0", []any{cursor.SnapshotID}},
		{"DELETE FROM sync_snapshot_row_proofs WHERE snapshot_id=? AND ordinal=0", []any{cursor.SnapshotID}},
		{"INSERT OR REPLACE INTO sync_snapshot_row_proofs VALUES(?,0,zeroblob(32))", []any{cursor.SnapshotID}},
		{"UPDATE sync_snapshot_seals SET proof=zeroblob(32) WHERE snapshot_id=?", []any{cursor.SnapshotID}},
		{"DELETE FROM sync_snapshot_seals WHERE snapshot_id=?", []any{cursor.SnapshotID}},
		{"INSERT OR REPLACE INTO sync_snapshot_seals VALUES(?,2,zeroblob(32))", []any{cursor.SnapshotID}},
	} {
		if _, err := s.db.ExecContext(t.Context(), attempt.statement, attempt.arguments...); err == nil {
			t.Fatal("sealed snapshot accepted a mutation", attempt.statement)
		}
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_snapshot_rows WHERE snapshot_id=?", cursor.SnapshotID) != 2 ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_snapshot_row_proofs WHERE snapshot_id=?", cursor.SnapshotID) != 2 ||
		!bytes.Equal(scalar[[]byte](t, s.db, "SELECT proof FROM sync_snapshot_seals WHERE snapshot_id=?", cursor.SnapshotID), proof) {
		t.Fatal("rejected writes changed sealed snapshot state")
	}
	// This explicit deletion affects only a disposable fixture. Future retention
	// must independently protect snapshots until their signed expiry.
	execSQL(t, s.db, "DELETE FROM sync_snapshots WHERE id=?", cursor.SnapshotID)
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_snapshot_rows") != 0 ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_snapshot_row_proofs") != 0 ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_snapshot_seals") != 0 {
		t.Fatal("deleting the fixture snapshot did not cascade its proof rows")
	}
}

func TestSyncSnapshotProofStorageRejectsUpdateReplacement(t *testing.T) {
	for _, test := range []struct{ name, statement string }{
		{"identity", "UPDATE OR REPLACE sync_snapshots SET id=? WHERE id='other'"},
		{"rowid", "UPDATE OR REPLACE sync_snapshots SET rowid=(SELECT rowid FROM sync_snapshots WHERE id=?) WHERE id='other'"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, _, _, _, cursor, _ := snapshotMetadataFixture(t)
			execSQL(t, s.db, "INSERT INTO sync_snapshot_seals VALUES(?,0,zeroblob(32))", cursor.SnapshotID)
			execSQL(t, s.db, `INSERT INTO sync_snapshots(id,server_instance_id,dataset_epoch,projection_version,high_watermark,created_at,expires_at,status)
 SELECT 'other',server_instance_id,dataset_epoch,projection_version,high_watermark,created_at,expires_at,status FROM sync_snapshots WHERE id=?`, cursor.SnapshotID)
			execSQL(t, s.db, "PRAGMA recursive_triggers=OFF")
			defer execSQL(t, s.db, "PRAGMA recursive_triggers=ON")
			if _, err := s.db.ExecContext(t.Context(), test.statement, cursor.SnapshotID); err == nil {
				t.Fatal("an unsealed snapshot replaced a sealed snapshot", test.name)
			}
			if scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_snapshot_seals WHERE snapshot_id=?", cursor.SnapshotID) != 1 ||
				scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_snapshots") != 2 {
				t.Fatal("a rejected update changed the frozen snapshot")
			}
		})
	}
}

func TestSyncSnapshotProofStorageRejectsRowIDSentinelReplacement(t *testing.T) {
	s, _, _, _, cursor, _ := snapshotMetadataFixture(t)
	execSQL(t, s.db, "UPDATE sync_snapshots SET rowid=-1 WHERE id=?", cursor.SnapshotID)
	execSQL(t, s.db, "INSERT INTO sync_snapshot_seals VALUES(?,0,zeroblob(32))", cursor.SnapshotID)
	execSQL(t, s.db, "PRAGMA recursive_triggers=OFF")
	defer execSQL(t, s.db, "PRAGMA recursive_triggers=ON")
	for _, statement := range []string{
		`INSERT OR REPLACE INTO sync_snapshots(rowid,id,server_instance_id,dataset_epoch,projection_version,high_watermark,created_at,expires_at,status)
 SELECT -1,'replacement',server_instance_id,dataset_epoch,projection_version,high_watermark,created_at,expires_at,status FROM sync_snapshots WHERE id=?`,
		`INSERT INTO sync_snapshots(id,server_instance_id,dataset_epoch,projection_version,high_watermark,created_at,expires_at,status)
 SELECT 'other',server_instance_id,dataset_epoch,projection_version,high_watermark,created_at,expires_at,status FROM sync_snapshots WHERE id=?`,
	} {
		if _, err := s.db.ExecContext(t.Context(), statement, cursor.SnapshotID); err == nil {
			t.Fatal("sealed rowid -1 did not fail closed", statement)
		}
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_snapshot_seals WHERE snapshot_id=?", cursor.SnapshotID) != 1 {
		t.Fatal("a conflicting insert removed the sealed snapshot")
	}
}

func TestSyncSnapshotProofStorageDoesNotAuthorizeOpaqueFixtureBytes(t *testing.T) {
	s, a, _, token, cursor, signed := snapshotMetadataFixture(t)
	addSnapshotMetadataRow(t, s, cursor.SnapshotID, 0, "search_doc", projectionPair("item", "fixture"), `{"private":"fixture-unreviewed-ocr"}`)
	execSQL(t, s.db, "INSERT INTO sync_snapshot_row_proofs VALUES(?,0,zeroblob(32))", cursor.SnapshotID)
	execSQL(t, s.db, "INSERT INTO sync_snapshot_seals VALUES(?,1,zeroblob(32))", cursor.SnapshotID)
	expected, err := a.syncSnapshotManifestProof(t.Context(), cursor, 1)
	if err != nil || expected == [32]byte{} || scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_snapshot_signer") != 0 {
		t.Fatal("arbitrary stored bytes unexpectedly gained a signer", err)
	}
	metadata, err := a.readSyncSnapshotMetadata(t.Context(), token, signed)
	if err != nil || metadata.TotalRows != 1 {
		t.Fatal("metadata-only observation unexpectedly validated a proof", err)
	}
	encoded, err := json.Marshal(metadata)
	if err != nil || strings.Contains(string(encoded), "fixture-unreviewed-ocr") {
		t.Fatal("metadata-only observation exposed unreviewed fixture contents", err)
	}
}

func TestSyncSnapshotProofStorageCascadesUnsealedRowsAndRequiresReadyManifest(t *testing.T) {
	for _, recursive := range []string{"OFF", "ON"} {
		t.Run(recursive, func(t *testing.T) {
			s := openTestStore(t)
			seedSnapshotSignerFixture(t, s, true)
			execSQL(t, s.db, "PRAGMA recursive_triggers="+recursive)
			defer execSQL(t, s.db, "PRAGMA recursive_triggers=ON")
			execSQL(t, s.db, "INSERT INTO sync_snapshot_row_proofs VALUES('signer-snapshot',0,zeroblob(32))")
			if _, err := s.db.ExecContext(t.Context(), "DELETE FROM sync_snapshot_row_proofs WHERE snapshot_id='signer-snapshot'"); err == nil {
				t.Fatal("a proof was removed while its source row remained")
			}
			execSQL(t, s.db, "DELETE FROM sync_snapshot_rows WHERE snapshot_id='signer-snapshot'")
			if scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_snapshot_row_proofs") != 0 {
				t.Fatal("deleting an unsealed row did not cascade its proof")
			}
			execSQL(t, s.db, "UPDATE sync_snapshots SET status='expired' WHERE id='signer-snapshot'")
			if _, err := s.db.ExecContext(t.Context(), "INSERT INTO sync_snapshot_seals VALUES('signer-snapshot',0,zeroblob(32))"); err == nil {
				t.Fatal("an expired snapshot was sealed")
			}
			execSQL(t, s.db, "UPDATE sync_snapshots SET status='ready' WHERE id='signer-snapshot'")
			execSQL(t, s.db, "INSERT INTO sync_snapshot_seals VALUES('signer-snapshot',0,zeroblob(32))")
		})
	}
}

func TestSyncSnapshotProofStorageSupportsEmptySnapshotsButRejectsOrdinalGaps(t *testing.T) {
	s, _, _, _, cursor, _ := snapshotMetadataFixture(t)
	execSQL(t, s.db, "INSERT INTO sync_snapshot_seals VALUES(?,0,zeroblob(32))", cursor.SnapshotID)
	if scalar[int](t, s.db, "SELECT total_rows FROM sync_snapshot_seals WHERE snapshot_id=?", cursor.SnapshotID) != 0 {
		t.Fatal("empty snapshot count was not retained")
	}
	execSQL(t, s.db, `INSERT INTO sync_snapshots(id,server_instance_id,dataset_epoch,projection_version,high_watermark,created_at,expires_at,status)
 SELECT 'gap',server_instance_id,dataset_epoch,projection_version,high_watermark,created_at,expires_at,status FROM sync_snapshots WHERE id=?`, cursor.SnapshotID)
	addSnapshotMetadataRow(t, s, "gap", 1, "location", "gap-one", `{}`)
	addSnapshotMetadataRow(t, s, "gap", 2, "location", "gap-two", `{}`)
	execSQL(t, s.db, "INSERT INTO sync_snapshot_row_proofs VALUES('gap',1,zeroblob(32)),('gap',2,zeroblob(32))")
	if _, err := s.db.ExecContext(t.Context(), "INSERT INTO sync_snapshot_seals VALUES('gap',2,zeroblob(32))"); err == nil {
		t.Fatal("a gapped ordinal range was sealed")
	}
	execSQL(t, s.db, `INSERT INTO sync_snapshots(id,server_instance_id,dataset_epoch,projection_version,high_watermark,created_at,expires_at,status)
 SELECT 'gap-tail',server_instance_id,dataset_epoch,projection_version,high_watermark,created_at,expires_at,status FROM sync_snapshots WHERE id=?`, cursor.SnapshotID)
	addSnapshotMetadataRow(t, s, "gap-tail", 0, "location", "first", `{}`)
	addSnapshotMetadataRow(t, s, "gap-tail", 2, "location", "third", `{}`)
	execSQL(t, s.db, "INSERT INTO sync_snapshot_row_proofs VALUES('gap-tail',0,zeroblob(32)),('gap-tail',2,zeroblob(32))")
	if _, err := s.db.ExecContext(t.Context(), "INSERT INTO sync_snapshot_seals VALUES('gap-tail',2,zeroblob(32))"); err == nil {
		t.Fatal("a missing middle ordinal was sealed")
	}
}
