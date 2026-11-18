package store

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestCatalogSearchRejectsSchemaValidUnreviewedCurrentTextWithoutReturningPrefix(t *testing.T) {
	s, catalog, auth, token := catalogFixture(t)
	taxonomy, err := NewTaxonomy(auth)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b"} {
		seedUnmaterializedCatalogItem(t, s, id, "Visible "+id)
		publishCatalogSearch(t, taxonomy, token, "authority-publish-"+id, id)
	}
	execSQL(t, s.db, `INSERT INTO ai_review_entries(id,item_id,field_key,state,raw_value,created_at,updated_at)
 VALUES('pending-private','b','keyword','needs_review','PendingPrivateOCR','2026','2026')`)
	identity := projectionPair("item", "b")
	payload, err := json.Marshal(searchData{EntityType: "item", EntityID: "b", ApprovedText: "Visible b\nPendingPrivateOCR"})
	if err != nil {
		t.Fatal(err)
	}
	execSQL(t, s.db, "UPDATE current_projections SET payload_json=? WHERE entity_type='search_doc' AND entity_id=?", string(payload), identity)
	revision := scalar[int64](t, s.db, "SELECT entity_revision FROM current_projections WHERE entity_type='search_doc' AND entity_id=?", identity)
	if err := validateSyncRecord("search_doc", identity, "upsert", revision, 1, payload); err != nil {
		t.Fatal("fixture must be structurally valid despite unreviewed text", err)
	}
	before := domainState(t, s)
	for _, query := range []string{"Visible", "PendingPrivateOCR"} {
		page, err := catalog.Page(t.Context(), token, "", 1, ItemFilters{Query: query})
		if !errors.Is(err, ErrDomainState) || page.Data != nil || page.NextCursor != nil {
			t.Fatal("schema-valid unreviewed search text was matched or returned a prefix", query, page, err)
		}
	}
	requireDomainState(t, s, before)
	// An explicit real source mutation republishes from approved source; reads
	// never silently repair a current projection or replay a mutation.
	owner := getCatalogItem(t, catalog, token, "b")
	if _, err := catalog.CreateAlias(t.Context(), token, "authority-explicit-rebuild", owner.ID, owner.Revision, "ReviewedAlias"); err != nil {
		t.Fatal(err)
	}
	requireCatalogNames(t, readAllCatalogPages(t, catalog, token, ItemFilters{Query: "Visible"}, 1), "Visible a", "Visible b")
	requireCatalogNames(t, readAllCatalogPages(t, catalog, token, ItemFilters{Query: "PendingPrivateOCR"}, 1))
}

func TestCatalogSearchRejectsWrongCurrentProjectionVersion(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Visible version"})
	identity := projectionPair("item", item.ID)
	execSQL(t, s.db, "PRAGMA ignore_check_constraints=ON")
	execSQL(t, s.db, "UPDATE current_projections SET projection_version=2 WHERE entity_type='search_doc' AND entity_id=?", identity)
	execSQL(t, s.db, "PRAGMA ignore_check_constraints=OFF")
	before := domainState(t, s)
	page, err := catalog.Page(t.Context(), token, "", 1, ItemFilters{Query: "Visible"})
	if !errors.Is(err, ErrDomainState) || page.Data != nil || page.NextCursor != nil {
		t.Fatal("incompatible current search version was accepted", page, err)
	}
	requireDomainState(t, s, before)
}

func TestCatalogSearchRejectsLossySurrogateEvenWhenDecodedTextMatches(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Visible �"})
	identity := projectionPair("item", item.ID)
	payload := `{"entity_type":"item","entity_id":"` + item.ID + `","approved_text":"Visible \ud800"}`
	var repaired searchData
	if err := json.Unmarshal([]byte(payload), &repaired); err != nil || repaired.ApprovedText != item.Name {
		t.Fatal("fixture must demonstrate Go's lossy surrogate repair", err)
	}
	execSQL(t, s.db, "UPDATE current_projections SET payload_json=? WHERE entity_type='search_doc' AND entity_id=?", payload, identity)
	before := domainState(t, s)
	page, err := catalog.Page(t.Context(), token, "", 1, ItemFilters{Query: "Visible"})
	if !errors.Is(err, ErrDomainState) || page.Data != nil || page.NextCursor != nil {
		t.Fatal("lossy Unicode current projection was accepted", page, err)
	}
	requireDomainState(t, s, before)
}
