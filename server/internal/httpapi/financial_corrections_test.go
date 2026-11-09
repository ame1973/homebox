package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"homebox/internal/store"
)

func exerciseFinancialCorrectionsHTTP(t *testing.T, call func(string, string, string, string, string) *httptest.ResponseRecorder, freshLogin func()) {
	t.Helper()
	quantity := catalogReply(t, call(http.MethodPost, "/api/v1/items", `{"name":"Corrected quantity","tracking_mode":"quantity","unit_code":"kg","base_unit":"g","scale_factor":1000}`, "correction-http-item", ""))
	purchase := purchaseHTTPReply(t, call(http.MethodPost, "/api/v1/purchases", `{"merchant_name":"Exact merchant","purchased_at":"2026-11-07T12:00:00.123456789+08:00","currency":"JPY"}`, "correction-http-purchase", ""))
	purchasePath := "/api/v1/purchases/" + purchase.ID
	lineBody := fmt.Sprintf(`{"item_id":%q,"item_revision":%d,"item_unit_id":null,"quantity_base":1500,"paid_minor":0}`, quantity.ID, quantity.Revision)
	line := purchaseLineHTTPReply(t, call(http.MethodPost, purchasePath+"/lines", lineBody, "correction-http-original-line", `"1"`), purchase.ID, quantity.ID)
	quantity = catalogReply(t, call(http.MethodGet, "/api/v1/items/"+quantity.ID, "", "", ""))
	lineCorrection := fmt.Sprintf(`{"line_id":%q,"line_revision":%d,"item_revision":%d,"action":"replace","replacement":{"item_id":%q,"item_revision":%d,"item_unit_id":null,"quantity_base":2500,"paid_minor":99,"note":"Exact replacement"},"reason":"\u0000Correct allocation"}`, line.ID, line.Revision, quantity.Revision, quantity.ID, quantity.Revision)
	lineReply := call(http.MethodPost, purchasePath+"/line-corrections", lineCorrection, "correction-http-replace-line", `"2"`)
	firstLine := assetHTTPReply[store.PurchaseLineCorrection](t, lineReply, 8)
	if firstLine.Revision != 1 || firstLine.PurchaseID != purchase.ID || firstLine.Before.ID != line.ID || firstLine.Before.PaidMinor != 0 || firstLine.After == nil || firstLine.After.ID == line.ID || firstLine.After.Revision != 1 || firstLine.After.QuantityBase != 2500 || firstLine.After.PaidMinor != 99 || firstLine.Reason != "\x00Correct allocation" {
		t.Fatal("allocation correction confused original/entry/replacement proofs or changed exact reviewed intent")
	}
	if page := ownedPageReply(t, call(http.MethodGet, purchasePath+"/lines", "", "", "")); len(page.Data) != 1 || page.NextCursor != nil {
		t.Fatal("voided original remained in the current Line collection")
	}
	expectError(t, call(http.MethodPost, purchasePath+"/line-corrections", strings.Replace(lineCorrection, "Correct allocation", "Changed allocation", 1), "correction-http-replace-line", `"2"`), 409, "IDEMPOTENCY_CONFLICT")
	expectError(t, call(http.MethodPost, purchasePath+"/line-corrections", lineCorrection, "correction-http-replace-line", `"3"`), 409, "IDEMPOTENCY_CONFLICT")
	expectError(t, call(http.MethodPost, purchasePath+"/line-corrections", lineCorrection, "correction-http-fresh-double-void", `"3"`), 409, "PURCHASE_LINE_VOIDED")
	voidLine := fmt.Sprintf(`{"line_id":%q,"line_revision":%d,"item_revision":%d,"action":"void","replacement":null,"reason":"Remove replacement"}`, firstLine.After.ID, firstLine.After.Revision, quantity.Revision+1)
	secondLine := assetHTTPReply[store.PurchaseLineCorrection](t, call(http.MethodPost, purchasePath+"/line-corrections", voidLine, "correction-http-void-line", `"3"`), 8)
	if secondLine.After != nil || !reflect.DeepEqual(secondLine.Before, *firstLine.After) || secondLine.Revision != 1 {
		t.Fatal("explicit void was converted to zero or lost independently reviewed replacement")
	}
	if page := ownedPageReply(t, call(http.MethodGet, purchasePath+"/lines", "", "", "")); len(page.Data) != 0 {
		t.Fatal("current Line collection resurrected a financially voided source")
	}
	if page := ownedPageReply(t, call(http.MethodGet, "/api/v1/items/"+quantity.ID+"/purchases", "", "", "")); len(page.Data) != 0 {
		t.Fatal("voided source remained current Item purchase evidence")
	}
	linePage := ownedPageReply(t, call(http.MethodGet, purchasePath+"/line-corrections?limit=1", "", "", ""))
	if len(linePage.Data) != 1 || linePage.NextCursor == nil {
		t.Fatal("actual correction history did not sign lookahead")
	}
	lineLast := ownedPageReply(t, call(http.MethodGet, purchasePath+"/line-corrections?limit=1&cursor="+url.QueryEscape(*linePage.NextCursor), "", "", ""))
	if len(lineLast.Data) != 1 || lineLast.NextCursor != nil {
		t.Fatal("actual correction history continuation was incomplete")
	}

	item := catalogReply(t, call(http.MethodPost, "/api/v1/items", `{"name":"Financial correction Unit"}`, "correction-http-asset-item", ""))
	unit := ownedReply[store.Unit](t, call(http.MethodPost, "/api/v1/items/"+item.ID+"/units", `{"location_id":null}`, "correction-http-unit", `"1"`), item.ID, 8)
	item = catalogReply(t, call(http.MethodGet, "/api/v1/items/"+item.ID, "", "", ""))
	unitPath := "/api/v1/units/" + unit.ID
	profile := fmt.Sprintf(`{"item_revision":%d,"asset_revision":null,"cost_basis":"manual","currency":"JPY","manual_acquisition_minor":100,"in_service_at":null,"retired_at":null,"estimated_residual_minor":50,"usage_unit":"times"}`, item.Revision)
	assetHTTPReply[store.Asset](t, call(http.MethodPut, unitPath+"/asset", profile, "correction-http-profile", `"1"`), 9)
	disposalBody := `{"unit_revision":1,"kind":"sold","disposed_at":"2026-11-07T12:00:00.123456789+08:00","realized_proceeds_minor":200,"currency":"JPY","note":"Original disposal"}`
	original := assetHTTPReply[store.Disposal](t, call(http.MethodPost, unitPath+"/disposal", disposalBody, "correction-http-original-disposal", `"1"`), 7)
	physical := ownedReply[store.Unit](t, call(http.MethodGet, unitPath, "", "", ""), item.ID, 8)
	retained := assetHTTPReply[store.DisposalRecord](t, call(http.MethodGet, unitPath+"/disposal-record", "", "", ""), 4)
	if retained.Revision != 1 || retained.Current == nil || *retained.Current != original.DisposalData || retained.Original != original.DisposalData || physical.Status != "disposed" || physical.Revision != 2 {
		t.Fatal("retained original/current or independent physical/Disposal authority was invented")
	}
	body := `{"asset_revision":2,"action":"replace","replacement":{"kind":"sold","disposed_at":"2026-11-07T04:00:00.123456789Z","realized_proceeds_minor":0,"currency":"JPY","note":"Explicit zero"},"reason":"Correct the sale"}`
	disposalReply := call(http.MethodPost, unitPath+"/disposal-corrections", body, "correction-http-zero-disposal", `"1"`)
	firstDisposal := assetHTTPReply[store.DisposalCorrection](t, disposalReply, 8)
	if firstDisposal.Revision != 1 || !reflect.DeepEqual(firstDisposal.Before, retained) || firstDisposal.After.Revision != 2 || firstDisposal.After.Current == nil || firstDisposal.After.Current.RealizedProceedsMinor != 0 || firstDisposal.After.Current.DisposedAt != original.DisposedAt {
		t.Fatal("zero/equivalent financial decision lost original independent proof/date or became void")
	}
	if current := assetHTTPReply[store.Disposal](t, call(http.MethodGet, unitPath+"/disposal", "", "", ""), 7); current.Revision != 2 || current.RealizedProceedsMinor != 0 {
		t.Fatal("current-disposal GET did not expose actual zero decision")
	}
	if summary := assetSummaryHTTPReply(t, call(http.MethodGet, unitPath+"/cost-summary", "", "", "")); summary.NetCostMinor != 100 || summary.RealizedProceedsMinor != 0 || summary.ProjectedNetCostMinor == nil || *summary.ProjectedNetCostMinor != 50 {
		t.Fatal("zero proceeds were mixed with residual or source calculation")
	}
	expectError(t, call(http.MethodPost, unitPath+"/disposal-corrections", strings.Replace(body, "Correct the sale", "Different reviewed reason", 1), "correction-http-zero-disposal", `"1"`), 409, "IDEMPOTENCY_CONFLICT")
	voidDisposal := `{"asset_revision":3,"action":"void","replacement":null,"reason":"Explicit financial void"}`
	secondDisposal := assetHTTPReply[store.DisposalCorrection](t, call(http.MethodPost, unitPath+"/disposal-corrections", voidDisposal, "correction-http-void-disposal", `"2"`), 8)
	if secondDisposal.After.Current != nil || secondDisposal.After.Revision != 3 || !reflect.DeepEqual(secondDisposal.Before, firstDisposal.After) {
		t.Fatal("financial void discarded retained proof or invented current zero")
	}
	expectError(t, call(http.MethodGet, unitPath+"/disposal", "", "", ""), 404, "NOT_FOUND")
	voided := assetHTTPReply[store.DisposalRecord](t, call(http.MethodGet, unitPath+"/disposal-record", "", "", ""), 4)
	if !reflect.DeepEqual(voided, secondDisposal.After) || voided.Original != retained.Original {
		t.Fatal("voided original financial subject became unreadable or changed")
	}
	if summary := assetSummaryHTTPReply(t, call(http.MethodGet, unitPath+"/cost-summary", "", "", "")); summary.RealizedProceedsMinor != 0 || summary.NetCostMinor != 100 {
		t.Fatal("void did not remove only current financial effect")
	}
	doubleVoid := strings.Replace(voidDisposal, `"asset_revision":3`, `"asset_revision":4`, 1)
	expectError(t, call(http.MethodPost, unitPath+"/disposal-corrections", doubleVoid, "correction-http-fresh-double-void-disposal", `"3"`), 409, "ASSET_HISTORY_LOCKED")
	ordinaryAfterVoid := strings.Replace(disposalBody, `"unit_revision":1`, fmt.Sprintf(`"unit_revision":%d`, physical.Revision), 1)
	expectError(t, call(http.MethodPost, unitPath+"/disposal", ordinaryAfterVoid, "correction-http-recreate-disposal", `"4"`), 409, "ASSET_HISTORY_LOCKED")
	reinstate := `{"asset_revision":4,"action":"replace","replacement":{"kind":"sold","disposed_at":"2026-11-07T12:00:00.123456789+08:00","realized_proceeds_minor":300,"currency":"JPY"},"reason":"Explicitly reinstate"}`
	thirdDisposal := assetHTTPReply[store.DisposalCorrection](t, call(http.MethodPost, unitPath+"/disposal-corrections", reinstate, "correction-http-reinstate-disposal", `"3"`), 8)
	if thirdDisposal.Before.Current != nil || thirdDisposal.After.Revision != 4 || thirdDisposal.After.Current == nil || thirdDisposal.After.Current.RealizedProceedsMinor != 300 || thirdDisposal.After.Original != retained.Original {
		t.Fatal("reinstatement altered original history or borrowed a different proof")
	}
	cursor := ""
	for index := range 3 {
		query := "?limit=1"
		if cursor != "" {
			query += "&cursor=" + url.QueryEscape(cursor)
		}
		page := ownedPageReply(t, call(http.MethodGet, unitPath+"/disposal-corrections"+query, "", "", ""))
		if len(page.Data) != 1 || (index < 2) != (page.NextCursor != nil) {
			t.Fatal("disposal decision history omitted its actual scoped page/continuation", index)
		}
		if page.NextCursor != nil {
			cursor = *page.NextCursor
		}
	}
	if current := ownedReply[store.Unit](t, call(http.MethodGet, unitPath, "", "", ""), item.ID, 8); !reflect.DeepEqual(current, physical) {
		t.Fatal("financial corrections changed physical Unit or placement")
	}
	if summary := assetSummaryHTTPReply(t, call(http.MethodGet, unitPath+"/cost-summary", "", "", "")); summary.NetCostMinor != -200 || summary.RealizedProceedsMinor != 300 || summary.ProjectedNetCostMinor == nil || *summary.ProjectedNetCostMinor != 50 {
		t.Fatal("reinstated current proceeds were clamped or converted to residual")
	}
	if freshLogin != nil {
		if response := call(http.MethodPost, "/api/v1/auth/logout", "", "correction-http-logout", ""); response.Code != 200 {
			t.Fatal("correction-session logout failed")
		}
		freshLogin()
	}
	for _, retry := range []struct {
		path, body, key, proof string
		original               *httptest.ResponseRecorder
	}{
		{purchasePath + "/line-corrections", lineCorrection, "correction-http-replace-line", `"2"`, lineReply},
		{unitPath + "/disposal-corrections", body, "correction-http-zero-disposal", `"1"`, disposalReply},
	} {
		reply := call(http.MethodPost, retry.path, retry.body, retry.key, retry.proof)
		if reply.Code != 200 || reply.Body.String() != retry.original.Body.String() || reply.Header().Get("ETag") != retry.original.Header().Get("ETag") {
			t.Fatal("explicit original-key correction replay was rebased onto later source")
		}
	}
	if current := assetHTTPReply[store.DisposalRecord](t, call(http.MethodGet, unitPath+"/disposal-record", "", "", ""), 4); !reflect.DeepEqual(current, thirdDisposal.After) {
		t.Fatal("frozen historical response replaced current financial authority")
	}
}

func TestFinancialCorrectionsHTTPAllFiveOperationsUseActualServicesAndIndependentProofs(t *testing.T) {
	_, handler, cookie, csrf := domainHTTPFixture(t)
	call := func(method, path, body, key, proof string) *httptest.ResponseRecorder {
		return respond(handler, domainRequest(method, path, body, cookie, csrf, key, proof))
	}
	exerciseFinancialCorrectionsHTTP(t, call, nil)
}

func TestFinancialCorrectionsHTTPAllFiveOperationsThroughVerifiedTLSAndFreshLogin(t *testing.T) {
	call, login := domainTLSFixture(t)
	exerciseFinancialCorrectionsHTTP(t, call, login)
}
