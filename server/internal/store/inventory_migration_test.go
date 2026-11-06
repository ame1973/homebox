package store

import (
	"reflect"
	"strings"
	"testing"

	"homebox/migrations"
)

func rootTenFixture(t *testing.T) (string, *Store) {
	t.Helper()
	path, fixture := rootNineFixture(t)
	all, err := migrations.Load()
	if err != nil {
		t.Fatal(err)
	}
	migration := all[9]
	execSQL(t, fixture.db, migration.SQL)
	execSQL(t, fixture.db, "INSERT INTO schema_migrations(version,name,applied_at) VALUES(?,?,'2026')", migration.Version, migration.Filename)
	execSQL(t, fixture.db, "INSERT INTO schema_migration_checksums VALUES(?,?,?)", migration.Version, migration.Filename, migration.Checksum)
	return path, fixture
}

const inventoryLegacyBytesTest = `SELECT json_group_array(json_array(id,item_id,item_unit_id,from_location_id,to_location_id,action,quantity_base,occurred_at,idempotency_key)) FROM (SELECT * FROM inventory_events ORDER BY id)`

func TestInventoryHistoryUpgradePreservesLegacyBytesPublishedHistoryEpochAndMissingAuthority(t *testing.T) {
	path, old := rootTenFixture(t)
	published := InventoryEventData{ID: "old-published", ItemID: "item-1", FromLocationID: testPointer("loc-1"), Action: "consume", QuantityBase: -2, OccurredAt: "2026-11-04T12:00:00Z"}
	execSQL(t, old.db, "INSERT INTO inventory_events(id,item_id,from_location_id,action,quantity_base,occurred_at,idempotency_key) VALUES(?,?,?,?,?,?,?)", published.ID, published.ItemID, published.FromLocationID, published.Action, published.QuantityBase, published.OccurredAt, "legacy-raw-claim")
	seedCatalogProjection(t, old, "inventory_event", published.ID, published)
	setCatalogProjectionRevision(t, old, "inventory_event", published.ID, 7)
	execSQL(t, old.db, "INSERT INTO inventory_events(id,item_id,action,quantity_base,occurred_at) VALUES('unpublished-legacy','item-1',?,0,'2026')", "\x00Exact legacy é\n")
	execSQL(t, old.db, "INSERT INTO projection_revisions VALUES('inventory_event','retained-counter-only',9)")
	execSQL(t, old.db, "INSERT INTO sync_changes(entity_type,entity_id,operation,entity_revision,projection_json,created_at) SELECT entity_type,entity_id,'upsert',entity_revision,payload_json,'retained-time' FROM current_projections WHERE entity_type='inventory_event'")
	execSQL(t, old.db, "INSERT INTO admin_accounts(id,username,password_hash,created_at,updated_at) VALUES('old-admin','Existing owner','retained-test-hash','2026','2026')")
	execSQL(t, old.db, "INSERT INTO api_idempotency VALUES('retained-domain-claim','test_retained',?,200,?,'2026','2027')", strings.Repeat("a", 64), ` {"ok":true,"operation_id":"retained-audit","revision":1} `)
	execSQL(t, old.db, "INSERT INTO mutation_events(id,admin_id,dataset_epoch,operation,idempotency_key,occurred_at,reason,result_json) SELECT 'retained-audit','old-admin',dataset_epoch,'test_retained','retained-domain-claim','2026',?,? FROM sync_state", "\x00Retained reason é\n", ` {"ok":true,"operation_id":"retained-audit","revision":1} `)
	legacyBytes := scalar[string](t, old.db, inventoryLegacyBytesTest)
	auditBytes := scalar[string](t, old.db, "SELECT json_array(id,admin_id,dataset_epoch,operation,idempotency_key,occurred_at,reason,result_json) FROM mutation_events")
	before := domainState(t, old)
	projections := currentProjectionState(t, old)
	epoch := scalar[string](t, old.db, "SELECT dataset_epoch FROM sync_state")
	_ = old.db.Close()
	s, err := Open(t.Context(), Config{Path: path, Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Report().From != 10 || s.Report().To < 11 || s.Report().BackupPath == "" || scalar[string](t, s.db, "SELECT dataset_epoch FROM sync_state") != epoch {
		t.Fatal("event history upgrade skipped snapshot or changed epoch")
	}
	if scalar[string](t, s.db, inventoryLegacyBytesTest) != legacyBytes || scalar[string](t, s.db, "SELECT json_array(id,admin_id,dataset_epoch,operation,idempotency_key,occurred_at,reason,result_json) FROM mutation_events") != auditBytes {
		t.Fatal("additive event columns rewrote original source/audit values")
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM inventory_events WHERE note='' AND reason='' AND mutation_event_id IS NULL") != 2 || scalar[string](t, s.db, "SELECT note FROM mutation_events") != "" {
		t.Fatal("legacy context received invented notes or audit associations")
	}
	after := domainState(t, s)
	for _, table := range []string{"items", "stock_balances", "item_units", "locations", "projection_revisions", "current_projections", "sync_changes", "api_idempotency", "data_revision", "sqlite_sequence"} {
		if before[table] != after[table] {
			t.Fatal("history migration fabricated authority, events, receipts or projections", table)
		}
	}
	if !reflect.DeepEqual(projections, currentProjectionState(t, s)) || scalar[int](t, s.db, "SELECT COUNT(*) FROM current_projections WHERE entity_type='inventory_event' AND entity_id IN('unpublished-legacy','retained-counter-only')") != 0 {
		t.Fatal("legacy/absent source was silently published or resurrected")
	}
	if got, err := loadInventoryEvent(t.Context(), s.db, published.ID); err != nil || !reflect.DeepEqual(got, published) {
		t.Fatal("retained published event became unreadable", err)
	}
	if _, err := s.db.ExecContext(t.Context(), "UPDATE inventory_events SET occurred_at='guessed' WHERE id='unpublished-legacy'"); err == nil {
		t.Fatal("legacy movement source remained mutable")
	}
	if _, err := s.db.ExecContext(t.Context(), "DELETE FROM inventory_events WHERE id='unpublished-legacy'"); err == nil {
		t.Fatal("legacy movement source could be erased")
	}
	for _, plan := range []struct{ query, index string }{
		{"SELECT id FROM inventory_events WHERE occurred_at>'2026' ORDER BY occurred_at,id LIMIT 50", "idx_inventory_events_timeline"},
		{"SELECT id FROM inventory_events WHERE mutation_event_id='retained-audit' ORDER BY id", "idx_inventory_events_mutation"},
	} {
		var a, b, c int
		var detail string
		if err := s.db.QueryRowContext(t.Context(), "EXPLAIN QUERY PLAN "+plan.query).Scan(&a, &b, &c, &detail); err != nil || !strings.Contains(detail, plan.index) {
			t.Fatal("event timeline/association lacks its bounded lookup index", detail, err)
		}
	}
}

func TestInventoryHistoryMigrationFailureRollsBackEveryPendingColumnIndexTriggerAndLedgerRow(t *testing.T) {
	path, old := rootNineFixture(t)
	execSQL(t, old.db, "INSERT INTO inventory_events(id,item_id,action,quantity_base,occurred_at) VALUES('retained','item-1','adjust',0,'2026')")
	execSQL(t, old.db, "CREATE TRIGGER fail_inventory_ledger BEFORE INSERT ON schema_migration_checksums WHEN NEW.version=11 BEGIN SELECT RAISE(ABORT,'event migration ledger failure'); END")
	bytes := scalar[string](t, old.db, inventoryLegacyBytesTest)
	epoch := scalar[string](t, old.db, "SELECT dataset_epoch FROM sync_state")
	_ = old.db.Close()
	if s, err := Open(t.Context(), Config{Path: path, Migrate: true}); err == nil {
		_ = s.Close()
		t.Fatal("failed event checksum publication escaped rollback")
	} else if !strings.Contains(err.Error(), "verified recovery snapshot retained") {
		t.Fatal("failed upgrade lost its verified recovery reference", err)
	}
	unchanged := rawDatabase(t, path)
	if scalar[int](t, unchanged, "SELECT MAX(version) FROM schema_migrations") != 9 || scalar[int](t, unchanged, "SELECT COUNT(*) FROM schema_migration_checksums") != 9 || scalar[string](t, unchanged, inventoryLegacyBytesTest) != bytes || scalar[string](t, unchanged, "SELECT dataset_epoch FROM sync_state") != epoch {
		t.Fatal("failed event migration changed source/epoch/ledger")
	}
	if scalar[int](t, unchanged, "SELECT COUNT(*) FROM pragma_table_info('inventory_events') WHERE name IN('note','reason','mutation_event_id')") != 0 || scalar[int](t, unchanged, "SELECT COUNT(*) FROM pragma_table_info('mutation_events') WHERE name='note'") != 0 || scalar[int](t, unchanged, "SELECT COUNT(*) FROM sqlite_schema WHERE name IN('idx_units_item_id','idx_inventory_events_timeline','idx_inventory_events_mutation','inventory_events_no_update','inventory_events_no_delete')") != 0 {
		t.Fatal("failed migration leaked pending holding/event DDL")
	}
}
