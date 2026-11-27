package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestSyncItemCoverageIncludesArchivedItemsAndApprovedSearch(t *testing.T) {
	s, catalog, auth, token := catalogFixture(t)
	taxonomy, err := NewTaxonomy(auth)
	if err != nil {
		t.Fatal(err)
	}
	category := createCategory(t, taxonomy, token, "ITEM-COVERAGE-CATEGORY", nil)
	active := createCatalogItem(t, catalog, token, ItemCreate{Name: "Active reviewed item", CategoryID: &category.ID})
	archived := createCatalogItem(t, catalog, token, ItemCreate{Name: "Archived item"})
	if _, err = catalog.Archive(t.Context(), token, "item-coverage-archive", archived.ID, archived.Revision, ""); err != nil {
		t.Fatal(err)
	}
	removed := createCatalogItem(t, catalog, token, ItemCreate{Name: "Removed item"})
	if _, err = catalog.Delete(t.Context(), token, "item-coverage-delete", removed.ID, removed.Revision); err != nil {
		t.Fatal(err)
	}
	// Unreviewed text remains absent from the current approved search projection.
	execSQL(t, s.db, `INSERT INTO ai_review_entries(id,item_id,field_key,state,raw_value,created_at,updated_at)
 VALUES('coverage-pending',?,'keyword','needs_review','UnreviewedPrivateText','2026','2026')`, active.ID)
	before := domainState(t, s)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		count, err := verifySyncItemCoverage(t.Context(), tx)
		if err != nil || count != 2 {
			t.Fatal("actual item and approved-search coverage was not established", count, err)
		}
	})
	requireDomainState(t, s, before)
	status, err := auth.ReadSyncStatus(t.Context(), token)
	if err != nil || status.BootstrapReady || status.RetentionEnabled {
		t.Fatal("item-only coverage enabled public synchronization", err)
	}
}

func TestSyncItemCoverageRejectsDamageWithoutPartialCounts(t *testing.T) {
	s, catalog, auth, token := catalogFixture(t)
	taxonomy, err := NewTaxonomy(auth)
	if err != nil {
		t.Fatal(err)
	}
	category := createCategory(t, taxonomy, token, "ITEM-DAMAGE-CATEGORY", nil)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Item damage source", CategoryID: &category.ID})
	searchID := projectionPair("item", item.ID)
	execSQL(t, s.db, `INSERT INTO ai_review_entries(id,item_id,field_key,state,raw_value,created_at,updated_at)
 VALUES('coverage-pending',?,'keyword','needs_review','UnreviewedPrivateText','2026','2026')`, item.ID)
	before := domainState(t, s)
	for _, test := range []struct {
		name   string
		damage func(*testing.T, *sql.Tx)
		want   error
	}{
		{"missing item current", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='item' AND entity_id=?", item.ID)
		}, errSyncItemUncovered},
		{"missing search current", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='search_doc' AND entity_id=?", searchID)
		}, errSyncItemSearchUncovered},
		{"changed current item", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE current_projections SET payload_json=json_set(payload_json,'$.name','wrong') WHERE entity_type='item' AND entity_id=?", item.ID)
		}, ErrDomainState},
		{"changed source", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE items SET name='changed source' WHERE id=?", item.ID)
		}, ErrDomainState},
		{"unreviewed search contamination", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE current_projections SET payload_json=json_set(payload_json,'$.approved_text',json_extract(payload_json,'$.approved_text')||char(10)||'UnreviewedPrivateText') WHERE entity_type='search_doc' AND entity_id=?", searchID)
		}, ErrDomainState},
		{"missing item counter", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='item' AND entity_id=?", item.ID)
			execSyncCoverageSQL(t, tx, "DELETE FROM projection_revisions WHERE entity_type='item' AND entity_id=?", item.ID)
		}, ErrDomainState},
		{"search counter differs", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE projection_revisions SET revision=revision+1 WHERE entity_type='search_doc' AND entity_id=?", searchID)
		}, ErrDomainState},
		{"search counter missing despite history", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='search_doc' AND entity_id=?", searchID)
			execSyncCoverageSQL(t, tx, "DELETE FROM projection_revisions WHERE entity_type='search_doc' AND entity_id=?", searchID)
		}, ErrDomainState},
		{"search history ahead of counter", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, `INSERT INTO sync_changes(entity_type,entity_id,operation,entity_revision,projection_json,created_at)
 SELECT 'search_doc',entity_id,'upsert',entity_revision+1,payload_json,'2026' FROM current_projections WHERE entity_type='search_doc' AND entity_id=?`, searchID)
		}, ErrDomainState},
		{"missing category", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "PRAGMA defer_foreign_keys=ON")
			execSyncCoverageSQL(t, tx, "UPDATE items SET category_id='missing-category' WHERE id=?", item.ID)
			execSyncCoverageSQL(t, tx, "UPDATE current_projections SET payload_json=json_set(payload_json,'$.category_id','missing-category') WHERE entity_type='item' AND entity_id=?", item.ID)
		}, ErrDomainState},
		{"category source differs", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE categories SET name='changed category' WHERE id=?", category.ID)
		}, ErrDomainState},
		{"orphan item current", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "INSERT INTO projection_revisions VALUES('item','orphan-item',1)")
			execSyncCoverageSQL(t, tx, `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at) VALUES('item','orphan-item',1,'{"id":"orphan-item"}','2026')`)
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
			count, err := verifySyncItemCoverage(t.Context(), tx)
			if !errors.Is(err, test.want) || count != 0 {
				t.Fatal("damaged item coverage returned a count", count, err)
			}
		})
	}
	requireDomainState(t, s, before)
}

func TestSyncItemCoverageEmptyAndExactPageBoundary(t *testing.T) {
	s, _, _, _ := catalogFixture(t)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		count, err := verifySyncItemCoverage(t.Context(), tx)
		if err != nil || count != 0 {
			t.Fatal("empty item coverage failed", count, err)
		}
	})
	ids := make([]string, 0, 100)
	for index := 0; index < 100; index++ {
		ids = append(ids, fmt.Sprintf("boundary-%03d", index))
	}
	seedSyncItemCoverage(t, s, ids)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		count, err := verifySyncItemCoverage(t.Context(), tx)
		if err != nil || count != 100 {
			t.Fatal("exact-boundary item coverage failed", count, err)
		}
	})
}

func TestSyncItemCoverageWithholdsOversizedCurrent(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Large item source"})
	before := domainState(t, s)
	oversized := `{"data":"` + strings.Repeat("x", maxSyncPageBytes) + `"}`
	for _, target := range []struct{ kind, id string }{{"item", item.ID}, {"search_doc", projectionPair("item", item.ID)}} {
		t.Run(target.kind, func(t *testing.T) {
			tx, err := s.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			execSyncCoverageSQL(t, tx, "UPDATE current_projections SET payload_json=? WHERE entity_type=? AND entity_id=?", oversized, target.kind, target.id)
			count, err := verifySyncItemCoverage(t.Context(), tx)
			if !errors.Is(err, errSyncPayloadTooLarge) || count != 0 {
				t.Fatal("an oversized item or search projection returned coverage", count, err)
			}
		})
	}
	requireDomainState(t, s, before)
}

func TestSyncItemCoveragePagesExactIDsAndWithholdsLateMissingSearch(t *testing.T) {
	s, _, _, _ := catalogFixture(t)
	ids := make([]string, 0, 209)
	for index := 0; index < 205; index++ {
		ids = append(ids, fmt.Sprintf("item-%03d", index))
	}
	ids = append(ids, "item-098\x00-家庭", "item-197\x00-第二", "zz-uncovered", strings.Repeat("😀", 128))
	seedSyncItemCoverage(t, s, ids)
	before := domainState(t, s)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		first, err := readSyncCoverageIDs(t.Context(), tx, "item", "", false)
		if err != nil || len(first) != 100 || first[99] != "item-098\x00-家庭" {
			t.Fatal("the first item page changed its embedded-NUL boundary", first, err)
		}
		second, err := readSyncCoverageIDs(t.Context(), tx, "item", first[99], true)
		if err != nil || len(second) != 100 || second[99] != "item-197\x00-第二" {
			t.Fatal("the second item page changed its embedded-NUL boundary", second, err)
		}
		count, err := verifySyncItemCoverage(t.Context(), tx)
		if err != nil || count != int64(len(ids)) {
			t.Fatal("multi-page item coverage changed identities", count, err)
		}
	})
	func() {
		tx, err := s.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='search_doc' AND entity_id=?", projectionPair("item", "zz-uncovered"))
		count, err := verifySyncItemCoverage(t.Context(), tx)
		if !errors.Is(err, errSyncItemSearchUncovered) || count != 0 {
			t.Fatal("a late missing search returned partial coverage", count, err)
		}
	}()
	requireDomainState(t, s, before)
}

func seedSyncItemCoverage(t *testing.T, s *Store, ids []string) {
	t.Helper()
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for index, id := range ids {
		name := fmt.Sprintf("coverage-item-%03d", index)
		execSyncCoverageSQL(t, tx, `INSERT INTO items(id,name,tracking_mode,unit_code,base_unit,scale_factor,status,created_at,updated_at)
 VALUES(?,?,'individual','pcs','pcs',1,'active','2026','2026')`, id, name)
		execSyncCoverageSQL(t, tx, "INSERT INTO projection_revisions VALUES('item',?,1)", id)
		execSyncCoverageSQL(t, tx, `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 SELECT 'item',id,1,json_object('id',id,'name',name,'tracking_mode',tracking_mode,'unit_code',unit_code,'category_id',category_id,
 'brand',brand,'model',model,'description',description,'notes',notes,'base_unit',base_unit,'scale_factor',scale_factor,'status',status),'2026'
 FROM items WHERE id=?`, id)
		searchID := projectionPair("item", id)
		execSyncCoverageSQL(t, tx, "INSERT INTO projection_revisions VALUES('search_doc',?,1)", searchID)
		execSyncCoverageSQL(t, tx, `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 VALUES('search_doc',?,1,json_object('entity_type','item','entity_id',?,'approved_text',?),'2026')`, searchID, id, name)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestSyncItemCoverageRejectsInvalidIDsAndTransactions(t *testing.T) {
	for _, id := range []string{"", strings.Repeat("x", 129), strings.Repeat("家", 171), string([]byte{0xff})} {
		t.Run(fmt.Sprintf("bytes-%d", len(id)), func(t *testing.T) {
			s, _, _, _ := catalogFixture(t)
			execSQL(t, s.db, `INSERT INTO items(id,name,tracking_mode,unit_code,base_unit,scale_factor,status,created_at,updated_at)
 VALUES(?,'invalid identity','individual','pcs','pcs',1,'active','2026','2026')`, id)
			withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
				count, err := verifySyncItemCoverage(t.Context(), tx)
				if !errors.Is(err, ErrDomainState) || count != 0 {
					t.Fatal("invalid item identity was skipped", count, err)
				}
			})
		})
	}
	s, _, _, _ := catalogFixture(t)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if count, err := verifySyncItemCoverage(nil, tx); !errors.Is(err, ErrDomainState) || count != 0 {
		t.Fatal("nil context returned coverage", count, err)
	}
	if count, err := verifySyncItemCoverage(t.Context(), nil); !errors.Is(err, ErrDomainState) || count != 0 {
		t.Fatal("nil transaction returned coverage", count, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if count, err := verifySyncItemCoverage(ctx, tx); !errors.Is(err, context.Canceled) || count != 0 {
		t.Fatal("canceled context returned coverage", count, err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if count, err := verifySyncItemCoverage(t.Context(), tx); !errors.Is(err, sql.ErrTxDone) || count != 0 {
		t.Fatal("closed transaction returned coverage", count, err)
	}
}
