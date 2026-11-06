package store

import (
	"path/filepath"
	"strings"
	"testing"

	"homebox/migrations"
)

func purchaseRootThirteenFixture(t *testing.T) (string, *Store) {
	t.Helper()
	path, fixture := rootTwelveFixture(t)
	seedPurchaseUpgradeSource(t, fixture.db)
	all, err := migrations.Load()
	if err != nil {
		t.Fatal(err)
	}
	migration := all[12]
	execSQL(t, fixture.db, migration.SQL)
	execSQL(t, fixture.db, "INSERT INTO schema_migrations(version,name,applied_at) VALUES(?,?,'2026')", migration.Version, migration.Filename)
	execSQL(t, fixture.db, "INSERT INTO schema_migration_checksums VALUES(?,?,?)", migration.Version, migration.Filename, migration.Checksum)
	return path, fixture
}

func TestPurchaseCorrectionUpgradePreservesSourceFrozenBytesEpochAndNoInventedCorrection(t *testing.T) {
	path, old := purchaseRootThirteenFixture(t)
	setCatalogProjectionRevision(t, old, "purchase", "retained-purchase", 7)
	setCatalogProjectionRevision(t, old, "purchase_line", "retained-line", 11)
	execSQL(t, old.db, "UPDATE current_projections SET payload_json=' {\"RetainedFinancialBytes\":true} ',updated_at='exact-retained-time' WHERE entity_type IN('purchase','purchase_line')")
	execSQL(t, old.db, "INSERT INTO sync_changes(entity_type,entity_id,operation,entity_revision,projection_json,created_at) SELECT entity_type,entity_id,'upsert',entity_revision,payload_json,updated_at FROM current_projections WHERE entity_type IN('purchase','purchase_line')")
	before, projections := domainState(t, old), currentProjectionState(t, old)
	epoch := scalar[string](t, old.db, "SELECT dataset_epoch FROM sync_state")
	_ = old.db.Close()
	s, err := Open(t.Context(), Config{Path: path, Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Report().From != 13 || s.Report().To < 14 || s.Report().BackupPath == "" || scalar[string](t, s.db, "SELECT dataset_epoch FROM sync_state") != epoch {
		t.Fatal("correction upgrade changed epoch or omitted the private recovery snapshot")
	}
	if err = VerifySnapshot(t.Context(), s.Report().BackupPath); err != nil {
		t.Fatal("pre-correction snapshot could not be independently verified", err)
	}
	backup, err := openSQL(s.Report().BackupPath, true)
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	if scalar[int](t, backup, "SELECT MAX(version) FROM schema_migrations") != 13 || scalar[int](t, backup, "SELECT COUNT(*) FROM sqlite_schema WHERE name='purchase_header_corrections'") != 0 {
		t.Fatal("snapshot was not the original certified schema")
	}
	after := domainState(t, s)
	for table, value := range before {
		if table != "current_projections" && table != "projection_revisions" && after[table] != value {
			t.Fatal("correction upgrade rewrote original financial/source/audit bytes", table)
		}
	}
	current := currentProjectionState(t, s)
	for key, value := range projections {
		if current[key] != value {
			t.Fatal("correction upgrade rewrote existing current authority", key)
		}
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM purchase_header_corrections") != 0 || scalar[int](t, s.db, "SELECT COUNT(*) FROM purchase_lines WHERE mutation_event_id IS NOT NULL OR created_at IS NOT NULL") != 0 {
		t.Fatal("migration invented historical correction/allocation provenance")
	}
	if value, err := loadPurchase(t.Context(), s.db, "retained-purchase"); err != nil || value.Revision != 7 {
		t.Fatal("retained header counter was changed", err)
	}
	if value, err := loadPurchaseLine(t.Context(), s.db, "retained-line"); err != nil || value.Revision != 11 || value.PaidMinor != 0 {
		t.Fatal("retained financial allocation was changed", err)
	}
	var a, b, c int
	var plan string
	if err = s.db.QueryRowContext(t.Context(), "EXPLAIN QUERY PLAN SELECT mutation_event_id FROM purchase_header_corrections WHERE purchase_id='retained-purchase' ORDER BY mutation_event_id").Scan(&a, &b, &c, &plan); err != nil || !strings.Contains(plan, "idx_purchase_header_corrections_owner") {
		t.Fatal("correction owner history lacks its bounded index", plan, err)
	}
}

func TestPurchaseCorrectionLedgerFailureRollsBackEveryNewTableIndexTriggerAndChecksum(t *testing.T) {
	path, old := purchaseRootThirteenFixture(t)
	execSQL(t, old.db, "CREATE TRIGGER fail_purchase_correction_ledger BEFORE INSERT ON schema_migration_checksums WHEN NEW.version=14 BEGIN SELECT RAISE(ABORT,'correction ledger failure'); END")
	before := domainState(t, old)
	_ = old.db.Close()
	if s, err := Open(t.Context(), Config{Path: path, Migrate: true}); err == nil {
		_ = s.Close()
		t.Fatal("late correction checksum failure committed pending DDL")
	} else if !strings.Contains(err.Error(), "verified recovery snapshot retained") {
		t.Fatal("failed correction upgrade lost verified recovery evidence", err)
	}
	unchanged := rawDatabase(t, path)
	requireDomainState(t, &Store{db: unchanged}, before)
	if scalar[int](t, unchanged, "SELECT MAX(version) FROM schema_migrations") != 13 || scalar[int](t, unchanged, "SELECT COUNT(*) FROM schema_migration_checksums") != 13 || scalar[int](t, unchanged, "SELECT COUNT(*) FROM sqlite_schema WHERE name IN('purchase_header_corrections','idx_purchase_header_corrections_owner','purchase_header_corrections_no_update','purchase_header_corrections_no_delete')") != 0 {
		t.Fatal("failed upgrade leaked correction DDL/ledger")
	}
	backups, err := filepath.Glob(filepath.Join(filepath.Dir(path), "migration-backups", "*.sqlite"))
	if err != nil || len(backups) != 1 {
		t.Fatal("failed upgrade did not retain exactly one original private snapshot", err)
	}
	if err = VerifySnapshot(t.Context(), backups[0]); err != nil {
		t.Fatal("retained recovery snapshot failed verification", err)
	}
}

func TestPurchaseCorrectionContextRequiresCommittedAuditAndRealOwnerWithValidJSON(t *testing.T) {
	fixture, p := purchaseCommands(t)
	s := fixture.store
	parent := createPurchaseCommand(t, p, fixture.token, "correction-constraint-parent", "JPY")
	event := scalar[string](t, s.db, "SELECT id FROM mutation_events WHERE operation='post_purchases'")
	before := purchaseCommandState(t, s)
	for _, statement := range []string{
		"INSERT INTO purchase_header_corrections VALUES('missing-audit',?,'{}')",
		"INSERT INTO purchase_header_corrections VALUES(NULL,?,'{}')",
		"INSERT INTO purchase_header_corrections SELECT id,'missing-purchase','{}' FROM mutation_events WHERE operation='post_purchases' AND ? IS NOT NULL",
		"INSERT INTO purchase_header_corrections SELECT id,?,'not JSON' FROM mutation_events WHERE operation='post_purchases'",
	} {
		if _, err := s.db.ExecContext(t.Context(), statement, parent.ID); err == nil {
			t.Fatal("correction context bypassed identity/owner/JSON/deferred audit authority")
		}
		requirePurchaseCommandState(t, s, before)
	}
	if _, err := p.Patch(t.Context(), fixture.token, "correction-constraint-real", parent.ID, parent.Revision, PurchasePatch{ReceiptNote: testPointer("Exact correction")}); err != nil {
		t.Fatal("deferred audit reference rejected a genuine atomic correction", err)
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM purchase_header_corrections c JOIN mutation_events e ON e.id=c.mutation_event_id WHERE c.purchase_id=? AND e.operation='patch_purchases_id' AND c.mutation_event_id<>?", parent.ID, event) != 1 {
		t.Fatal("context did not bind its own real correction event")
	}
}
