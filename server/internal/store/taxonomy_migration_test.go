package store

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTaxonomyProjectionBackfillPreservesSourceCountersAndCanonicalLinks(t *testing.T) {
	path, old := legacyFixture(t, 2)
	categoryID, tagID := "category:<&\"\\ ", "標籤:<& "
	linkID := projectionPair("item-1", tagID)
	searchID := projectionPair("item", "item-1")
	execSQL(t, old, "INSERT INTO categories(id,name) VALUES(?,'分類 😀')", categoryID)
	execSQL(t, old, "UPDATE items SET category_id=? WHERE id='item-1'", categoryID)
	execSQL(t, old, "INSERT INTO tags(id,name) VALUES(?,'  保留標籤  '),('other-tag','Other')", tagID)
	execSQL(t, old, "INSERT INTO item_tags VALUES('item-1',?),('item-1','other-tag')", tagID)
	execSQL(t, old, "INSERT INTO search_documents(item_id,title,reviewed_ai,revision,updated_at) VALUES('item-1','Cable','unverified legacy cache',8,'2026')")
	for _, row := range []struct {
		kind, id string
		revision int
	}{{"category", categoryID, 9}, {"tag", tagID, 11}, {"item_tag", linkID, 4}, {"item", "item-1", 7}, {"search_doc", searchID, 9}} {
		execSQL(t, old, "INSERT INTO projection_revisions VALUES(?,?,?)", row.kind, row.id, row.revision)
	}
	_ = old.Close()
	s, err := Open(t.Context(), Config{Path: path, Migrate: true, AllowLegacy: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, row := range []struct {
		kind, id string
		revision int
	}{{"category", categoryID, 9}, {"tag", tagID, 11}, {"item_tag", linkID, 4}, {"tag", "other-tag", 1}} {
		if scalar[int](t, s.db, "SELECT entity_revision FROM current_projections WHERE entity_type=? AND entity_id=?", row.kind, row.id) != row.revision {
			t.Fatal("taxonomy backfill replaced identity or counter", row.kind)
		}
	}
	var category struct {
		ID       string  `json:"id"`
		ParentID *string `json:"parent_id"`
		Name     string  `json:"name"`
	}
	encoded := scalar[string](t, s.db, "SELECT payload_json FROM current_projections WHERE entity_type='category'")
	if json.Unmarshal([]byte(encoded), &category) != nil || category.ID != categoryID || category.ParentID != nil || category.Name != "分類 😀" {
		t.Fatal("category backfill changed source metadata")
	}
	var link struct {
		ItemID string `json:"item_id"`
		TagID  string `json:"tag_id"`
	}
	encoded = scalar[string](t, s.db, "SELECT payload_json FROM current_projections WHERE entity_type='item_tag' AND entity_id=?", linkID)
	if json.Unmarshal([]byte(encoded), &link) != nil || link.ItemID != "item-1" || link.TagID != tagID {
		t.Fatal("link backfill was not a canonical independent projection")
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM current_projections WHERE entity_type IN('item','search_doc')") != 0 ||
		scalar[int](t, s.db, "SELECT revision FROM projection_revisions WHERE entity_type='item' AND entity_id='item-1'") != 7 ||
		scalar[int](t, s.db, "SELECT revision FROM projection_revisions WHERE entity_type='search_doc' AND entity_id=?", searchID) != 9 ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_changes") != 0 ||
		scalar[int](t, s.db, "SELECT quantity_base FROM stock_balances") != 1500 ||
		scalar[int](t, s.db, "SELECT SUM(revision) FROM data_revision") != 0 {
		t.Fatal("partial backfill published unverified search, reset counters, rescaled stock, or pretended to enable sync")
	}
	for _, fixture := range []struct{ query, index string }{
		{"SELECT id FROM categories WHERE parent_id='parent'", "idx_categories_parent"},
		{"SELECT item_id FROM item_tags WHERE tag_id='tag'", "idx_item_tags_tag"},
	} {
		var id, parent, unused int
		var detail string
		if err := s.db.QueryRowContext(t.Context(), "EXPLAIN QUERY PLAN "+fixture.query).Scan(&id, &parent, &unused, &detail); err != nil || !strings.Contains(detail, fixture.index) {
			t.Fatal("taxonomy lookup lost its required index", detail, err)
		}
	}
}

func TestTaxonomyUnsafeBackfillRollsBackPendingDDLAndLedger(t *testing.T) {
	path, old := legacyFixture(t, 2)
	execSQL(t, old, "INSERT INTO categories(id,name) VALUES('category','Kept')")
	execSQL(t, old, "INSERT INTO projection_revisions VALUES('category','category',?)", maxSafeInteger+1)
	_ = old.Close()
	if s, err := Open(t.Context(), Config{Path: path, Migrate: true, AllowLegacy: true}); err == nil {
		_ = s.Close()
		t.Fatal("unsafe category counter was materialized as a safe public revision")
	}
	unchanged := rawDatabase(t, path)
	if scalar[int](t, unchanged, "SELECT MAX(version) FROM schema_migrations") != 2 ||
		scalar[int](t, unchanged, "SELECT COUNT(*) FROM sqlite_master WHERE name IN('current_projections','idx_categories_parent')") != 0 ||
		scalar[string](t, unchanged, "SELECT name FROM categories WHERE id='category'") != "Kept" ||
		scalar[int64](t, unchanged, "SELECT revision FROM projection_revisions WHERE entity_type='category'") != maxSafeInteger+1 {
		t.Fatal("failed taxonomy backfill leaked DDL, ledger or source changes")
	}
}
