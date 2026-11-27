package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
)

func TestSyncApprovedSearchCoverageIncludesArchivedItemsAndDisposedUnits(t *testing.T) {
	s, catalog, auth, token := catalogFixture(t)
	inventory, err := NewInventory(auth)
	if err != nil {
		t.Fatal(err)
	}
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Approved item"})
	unit, err := inventory.CreateUnit(t.Context(), token, "approved-coverage-unit", item.ID, item.Revision, UnitCreate{Notes: "Actual unit"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = inventory.SetUnitStatus(t.Context(), token, "approved-coverage-dispose", unit.ID, unit.Revision, UnitStatus{Status: "disposed", DisposalKind: testPointer("discarded")}); err != nil {
		t.Fatal(err)
	}
	item = getCatalogItem(t, catalog, token, item.ID)
	if _, err = catalog.Archive(t.Context(), token, "approved-coverage-archive", item.ID, item.Revision, ""); err != nil {
		t.Fatal(err)
	}
	execSQL(t, s.db, `INSERT INTO ai_review_entries(id,item_id,field_key,state,raw_value,created_at,updated_at)
 VALUES('approved-coverage-pending',?,'keyword','needs_review','PrivateUnreviewedText','2026','2026')`, item.ID)
	execSQL(t, s.db, "UPDATE search_documents SET title='Untrusted cached title' WHERE item_id=?", item.ID)
	execSQL(t, s.db, "UPDATE unit_search_documents SET title='Untrusted cached unit title' WHERE unit_id=?", unit.ID)
	before := domainState(t, s)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		coverage, err := verifySyncApprovedSearchCoverage(t.Context(), tx)
		if err != nil || coverage.Items != 1 || coverage.Units != 1 {
			t.Fatal("actual approved item/unit source was not covered", coverage, err)
		}
	})
	requireDomainState(t, s, before)
	status, err := auth.ReadSyncStatus(t.Context(), token)
	if err != nil || status.BootstrapReady || status.RetentionEnabled {
		t.Fatal("approved search coverage enabled public sync", err)
	}
}

func TestSyncApprovedSearchCoverageRejectsDamageWithoutPartialCounts(t *testing.T) {
	s, catalog, auth, token := catalogFixture(t)
	inventory, err := NewInventory(auth)
	if err != nil {
		t.Fatal(err)
	}
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Search damage source"})
	unit, err := inventory.CreateUnit(t.Context(), token, "approved-damage-unit", item.ID, item.Revision, UnitCreate{})
	if err != nil {
		t.Fatal(err)
	}
	itemSearch, unitSearch := projectionPair("item", item.ID), projectionPair("unit", unit.ID)
	execSQL(t, s.db, `INSERT INTO ai_review_entries(id,item_id,field_key,state,raw_value,created_at,updated_at)
 VALUES('approved-damage-pending',?,'keyword','needs_review','PrivateUnreviewedText','2026','2026')`, item.ID)
	before := domainState(t, s)
	for _, test := range []struct {
		name   string
		damage func(*testing.T, *sql.Tx)
		want   error
	}{
		{"missing item search", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='search_doc' AND entity_id=?", itemSearch)
		}, errSyncApprovedSearchUncovered},
		{"missing unit search after item", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='search_doc' AND entity_id=?", unitSearch)
		}, errSyncApprovedSearchUncovered},
		{"unreviewed item contamination", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE current_projections SET payload_json=json_set(payload_json,'$.approved_text',json_extract(payload_json,'$.approved_text')||char(10)||'PrivateUnreviewedText') WHERE entity_type='search_doc' AND entity_id=?", itemSearch)
		}, ErrDomainState},
		{"unreviewed unit contamination", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE current_projections SET payload_json=json_set(payload_json,'$.approved_text',json_extract(payload_json,'$.approved_text')||char(10)||'PrivateUnreviewedText') WHERE entity_type='search_doc' AND entity_id=?", unitSearch)
		}, ErrDomainState},
		{"new reviewed source missing from search", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, `INSERT INTO ai_review_entries(id,item_id,field_key,state,raw_value,reviewed_at,created_at,updated_at)
 VALUES('approved-damage-accepted',?,'keyword','accepted','NewReviewedText','2026','2026','2026')`, item.ID)
		}, ErrDomainState},
		{"unit counter missing", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='unit' AND entity_id=?", unit.ID)
			execSyncCoverageSQL(t, tx, "DELETE FROM projection_revisions WHERE entity_type='unit' AND entity_id=?", unit.ID)
		}, ErrDomainState},
		{"search counter mismatch", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE projection_revisions SET revision=revision+1 WHERE entity_type='search_doc' AND entity_id=?", unitSearch)
		}, ErrDomainState},
		{"orphan item search", func(t *testing.T, tx *sql.Tx) {
			seedOrphanSearchCurrent(t, tx, projectionPair("item", "missing-item"))
		}, ErrDomainState},
		{"orphan unit search", func(t *testing.T, tx *sql.Tx) {
			seedOrphanSearchCurrent(t, tx, projectionPair("unit", "missing-unit"))
		}, ErrDomainState},
		{"noncanonical search identity", func(t *testing.T, tx *sql.Tx) {
			seedOrphanSearchCurrent(t, tx, `[ "item","noncanonical"]`)
		}, ErrDomainState},
		{"unsupported search kind", func(t *testing.T, tx *sql.Tx) {
			seedOrphanSearchCurrent(t, tx, projectionPair("other", "unsupported"))
		}, ErrDomainState},
		{"oversized search identity", func(t *testing.T, tx *sql.Tx) {
			seedOrphanSearchCurrent(t, tx, strings.Repeat("x", maxSyncIdentityBytes+1))
		}, ErrDomainState},
		{"empty search identity", func(t *testing.T, tx *sql.Tx) {
			seedOrphanSearchCurrent(t, tx, "")
		}, ErrDomainState},
	} {
		t.Run(test.name, func(t *testing.T) {
			tx, err := s.db.BeginTx(t.Context(), nil)
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
			coverage, err := verifySyncApprovedSearchCoverage(t.Context(), tx)
			if !errors.Is(err, test.want) || coverage != (syncApprovedSearchCoverage{}) {
				t.Fatal("damaged approved search returned coverage", coverage, err)
			}
		})
	}
	requireDomainState(t, s, before)
}

func seedOrphanSearchCurrent(t *testing.T, tx *sql.Tx, identity string) {
	t.Helper()
	execSyncCoverageSQL(t, tx, "INSERT INTO projection_revisions VALUES('search_doc',?,1)", identity)
	execSyncCoverageSQL(t, tx, `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 VALUES('search_doc',?,1,'{}','2026')`, identity)
}

func TestSyncApprovedSearchCoverageEmptyAndExactPageBoundaries(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		coverage, err := verifySyncApprovedSearchCoverage(t.Context(), tx)
		if err != nil || coverage != (syncApprovedSearchCoverage{}) {
			t.Fatal("empty approved search coverage failed", coverage, err)
		}
	})
	owner := createCatalogItem(t, catalog, token, ItemCreate{Name: "Boundary search owner"})
	ids := make([]string, 0, 100)
	for index := 0; index < 100; index++ {
		ids = append(ids, fmt.Sprintf("boundary-%03d", index))
	}
	seedSyncUnitSearchCoverage(t, s, owner, ids)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		coverage, err := verifySyncApprovedSearchCoverage(t.Context(), tx)
		if err != nil || coverage.Items != 1 || coverage.Units != 100 {
			t.Fatal("exact-boundary unit source coverage failed", coverage, err)
		}
	})
	func() {
		tx, err := s.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		identity := projectionPair("unit", ids[99])
		execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='search_doc' AND entity_id=?", identity)
		execSyncCoverageSQL(t, tx, "DELETE FROM item_units WHERE id=?", ids[99])
		coverage, err := verifySyncApprovedSearchCoverage(t.Context(), tx)
		if err != nil || coverage.Items != 1 || coverage.Units != 99 {
			t.Fatal("exact-boundary current search coverage failed", coverage, err)
		}
	}()
}

func TestSyncApprovedSearchCoverageWithholdsOversizedCurrent(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Large search source"})
	before := domainState(t, s)
	func() {
		tx, err := s.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		oversized := `{"data":"` + strings.Repeat("x", maxSyncPageBytes) + `"}`
		execSyncCoverageSQL(t, tx, "UPDATE current_projections SET payload_json=? WHERE entity_type='search_doc' AND entity_id=?", oversized, projectionPair("item", item.ID))
		coverage, err := verifySyncApprovedSearchCoverage(t.Context(), tx)
		if !errors.Is(err, errSyncPayloadTooLarge) || coverage != (syncApprovedSearchCoverage{}) {
			t.Fatal("oversized approved search returned coverage", coverage, err)
		}
	}()
	requireDomainState(t, s, before)
}

func TestSyncApprovedSearchCoveragePagesSourceAndCanonicalIdentities(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	owner := createCatalogItem(t, catalog, token, ItemCreate{Name: "Search owner"})
	ids := make([]string, 0, 209)
	for index := 0; index < 205; index++ {
		ids = append(ids, fmt.Sprintf("unit-%03d", index))
	}
	ids = append(ids, "unit-098\x00-家庭", "unit-197\x00-第二", strings.Repeat("\x00", 128), "zz-uncovered")
	seedSyncUnitSearchCoverage(t, s, owner, ids)
	before := domainState(t, s)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		coverage, err := verifySyncApprovedSearchCoverage(t.Context(), tx)
		if err != nil || coverage.Items != 1 || coverage.Units != int64(len(ids)) {
			t.Fatal("multi-page approved source coverage changed identities", coverage, err)
		}
		identities := make([]string, 0, len(ids)+1)
		identities = append(identities, projectionPair("item", owner.ID))
		for _, id := range ids {
			identities = append(identities, projectionPair("unit", id))
		}
		sort.Strings(identities)
		first, err := readSyncPairProjectionIDs(t.Context(), tx, "search_doc", "", false)
		if err != nil || len(first) != 100 || first[99] != identities[99] {
			t.Fatal("first canonical search page changed its boundary", first, err)
		}
		second, err := readSyncPairProjectionIDs(t.Context(), tx, "search_doc", first[99], true)
		if err != nil || len(second) != 100 || second[99] != identities[199] {
			t.Fatal("second canonical search page changed its boundary", second, err)
		}
	})
	func() {
		tx, err := s.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='search_doc' AND entity_id=?", projectionPair("unit", "zz-uncovered"))
		coverage, err := verifySyncApprovedSearchCoverage(t.Context(), tx)
		if !errors.Is(err, errSyncApprovedSearchUncovered) || coverage != (syncApprovedSearchCoverage{}) {
			t.Fatal("late missing unit search returned a partial count", coverage, err)
		}
	}()
	requireDomainState(t, s, before)
}

func seedSyncUnitSearchCoverage(t *testing.T, s *Store, owner Item, ids []string) {
	t.Helper()
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for index, id := range ids {
		code := fmt.Sprintf("ASSET-%03d", index)
		execSyncCoverageSQL(t, tx, "INSERT INTO item_units(id,item_id,asset_code,created_at,updated_at) VALUES(?,?,?,'2026','2026')", id, owner.ID, code)
		execSyncCoverageSQL(t, tx, "INSERT INTO projection_revisions VALUES('unit',?,1)", id)
		identity := projectionPair("unit", id)
		execSyncCoverageSQL(t, tx, "INSERT INTO projection_revisions VALUES('search_doc',?,1)", identity)
		execSyncCoverageSQL(t, tx, `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 VALUES('search_doc',?,1,json_object('entity_type','unit','entity_id',?,'approved_text',?),'2026')`, identity, id, owner.Name+"\n"+code)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestSyncApprovedSearchCoverageRejectsInvalidUnitIDs(t *testing.T) {
	for _, id := range []string{"", strings.Repeat("x", 129), strings.Repeat("家", 171), string([]byte{0xff})} {
		t.Run(fmt.Sprintf("bytes-%d", len(id)), func(t *testing.T) {
			s, catalog, _, token := catalogFixture(t)
			owner := createCatalogItem(t, catalog, token, ItemCreate{Name: "Invalid unit owner"})
			execSQL(t, s.db, "INSERT INTO item_units(id,item_id,asset_code,created_at,updated_at) VALUES(?,?,'bad-id','2026','2026')", id, owner.ID)
			withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
				coverage, err := verifySyncApprovedSearchCoverage(t.Context(), tx)
				if !errors.Is(err, ErrDomainState) || coverage != (syncApprovedSearchCoverage{}) {
					t.Fatal("invalid unit source identity was skipped", coverage, err)
				}
			})
		})
	}
}

func TestSyncApprovedSearchCoverageRejectsInvalidContextAndTransactions(t *testing.T) {
	s, _, _, _ := catalogFixture(t)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := verifySyncApprovedSearchCoverage(nil, tx); !errors.Is(err, ErrDomainState) || result != (syncApprovedSearchCoverage{}) {
		t.Fatal("nil context returned coverage", result, err)
	}
	if result, err := verifySyncApprovedSearchCoverage(t.Context(), nil); !errors.Is(err, ErrDomainState) || result != (syncApprovedSearchCoverage{}) {
		t.Fatal("nil transaction returned coverage", result, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if result, err := verifySyncApprovedSearchCoverage(ctx, tx); !errors.Is(err, context.Canceled) || result != (syncApprovedSearchCoverage{}) {
		t.Fatal("canceled context returned coverage", result, err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if result, err := verifySyncApprovedSearchCoverage(t.Context(), tx); !errors.Is(err, sql.ErrTxDone) || result != (syncApprovedSearchCoverage{}) {
		t.Fatal("closed transaction returned coverage", result, err)
	}
}
