package store

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"homebox/migrations"
)

func retainedRowsRootNineteenFixture(t *testing.T) (string, *Store) {
	t.Helper()
	path, fixture := mediaRootEighteenFixture(t)
	all, err := migrations.Load()
	if err != nil {
		t.Fatal(err)
	}
	migration := all[18]
	execSQL(t, fixture.db, migration.SQL)
	execSQL(t, fixture.db, "INSERT INTO schema_migrations(version,name,applied_at) VALUES(?,?,'2026')", migration.Version, migration.Filename)
	execSQL(t, fixture.db, "INSERT INTO schema_migration_checksums VALUES(?,?,?)", migration.Version, migration.Filename, migration.Checksum)
	return path, fixture
}

func TestRetainedRowsMigrationPreservesSourceProjectionsFrozenBytesAndEpoch(t *testing.T) {
	path, old := retainedRowsRootNineteenFixture(t)
	execSQL(t, old.db, `UPDATE current_projections SET payload_json=' { "retained" : true } ',updated_at='exact-original' WHERE entity_type IN('purchase','asset')`)
	execSQL(t, old.db, `INSERT INTO sync_changes(entity_type,entity_id,operation,entity_revision,projection_json,created_at) SELECT entity_type,entity_id,'upsert',entity_revision,payload_json,updated_at FROM current_projections WHERE entity_type IN('purchase','asset')`)
	before, projected := assetProfileState(t, old), currentProjectionState(t, old)
	epoch := scalar[string](t, old.db, "SELECT dataset_epoch FROM sync_state")
	_ = old.db.Close()
	s, err := Open(t.Context(), Config{Path: path, Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Report().From != 19 || s.Report().To < 20 || s.Report().BackupPath == "" || scalar[string](t, s.db, "SELECT dataset_epoch FROM sync_state") != epoch {
		t.Fatal("retained-row upgrade omitted its verified recovery or changed the dataset epoch")
	}
	requireAssetProfileState(t, s, before)
	if !reflect.DeepEqual(projected, currentProjectionState(t, s)) {
		t.Fatal("retained-row upgrade rewrote existing projection bytes")
	}
	if err = VerifySnapshot(t.Context(), s.Report().BackupPath); err != nil {
		t.Fatal("pre-upgrade snapshot failed verification", err)
	}
	backup, err := openSQL(s.Report().BackupPath, true)
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	if scalar[int](t, backup, "SELECT MAX(version) FROM schema_migrations") != 19 || scalar[int](t, backup, "SELECT COUNT(*) FROM sqlite_schema WHERE name='sync_changes_no_replace'") != 0 {
		t.Fatal("verified snapshot was not the actual certified pre-upgrade schema")
	}
	execSQL(t, s.db, "PRAGMA recursive_triggers=OFF")
	if _, err = s.db.ExecContext(t.Context(), `INSERT OR REPLACE INTO sync_changes SELECT seq,entity_type,entity_id,operation,entity_revision,'{"poison":true}',created_at FROM sync_changes WHERE operation='upsert' LIMIT 1`); err == nil || !strings.Contains(err.Error(), "sync payloads are frozen") {
		t.Fatal("upgraded frozen payload was still replaceable on a default-style connection", err)
	}
	requireAssetProfileState(t, s, before)
}

func TestRetainedRowsMigrationLateFailureRollsBackAllGuardsAndLedger(t *testing.T) {
	path, old := retainedRowsRootNineteenFixture(t)
	execSQL(t, old.db, `CREATE TRIGGER fail_retained_ledger BEFORE INSERT ON schema_migration_checksums WHEN NEW.version=20 BEGIN SELECT RAISE(ABORT,'retained guard ledger failure'); END`)
	before := assetProfileState(t, old)
	ddl, err := schemaDescription(t.Context(), old.db)
	if err != nil {
		t.Fatal(err)
	}
	_ = old.db.Close()
	if s, err := Open(t.Context(), Config{Path: path, Migrate: true}); err == nil {
		_ = s.Close()
		t.Fatal("failed ledger insertion committed retained-row guards")
	} else if !strings.Contains(err.Error(), "verified recovery snapshot retained") {
		t.Fatal("failed upgrade lost verified recovery evidence", err)
	}
	unchanged := rawDatabase(t, path)
	requireAssetProfileState(t, &Store{db: unchanged}, before)
	actual, err := schemaDescription(t.Context(), unchanged)
	if err != nil || !reflect.DeepEqual(actual, ddl) || scalar[int](t, unchanged, "SELECT MAX(version) FROM schema_migrations") != 19 ||
		scalar[int](t, unchanged, "SELECT COUNT(*) FROM schema_migration_checksums") != 19 {
		t.Fatal("failed guard upgrade leaked DDL or migration history", err)
	}
	backups, err := filepath.Glob(filepath.Join(filepath.Dir(path), "migration-backups", "*.sqlite"))
	if err != nil || len(backups) != 1 || VerifySnapshot(t.Context(), backups[0]) != nil {
		t.Fatal("failed guard upgrade did not retain one verified original snapshot", err)
	}
}
