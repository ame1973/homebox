package store

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type ownedCatalogFixture struct {
	store       *Store
	catalog     *Catalog
	auth        *Auth
	token       string
	item, other Item
	category    Category
	tag         Tag
}

func seedCatalogProjection(t *testing.T, s *Store, kind, id string, payload any) {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	execSQL(t, s.db, "INSERT INTO projection_revisions VALUES(?,?,1)", kind, id)
	execSQL(t, s.db, `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 VALUES(?,?,1,?,'2026')`, kind, id, string(encoded))
}

// Model existing independently projected links without pretending their future
// HTTP/upsert services exist. Actual catalog mutations still use their real API.
func catalogWithOwnedRows(t *testing.T) ownedCatalogFixture {
	t.Helper()
	s, catalog, auth, token := catalogFixture(t)
	taxonomy, err := NewTaxonomy(auth)
	if err != nil {
		t.Fatal(err)
	}
	category := createCategory(t, taxonomy, token, "Classification", nil)
	tag := createTag(t, taxonomy, token, "PublishedTag")
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "DeleteCatalogTitle", CategoryID: &category.ID})
	other := createCatalogItem(t, catalog, token, ItemCreate{Name: "RetainedCatalogTitle", CategoryID: &category.ID})
	execSQL(t, s.db, "INSERT INTO media(id,storage_key,sha256,mime_type,width,height,bytes,created_at) VALUES('image','private-image',?,'image/png',1,1,1,'2026')", strings.Repeat("a", 64))
	seedMediaProjection(t, s, "image")
	for _, owner := range []struct{ prefix, id string }{{"owned", item.ID}, {"other", other.ID}} {
		execSQL(t, s.db, "INSERT INTO item_barcodes VALUES(?,?,'CODE_128','001234','001234','2026')", owner.prefix+"-barcode", owner.id)
		execSQL(t, s.db, "INSERT INTO item_aliases VALUES(?,?,'AliasTerm','2026')", owner.prefix+"-alias", owner.id)
		execSQL(t, s.db, "INSERT INTO item_attributes VALUES(?,?,'AttributeKey','AttributeValue','2026')", owner.prefix+"-attribute", owner.id)
		execSQL(t, s.db, "INSERT INTO media_links(id,media_id,item_id) VALUES(?,'image',?)", owner.prefix+"-link", owner.id)
		execSQL(t, s.db, "INSERT INTO item_tags VALUES(?,?)", owner.id, tag.ID)
		execSQL(t, s.db, `INSERT INTO ai_jobs(id,kind,media_id,item_id,status,available_at,created_at,updated_at)
 VALUES(?,'ocr','image',?,'pending','2026','2026','2026')`, owner.prefix+"-job", owner.id)
		execSQL(t, s.db, `INSERT INTO ai_review_entries(id,item_id,media_id,source_job_id,field_key,raw_value,state,created_at,updated_at)
 VALUES(?,?,'image',?,'keyword','NeverPublishRawOCR','needs_review','2026','2026')`, owner.prefix+"-review", owner.id, owner.prefix+"-job")
		execSQL(t, s.db, "INSERT INTO ocr_results(id,media_id,job_id,raw_text,engine,engine_version,created_at) VALUES(?,'image',?,'NeverPublishRawOCR','fixture','1','2026')", owner.prefix+"-ocr", owner.prefix+"-job")
		for _, projected := range []struct {
			kind, suffix string
			payload      map[string]any
		}{
			{"barcode", "barcode", map[string]any{"item_id": owner.id, "symbology": "CODE_128", "raw_value": "001234", "normalized_value": "001234"}},
			{"alias", "alias", map[string]any{"item_id": owner.id, "alias": "AliasTerm"}},
			{"attribute", "attribute", map[string]any{"item_id": owner.id, "attribute_key": "AttributeKey", "attribute_value": "AttributeValue"}},
			{"media_link", "link", map[string]any{"media_id": "image", "owner_type": "item", "owner_id": owner.id, "is_cover": false, "sort_order": 0, "description": ""}},
			{"ai_review", "review", map[string]any{"item_id": owner.id, "media_id": "image", "field_key": "keyword", "state": "needs_review", "effective_value": nil}},
		} {
			id := owner.prefix + "-" + projected.suffix
			projected.payload["id"] = id
			seedCatalogProjection(t, s, projected.kind, id, projected.payload)
		}
	}
	_, err = runDomainMutation(t.Context(), auth, token, "fixture-catalog-links", "test_catalog_links", struct{}{}, func(m *mutationTx) (Tag, error) {
		for _, id := range []string{item.ID, other.ID} {
			if _, err := m.touch("item_tag", projectionPair(id, tag.ID), false); err != nil {
				return Tag{}, err
			}
			if err := m.touchItemSearch(id, true); err != nil {
				return Tag{}, err
			}
		}
		if _, err := m.touch("tag", tag.ID, false); err != nil {
			return Tag{}, err
		}
		return loadTag(m.ctx, m.tx, tag.ID)
	})
	if err != nil {
		t.Fatal(err)
	}
	return ownedCatalogFixture{s, catalog, auth, token, getCatalogItem(t, catalog, token, item.ID), getCatalogItem(t, catalog, token, other.ID),
		getCategory(t, taxonomy, token, category.ID), Tag{TagData: tag.TagData, Revision: 2}}
}

func TestCatalogDeleteRetainsHistoryIdentitiesAndOnlyItsIndependentOwnedTombstones(t *testing.T) {
	fixture := catalogWithOwnedRows(t)
	s, catalog, item := fixture.store, fixture.catalog, fixture.item
	before := domainState(t, s)
	var ack ActionAck
	changed := requireProjectionReplay(t, s, func() {
		var err error
		ack, err = catalog.Delete(t.Context(), fixture.token, "delete-owned-catalog-item", item.ID, item.Revision)
		if err != nil {
			t.Fatal(err)
		}
	})
	for _, key := range []projectionKey{
		{"item", item.ID}, {"search_doc", projectionPair("item", item.ID)}, {"item_tag", projectionPair(item.ID, fixture.tag.ID)},
		{"barcode", "owned-barcode"}, {"alias", "owned-alias"}, {"attribute", "owned-attribute"}, {"media_link", "owned-link"}, {"ai_review", "owned-review"},
	} {
		if !changed[key] || scalar[int](t, s.db, "SELECT COUNT(*) FROM current_projections WHERE entity_type=? AND entity_id=?", key.kind, key.id) != 0 ||
			scalar[string](t, s.db, "SELECT operation FROM sync_changes WHERE entity_type=? AND entity_id=? ORDER BY seq DESC LIMIT 1", key.kind, key.id) != "delete" ||
			scalar[int](t, s.db, "SELECT projection_json IS NULL FROM sync_changes WHERE entity_type=? AND entity_id=? ORDER BY seq DESC LIMIT 1", key.kind, key.id) != 1 {
			t.Fatal("owned deletion omitted an independent null-payload tombstone", key)
		}
	}
	if !ack.OK || ack.Revision != item.Revision+1 || ack.OperationID == "" || len(changed) != 11 ||
		!changed[projectionKey{"media", "image"}] || scalar[int64](t, s.db, "SELECT revision FROM projection_revisions WHERE entity_type='media' AND entity_id='image'") != 2 ||
		scalar[string](t, s.db, "SELECT operation FROM sync_changes WHERE entity_type='media' AND entity_id='image' ORDER BY seq DESC LIMIT 1") != "upsert" ||
		scalar[int64](t, s.db, "SELECT revision FROM projection_revisions WHERE entity_type='item' AND entity_id=?", item.ID) != ack.Revision ||
		scalar[int64](t, s.db, "SELECT revision FROM projection_revisions WHERE entity_type='category' AND entity_id=?", fixture.category.ID) != fixture.category.Revision+1 ||
		scalar[int64](t, s.db, "SELECT revision FROM projection_revisions WHERE entity_type='tag' AND entity_id=?", fixture.tag.ID) != fixture.tag.Revision+1 {
		t.Fatal("deletion lost its acknowledgement, retained identity or membership aggregates")
	}
	if value, err := catalog.Get(t.Context(), fixture.token, item.ID); !errors.Is(err, ErrNotFound) || value.ID != "" {
		t.Fatal("deleted source remained a current resource", err)
	}
	if !reflect.DeepEqual(fixture.other, getCatalogItem(t, catalog, fixture.token, fixture.other.ID)) ||
		changed[projectionKey{"item", fixture.other.ID}] || changed[projectionKey{"search_doc", projectionPair("item", fixture.other.ID)}] ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM item_barcodes WHERE normalized_value='001234'") != 1 ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM item_tags WHERE tag_id=?", fixture.tag.ID) != 1 ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM search_fts WHERE search_fts MATCH 'DeleteCatalogTitle'") != 0 ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM search_fts WHERE search_fts MATCH 'RetainedCatalogTitle'") != 1 {
		t.Fatal("deleting one identity removed a shared product identifier or another owner's metadata/search")
	}
	for _, table := range []string{"item_aliases", "item_attributes", "media_links", "ai_review_entries", "ai_jobs"} {
		if scalar[int](t, s.db, "SELECT COUNT(*) FROM "+table+" WHERE item_id=?", item.ID) != 0 ||
			scalar[int](t, s.db, "SELECT COUNT(*) FROM "+table+" WHERE item_id=?", fixture.other.ID) != 1 {
			t.Fatal("deletion crossed independent ownership", table)
		}
	}
	if domainState(t, s)["media"] != before["media"] || scalar[int](t, s.db, "SELECT COUNT(*) FROM ocr_results") != 2 ||
		scalar[int](t, s.db, "SELECT job_id IS NULL FROM ocr_results WHERE id='owned-ocr'") != 1 ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_changes WHERE projection_json LIKE '%NeverPublishRawOCR%'") != 0 {
		t.Fatal("shared media/raw evidence was destroyed or unreviewed OCR became publication")
	}
	_, fresh := login(t, fixture.auth)
	before = domainState(t, s)
	replayed, err := catalog.Delete(t.Context(), fresh, "delete-owned-catalog-item", item.ID, item.Revision)
	if err != nil || !reflect.DeepEqual(ack, replayed) {
		t.Fatal("fresh login could not reconcile deletion after its source vanished", err)
	}
	requireDomainState(t, s, before)
}
