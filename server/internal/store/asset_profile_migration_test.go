package store

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"homebox/migrations"
)

func assetRootFifteenFixture(t *testing.T) (string, *Store) {
	t.Helper()
	path, fixture := assetRootFourteenFixture(t)
	seedAssetUpgradeSource(t, fixture.db)
	execSQL(t, fixture.db, "INSERT INTO projection_revisions VALUES('item','asset-item',1),('unit','asset-unit',1)")
	all, err := migrations.Load()
	if err != nil {
		t.Fatal(err)
	}
	migration := all[14]
	tx, err := fixture.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(t.Context(), migration.SQL); err != nil {
		t.Fatal(err)
	}
	if err = validateAssetsBackfill(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(t.Context(), "INSERT INTO schema_migrations(version,name,applied_at) VALUES(?,?,'2026')", migration.Version, migration.Filename); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(t.Context(), "INSERT INTO schema_migration_checksums VALUES(?,?,?)", migration.Version, migration.Filename, migration.Checksum); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return path, fixture
}

func TestAssetProfileHistoryUpgradePreservesEveryOriginalSourceCurrentFrozenByteAndEpoch(t *testing.T) {
	path, old := assetRootFifteenFixture(t)
	execSQL(t, old.db, "UPDATE current_projections SET payload_json=' {\"ExactPreviousProfileBytes\":true} ',updated_at='unknown-original-projection-time' WHERE entity_type IN('asset','usage','cost','disposal')")
	execSQL(t, old.db, "INSERT INTO sync_changes(entity_type,entity_id,operation,entity_revision,projection_json,created_at) SELECT entity_type,entity_id,'upsert',entity_revision,payload_json,updated_at FROM current_projections WHERE entity_type IN('asset','usage','cost','disposal')")
	before, projections := domainState(t, old), currentProjectionState(t, old)
	epoch := scalar[string](t, old.db, "SELECT dataset_epoch FROM sync_state")
	_ = old.db.Close()
	s, err := Open(t.Context(), Config{Path: path, Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Report().From != 15 || s.Report().To < 16 || s.Report().BackupPath == "" || scalar[string](t, s.db, "SELECT dataset_epoch FROM sync_state") != epoch {
		t.Fatal("profile history upgrade changed epoch or omitted verified recovery")
	}
	requireDomainState(t, s, before)
	if !reflect.DeepEqual(currentProjectionState(t, s), projections) || scalar[int](t, s.db, "SELECT COUNT(*) FROM asset_profile_changes") != 0 {
		t.Fatal("new audit table rewrote frozen/current source or fabricated old before-images")
	}
	if err = VerifySnapshot(t.Context(), s.Report().BackupPath); err != nil {
		t.Fatal(err)
	}
	backup, err := openSQL(s.Report().BackupPath, true)
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	if scalar[int](t, backup, "SELECT MAX(version) FROM schema_migrations") != 15 || scalar[int](t, backup, "SELECT COUNT(*) FROM sqlite_schema WHERE name='asset_profile_changes'") != 0 {
		t.Fatal("snapshot does not retain the actual pre-audit schema")
	}
	var a, b, c int
	var plan string
	if err = s.db.QueryRowContext(t.Context(), "EXPLAIN QUERY PLAN SELECT mutation_event_id FROM asset_profile_changes WHERE item_unit_id='asset-unit' ORDER BY mutation_event_id").Scan(&a, &b, &c, &plan); err != nil || !strings.Contains(plan, "idx_asset_profile_changes_owner") {
		t.Fatal("immutable profile history lacks its actual owner lookup index", plan, err)
	}
}

func TestAssetProfileHistoryLedgerFailureRollsBackTableIndexTriggersAndRetainsOriginalSnapshot(t *testing.T) {
	path, old := assetRootFifteenFixture(t)
	execSQL(t, old.db, "CREATE TRIGGER fail_profile_ledger BEFORE INSERT ON schema_migration_checksums WHEN NEW.version=16 BEGIN SELECT RAISE(ABORT,'profile ledger failure'); END")
	before := domainState(t, old)
	ddl, err := schemaDescription(t.Context(), old.db)
	if err != nil {
		t.Fatal(err)
	}
	_ = old.db.Close()
	if s, err := Open(t.Context(), Config{Path: path, Migrate: true}); err == nil {
		_ = s.Close()
		t.Fatal("failed profile checksum publication committed audit DDL")
	} else if !strings.Contains(err.Error(), "verified recovery snapshot retained") {
		t.Fatal("failed audit upgrade lost verified original recovery evidence", err)
	}
	unchanged := rawDatabase(t, path)
	requireDomainState(t, &Store{db: unchanged}, before)
	actual, err := schemaDescription(t.Context(), unchanged)
	if err != nil || !reflect.DeepEqual(actual, ddl) || scalar[int](t, unchanged, "SELECT MAX(version) FROM schema_migrations") != 15 || scalar[int](t, unchanged, "SELECT COUNT(*) FROM schema_migration_checksums") != 15 {
		t.Fatal("failed profile upgrade changed original tables/indices/triggers/checksums", err)
	}
	backups, err := filepath.Glob(filepath.Join(filepath.Dir(path), "migration-backups", "*.sqlite"))
	if err != nil || len(backups) != 1 || VerifySnapshot(t.Context(), backups[0]) != nil {
		t.Fatal("failed audit upgrade did not retain one verified original snapshot", err)
	}
}

func TestAssetProfileHistoryRequiresActualDeferredAuditRealOwnerAndJSONObject(t *testing.T) {
	fixture := holdingsFixture(t)
	s := fixture.store
	if _, err := assetReader(t, fixture).PutProfile(t.Context(), fixture.token, "profile-real-audit-owner", fixture.unit.ID, fixture.unit.Revision, manualProfileInput(fixture.individual.Revision, 0)); err != nil {
		t.Fatal(err)
	}
	unusedEvent := scalar[string](t, s.db, "SELECT id FROM mutation_events WHERE operation='post_items' LIMIT 1")
	before := assetProfileState(t, s)
	for _, invalid := range []struct {
		event any
		owner string
		body  string
	}{{"missing-audit", fixture.unit.ID, "{}"}, {nil, fixture.unit.ID, "{}"}, {unusedEvent, "missing-owner", "{}"},
		{unusedEvent, fixture.unit.ID, "not JSON"}, {unusedEvent, fixture.unit.ID, "null"}, {unusedEvent, fixture.unit.ID, "[]"}} {
		if _, err := s.db.ExecContext(t.Context(), "INSERT INTO asset_profile_changes VALUES(?,?,?)", invalid.event, invalid.owner, invalid.body); err == nil {
			t.Fatal("profile before-image bypassed real audit/owner/object authority")
		}
		requireAssetProfileState(t, s, before)
	}
	for _, statement := range []string{"UPDATE asset_profile_changes SET context_json=context_json", "DELETE FROM asset_profile_changes"} {
		if _, err := s.db.ExecContext(t.Context(), statement); err == nil {
			t.Fatal("profile before-image was destructively mutable", statement)
		}
		requireAssetProfileState(t, s, before)
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM asset_profile_changes c JOIN mutation_events e ON e.id=c.mutation_event_id WHERE e.operation='put_units_id_asset'") != 1 {
		t.Fatal("genuine profile write did not atomically satisfy its deferred audit FK")
	}
}
