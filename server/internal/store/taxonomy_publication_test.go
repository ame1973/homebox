package store

import (
	"database/sql"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func seedCatalogItem(t *testing.T, taxonomy *Taxonomy, token, id string, category *string, tags ...string) {
	t.Helper()
	request := struct {
		ID       string
		Category *string
		Tags     []string
	}{id, category, tags}
	_, err := runDomainMutation(t.Context(), taxonomy.auth, token, "seed-catalog-"+id, "test_seed_catalog_item", request, func(m *mutationTx) (ActionAck, error) {
		if _, err := m.tx.ExecContext(m.ctx, `INSERT INTO items(id,name,brand,model,tracking_mode,category_id,description,notes,created_at,updated_at)
 VALUES(?,'ManualTitle','ManualBrand','ManualModel','individual',?,'ManualDescription','ManualNote',?,?)`, id, category, stamp(m.now), stamp(m.now)); err != nil {
			return ActionAck{}, err
		}
		if category != nil {
			if err := m.touchCategoryPath(*category); err != nil {
				return ActionAck{}, err
			}
		}
		for _, tagID := range tags {
			if _, err := m.tx.ExecContext(m.ctx, "INSERT INTO item_tags VALUES(?,?)", id, tagID); err != nil {
				return ActionAck{}, err
			}
			if _, err := m.touch("tag", tagID, false); err != nil {
				return ActionAck{}, err
			}
			if _, err := m.touch("item_tag", projectionPair(id, tagID), false); err != nil {
				return ActionAck{}, err
			}
		}
		if err := m.touchItemSearch(id, true); err != nil {
			return ActionAck{}, err
		}
		return ActionAck{OK: true, OperationID: m.eventID, Revision: m.pending[projectionKey{"item", id}].revision}, nil
	})
	if err != nil {
		t.Fatal("catalog fixture publication failed", err)
	}
}

type projectionValue struct {
	Revision int64
	Payload  string
}

func currentProjectionState(t *testing.T, s *Store) map[projectionKey]projectionValue {
	t.Helper()
	rows, err := s.db.QueryContext(t.Context(), "SELECT entity_type,entity_id,entity_revision,payload_json FROM current_projections ORDER BY entity_type,entity_id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	state := make(map[projectionKey]projectionValue)
	for rows.Next() {
		var key projectionKey
		var value projectionValue
		if err = rows.Scan(&key.kind, &key.id, &value.Revision, &value.Payload); err != nil {
			t.Fatal(err)
		}
		state[key] = value
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return state
}

func requireProjectionReplay(t *testing.T, s *Store, mutation func()) map[projectionKey]bool {
	t.Helper()
	replayed := currentProjectionState(t, s)
	sequence := scalar[int64](t, s.db, "SELECT COALESCE(MAX(seq),0) FROM sync_changes")
	mutation()
	rows, err := s.db.QueryContext(t.Context(), "SELECT entity_type,entity_id,operation,entity_revision,projection_json FROM sync_changes WHERE seq>? ORDER BY seq", sequence)
	if err != nil {
		t.Fatal(err)
	}
	changed := make(map[projectionKey]bool)
	for rows.Next() {
		var key projectionKey
		var operation string
		var revision int64
		var payload sql.NullString
		if err = rows.Scan(&key.kind, &key.id, &operation, &revision, &payload); err != nil {
			t.Fatal(err)
		}
		if changed[key] || revision <= replayed[key].Revision {
			t.Fatal("one mutation emitted repeated/nonmonotone projection identities", key.kind)
		}
		changed[key] = true
		if operation == "delete" {
			if payload.Valid {
				t.Fatal("tombstone retained a payload")
			}
			delete(replayed, key)
		} else {
			if operation != "upsert" || !payload.Valid || !json.Valid([]byte(payload.String)) {
				t.Fatal("change log contains an invalid full projection")
			}
			replayed[key] = projectionValue{Revision: revision, Payload: payload.String}
		}
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(replayed, currentProjectionState(t, s)) {
		t.Fatal("replaying frozen changes did not reproduce the exact new projection")
	}
	return changed
}

func approvedText(t *testing.T, s *Store, itemID string) string {
	t.Helper()
	return projectedSearchText(t, s, "item", itemID)
}

func projectedSearchText(t *testing.T, s *Store, kind, id string) string {
	t.Helper()
	var data searchData
	encoded := scalar[string](t, s.db, "SELECT payload_json FROM current_projections WHERE entity_type='search_doc' AND entity_id=?", projectionPair(kind, id))
	if json.Unmarshal([]byte(encoded), &data) != nil || data.EntityType != kind || data.EntityID != id {
		t.Fatal("published search identity/payload mismatch")
	}
	return data.ApprovedText
}

func TestTaxonomyCategoryRenameRebuildsOnlyRealizedManualAndApprovedFinalSource(t *testing.T) {
	s, taxonomy, _, token := taxonomyFixture(t)
	root := createCategory(t, taxonomy, token, "OldParentLabel", nil)
	leaf := createCategory(t, taxonomy, token, "LeafLabel", &root.ID)
	tag := createTag(t, taxonomy, token, "OldTagLabel")
	seedCatalogItem(t, taxonomy, token, "item-1", &leaf.ID, tag.ID)
	execSQL(t, s.db, "INSERT INTO item_barcodes VALUES('barcode','item-1','CODE_128','00001234','00001234','2026')")
	execSQL(t, s.db, "INSERT INTO item_aliases VALUES('alias','item-1','ManualAlias','2026')")
	execSQL(t, s.db, "INSERT INTO item_attributes VALUES('attribute','item-1','ManualAttribute','ManualValue','2026')")
	execSQL(t, s.db, "INSERT INTO item_units(id,item_id,asset_code,serial_number,notes,created_at,updated_at) VALUES('unit','item-1','UNIT-001','UnitSerialOnly','UnitNotesOnly','2026','2026')")
	execSQL(t, s.db, "INSERT INTO purchases(id,merchant_name,purchased_at,currency,receipt_note,created_at) VALUES('purchase','ManualMerchant','2026-01-01','TWD','ManualReceipt','2026')")
	execSQL(t, s.db, "INSERT INTO purchase_lines(id,purchase_id,item_id,item_unit_id,quantity_base,paid_minor,note) VALUES('line','purchase','item-1','unit',1,100,'ManualLineNote')")
	execSQL(t, s.db, "INSERT INTO media(id,storage_key,sha256,mime_type,width,height,bytes,created_at) VALUES('image','private-key',?,'image/png',1,1,1,'2026')", strings.Repeat("a", 64))
	execSQL(t, s.db, "INSERT INTO ai_jobs(id,kind,media_id,item_id,status,available_at,error_message,created_at,updated_at) VALUES('job','ocr','image','item-1','completed','2026','UnpublishedJobError','2026','2026')")
	execSQL(t, s.db, "INSERT INTO ocr_results(id,media_id,job_id,raw_text,engine,engine_version,created_at) VALUES('ocr','image','job','UnreviewedRawOCR','fixture','1','2026')")
	for _, review := range []struct {
		id, field, state, raw string
		corrected, reviewed   *string
	}{
		{"pending", "keyword", "needs_review", "PendingCandidate", nil, nil},
		{"rejected", "keyword", "rejected", "RejectedCandidate", nil, testPointer("2026-01-01T00:00:00Z")},
		{"accepted", "keyword", "accepted", "AcceptedKeyword", nil, testPointer("2026-01-01T00:00:00Z")},
		{"corrected", "specification", "corrected", "OldWrongSpec", testPointer("CorrectSpec"), testPointer("2026-01-01T00:00:00Z")},
		{"old-tag", "tag", "accepted", "HistoricalTag", nil, testPointer("2026-01-01T00:00:00Z")},
		{"old-category", "category", "accepted", "HistoricalCategory", nil, testPointer("2026-01-01T00:00:00Z")},
		{"old-title", "title", "accepted", "HistoricalTitle", nil, testPointer("2026-01-01T00:00:00Z")},
		{"old-brand", "brand", "accepted", "HistoricalBrand", nil, testPointer("2026-01-01T00:00:00Z")},
		{"old-model", "model", "accepted", "HistoricalModel", nil, testPointer("2026-01-01T00:00:00Z")},
		{"unreviewed-time", "keyword", "accepted", "UnreviewedTimestampCandidate", nil, nil},
	} {
		execSQL(t, s.db, "INSERT INTO ai_review_entries(id,item_id,field_key,state,raw_value,corrected_value,reviewed_at,created_at,updated_at) VALUES(?,'item-1',?,?,?,?,?,'2026','2026')", review.id, review.field, review.state, review.raw, review.corrected, review.reviewed)
	}
	execSQL(t, s.db, "UPDATE search_documents SET reviewed_ai='PoisonedOldCache' WHERE item_id='item-1'")
	execSQL(t, s.db, "INSERT INTO unit_search_documents(unit_id,title,reviewed_ai,revision,updated_at) VALUES('unit','PoisonedUnitTitle','PoisonedUnitCache',9,'2026')")
	execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('search_doc',?,11)", projectionPair("unit", "unit"))
	root = getCategory(t, taxonomy, token, root.ID)
	before := domainState(t, s)
	changed := requireProjectionReplay(t, s, func() {
		if _, err := taxonomy.PatchCategory(t.Context(), token, "rename-parent-key", root.ID, root.Revision, CategoryPatch{Name: testPointer("NewParentLabel")}); err != nil {
			t.Fatal(err)
		}
	})
	for _, key := range []projectionKey{{"category", root.ID}, {"item", "item-1"}, {"search_doc", projectionPair("item", "item-1")}, {"search_doc", projectionPair("unit", "unit")}} {
		if !changed[key] {
			t.Fatal("taxonomy mutation omitted a dependent projection", key.kind)
		}
	}
	if changed[projectionKey{"category", leaf.ID}] || getCategory(t, taxonomy, token, leaf.ID).Revision != 2 {
		t.Fatal("ancestor rename republished unchanged descendants")
	}
	published := approvedText(t, s, "item-1")
	unitPublished := projectedSearchText(t, s, "unit", "unit")
	for _, text := range []string{"ManualTitle", "ManualBrand", "ManualModel", "ManualDescription", "ManualNote", "NewParentLabel", "LeafLabel", "OldTagLabel", "00001234", "ManualAlias", "ManualAttribute", "ManualValue", "ManualMerchant", "ManualReceipt", "ManualLineNote", "AcceptedKeyword", "CorrectSpec"} {
		if !strings.Contains(published, text) || !strings.Contains(unitPublished, text) ||
			scalar[int](t, s.db, "SELECT COUNT(*) FROM search_fts WHERE search_fts MATCH ?", text) != 1 ||
			scalar[int](t, s.db, "SELECT COUNT(*) FROM unit_search_fts WHERE unit_search_fts MATCH ?", text) != 1 {
			t.Fatal("approved source contribution was lost", text)
		}
	}
	for _, text := range []string{"OldParentLabel", "PendingCandidate", "RejectedCandidate", "UnreviewedRawOCR", "OldWrongSpec", "HistoricalTag", "HistoricalCategory", "HistoricalTitle", "HistoricalBrand", "HistoricalModel", "UnreviewedTimestampCandidate", "PoisonedOldCache", "UnpublishedJobError", "UnitSerialOnly", "UnitNotesOnly", "UNIT-001"} {
		if strings.Contains(published, text) || scalar[int](t, s.db, "SELECT COUNT(*) FROM search_fts WHERE search_fts MATCH ?", `"`+text+`"`) != 0 {
			t.Fatal("unapproved, stale, private or wrongly scoped text contaminated publication", text)
		}
	}
	for _, text := range []string{"UnitSerialOnly", "UnitNotesOnly", "UNIT-001"} {
		if !strings.Contains(unitPublished, text) || scalar[int](t, s.db, "SELECT COUNT(*) FROM unit_search_fts WHERE unit_search_fts MATCH ?", `"`+text+`"`) != 1 {
			t.Fatal("unit-specific identifiers were not published in their own scope", text)
		}
	}
	for _, text := range []string{"OldParentLabel", "PendingCandidate", "RejectedCandidate", "UnreviewedRawOCR", "OldWrongSpec", "HistoricalTag", "HistoricalCategory", "HistoricalTitle", "HistoricalBrand", "HistoricalModel", "UnreviewedTimestampCandidate", "PoisonedOldCache", "UnpublishedJobError", "PoisonedUnitTitle", "PoisonedUnitCache"} {
		if strings.Contains(unitPublished, text) || scalar[int](t, s.db, "SELECT COUNT(*) FROM unit_search_fts WHERE unit_search_fts MATCH ?", `"`+text+`"`) != 0 {
			t.Fatal("unapproved or stale text contaminated unit publication", text)
		}
	}
	if scalar[int](t, s.db, "SELECT revision FROM unit_search_documents WHERE unit_id='unit'") != 12 ||
		scalar[int](t, s.db, "SELECT entity_revision FROM current_projections WHERE entity_type='search_doc' AND entity_id=?", projectionPair("unit", "unit")) != 12 {
		t.Fatal("unit search ignored its retained authoritative counter")
	}
	after := domainState(t, s)
	for _, table := range []string{"item_units", "stock_balances", "purchases", "purchase_lines", "media", "ai_jobs", "ai_review_entries", "ocr_results"} {
		if after[table] != before[table] {
			t.Fatal("taxonomy rebuilt text by altering its source evidence", table)
		}
	}
	if scalar[int](t, s.db, "SELECT revision FROM search_documents WHERE item_id='item-1'") != 2 ||
		scalar[int](t, s.db, "SELECT revision FROM projection_revisions WHERE entity_type='item' AND entity_id='item-1'") != 2 {
		t.Fatal("dependent source/projection revisions diverged")
	}
}

func TestTaxonomyTagRenameAndDeleteDoNotResurrectHistoricalAcceptedLabels(t *testing.T) {
	s, taxonomy, _, token := taxonomyFixture(t)
	tag := createTag(t, taxonomy, token, "OldTag")
	retained := createTag(t, taxonomy, token, "RetainedTag")
	seedCatalogItem(t, taxonomy, token, "item-1", nil, tag.ID, retained.ID)
	seedSearchUnit(t, s, "unit", "item-1", "ASSET", nil, "UnitNote")
	execSQL(t, s.db, "INSERT INTO ai_review_entries(id,item_id,field_key,state,raw_value,reviewed_at,created_at,updated_at) VALUES('old-tag','item-1','tag','accepted','OldTag','2026-01-01T00:00:00Z','2026','2026')")
	var renamed Tag
	requireProjectionReplay(t, s, func() {
		var err error
		renamed, err = taxonomy.PatchTag(t.Context(), token, "rename-linked-tag", tag.ID, 2, "NewTag")
		if err != nil {
			t.Fatal(err)
		}
	})
	if renamed.Revision != 3 || !strings.Contains(approvedText(t, s, "item-1"), "NewTag") || strings.Contains(approvedText(t, s, "item-1"), "OldTag") ||
		!strings.Contains(projectedSearchText(t, s, "unit", "unit"), "NewTag") || strings.Contains(projectedSearchText(t, s, "unit", "unit"), "OldTag") ||
		scalar[int](t, s.db, "SELECT revision FROM projection_revisions WHERE entity_type='item' AND entity_id='item-1'") != 1 {
		t.Fatal("tag rename lost current text or unnecessarily changed item membership")
	}
	changed := requireProjectionReplay(t, s, func() {
		if _, err := taxonomy.DeleteTag(t.Context(), token, "delete-linked-tag", tag.ID, 3); err != nil {
			t.Fatal(err)
		}
	})
	if !changed[projectionKey{"tag", tag.ID}] || !changed[projectionKey{"item_tag", projectionPair("item-1", tag.ID)}] || !changed[projectionKey{"item", "item-1"}] ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM item_tags WHERE tag_id=?", retained.ID) != 1 || scalar[int](t, s.db, "SELECT COUNT(*) FROM items") != 1 ||
		strings.Contains(approvedText(t, s, "item-1"), "NewTag") || strings.Contains(approvedText(t, s, "item-1"), "OldTag") || !strings.Contains(approvedText(t, s, "item-1"), "RetainedTag") ||
		strings.Contains(projectedSearchText(t, s, "unit", "unit"), "NewTag") || strings.Contains(projectedSearchText(t, s, "unit", "unit"), "OldTag") || !strings.Contains(projectedSearchText(t, s, "unit", "unit"), "RetainedTag") {
		t.Fatal("tag delete erased unrelated inventory/links or resurrected a historical label")
	}
}

func TestTaxonomyCategoryDeletionClearsClassificationWithoutDeletingHoldings(t *testing.T) {
	s, taxonomy, _, token := taxonomyFixture(t)
	category := createCategory(t, taxonomy, token, "DeletedClass", nil)
	seedCatalogItem(t, taxonomy, token, "individual", &category.ID)
	seedCatalogItem(t, taxonomy, token, "quantity", &category.ID)
	execSQL(t, s.db, "UPDATE items SET tracking_mode='quantity' WHERE id='quantity'")
	execSQL(t, s.db, "INSERT INTO locations(id,code,name,created_at,updated_at) VALUES('location','BOX','Box','2026','2026')")
	execSQL(t, s.db, "INSERT INTO item_units(id,item_id,location_id,asset_code,created_at,updated_at) VALUES('unit','individual','location','ASSET-1','2026','2026')")
	execSQL(t, s.db, "INSERT INTO stock_balances VALUES('quantity','location',1500,'2026')")
	category = getCategory(t, taxonomy, token, category.ID)
	before := domainState(t, s)
	changed := requireProjectionReplay(t, s, func() {
		if _, err := taxonomy.DeleteCategory(t.Context(), token, "delete-used-classification", category.ID, category.Revision); err != nil {
			t.Fatal(err)
		}
	})
	if !changed[projectionKey{"category", category.ID}] || scalar[int](t, s.db, "SELECT COUNT(*) FROM items WHERE category_id IS NOT NULL") != 0 || scalar[int](t, s.db, "SELECT COUNT(*) FROM items") != 2 {
		t.Fatal("category deletion did not atomically clear only classification")
	}
	after := domainState(t, s)
	for _, table := range []string{"locations", "item_units", "stock_balances", "inventory_events"} {
		if after[table] != before[table] {
			t.Fatal("classification deletion modified physical holdings", table)
		}
	}
	for _, id := range []string{"individual", "quantity"} {
		if strings.Contains(approvedText(t, s, id), "DeletedClass") || !changed[projectionKey{"item", id}] || !changed[projectionKey{"search_doc", projectionPair("item", id)}] {
			t.Fatal("classification deletion omitted item/search invalidation")
		}
	}
	if !changed[projectionKey{"search_doc", projectionPair("unit", "unit")}] || strings.Contains(projectedSearchText(t, s, "unit", "unit"), "DeletedClass") {
		t.Fatal("classification deletion left a stale unit search document")
	}
}

func TestTaxonomySearchFlushUsesFinalStateAndOneRevisionPerIdentity(t *testing.T) {
	s, taxonomy, _, token := taxonomyFixture(t)
	tag := createTag(t, taxonomy, token, "Before")
	seedCatalogItem(t, taxonomy, token, "item-1", nil, tag.ID)
	changed := requireProjectionReplay(t, s, func() {
		_, err := runDomainMutation(t.Context(), taxonomy.auth, token, "staged-search-final-state", "test_final_search_state", struct{}{}, func(m *mutationTx) (Tag, error) {
			if err := m.touchItemSearch("item-1", false); err != nil {
				return Tag{}, err
			}
			for _, name := range []string{"Intermediate", "FinalTag"} {
				if _, err := m.tx.ExecContext(m.ctx, "UPDATE tags SET name=? WHERE id=?", name, tag.ID); err != nil {
					return Tag{}, err
				}
				if _, err := m.touch("tag", tag.ID, false); err != nil {
					return Tag{}, err
				}
				if err := m.touchItemSearch("item-1", false); err != nil {
					return Tag{}, err
				}
			}
			return loadTag(m.ctx, m.tx, tag.ID)
		})
		if err != nil {
			t.Fatal(err)
		}
	})
	if len(changed) != 2 || !strings.Contains(approvedText(t, s, "item-1"), "FinalTag") || strings.Contains(approvedText(t, s, "item-1"), "Intermediate") ||
		scalar[int](t, s.db, "SELECT revision FROM search_documents WHERE item_id='item-1'") != 2 {
		t.Fatal("publication captured an intermediate state or duplicate revision")
	}
}
