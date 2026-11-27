package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestSyncInventoryEventCoverageRetainsDeletedUnitsAndLocations(t *testing.T) {
	s, catalog, auth, token := catalogFixture(t)
	locations, err := NewLocations(auth)
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := NewInventory(auth)
	if err != nil {
		t.Fatal(err)
	}
	location := createLocation(t, locations, token, "EVENT-HISTORIC-PLACE", nil)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Historic event item"})
	unit, err := inventory.CreateUnit(t.Context(), token, "event-coverage-create-unit", item.ID, item.Revision, UnitCreate{LocationID: &location.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = inventory.DeleteUnit(t.Context(), token, "event-coverage-delete-unit", unit.ID, unit.Revision); err != nil {
		t.Fatal(err)
	}
	location = getLocation(t, locations, token, location.ID)
	if _, err = locations.Delete(t.Context(), token, "event-coverage-delete-location", location.ID, location.Revision); err != nil {
		t.Fatal(err)
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM item_units WHERE id=?", unit.ID) != 0 ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM inventory_events WHERE item_unit_id=?", unit.ID) != 2 ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM locations WHERE id=? AND deleted_at IS NOT NULL", location.ID) != 1 {
		t.Fatal("fixture did not preserve deleted Unit and Location history")
	}
	before := domainState(t, s)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		count, err := verifySyncInventoryEventCoverage(t.Context(), tx)
		if err != nil || count != 2 {
			t.Fatal("deleted Unit and Location history was not covered", count, err)
		}
	})
	requireDomainState(t, s, before)
	status, err := auth.ReadSyncStatus(t.Context(), token)
	if err != nil || status.BootstrapReady || status.RetentionEnabled {
		t.Fatal("inventory event coverage enabled public synchronization", err)
	}
}

func TestSyncInventoryEventCoverageRejectsDamageWithoutPartialCounts(t *testing.T) {
	fixture, inventory := inventoryFixture(t)
	unit := createUnitTest(t, fixture, inventory, "event-damage-create-unit", testPointer("source"))
	id := scalar[string](t, fixture.store.db, "SELECT id FROM inventory_events WHERE item_unit_id=?", unit.ID)
	before := domainState(t, fixture.store)
	for _, test := range []struct {
		name   string
		damage func(*testing.T, *sql.Tx)
		want   error
	}{
		{"missing projection", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='inventory_event' AND entity_id=?", id)
		}, errSyncInventoryEventUncovered},
		{"changed projection", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE current_projections SET payload_json=json_set(payload_json,'$.quantity_base',2) WHERE entity_type='inventory_event' AND entity_id=?", id)
		}, ErrDomainState},
		{"counter mismatch", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE projection_revisions SET revision=revision+1 WHERE entity_type='inventory_event' AND entity_id=?", id)
		}, ErrDomainState},
		{"missing counter", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='inventory_event' AND entity_id=?", id)
			execSyncCoverageSQL(t, tx, "DELETE FROM projection_revisions WHERE entity_type='inventory_event' AND entity_id=?", id)
		}, ErrDomainState},
		{"invalid counter", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE projection_revisions SET revision=? WHERE entity_type='inventory_event' AND entity_id=?", maxSafeInteger+1, id)
		}, ErrDomainState},
		{"item counter missing", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='item' AND entity_id=?", unit.ItemID)
			execSyncCoverageSQL(t, tx, "DELETE FROM projection_revisions WHERE entity_type='item' AND entity_id=?", unit.ItemID)
		}, ErrDomainState},
		{"unit counter missing", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='unit' AND entity_id=?", unit.ID)
			execSyncCoverageSQL(t, tx, "DELETE FROM projection_revisions WHERE entity_type='unit' AND entity_id=?", unit.ID)
		}, ErrDomainState},
		{"orphan projection", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "INSERT INTO projection_revisions VALUES('inventory_event','orphan',1)")
			execSyncCoverageSQL(t, tx, `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at) VALUES('inventory_event','orphan',1,'{"id":"orphan"}','2026')`)
		}, ErrDomainState},
		{"invalid action", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DROP TRIGGER inventory_events_no_update")
			execSyncCoverageSQL(t, tx, "UPDATE inventory_events SET action='' WHERE id=?", id)
		}, ErrDomainState},
		{"invalid time", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DROP TRIGGER inventory_events_no_update")
			execSyncCoverageSQL(t, tx, "UPDATE inventory_events SET occurred_at='not-an-instant' WHERE id=?", id)
		}, ErrDomainState},
		{"oversized action", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DROP TRIGGER inventory_events_no_update")
			execSyncCoverageSQL(t, tx, "UPDATE inventory_events SET action=? WHERE id=?", strings.Repeat("😀", 10001), id)
		}, ErrDomainState},
		{"oversized NUL-prefixed note", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DROP TRIGGER inventory_events_no_update")
			execSyncCoverageSQL(t, tx, "UPDATE inventory_events SET note=? WHERE id=?", "\x00"+strings.Repeat("😀", 10000), id)
		}, ErrDomainState},
		{"oversized NUL-prefixed reason", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DROP TRIGGER inventory_events_no_update")
			execSyncCoverageSQL(t, tx, "UPDATE inventory_events SET reason=? WHERE id=?", "\x00"+strings.Repeat("😀", 10000), id)
		}, ErrDomainState},
		{"oversized Unit ID", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DROP TRIGGER inventory_events_no_update")
			execSyncCoverageSQL(t, tx, "UPDATE inventory_events SET item_unit_id=? WHERE id=?", strings.Repeat("家", 171), id)
		}, ErrDomainState},
		{"oversized Location ID", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "PRAGMA defer_foreign_keys=ON")
			execSyncCoverageSQL(t, tx, "DROP TRIGGER inventory_events_no_update")
			execSyncCoverageSQL(t, tx, "UPDATE inventory_events SET to_location_id=? WHERE id=?", strings.Repeat("家", 171), id)
		}, ErrDomainState},
		{"oversized Item ID", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "PRAGMA defer_foreign_keys=ON")
			execSyncCoverageSQL(t, tx, "DROP TRIGGER inventory_events_no_update")
			execSyncCoverageSQL(t, tx, "UPDATE inventory_events SET item_id=? WHERE id=?", strings.Repeat("家", 171), id)
		}, ErrDomainState},
		{"invalid note UTF-8", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DROP TRIGGER inventory_events_no_update")
			execSyncCoverageSQL(t, tx, "UPDATE inventory_events SET note=CAST(X'ff' AS TEXT) WHERE id=?", id)
		}, ErrDomainState},
		{"missing item", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "PRAGMA defer_foreign_keys=ON")
			execSyncCoverageSQL(t, tx, "DROP TRIGGER inventory_events_no_update")
			execSyncCoverageSQL(t, tx, "UPDATE inventory_events SET item_id='missing-item' WHERE id=?", id)
			execSyncCoverageSQL(t, tx, "UPDATE current_projections SET payload_json=json_set(payload_json,'$.item_id','missing-item') WHERE entity_type='inventory_event' AND entity_id=?", id)
		}, ErrDomainState},
		{"mismatched live Unit owner", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DROP TRIGGER inventory_events_no_update")
			execSyncCoverageSQL(t, tx, "UPDATE inventory_events SET item_id=? WHERE id=?", fixture.quantity.ID, id)
			execSyncCoverageSQL(t, tx, "UPDATE current_projections SET payload_json=json_set(payload_json,'$.item_id',?) WHERE entity_type='inventory_event' AND entity_id=?", fixture.quantity.ID, id)
		}, ErrDomainState},
		{"missing historical Location", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "PRAGMA defer_foreign_keys=ON")
			execSyncCoverageSQL(t, tx, "DROP TRIGGER inventory_events_no_update")
			execSyncCoverageSQL(t, tx, "UPDATE inventory_events SET to_location_id='missing-location' WHERE id=?", id)
			execSyncCoverageSQL(t, tx, "UPDATE current_projections SET payload_json=json_set(payload_json,'$.to_location_id','missing-location') WHERE entity_type='inventory_event' AND entity_id=?", id)
		}, ErrDomainState},
		{"location counter missing", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='location' AND entity_id='source'")
			execSyncCoverageSQL(t, tx, "DELETE FROM projection_revisions WHERE entity_type='location' AND entity_id='source'")
		}, ErrDomainState},
	} {
		t.Run(test.name, func(t *testing.T) {
			tx, err := fixture.store.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			test.damage(t, tx)
			if _, err = tx.ExecContext(t.Context(), "PRAGMA query_only=ON"); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if _, err := tx.ExecContext(t.Context(), "PRAGMA query_only=OFF"); err != nil {
					t.Error("restore query-only mode:", err)
				}
			}()
			count, err := verifySyncInventoryEventCoverage(t.Context(), tx)
			if !errors.Is(err, test.want) || count != 0 {
				t.Fatal("damaged inventory event returned coverage", count, err)
			}
		})
	}
	requireDomainState(t, fixture.store, before)
}

func TestSyncInventoryEventCoveragePagesExactIDsAndWithholdsLateMissingRow(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Event coverage item"})
	ids := make([]string, 0, 209)
	for index := 0; index < 205; index++ {
		ids = append(ids, fmt.Sprintf("event-%03d", index))
	}
	ids = append(ids, "event-098\x00-家庭", "event-197\x00-第二", "zz-uncovered", strings.Repeat("😀", 128))
	seedSyncInventoryCoverageEvents(t, s, item.ID, ids)
	before := domainState(t, s)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		first, err := readSyncCoverageIDs(t.Context(), tx, "inventory_event", "", false)
		if err != nil || len(first) != 100 || first[99] != "event-098\x00-家庭" {
			t.Fatal("first event page changed its embedded-NUL boundary", first, err)
		}
		second, err := readSyncCoverageIDs(t.Context(), tx, "inventory_event", first[99], true)
		if err != nil || len(second) != 100 || second[99] != "event-197\x00-第二" {
			t.Fatal("second event page changed its embedded-NUL boundary", second, err)
		}
		count, err := verifySyncInventoryEventCoverage(t.Context(), tx)
		if err != nil || count != int64(len(ids)) {
			t.Fatal("multi-page events changed exact identities", count, err)
		}
	})
	func() {
		tx, err := s.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='inventory_event' AND entity_id='zz-uncovered'")
		count, err := verifySyncInventoryEventCoverage(t.Context(), tx)
		if !errors.Is(err, errSyncInventoryEventUncovered) || count != 0 {
			t.Fatal("late missing event returned partial coverage", count, err)
		}
	}()
	requireDomainState(t, s, before)
}

func seedSyncInventoryCoverageEvents(t *testing.T, s *Store, itemID string, ids []string) {
	t.Helper()
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, id := range ids {
		execSyncCoverageSQL(t, tx, `INSERT INTO inventory_events(id,item_id,action,quantity_base,occurred_at) VALUES(?,?,'legacy',0,'2026-01-01T00:00:00Z')`, id, itemID)
		execSyncCoverageSQL(t, tx, "INSERT INTO projection_revisions VALUES('inventory_event',?,1)", id)
		execSyncCoverageSQL(t, tx, `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 SELECT 'inventory_event',id,1,json_object('id',id,'item_id',item_id,'action',action,'occurred_at',occurred_at,'item_unit_id',item_unit_id,'from_location_id',from_location_id,'to_location_id',to_location_id,'quantity_base',quantity_base),'2026'
 FROM inventory_events WHERE id=?`, id)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestSyncInventoryEventCoverageIncludesRealStockMovement(t *testing.T) {
	fixture, inventory := inventoryFixture(t)
	if _, err := inventory.Receive(t.Context(), fixture.token, "event-coverage-receive", fixture.quantity.Revision, StockReceive{
		ItemID: fixture.quantity.ID, TargetLocationID: "target", QuantityBase: 7, Note: "Reviewed receipt",
	}); err != nil {
		t.Fatal(err)
	}
	item := getCatalogItem(t, fixture.catalog, fixture.token, fixture.quantity.ID)
	if _, err := inventory.Transfer(t.Context(), fixture.token, "event-coverage-transfer", item.Revision, StockTransfer{
		ItemID: item.ID, SourceLocationID: "source", TargetLocationID: "target", QuantityBase: 3,
	}); err != nil {
		t.Fatal(err)
	}
	before := domainState(t, fixture.store)
	withQueryOnlySearchTx(t, fixture.store, func(tx *sql.Tx) {
		count, err := verifySyncInventoryEventCoverage(t.Context(), tx)
		if err != nil || count != 2 {
			t.Fatal("real Stock movements were not covered", count, err)
		}
	})
	requireDomainState(t, fixture.store, before)
}

func TestSyncInventoryEventCoverageEmptyAndExactPageBoundary(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Event boundary item"})
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		if count, err := verifySyncInventoryEventCoverage(t.Context(), tx); err != nil || count != 0 {
			t.Fatal("empty event coverage failed", count, err)
		}
	})
	ids := make([]string, 0, 100)
	for index := 0; index < 100; index++ {
		ids = append(ids, fmt.Sprintf("boundary-%03d", index))
	}
	seedSyncInventoryCoverageEvents(t, s, item.ID, ids)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		if count, err := verifySyncInventoryEventCoverage(t.Context(), tx); err != nil || count != 100 {
			t.Fatal("exact-boundary event coverage failed", count, err)
		}
	})
}

func TestSyncInventoryEventCoverageAcceptsBoundedSourceAndLongRetainedInstant(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Event source boundaries"})
	id := strings.Repeat("😀", 128)
	action, contextText := strings.Repeat("😀", 10000), strings.Repeat("家", 10000)
	instant := "2026-01-01T00:00:00." + strings.Repeat("1", 50000) + "Z"
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	execSyncCoverageSQL(t, tx, `INSERT INTO inventory_events(id,item_id,action,quantity_base,occurred_at,note,reason) VALUES(?,?,?,0,?,?,?)`, id, item.ID, action, instant, contextText, contextText)
	execSyncCoverageSQL(t, tx, "INSERT INTO projection_revisions VALUES('inventory_event',?,1)", id)
	execSyncCoverageSQL(t, tx, `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 SELECT 'inventory_event',id,1,json_object('id',id,'item_id',item_id,'action',action,'occurred_at',occurred_at,'item_unit_id',item_unit_id,'from_location_id',from_location_id,'to_location_id',to_location_id,'quantity_base',quantity_base),'2026'
 FROM inventory_events WHERE id=?`, id)
	if _, err = tx.ExecContext(t.Context(), "PRAGMA query_only=ON"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := tx.ExecContext(t.Context(), "PRAGMA query_only=OFF"); err != nil {
			t.Error("restore query-only mode:", err)
		}
	}()
	if count, err := verifySyncInventoryEventCoverage(t.Context(), tx); err != nil || count != 1 {
		t.Fatal("bounded event fields or long retained instant were rejected", count, err)
	}
}

func TestSyncInventoryEventCoverageWithholdsOversizedCurrent(t *testing.T) {
	fixture, inventory := inventoryFixture(t)
	unit := createUnitTest(t, fixture, inventory, "event-large-create", testPointer("source"))
	id := scalar[string](t, fixture.store.db, "SELECT id FROM inventory_events WHERE item_unit_id=?", unit.ID)
	before := domainState(t, fixture.store)
	func() {
		tx, err := fixture.store.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		oversized := `{"data":"` + strings.Repeat("x", maxSyncPageBytes) + `"}`
		execSyncCoverageSQL(t, tx, "UPDATE current_projections SET payload_json=? WHERE entity_type='inventory_event' AND entity_id=?", oversized, id)
		if count, err := verifySyncInventoryEventCoverage(t.Context(), tx); !errors.Is(err, errSyncPayloadTooLarge) || count != 0 {
			t.Fatal("oversized event projection returned coverage", count, err)
		}
	}()
	requireDomainState(t, fixture.store, before)
}

func TestSyncInventoryEventCoverageWithholdsOversizedSourceInstant(t *testing.T) {
	fixture, inventory := inventoryFixture(t)
	unit := createUnitTest(t, fixture, inventory, "event-large-instant-create", nil)
	id := scalar[string](t, fixture.store.db, "SELECT id FROM inventory_events WHERE item_unit_id=?", unit.ID)
	before := domainState(t, fixture.store)
	func() {
		tx, err := fixture.store.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		execSyncCoverageSQL(t, tx, "DROP TRIGGER inventory_events_no_update")
		instant := "2026-01-01T00:00:00." + strings.Repeat("1", maxSyncPageBytes) + "Z"
		execSyncCoverageSQL(t, tx, "UPDATE inventory_events SET occurred_at=? WHERE id=?", instant, id)
		if count, err := verifySyncInventoryEventCoverage(t.Context(), tx); !errors.Is(err, ErrDomainState) || count != 0 {
			t.Fatal("oversized source instant returned coverage", count, err)
		}
	}()
	requireDomainState(t, fixture.store, before)
}

func TestSyncInventoryEventCoverageRejectsInvalidSourceIDs(t *testing.T) {
	for _, id := range []string{"", strings.Repeat("x", 129), strings.Repeat("家", 171), string([]byte{0xff})} {
		t.Run(fmt.Sprintf("bytes-%d", len(id)), func(t *testing.T) {
			s, catalog, _, token := catalogFixture(t)
			item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Invalid event identity"})
			seedSyncInventoryCoverageEvents(t, s, item.ID, []string{id})
			withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
				count, err := verifySyncInventoryEventCoverage(t.Context(), tx)
				if !errors.Is(err, ErrDomainState) || count != 0 {
					t.Fatal("invalid event identity was skipped", count, err)
				}
			})
		})
	}
}

func TestSyncInventoryEventCoverageRejectsInvalidContextsAndTransactions(t *testing.T) {
	s, _, _, _ := catalogFixture(t)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := verifySyncInventoryEventCoverage(nil, tx); !errors.Is(err, ErrDomainState) || result != 0 {
		t.Fatal("nil context returned coverage", result, err)
	}
	if result, err := verifySyncInventoryEventCoverage(t.Context(), nil); !errors.Is(err, ErrDomainState) || result != 0 {
		t.Fatal("nil transaction returned coverage", result, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if result, err := verifySyncInventoryEventCoverage(ctx, tx); !errors.Is(err, context.Canceled) || result != 0 {
		t.Fatal("canceled context returned coverage", result, err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if result, err := verifySyncInventoryEventCoverage(t.Context(), tx); !errors.Is(err, sql.ErrTxDone) || result != 0 {
		t.Fatal("closed transaction returned coverage", result, err)
	}
}
