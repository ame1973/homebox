package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCatalogOwnedPagesWithholdCommitKeySQLAndOwnerAuthorityFailures(t *testing.T) {
	for _, failure := range []string{"commit", "key", "cancelled", "missing owner counter"} {
		t.Run(failure, func(t *testing.T) {
			s, catalog, auth, token := catalogFixture(t)
			item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Owner"})
			seedOwnedPages(t, catalog, auth, token, item)
			now := auth.now().Add(time.Minute)
			auth.now = func() time.Time { return now }
			ctx, want := t.Context(), ErrDomainState
			switch failure {
			case "commit":
				execSQL(t, s.db, "CREATE TABLE owned_page_guard(id TEXT REFERENCES admin_accounts(id) DEFERRABLE INITIALLY DEFERRED) STRICT")
				execSQL(t, s.db, "CREATE TRIGGER fail_owned_page AFTER UPDATE ON sessions BEGIN INSERT INTO owned_page_guard VALUES('missing'); END")
			case "key":
				auth.serverSecret = nil
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = context.Canceled
			case "missing owner counter":
				execSQL(t, s.db, "DELETE FROM current_projections WHERE entity_type='item' AND entity_id=?", item.ID)
				execSQL(t, s.db, "DELETE FROM projection_revisions WHERE entity_type='item' AND entity_id=?", item.ID)
			}
			before := domainState(t, s)
			for _, kind := range []string{"barcode", "alias", "attribute", "tag"} {
				if _, err := readOwnedPage(t, ctx, catalog, kind, token, item.ID, "", 1); err == nil || failure != "commit" && !errors.Is(err, want) {
					t.Fatal("failed read/commit released a prefix, repaired authority or signed a continuation", kind, err)
				}
				requireDomainState(t, s, before)
			}
			if failure == "commit" && scalar[int](t, s.db, "SELECT COUNT(*) FROM owned_page_guard") != 0 {
				t.Fatal("failed read COMMIT left a deferred guard row")
			}
		})
	}
	s, catalog, _, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Owner"})
	before := domainState(t, s)
	page, err := readCatalogLinksPage(t.Context(), catalog.auth, token, item.ID, "", 1, "barcodes", "SELECT no_such_column", scanBarcode, func(value Barcode) string { return value.ID })
	if err == nil || page.Data != nil || page.NextCursor != nil {
		t.Fatal("SQL query failure exposed a collection result")
	}
	requireDomainState(t, s, before)
}

func TestCatalogOwnedPagesAreLiveAndDoNotRequireTheLastSourceRowToSurvive(t *testing.T) {
	for _, kind := range []string{"barcode", "alias", "attribute", "tag"} {
		t.Run(kind, func(t *testing.T) {
			_, catalog, auth, token := catalogFixture(t)
			item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Owner"})
			taxonomy := seedOwnedPages(t, catalog, auth, token, item)
			all, err := readOwnedPage(t, t.Context(), catalog, kind, token, item.ID, "", 100)
			if err != nil {
				t.Fatal(err)
			}
			first, err := readOwnedPage(t, t.Context(), catalog, kind, token, item.ID, "", 1)
			if err != nil || first.NextCursor == nil {
				t.Fatal(err)
			}
			firstID, nextID := wireID(t, all.Data[0]), wireID(t, all.Data[1])
			field := ""
			switch kind {
			case "barcode":
				_, err = catalog.DeleteBarcode(t.Context(), token, "delete-owned-page-last", firstID, 1)
				if err == nil {
					_, err = catalog.PatchBarcode(t.Context(), token, "edit-owned-page-next", nextID, 1, BarcodeWrite{"CODE_128", "LiveChanged"})
				}
				field = "raw_value"
			case "alias":
				_, err = catalog.DeleteAlias(t.Context(), token, "delete-owned-page-last", firstID, 1)
				if err == nil {
					_, err = catalog.PatchAlias(t.Context(), token, "edit-owned-page-next", nextID, 1, "LiveChanged")
				}
				field = "alias"
			case "attribute":
				_, err = catalog.DeleteAttribute(t.Context(), token, "delete-owned-page-last", firstID, 1)
				if err == nil {
					_, err = catalog.PatchAttribute(t.Context(), token, "edit-owned-page-next", nextID, 1, AttributePatch{AttributeValue: testPointer("LiveChanged")})
				}
				field = "attribute_value"
			case "tag":
				current := getCatalogItem(t, catalog, token, item.ID)
				_, err = catalog.ReplaceTags(t.Context(), token, "delete-owned-page-last", item.ID, current.Revision, []string{nextID, wireID(t, all.Data[2])})
				if err == nil {
					_, err = taxonomy.PatchTag(t.Context(), token, "edit-owned-page-next", nextID, 2, "LiveChanged")
				}
				field = "name"
			}
			if err != nil {
				t.Fatal(err)
			}
			next, err := readOwnedPage(t, t.Context(), catalog, kind, token, item.ID, *first.NextCursor, 1)
			if err != nil || len(next.Data) != 1 || wireID(t, next.Data[0]) != nextID || string(next.Data[0][field]) != `"LiveChanged"` {
				t.Fatal("live continuation became a snapshot, skipped an identity or required the deleted last row", err)
			}
			current := getCatalogItem(t, catalog, token, item.ID)
			if _, err := catalog.Delete(t.Context(), token, "delete-owned-page-owner", item.ID, current.Revision); err != nil {
				t.Fatal(err)
			}
			if _, err := readOwnedPage(t, t.Context(), catalog, kind, token, item.ID, *first.NextCursor, 1); !errors.Is(err, ErrNotFound) {
				t.Fatal("deleted owner became an empty or historical collection", err)
			}
		})
	}
}

func TestCatalogOwnedTagPagesRejectMissingUnsafeAndNoncanonicalLinkAuthority(t *testing.T) {
	for _, failure := range []string{"missing", "unsafe", "noncanonical"} {
		t.Run(failure, func(t *testing.T) {
			s, catalog, auth, token := catalogFixture(t)
			_, tags := catalogTags(t, auth, token, 1)
			item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Owner"})
			if _, err := catalog.ReplaceTags(t.Context(), token, "prepare-link-authority", item.ID, 1, []string{tags[0].ID}); err != nil {
				t.Fatal(err)
			}
			id := projectionPair(item.ID, tags[0].ID)
			execSQL(t, s.db, "DELETE FROM current_projections WHERE entity_type='item_tag' AND entity_id=?", id)
			switch failure {
			case "missing":
				execSQL(t, s.db, "DELETE FROM projection_revisions WHERE entity_type='item_tag' AND entity_id=?", id)
			case "unsafe":
				execSQL(t, s.db, "UPDATE projection_revisions SET revision=? WHERE entity_type='item_tag' AND entity_id=?", maxSafeInteger+1, id)
			case "noncanonical":
				execSQL(t, s.db, "UPDATE projection_revisions SET entity_id=? WHERE entity_type='item_tag' AND entity_id=?", " "+id, id)
			}
			before := domainState(t, s)
			if _, err := readOwnedPage(t, t.Context(), catalog, "tag", token, item.ID, "", 1); !errors.Is(err, ErrDomainState) {
				t.Fatal("bad link authority was treated as a valid Tag list", err)
			}
			if value, err := catalog.ReplaceTags(t.Context(), token, "clear-bad-link-authority", item.ID, 2, []string{}); !errors.Is(err, ErrDomainState) || value.ID != "" {
				t.Fatal("removal fabricated a fresh link counter instead of rejecting corruption", err)
			}
			requireDomainState(t, s, before)
		})
	}
}
