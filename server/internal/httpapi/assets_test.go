package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"

	"homebox/internal/store"
)

func assetHTTPReply[T any](t *testing.T, response *httptest.ResponseRecorder, fieldCount int) T {
	t.Helper()
	var value T
	var metadata struct {
		Revision int64 `json:"revision"`
	}
	var fields map[string]json.RawMessage
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &value) != nil || json.Unmarshal(response.Body.Bytes(), &metadata) != nil ||
		json.Unmarshal(response.Body.Bytes(), &fields) != nil || len(fields) != fieldCount || metadata.Revision < 1 || metadata.Revision > maxJSONInteger ||
		response.Header().Get("ETag") != strconv.Quote(strconv.FormatInt(metadata.Revision, 10)) || response.Header().Get("Set-Cookie") != "" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("invalid actual independently versioned Asset resource: %d %s", response.Code, response.Body.String())
	}
	return value
}

func assetSummaryHTTPReply(t *testing.T, response *httptest.ResponseRecorder) store.AssetCostSummary {
	t.Helper()
	var value store.AssetCostSummary
	var fields map[string]json.RawMessage
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &value) != nil || json.Unmarshal(response.Body.Bytes(), &fields) != nil || len(fields) != 20 ||
		response.Header().Get("ETag") != "" || response.Header().Get("Set-Cookie") != "" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("invalid actual Asset calculation reply: %d %s", response.Code, response.Body.String())
	}
	return value
}

func exerciseAssetsHTTP(t *testing.T, call func(string, string, string, string, string) *httptest.ResponseRecorder, freshLogin func()) {
	t.Helper()
	location := responseLocation(t, call(http.MethodPost, "/api/v1/locations", `{"code":"ASSET-HOME","name":"Asset location","parent_id":null}`, "asset-http-location", ""))
	item := catalogReply(t, call(http.MethodPost, "/api/v1/items", `{"name":"Actual financial Unit"}`, "asset-http-item", ""))
	unit := ownedReply[store.Unit](t, call(http.MethodPost, "/api/v1/items/"+item.ID+"/units", fmt.Sprintf(`{"location_id":%q}`, location.ID), "asset-http-unit", `"1"`), item.ID, 8)
	item = catalogReply(t, call(http.MethodGet, "/api/v1/items/"+item.ID, "", "", ""))
	path := "/api/v1/units/" + unit.ID
	expectError(t, call(http.MethodGet, path+"/asset", "", "", ""), 404, "NOT_FOUND")
	expectError(t, call(http.MethodGet, path+"/disposal", "", "", ""), 404, "NOT_FOUND")
	body := fmt.Sprintf(`{"item_revision":%d,"asset_revision":null,"cost_basis":"manual","currency":"JPY","manual_acquisition_minor":100,"in_service_at":"2024-11-07T12:00:00+08:00","retired_at":null,"estimated_residual_minor":20,"usage_unit":"times"}`, item.Revision)
	createdReply := call(http.MethodPut, path+"/asset", body, "asset-http-original-profile", `"1"`)
	created := assetHTTPReply[store.Asset](t, createdReply, 9)
	if created.Revision != 1 || created.ItemUnitID != unit.ID || created.InServiceAt == nil || *created.InServiceAt != "2024-11-07T04:00:00.000000000Z" || created.ManualAcquisitionMinor == nil || *created.ManualAcquisitionMinor != 100 {
		t.Fatal("profile lost complete intent, actual owner or independent new revision", created)
	}
	if current := assetHTTPReply[store.Asset](t, call(http.MethodGet, path+"/asset", "", "", ""), 9); !reflect.DeepEqual(current, created) {
		t.Fatal("actual profile read did not match committed source")
	}
	if page := ownedPageReply(t, call(http.MethodGet, "/api/v1/assets?currency=JPY&status=active", "", "", "")); len(page.Data) != 1 || page.NextCursor != nil {
		t.Fatal("global Asset page omitted actual source or invented a continuation")
	}
	for _, collection := range []string{"usage", "costs"} {
		if page := ownedPageReply(t, call(http.MethodGet, path+"/"+collection, "", "", "")); len(page.Data) != 0 || page.NextCursor != nil {
			t.Fatal("profile opt-in implicitly posted financial history")
		}
	}
	item = catalogReply(t, call(http.MethodGet, "/api/v1/items/"+item.ID, "", "", ""))
	replacement := fmt.Sprintf(`{"item_revision":%d,"asset_revision":1.0,"cost_basis":"manual","currency":"JPY","manual_acquisition_minor":100,"in_service_at":"2024-11-07T12:00:00+08:00","retired_at":null,"estimated_residual_minor":30,"usage_unit":"times"}`, item.Revision)
	profile := assetHTTPReply[store.Asset](t, call(http.MethodPut, path+"/asset", replacement, "asset-http-replacement", `"1"`), 9)
	if profile.Revision != 2 || profile.EstimatedResidualMinor == nil || *profile.EstimatedResidualMinor != 30 {
		t.Fatal("replacement guessed presence from coincident Unit/Asset proofs or lost independent authority")
	}
	useBody := `{"delta":3.0e0,"usage_unit":"times","event_type":"use","occurred_at":"2026-11-07T12:00:00.123456789+08:00","note":"\u0000Exact use é\n"}`
	useReply := call(http.MethodPost, path+"/usage", useBody, "asset-http-original-use", `"2"`)
	use := assetHTTPReply[store.Usage](t, useReply, 9)
	if use.Revision != 1 || use.Delta != 3 || use.ItemUnitID != unit.ID || use.CorrectionOf != nil || use.OccurredAt != "2026-11-07T04:00:00.123456789Z" || use.Note != "\x00Exact use é\n" {
		t.Fatal("usage confused parent/independent proof, changed exact input or invented provenance", use)
	}
	correction := fmt.Sprintf(`{"delta":-1,"usage_unit":"times","event_type":"correction","occurred_at":"2026-11-07T04:00:00.123456789Z","correction_of":%q}`, use.ID)
	fixed := assetHTTPReply[store.Usage](t, call(http.MethodPost, path+"/usage", correction, "asset-http-correction", `"3"`), 9)
	if fixed.Revision != 1 || fixed.CorrectionOf == nil || *fixed.CorrectionOf != use.ID || fixed.Note != "" {
		t.Fatal("actual referenced correction or documented omitted-note default was lost")
	}
	chargeBody := `{"amount_minor":5,"currency":"JPY","kind":"repair","direction":"expense","occurred_at":"2026-11-07T04:00:00.123456789Z"}`
	chargeReply := call(http.MethodPost, path+"/costs", chargeBody, "asset-http-original-charge", `"4"`)
	charge := assetHTTPReply[store.Cost](t, chargeReply, 10)
	adjustment := fmt.Sprintf(`{"amount_minor":2,"currency":"JPY","kind":"adjustment","direction":"credit","occurred_at":"2026-11-07T04:00:00.123456789Z","correction_of":%q}`, charge.ID)
	credit := assetHTTPReply[store.Cost](t, call(http.MethodPost, path+"/costs", adjustment, "asset-http-adjustment", `"5"`), 10)
	if charge.Revision != 1 || credit.Revision != 1 || credit.CorrectionOf == nil || *credit.CorrectionOf != charge.ID || credit.Direction != "credit" || charge.Note != "" {
		t.Fatal("actual cost/adjustment changed magnitude, direction or independent history")
	}
	for _, collection := range []string{"usage", "costs"} {
		if page := ownedPageReply(t, call(http.MethodGet, path+"/"+collection+"?limit=1", "", "", "")); len(page.Data) != 1 || page.NextCursor == nil {
			t.Fatal("owned collection did not sign actual immutable-event lookahead")
		}
	}
	retireBody := `{"unit_revision":1,"retired_at":"2026-11-07T12:00:00.123456789+08:00","note":"Exact retirement\n"}`
	retireReply := call(http.MethodPost, path+"/retire", retireBody, "asset-http-original-retirement", `"6"`)
	retired := assetHTTPReply[store.Unit](t, retireReply, 8)
	if retired.Revision != 2 || retired.Status != "retired" || !reflect.DeepEqual(retired.LocationID, unit.LocationID) {
		t.Fatal("retirement used Asset-plus-one, removed placement or disposed the Unit", retired)
	}
	disposeBody := `{"unit_revision":2,"kind":"sold","disposed_at":"2026-11-07T04:00:00.123456789Z","realized_proceeds_minor":200,"currency":"JPY","note":"Exact sale\n"}`
	disposeReply := call(http.MethodPost, path+"/disposal", disposeBody, "asset-http-original-disposal", `"7"`)
	disposal := assetHTTPReply[store.Disposal](t, disposeReply, 7)
	if disposal.Revision != 1 || disposal.RealizedProceedsMinor != 200 || disposal.Note != "Exact sale\n" {
		t.Fatal("disposal used an unrelated next proof or inferred proceeds")
	}
	if actual := assetHTTPReply[store.Disposal](t, call(http.MethodGet, path+"/disposal", "", "", ""), 7); !reflect.DeepEqual(actual, disposal) {
		t.Fatal("disposal GET inferred source from physical status or a volatile reply")
	}
	physical := ownedReply[store.Unit](t, call(http.MethodGet, path, "", "", ""), item.ID, 8)
	if physical.Revision != 3 || physical.Status != "disposed" || !reflect.DeepEqual(physical.LocationID, unit.LocationID) {
		t.Fatal("financial disposal did not preserve terminal physical source/last placement")
	}
	summary := assetSummaryHTTPReply(t, call(http.MethodGet, path+"/cost-summary?as_of=2026-11-08T04:00:00.123456789Z", "", "", ""))
	if summary.AcquisitionMinor != 100 || summary.ExtraMinor != 3 || summary.RealizedProceedsMinor != 200 || summary.NetCostMinor != -97 || summary.ProjectedNetCostMinor == nil || *summary.ProjectedNetCostMinor != 73 ||
		summary.TotalUsage != 2 || summary.ElapsedDays == nil || *summary.ElapsedDays != 731 || summary.Timezone != "UTC" || summary.DayPolicy != "inclusive_civil_days" || summary.SourceRevision < 1 {
		t.Fatal("actual public summary mixed proceeds/residual, lost complete exact source or reinterpreted as_of", summary)
	}
	current := assetHTTPReply[store.Asset](t, call(http.MethodGet, path+"/asset", "", "", ""), 9)
	if current.Revision != 8 || current.RetiredAt == nil || *current.RetiredAt != "2026-11-07T04:00:00.123456789Z" {
		t.Fatal("actual Asset financial authority/dates did not advance independently")
	}
	if freshLogin != nil {
		if response := call(http.MethodPost, "/api/v1/auth/logout", "", "asset-http-logout", ""); response.Code != 200 {
			t.Fatal("actual financial-session logout failed")
		}
		freshLogin()
	}
	for _, retry := range []struct {
		method, route, body, key, proof string
		reply                           *httptest.ResponseRecorder
	}{
		{http.MethodPut, "asset", body, "asset-http-original-profile", `"1"`, createdReply},
		{http.MethodPost, "usage", useBody, "asset-http-original-use", `"2"`, useReply},
		{http.MethodPost, "costs", chargeBody, "asset-http-original-charge", `"4"`, chargeReply},
		{http.MethodPost, "retire", retireBody, "asset-http-original-retirement", `"6"`, retireReply},
		{http.MethodPost, "disposal", disposeBody, "asset-http-original-disposal", `"7"`, disposeReply},
	} {
		response := call(retry.method, path+"/"+retry.route, retry.body, retry.key, retry.proof)
		if response.Code != 200 || response.Body.String() != retry.reply.Body.String() || response.Header().Get("ETag") != retry.reply.Header().Get("ETag") {
			t.Fatal("original financial/physical command was rebased or replayed against later current state")
		}
	}
	if latest := assetHTTPReply[store.Asset](t, call(http.MethodGet, path+"/asset", "", "", ""), 9); !reflect.DeepEqual(latest, current) {
		t.Fatal("historical replies replaced current Asset authority")
	}
}

func TestAssetsHTTPAllElevenOperationsUseActualFinancialAndPhysicalServices(t *testing.T) {
	_, handler, cookie, csrf := domainHTTPFixture(t)
	call := func(method, path, body, key, proof string) *httptest.ResponseRecorder {
		return respond(handler, domainRequest(method, path, body, cookie, csrf, key, proof))
	}
	exerciseAssetsHTTP(t, call, nil)
}

func TestAssetsHTTPAllElevenOperationsThroughVerifiedTLSAndFreshLogin(t *testing.T) {
	call, login := domainTLSFixture(t)
	exerciseAssetsHTTP(t, call, login)
}
