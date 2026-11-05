package store

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestCatalogCursorBindsEveryEffectiveFilterAndTimezoneWithoutExposingQueries(t *testing.T) {
	s, catalog, auth, token := catalogFixture(t)
	taxonomy, err := NewTaxonomy(auth)
	if err != nil {
		t.Fatal(err)
	}
	locations, err := NewLocations(auth)
	if err != nil {
		t.Fatal(err)
	}
	category := createCategory(t, taxonomy, token, "Category", nil)
	box := createLocation(t, locations, token, "BOX", nil)
	for index := range 3 {
		name := fmt.Sprintf("PrivateQuery%d", index)
		item := createCatalogItem(t, catalog, token, ItemCreate{Name: name, CategoryID: &category.ID})
		seedSearchUnit(t, s, name, item.ID, "ASSET-"+name, nil, "")
		execSQL(t, s.db, "UPDATE item_units SET location_id=? WHERE id=?", box.ID, name)
		seedCatalogPurchase(t, s, item.ID, name, "2026-11-05T12:00:00Z", "TWD", "PrivateMerchant")
	}
	filters := ItemFilters{From: "2026-11-05", To: "2026-11-06", Currency: "TWD", CategoryID: category.ID,
		LocationID: box.ID, Status: "active", Merchant: "PrivateMerchant", Query: "  PRIVATEQUERY  "}
	first, err := catalog.Page(t.Context(), token, "", 1, filters)
	if err != nil || len(first.Data) != 1 || first.NextCursor == nil {
		t.Fatal("first completely filtered signed page failed", err)
	}
	body, err := base64.RawURLEncoding.DecodeString(strings.Split(*first.NextCursor, ".")[0])
	if err != nil || strings.Contains(string(body), "PrivateQuery") || strings.Contains(string(body), "PrivateMerchant") ||
		strings.Contains(string(body), category.ID) || strings.Contains(string(body), box.ID) || strings.Contains(string(body), token) {
		t.Fatal("cursor exposed filter text, unrelated identifiers, or a credential")
	}
	for _, mutate := range []func(*ItemFilters){
		func(v *ItemFilters) { v.From = "2026-11-04" }, func(v *ItemFilters) { v.To = "2026-11-07" },
		func(v *ItemFilters) { v.Currency = "USD" }, func(v *ItemFilters) { v.CategoryID = "different" },
		func(v *ItemFilters) { v.LocationID = "different" }, func(v *ItemFilters) { v.Scope = "direct" },
		func(v *ItemFilters) { v.Status = "archived" }, func(v *ItemFilters) { v.Merchant = "privatemerchant" },
		func(v *ItemFilters) { v.Query = "different" },
	} {
		changed := filters
		mutate(&changed)
		if page, err := catalog.Page(t.Context(), token, *first.NextCursor, 1, changed); !errors.Is(err, ErrInvalidCursor) || page.Data != nil || page.NextCursor != nil {
			t.Fatal("cursor silently changed an original filter", err)
		}
	}
	if _, err := catalog.Page(t.Context(), token, *first.NextCursor, 2, filters); !errors.Is(err, ErrInvalidCursor) {
		t.Fatal("cursor moved to another page size", err)
	}
	equivalent := filters
	equivalent.Scope, equivalent.Query = "subtree", "privatequery"
	setCatalogTimezone(t, s, "UTC")
	if page, err := catalog.Page(t.Context(), token, *first.NextCursor, 1, equivalent); err != nil || len(page.Data) != 1 || page.Data[0].ID <= first.Data[0].ID {
		t.Fatal("semantically identical defaults/query normalization changed scope", err)
	}
	setCatalogTimezone(t, s, "Asia/Taipei")
	if _, err := catalog.Page(t.Context(), token, *first.NextCursor, 1, filters); !errors.Is(err, ErrInvalidCursor) {
		t.Fatal("cursor adopted a changed home timezone", err)
	}
	setCatalogTimezone(t, s, "UTC")
	createLocation(t, locations, token, "OTHER", nil)
	createCategory(t, taxonomy, token, "Other", nil)
	createTag(t, taxonomy, token, "TagA")
	createTag(t, taxonomy, token, "TagB")
	for _, read := range []func() (*string, error){
		func() (*string, error) {
			page, err := locations.Page(t.Context(), token, "", 1)
			return page.NextCursor, err
		},
		func() (*string, error) {
			page, err := taxonomy.PageCategories(t.Context(), token, "", 1)
			return page.NextCursor, err
		},
		func() (*string, error) {
			page, err := taxonomy.PageTags(t.Context(), token, "", 1)
			return page.NextCursor, err
		},
	} {
		cursor, err := read()
		if err != nil || cursor == nil {
			t.Fatal("cross-domain cursor fixture failed", err)
		}
		if _, err := catalog.Page(t.Context(), token, *cursor, 1, filters); !errors.Is(err, ErrInvalidCursor) {
			t.Fatal("another metadata cursor entered the catalog namespace", err)
		}
	}
	if _, err := locations.Page(t.Context(), token, *first.NextCursor, 1); !errors.Is(err, ErrInvalidCursor) {
		t.Fatal("catalog cursor entered the location namespace", err)
	}
}

func TestCatalogCursorFreshLoginExpiryRestoreAndAuthenticationRemainIndependent(t *testing.T) {
	for _, state := range []string{"fresh login", "expired cursor", "restored", "mandatory", "revoked", "expired session", "invalid"} {
		t.Run(state, func(t *testing.T) {
			s, catalog, auth, token := catalogFixture(t)
			createCatalogItem(t, catalog, token, ItemCreate{Name: "First"})
			createCatalogItem(t, catalog, token, ItemCreate{Name: "Second"})
			page, err := catalog.Page(t.Context(), token, "", 1, ItemFilters{})
			if err != nil || page.NextCursor == nil {
				t.Fatal(err)
			}
			want := ErrAuthentication
			switch state {
			case "fresh login":
				if _, err := auth.Logout(t.Context(), token, "logout-catalog-list"); err != nil {
					t.Fatal(err)
				}
				if _, err := catalog.Page(t.Context(), token, *page.NextCursor, 1, ItemFilters{}); !errors.Is(err, ErrAuthentication) {
					t.Fatal("cursor reactivated a revoked cookie", err)
				}
				_, fresh := login(t, auth)
				if next, err := catalog.Page(t.Context(), fresh, *page.NextCursor, 1, ItemFilters{}); err != nil || len(next.Data) != 1 {
					t.Fatal("fresh same-principal session could not continue a read", err)
				}
				return
			case "expired cursor":
				now := auth.now().Add(metadataCursorLifetime)
				auth.now = func() time.Time { return now }
				want = ErrInvalidCursor
			case "restored":
				execSQL(t, s.db, "UPDATE sync_state SET dataset_epoch='restored-catalog-epoch'")
				if _, err := catalog.Page(t.Context(), token, *page.NextCursor, 1, ItemFilters{}); !errors.Is(err, ErrAuthentication) {
					t.Fatal("old epoch cookie remained active", err)
				}
				_, token = login(t, auth)
				want = ErrInvalidCursor
			case "mandatory":
				execSQL(t, s.db, "UPDATE admin_accounts SET must_change_password=1")
				want = ErrPasswordChangeRequired
			case "revoked":
				execSQL(t, s.db, "UPDATE sessions SET revoked_at=?", stamp(auth.now()))
			case "expired session":
				execSQL(t, s.db, "UPDATE sessions SET expires_at=?", stamp(auth.now()))
			case "invalid":
				token = "invalid"
			}
			if value, err := catalog.Page(t.Context(), token, *page.NextCursor, 1, ItemFilters{}); !errors.Is(err, want) || value.Data != nil || value.NextCursor != nil {
				t.Fatal("stale or unauthorized continuation exposed catalog data", err)
			}
			if _, err := catalog.Page(t.Context(), *page.NextCursor, "", 1, ItemFilters{}); !errors.Is(err, ErrAuthentication) {
				t.Fatal("cursor was accepted as a credential", err)
			}
		})
	}
}

func TestCatalogSignedPageWithholdsAllDataOnCommitOrPrivateKeyFailure(t *testing.T) {
	for _, failure := range []string{"commit", "private key", "cancelled"} {
		t.Run(failure, func(t *testing.T) {
			s, catalog, auth, token := catalogFixture(t)
			createCatalogItem(t, catalog, token, ItemCreate{Name: "First"})
			createCatalogItem(t, catalog, token, ItemCreate{Name: "Second"})
			now := auth.now().Add(time.Minute)
			auth.now = func() time.Time { return now }
			ctx := t.Context()
			switch failure {
			case "commit":
				execSQL(t, s.db, "CREATE TABLE filtered_read_guard(id TEXT REFERENCES admin_accounts(id) DEFERRABLE INITIALLY DEFERRED) STRICT")
				execSQL(t, s.db, "CREATE TRIGGER fail_filtered_read AFTER UPDATE ON sessions BEGIN INSERT INTO filtered_read_guard VALUES('missing'); END")
			case "private key":
				auth.serverSecret = nil
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			before := domainState(t, s)
			if value, err := catalog.Page(ctx, token, "", 1, ItemFilters{}); err == nil || value.Data != nil || value.NextCursor != nil {
				t.Fatal("failed read released a prefix or signed continuation", err)
			}
			requireDomainState(t, s, before)
		})
	}
}

func TestCatalogCurrentSearchValidationRejectsWrongScopeDuplicatesNullsAndPrivateFields(t *testing.T) {
	s, catalog, auth, token := catalogFixture(t)
	taxonomy, err := NewTaxonomy(auth)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b"} {
		seedUnmaterializedCatalogItem(t, s, id, "Visible")
		publishCatalogSearch(t, taxonomy, token, "publish-visible-"+id, id)
	}
	for _, payload := range []string{
		`{"entity_type":"unit","entity_id":"b","approved_text":"Visible"}`,
		`{"entity_type":"item","entity_id":"wrong","approved_text":"Visible"}`,
		`{"entity_type":"item","entity_id":"b"}`,
		`{"entity_type":"item","entity_id":"b","approved_text":null}`,
		`{"entity_type":"item","entity_id":"b","approved_text":10}`,
		`{"entity_type":"item","entity_id":"b","approved_text":"Visible","raw_ocr":"Private"}`,
		`{"entity_type":"item","entity_id":"b","approved_text":"Visible","approved_text":"Private"}`,
		`{"ENTITY_TYPE":"item","entity_id":"b","approved_text":"Visible"}`,
	} {
		execSQL(t, s.db, "UPDATE current_projections SET payload_json=? WHERE entity_type='search_doc' AND entity_id=?", payload, projectionPair("item", "b"))
		if page, err := catalog.Page(t.Context(), token, "", 2, ItemFilters{Query: "Visible"}); !errors.Is(err, ErrDomainState) || page.Data != nil || page.NextCursor != nil {
			t.Fatal("corrupt published search exposed a valid prefix or private text", err)
		}
	}
	// Payload JSON spelling is not composite identity spelling: equivalent
	// whitespace/escapes remain valid, unlike noncanonical identity keys.
	execSQL(t, s.db, "UPDATE current_projections SET payload_json=? WHERE entity_type='search_doc' AND entity_id=?",
		`{ "approved_text":"Visible", "entity_id":"b", "entity_type":"item" }`, projectionPair("item", "b"))
	requireCatalogNames(t, readAllCatalogPages(t, catalog, token, ItemFilters{Query: "visible"}, 1), "Visible", "Visible")
}

func TestCatalogSignedContinuationRemainsLiveWithoutResurrectingDeletedItems(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	seedUnmaterializedCatalogItem(t, s, "a", "Match A")
	seedUnmaterializedCatalogItem(t, s, "b", "Match B")
	first, err := catalog.Page(t.Context(), token, "", 1, ItemFilters{Query: "match"})
	if err != nil || first.NextCursor == nil || first.Data[0].ID != "a" {
		t.Fatal(err)
	}
	if _, err := catalog.Delete(t.Context(), token, "delete-live-later-item", "b", 1); err != nil {
		t.Fatal(err)
	}
	next, err := catalog.Page(t.Context(), token, *first.NextCursor, 1, ItemFilters{Query: "match"})
	if err != nil || next.Data == nil || len(next.Data) != 0 || next.NextCursor != nil {
		t.Fatal("live continuation froze/resurrected a subsequently deleted item", err)
	}
}
