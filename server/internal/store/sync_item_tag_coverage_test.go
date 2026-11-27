package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestSyncItemTagCoveragePreservesIndependentMemberships(t *testing.T) {
	s, catalog, auth, token := catalogFixture(t)
	taxonomy, err := NewTaxonomy(auth)
	if err != nil {
		t.Fatal(err)
	}
	first := createCatalogItem(t, catalog, token, ItemCreate{Name: "Membership first"})
	second := createCatalogItem(t, catalog, token, ItemCreate{Name: "Membership second"})
	shared := createTag(t, taxonomy, token, "Shared tag")
	removed := createTag(t, taxonomy, token, "Removed tag")
	first, err = catalog.ReplaceTags(t.Context(), token, "membership-first", first.ID, first.Revision, []string{shared.ID, removed.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = catalog.ReplaceTags(t.Context(), token, "membership-second", second.ID, second.Revision, []string{shared.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err = catalog.ReplaceTags(t.Context(), token, "membership-remove", first.ID, first.Revision, []string{shared.ID}); err != nil {
		t.Fatal(err)
	}
	before := domainState(t, s)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		count, err := verifySyncItemTagCoverage(t.Context(), tx)
		if err != nil || count != 2 {
			t.Fatal("shared Tag memberships were not independently covered", count, err)
		}
	})
	requireDomainState(t, s, before)
	removedID := projectionPair(first.ID, removed.ID)
	if scalar[int](t, s.db, "SELECT revision FROM projection_revisions WHERE entity_type='item_tag' AND entity_id=?", removedID) != 2 ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_changes WHERE entity_type='item_tag' AND entity_id=? AND operation='delete'", removedID) != 1 {
		t.Fatal("removed membership lost its independent counter or tombstone")
	}
	status, err := auth.ReadSyncStatus(t.Context(), token)
	if err != nil || status.BootstrapReady || status.RetentionEnabled {
		t.Fatal("Item/Tag coverage enabled public synchronization", err)
	}
}

func TestSyncItemTagCoverageRejectsDamageWithoutPartialCounts(t *testing.T) {
	s, catalog, auth, token := catalogFixture(t)
	taxonomy, err := NewTaxonomy(auth)
	if err != nil {
		t.Fatal(err)
	}
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Membership damage"})
	tag := createTag(t, taxonomy, token, "Damage tag")
	if _, err = catalog.ReplaceTags(t.Context(), token, "membership-damage-add", item.ID, item.Revision, []string{tag.ID}); err != nil {
		t.Fatal(err)
	}
	identity := projectionPair(item.ID, tag.ID)
	before := domainState(t, s)
	for _, test := range []struct {
		name   string
		damage func(*testing.T, *sql.Tx)
		want   error
	}{
		{"missing current", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='item_tag' AND entity_id=?", identity)
		}, errSyncItemTagUncovered},
		{"changed current", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE current_projections SET payload_json=json_set(payload_json,'$.tag_id','wrong') WHERE entity_type='item_tag' AND entity_id=?", identity)
		}, ErrDomainState},
		{"counter mismatch", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE projection_revisions SET revision=revision+1 WHERE entity_type='item_tag' AND entity_id=?", identity)
		}, ErrDomainState},
		{"counter missing", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='item_tag' AND entity_id=?", identity)
			execSyncCoverageSQL(t, tx, "DELETE FROM projection_revisions WHERE entity_type='item_tag' AND entity_id=?", identity)
		}, ErrDomainState},
		{"invalid counter", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE projection_revisions SET revision=? WHERE entity_type='item_tag' AND entity_id=?", maxSafeInteger+1, identity)
		}, ErrDomainState},
		{"missing item source", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "PRAGMA defer_foreign_keys=ON")
			execSyncCoverageSQL(t, tx, "UPDATE item_tags SET item_id='missing-item' WHERE item_id=? AND tag_id=?", item.ID, tag.ID)
		}, ErrDomainState},
		{"missing tag source", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "PRAGMA defer_foreign_keys=ON")
			execSyncCoverageSQL(t, tx, "UPDATE item_tags SET tag_id='missing-tag' WHERE item_id=? AND tag_id=?", item.ID, tag.ID)
		}, ErrDomainState},
		{"invalid item source", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE items SET name='' WHERE id=?", item.ID)
		}, ErrDomainState},
		{"item counter missing", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='item' AND entity_id=?", item.ID)
			execSyncCoverageSQL(t, tx, "DELETE FROM projection_revisions WHERE entity_type='item' AND entity_id=?", item.ID)
		}, ErrDomainState},
		{"tag counter missing", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='tag' AND entity_id=?", tag.ID)
			execSyncCoverageSQL(t, tx, "DELETE FROM projection_revisions WHERE entity_type='tag' AND entity_id=?", tag.ID)
		}, ErrDomainState},
		{"invalid tag source", func(t *testing.T, tx *sql.Tx) {
			execSyncCoverageSQL(t, tx, "UPDATE tags SET name='' WHERE id=?", tag.ID)
		}, ErrDomainState},
		{"orphan current", func(t *testing.T, tx *sql.Tx) {
			seedOrphanItemTagCurrent(t, tx, projectionPair(item.ID, "missing-tag"))
		}, ErrDomainState},
		{"noncanonical pair", func(t *testing.T, tx *sql.Tx) {
			seedOrphanItemTagCurrent(t, tx, `[ "item","tag"]`)
		}, ErrDomainState},
		{"oversized pair", func(t *testing.T, tx *sql.Tx) {
			seedOrphanItemTagCurrent(t, tx, strings.Repeat("x", maxSyncIdentityBytes+1))
		}, ErrDomainState},
		{"empty pair", func(t *testing.T, tx *sql.Tx) {
			seedOrphanItemTagCurrent(t, tx, "")
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
			count, err := verifySyncItemTagCoverage(t.Context(), tx)
			if !errors.Is(err, test.want) || count != 0 {
				t.Fatal("damaged membership returned coverage", count, err)
			}
		})
	}
	requireDomainState(t, s, before)
}

func seedOrphanItemTagCurrent(t *testing.T, tx *sql.Tx, identity string) {
	t.Helper()
	execSyncCoverageSQL(t, tx, "INSERT INTO projection_revisions VALUES('item_tag',?,1)", identity)
	execSyncCoverageSQL(t, tx, `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 VALUES('item_tag',?,1,'{}','2026')`, identity)
}

func TestSyncItemTagCoveragePagesExactCompositeIDsAndWithholdsLateMissingRow(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	owner := createCatalogItem(t, catalog, token, ItemCreate{Name: "Membership owner"})
	ids := make([]string, 0, 209)
	for index := 0; index < 205; index++ {
		ids = append(ids, fmt.Sprintf("tag-%03d", index))
	}
	ids = append(ids, "tag-098\x00-家庭", "tag-197\x00-第二", strings.Repeat("😀", 128), "zz-uncovered")
	seedSyncTaxonomyCoverage(t, s, ids)
	seedSyncItemTagCoverage(t, s, owner.ID, ids)
	before := domainState(t, s)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		first, err := readSyncItemPairSources(t.Context(), tx, "item_tag", "", "", false)
		if err != nil || len(first) != 100 || first[99].RelatedID != "tag-098\x00-家庭" {
			t.Fatal("first membership page changed its embedded-NUL boundary", first, err)
		}
		second, err := readSyncItemPairSources(t.Context(), tx, "item_tag", first[99].ItemID, first[99].RelatedID, true)
		if err != nil || len(second) != 100 || second[99].RelatedID != "tag-197\x00-第二" {
			t.Fatal("second membership page changed its embedded-NUL boundary", second, err)
		}
		count, err := verifySyncItemTagCoverage(t.Context(), tx)
		if err != nil || count != int64(len(ids)) {
			t.Fatal("multi-page membership coverage changed identities", count, err)
		}
	})
	func() {
		tx, err := s.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		execSyncCoverageSQL(t, tx, "DELETE FROM current_projections WHERE entity_type='item_tag' AND entity_id=?", projectionPair(owner.ID, "zz-uncovered"))
		count, err := verifySyncItemTagCoverage(t.Context(), tx)
		if !errors.Is(err, errSyncItemTagUncovered) || count != 0 {
			t.Fatal("late missing membership returned partial coverage", count, err)
		}
	}()
	requireDomainState(t, s, before)
}

func seedSyncItemTagCoverage(t *testing.T, s *Store, itemID string, tagIDs []string) {
	t.Helper()
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, tagID := range tagIDs {
		identity := projectionPair(itemID, tagID)
		execSyncCoverageSQL(t, tx, "INSERT INTO item_tags(item_id,tag_id) VALUES(?,?)", itemID, tagID)
		execSyncCoverageSQL(t, tx, "INSERT INTO projection_revisions VALUES('item_tag',?,1)", identity)
		execSyncCoverageSQL(t, tx, `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 VALUES('item_tag',?,1,json_object('item_id',?,'tag_id',?),'2026')`, identity, itemID, tagID)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestSyncItemTagCoverageAcceptsMaximumEscapedCompositeIdentity(t *testing.T) {
	s, _, _, _ := catalogFixture(t)
	id := strings.Repeat("\x00", 128)
	execSQL(t, s.db, `INSERT INTO items(id,name,tracking_mode,created_at,updated_at) VALUES(?,'Maximum membership owner','individual','2026','2026')`, id)
	execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('item',?,1)", id)
	execSQL(t, s.db, "INSERT INTO tags(id,name) VALUES(?,'Maximum tag')", id)
	execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('tag',?,1)", id)
	seedSyncItemTagCoverage(t, s, id, []string{id})
	if len(projectionPair(id, id)) != maxSyncIdentityBytes {
		t.Fatal("fixture did not exercise the maximum escaped composite identity")
	}
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		count, err := verifySyncItemTagCoverage(t.Context(), tx)
		if err != nil || count != 1 {
			t.Fatal("maximum escaped membership pair was rejected", count, err)
		}
	})
}

func TestSyncItemTagCoverageEmptyAndExactPageBoundary(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	owner := createCatalogItem(t, catalog, token, ItemCreate{Name: "Membership boundary owner"})
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		if count, err := verifySyncItemTagCoverage(t.Context(), tx); err != nil || count != 0 {
			t.Fatal("empty membership coverage failed", count, err)
		}
	})
	ids := make([]string, 0, 100)
	for index := 0; index < 100; index++ {
		ids = append(ids, fmt.Sprintf("tag-boundary-%03d", index))
	}
	seedSyncTaxonomyCoverage(t, s, ids)
	seedSyncItemTagCoverage(t, s, owner.ID, ids)
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		if count, err := verifySyncItemTagCoverage(t.Context(), tx); err != nil || count != 100 {
			t.Fatal("exact-boundary membership coverage failed", count, err)
		}
	})
}

func TestSyncItemTagCoveragePagesAcrossItemBoundary(t *testing.T) {
	s, _, _, _ := catalogFixture(t)
	for _, id := range []string{"item-a", "item-b"} {
		execSQL(t, s.db, `INSERT INTO items(id,name,tracking_mode,created_at,updated_at) VALUES(?,?,'individual','2026','2026')`, id, id)
		execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('item',?,1)", id)
	}
	ids := make([]string, 0, 100)
	for index := 0; index < 100; index++ {
		ids = append(ids, fmt.Sprintf("tag-%03d", index))
	}
	seedSyncTaxonomyCoverage(t, s, ids)
	seedSyncItemTagCoverage(t, s, "item-a", ids)
	seedSyncItemTagCoverage(t, s, "item-b", ids[:1])
	withQueryOnlySearchTx(t, s, func(tx *sql.Tx) {
		first, err := readSyncItemPairSources(t.Context(), tx, "item_tag", "", "", false)
		if err != nil || len(first) != 100 || first[99] != (syncItemPair{ItemID: "item-a", RelatedID: "tag-099"}) {
			t.Fatal("first membership page changed its Item boundary", first, err)
		}
		second, err := readSyncItemPairSources(t.Context(), tx, "item_tag", first[99].ItemID, first[99].RelatedID, true)
		if err != nil || len(second) != 1 || second[0] != (syncItemPair{ItemID: "item-b", RelatedID: "tag-000"}) {
			t.Fatal("second membership page changed its Item boundary", second, err)
		}
		if count, err := verifySyncItemTagCoverage(t.Context(), tx); err != nil || count != 101 {
			t.Fatal("cross-Item membership coverage failed", count, err)
		}
	})
}

func TestSyncItemTagCoverageWithholdsOversizedCurrent(t *testing.T) {
	s, catalog, auth, token := catalogFixture(t)
	taxonomy, err := NewTaxonomy(auth)
	if err != nil {
		t.Fatal(err)
	}
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Large membership"})
	tag := createTag(t, taxonomy, token, "Large membership tag")
	if _, err = catalog.ReplaceTags(t.Context(), token, "membership-large-add", item.ID, item.Revision, []string{tag.ID}); err != nil {
		t.Fatal(err)
	}
	before := domainState(t, s)
	func() {
		tx, err := s.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		oversized := `{"data":"` + strings.Repeat("x", maxSyncPageBytes) + `"}`
		execSyncCoverageSQL(t, tx, "UPDATE current_projections SET payload_json=? WHERE entity_type='item_tag' AND entity_id=?", oversized, projectionPair(item.ID, tag.ID))
		if count, err := verifySyncItemTagCoverage(t.Context(), tx); !errors.Is(err, errSyncPayloadTooLarge) || count != 0 {
			t.Fatal("oversized membership projection returned coverage", count, err)
		}
	}()
	requireDomainState(t, s, before)
}

func TestSyncItemTagCoverageRejectsInvalidSourcePairs(t *testing.T) {
	s, catalog, auth, token := catalogFixture(t)
	taxonomy, err := NewTaxonomy(auth)
	if err != nil {
		t.Fatal(err)
	}
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Invalid membership"})
	tag := createTag(t, taxonomy, token, "Invalid membership tag")
	if _, err = catalog.ReplaceTags(t.Context(), token, "membership-invalid-add", item.ID, item.Revision, []string{tag.ID}); err != nil {
		t.Fatal(err)
	}
	before := domainState(t, s)
	for _, field := range []string{"item_id", "tag_id"} {
		for _, id := range []string{"", strings.Repeat("x", 129), strings.Repeat("家", 171), string([]byte{0xff})} {
			t.Run(fmt.Sprintf("%s-bytes-%d", field, len(id)), func(t *testing.T) {
				tx, err := s.db.BeginTx(t.Context(), nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				execSyncCoverageSQL(t, tx, "PRAGMA defer_foreign_keys=ON")
				execSyncCoverageSQL(t, tx, "UPDATE item_tags SET "+field+"=? WHERE item_id=? AND tag_id=?", id, item.ID, tag.ID)
				if count, err := verifySyncItemTagCoverage(t.Context(), tx); !errors.Is(err, ErrDomainState) || count != 0 {
					t.Fatal("invalid membership source identity was skipped", count, err)
				}
			})
		}
	}
	requireDomainState(t, s, before)
}

func TestSyncItemTagCoverageRejectsInvalidContextsAndTransactions(t *testing.T) {
	s, _, _, _ := catalogFixture(t)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if pairs, err := readSyncItemPairSources(t.Context(), tx, "item_tag;DROP TABLE items", "", "", false); !errors.Is(err, ErrDomainState) || pairs != nil {
		t.Fatal("unsupported source kind was queried", pairs, err)
	}
	if count, err := verifySyncItemTagCoverage(nil, tx); !errors.Is(err, ErrDomainState) || count != 0 {
		t.Fatal("nil context returned coverage", count, err)
	}
	if count, err := verifySyncItemTagCoverage(t.Context(), nil); !errors.Is(err, ErrDomainState) || count != 0 {
		t.Fatal("nil transaction returned coverage", count, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if count, err := verifySyncItemTagCoverage(ctx, tx); !errors.Is(err, context.Canceled) || count != 0 {
		t.Fatal("canceled context returned coverage", count, err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if count, err := verifySyncItemTagCoverage(t.Context(), tx); !errors.Is(err, sql.ErrTxDone) || count != 0 {
		t.Fatal("closed transaction returned coverage", count, err)
	}
}
