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

func stockHTTPReply(t *testing.T, response *httptest.ResponseRecorder, itemID, locationID string) store.Stock {
	t.Helper()
	var value store.Stock
	var fields map[string]json.RawMessage
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &value) != nil || json.Unmarshal(response.Body.Bytes(), &fields) != nil || len(fields) != 7 || value.ItemID != itemID || value.LocationID != locationID || value.Revision < 1 || response.Header().Get("ETag") != strconv.Quote(strconv.FormatInt(value.Revision, 10)) || response.Header().Get("Set-Cookie") != "" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("invalid independent Stock reply: %d %s", response.Code, response.Body.String())
	}
	return value
}

func exerciseHoldingsHTTP(t *testing.T, call func(string, string, string, string, string) *httptest.ResponseRecorder, freshLogin func()) {
	t.Helper()
	source := responseLocation(t, call(http.MethodPost, "/api/v1/locations", `{"code":"SOURCE","name":"Source"}`, "holding-source-location", ""))
	target := responseLocation(t, call(http.MethodPost, "/api/v1/locations", `{"code":"TARGET","name":"Target"}`, "holding-target-location", ""))
	quantity := catalogReply(t, call(http.MethodPost, "/api/v1/items", `{"name":"Quantity","tracking_mode":"quantity","unit_code":"kg","base_unit":"g","scale_factor":1000}`, "holding-quantity-item", ""))
	individual := catalogReply(t, call(http.MethodPost, "/api/v1/items", `{"name":"Individual"}`, "holding-individual-item", ""))
	if page := ownedPageReply(t, call(http.MethodGet, "/api/v1/inventory", "", "", "")); len(page.Data) != 0 || page.NextCursor != nil {
		t.Fatal("new database invented stock")
	}
	unitCollection := "/api/v1/items/" + individual.ID + "/units"
	if page := ownedPageReply(t, call(http.MethodGet, unitCollection, "", "", "")); len(page.Data) != 0 || page.NextCursor != nil {
		t.Fatal("new catalog record invented physical Units")
	}

	receiveBody := fmt.Sprintf(`{"item_id":%q,"target_location_id":%q,"quantity_base":1.5e3,"note":"\u0000Exact receive é\n"}`, quantity.ID, source.ID)
	received := call(http.MethodPost, "/api/v1/stock/receive", receiveBody, "http-stock-original-receive", `"1"`)
	stock := stockHTTPReply(t, received, quantity.ID, source.ID)
	if stock.Revision != 1 || stock.QuantityBase != 1500 || stock.UnitCode != "kg" || stock.BaseUnit != "g" || stock.ScaleFactor != 1000 {
		t.Fatal("receive confused Item proof, base conversion or exact integral JSON")
	}
	if item := catalogReply(t, call(http.MethodGet, "/api/v1/items/"+quantity.ID, "", "", "")); item.Revision != 2 {
		t.Fatal("receive did not advance original Item aggregate")
	}
	stockHTTPReply(t, call(http.MethodPost, "/api/v1/stock/receive", fmt.Sprintf(`{"item_id":%q,"target_location_id":%q,"quantity_base":1}`, quantity.ID, target.ID), "http-stock-second-receive", `"2"`), quantity.ID, target.ID)
	stock = stockHTTPReply(t, call(http.MethodPost, "/api/v1/stock/consume", fmt.Sprintf(`{"item_id":%q,"source_location_id":%q,"quantity_base":500}`, quantity.ID, source.ID), "http-stock-consume", `"3"`), quantity.ID, source.ID)
	if stock.QuantityBase != 1000 || stock.Revision != 2 {
		t.Fatal("consumption returned wrong source/revision")
	}
	stock = stockHTTPReply(t, call(http.MethodPost, "/api/v1/stock/transfer", fmt.Sprintf(`{"item_id":%q,"source_location_id":%q,"target_location_id":%q,"quantity_base":1000}`, quantity.ID, source.ID, target.ID), "http-stock-transfer", `"4"`), quantity.ID, target.ID)
	if stock.QuantityBase != 1001 || stock.Revision != 2 {
		t.Fatal("transfer returned an Item proof or wrong target")
	}
	selectedStock := "/api/v1/inventory?item_id=" + url.QueryEscape(quantity.ID)
	first := ownedPageReply(t, call(http.MethodGet, selectedStock+"&limit=1", "", "", ""))
	if len(first.Data) != 1 || first.NextCursor == nil {
		t.Fatal("Stock first page did not sign a complete tuple continuation")
	}
	last := ownedPageReply(t, call(http.MethodGet, selectedStock+"&limit=1&cursor="+url.QueryEscape(*first.NextCursor), "", "", ""))
	if len(last.Data) != 1 || last.NextCursor != nil {
		t.Fatal("Stock continuation truncated or repeated a balance")
	}
	for _, scope := range []string{"/api/v1/inventory?limit=1", "/api/v1/inventory?limit=1&item_id=" + url.QueryEscape(individual.ID)} {
		expectError(t, call(http.MethodGet, scope+"&cursor="+url.QueryEscape(*first.NextCursor), "", "", ""), 400, "INVALID_CURSOR")
	}
	if empty := ownedPageReply(t, call(http.MethodGet, "/api/v1/inventory?item_id="+url.QueryEscape(individual.ID), "", "", "")); len(empty.Data) != 0 || empty.NextCursor != nil {
		t.Fatal("Item stock filtering returned another Item's balances")
	}
	balances := make(map[string]int64)
	for _, row := range append(first.Data, last.Data...) {
		var location string
		var count int64
		_ = json.Unmarshal(row["location_id"], &location)
		_ = json.Unmarshal(row["quantity_base"], &count)
		balances[location] = count
	}
	if balances[source.ID] != 0 || balances[target.ID] != 1001 {
		t.Fatal("transfer omitted a leg, removed zero source or rescaled quantities")
	}
	stock = stockHTTPReply(t, call(http.MethodPost, "/api/v1/stock/adjust", fmt.Sprintf(`{"item_id":%q,"target_location_id":%q,"quantity_base":0,"reason":"  Absolute count é  "}`, quantity.ID, target.ID), "http-stock-adjust", `"5"`), quantity.ID, target.ID)
	if stock.QuantityBase != 0 || stock.Revision != 3 {
		t.Fatal("absolute adjustment became a delta or lost zero source")
	}
	expectError(t, call(http.MethodPost, "/api/v1/stock/consume", fmt.Sprintf(`{"item_id":%q,"source_location_id":%q,"quantity_base":1}`, quantity.ID, source.ID), "http-stock-insufficient", `"6"`), 409, "INSUFFICIENT_STOCK")

	unitBody := fmt.Sprintf(`{"location_id":%q,"serial_number":null,"notes":"  Unit é  "}`, source.ID)
	created := call(http.MethodPost, unitCollection, unitBody, "http-unit-original-create", `"1"`)
	unit := ownedReply[store.Unit](t, created, individual.ID, 8)
	if unit.Revision != 1 || unit.SerialNumber != nil || unit.Status != "stored" || unit.AssetCode != "AST-"+unit.ID || unit.Notes != "  Unit é  " {
		t.Fatal("Unit creation changed exact fields, identity/code or default semantics")
	}
	unitPath := "/api/v1/units/" + unit.ID
	if current := ownedReply[store.Unit](t, call(http.MethodGet, unitPath, "", "", ""), individual.ID, 8); !reflect.DeepEqual(current, unit) {
		t.Fatal("Unit GET differed from committed source")
	}
	if current := ownedReply[store.Unit](t, call(http.MethodGet, "/api/v1/units?id="+url.QueryEscape(unit.ID), "", "", ""), individual.ID, 8); !reflect.DeepEqual(current, unit) {
		t.Fatal("query Unit GET differed from the same independent source")
	}
	unit = ownedReply[store.Unit](t, call(http.MethodPatch, unitPath, `{"serial_number":"","notes":"\u0000Changed unit é\n"}`, "http-unit-patch", `"1"`), individual.ID, 8)
	if unit.Revision != 2 || unit.SerialNumber == nil || *unit.SerialNumber != "" || unit.Notes != "\x00Changed unit é\n" {
		t.Fatal("Unit partial metadata lost null/empty/Unicode distinction")
	}
	unit = ownedReply[store.Unit](t, call(http.MethodPatch, unitPath, `{"serial_number":null}`, "http-unit-clear-serial", `"2"`), individual.ID, 8)
	if unit.Revision != 3 || unit.SerialNumber != nil || unit.Notes != "\x00Changed unit é\n" {
		t.Fatal("Unit serial clear overwrote omitted notes")
	}
	unit = ownedReply[store.Unit](t, call(http.MethodPost, unitPath+"/move", fmt.Sprintf(`{"target_location_id":%q}`, target.ID), "http-unit-move", `"3"`), individual.ID, 8)
	if unit.Revision != 4 || unit.LocationID == nil || *unit.LocationID != target.ID {
		t.Fatal("Unit move reassigned identity or lost exact destination")
	}
	unit = ownedReply[store.Unit](t, call(http.MethodPost, unitPath+"/status", `{"status":"retired","note":"Physical retirement"}`, "http-unit-retire", `"4"`), individual.ID, 8)
	if unit.Revision != 5 || unit.LocationID == nil || *unit.LocationID != target.ID {
		t.Fatal("retirement implicitly removed physical placement")
	}
	unit = ownedReply[store.Unit](t, call(http.MethodPost, unitPath+"/status", `{"status":"disposed","disposal_kind":"recycled","note":"Explicit non-asset action"}`, "http-unit-dispose", `"5"`), individual.ID, 8)
	if unit.Revision != 6 || unit.Status != "disposed" {
		t.Fatal("explicit physical disposal did not produce the independent Unit")
	}
	expectError(t, call(http.MethodPost, unitPath+"/move", fmt.Sprintf(`{"target_location_id":%q}`, source.ID), "http-unit-disposed-move", `"6"`), 409, "UNIT_DISPOSED")
	deleted := call(http.MethodDelete, unitPath, "", "http-unit-delete", `"6"`)
	var ack store.ActionAck
	if deleted.Code != 200 || json.Unmarshal(deleted.Body.Bytes(), &ack) != nil || !ack.OK || ack.Revision != 7 || deleted.Header().Get("ETag") != "" {
		t.Fatal("Unit deletion lost original proof/ack semantics or fabricated a financial disposal")
	}
	expectError(t, call(http.MethodGet, unitPath, "", "", ""), 404, "NOT_FOUND")
	expectError(t, call(http.MethodGet, "/api/v1/units?id="+url.QueryEscape(unit.ID), "", "", ""), 404, "NOT_FOUND")
	if freshLogin != nil {
		if response := call(http.MethodPost, "/api/v1/auth/logout", "", "http-holding-logout", ""); response.Code != 200 {
			t.Fatal("holding session logout failed")
		}
		freshLogin()
	}
	for _, replay := range []struct {
		method, path, body, key, proof string
		first                          *httptest.ResponseRecorder
	}{
		{http.MethodPost, "/api/v1/stock/receive", receiveBody, "http-stock-original-receive", `"1"`, received},
		{http.MethodPost, unitCollection, unitBody, "http-unit-original-create", `"1"`, created},
		{http.MethodDelete, unitPath, "", "http-unit-delete", `"6"`, deleted},
	} {
		response := call(replay.method, replay.path, replay.body, replay.key, replay.proof)
		if response.Code != 200 || response.Body.String() != replay.first.Body.String() || response.Header().Get("ETag") != replay.first.Header().Get("ETag") {
			t.Fatal("original-key holding replay was rebased onto changed/deleted source", replay.path)
		}
	}
	if page := ownedPageReply(t, call(http.MethodGet, unitCollection, "", "", "")); len(page.Data) != 0 {
		t.Fatal("historical Unit acknowledgement resurrected current source")
	}
	if page := ownedPageReply(t, call(http.MethodGet, "/api/v1/inventory", "", "", "")); len(page.Data) != 2 {
		t.Fatal("historical Stock reply replaced retained current zero balances")
	}
}

func TestHoldingsHTTPAllThirteenOperationsUseRealServicesOriginalProofsAndFrozenReplies(t *testing.T) {
	_, handler, cookie, csrf := domainHTTPFixture(t)
	call := func(method, path, body, key, revision string) *httptest.ResponseRecorder {
		return respond(handler, domainRequest(method, path, body, cookie, csrf, key, revision))
	}
	exerciseHoldingsHTTP(t, call, nil)
}

func TestHoldingsHTTPAllThirteenOperationsThroughVerifiedTLSAndFreshSession(t *testing.T) {
	call, login := domainTLSFixture(t)
	exerciseHoldingsHTTP(t, call, login)
}
