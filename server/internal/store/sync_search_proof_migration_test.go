package store

import (
	"bytes"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"homebox/migrations"
)

func syncProofRootTwentyFiveFixture(t *testing.T) (string, *Store) {
	t.Helper()
	path, old := mediaPendingRootTwentyThreeFixture(t)
	all, err := migrations.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range all[23:25] {
		execSQL(t, old.db, migration.SQL)
		execSQL(t, old.db, "INSERT INTO schema_migrations(version,name,applied_at) VALUES(?,?,'2026')", migration.Version, migration.Filename)
		execSQL(t, old.db, "INSERT INTO schema_migration_checksums VALUES(?,?,?)", migration.Version, migration.Filename, migration.Checksum)
	}
	return path, old
}

func seedUnprovenSearchChange(t *testing.T, s *Store) (int64, string) {
	t.Helper()
	payload := ` { "entity_type":"item", "entity_id":"legacy", "approved_text":"retained-unproven" } `
	execSQL(t, s.db, `INSERT INTO sync_changes(entity_type,entity_id,operation,entity_revision,projection_json,created_at)
 VALUES('search_doc',?,'upsert',7,?,'2026-01-01T00:00:00Z')`, projectionPair("item", "legacy"), payload)
	return scalar[int64](t, s.db, "SELECT MAX(seq) FROM sync_changes"), payload
}

func TestSyncSearchProofMigrationPreservesHistoryAndStartsEmpty(t *testing.T) {
	path, old := syncProofRootTwentyFiveFixture(t)
	seq, payload := seedUnprovenSearchChange(t, old)
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
	if s.Report().From != 25 || s.Report().To < 26 || s.Report().BackupPath == "" || scalar[string](t, s.db, "SELECT dataset_epoch FROM sync_state") != epoch {
		t.Fatal("proof migration changed the dataset or omitted a recovery snapshot")
	}
	requireDomainState(t, s, before)
	if !reflect.DeepEqual(projections, currentProjectionState(t, s)) || scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_search_proofs") != 0 ||
		scalar[string](t, s.db, "SELECT projection_json FROM sync_changes WHERE seq=?", seq) != payload {
		t.Fatal("migration guessed historical proof or changed frozen source")
	}
	if err = VerifySnapshot(t.Context(), s.Report().BackupPath); err != nil {
		t.Fatal("the pre-migration snapshot did not verify", err)
	}
}

func TestSyncSearchProofMigrationRejectsLateInjectionAndRollsBack(t *testing.T) {
	path, old := syncProofRootTwentyFiveFixture(t)
	seedUnprovenSearchChange(t, old)
	execSQL(t, old.db, `CREATE TRIGGER inject_sync_search_proof AFTER INSERT ON schema_migration_checksums WHEN NEW.version=26 BEGIN
 INSERT INTO sync_search_proofs(seq,proof) SELECT MAX(seq),zeroblob(32) FROM sync_changes WHERE entity_type='search_doc' AND operation='upsert'; END`)
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
		t.Fatal("a migration committed an injected proof")
	} else if !strings.Contains(err.Error(), "verified recovery snapshot retained") {
		t.Fatal("failed migration did not retain its recovery snapshot", err)
	}
	unchanged := rawDatabase(t, path)
	requireDomainState(t, &Store{db: unchanged}, before)
	actual, err := schemaDescription(t.Context(), unchanged)
	if err != nil || !reflect.DeepEqual(actual, ddl) || scalar[int](t, unchanged, "SELECT MAX(version) FROM schema_migrations") != 25 ||
		scalar[int](t, unchanged, "SELECT COUNT(*) FROM sqlite_schema WHERE name='sync_search_proofs'") != 0 {
		t.Fatal("failed migration leaked proof schema or ledger state", err)
	}
	backups, err := filepath.Glob(filepath.Join(filepath.Dir(path), "migration-backups", "*.sqlite"))
	if err != nil || len(backups) != 1 || VerifySnapshot(t.Context(), backups[0]) != nil {
		t.Fatal("failed migration did not preserve a verified snapshot", err)
	}
}

func TestSyncSearchProofRowsBindOnlyUpsertParentsAndCannotBeReplaced(t *testing.T) {
	s, locations, auth, token := locationFixture(t)
	location := createLocation(t, locations, token, "NON-SEARCH-PARENT", nil)
	identity := projectionPair("item", "legacy")
	searchSeq, _ := seedUnprovenSearchChange(t, s)
	otherIdentity := projectionPair("item", "other-legacy")
	execSQL(t, s.db, `INSERT INTO sync_changes(entity_type,entity_id,operation,entity_revision,projection_json,created_at)
 VALUES('search_doc',?,'upsert',1,?,'2026-01-01T00:00:00Z')`, otherIdentity, `{"entity_type":"item","entity_id":"other-legacy","approved_text":"other"}`)
	otherSearchSeq := scalar[int64](t, s.db, "SELECT seq FROM sync_changes WHERE entity_type='search_doc' AND entity_id=?", otherIdentity)
	locationSeq := scalar[int64](t, s.db, "SELECT seq FROM sync_changes WHERE entity_type='location' AND entity_id=?", location.ID)
	execSQL(t, s.db, `INSERT INTO sync_changes(entity_type,entity_id,operation,entity_revision,projection_json,created_at)
 VALUES('search_doc',?,'delete',2,NULL,'2026-01-01T00:00:00Z')`, identity)
	deleteSeq := scalar[int64](t, s.db, "SELECT MAX(seq) FROM sync_changes")
	proof := bytes.Repeat([]byte{0x5a}, 32)
	execSQL(t, s.db, "INSERT INTO sync_search_proofs(seq,proof) VALUES(?,?)", searchSeq, proof)
	execSQL(t, s.db, "PRAGMA recursive_triggers=OFF")
	defer execSQL(t, s.db, "PRAGMA recursive_triggers=ON")
	for _, attempt := range []struct {
		statement string
		arguments []any
	}{
		{"UPDATE sync_search_proofs SET proof=zeroblob(32) WHERE seq=?", []any{searchSeq}},
		{"DELETE FROM sync_search_proofs WHERE seq=?", []any{searchSeq}},
		{"INSERT OR REPLACE INTO sync_search_proofs(seq,proof) VALUES(?,zeroblob(32))", []any{searchSeq}},
		{"INSERT INTO sync_search_proofs(seq,proof) VALUES(?,zeroblob(32))", []any{locationSeq}},
		{"INSERT INTO sync_search_proofs(seq,proof) VALUES(?,zeroblob(32))", []any{deleteSeq}},
		{"INSERT INTO sync_search_proofs(seq,proof) VALUES(?,zeroblob(32))", []any{deleteSeq + 100}},
		{"INSERT INTO sync_search_proofs(seq,proof) VALUES(?,zeroblob(31))", []any{otherSearchSeq}},
		{"INSERT INTO sync_search_proofs(seq,proof) VALUES(?,'not a blob')", []any{otherSearchSeq}},
		{"INSERT INTO sync_search_proofs(seq,proof) VALUES(?,NULL)", []any{otherSearchSeq}},
		{"INSERT INTO sync_search_proofs(seq,proof) VALUES(NULL,zeroblob(32))", nil},
	} {
		if _, err := s.db.ExecContext(t.Context(), attempt.statement, attempt.arguments...); err == nil {
			t.Fatal("invalid or replacing proof row was accepted", attempt.statement)
		}
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_search_proofs") != 1 || !bytes.Equal(scalar[[]byte](t, s.db, "SELECT proof FROM sync_search_proofs WHERE seq=?", searchSeq), proof) {
		t.Fatal("rejected proof writes changed the original marker")
	}
	cursor := testSyncDeltaCursor(t, s, auth, 0)
	if page, err := auth.readSyncDeltaPage(t.Context(), token, cursor, syncPageLimits{Records: 500, PayloadBytes: 4096, PageBytes: 8192}); !errors.Is(err, errSyncSearchUnproven) || !reflect.DeepEqual(page, syncDeltaPage{}) {
		t.Fatal("an arbitrary proof row authorized search text", page, err)
	}
	execSQL(t, s.db, "DELETE FROM sync_changes WHERE seq=?", searchSeq)
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_search_proofs") != 0 {
		t.Fatal("pruning a retained change did not remove its proof with recursive triggers disabled")
	}
	execSQL(t, s.db, "PRAGMA recursive_triggers=ON")
	execSQL(t, s.db, "INSERT INTO sync_search_proofs(seq,proof) VALUES(?,?)", otherSearchSeq, proof)
	if _, err := s.db.ExecContext(t.Context(), "DELETE FROM sync_search_proofs WHERE seq=?", otherSearchSeq); err == nil {
		t.Fatal("direct proof deletion succeeded with recursive triggers enabled")
	}
	execSQL(t, s.db, "DELETE FROM sync_changes WHERE seq=?", otherSearchSeq)
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_search_proofs") != 0 {
		t.Fatal("pruning a retained change did not remove its proof with recursive triggers enabled")
	}
}
