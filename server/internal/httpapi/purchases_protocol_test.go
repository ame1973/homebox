package httpapi

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"testing"

	"homebox/internal/store"
)

func TestPurchasesHTTPBothOriginalProofsAreRecheckedAfterBodyBeforeFirstAllocationOrCorrection(t *testing.T) {
	for _, kind := range []string{"Item interpretation", "Purchase currency", "header correction"} {
		t.Run(kind, func(t *testing.T) {
			auth, handler, cookie, csrf := domainHTTPFixture(t)
			p, err := store.NewPurchases(auth)
			if err != nil {
				t.Fatal(err)
			}
			catalog, err := store.NewCatalog(auth)
			if err != nil {
				t.Fatal(err)
			}
			mode, unit, base, scale := "quantity", "kg", "g", int64(1000)
			item, err := catalog.Create(t.Context(), cookie.Value, "proof-before-body-item", store.ItemCreate{Name: "Reviewed measurement", TrackingMode: &mode, UnitCode: &unit, BaseUnit: &base, ScaleFactor: &scale})
			if err != nil {
				t.Fatal(err)
			}
			parent, err := p.Create(t.Context(), cookie.Value, "proof-before-body-purchase", store.PurchaseCreate{MerchantName: "Reviewed", PurchasedAt: "2026-11-06T00:00:00Z", Currency: "JPY"})
			if err != nil {
				t.Fatal(err)
			}
			method, path, body := http.MethodPost, "/api/v1/purchases/"+parent.ID+"/lines", fmt.Sprintf(`{"item_id":%q,"item_revision":1,"item_unit_id":null,"quantity_base":1500,"paid_minor":123}`, item.ID)
			if kind == "header correction" {
				method, path, body = http.MethodPatch, "/api/v1/purchases/"+parent.ID, `{"receipt_note":"Must not overwrite"}`
			}
			r := domainRequest(method, path, body, cookie, csrf, "proof-after-body-original", `"1"`)
			r.Body = io.NopCloser(&beforeRead{Reader: r.Body, before: func() {
				var err error
				if kind == "Item interpretation" {
					unit, base := "L", "ml"
					_, err = catalog.Patch(t.Context(), cookie.Value, "proof-body-changed-item", item.ID, 1, store.ItemPatch{UnitCode: &unit, BaseUnit: &base, ScaleFactor: &scale})
				} else if kind == "Purchase currency" {
					code := "USD"
					_, err = p.Patch(t.Context(), cookie.Value, "proof-body-changed-currency", parent.ID, 1, store.PurchasePatch{Currency: &code})
				} else {
					merchant := "Concurrent merchant"
					_, err = p.Patch(t.Context(), cookie.Value, "proof-body-changed-header", parent.ID, 1, store.PurchasePatch{MerchantName: &merchant})
				}
				if err != nil {
					t.Fatal(err)
				}
			}})
			expectError(t, respond(handler, r), 409, "REVISION_CONFLICT")
			if lines, err := p.PageLines(t.Context(), cookie.Value, parent.ID, "", 50); err != nil || len(lines.Data) != 0 {
				t.Fatal("a stale first proposal created financial history", err)
			}
			current, err := p.Get(t.Context(), cookie.Value, parent.ID)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "header correction" && (current.Revision != 2 || current.MerchantName != "Concurrent merchant" || current.ReceiptNote != "") {
				t.Fatal("stale correction replaced concurrent source")
			}
			if kind == "Purchase currency" && (current.Revision != 2 || current.Currency != "USD") {
				t.Fatal("stale allocation changed actual currency")
			}
			if kind == "Item interpretation" {
				currentItem, err := catalog.Get(t.Context(), cookie.Value, item.ID)
				if err != nil || currentItem.Revision != 2 || currentItem.BaseUnit != "ml" {
					t.Fatal("stale first allocation repaired/reinterpreted current measurement", err)
				}
			}
		})
	}
}

func TestPurchasesHTTPExplicitKeysBindOriginalBodyBothProofsAndOperation(t *testing.T) {
	auth, handler, cookie, csrf := domainHTTPFixture(t)
	p, _ := store.NewPurchases(auth)
	catalog, _ := store.NewCatalog(auth)
	item, err := catalog.Create(t.Context(), cookie.Value, "purchase-key-owner", store.ItemCreate{Name: "Owner"})
	if err != nil {
		t.Fatal(err)
	}
	parent, err := p.Create(t.Context(), cookie.Value, "purchase-key-parent", store.PurchaseCreate{MerchantName: "Merchant", PurchasedAt: "2026-11-06T00:00:00Z", Currency: "JPY"})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/purchases/" + parent.ID
	body := fmt.Sprintf(`{"item_id":%q,"item_revision":1,"item_unit_id":null,"quantity_base":1,"paid_minor":0}`, item.ID)
	original := respond(handler, domainRequest(http.MethodPost, path+"/lines", body, cookie, csrf, "purchase-original-key", `"1"`))
	purchaseLineHTTPReply(t, original, parent.ID, item.ID)
	merchant := "Later corrected"
	if _, err = p.Patch(t.Context(), cookie.Value, "purchase-key-later-source", parent.ID, 2, store.PurchasePatch{MerchantName: &merchant}); err != nil {
		t.Fatal(err)
	}
	replay := respond(handler, domainRequest(http.MethodPost, path+"/lines", body, cookie, csrf, "purchase-original-key", `"1"`))
	if replay.Code != 200 || replay.Body.String() != original.Body.String() || replay.Header().Get("ETag") != original.Header().Get("ETag") {
		t.Fatal("exact original-key replay became new spend/current source")
	}
	for _, input := range []struct{ method, path, body, proof string }{
		{http.MethodPost, path + "/lines", fmt.Sprintf(`{"item_id":%q,"item_revision":2,"item_unit_id":null,"quantity_base":1,"paid_minor":0}`, item.ID), `"1"`},
		{http.MethodPost, path + "/lines", fmt.Sprintf(`{"item_id":%q,"item_revision":1,"item_unit_id":null,"quantity_base":1,"paid_minor":1}`, item.ID), `"1"`},
		{http.MethodPost, path + "/lines", body, `"2"`},
		{http.MethodPatch, path, `{"receipt_note":"Different operation"}`, `"3"`},
	} {
		expectError(t, respond(handler, domainRequest(input.method, input.path, input.body, cookie, csrf, "purchase-original-key", input.proof)), 409, "IDEMPOTENCY_CONFLICT")
	}
	if lines, err := p.PageLines(t.Context(), cookie.Value, parent.ID, "", 50); err != nil || len(lines.Data) != 1 {
		t.Fatal("key conflict duplicated financial history", err)
	}
}

func TestPurchasesHTTPDefaultPagesFinishAllThreeActualCollectionsWithoutAdmissionOverrides(t *testing.T) {
	auth, handler, cookie, csrf := domainHTTPFixture(t)
	p, _ := store.NewPurchases(auth)
	catalog, _ := store.NewCatalog(auth)
	item, err := catalog.Create(t.Context(), cookie.Value, "purchase-default-owner", store.ItemCreate{Name: "Owner"})
	if err != nil {
		t.Fatal(err)
	}
	var first store.Purchase
	// Use real command services to establish authority/history, never SQL fixtures,
	// fake receipts, request flooding or a production limiter override.
	for index := range 51 {
		parent, err := p.Create(t.Context(), cookie.Value, fmt.Sprintf("purchase-default-header-%d", index), store.PurchaseCreate{MerchantName: "Merchant", PurchasedAt: "2026-11-06T00:00:00Z", Currency: "JPY"})
		if err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			first = parent
		}
		if _, err = p.CreateLine(t.Context(), cookie.Value, fmt.Sprintf("purchase-default-link-%d", index), parent.ID, 1, store.PurchaseLineCreate{ItemID: item.ID, ItemRevision: int64(index + 1), QuantityBase: 1, PaidMinor: 0}); err != nil {
			t.Fatal(err)
		}
	}
	for index := range 50 {
		if _, err = p.CreateLine(t.Context(), cookie.Value, fmt.Sprintf("purchase-default-owned-line-%d", index), first.ID, int64(index+2), store.PurchaseLineCreate{ItemID: item.ID, ItemRevision: int64(index + 52), QuantityBase: 1, PaidMinor: 0}); err != nil {
			t.Fatal(err)
		}
	}
	cursors := make(map[string]string)
	paths := []string{"/api/v1/purchases", "/api/v1/items/" + item.ID + "/purchases", "/api/v1/purchases/" + first.ID + "/lines"}
	for _, path := range paths {
		page := ownedPageReply(t, respond(handler, domainRequest(http.MethodGet, path, "", cookie, csrf, "", "")))
		if len(page.Data) != 50 || page.NextCursor == nil {
			t.Fatal("purchase collection lost its default complete 50-row page", path)
		}
		cursors[path] = *page.NextCursor
		last := ownedPageReply(t, respond(handler, domainRequest(http.MethodGet, path+"?cursor="+url.QueryEscape(*page.NextCursor), "", cookie, csrf, "", "")))
		if len(last.Data) != 1 || last.NextCursor != nil {
			t.Fatal("purchase collection did not finish its remaining actual row", path)
		}
	}
	for _, path := range paths {
		for other, cursor := range cursors {
			if other == path {
				continue
			}
			expectError(t, respond(handler, domainRequest(http.MethodGet, path+"?cursor="+url.QueryEscape(cursor), "", cookie, csrf, "", "")), 400, "INVALID_CURSOR")
		}
	}
	if current := purchaseHTTPReply(t, respond(handler, domainRequest(http.MethodGet, "/api/v1/purchases/"+first.ID, "", cookie, csrf, "", ""))); current.Revision != 52 {
		t.Fatal("Line paging invented an aggregate or independent revision", strconv.FormatInt(current.Revision, 10))
	}
}
