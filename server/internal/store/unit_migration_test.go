package store

import (
	"strings"
	"testing"

	"homebox/migrations"
)

func rootElevenFixture(t *testing.T) (string, *Store) {
	t.Helper()
	path, fixture := rootTenFixture(t)
	all, err := migrations.Load()
	if err != nil {
		t.Fatal(err)
	}
	migration := all[10]
	execSQL(t, fixture.db, migration.SQL)
	execSQL(t, fixture.db, "INSERT INTO schema_migrations(version,name,applied_at) VALUES(?,?,'2026')", migration.Version, migration.Filename)
	execSQL(t, fixture.db, "INSERT INTO schema_migration_checksums VALUES(?,?,?)", migration.Version, migration.Filename, migration.Checksum)
	return path, fixture
}

const unitHistoryBytesTest = `SELECT json_group_array(json_array(id,item_id,item_unit_id,from_location_id,to_location_id,action,quantity_base,occurred_at,idempotency_key,note,reason,mutation_event_id)) FROM (SELECT * FROM inventory_events ORDER BY id)`

func seedUnitHistoryUpgradeTest(t *testing.T, fixture *Store) {
	t.Helper()
	execSQL(t, fixture.db, "INSERT INTO items(id,name,tracking_mode,created_at,updated_at) VALUES('individual','Retained Unit owner','individual','2026','2026')")
	execSQL(t, fixture.db, "INSERT INTO projection_revisions VALUES('item','individual',1)")
	execSQL(t, fixture.db, "INSERT INTO item_units(id,item_id,location_id,asset_code,status,serial_number,notes,created_at,updated_at) VALUES('retained-unit','individual','loc-1',?,'stored',NULL,?,'2026','2026')", "\x00Exact asset é", "\x00Exact Unit notes é\n")
	seedCatalogProjection(t, fixture, "unit", "retained-unit", UnitData{ID: "retained-unit", ItemID: "individual", LocationID: testPointer("loc-1"), AssetCode: "\x00Exact asset é", Status: "stored", Notes: "\x00Exact Unit notes é\n"})
	execSQL(t, fixture.db, `INSERT INTO inventory_events(id,item_id,item_unit_id,to_location_id,action,quantity_base,occurred_at,idempotency_key,note,reason)
 VALUES('retained-movement','individual','retained-unit','loc-1','unit_create',1,'2026-11-04T12:00:00Z','legacy-raw-key',?,?)`, "\x00Exact producer note é\n", "\x00Exact producer reason é\n")
	seedCatalogProjection(t, fixture, "inventory_event", "retained-movement", InventoryEventData{ID: "retained-movement", ItemID: "individual", ItemUnitID: testPointer("retained-unit"), ToLocationID: testPointer("loc-1"), Action: "unit_create", QuantityBase: 1, OccurredAt: "2026-11-04T12:00:00Z"})
	setCatalogProjectionRevision(t, fixture, "inventory_event", "retained-movement", 7)
	execSQL(t, fixture.db, "UPDATE current_projections SET updated_at='retained-time' WHERE entity_type='inventory_event'")
	execSQL(t, fixture.db, "INSERT INTO sync_changes(entity_type,entity_id,operation,entity_revision,projection_json,created_at) SELECT entity_type,entity_id,'upsert',entity_revision,payload_json,'retained-time' FROM current_projections WHERE entity_type IN('unit','inventory_event')")
	execSQL(t, fixture.db, "INSERT INTO inventory_events(id,item_id,item_unit_id,action,quantity_base,occurred_at,note) VALUES('unpublished-legacy','individual','retained-unit','legacy_adjust',0,'2026',?)", "\x00Unpublished é\n")
}

func TestUnitEventIdentityUpgradePreservesEverySourceCounterCurrentAndFrozenByte(t *testing.T) {
	path, old := rootElevenFixture(t)
	seedUnitHistoryUpgradeTest(t, old)
	before := domainState(t, old)
	bytes := scalar[string](t, old.db, unitHistoryBytesTest)
	epoch := scalar[string](t, old.db, "SELECT dataset_epoch FROM sync_state")
	_ = old.db.Close()
	s, err := Open(t.Context(), Config{Path: path, Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Report().From != 11 || s.Report().To < 12 || s.Report().BackupPath == "" || scalar[string](t, s.db, unitHistoryBytesTest) != bytes || scalar[string](t, s.db, "SELECT dataset_epoch FROM sync_state") != epoch {
		t.Fatal("history identity migration rewrote source or skipped private snapshot")
	}
	requireDomainState(t, s, before)
	if scalar[int](t, s.db, `SELECT COUNT(*) FROM pragma_foreign_key_list('inventory_events') WHERE "table"='item_units'`) != 0 || scalar[int](t, s.db, `SELECT COUNT(*) FROM pragma_foreign_key_list('inventory_events') WHERE "table" IN('items','locations','mutation_events')`) != 4 {
		t.Fatal("migration kept the live Unit FK or removed unrelated authoritative references")
	}
	for _, table := range []string{"asset_profiles", "purchase_lines", "media_links"} {
		if scalar[int](t, s.db, `SELECT COUNT(*) FROM pragma_foreign_key_list('`+table+`') WHERE "table"='item_units'`) == 0 {
			t.Fatal("history migration weakened a financial/ownership FK", table)
		}
	}
	for _, statement := range []string{"UPDATE inventory_events SET note='changed'", "DELETE FROM inventory_events"} {
		if _, err := s.db.ExecContext(t.Context(), statement); err == nil {
			t.Fatal("rebuilt movement history lost immutable triggers")
		}
	}
	if _, err := s.db.ExecContext(t.Context(), "INSERT INTO inventory_events(id,item_id,action,quantity_base,occurred_at,idempotency_key) VALUES('duplicate','individual','legacy',0,'2026','legacy-raw-key')"); err == nil {
		t.Fatal("migration dropped retained raw-key uniqueness")
	}
	// Schema probe only: real service deletion/tombstones are tested separately.
	// A stable historical subject may outlive live source without FK laundering.
	execSQL(t, s.db, "DELETE FROM current_projections WHERE entity_type='unit' AND entity_id='retained-unit'")
	execSQL(t, s.db, "DELETE FROM item_units WHERE id='retained-unit'")
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM inventory_events WHERE item_unit_id='retained-unit'") != 2 || scalar[string](t, s.db, unitHistoryBytesTest) != bytes {
		t.Fatal("deletion nullified or erased immutable historical Unit identity")
	}
	if _, err = s.Check(t.Context()); err != nil {
		t.Fatal("stable historical Unit IDs were incorrectly treated as live source or invalid FK state", err)
	}
}

func TestUnitEventIdentityMigrationFailureRollsBackTableRebuildTriggersIndicesAndLedger(t *testing.T) {
	path, old := rootElevenFixture(t)
	seedUnitHistoryUpgradeTest(t, old)
	execSQL(t, old.db, "CREATE TRIGGER fail_unit_identity_ledger BEFORE INSERT ON schema_migration_checksums WHEN NEW.version=12 BEGIN SELECT RAISE(ABORT,'Unit history migration failure'); END")
	before := domainState(t, old)
	ddl := scalar[string](t, old.db, "SELECT sql FROM sqlite_schema WHERE type='table' AND name='inventory_events'")
	_ = old.db.Close()
	if s, err := Open(t.Context(), Config{Path: path, Migrate: true}); err == nil {
		_ = s.Close()
		t.Fatal("failed Unit-history migration committed a partial rebuild")
	} else if !strings.Contains(err.Error(), "verified recovery snapshot retained") {
		t.Fatal("failed migration lost its private recovery reference", err)
	}
	unchanged := rawDatabase(t, path)
	requireDomainState(t, &Store{db: unchanged}, before)
	if scalar[string](t, unchanged, "SELECT sql FROM sqlite_schema WHERE type='table' AND name='inventory_events'") != ddl || scalar[int](t, unchanged, "SELECT MAX(version) FROM schema_migrations") != 11 || scalar[int](t, unchanged, "SELECT COUNT(*) FROM schema_migration_checksums") != 11 || scalar[int](t, unchanged, "SELECT COUNT(*) FROM sqlite_schema WHERE name='inventory_events_v12'") != 0 {
		t.Fatal("failed rebuild changed table definitions or published a checksum/version")
	}
	if scalar[int](t, unchanged, `SELECT COUNT(*) FROM pragma_foreign_key_list('inventory_events') WHERE "table"='item_units'`) != 1 || scalar[int](t, unchanged, "SELECT COUNT(*) FROM sqlite_schema WHERE name IN('idx_inventory_events_item','idx_inventory_events_timeline','idx_inventory_events_mutation','inventory_events_no_update','inventory_events_no_delete')") != 5 {
		t.Fatal("failed migration lost old FK/index/immutability guarantees")
	}
}

func TestUnitEventIdentityUpgradeRejectsPreexistingInvalidLiveUnitReferencesBeforeDroppingTheFK(t *testing.T) {
	path, old := rootElevenFixture(t)
	execSQL(t, old.db, "PRAGMA foreign_keys=OFF")
	execSQL(t, old.db, "INSERT INTO inventory_events(id,item_id,item_unit_id,action,quantity_base,occurred_at) VALUES('bad-unit-reference','item-1','missing-unit','legacy',0,'2026')")
	execSQL(t, old.db, "PRAGMA foreign_keys=ON")
	_ = old.db.Close()
	if s, err := Open(t.Context(), Config{Path: path, Migrate: true}); err == nil {
		_ = s.Close()
		t.Fatal("dropping a live FK laundered preexisting invalid reference state")
	}
	unchanged := rawDatabase(t, path)
	if scalar[int](t, unchanged, "SELECT MAX(version) FROM schema_migrations") != 11 || scalar[int](t, unchanged, `SELECT COUNT(*) FROM pragma_foreign_key_list('inventory_events') WHERE "table"='item_units'`) != 1 || scalar[int](t, unchanged, "SELECT COUNT(*) FROM inventory_events WHERE id='bad-unit-reference' AND item_unit_id='missing-unit'") != 1 {
		t.Fatal("failed preflight repaired/dropped invalid legacy source or published schema")
	}
}
