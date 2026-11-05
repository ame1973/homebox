package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"homebox/migrations"
)

func rootNineFixture(t *testing.T) (string, *Store) {
	t.Helper()
	path, fixture := rootEightFixture(t)
	all, err := migrations.Load()
	if err != nil {
		t.Fatal(err)
	}
	migration := all[8]
	execSQL(t, fixture.db, migration.SQL)
	execSQL(t, fixture.db, "INSERT INTO schema_migrations(version,name,applied_at) VALUES(?,?,'2026')", migration.Version, migration.Filename)
	execSQL(t, fixture.db, "INSERT INTO schema_migration_checksums VALUES(?,?,?)", migration.Version, migration.Filename, migration.Checksum)
	return path, fixture
}

func TestHoldingBackfillPreservesExactNullableSourceCountsCountersAndLookupPaths(t *testing.T) {
	path, old := legacyFixture(t, 2)
	unitID := strings.Repeat("界", 124) + "<&\" "
	itemID, locationID := strings.Repeat("😀", 128), strings.Repeat("<", 128)
	execSQL(t, old, "INSERT INTO items(id,name,tracking_mode,created_at,updated_at) VALUES('individual','Physical','individual','2026','2026'),(?,'Quantity','quantity','2026','2026')", itemID)
	execSQL(t, old, "INSERT INTO locations(id,code,name,created_at,updated_at) VALUES(?,'BOUND','Bound','2026','2026')", locationID)
	execSQL(t, old, "INSERT INTO item_units(id,item_id,asset_code,status,serial_number,notes,created_at,updated_at) VALUES(?,'individual',?,'lost',NULL,?,'2026','2026')", unitID, "\x00AST é", "\x00Exact\nNotes")
	execSQL(t, old, "INSERT INTO item_units(id,item_id,location_id,asset_code,status,serial_number,notes,created_at,updated_at) VALUES('empty-serial','individual','loc-1','AST-002','disposed','','','2026','2026')")
	execSQL(t, old, "INSERT INTO stock_balances VALUES(?,?,0,'2026')", itemID, locationID)
	for _, counter := range []struct {
		kind, id string
		revision int64
	}{
		{"unit", unitID, maxSafeInteger}, {"stock", projectionPair("item-1", "loc-1"), 7},
		{"unit", "gone-unit", 9}, {"stock", projectionPair("gone-item", "gone-location"), 11},
	} {
		execSQL(t, old, "INSERT INTO projection_revisions VALUES(?,?,?)", counter.kind, counter.id, counter.revision)
	}
	execSQL(t, old, "INSERT INTO search_documents(item_id,title,reviewed_ai,revision,updated_at) VALUES('item-1','PrivateItemCache','NeverPublishUnreviewedItemCache',8,'2026')")
	execSQL(t, old, "INSERT INTO unit_search_documents(unit_id,title,reviewed_ai,revision,updated_at) VALUES(?,'PrivateUnitCache','NeverPublishUnreviewedUnitCache',987,'2026')", unitID)
	_ = old.Close()
	s, err := Open(t.Context(), Config{Path: path, Migrate: true, AllowLegacy: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	unit, err := loadUnit(t.Context(), s.db, unitID)
	if err != nil || unit.ID != unitID || unit.AssetCode != "\x00AST é" || unit.Notes != "\x00Exact\nNotes" || unit.LocationID != nil || unit.SerialNumber != nil || unit.Revision != maxSafeInteger {
		t.Fatal("backfill changed exact retained unit identity/text/nulls/counter", err)
	}
	empty, err := loadUnit(t.Context(), s.db, "empty-serial")
	if err != nil || empty.SerialNumber == nil || *empty.SerialNumber != "" || empty.LocationID == nil || *empty.LocationID != "loc-1" || empty.Status != "disposed" {
		t.Fatal("supplied-empty serial or retained lifecycle/location was lost", err)
	}
	stock, err := loadStock(t.Context(), s.db, "item-1", "loc-1")
	if err != nil || stock.Revision != 7 || stock.QuantityBase != 1500 || stock.UnitCode != "pcs" || stock.BaseUnit != "pcs" || stock.ScaleFactor != 1 {
		t.Fatal("backfill reset a balance or guessed a measurement conversion", err)
	}
	zero, err := loadStock(t.Context(), s.db, itemID, locationID)
	if err != nil || zero.QuantityBase != 0 || zero.Revision != 1 {
		t.Fatal("zero or full-width composite source was hidden", err)
	}
	for _, value := range []struct {
		kind, id string
		payload  any
	}{
		{"unit", unitID, unit.UnitData}, {"unit", empty.ID, empty.UnitData},
		{"stock", projectionPair(stock.ItemID, stock.LocationID), stock.StockData}, {"stock", projectionPair(itemID, locationID), zero.StockData},
	} {
		encoded, err := json.Marshal(value.payload)
		var expected, actual any
		if err != nil || json.Unmarshal(encoded, &expected) != nil || json.Unmarshal([]byte(scalar[string](t, s.db, "SELECT payload_json FROM current_projections WHERE entity_type=? AND entity_id=?", value.kind, value.id)), &actual) != nil || !reflect.DeepEqual(expected, actual) {
			t.Fatal("partial backfill changed the version-one payload", value.kind, err)
		}
		if _, present := actual.(map[string]any)["revision"]; present {
			t.Fatal("resource revision leaked inside offline payload")
		}
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM current_projections WHERE entity_type IN('unit','stock')") != 4 ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM current_projections WHERE entity_type='unit' AND entity_id='gone-unit'") != 0 ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM current_projections WHERE entity_type='stock' AND entity_id=?", projectionPair("gone-item", "gone-location")) != 0 ||
		scalar[int](t, s.db, "SELECT revision FROM projection_revisions WHERE entity_type='unit' AND entity_id='gone-unit'") != 9 ||
		scalar[int](t, s.db, "SELECT revision FROM projection_revisions WHERE entity_type='stock' AND entity_id=?", projectionPair("gone-item", "gone-location")) != 11 {
		t.Fatal("partial backfill resurrected counter-only identities or reset history")
	}
	for _, table := range []string{"sync_changes", "mutation_events", "inventory_events"} {
		if scalar[int](t, s.db, "SELECT COUNT(*) FROM "+table) != 0 {
			t.Fatal("backfill invented a movement/receipt/change", table)
		}
	}
	if scalar[int](t, s.db, "SELECT SUM(revision) FROM data_revision") != 0 || scalar[int](t, s.db, "SELECT COUNT(*) FROM current_projections WHERE entity_type IN('item','search_doc')") != 0 ||
		scalar[string](t, s.db, "SELECT reviewed_ai FROM search_documents") != "NeverPublishUnreviewedItemCache" || scalar[string](t, s.db, "SELECT reviewed_ai FROM unit_search_documents") != "NeverPublishUnreviewedUnitCache" ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM projection_revisions WHERE entity_type='search_doc' AND entity_id=?", projectionPair("unit", unitID)) != 0 ||
		scalar[int](t, s.db, "SELECT quantity_base FROM stock_balances WHERE item_id='item-1'") != 1500 {
		t.Fatal("backfill published a cache, changed unrelated counters or rescaled physical source")
	}
	var order, parent, unused int
	var detail string
	if err = s.db.QueryRowContext(t.Context(), "EXPLAIN QUERY PLAN SELECT id FROM item_units WHERE item_id='individual' AND id>'a' ORDER BY id LIMIT 51").Scan(&order, &parent, &unused, &detail); err != nil || !strings.Contains(detail, "idx_units_item_id") {
		t.Fatal("unit stable-ID page lacks its bounded lookup path", detail, err)
	}
	if err = s.db.QueryRowContext(t.Context(), "EXPLAIN QUERY PLAN SELECT quantity_base FROM stock_balances WHERE item_id='item-1' AND location_id='loc-1'").Scan(&order, &parent, &unused, &detail); err != nil || !strings.Contains(detail, "PRIMARY KEY") {
		t.Fatal("stock source lookup lost its real pair authority", detail, err)
	}
}

func TestHoldingCertifiedRootUpgradePreservesExistingPayloadBytesEpochAndFrozenChanges(t *testing.T) {
	path, fixture := rootNineFixture(t)
	execSQL(t, fixture.db, "INSERT INTO items(id,name,tracking_mode,created_at,updated_at) VALUES('individual','Physical','individual','2026','2026')")
	execSQL(t, fixture.db, "INSERT INTO projection_revisions VALUES('item','individual',1)")
	unit := UnitData{ID: "retained-unit", ItemID: "individual", AssetCode: "  AST é  ", Status: "retired", SerialNumber: testPointer(""), Notes: "Notes"}
	execSQL(t, fixture.db, "INSERT INTO item_units(id,item_id,asset_code,status,serial_number,notes,created_at,updated_at) VALUES(?,?,?,?,?,?,'2026','2026')", unit.ID, unit.ItemID, unit.AssetCode, unit.Status, unit.SerialNumber, unit.Notes)
	seedCatalogProjection(t, fixture, "unit", unit.ID, unit)
	stock := StockData{ItemID: "item-1", LocationID: "loc-1", QuantityBase: 1500, UnitCode: "pcs", BaseUnit: "pcs", ScaleFactor: 1}
	identity := projectionPair(stock.ItemID, stock.LocationID)
	seedCatalogProjection(t, fixture, "stock", identity, stock)
	setCatalogProjectionRevision(t, fixture, "unit", unit.ID, 7)
	setCatalogProjectionRevision(t, fixture, "stock", identity, 11)
	for _, retained := range []struct{ kind, payload string }{
		{"unit", ` { "status":"retired", "notes":"Notes", "serial_number":"", "asset_code":"  AST é  ", "location_id":null, "item_id":"individual", "id":"retained-unit" } `},
		{"stock", ` { "location_id":"loc-1", "item_id":"item-1", "quantity_base":1500, "base_unit":"pcs", "unit_code":"pcs", "scale_factor":1 } `},
	} {
		execSQL(t, fixture.db, "UPDATE current_projections SET payload_json=?,updated_at='retained-time' WHERE entity_type=?", retained.payload, retained.kind)
	}
	execSQL(t, fixture.db, "INSERT INTO sync_changes(entity_type,entity_id,operation,entity_revision,projection_json,created_at) SELECT entity_type,entity_id,'upsert',entity_revision,payload_json,'2026' FROM current_projections WHERE entity_type IN('unit','stock')")
	before := domainState(t, fixture)
	projections := currentProjectionState(t, fixture)
	epoch := scalar[string](t, fixture.db, "SELECT dataset_epoch FROM sync_state")
	_ = fixture.db.Close()
	s, err := Open(t.Context(), Config{Path: path, Migrate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Report().From != 9 || s.Report().To < 10 || s.Report().BackupPath == "" || scalar[string](t, s.db, "SELECT dataset_epoch FROM sync_state") != epoch {
		t.Fatal("certified root upgrade changed epoch or skipped private snapshot")
	}
	for key, value := range projections {
		if currentProjectionState(t, s)[key] != value {
			t.Fatal("existing current bytes/revision were rewritten", key)
		}
	}
	after := domainState(t, s)
	for _, table := range []string{"items", "item_units", "stock_balances", "sync_changes", "mutation_events", "inventory_events", "data_revision", "search_documents", "unit_search_documents"} {
		if after[table] != before[table] {
			t.Fatal("root backfill changed retained source/history/cache", table)
		}
	}
	if scalar[string](t, s.db, "SELECT updated_at FROM current_projections WHERE entity_type='unit'") != "retained-time" {
		t.Fatal("materialized timestamp was rewritten")
	}
	value, err := loadUnit(t.Context(), s.db, unit.ID)
	if err != nil || value.Notes != "Notes" || value.Revision != 7 {
		t.Fatal("loader copied retained cache bytes instead of final source", err)
	}
}

func TestHoldingInvalidLegacyBackfillRollsBackSourceLedgerAndAllPendingDDL(t *testing.T) {
	for _, invalid := range []struct{ name, query string }{
		{"empty unit ID", "INSERT INTO item_units(id,item_id,asset_code,created_at,updated_at) VALUES('','individual','AST','2026','2026')"},
		{"wide unit ID", "INSERT INTO item_units(id,item_id,asset_code,created_at,updated_at) VALUES(printf('%0129d',0),'individual','AST','2026','2026')"},
		{"invalid unit ID UTF", "INSERT INTO item_units(id,item_id,asset_code,created_at,updated_at) VALUES(CAST(x'ff' AS TEXT),'individual','AST','2026','2026')"},
		{"empty asset code", "INSERT INTO item_units(id,item_id,asset_code,created_at,updated_at) VALUES('bad','individual','','2026','2026')"},
		{"wide asset code", "INSERT INTO item_units(id,item_id,asset_code,created_at,updated_at) VALUES('bad','individual',printf('%010001d',0),'2026','2026')"},
		{"native NUL asset bound", "INSERT INTO item_units(id,item_id,asset_code,created_at,updated_at) VALUES('bad','individual',char(0)||printf('%010000d',0),'2026','2026')"},
		{"asset code UTF", "INSERT INTO item_units(id,item_id,asset_code,created_at,updated_at) VALUES('bad','individual',CAST(x'ff' AS TEXT),'2026','2026')"},
		{"serial UTF", "INSERT INTO item_units(id,item_id,asset_code,serial_number,created_at,updated_at) VALUES('bad','individual','AST',CAST(x'ff' AS TEXT),'2026','2026')"},
		{"native NUL serial bound", "INSERT INTO item_units(id,item_id,asset_code,serial_number,created_at,updated_at) VALUES('bad','individual','AST',char(0)||printf('%010000d',0),'2026','2026')"},
		{"notes UTF", "INSERT INTO item_units(id,item_id,asset_code,notes,created_at,updated_at) VALUES('bad','individual','AST',CAST(x'ff' AS TEXT),'2026','2026')"},
		{"native NUL notes bound", "INSERT INTO item_units(id,item_id,asset_code,notes,created_at,updated_at) VALUES('bad','individual','AST',char(0)||printf('%010000d',0),'2026','2026')"},
		{"unsafe quantity", "UPDATE stock_balances SET quantity_base=9007199254740992"},
		{"unsafe scale", "UPDATE items SET scale_factor=9007199254740992 WHERE id='item-1'"},
		{"empty display unit", "UPDATE items SET unit_code='' WHERE id='item-1'"},
		{"display unit UTF", "UPDATE items SET unit_code=CAST(x'ff' AS TEXT) WHERE id='item-1'"},
		{"wide display unit", "UPDATE items SET unit_code=printf('%033d',0) WHERE id='item-1'"},
		{"invalid base unit", "UPDATE items SET base_unit='g'||char(10) WHERE id='item-1'"},
		{"unsafe unit counter", "INSERT INTO projection_revisions VALUES('unit','gone',9007199254740992)"},
		{"unsafe stock counter", "INSERT INTO projection_revisions VALUES('stock',json_array('item-1','loc-1'),9007199254740992)"},
		{"noncanonical stock counter", "INSERT INTO projection_revisions VALUES('stock','[ \"item-1\", \"loc-1\" ]',7)"},
		{"invalid stock pair", "INSERT INTO projection_revisions VALUES('stock','not-a-pair',7)"},
	} {
		t.Run(invalid.name, func(t *testing.T) {
			path, old := legacyFixture(t, 2)
			execSQL(t, old, "INSERT INTO items(id,name,tracking_mode,created_at,updated_at) VALUES('individual','Physical','individual','2026','2026')")
			execSQL(t, old, invalid.query)
			before := scalar[string](t, old, "SELECT quote(quantity_base) FROM stock_balances")
			_ = old.Close()
			if s, err := Open(t.Context(), Config{Path: path, Migrate: true, AllowLegacy: true}); err == nil {
				_ = s.Close()
				t.Fatal("invalid physical source/counter was certified by upgrade")
			}
			unchanged := rawDatabase(t, path)
			if scalar[int](t, unchanged, "SELECT MAX(version) FROM schema_migrations") != 2 || scalar[int](t, unchanged, "SELECT COUNT(*) FROM sqlite_schema WHERE name IN('current_projections','schema_migration_checksums','idx_units_item_id')") != 0 || scalar[string](t, unchanged, "SELECT quote(quantity_base) FROM stock_balances") != before {
				t.Fatal("failed backfill leaked schema/index/ledger publication or repaired source")
			}
		})
	}
}

func TestHoldingCertifiedUpgradeValidationFailureKeepsExistingSourceAndFrozenHistory(t *testing.T) {
	path, fixture := rootNineFixture(t)
	execSQL(t, fixture.db, "INSERT INTO items(id,name,tracking_mode,created_at,updated_at) VALUES('individual','Physical','individual','2026','2026')")
	execSQL(t, fixture.db, "INSERT INTO projection_revisions VALUES('item','individual',1)")
	execSQL(t, fixture.db, "INSERT INTO item_units(id,item_id,asset_code,notes,created_at,updated_at) VALUES('invalid','individual','AST',char(0)||printf('%010000d',0),'2026','2026')")
	seedCatalogProjection(t, fixture, "stock", projectionPair("item-1", "loc-1"), StockData{"item-1", "loc-1", 1500, "pcs", "pcs", 1})
	execSQL(t, fixture.db, "INSERT INTO sync_changes(entity_type,entity_id,operation,entity_revision,projection_json,created_at) SELECT entity_type,entity_id,'upsert',entity_revision,payload_json,'2026' FROM current_projections WHERE entity_type='stock'")
	before := domainState(t, fixture)
	_ = fixture.db.Close()
	if s, err := Open(t.Context(), Config{Path: path, Migrate: true}); !errors.Is(err, ErrDomainState) {
		if s != nil {
			_ = s.Close()
		}
		t.Fatal("invalid source escaped post-SQL transactional Go validation", err)
	}
	unchanged := rawDatabase(t, path)
	requireDomainState(t, &Store{db: unchanged}, before)
	if scalar[int](t, unchanged, "SELECT MAX(version) FROM schema_migrations") != 9 || scalar[int](t, unchanged, "SELECT COUNT(*) FROM schema_migration_checksums") != 9 || scalar[int](t, unchanged, "SELECT COUNT(*) FROM sqlite_schema WHERE name='idx_units_item_id'") != 0 {
		t.Fatal("failed certified upgrade published a new index/version/checksum")
	}
}

func TestHoldingBackfillValidationQueriesFailClosed(t *testing.T) {
	for _, stage := range []string{"counters", "units", "stock", "canceled"} {
		t.Run(stage, func(t *testing.T) {
			s := openTestStore(t)
			tx, err := s.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			statement := map[string]string{"counters": "DROP TABLE projection_revisions", "units": "DROP TABLE item_units", "stock": "DROP TABLE stock_balances"}[stage]
			if stage != "canceled" {
				if _, err = tx.ExecContext(t.Context(), statement); err != nil {
					t.Fatal(err)
				}
			}
			ctx := t.Context()
			if stage == "canceled" {
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
			}
			if err = validateHoldingsBackfill(ctx, tx); err == nil {
				t.Fatal("failed source/counter query was ignored")
			}
		})
	}
}
