package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"testing"

	"homebox/internal/store"
)

func purchaseHTTPReply(t *testing.T, response *httptest.ResponseRecorder) store.Purchase {
	t.Helper()
	var value store.Purchase
	var fields map[string]json.RawMessage
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &value) != nil || json.Unmarshal(response.Body.Bytes(), &fields) != nil || len(fields) != 6 || value.ID == "" || value.Revision < 1 || response.Header().Get("ETag") != strconv.Quote(strconv.FormatInt(value.Revision, 10)) || response.Header().Get("Set-Cookie") != "" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("invalid Purchase reply: %d %s", response.Code, response.Body.String())
	}
	return value
}
func purchaseLineHTTPReply(t *testing.T, response *httptest.ResponseRecorder, parent, item string) store.PurchaseLine {
	t.Helper()
	var value store.PurchaseLine
	var fields map[string]json.RawMessage
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &value) != nil || json.Unmarshal(response.Body.Bytes(), &fields) != nil || len(fields) != 8 || value.PurchaseID != parent || value.ItemID != item || value.ID == "" || value.Revision < 1 || response.Header().Get("ETag") != strconv.Quote(strconv.FormatInt(value.Revision, 10)) || response.Header().Get("Set-Cookie") != "" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("invalid independent PurchaseLine reply: %d %s", response.Code, response.Body.String())
	}
	return value
}
func exercisePurchasesHTTP(t *testing.T, call func(string, string, string, string, string) *httptest.ResponseRecorder, freshLogin func()) {
	t.Helper()
	quantity := catalogReply(t, call(http.MethodPost, "/api/v1/items", `{"name":"Purchase quantity","tracking_mode":"quantity","unit_code":"kg","base_unit":"g","scale_factor":1000}`, "purchase-http-quantity", ""))
	individual := catalogReply(t, call(http.MethodPost, "/api/v1/items", `{"name":"Purchase individual"}`, "purchase-http-individual", ""))
	unit := ownedReply[store.Unit](t, call(http.MethodPost, "/api/v1/items/"+individual.ID+"/units", `{"location_id":null}`, "purchase-http-unit", `"1"`), individual.ID, 8)
	body := `{"merchant_name":"  Merchant é  ","purchased_at":"2026-11-06T12:00:00.123456789+08:00","currency":"JPY","receipt_note":"\u0000Exact receipt é\n"}`
	created := call(http.MethodPost, "/api/v1/purchases", body, "purchase-http-original-header", "")
	parent := purchaseHTTPReply(t, created)
	path := "/api/v1/purchases/" + parent.ID
	if parent.Revision != 1 || parent.PurchasedAt != "2026-11-06T04:00:00.123456789Z" || parent.ReceiptNote != "\x00Exact receipt é\n" {
		t.Fatal("header creation repaired source, guessed a total or lost independent identity")
	}
	if current := purchaseHTTPReply(t, call(http.MethodGet, path, "", "", "")); current != parent {
		t.Fatal("actual header GET differs from committed source")
	}
	if page := ownedPageReply(t, call(http.MethodGet, path+"/lines", "", "", "")); len(page.Data) != 0 || page.NextCursor != nil {
		t.Fatal("header creation implicitly allocated money")
	}
	lineBody := fmt.Sprintf(`{"item_id":%q,"item_revision":1.0,"item_unit_id":null,"quantity_base":1.5e3,"paid_minor":-0.0e999,"note":"\u0000Exact allocation é\n"}`, quantity.ID)
	allocated := call(http.MethodPost, path+"/lines", lineBody, "purchase-http-original-line", `"1"`)
	line := purchaseLineHTTPReply(t, allocated, parent.ID, quantity.ID)
	if line.Revision != 1 || line.ItemUnitID != nil || line.QuantityBase != 1500 || line.PaidMinor != 0 || line.Note != "\x00Exact allocation é\n" {
		t.Fatal("allocation guessed a Unit, multiplied money or confused independent Line and parent proofs")
	}
	individual = catalogReply(t, call(http.MethodGet, "/api/v1/items/"+individual.ID, "", "", ""))
	unitBody := fmt.Sprintf(`{"item_id":%q,"item_revision":%d,"item_unit_id":%q,"quantity_base":1,"paid_minor":120000}`, individual.ID, individual.Revision, unit.ID)
	unitLine := purchaseLineHTTPReply(t, call(http.MethodPost, path+"/lines", unitBody, "purchase-http-named-unit-line", `"2"`), parent.ID, individual.ID)
	if unitLine.ItemUnitID == nil || *unitLine.ItemUnitID != unit.ID || unitLine.QuantityBase != 1 || unitLine.PaidMinor != 120000 || unitLine.Revision != 1 {
		t.Fatal("named Unit allocation lost explicit ownership or full minor amount")
	}
	currentQuantity := catalogReply(t, call(http.MethodGet, "/api/v1/items/"+quantity.ID, "", "", ""))
	purchaseLineHTTPReply(t, call(http.MethodPost, path+"/lines", fmt.Sprintf(`{"item_id":%q,"item_revision":%d,"item_unit_id":null,"quantity_base":17,"paid_minor":123}`, quantity.ID, currentQuantity.Revision), "purchase-http-another-line", `"3"`), parent.ID, quantity.ID)
	current := purchaseHTTPReply(t, call(http.MethodGet, path, "", "", ""))
	if current.Revision != 4 {
		t.Fatal("allocation did not advance actual Purchase aggregate independently")
	}
	expectError(t, call(http.MethodPost, path+"/lines", lineBody, "purchase-http-stale-item", `"4"`), 409, "REVISION_CONFLICT")
	expectError(t, call(http.MethodPatch, path, `{"receipt_note":"Stale"}`, "purchase-http-stale-parent", `"1"`), 409, "REVISION_CONFLICT")
	expectError(t, call(http.MethodPatch, path, `{"currency":"USD"}`, "purchase-http-locked-currency", `"4"`), 409, "PURCHASE_CURRENCY_LOCKED")
	patchBody := `{"merchant_name":"Corrected Merchant","receipt_note":""}`
	correctedReply := call(http.MethodPatch, path, patchBody, "purchase-http-original-correction", `"4"`)
	corrected := purchaseHTTPReply(t, correctedReply)
	if corrected.Revision != 5 || corrected.Currency != "JPY" || corrected.ReceiptNote != "" || corrected.PurchasedAt != parent.PurchasedAt {
		t.Fatal("partial correction changed omitted finance/source")
	}
	if noOp := purchaseHTTPReply(t, call(http.MethodPatch, path, `{"receipt_note":""}`, "purchase-http-noop-correction", `"5"`)); noOp != corrected {
		t.Fatal("equal header correction invented a revision")
	}
	cursor := ""
	count := 0
	for {
		query := "?limit=1"
		if cursor != "" {
			query += "&cursor=" + url.QueryEscape(cursor)
		}
		page := ownedPageReply(t, call(http.MethodGet, path+"/lines"+query, "", "", ""))
		count += len(page.Data)
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	if count != 3 {
		t.Fatal("complete independent Line collection was truncated/duplicated")
	}
	for _, item := range []string{quantity.ID, individual.ID} {
		if page := ownedPageReply(t, call(http.MethodGet, "/api/v1/items/"+item+"/purchases", "", "", "")); len(page.Data) != 1 || page.NextCursor != nil {
			t.Fatal("Item association duplicated a header per Line or hid a real purchase")
		}
	}
	query := url.Values{"from": {"2026-11-06"}, "to": {"2026-11-06"}, "currency": {"JPY"}, "merchant": {"Corrected Merchant"}, "status": {"active"}, "scope": {"subtree"}}
	if page := ownedPageReply(t, call(http.MethodGet, "/api/v1/purchases?"+query.Encode(), "", "", "")); len(page.Data) != 1 {
		t.Fatal("header/Item/civil filters lost actual allocation source")
	}
	if page := ownedPageReply(t, call(http.MethodGet, "/api/v1/inventory", "", "", "")); len(page.Data) != 0 {
		t.Fatal("financial allocation implicitly received Stock")
	}
	if physical := ownedReply[store.Unit](t, call(http.MethodGet, "/api/v1/units/"+unit.ID, "", "", ""), individual.ID, 8); !reflect.DeepEqual(physical, unit) {
		t.Fatal("financial allocation altered independent physical Unit")
	}
	expectError(t, call(http.MethodPost, "/api/v1/purchases", `{"merchant_name":"Unsupported","purchased_at":"2026-11-06T00:00:00Z","currency":"XAU"}`, "purchase-http-unsupported-currency", ""), 409, "PURCHASE_CURRENCY_CONFLICT")
	purchaseHTTPReply(t, call(http.MethodPost, "/api/v1/purchases", `{"merchant_name":"Unallocated","purchased_at":"2026-11-06T00:00:00Z","currency":"TWD"}`, "purchase-http-empty-header", ""))
	first := ownedPageReply(t, call(http.MethodGet, "/api/v1/purchases?limit=1", "", "", ""))
	if first.NextCursor == nil {
		t.Fatal("global purchase list did not sign lookahead")
	}
	last := ownedPageReply(t, call(http.MethodGet, "/api/v1/purchases?limit=1&cursor="+url.QueryEscape(*first.NextCursor), "", "", ""))
	if len(last.Data) != 1 || last.NextCursor != nil {
		t.Fatal("global continuation was incomplete")
	}
	if freshLogin != nil {
		if response := call(http.MethodPost, "/api/v1/auth/logout", "", "purchase-http-logout", ""); response.Code != 200 {
			t.Fatal("purchase logout failed")
		}
		freshLogin()
	}
	for _, retry := range []struct {
		method, path, body, key, proof string
		original                       *httptest.ResponseRecorder
	}{
		{http.MethodPost, "/api/v1/purchases", body, "purchase-http-original-header", "", created},
		{http.MethodPost, path + "/lines", lineBody, "purchase-http-original-line", `"1"`, allocated},
		{http.MethodPatch, path, patchBody, "purchase-http-original-correction", `"4"`, correctedReply},
	} {
		reply := call(retry.method, retry.path, retry.body, retry.key, retry.proof)
		if reply.Code != 200 || reply.Body.String() != retry.original.Body.String() || reply.Header().Get("ETag") != retry.original.Header().Get("ETag") {
			t.Fatal("explicit original-key retry was rebased onto later finance/source")
		}
	}
	if latest := purchaseHTTPReply(t, call(http.MethodGet, path, "", "", "")); latest != corrected {
		t.Fatal("historical replies replaced current source")
	}
}

func TestPurchasesHTTPAllSevenOperationsUseActualServicesDualProofsAndIndependentETags(t *testing.T) {
	_, handler, cookie, csrf := domainHTTPFixture(t)
	call := func(method, path, body, key, proof string) *httptest.ResponseRecorder {
		return respond(handler, domainRequest(method, path, body, cookie, csrf, key, proof))
	}
	exercisePurchasesHTTP(t, call, nil)
}
func TestPurchasesHTTPAllSevenOperationsThroughVerifiedTLSAndFreshLogin(t *testing.T) {
	call, login := domainTLSFixture(t)
	exercisePurchasesHTTP(t, call, login)
}
