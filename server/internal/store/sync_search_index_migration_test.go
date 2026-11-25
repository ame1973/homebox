package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"homebox/migrations"
)

func syncProofRootTwentySevenFixture(t *testing.T) (string, *Store) {
	t.Helper()
	path, old := syncProofRootTwentySixFixture(t)
	all, err := migrations.Load()
	if err != nil {
		t.Fatal(err)
	}
	migration := all[26]
	execSQL(t, old.db, migration.SQL)
	execSQL(t, old.db, "INSERT INTO schema_migrations(version,name,applied_at) VALUES(?,?,'2026')", migration.Version, migration.Filename)
	execSQL(t, old.db, "INSERT INTO schema_migration_checksums VALUES(?,?,?)", migration.Version, migration.Filename, migration.Checksum)
	return path, old
}

func seedSyncSearchIndexSnapshot(t *testing.T, s *Store, identity, payload string, revision int64) {
	t.Helper()
	execSQL(t, s.db, `INSERT INTO sync_snapshots(id,server_instance_id,dataset_epoch,projection_version,high_watermark,created_at,expires_at,status)
 SELECT 'index-snapshot',server_instance_id,dataset_epoch,1,0,'2026-11-04T12:00:00Z','2026-11-04T12:15:00Z','ready' FROM sync_state`)
	execSQL(t, s.db, `INSERT INTO sync_snapshot_rows(snapshot_id,ordinal,entity_type,entity_id,entity_revision,payload_json)
 VALUES('index-snapshot',0,'search_doc',?,?,?)`, identity, revision, payload)
}

func requireSearchRevisionIndexes(t *testing.T, db *sql.DB, dataset syncDataset, identity string) {
	t.Helper()
	for _, query := range []struct {
		statement, index string
		args             []any
	}{
		{`SELECT MAX(entity_revision) FROM sync_changes WHERE entity_type='search_doc' AND entity_id=?`, "idx_sync_change_search_revision", []any{identity}},
		{`SELECT MAX(r.entity_revision) FROM sync_snapshot_rows r JOIN sync_snapshots s ON s.id=r.snapshot_id
 WHERE r.entity_type='search_doc' AND r.entity_id=? AND s.server_instance_id=? AND s.dataset_epoch=? AND s.projection_version=?`,
			"idx_sync_snapshot_search_revision", []any{identity, dataset.Instance, dataset.Epoch, dataset.Version}},
	} {
		rows, err := db.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+query.statement, query.args...)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for rows.Next() {
			var node, parent, unused int
			var detail string
			if err = rows.Scan(&node, &parent, &unused, &detail); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			found = found || strings.Contains(detail, query.index)
		}
		if err = errors.Join(rows.Err(), rows.Close()); err != nil {
			t.Fatal(err)
		}
		if !found {
			t.Fatal("search revision lookup did not use its supporting index", query.index)
		}
	}
}

func TestSyncSearchRevisionIndexesSupportActualBackfillLookups(t *testing.T) {
	s := openTestStore(t)
	dataset := syncDataset{
		Instance: scalar[string](t, s.db, "SELECT server_instance_id FROM sync_state"),
		Epoch:    scalar[string](t, s.db, "SELECT dataset_epoch FROM sync_state"),
		Version:  1,
	}
	target := projectionPair("item", "identity/\x00家庭\n")
	payload := `{"entity_type":"item","entity_id":"identity/\u0000家庭\n","approved_text":"Approved"}`
	execSQL(t, s.db, `INSERT INTO sync_changes(entity_type,entity_id,operation,entity_revision,projection_json,created_at)
 VALUES('search_doc',?,'upsert',3,?,'2026-11-04T12:00:00Z')`, target, payload)
	execSQL(t, s.db, `INSERT INTO sync_changes(entity_type,entity_id,operation,entity_revision,projection_json,created_at)
 VALUES('item',?,'delete',999,NULL,'2026-11-04T12:00:00Z')`, target)
	execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('search_doc',?,5)", target)
	seedSyncSearchIndexSnapshot(t, s, target, payload, 5)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.PrepareContext(t.Context(), `INSERT INTO sync_snapshot_rows(snapshot_id,ordinal,entity_type,entity_id,entity_revision,payload_json)
 VALUES('index-snapshot',?,'search_doc',?,1,'{}')`)
	if err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= 2000; n++ {
		if _, err = stmt.ExecContext(t.Context(), n, projectionPair("item", fmt.Sprintf("unrelated-%04d", n))); err != nil {
			stmt.Close()
			t.Fatal(err)
		}
	}
	if err = errors.Join(stmt.Close(), tx.Commit()); err != nil {
		t.Fatal(err)
	}
	requireSearchRevisionIndexes(t, s.db, dataset, target)
	tx, err = s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	counter, err := readSyncSearchCounter(t.Context(), tx, dataset, target)
	if err != nil || !counter.Valid || counter.Int64 != 5 {
		t.Fatal("indexed revision lookup changed actual source semantics", counter, err)
	}
	if _, err = tx.ExecContext(t.Context(), `INSERT INTO sync_changes(entity_type,entity_id,operation,entity_revision,projection_json,created_at)
 VALUES('search_doc',?,'upsert',6,?,'2026-11-04T12:00:00Z')`, target, payload); err != nil {
		t.Fatal(err)
	}
	if _, err = readSyncSearchCounter(t.Context(), tx, dataset, target); !errors.Is(err, ErrDomainState) {
		t.Fatal("new indexed history did not expose a regressed counter", err)
	}
	if _, err = tx.ExecContext(t.Context(), "UPDATE projection_revisions SET revision=6 WHERE entity_type='search_doc' AND entity_id=?", target); err != nil {
		t.Fatal(err)
	}
	if counter, err = readSyncSearchCounter(t.Context(), tx, dataset, target); err != nil || !counter.Valid || counter.Int64 != 6 {
		t.Fatal("indexed history did not follow a valid counter advance", counter, err)
	}
	if _, err = tx.ExecContext(t.Context(), "DELETE FROM sync_changes WHERE entity_type='search_doc' AND entity_id=? AND entity_revision=6", target); err != nil {
		t.Fatal(err)
	}
	if counter, err = readSyncSearchCounter(t.Context(), tx, dataset, target); err != nil || !counter.Valid || counter.Int64 != 6 {
		t.Fatal("pruned indexed history changed the retained counter", counter, err)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = readSyncSearchCounter(cancelled, tx, dataset, target); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled indexed lookup returned a result", err)
	}
}

func TestSyncSearchRevisionIndexMigrationPreservesRetainedBytes(t *testing.T) {
	path, old := syncProofRootTwentySevenFixture(t)
	seq, payload := seedUnprovenSearchChange(t, old)
	identity := projectionPair("item", "legacy")
	proof := bytes.Repeat([]byte{0x5a}, 32)
	signer := bytes.Repeat([]byte{0xa5}, 32)
	execSQL(t, old.db, "INSERT INTO sync_search_proofs(seq,proof) VALUES(?,?)", seq, proof)
	execSQL(t, old.db, "INSERT INTO sync_search_signer(singleton,key_proof) VALUES(1,?)", signer)
	seedSyncSearchIndexSnapshot(t, old, identity, payload, 7)
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
	if s.Report().From != 27 || s.Report().To < 28 || s.Report().BackupPath == "" || scalar[string](t, s.db, "SELECT dataset_epoch FROM sync_state") != epoch {
		t.Fatal("index migration changed dataset or omitted its recovery snapshot")
	}
	requireDomainState(t, s, before)
	if !reflect.DeepEqual(projections, currentProjectionState(t, s)) || scalar[string](t, s.db, "SELECT projection_json FROM sync_changes WHERE seq=?", seq) != payload ||
		scalar[string](t, s.db, "SELECT payload_json FROM sync_snapshot_rows WHERE snapshot_id='index-snapshot'") != payload ||
		!bytes.Equal(scalar[[]byte](t, s.db, "SELECT proof FROM sync_search_proofs WHERE seq=?", seq), proof) ||
		!bytes.Equal(scalar[[]byte](t, s.db, "SELECT key_proof FROM sync_search_signer"), signer) {
		t.Fatal("index migration rewrote retained payload, proof or signer bytes")
	}
	if err = VerifySnapshot(t.Context(), s.Report().BackupPath); err != nil {
		t.Fatal("pre-migration snapshot did not verify", err)
	}
	dataset := syncDataset{Instance: scalar[string](t, s.db, "SELECT server_instance_id FROM sync_state"), Epoch: epoch, Version: 1}
	requireSearchRevisionIndexes(t, s.db, dataset, identity)
}

func TestSyncSearchRevisionIndexMigrationRollsBackLateLedgerFailure(t *testing.T) {
	path, old := syncProofRootTwentySevenFixture(t)
	seq, payload := seedUnprovenSearchChange(t, old)
	seedSyncSearchIndexSnapshot(t, old, projectionPair("item", "legacy"), payload, 7)
	execSQL(t, old.db, `CREATE TRIGGER fail_search_indexes BEFORE INSERT ON schema_migration_checksums WHEN NEW.version=28 BEGIN
 SELECT RAISE(ABORT,'search index ledger failure'); END`)
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
		t.Fatal("failed ledger write committed the search indexes")
	} else if !strings.Contains(err.Error(), "verified recovery snapshot retained") {
		t.Fatal("failed migration did not retain its recovery snapshot", err)
	}
	unchanged := rawDatabase(t, path)
	requireDomainState(t, &Store{db: unchanged}, before)
	actual, err := schemaDescription(t.Context(), unchanged)
	if err != nil || !reflect.DeepEqual(actual, ddl) || scalar[int](t, unchanged, "SELECT MAX(version) FROM schema_migrations") != 27 ||
		scalar[int](t, unchanged, "SELECT COUNT(*) FROM sqlite_schema WHERE name IN ('idx_sync_change_search_revision','idx_sync_snapshot_search_revision')") != 0 ||
		scalar[string](t, unchanged, "SELECT projection_json FROM sync_changes WHERE seq=?", seq) != payload ||
		scalar[string](t, unchanged, "SELECT payload_json FROM sync_snapshot_rows WHERE snapshot_id='index-snapshot'") != payload {
		t.Fatal("failed index migration leaked schema or changed frozen bytes", err)
	}
	backups, err := filepath.Glob(filepath.Join(filepath.Dir(path), "migration-backups", "*.sqlite"))
	if err != nil || len(backups) != 1 || VerifySnapshot(t.Context(), backups[0]) != nil {
		t.Fatal("failed migration did not preserve a verified snapshot", err)
	}
}
