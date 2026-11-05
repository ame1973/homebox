package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"homebox/internal/store"
)

func TestCatalogHTTPAllSixOperationsThroughVerifiedTLSAndFreshSessionReplay(t *testing.T) {
	call, login := domainTLSFixture(t)
	category := taxonomyReply[store.Category](t, call(http.MethodPost, "/api/v1/categories", `{"name":"TLS category"}`, "tls-item-category", ""))
	body := fmt.Sprintf(`{"name":"TLS First 😀","category_id":%q,"brand":"Brand","model":null,"tracking_mode":"quantity","unit_code":"kg","base_unit":"g","scale_factor":1000}`, category.ID)
	created := call(http.MethodPost, "/api/v1/items", body, "tls-item-create", "")
	item := catalogReply(t, created)
	if item.Revision != 1 || item.CategoryID == nil || *item.CategoryID != category.ID || item.ScaleFactor != 1000 || item.Model != nil {
		t.Fatal("TLS creation lost its typed metadata/defaults")
	}
	catalogReply(t, call(http.MethodPost, "/api/v1/items", `{"name":"TLS Second"}`, "tls-item-second", ""))
	path := "/api/v1/items/" + item.ID
	current := catalogReply(t, call(http.MethodGet, path, "", "", ""))
	if current.ID != item.ID || current.Revision != 1 {
		t.Fatal("TLS resource GET did not return current authoritative metadata")
	}
	item = catalogReply(t, call(http.MethodPatch, path, `{"name":"TLS Renamed","brand":null,"model":""}`, "tls-item-patch", `"1"`))
	if item.Revision != 2 || item.Brand != nil || item.Model == nil || *item.Model != "" || item.CategoryID == nil {
		t.Fatal("TLS patch lost exact nullable/omitted semantics")
	}
	expectError(t, call(http.MethodPatch, path, `{"name":"Stale"}`, "tls-item-stale", `"1"`), 409, "REVISION_CONFLICT")
	item = catalogReply(t, call(http.MethodPost, path+"/archive", `{}`, "tls-item-archive", `"2"`))
	if item.Revision != 3 || item.Status != "archived" {
		t.Fatal("TLS archive did not retain the original proof")
	}
	item = catalogReply(t, call(http.MethodPatch, path, `{"status":"active"}`, "tls-item-restore", `"3"`))
	if item.Revision != 4 || item.Status != "active" {
		t.Fatal("TLS restoration failed")
	}
	first := catalogPageReply(t, call(http.MethodGet, "/api/v1/items?limit=1&status=active&q=tls", "", "", ""))
	if len(first.Data) != 1 || first.NextCursor == nil {
		t.Fatal("TLS filtered list did not provide a signed live continuation")
	}
	last := catalogPageReply(t, call(http.MethodGet, "/api/v1/items?limit=1&status=active&q=tls&cursor="+url.QueryEscape(*first.NextCursor), "", "", ""))
	if len(last.Data) != 1 || last.NextCursor != nil || last.Data[0].ID <= first.Data[0].ID {
		t.Fatal("TLS signed list lost complete stable ordering")
	}
	deleted := call(http.MethodDelete, path, "", "tls-item-delete", `"4"`)
	var ack store.ActionAck
	if deleted.Code != 200 || json.Unmarshal(deleted.Body.Bytes(), &ack) != nil || !ack.OK || ack.Revision != 5 || ack.OperationID == "" || deleted.Header().Get("ETag") != "" {
		t.Fatal("TLS deletion did not return its frozen acknowledgement")
	}
	if response := call(http.MethodPost, "/api/v1/auth/logout", "", "tls-item-logout", ""); response.Code != 200 {
		t.Fatal("TLS logout failed")
	}
	expectError(t, call(http.MethodGet, "/api/v1/items", "", "", ""), 401, "AUTHENTICATION_REQUIRED")
	login()
	if replay := call(http.MethodPost, "/api/v1/items", body, "tls-item-create", ""); replay.Code != 200 || replay.Body.String() != created.Body.String() || replay.Header().Get("ETag") != `"1"` {
		t.Fatal("fresh TLS session could not reconcile an original create without rebasing")
	}
	if replay := call(http.MethodDelete, path, "", "tls-item-delete", `"4"`); replay.Code != 200 || replay.Body.String() != deleted.Body.String() {
		t.Fatal("fresh TLS session could not reconcile a deletion after source removal")
	}
	expectError(t, call(http.MethodGet, path, "", "", ""), 404, "NOT_FOUND")
	if page := catalogPageReply(t, call(http.MethodGet, "/api/v1/items", "", "", "")); len(page.Data) != 1 || page.Data[0].ID == item.ID {
		t.Fatal("TLS historical replay resurrected the deleted source")
	}
}
