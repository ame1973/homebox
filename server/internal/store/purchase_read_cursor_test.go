package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

type purchasePageTest struct {
	Data       []json.RawMessage `json:"data"`
	NextCursor *string           `json:"next_cursor"`
}

func readPurchaseCollection(ctx context.Context, fixture purchaseFixture, p *Purchases, kind, cursor string, size int) (purchasePageTest, error) {
	var value any
	var err error
	switch kind {
	case "global":
		value, err = p.Page(ctx, fixture.token, cursor, size, PurchaseFilters{})
	case "item":
		value, err = p.PageForItem(ctx, fixture.token, fixture.quantity.ID, cursor, size)
	case "lines":
		value, err = p.PageLines(ctx, fixture.token, fixture.purchase.ID, cursor, size)
	default:
		panic("unknown purchase collection test")
	}
	// Marshal errors too: tests must inspect the actual error reply for leaked
	// prefixes, not replace it with a zero value in this test-only adapter.
	encoded, encodeErr := json.Marshal(value)
	var result purchasePageTest
	decodeErr := json.Unmarshal(encoded, &result)
	return result, errors.Join(err, encodeErr, decodeErr)
}
func seedPurchaseReadContinuation(t *testing.T, fixture purchaseFixture) {
	t.Helper()
	seedReadPurchase(t, fixture.store, PurchaseData{ID: "zz-continuation", MerchantName: "Continuation", PurchasedAt: "2026-11-06T00:00:00Z", Currency: "JPY"})
	seedReadPurchaseLine(t, fixture.store, PurchaseLineData{ID: "zz-continuation-line", PurchaseID: "zz-continuation", ItemID: fixture.quantity.ID, QuantityBase: 1, PaidMinor: 0})
}

func TestPurchaseReadCursorsBindAllNormalizedFiltersTimezoneSizeAndCollection(t *testing.T) {
	fixture, p := purchaseCommands(t)
	s := fixture.store
	taxonomy, err := NewTaxonomy(fixture.auth)
	if err != nil {
		t.Fatal(err)
	}
	category := createCategory(t, taxonomy, fixture.token, "Private category", nil)
	item, err := fixture.catalog.Patch(t.Context(), fixture.token, "classify-private-purchase-cursor", fixture.quantity.ID, fixture.quantity.Revision, ItemPatch{CategoryID: NullableID{Set: true, Value: &category.ID}})
	if err != nil {
		t.Fatal(err)
	}
	fixture.quantity = item
	for _, id := range []string{"private-header-a", "private-header-b"} {
		seedReadPurchase(t, s, PurchaseData{ID: id, MerchantName: "Private purchase filter é", PurchasedAt: "2026-11-06T00:00:00Z", Currency: "JPY"})
		seedReadPurchaseLine(t, s, PurchaseLineData{ID: id, PurchaseID: id, ItemID: item.ID, QuantityBase: 1, PaidMinor: 0})
	}
	filters := PurchaseFilters{From: "2026-11-05", To: "2026-11-07", Currency: "JPY", CategoryID: category.ID, LocationID: "root", Status: "active", Merchant: "Private purchase filter é"}
	first, err := p.Page(t.Context(), fixture.token, "", 1, filters)
	if err != nil || first.NextCursor == nil || len(first.Data) != 1 {
		t.Fatal("fully filtered cursor fixture failed", err)
	}
	body, err := base64.RawURLEncoding.DecodeString(strings.Split(*first.NextCursor, ".")[0])
	if err != nil || strings.Contains(string(body), filters.Merchant) || strings.Contains(string(body), category.ID) || strings.Contains(string(body), fixture.token) {
		t.Fatal("cursor exposed filter text, owner metadata or credentials", err)
	}
	for _, mutate := range []func(*PurchaseFilters){
		func(v *PurchaseFilters) { v.From = "2026-11-04" }, func(v *PurchaseFilters) { v.To = "2026-11-08" }, func(v *PurchaseFilters) { v.Currency = "USD" },
		func(v *PurchaseFilters) { v.CategoryID = "other" }, func(v *PurchaseFilters) { v.LocationID = "other" }, func(v *PurchaseFilters) { v.Scope = "direct" },
		func(v *PurchaseFilters) { v.Status = "archived" }, func(v *PurchaseFilters) { v.Merchant = "private purchase filter é" },
	} {
		changed := filters
		mutate(&changed)
		if page, err := p.Page(t.Context(), fixture.token, *first.NextCursor, 1, changed); !errors.Is(err, ErrInvalidCursor) || page.Data != nil || page.NextCursor != nil {
			t.Fatal("purchase cursor adopted another effective filter", err)
		}
	}
	if _, err = p.Page(t.Context(), fixture.token, *first.NextCursor, 2, filters); !errors.Is(err, ErrInvalidCursor) {
		t.Fatal("purchase cursor adopted another page size", err)
	}
	setCatalogTimezone(t, s, "UTC")
	equivalent := filters
	equivalent.Scope = "subtree"
	if page, err := p.Page(t.Context(), fixture.token, *first.NextCursor, 1, equivalent); err != nil || len(page.Data) != 1 {
		t.Fatal("explicit UTC/default scope changed an equivalent continuation", err)
	}
	setCatalogTimezone(t, s, "Asia/Taipei")
	if _, err = p.Page(t.Context(), fixture.token, *first.NextCursor, 1, filters); !errors.Is(err, ErrInvalidCursor) {
		t.Fatal("purchase cursor adopted another civil timezone", err)
	}
	for _, kind := range []string{"item", "lines"} {
		if page, err := readPurchaseCollection(t.Context(), fixture, p, kind, *first.NextCursor, 1); !errors.Is(err, ErrInvalidCursor) || page.Data != nil || page.NextCursor != nil {
			t.Fatal("filtered global cursor entered an owned collection", kind, err)
		}
	}
}

func TestPurchaseReadCursorsRejectOtherOwnersPurposesAndMalformedSignedPositions(t *testing.T) {
	fixture, p := purchaseCommands(t)
	seedPurchaseReadContinuation(t, fixture)
	cursors := make(map[string]string)
	for _, kind := range []string{"global", "item", "lines"} {
		first, err := readPurchaseCollection(t.Context(), fixture, p, kind, "", 1)
		if err != nil || first.NextCursor == nil {
			t.Fatal("cursor fixture missing", kind, err)
		}
		cursors[kind] = *first.NextCursor
	}
	for kind, cursor := range cursors {
		for _, other := range []string{"global", "item", "lines"} {
			if other == kind {
				continue
			}
			if page, err := readPurchaseCollection(t.Context(), fixture, p, other, cursor, 1); !errors.Is(err, ErrInvalidCursor) || page.Data != nil || page.NextCursor != nil {
				t.Fatal("cursor escaped its collection", kind, other, err)
			}
		}
		for _, bad := range []string{"malformed", "a.b.c", cursor + "=", cursor[:len(cursor)-1]} {
			if page, err := readPurchaseCollection(t.Context(), fixture, p, kind, bad, 1); !errors.Is(err, ErrInvalidCursor) || page.Data != nil || page.NextCursor != nil {
				t.Fatal("malformed purchase continuation was accepted", err)
			}
		}
		if _, err := readPurchaseCollection(t.Context(), fixture, p, kind, cursor, 2); !errors.Is(err, ErrInvalidCursor) {
			t.Fatal("owned/global cursor adopted another size", err)
		}
	}
	if _, err := p.PageForItem(t.Context(), fixture.token, fixture.individual.ID, cursors["item"], 1); !errors.Is(err, ErrInvalidCursor) {
		t.Fatal("associated cursor escaped the original Item", err)
	}
	if _, err := p.PageLines(t.Context(), fixture.token, "zz-continuation", cursors["lines"], 1); !errors.Is(err, ErrInvalidCursor) {
		t.Fatal("line cursor escaped its original Purchase", err)
	}
	locations, _ := NewLocations(fixture.auth)
	locationPage, err := locations.Page(t.Context(), fixture.token, "", 1)
	if err != nil || locationPage.NextCursor == nil {
		t.Fatal(err)
	}
	catalogPage, err := fixture.catalog.Page(t.Context(), fixture.token, "", 1, ItemFilters{})
	if err != nil || catalogPage.NextCursor == nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"global", "item", "lines"} {
		for _, cursor := range []string{*locationPage.NextCursor, *catalogPage.NextCursor} {
			if page, err := readPurchaseCollection(t.Context(), fixture, p, kind, cursor, 1); !errors.Is(err, ErrInvalidCursor) || page.Data != nil {
				t.Fatal("another domain's cursor entered purchases", err)
			}
		}
	}
	if _, err = locations.Page(t.Context(), fixture.token, cursors["global"], 1); !errors.Is(err, ErrInvalidCursor) {
		t.Fatal("purchase cursor entered original location namespace", err)
	}
	body, _ := base64.RawURLEncoding.DecodeString(strings.Split(cursors["lines"], ".")[0])
	var value metadataCursor
	if err = json.Unmarshal(body, &value); err != nil {
		t.Fatal(err)
	}
	purpose := "purchase-lines-list-cursor-v1\x00" + projectionPair("lines", fixture.purchase.ID)
	for _, position := range []string{"", strings.Repeat("界", 129)} {
		changed := value
		changed.AfterID = position
		cursor, err := encodeMetadataCursor(fixture.auth, purpose, changed)
		if err != nil {
			t.Fatal(err)
		}
		if page, err := p.PageLines(t.Context(), fixture.token, fixture.purchase.ID, cursor, 1); !errors.Is(err, ErrInvalidCursor) || page.Data != nil || page.NextCursor != nil {
			t.Fatal("signed invalid source identity escaped original bounds", err)
		}
	}
}

func TestPurchaseReadCursorsRequireActiveCompletedSessionAndEpochButPermitFreshLogin(t *testing.T) {
	for _, state := range []string{"fresh login", "expired cursor", "epoch", "mandatory", "revoked", "expired session", "invalid"} {
		t.Run(state, func(t *testing.T) {
			fixture, p := purchaseCommands(t)
			seedPurchaseReadContinuation(t, fixture)
			cursors := make(map[string]string)
			for _, kind := range []string{"global", "item", "lines"} {
				first, err := readPurchaseCollection(t.Context(), fixture, p, kind, "", 1)
				if err != nil || first.NextCursor == nil {
					t.Fatal(err)
				}
				cursors[kind] = *first.NextCursor
			}
			want := ErrAuthentication
			switch state {
			case "fresh login":
				if _, err := fixture.auth.Logout(t.Context(), fixture.token, "logout-purchase-collections"); err != nil {
					t.Fatal(err)
				}
				for kind, cursor := range cursors {
					if page, err := readPurchaseCollection(t.Context(), fixture, p, kind, cursor, 1); !errors.Is(err, ErrAuthentication) || page.Data != nil {
						t.Fatal("cursor revived a revoked cookie", err)
					}
				}
				_, fixture.token = login(t, fixture.auth)
				before := purchaseCommandState(t, fixture.store)
				for kind, cursor := range cursors {
					if page, err := readPurchaseCollection(t.Context(), fixture, p, kind, cursor, 1); err != nil || len(page.Data) != 1 {
						t.Fatal("same-principal fresh login could not continue the original scope", err)
					}
				}
				requirePurchaseCommandState(t, fixture.store, before)
				return
			case "expired cursor":
				now := fixture.auth.now().Add(metadataCursorLifetime)
				fixture.auth.now = func() time.Time { return now }
				want = ErrInvalidCursor
			case "epoch":
				execSQL(t, fixture.store.db, "UPDATE sync_state SET dataset_epoch='new-purchase-epoch'")
				for kind, cursor := range cursors {
					if page, err := readPurchaseCollection(t.Context(), fixture, p, kind, cursor, 1); !errors.Is(err, ErrAuthentication) || page.Data != nil {
						t.Fatal("old epoch session survived", err)
					}
				}
				_, fixture.token = login(t, fixture.auth)
				want = ErrInvalidCursor
			case "mandatory":
				execSQL(t, fixture.store.db, "UPDATE admin_accounts SET must_change_password=1")
				want = ErrPasswordChangeRequired
			case "revoked":
				execSQL(t, fixture.store.db, "UPDATE sessions SET revoked_at=?", stamp(fixture.auth.now()))
			case "expired session":
				execSQL(t, fixture.store.db, "UPDATE sessions SET expires_at=?", stamp(fixture.auth.now()))
			case "invalid":
				fixture.token = "invalid"
			}
			before := purchaseCommandState(t, fixture.store)
			for kind, cursor := range cursors {
				if page, err := readPurchaseCollection(t.Context(), fixture, p, kind, cursor, 1); !errors.Is(err, want) || page.Data != nil || page.NextCursor != nil {
					t.Fatal("unauthorized/expired collection exposed a result", state, kind, err)
				}
				copy := fixture
				copy.token = cursor
				if page, err := readPurchaseCollection(t.Context(), copy, p, kind, "", 1); !errors.Is(err, ErrAuthentication) || page.Data != nil {
					t.Fatal("read cursor became a credential", err)
				}
			}
			requirePurchaseCommandState(t, fixture.store, before)
		})
	}
}

func TestPurchaseReadContinuationsStayLiveAfterGenuineCorrectionAndNewFinancialCommands(t *testing.T) {
	fixture, p := purchaseCommands(t)
	s := fixture.store
	anchor := seedReadPurchase(t, s, PurchaseData{ID: "000-anchor-purchase", MerchantName: "Live Scope", PurchasedAt: "2026-11-06T00:00:00Z", Currency: "JPY"})
	seedReadPurchaseLine(t, s, PurchaseLineData{ID: "anchor-owner-line", PurchaseID: anchor.ID, ItemID: fixture.quantity.ID, QuantityBase: 1, PaidMinor: 0})
	seedReadPurchaseLine(t, s, PurchaseLineData{ID: "000-anchor-line", PurchaseID: fixture.purchase.ID, ItemID: fixture.quantity.ID, QuantityBase: 1, PaidMinor: 0})
	seedPurchaseReadContinuation(t, fixture)
	filters := PurchaseFilters{Merchant: "Live Scope"}
	// A second matching header establishes a real, not fabricated, lookahead.
	execSQL(t, s.db, "UPDATE purchases SET merchant_name='Live Scope' WHERE id='zz-continuation'")
	global, err := p.Page(t.Context(), fixture.token, "", 1, filters)
	if err != nil || global.NextCursor == nil || global.Data[0].ID != anchor.ID {
		t.Fatal(err)
	}
	associated, err := p.PageForItem(t.Context(), fixture.token, fixture.quantity.ID, "", 1)
	if err != nil || associated.NextCursor == nil || associated.Data[0].ID != anchor.ID {
		t.Fatal(err)
	}
	lines, err := p.PageLines(t.Context(), fixture.token, fixture.purchase.ID, "", 1)
	if err != nil || lines.NextCursor == nil || lines.Data[0].ID != "000-anchor-line" {
		t.Fatal(err)
	}
	if _, err = p.Patch(t.Context(), fixture.token, "live-correct-positional-header", anchor.ID, anchor.Revision, PurchasePatch{MerchantName: testPointer("No longer matches")}); err != nil {
		t.Fatal(err)
	}
	created, err := p.Create(t.Context(), fixture.token, "live-created-purchase", PurchaseCreate{MerchantName: "Live Scope", PurchasedAt: "2026-11-06T00:00:00Z", Currency: "JPY"})
	if err != nil {
		t.Fatal(err)
	}
	item := getCatalogItem(t, fixture.catalog, fixture.token, fixture.quantity.ID)
	newLine, err := p.CreateLine(t.Context(), fixture.token, "live-created-existing-owner-line", fixture.purchase.ID, fixture.purchase.Revision, PurchaseLineCreate{ItemID: item.ID, ItemRevision: item.Revision, QuantityBase: 1, PaidMinor: 0})
	if err != nil {
		t.Fatal(err)
	}
	item = getCatalogItem(t, fixture.catalog, fixture.token, item.ID)
	if _, err = p.CreateLine(t.Context(), fixture.token, "live-associate-new-purchase", created.ID, created.Revision, PurchaseLineCreate{ItemID: item.ID, ItemRevision: item.Revision, QuantityBase: 1, PaidMinor: 0}); err != nil {
		t.Fatal(err)
	}
	before := purchaseCommandState(t, s)
	if next, err := p.Page(t.Context(), fixture.token, *global.NextCursor, 1, filters); err != nil || len(next.Data) != 1 || next.Data[0].ID != created.ID {
		t.Fatal("global continuation required old matching metadata or froze the dataset", err)
	}
	if next, err := p.PageForItem(t.Context(), fixture.token, item.ID, *associated.NextCursor, 1); err != nil || len(next.Data) != 1 || next.Data[0].ID != created.ID {
		t.Fatal("associated continuation bound an old aggregate instead of live identity scope", err)
	}
	if next, err := p.PageLines(t.Context(), fixture.token, fixture.purchase.ID, *lines.NextCursor, 1); err != nil || len(next.Data) != 1 || next.Data[0].ID != newLine.ID {
		t.Fatal("independent Line continuation froze an old parent/collection", err)
	}
	requirePurchaseCommandState(t, s, before)
}

func TestPurchaseReadCursorsRetainMaximumUnicodeControlIdentitiesInOriginalSingleIDBudget(t *testing.T) {
	fixture, p := purchaseCommands(t)
	s := fixture.store
	owner := strings.Repeat("\x03", 128)
	seedUnmaterializedCatalogItem(t, s, owner, "Imported owner")
	ids := []string{strings.Repeat("\x00", 128), strings.Repeat("\x01", 128), "new", "tree", " <&\" é ", strings.Repeat("😀", 128)}
	for index, id := range ids {
		seedReadPurchase(t, s, PurchaseData{ID: id, MerchantName: "Imported financial source", PurchasedAt: "2026-11-06T00:00:00Z", Currency: "JPY", ReceiptNote: "\x00Exact note é"})
		seedReadPurchaseLine(t, s, PurchaseLineData{ID: "owned-" + strconv.Itoa(index), PurchaseID: id, ItemID: owner, QuantityBase: 1, PaidMinor: 0})
	}
	for _, id := range ids {
		seedReadPurchaseLine(t, s, PurchaseLineData{ID: id, PurchaseID: "new", ItemID: owner, QuantityBase: 1, PaidMinor: 0})
	}
	before := purchaseCommandState(t, s)
	requirePurchaseIDs(t, allReadPurchases(t, p, fixture.token, "", PurchaseFilters{Merchant: "Imported financial source"}, 1), ids...)
	requirePurchaseIDs(t, allReadPurchases(t, p, fixture.token, owner, PurchaseFilters{}, 1), ids...)
	lines := allReadPurchaseLines(t, p, fixture.token, "new", 1)
	if len(lines) != len(ids)+1 {
		t.Fatal("full Unicode/control Line identity was truncated/normalized")
	}
	requirePurchaseCommandState(t, s, before)
}
