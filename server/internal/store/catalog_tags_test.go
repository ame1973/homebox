package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func catalogTags(t *testing.T, auth *Auth, token string, count int) (*Taxonomy, []Tag) {
	t.Helper()
	taxonomy, err := NewTaxonomy(auth)
	if err != nil {
		t.Fatal(err)
	}
	tags := make([]Tag, 0, count)
	for n := range count {
		tags = append(tags, createTag(t, taxonomy, token, fmt.Sprintf("PublishedTag%03d", n)))
	}
	return taxonomy, tags
}

func TestCatalogTagsReplaceCompleteSetPublishesOnlyChangedMembershipAndApprovedSearch(t *testing.T) {
	s, catalog, auth, token := catalogFixture(t)
	taxonomy, tags := catalogTags(t, auth, token, 3)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Owner"})
	other := createCatalogItem(t, catalog, token, ItemCreate{Name: "OtherOwner"})
	execSQL(t, s.db, "INSERT INTO item_units(id,item_id,asset_code,created_at,updated_at) VALUES('unit',?,'UNIT-001','2026','2026')", item.ID)
	execSQL(t, s.db, "INSERT INTO ai_review_entries(id,item_id,field_key,raw_value,state,reviewed_at,created_at,updated_at) VALUES('historical',?,'tag','PublishedTag000','accepted','2026','2026','2026'),('pending',?,'keyword','NeverPublishPendingMembership','needs_review',NULL,'2026','2026')", item.ID, item.ID)
	before := domainState(t, s)
	var original Item
	changed := requireProjectionReplay(t, s, func() {
		var err error
		original, err = catalog.ReplaceTags(t.Context(), token, "replace-tags-original", item.ID, 1, []string{tags[0].ID, tags[1].ID})
		if err != nil {
			t.Fatal(err)
		}
	})
	for _, key := range []projectionKey{{"item", item.ID}, {"search_doc", projectionPair("item", item.ID)}, {"search_doc", projectionPair("unit", "unit")}, {"tag", tags[0].ID}, {"tag", tags[1].ID}, {"item_tag", projectionPair(item.ID, tags[0].ID)}, {"item_tag", projectionPair(item.ID, tags[1].ID)}} {
		if !changed[key] {
			t.Fatal("replacement omitted an independent member, aggregate or dependent search", key)
		}
	}
	if len(changed) != 7 || original.Revision != 2 || !reflect.DeepEqual(original.ItemData, item.ItemData) ||
		!reflect.DeepEqual(other, getCatalogItem(t, catalog, token, other.ID)) {
		t.Fatal("tag replacement returned a truncated page or changed unrelated metadata/ownership")
	}
	encoded, err := json.Marshal(original)
	var resource map[string]any
	if err != nil || json.Unmarshal(encoded, &resource) != nil || len(resource) != 13 || resource["id"] != item.ID || resource["brand"] != nil {
		t.Fatal("tag replacement did not freeze a complete owning Item")
	}
	created := domainState(t, s)
	for _, table := range []string{"item_units", "stock_balances", "purchases", "purchase_lines", "ai_review_entries", "media"} {
		if before[table] != created[table] {
			t.Fatal("membership fabricated physical inventory or overwrote review evidence", table)
		}
	}
	if _, err := taxonomy.DeleteTag(t.Context(), token, "stale-delete-linked-tag", tags[0].ID, 1); !errors.Is(err, ErrRevisionConflict) {
		t.Fatal("old Tag proof silently absorbed new item membership", err)
	}
	shared, err := catalog.ReplaceTags(t.Context(), token, "replace-tags-other", other.ID, 1, []string{tags[0].ID})
	if err != nil || shared.Revision != 2 {
		t.Fatal(err)
	}
	var current Item
	changed = requireProjectionReplay(t, s, func() {
		var err error
		current, err = catalog.ReplaceTags(t.Context(), token, "replace-tags-current", item.ID, 2, []string{tags[1].ID, tags[2].ID})
		if err != nil {
			t.Fatal(err)
		}
	})
	if len(changed) != 7 || current.Revision != 3 || changed[projectionKey{"tag", tags[1].ID}] || changed[projectionKey{"item_tag", projectionPair(item.ID, tags[1].ID)}] ||
		!reflect.DeepEqual(shared, getCatalogItem(t, catalog, token, other.ID)) ||
		scalar[int](t, s.db, "SELECT revision FROM projection_revisions WHERE entity_type='tag' AND entity_id=?", tags[0].ID) != 4 {
		t.Fatal("replacement republished unchanged membership, reset an aggregate or crossed owners")
	}
	if scalar[int](t, s.db, "SELECT revision FROM projection_revisions WHERE entity_type='item_tag' AND entity_id=?", projectionPair(item.ID, tags[0].ID)) != 2 ||
		scalar[int](t, s.db, "SELECT projection_json IS NULL FROM sync_changes WHERE entity_type='item_tag' AND entity_id=? ORDER BY seq DESC LIMIT 1", projectionPair(item.ID, tags[0].ID)) != 1 ||
		strings.Contains(approvedText(t, s, item.ID), tags[0].Name) || strings.Contains(projectedSearchText(t, s, "unit", "unit"), tags[0].Name) ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM search_fts WHERE search_fts MATCH ?", tags[0].Name) != 1 {
		t.Fatal("detachment omitted a tombstone or resurrected a historical reviewed tag label")
	}
	for _, tag := range tags[1:] {
		if !strings.Contains(approvedText(t, s, item.ID), tag.Name) || !strings.Contains(projectedSearchText(t, s, "unit", "unit"), tag.Name) {
			t.Fatal("retained/new realized labels were not published with the final set")
		}
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_changes WHERE projection_json LIKE '%NeverPublishPendingMembership%'") != 0 {
		t.Fatal("unreviewed text entered membership publication")
	}
	_, fresh := login(t, auth)
	before = domainState(t, s)
	if frozen, err := catalog.ReplaceTags(t.Context(), fresh, "replace-tags-original", item.ID, 1, []string{tags[0].ID, tags[1].ID}); err != nil || !reflect.DeepEqual(frozen, original) {
		t.Fatal("fresh login rebased the original owning-Item reply onto newer membership", err)
	}
	requireDomainState(t, s, before)
	if _, err := catalog.ReplaceTags(t.Context(), fresh, "replace-tags-clear", item.ID, 3, []string{}); err != nil {
		t.Fatal(err)
	}
	relinked, err := catalog.ReplaceTags(t.Context(), fresh, "replace-tags-relink", item.ID, 4, []string{tags[1].ID})
	if err != nil || relinked.Revision != 5 || scalar[int](t, s.db, "SELECT revision FROM projection_revisions WHERE entity_type='item_tag' AND entity_id=?", projectionPair(item.ID, tags[1].ID)) != 3 {
		t.Fatal("explicit relinking reset the retained composite identity", err)
	}
	if _, err := catalog.Delete(t.Context(), fresh, "delete-tagged-other-item", other.ID, shared.Revision); err != nil {
		t.Fatal(err)
	}
	before = domainState(t, s)
	if frozen, err := catalog.ReplaceTags(t.Context(), fresh, "replace-tags-other", other.ID, 1, []string{tags[0].ID}); err != nil || !reflect.DeepEqual(frozen, shared) {
		t.Fatal("deleted owner prevented original-key membership reconciliation", err)
	}
	requireDomainState(t, s, before)
}

func TestCatalogTagsNoopOriginalFieldOrderAndInputCopyRemainExplicit(t *testing.T) {
	s, catalog, auth, token := catalogFixture(t)
	_, tags := catalogTags(t, auth, token, 2)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Owner"})
	input := []string{tags[0].ID, tags[1].ID}
	now := auth.now()
	changedInput := false
	auth.now = func() time.Time {
		if !changedInput {
			input[0] = "changed-after-entry"
			changedInput = true
		}
		return now
	}
	original, err := catalog.ReplaceTags(t.Context(), token, "tags-copied-original", item.ID, 1, input)
	if err != nil || original.Revision != 2 || scalar[int](t, s.db, "SELECT COUNT(*) FROM item_tags WHERE item_id=?", item.ID) != 2 {
		t.Fatal("caller mutation changed the copied original command", err)
	}
	before := domainState(t, s)
	noop, err := catalog.ReplaceTags(t.Context(), token, "tags-permuted-noop", item.ID, 2, []string{tags[1].ID, tags[0].ID})
	if err != nil || !reflect.DeepEqual(noop, original) {
		t.Fatal("equal sets in a different order invented another Item revision", err)
	}
	after := domainState(t, s)
	for table, contents := range before {
		if table != "api_idempotency" && table != "mutation_events" && after[table] != contents {
			t.Fatal("set-equal no-op changed source/publication", table)
		}
	}
	before = domainState(t, s)
	for _, call := range []func() (Item, error){
		func() (Item, error) {
			return catalog.ReplaceTags(t.Context(), token, "tags-copied-original", item.ID, 1, []string{tags[1].ID, tags[0].ID})
		},
		func() (Item, error) {
			return catalog.ReplaceTags(t.Context(), token, "tags-copied-original", item.ID, 2, []string{tags[0].ID, tags[1].ID})
		},
		func() (Item, error) {
			return catalog.ReplaceTags(t.Context(), token, "tags-copied-original", "other-owner", 1, []string{tags[0].ID, tags[1].ID})
		},
	} {
		if value, err := call(); !errors.Is(err, ErrIdempotencyConflict) || value.ID != "" {
			t.Fatal("original order/owner/proof was silently replaced during reconciliation", err)
		}
	}
	requireDomainState(t, s, before)
}

func TestCatalogTagsLargeLegacyMembershipIsNotTruncatedAndUnicodePairsStayCanonical(t *testing.T) {
	s, catalog, auth, token := catalogFixture(t)
	_, tags := catalogTags(t, auth, token, 205)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "LegacyOwner"})
	// Model a pre-existing larger imported set through the real private boundary;
	// this is not permission for a public replacement command above 100 IDs.
	legacy, err := runDomainMutation(t.Context(), auth, token, "fixture-legacy-tag-membership", "test_legacy_tag_set", struct{}{}, func(m *mutationTx) (Item, error) {
		for _, tag := range tags {
			if _, err := m.tx.ExecContext(m.ctx, "INSERT INTO item_tags VALUES(?,?)", item.ID, tag.ID); err != nil {
				return Item{}, err
			}
			if _, err := m.touch("item_tag", projectionPair(item.ID, tag.ID), false); err != nil {
				return Item{}, err
			}
			if _, err := m.touch("tag", tag.ID, false); err != nil {
				return Item{}, err
			}
		}
		if err := m.touchItemSearch(item.ID, true); err != nil {
			return Item{}, err
		}
		return loadItem(m.ctx, m.tx, item.ID)
	})
	if err != nil {
		t.Fatal(err)
	}
	cursor, count := "", 0
	for {
		page, err := catalog.PageTags(t.Context(), token, item.ID, cursor, 50)
		if err != nil || len(page.Data) == 0 || len(page.Data) > 50 {
			t.Fatal("legacy assigned tags did not form complete signed pages", err)
		}
		count += len(page.Data)
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	if count != 205 {
		t.Fatal("legacy membership was silently capped at a command/page bound")
	}
	changed := requireProjectionReplay(t, s, func() {
		if _, err := catalog.ReplaceTags(t.Context(), token, "clear-complete-legacy-tags", item.ID, legacy.Revision, []string{}); err != nil {
			t.Fatal(err)
		}
	})
	if len(changed) != 412 || scalar[int](t, s.db, "SELECT COUNT(*) FROM item_tags") != 0 || scalar[int](t, s.db, "SELECT COUNT(*) FROM tags") != 205 ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_changes WHERE entity_type='item_tag' AND operation='delete'") != 205 {
		t.Fatal("clearing a legacy set truncated changes, deleted Tag identities or missed independent tombstones")
	}
	id, tagID := strings.Repeat("界", 124)+"<&\" ", strings.Repeat("標", 124)+"<&\" "
	execSQL(t, s.db, "INSERT INTO items(id,name,tracking_mode,created_at,updated_at) VALUES(?,'Imported','individual','2026','2026')", id)
	seedCatalogProjection(t, s, "item", id, ItemData{ID: id, Name: "Imported", TrackingMode: "individual", UnitCode: "pcs", BaseUnit: "pcs", ScaleFactor: 1, Status: "active"})
	execSQL(t, s.db, "INSERT INTO tags VALUES(?,'ImportedTag')", tagID)
	seedCatalogProjection(t, s, "tag", tagID, TagData{ID: tagID, Name: "ImportedTag"})
	if _, err := catalog.ReplaceTags(t.Context(), token, "link-full-unicode-pair", id, 1, []string{tagID}); err != nil {
		t.Fatal(err)
	}
	page, err := catalog.PageTags(t.Context(), token, id, "", 1)
	if err != nil || len(page.Data) != 1 || page.Data[0].ID != tagID {
		t.Fatal("SQLite/Go canonical Unicode pair identity diverged on an assigned read", err)
	}
	if _, err := catalog.ReplaceTags(t.Context(), token, "unlink-full-unicode-pair", id, 2, []string{}); err != nil ||
		scalar[int](t, s.db, "SELECT revision FROM projection_revisions WHERE entity_type='item_tag' AND entity_id=?", projectionPair(id, tagID)) != 2 {
		t.Fatal("canonical full-length relationship identity was reset or rejected", err)
	}
}
