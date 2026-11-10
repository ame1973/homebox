package store

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"homebox/migrations"
)

func disposalRootSeventeenFixture(t *testing.T) (string, *Store) {
	t.Helper()
	path, fixture := purchaseRootSixteenFixture(t)
	all, err := migrations.Load()
	if err != nil {
		t.Fatal(err)
	}
	migration := all[16]
	execSQL(t, fixture.db, migration.SQL)
	execSQL(t, fixture.db, "INSERT INTO schema_migrations(version,name,applied_at) VALUES(?,?,'2026')", migration.Version, migration.Filename)
	execSQL(t, fixture.db, "INSERT INTO schema_migration_checksums VALUES(?,?,?)", migration.Version, migration.Filename, migration.Checksum)
	return path, fixture
}

func TestDisposalCorrectionMigrationRetainsAllOriginalSourceCountersFrozenBytesAndCertifiedEpoch(t *testing.T) {
	path, old := disposalRootSeventeenFixture(t)
	execSQL(t, old.db, "UPDATE current_projections SET payload_json=' {\"ExactRetainedFinancialBytes\":true} ',updated_at='unknown-retained-time' WHERE entity_type IN('purchase','purchase_line','asset','usage','cost','disposal')")
	execSQL(t, old.db, "INSERT INTO sync_changes(entity_type,entity_id,operation,entity_revision,projection_json,created_at) SELECT entity_type,entity_id,'upsert',entity_revision,payload_json,updated_at FROM current_projections WHERE entity_type IN('purchase','purchase_line','asset','usage','cost','disposal')")
	before, projected := assetProfileState(t, old), currentProjectionState(t, old)
	epoch := scalar[string](t, old.db, "SELECT dataset_epoch FROM sync_state")
	_ = old.db.Close()
	s, err := Open(t.Context(), Config{Path: path, Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Report().From != 17 || s.Report().To < 18 || s.Report().BackupPath == "" || scalar[string](t, s.db, "SELECT dataset_epoch FROM sync_state") != epoch {
		t.Fatal("disposal correction upgrade omitted actual recovery or rotated certified authority")
	}
	requireAssetProfileState(t, s, before)
	if !reflect.DeepEqual(projected, currentProjectionState(t, s)) || scalar[int](t, s.db, "SELECT COUNT(*) FROM asset_disposal_corrections") != 0 {
		t.Fatal("migration rewrote source/frozen bytes or fabricated financial decisions")
	}
	if err = VerifySnapshot(t.Context(), s.Report().BackupPath); err != nil {
		t.Fatal(err)
	}
	backup, err := openSQL(s.Report().BackupPath, true)
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	if scalar[int](t, backup, "SELECT MAX(version) FROM schema_migrations") != 17 || scalar[int](t, backup, "SELECT COUNT(*) FROM sqlite_schema WHERE name='asset_disposal_corrections'") != 0 {
		t.Fatal("verified sensitive DB-only snapshot was not the actual certified pre-upgrade database")
	}
	var a, b, c int
	var plan string
	if err = s.db.QueryRowContext(t.Context(), "EXPLAIN QUERY PLAN SELECT mutation_event_id FROM asset_disposal_corrections WHERE item_unit_id=? AND mutation_event_id>? ORDER BY mutation_event_id LIMIT ?", "asset-unit", "", 51).Scan(&a, &b, &c, &plan); err != nil || !strings.Contains(plan, "idx_disposal_corrections_owner") {
		t.Fatal("actual live owner/continuation query lacks its bounded index", plan, err)
	}
	if err = s.db.QueryRowContext(t.Context(), "EXPLAIN QUERY PLAN SELECT mutation_event_id FROM asset_disposal_corrections WHERE item_unit_id=? ORDER BY subject_revision", "asset-unit").Scan(&a, &b, &c, &plan); err != nil || !strings.Contains(plan, "INDEX") || strings.Contains(plan, "TEMP") {
		t.Fatal("complete chain lookup lacks its owner/subject revision index", plan, err)
	}
}

func TestDisposalCorrectionMigrationLateLedgerFailureRollsBackDDLAndPreservesVerifiedOriginalSnapshot(t *testing.T) {
	path, old := disposalRootSeventeenFixture(t)
	execSQL(t, old.db, "CREATE TRIGGER fail_disposal_ledger BEFORE INSERT ON schema_migration_checksums WHEN NEW.version=18 BEGIN SELECT RAISE(ABORT,'disposal correction ledger failure'); END")
	before := assetProfileState(t, old)
	ddl, err := schemaDescription(t.Context(), old.db)
	if err != nil {
		t.Fatal(err)
	}
	_ = old.db.Close()
	if s, err := Open(t.Context(), Config{Path: path, Migrate: true}); err == nil {
		_ = s.Close()
		t.Fatal("failed disposal correction ledger committed new source DDL")
	} else if !strings.Contains(err.Error(), "verified recovery snapshot retained") {
		t.Fatal("failed upgrade lost its verified original recovery evidence", err)
	}
	unchanged := rawDatabase(t, path)
	requireAssetProfileState(t, &Store{db: unchanged}, before)
	actual, err := schemaDescription(t.Context(), unchanged)
	if err != nil || !reflect.DeepEqual(actual, ddl) || scalar[int](t, unchanged, "SELECT MAX(version) FROM schema_migrations") != 17 || scalar[int](t, unchanged, "SELECT COUNT(*) FROM schema_migration_checksums") != 17 {
		t.Fatal("failed upgrade leaked table/indices/triggers/checksums", err)
	}
	backups, err := filepath.Glob(filepath.Join(filepath.Dir(path), "migration-backups", "*.sqlite"))
	if err != nil || len(backups) != 1 || VerifySnapshot(t.Context(), backups[0]) != nil {
		t.Fatal("failed upgrade did not retain exactly one verified original snapshot", err)
	}
}

func TestDisposalCorrectionFreshMigrationAndNativeConstraintsRequireActualStructuredReferences(t *testing.T) {
	s := openFreshTestStore(t)
	if s.Report().From != 0 || s.Report().To < 18 || scalar[int](t, s.db, "SELECT COUNT(*) FROM asset_disposal_corrections") != 0 {
		t.Fatal("fresh initialization did not atomically apply empty disposal correction support")
	}
	fixture, service, asset, record := disposalCommands(t)
	s = fixture.store
	event := scalar[string](t, s.db, "SELECT id FROM mutation_events WHERE operation='post_items' LIMIT 1")
	type row struct {
		event, owner, previous   any
		revision, subject        int64
		action, reason, at, body string
	}
	original := row{event, fixture.unit.ID, nil, 1, 2, "void", "Exact reason", "2026-11-07T00:00:00Z", "{}"}
	before := disposalCorrectionState(t, s)
	for _, change := range []func(*row){
		func(v *row) { v.event = nil }, func(v *row) { v.event = "missing-audit" },
		func(v *row) { v.owner = nil }, func(v *row) { v.owner = "missing-original" },
		func(v *row) { v.previous = "missing-predecessor" }, func(v *row) { v.previous = v.event },
		func(v *row) { v.revision = 0 }, func(v *row) { v.revision = 2 },
		func(v *row) { v.subject = 1 }, func(v *row) { v.subject = maxSafeInteger + 1 },
		func(v *row) { v.action = "delete" }, func(v *row) { v.reason = "" },
		func(v *row) { v.reason = strings.Repeat("😀", 10001) }, func(v *row) { v.at = "2026" },
		func(v *row) { v.body = "invalid JSON" }, func(v *row) { v.body = "[]" }, func(v *row) { v.body = "null" },
	} {
		value := original
		change(&value)
		if _, err := s.db.ExecContext(t.Context(), "INSERT INTO asset_disposal_corrections VALUES(?,?,?,?,?,?,?,?,?)", value.event, value.revision, value.owner, value.previous, value.subject, value.action, value.reason, value.at, value.body); err == nil || !strings.Contains(err.Error(), "constraint") {
			t.Fatal("native disposal journal accepted invalid structure/reference or failed for the wrong reason", err)
		}
		requireDisposalCorrectionState(t, s, before)
	}
	entry, err := service.CorrectDisposal(t.Context(), fixture.token, "actual-structured-disposal-correction", fixture.unit.ID, record.Revision, disposalCorrectionInput(asset.Revision, 0))
	if err != nil {
		t.Fatal("actual atomic producer did not satisfy structured deferred audit/subject references", err)
	}
	second, err := service.CorrectDisposal(t.Context(), fixture.token, "actual-next-disposal-correction", fixture.unit.ID, entry.After.Revision, disposalCorrectionInput(asset.Revision+1, 0))
	if err != nil {
		t.Fatal(err)
	}
	before = disposalCorrectionState(t, s)
	// Verify the underlying unique indexes independently of the new insert guard.
	execSQL(t, s.db, "DROP TRIGGER asset_disposal_corrections_no_replace")
	for _, value := range []row{
		{event, fixture.unit.ID, nil, 1, 100, "void", "Duplicate root", entry.CreatedAt, "{}"},
		{event, fixture.unit.ID, entry.ID, 1, 100, "void", "Branched predecessor", entry.CreatedAt, "{}"},
		{event, fixture.unit.ID, second.ID, 1, second.After.Revision, "void", "Duplicate subject revision", entry.CreatedAt, "{}"},
	} {
		if _, err := s.db.ExecContext(t.Context(), "INSERT INTO asset_disposal_corrections VALUES(?,?,?,?,?,?,?,?,?)", value.event, value.revision, value.owner, value.previous, value.subject, value.action, value.reason, value.at, value.body); err == nil || !strings.Contains(err.Error(), "UNIQUE") {
			t.Fatal("native journal allowed another root, branch or duplicate independent subject version", err)
		}
		requireDisposalCorrectionState(t, s, before)
	}
}
