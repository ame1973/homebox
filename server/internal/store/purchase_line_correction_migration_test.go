package store

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"homebox/migrations"
)

func purchaseRootSixteenFixture(t *testing.T) (string, *Store) {
	t.Helper()
	path, fixture := assetRootFifteenFixture(t)
	all, err := migrations.Load()
	if err != nil {
		t.Fatal(err)
	}
	migration := all[15]
	execSQL(t, fixture.db, migration.SQL)
	execSQL(t, fixture.db, "INSERT INTO schema_migrations(version,name,applied_at) VALUES(?,?,'2026')", migration.Version, migration.Filename)
	execSQL(t, fixture.db, "INSERT INTO schema_migration_checksums VALUES(?,?,?)", migration.Version, migration.Filename, migration.Checksum)
	return path, fixture
}

func TestPurchaseLineCorrectionMigrationAddsNoFabricatedHistoryAndPreservesEverySourceFrozenByteAndEpoch(t *testing.T) {
	path, old := purchaseRootSixteenFixture(t)
	execSQL(t, old.db, "UPDATE current_projections SET payload_json=' {\"ExactRetainedFinancialBytes\":true} ',updated_at='unknown-retained-time' WHERE entity_type IN('purchase','purchase_line','asset','usage','cost','disposal')")
	execSQL(t, old.db, "INSERT INTO sync_changes(entity_type,entity_id,operation,entity_revision,projection_json,created_at) SELECT entity_type,entity_id,'upsert',entity_revision,payload_json,updated_at FROM current_projections WHERE entity_type IN('purchase','purchase_line','asset','usage','cost','disposal')")
	before, projected := domainState(t, old), currentProjectionState(t, old)
	epoch := scalar[string](t, old.db, "SELECT dataset_epoch FROM sync_state")
	_ = old.db.Close()
	s, err := Open(t.Context(), Config{Path: path, Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Report().From != 16 || s.Report().To < 17 || s.Report().BackupPath == "" || scalar[string](t, s.db, "SELECT dataset_epoch FROM sync_state") != epoch {
		t.Fatal("allocation correction upgrade omitted actual recovery or rotated existing authority")
	}
	requireDomainState(t, s, before)
	if !reflect.DeepEqual(projected, currentProjectionState(t, s)) || scalar[int](t, s.db, "SELECT COUNT(*) FROM purchase_line_corrections") != 0 ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM purchase_header_corrections") != 0 || scalar[int](t, s.db, "SELECT COUNT(*) FROM asset_profile_changes") != 0 {
		t.Fatal("migration rewrote existing/frozen projections or fabricated corrections")
	}
	if err = VerifySnapshot(t.Context(), s.Report().BackupPath); err != nil {
		t.Fatal(err)
	}
	backup, err := openSQL(s.Report().BackupPath, true)
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	if scalar[int](t, backup, "SELECT MAX(version) FROM schema_migrations") != 16 || scalar[int](t, backup, "SELECT COUNT(*) FROM sqlite_schema WHERE name='purchase_line_corrections'") != 0 {
		t.Fatal("verified snapshot was not the actual certified pre-correction database")
	}
	var a, b, c int
	var plan string
	if err = s.db.QueryRowContext(t.Context(), "EXPLAIN QUERY PLAN SELECT mutation_event_id FROM purchase_line_corrections WHERE purchase_id=? AND mutation_event_id>? ORDER BY mutation_event_id LIMIT ?", "retained-purchase", "", 51).Scan(&a, &b, &c, &plan); err != nil || !strings.Contains(plan, "idx_purchase_line_corrections_owner") {
		t.Fatal("actual live correction query lacks its bounded owner/continuation index", plan, err)
	}
}

func TestPurchaseLineCorrectionMigrationLedgerFailureRollsBackAllDDLAndRetainsVerifiedOriginalSnapshot(t *testing.T) {
	path, old := purchaseRootSixteenFixture(t)
	execSQL(t, old.db, "CREATE TRIGGER fail_line_correction_ledger BEFORE INSERT ON schema_migration_checksums WHEN NEW.version=17 BEGIN SELECT RAISE(ABORT,'allocation correction ledger failure'); END")
	before := domainState(t, old)
	ddl, err := schemaDescription(t.Context(), old.db)
	if err != nil {
		t.Fatal(err)
	}
	_ = old.db.Close()
	if s, err := Open(t.Context(), Config{Path: path, Migrate: true}); err == nil {
		_ = s.Close()
		t.Fatal("failed allocation correction ledger committed source DDL")
	} else if !strings.Contains(err.Error(), "verified recovery snapshot retained") {
		t.Fatal("failed correction upgrade lost its verified original recovery evidence", err)
	}
	unchanged := rawDatabase(t, path)
	requireDomainState(t, &Store{db: unchanged}, before)
	actual, err := schemaDescription(t.Context(), unchanged)
	if err != nil || !reflect.DeepEqual(actual, ddl) || scalar[int](t, unchanged, "SELECT MAX(version) FROM schema_migrations") != 16 || scalar[int](t, unchanged, "SELECT COUNT(*) FROM schema_migration_checksums") != 16 {
		t.Fatal("failed correction upgrade leaked table/index/triggers/checksums", err)
	}
	backups, err := filepath.Glob(filepath.Join(filepath.Dir(path), "migration-backups", "*.sqlite"))
	if err != nil || len(backups) != 1 || VerifySnapshot(t.Context(), backups[0]) != nil {
		t.Fatal("failed correction upgrade did not retain exactly one verified original snapshot", err)
	}
}

func TestPurchaseLineCorrectionFreshMigrationAndNativeConstraintsRequireActualStructuredReferences(t *testing.T) {
	s := openFreshTestStore(t)
	if s.Report().From != 0 || s.Report().To < 17 || scalar[int](t, s.db, "SELECT COUNT(*) FROM purchase_line_corrections") != 0 {
		t.Fatal("fresh initialization did not atomically apply empty correction support")
	}
	fixture, purchases := purchaseCommands(t)
	s = fixture.store
	event := scalar[string](t, s.db, "SELECT id FROM mutation_events WHERE operation='post_items' LIMIT 1")
	type row struct {
		event                    any
		revision                 int64
		purchase, line           string
		replacement              any
		action, reason, at, body string
	}
	original := row{event, 1, fixture.purchase.ID, fixture.unitLine.ID, nil, "void", "Exact reason", "2026-11-07T00:00:00Z", "{}"}
	before := assetProfileState(t, s)
	for _, change := range []func(*row){
		func(v *row) { v.event = nil }, func(v *row) { v.event = "missing-audit" },
		func(v *row) { v.revision = 0 }, func(v *row) { v.revision = 2 },
		func(v *row) { v.purchase = "missing-purchase" }, func(v *row) { v.line = "missing-line" },
		func(v *row) { v.action = "delete" }, func(v *row) { v.action = "replace" },
		func(v *row) { v.replacement = fixture.stockLine.ID },
		func(v *row) { v.action = "replace"; v.replacement = fixture.unitLine.ID },
		func(v *row) { v.action = "replace"; v.replacement = "missing-line" },
		func(v *row) { v.reason = "" }, func(v *row) { v.reason = strings.Repeat("😀", 10001) },
		func(v *row) { v.at = "2026" }, func(v *row) { v.body = "invalid JSON" },
		func(v *row) { v.body = "[]" }, func(v *row) { v.body = "null" },
	} {
		value := original
		change(&value)
		if _, err := s.db.ExecContext(t.Context(), "INSERT INTO purchase_line_corrections VALUES(?,?,?,?,?,?,?,?,?)", value.event, value.revision, value.purchase, value.line, value.replacement, value.action, value.reason, value.at, value.body); err == nil || !strings.Contains(err.Error(), "constraint") {
			t.Fatal("native correction storage accepted invalid structure/reference or failed for the wrong reason", err)
		}
		requireAssetProfileState(t, s, before)
	}
	input := reviewedLineVoid(t, fixture, fixture.unitLine)
	input.Action = "replace"
	input.Replacement = &PurchaseLineCreate{ItemID: fixture.quantity.ID, ItemRevision: fixture.quantity.Revision, QuantityBase: 1, PaidMinor: 0}
	entry, err := purchases.CorrectLine(t.Context(), fixture.token, "real-structured-correction", fixture.purchase.ID, fixture.purchase.Revision, input)
	if err != nil {
		t.Fatal("actual atomic producer did not satisfy deferred audit/subject constraints", err)
	}
	before = assetProfileState(t, s)
	for _, pair := range [][2]string{{fixture.unitLine.ID, ""}, {fixture.stockLine.ID, entry.After.ID}} {
		action := "void"
		var replacement any
		if pair[1] != "" {
			action, replacement = "replace", pair[1]
		}
		if _, err := s.db.ExecContext(t.Context(), "INSERT INTO purchase_line_corrections VALUES(?,1,?,?,?,?,?,?,?)", event, fixture.purchase.ID, pair[0], replacement, action, "Duplicate source association", entry.CreatedAt, "{}"); err == nil || !strings.Contains(err.Error(), "UNIQUE") {
			t.Fatal("original/replacement source was reused across independent correction associations", err)
		}
		requireAssetProfileState(t, s, before)
	}
}
