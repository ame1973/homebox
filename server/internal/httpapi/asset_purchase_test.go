package httpapi

import (
	"fmt"
	"net/http"
	"reflect"
	"testing"

	"homebox/internal/store"
)

func TestAssetPurchaseHTTPCommitsActualFinanceThroughVerifiedTLSAndReconcilesOriginalReplies(t *testing.T) {
	var assets *store.Assets
	var token string
	call, login := domainTLSFixture(t, func(auth *store.Auth, session string) {
		var err error
		assets, err = store.NewAssets(auth)
		if err != nil {
			t.Fatal(err)
		}
		token = session
	})
	item := catalogReply(t, call(http.MethodPost, "/api/v1/items", `{"name":"Actual backed financial allocation"}`, "asset-purchase-http-item", ""))
	unit := ownedReply[store.Unit](t, call(http.MethodPost, "/api/v1/items/"+item.ID+"/units", `{"location_id":null}`, "asset-purchase-http-unit", `"1"`), item.ID, 8)
	item = catalogReply(t, call(http.MethodGet, "/api/v1/items/"+item.ID, "", "", ""))
	profile, err := assets.PutProfile(t.Context(), token, "asset-purchase-http-profile", unit.ID, unit.Revision,
		store.AssetProfileWrite{ItemRevision: item.Revision, CostBasis: "purchase", Currency: "JPY", UsageUnit: "times"})
	if err != nil {
		t.Fatal(err)
	}
	item = catalogReply(t, call(http.MethodGet, "/api/v1/items/"+item.ID, "", "", ""))
	parent := purchaseHTTPReply(t, call(http.MethodPost, "/api/v1/purchases", `{"merchant_name":"Actual Asset purchase","purchased_at":"2026-11-07T00:00:00Z","currency":"JPY"}`, "asset-purchase-http-header", ""))
	path := "/api/v1/purchases/" + parent.ID
	body := fmt.Sprintf(`{"item_id":%q,"item_revision":%d,"item_unit_id":%q,"quantity_base":1,"paid_minor":0,"note":"Exact zero é\n"}`, item.ID, item.Revision, unit.ID)
	original := call(http.MethodPost, path+"/lines", body, "asset-purchase-http-zero", `"1"`)
	zero := purchaseLineHTTPReply(t, original, parent.ID, item.ID)
	item = catalogReply(t, call(http.MethodGet, "/api/v1/items/"+item.ID, "", "", ""))
	const maximum int64 = 9007199254740991
	largeBody := fmt.Sprintf(`{"item_id":%q,"item_revision":%d,"item_unit_id":%q,"quantity_base":1,"paid_minor":%d}`, item.ID, item.Revision, unit.ID, maximum)
	large := purchaseLineHTTPReply(t, call(http.MethodPost, path+"/lines", largeBody, "asset-purchase-http-maximum", `"2"`), parent.ID, item.ID)
	if zero.Revision != 1 || zero.PaidMinor != 0 || large.Revision != 1 || large.PaidMinor != maximum || zero.ID == large.ID {
		t.Fatal("verified socket response confused independent Lines, zero history or whole acquisition amounts")
	}
	current, err := assets.Get(t.Context(), token, unit.ID)
	if err != nil || current.Revision != profile.Revision+2 || !reflect.DeepEqual(current.AssetData, profile.AssetData) {
		t.Fatal("public allocation omitted actual Asset authority or changed profile interpretation", current, err)
	}
	summary, err := assets.CostSummary(t.Context(), token, unit.ID, "2026-11-07T00:00:00Z")
	if err != nil || summary.AcquisitionMinor != maximum || summary.NetCostMinor != maximum {
		t.Fatal("publicly committed allocations were not exact actual financial source", summary, err)
	}
	item = catalogReply(t, call(http.MethodGet, "/api/v1/items/"+item.ID, "", "", ""))
	unsafe := fmt.Sprintf(`{"item_id":%q,"item_revision":%d,"item_unit_id":%q,"quantity_base":1,"paid_minor":1}`, item.ID, item.Revision, unit.ID)
	rejected := call(http.MethodPost, path+"/lines", unsafe, "asset-purchase-http-unsafe", `"3"`)
	expectError(t, rejected, 409, "ASSET_TOTAL_OVERFLOW")
	if rejected.Header().Get("ETag") != "" || rejected.Header().Get("Set-Cookie") != "" {
		t.Fatal("rejected financial range exposed a resource/session change")
	}
	if page := ownedPageReply(t, call(http.MethodGet, path+"/lines", "", "", "")); len(page.Data) != 2 || page.NextCursor != nil {
		t.Fatal("unsafe financial command left a partial public Line")
	}
	if after, err := assets.Get(t.Context(), token, unit.ID); err != nil || !reflect.DeepEqual(after, current) {
		t.Fatal("unsafe socket command advanced the financial aggregate", after, err)
	}
	if physical := ownedReply[store.Unit](t, call(http.MethodGet, "/api/v1/units/"+unit.ID, "", "", ""), item.ID, 8); !reflect.DeepEqual(physical, unit) {
		t.Fatal("financial attribution changed independent physical source")
	}
	if response := call(http.MethodPost, "/api/v1/auth/logout", "", "asset-purchase-http-logout", ""); response.Code != 200 {
		t.Fatal("actual logout failed")
	}
	login()
	frozen := call(http.MethodPost, path+"/lines", body, "asset-purchase-http-zero", `"1"`)
	if frozen.Code != 200 || frozen.Body.String() != original.Body.String() || frozen.Header().Get("ETag") != original.Header().Get("ETag") {
		t.Fatal("fresh-session original financial reply was rebased onto current totals/proofs")
	}
	if latest := purchaseHTTPReply(t, call(http.MethodGet, path, "", "", "")); latest.Revision != 3 {
		t.Fatal("replay or rejected command advanced Purchase authority")
	}
	safe := fmt.Sprintf(`{"item_id":%q,"item_revision":%d,"item_unit_id":%q,"quantity_base":1,"paid_minor":0}`, item.ID, item.Revision, unit.ID)
	purchaseLineHTTPReply(t, call(http.MethodPost, path+"/lines", safe, "asset-purchase-http-unsafe", `"3"`), parent.ID, item.ID)
}

func TestAssetPurchaseHTTPRetainsManualBasisAndCurrencyConflictsThroughVerifiedTLS(t *testing.T) {
	for _, basis := range []string{"manual", "purchase"} {
		t.Run(basis, func(t *testing.T) {
			var assets *store.Assets
			var token string
			call, _ := domainTLSFixture(t, func(auth *store.Auth, session string) {
				var err error
				assets, err = store.NewAssets(auth)
				if err != nil {
					t.Fatal(err)
				}
				token = session
			})
			item := catalogReply(t, call(http.MethodPost, "/api/v1/items", `{"name":"Guarded backed allocation"}`, "asset-purchase-guard-item", ""))
			unit := ownedReply[store.Unit](t, call(http.MethodPost, "/api/v1/items/"+item.ID+"/units", `{"location_id":null}`, "asset-purchase-guard-unit", `"1"`), item.ID, 8)
			item = catalogReply(t, call(http.MethodGet, "/api/v1/items/"+item.ID, "", "", ""))
			input := store.AssetProfileWrite{ItemRevision: item.Revision, CostBasis: basis, Currency: "USD", UsageUnit: "times"}
			want := "PURCHASE_CURRENCY_CONFLICT"
			if basis == "manual" {
				var zero int64
				input.Currency, input.ManualAcquisitionMinor, want = "JPY", &zero, "ASSET_COST_BASIS_CONFLICT"
			}
			if _, err := assets.PutProfile(t.Context(), token, "asset-purchase-guard-profile", unit.ID, unit.Revision, input); err != nil {
				t.Fatal(err)
			}
			item = catalogReply(t, call(http.MethodGet, "/api/v1/items/"+item.ID, "", "", ""))
			parent := purchaseHTTPReply(t, call(http.MethodPost, "/api/v1/purchases", `{"merchant_name":"Guarded","purchased_at":"2026-11-07T00:00:00Z","currency":"JPY"}`, "asset-purchase-guard-header", ""))
			body := fmt.Sprintf(`{"item_id":%q,"item_revision":%d,"item_unit_id":%q,"quantity_base":1,"paid_minor":0}`, item.ID, item.Revision, unit.ID)
			expectError(t, call(http.MethodPost, "/api/v1/purchases/"+parent.ID+"/lines", body, "asset-purchase-guard-line", `"1"`), 409, want)
			if page := ownedPageReply(t, call(http.MethodGet, "/api/v1/purchases/"+parent.ID+"/lines", "", "", "")); len(page.Data) != 0 {
				t.Fatal("manual/currency conflict silently created a financial allocation")
			}
		})
	}
}
