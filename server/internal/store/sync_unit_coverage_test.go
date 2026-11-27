package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestSyncUnitCoverageIncludesDisposedAndUnlocatedUnits(t *testing.T) {
	fixture := holdingsFixture(t)
	inventory, err := NewInventory(fixture.auth)
	if err != nil {
		t.Fatal(err)
	}
	disposed, err := inventory.SetUnitStatus(t.Context(), fixture.token, "unit-coverage-dispose", fixture.unit.ID, fixture.unit.Revision, UnitStatus{Status: "disposed", DisposalKind: testPointer("recycled")})
	if err != nil {
		t.Fatal(err)
	}
	owner := getCatalogItem(t, fixture.catalog, fixture.token, fixture.individual.ID)
	unlocated, err := inventory.CreateUnit(t.Context(), fixture.token, "unit-coverage-unlocated", owner.ID, owner.Revision, UnitCreate{})
	if err != nil {
		t.Fatal(err)
	}
	owner = getCatalogItem(t, fixture.catalog, fixture.token, owner.ID)
	removed, err := inventory.CreateUnit(t.Context(), fixture.token, "unit-coverage-removed", owner.ID, owner.Revision, UnitCreate{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = inventory.DeleteUnit(t.Context(), fixture.token, "unit-coverage-delete", removed.ID, removed.Revision); err != nil {
		t.Fatal(err)
	}
	owner = getCatalogItem(t, fixture.catalog, fixture.token, owner.ID)
	if _, err = fixture.catalog.Archive(t.Context(), fixture.token, "unit-coverage-archive-owner", owner.ID, owner.Revision, ""); err != nil {
		t.Fatal(err)
	}
	if disposed.Status != "disposed" || unlocated.LocationID != nil {
		t.Fatal("fixture did not preserve disposed and unlocated Units")
	}
	before := domainState(t, fixture.store)
	withQueryOnlySearchTx(t, fixture.store, func(tx *sql.Tx) {
		count, err := verifySyncUnitCoverage(t.Context(), tx)
		if err != nil || count != 2 {
			t.Fatal("retained Unit sources were not covered", count, err)
		}
	})
	requireDomainState(t, fixture.store, before)
	status, err := fixture.auth.ReadSyncStatus(t.Context(), fixture.token)
	if err != nil || status.BootstrapReady || status.RetentionEnabled {
		t.Fatal("unit-only coverage enabled public synchronization", err)
	}
}

func TestSyncUnitCoverageRejectsDamageWithoutPartialCounts(t *testing.T) {
	fixture := holdingsFixture(t)
	before := domainState(t, fixture.store)
	for _, test := range []struct {
		name   string
		damage func(*testing.T, *sql.Tx)
		want   error
	}{
		{"missing current", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='unit' AND entity_id=?", fixture.unit.ID)
		}, errSyncUnitUncovered},
		{"stale current", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE current_projections SET payload_json=json_set(payload_json,'$.notes','changed') WHERE entity_type='unit' AND entity_id=?", fixture.unit.ID)
		}, ErrDomainState},
		{"counter mismatch", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE projection_revisions SET revision=revision+1 WHERE entity_type='unit' AND entity_id=?", fixture.unit.ID)
		}, ErrDomainState},
		{"owner counter missing", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='item' AND entity_id=?", fixture.individual.ID)
			execSyncCoverageSQL(t, tx, "DELETE FROM projection_revisions WHERE entity_type='item' AND entity_id=?", fixture.individual.ID)
		}, ErrDomainState},
		{"owner tracking mismatch", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE items SET tracking_mode='quantity' WHERE id=?", fixture.individual.ID)
		}, ErrDomainState},
		{"missing owner", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "PRAGMA defer_foreign_keys=ON")
			execSyncCoverageSQL(t, tx, "UPDATE item_units SET item_id='missing-owner' WHERE id=?", fixture.unit.ID)
			execSyncCoverageSQL(t, tx, "UPDATE current_projections SET payload_json=json_set(payload_json,'$.item_id','missing-owner') WHERE entity_type='unit' AND entity_id=?", fixture.unit.ID)
		}, ErrDomainState},
		{"deleted location", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE locations SET deleted_at='2026' WHERE id='source'")
			execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='location' AND entity_id='source'")
		}, ErrDomainState},
		{"missing location", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "PRAGMA defer_foreign_keys=ON")
			execSyncCoverageSQL(t, tx, "UPDATE item_units SET location_id='missing-location' WHERE id=?", fixture.unit.ID)
			execSyncCoverageSQL(t, tx, "UPDATE current_projections SET payload_json=json_set(payload_json,'$.location_id','missing-location') WHERE entity_type='unit' AND entity_id=?", fixture.unit.ID)
		}, ErrDomainState},
		{"ancestor cycle", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE locations SET parent_id='source' WHERE id='root'")
			execSyncCoverageSQL(t, tx, "UPDATE current_projections SET payload_json=json_set(payload_json,'$.parent_id','source') WHERE entity_type='location' AND entity_id='root'")
		}, ErrDomainState},
		{"orphan current", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "INSERT INTO projection_revisions VALUES('unit','orphan-unit',1)")
			execSyncCoverageSQL(t, tx, `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at) VALUES('unit','orphan-unit',1,'{"id":"orphan-unit"}','2026')`)
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
			count, err := verifySyncUnitCoverage(t.Context(), tx)
			if !errors.Is(err, test.want) || count != 0 {
				t.Fatal("damaged unit returned coverage", count, err)
			}
		})
	}
	requireDomainState(t, fixture.store, before)
}

func TestSyncUnitCoverageEmptyAndExactPageBoundary(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	owner := createCatalogItem(t, catalog, token, ItemCreate{Name: "Unit boundary owner"})
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		count, err := verifySyncUnitCoverage(t.Context(), tx)
		if err != nil || count != 0 {
			t.Fatal("empty Unit coverage failed", count, err)
		}
	})
	ids := make([]string, 0, 100)
	for index := 0; index < 100; index++ {
		ids = append(ids, fmt.Sprintf("boundary-%03d", index))
	}
	seedSyncUnitSearchCoverage(t, s, owner, ids)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, id := range ids {
		execSyncCoverageSQL(t, tx, `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 SELECT 'unit',id,1,json_object('id',id,'item_id',item_id,'location_id',location_id,'asset_code',asset_code,
 'status',status,'serial_number',serial_number,'notes',notes),'2026' FROM item_units WHERE id=?`, id)
	}
	count, err := verifySyncUnitCoverage(t.Context(), tx)
	if err != nil || count != 100 {
		t.Fatal("exact-boundary Unit coverage failed", count, err)
	}
}

func TestSyncUnitCoverageWithholdsOversizedCurrent(t *testing.T) {
	fixture := holdingsFixture(t)
	before := domainState(t, fixture.store)
	func() {
		tx, err := fixture.store.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		oversized := `{"data":"` + strings.Repeat("x", maxSyncPageBytes) + `"}`
		execSyncCoverageSQL(t, tx, "UPDATE current_projections SET payload_json=? WHERE entity_type='unit' AND entity_id=?", oversized, fixture.unit.ID)
		count, err := verifySyncUnitCoverage(t.Context(), tx)
		if !errors.Is(err, errSyncPayloadTooLarge) || count != 0 {
			t.Fatal("oversized Unit projection returned coverage", count, err)
		}
	}()
	requireDomainState(t, fixture.store, before)
}

func TestSyncUnitCoveragePagesExactIDsAndWithholdsLateMissingRow(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	owner := createCatalogItem(t, catalog, token, ItemCreate{Name: "Unit coverage owner"})
	ids := make([]string, 0, 209)
	for index := 0; index < 205; index++ {
		ids = append(ids, fmt.Sprintf("unit-%03d", index))
	}
	ids = append(ids, "unit-098\x00-家庭", "unit-197\x00-第二", strings.Repeat("😀", 128), "zz-uncovered")
	seedSyncUnitSearchCoverage(t, s, owner, ids)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		execSyncCoverageSQL(t, tx, `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 SELECT 'unit',id,1,json_object('id',id,'item_id',item_id,'location_id',location_id,'asset_code',asset_code,
 'status',status,'serial_number',serial_number,'notes',notes),'2026' FROM item_units WHERE id=?`, id)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	before := domainState(t, s)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		first, err := readSyncCoverageIDs(t.Context(), tx, "unit", "", false)
		if err != nil || len(first) != 100 || first[99] != "unit-098\x00-家庭" {
			t.Fatal("the first Unit page changed its embedded-NUL boundary", first, err)
		}
		second, err := readSyncCoverageIDs(t.Context(), tx, "unit", first[99], true)
		if err != nil || len(second) != 100 || second[99] != "unit-197\x00-第二" {
			t.Fatal("the second Unit page changed its embedded-NUL boundary", second, err)
		}
		count, err := verifySyncUnitCoverage(t.Context(), tx)
		if err != nil || count != int64(len(ids)) {
			t.Fatal("multi-page Unit coverage changed identities", count, err)
		}
	})
	func() {
		tx, err := s.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='unit' AND entity_id='zz-uncovered'")
		count, err := verifySyncUnitCoverage(t.Context(), tx)
		if !errors.Is(err, errSyncUnitUncovered) || count != 0 {
			t.Fatal("a late missing Unit returned partial coverage", count, err)
		}
	}()
	requireDomainState(t, s, before)
}

func TestSyncUnitCoverageRejectsInvalidIDs(t *testing.T) {
	for _, id := range []string{"", strings.Repeat("x", 129), strings.Repeat("家", 171), string([]byte{0xff})} {
		t.Run(fmt.Sprintf("bytes-%d", len(id)), func(t *testing.T) {
			s, catalog, _, token := catalogFixture(t)
			owner := createCatalogItem(t, catalog, token, ItemCreate{Name: "Invalid Unit owner"})
			execSQL(t, s.db, "INSERT INTO item_units(id,item_id,asset_code,created_at,updated_at) VALUES(?,?,'bad-id','2026','2026')", id, owner.ID)
			withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
				count, err := verifySyncUnitCoverage(t.Context(), tx)
				if !errors.Is(err, ErrDomainState) || count != 0 {
					t.Fatal("invalid Unit identity was skipped", count, err)
				}
			})
		})
	}
}

func TestSyncUnitCoverageRejectsInvalidContextsAndTransactions(t *testing.T) {
	s, _, _, _ := catalogFixture(t)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if count, err := verifySyncUnitCoverage(nil, tx); !errors.Is(err, ErrDomainState) || count != 0 {
		t.Fatal("nil context returned coverage", count, err)
	}
	if count, err := verifySyncUnitCoverage(t.Context(), nil); !errors.Is(err, ErrDomainState) || count != 0 {
		t.Fatal("nil transaction returned coverage", count, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if count, err := verifySyncUnitCoverage(ctx, tx); !errors.Is(err, context.Canceled) || count != 0 {
		t.Fatal("canceled context returned coverage", count, err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if count, err := verifySyncUnitCoverage(t.Context(), tx); !errors.Is(err, sql.ErrTxDone) || count != 0 {
		t.Fatal("closed transaction returned coverage", count, err)
	}
}
