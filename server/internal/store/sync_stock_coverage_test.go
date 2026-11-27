package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestSyncStockCoverageIncludesZeroBalancesWithoutMixingUnits(t *testing.T) {
	fixture := holdingsFixture(t)
	inventory, err := NewInventory(fixture.auth)
	if err != nil {
		t.Fatal(err)
	}
	liquid := createCatalogItem(t, fixture.catalog, fixture.token, ItemCreate{Name: "Liquid", TrackingMode: testPointer("quantity"), UnitCode: testPointer("l"), BaseUnit: testPointer("ml"), ScaleFactor: testPointer(int64(1000))})
	if _, err = inventory.Receive(t.Context(), fixture.token, "stock-coverage-liquid", liquid.Revision, StockReceive{ItemID: liquid.ID, TargetLocationID: "target", QuantityBase: 1200}); err != nil {
		t.Fatal(err)
	}
	before := domainState(t, fixture.store)
	withQueryOnlySearchTx(t, fixture.store, func(tx *sql.Tx) {
		coverage, err := verifySyncStockCoverage(t.Context(), tx)
		if err != nil || coverage.Nonzero != 2 || coverage.Zero != 1 {
			t.Fatal("present and zero Stock sources were not covered", coverage, err)
		}
	})
	requireDomainState(t, fixture.store, before)
	status, err := fixture.auth.ReadSyncStatus(t.Context(), fixture.token)
	if err != nil || status.BootstrapReady || status.RetentionEnabled {
		t.Fatal("Stock-only coverage enabled public synchronization", err)
	}
}

func TestSyncStockCoverageRejectsDamageWithoutPartialCounts(t *testing.T) {
	fixture := holdingsFixture(t)
	identity := projectionPair(fixture.stock.ItemID, fixture.stock.LocationID)
	before := domainState(t, fixture.store)
	for _, test := range []struct {
		name   string
		damage func(*testing.T, *sql.Tx)
		want   error
	}{
		{"missing current", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='stock' AND entity_id=?", identity)
		}, errSyncStockUncovered},
		{"changed quantity", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE current_projections SET payload_json=json_set(payload_json,'$.quantity_base',999) WHERE entity_type='stock' AND entity_id=?", identity)
		}, ErrDomainState},
		{"changed measurement", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE current_projections SET payload_json=json_set(payload_json,'$.base_unit','ml') WHERE entity_type='stock' AND entity_id=?", identity)
		}, ErrDomainState},
		{"counter mismatch", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE projection_revisions SET revision=revision+1 WHERE entity_type='stock' AND entity_id=?", identity)
		}, ErrDomainState},
		{"counter missing", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='stock' AND entity_id=?", identity)
			execSyncCoverageSQL(t, tx, "DELETE FROM projection_revisions WHERE entity_type='stock' AND entity_id=?", identity)
		}, ErrDomainState},
		{"wrong owner tracking", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE items SET tracking_mode='individual' WHERE id=?", fixture.quantity.ID)
		}, ErrDomainState},
		{"owner counter missing", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='item' AND entity_id=?", fixture.quantity.ID)
			execSyncCoverageSQL(t, tx, "DELETE FROM projection_revisions WHERE entity_type='item' AND entity_id=?", fixture.quantity.ID)
		}, ErrDomainState},
		{"deleted location", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE locations SET deleted_at='2026' WHERE id='source'")
			execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='location' AND entity_id='source'")
		}, ErrDomainState},
		{"ancestor cycle", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE locations SET parent_id='source' WHERE id='root'")
			execSyncCoverageSQL(t, tx, "UPDATE current_projections SET payload_json=json_set(payload_json,'$.parent_id','source') WHERE entity_type='location' AND entity_id='root'")
		}, ErrDomainState},
		{"orphan current", func(t *testing.T, tx *sql.Tx) {
			seedOrphanStockCurrent(t, tx, projectionPair("missing-item", "source"))
		}, ErrDomainState},
		{"noncanonical pair", func(t *testing.T, tx *sql.Tx) {
			seedOrphanStockCurrent(t, tx, `[ "missing-item","source"]`)
		}, ErrDomainState},
		{"oversized pair", func(t *testing.T, tx *sql.Tx) {
			seedOrphanStockCurrent(t, tx, strings.Repeat("x", maxSyncIdentityBytes+1))
		}, ErrDomainState},
		{"empty pair", func(t *testing.T, tx *sql.Tx) {
			seedOrphanStockCurrent(t, tx, "")
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
			coverage, err := verifySyncStockCoverage(t.Context(), tx)
			if !errors.Is(err, test.want) || coverage != (syncStockCoverage{}) {
				t.Fatal("damaged Stock returned coverage", coverage, err)
			}
		})
	}
	requireDomainState(t, fixture.store, before)
}

func seedOrphanStockCurrent(t *testing.T, tx *sql.Tx, identity string) {
	t.Helper()
	execSyncCoverageSQL(t, tx, "INSERT INTO projection_revisions VALUES('stock',?,1)", identity)
	execSyncCoverageSQL(t, tx, `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 VALUES('stock',?,1,'{}','2026')`, identity)
}

func TestSyncStockCoverageEmptyAndExactPageBoundary(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	owner := createCatalogItem(t, catalog, token, ItemCreate{Name: "Stock boundary owner", TrackingMode: testPointer("quantity"), UnitCode: testPointer("kg"), BaseUnit: testPointer("g"), ScaleFactor: testPointer(int64(1000))})
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		coverage, err := verifySyncStockCoverage(t.Context(), tx)
		if err != nil || coverage != (syncStockCoverage{}) {
			t.Fatal("empty Stock coverage failed", coverage, err)
		}
	})
	ids := make([]string, 0, 100)
	for index := 0; index < 100; index++ {
		ids = append(ids, fmt.Sprintf("boundary-%03d", index))
	}
	seedSyncCoverageLocations(t, s, ids)
	seedSyncStockCoverage(t, s, owner, ids)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		coverage, err := verifySyncStockCoverage(t.Context(), tx)
		if err != nil || coverage.Nonzero != 50 || coverage.Zero != 50 {
			t.Fatal("exact-boundary Stock coverage failed", coverage, err)
		}
	})
}

func TestSyncStockCoveragePagesAcrossItemBoundary(t *testing.T) {
	s, _, _, _ := catalogFixture(t)
	owners := []Item{
		{ItemData: ItemData{ID: "item-a", UnitCode: "kg", BaseUnit: "g", ScaleFactor: 1000}},
		{ItemData: ItemData{ID: "item-b", UnitCode: "kg", BaseUnit: "g", ScaleFactor: 1000}},
	}
	for _, owner := range owners {
		execSQL(t, s.db, `INSERT INTO items(id,name,tracking_mode,unit_code,base_unit,scale_factor,status,created_at,updated_at)
 VALUES(?,?,'quantity','kg','g',1000,'active','2026','2026')`, owner.ID, owner.ID)
		execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('item',?,1)", owner.ID)
	}
	ids := make([]string, 0, 100)
	for index := 0; index < 100; index++ {
		ids = append(ids, fmt.Sprintf("location-%03d", index))
	}
	seedSyncCoverageLocations(t, s, ids)
	seedSyncStockCoverage(t, s, owners[0], ids)
	seedSyncStockCoverage(t, s, owners[1], ids[:1])
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		first, err := readSyncItemPairSources(t.Context(), tx, "stock", "", "", false)
		if err != nil || len(first) != 100 || first[99] != (syncItemPair{ItemID: "item-a", RelatedID: "location-099"}) {
			t.Fatal("first source page changed its Item boundary", first, err)
		}
		second, err := readSyncItemPairSources(t.Context(), tx, "stock", first[99].ItemID, first[99].RelatedID, true)
		if err != nil || len(second) != 1 || second[0] != (syncItemPair{ItemID: "item-b", RelatedID: "location-000"}) {
			t.Fatal("second source page changed its Item boundary", second, err)
		}
		coverage, err := verifySyncStockCoverage(t.Context(), tx)
		if err != nil || coverage.Nonzero != 50 || coverage.Zero != 51 {
			t.Fatal("cross-Item source coverage failed", coverage, err)
		}
	})
}

func TestSyncStockCoverageWithholdsOversizedCurrent(t *testing.T) {
	fixture := holdingsFixture(t)
	before := domainState(t, fixture.store)
	func() {
		tx, err := fixture.store.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		oversized := `{"data":"` + strings.Repeat("x", maxSyncPageBytes) + `"}`
		execSyncCoverageSQL(t, tx, "UPDATE current_projections SET payload_json=? WHERE entity_type='stock' AND entity_id=?", oversized, projectionPair(fixture.stock.ItemID, fixture.stock.LocationID))
		coverage, err := verifySyncStockCoverage(t.Context(), tx)
		if !errors.Is(err, errSyncPayloadTooLarge) || coverage != (syncStockCoverage{}) {
			t.Fatal("oversized Stock projection returned coverage", coverage, err)
		}
	}()
	requireDomainState(t, fixture.store, before)
}

func TestSyncStockCoveragePagesExactCompositeIDsAndWithholdsLateMissingRow(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	owner := createCatalogItem(t, catalog, token, ItemCreate{Name: "Stock coverage owner", TrackingMode: testPointer("quantity"), UnitCode: testPointer("kg"), BaseUnit: testPointer("g"), ScaleFactor: testPointer(int64(1000))})
	ids := make([]string, 0, 209)
	for index := 0; index < 205; index++ {
		ids = append(ids, fmt.Sprintf("location-%03d", index))
	}
	ids = append(ids, "location-098\x00-家庭", "location-197\x00-第二", strings.Repeat("😀", 128), "zz-uncovered")
	seedSyncCoverageLocations(t, s, ids)
	seedSyncStockCoverage(t, s, owner, ids)
	before := domainState(t, s)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		first, err := readSyncItemPairSources(t.Context(), tx, "stock", "", "", false)
		if err != nil || len(first) != 100 || first[99].RelatedID != "location-098\x00-家庭" {
			t.Fatal("the first Stock page changed its embedded-NUL boundary", first, err)
		}
		second, err := readSyncItemPairSources(t.Context(), tx, "stock", first[99].ItemID, first[99].RelatedID, true)
		if err != nil || len(second) != 100 || second[99].RelatedID != "location-197\x00-第二" {
			t.Fatal("the second Stock page changed its embedded-NUL boundary", second, err)
		}
		coverage, err := verifySyncStockCoverage(t.Context(), tx)
		if err != nil || coverage.Nonzero != int64(len(ids)/2) || coverage.Zero != int64(len(ids)-len(ids)/2) {
			t.Fatal("multi-page Stock coverage changed identities or zero rows", coverage, err)
		}
	})
	func() {
		tx, err := s.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='stock' AND entity_id=?", projectionPair(owner.ID, "zz-uncovered"))
		coverage, err := verifySyncStockCoverage(t.Context(), tx)
		if !errors.Is(err, errSyncStockUncovered) || coverage != (syncStockCoverage{}) {
			t.Fatal("a late missing Stock returned partial coverage", coverage, err)
		}
	}()
	requireDomainState(t, s, before)
}

func seedSyncStockCoverage(t *testing.T, s *Store, owner Item, locationIDs []string) {
	t.Helper()
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for index, locationID := range locationIDs {
		quantity := int64(index % 2)
		execSyncCoverageSQL(t, tx, "INSERT INTO stock_balances(item_id,location_id,quantity_base,updated_at) VALUES(?,?,?,'2026')", owner.ID, locationID, quantity)
		identity := projectionPair(owner.ID, locationID)
		execSyncCoverageSQL(t, tx, "INSERT INTO projection_revisions VALUES('stock',?,1)", identity)
		execSyncCoverageSQL(t, tx, `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 VALUES('stock',?,1,json_object('item_id',?,'location_id',?,'quantity_base',?,'unit_code',?,'base_unit',?,'scale_factor',?),'2026')`,
			identity, owner.ID, locationID, quantity, owner.UnitCode, owner.BaseUnit, owner.ScaleFactor)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestSyncStockCoverageAcceptsMaximumEscapedCompositeIdentity(t *testing.T) {
	s, _, _, _ := catalogFixture(t)
	id := strings.Repeat("\x00", 128)
	execSQL(t, s.db, `INSERT INTO items(id,name,tracking_mode,unit_code,base_unit,scale_factor,status,created_at,updated_at)
 VALUES(?,'Maximum stock owner','quantity','kg','g',1000,'active','2026','2026')`, id)
	execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('item',?,1)", id)
	seedSyncCoverageLocations(t, s, []string{id})
	owner := Item{ItemData: ItemData{ID: id, UnitCode: "kg", BaseUnit: "g", ScaleFactor: 1000}}
	seedSyncStockCoverage(t, s, owner, []string{id})
	if len(projectionPair(id, id)) != maxSyncIdentityBytes {
		t.Fatal("fixture did not exercise the maximum escaped composite identity")
	}
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		coverage, err := verifySyncStockCoverage(t.Context(), tx)
		if err != nil || coverage.Nonzero != 0 || coverage.Zero != 1 {
			t.Fatal("maximum escaped Stock pair was rejected", coverage, err)
		}
	})
}

func TestSyncStockCoverageRejectsInvalidSourcePairs(t *testing.T) {
	fixture := holdingsFixture(t)
	before := domainState(t, fixture.store)
	for _, field := range []string{"item_id", "location_id"} {
		for _, id := range []string{"", strings.Repeat("x", 129), strings.Repeat("家", 171), string([]byte{0xff})} {
			t.Run(fmt.Sprintf("%s-bytes-%d", field, len(id)), func(t *testing.T) {
				tx, err := fixture.store.db.BeginTx(t.Context(), nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				execSyncCoverageSQL(t, tx, "PRAGMA defer_foreign_keys=ON")
				execSyncCoverageSQL(t, tx, "UPDATE stock_balances SET "+field+"=? WHERE item_id=? AND location_id=?", id, fixture.stock.ItemID, fixture.stock.LocationID)
				coverage, err := verifySyncStockCoverage(t.Context(), tx)
				if !errors.Is(err, ErrDomainState) || coverage != (syncStockCoverage{}) {
					t.Fatal("invalid Stock source identity was skipped", coverage, err)
				}
			})
		}
	}
	requireDomainState(t, fixture.store, before)
}

func TestSyncStockCoverageRejectsInvalidContextsAndTransactions(t *testing.T) {
	s, _, _, _ := catalogFixture(t)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if ids, err := readSyncPairProjectionIDs(t.Context(), tx, "stock;DROP TABLE items", "", false); !errors.Is(err, ErrDomainState) || ids != nil {
		t.Fatal("unsupported projection kind was queried", ids, err)
	}
	if value, err := verifySyncStockCoverage(nil, tx); !errors.Is(err, ErrDomainState) || value != (syncStockCoverage{}) {
		t.Fatal("nil context returned coverage", value, err)
	}
	if value, err := verifySyncStockCoverage(t.Context(), nil); !errors.Is(err, ErrDomainState) || value != (syncStockCoverage{}) {
		t.Fatal("nil transaction returned coverage", value, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if value, err := verifySyncStockCoverage(ctx, tx); !errors.Is(err, context.Canceled) || value != (syncStockCoverage{}) {
		t.Fatal("canceled context returned coverage", value, err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if value, err := verifySyncStockCoverage(t.Context(), tx); !errors.Is(err, sql.ErrTxDone) || value != (syncStockCoverage{}) {
		t.Fatal("closed transaction returned coverage", value, err)
	}
}
