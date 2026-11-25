package store

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"homebox/migrations"
)

func locationMovementRootTwentyFourFixture(t *testing.T) (string, *Store) {
	t.Helper()
	path, old := mediaPendingRootTwentyThreeFixture(t)
	all, err := migrations.Load()
	if err != nil {
		t.Fatal(err)
	}
	migration := all[23]
	execSQL(t, old.db, migration.SQL)
	execSQL(t, old.db, "INSERT INTO schema_migrations(version,name,applied_at) VALUES(?,?,'2026')", migration.Version, migration.Filename)
	execSQL(t, old.db, "INSERT INTO schema_migration_checksums VALUES(?,?,?)", migration.Version, migration.Filename, migration.Checksum)
	return path, old
}

func TestLocationMovementIndexMigrationPreservesRetainedSourceAndUsesOrderedIndex(t *testing.T) {
	path, old := locationMovementRootTwentyFourFixture(t)
	location := LocationData{ID: "history-owner", Code: "HISTORY-OLD", Name: "Old location", Kind: "box", Status: "active"}
	execSQL(t, old.db, `INSERT INTO locations(id,code,name,kind,status,created_at,updated_at) VALUES(?,?,?,?,?,'legacy','legacy')`, location.ID, location.Code, location.Name, location.Kind, location.Status)
	seedCatalogProjection(t, old, "location", location.ID, location)
	seedLocationMovement(t, old, "history-event", location.ID, nil, nil, "legacy time", true)
	execSQL(t, old.db, `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at) VALUES('location_event','history-event',1,' {"legacy":true} ','legacy')`)
	execSQL(t, old.db, `INSERT INTO sync_changes(entity_type,entity_id,operation,entity_revision,projection_json,created_at) VALUES('location_event','history-event','upsert',1,' {"legacy":true} ','legacy')`)
	before, projected := domainState(t, old), currentProjectionState(t, old)
	epoch := scalar[string](t, old.db, "SELECT dataset_epoch FROM sync_state")
	_ = old.db.Close()
	s, err := Open(t.Context(), Config{Path: path, Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Report().From != 24 || s.Report().To < 25 || s.Report().BackupPath == "" || scalar[string](t, s.db, "SELECT dataset_epoch FROM sync_state") != epoch {
		t.Fatal("movement index upgrade changed the dataset epoch or omitted recovery")
	}
	requireDomainState(t, s, before)
	if !reflect.DeepEqual(projected, currentProjectionState(t, s)) || VerifySnapshot(t.Context(), s.Report().BackupPath) != nil {
		t.Fatal("movement index upgrade changed frozen bytes or lost the verified backup")
	}
	rows, err := s.db.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+locationMovementSelect+" WHERE e.location_id=? AND e.id>? ORDER BY e.id LIMIT ?", location.ID, "", 51)
	if err != nil {
		t.Fatal(err)
	}
	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	plan := strings.Join(details, " ")
	if !strings.Contains(plan, "idx_location_events_location_id") || strings.Contains(plan, "USE TEMP B-TREE") {
		t.Fatal("movement page did not use the owner-scoped ordered index", plan)
	}
}

func TestLocationMovementIndexMigrationRollsBackOnLedgerFailure(t *testing.T) {
	path, old := locationMovementRootTwentyFourFixture(t)
	execSQL(t, old.db, `CREATE TRIGGER fail_movement_ledger BEFORE INSERT ON schema_migration_checksums WHEN NEW.version=25 BEGIN SELECT RAISE(ABORT,'movement index ledger failure'); END`)
	before := domainState(t, old)
	ddl, err := schemaDescription(t.Context(), old.db)
	if err != nil {
		t.Fatal(err)
	}
	_ = old.db.Close()
	if s, err := Open(t.Context(), Config{Path: path, Migrate: true}); err == nil {
		_ = s.Close()
		t.Fatal("failed movement index ledger committed")
	} else if !strings.Contains(err.Error(), "verified recovery snapshot retained") {
		t.Fatal("failed movement upgrade lost recovery evidence", err)
	}
	unchanged := rawDatabase(t, path)
	requireDomainState(t, &Store{db: unchanged}, before)
	actual, err := schemaDescription(t.Context(), unchanged)
	if err != nil || !reflect.DeepEqual(actual, ddl) || scalar[int](t, unchanged, "SELECT MAX(version) FROM schema_migrations") != 24 ||
		scalar[int](t, unchanged, "SELECT COUNT(*) FROM sqlite_schema WHERE name='idx_location_events_location_id'") != 0 {
		t.Fatal("failed movement index upgrade leaked DDL or ledger rows", err)
	}
	backups, err := filepath.Glob(filepath.Join(filepath.Dir(path), "migration-backups", "*.sqlite"))
	if err != nil || len(backups) != 1 || VerifySnapshot(t.Context(), backups[0]) != nil {
		t.Fatal("failed movement upgrade did not retain its verified original backup", err)
	}
}
