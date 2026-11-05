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

func ownedReply[T any](t *testing.T, response *httptest.ResponseRecorder, owner string, fields int) T {
	t.Helper()
	var value T
	var data map[string]json.RawMessage
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &value) != nil || json.Unmarshal(response.Body.Bytes(), &data) != nil || len(data) != fields {
		t.Fatalf("invalid owned resource reply: %d %s", response.Code, response.Body.String())
	}
	var id, itemID string
	var revision int64
	if json.Unmarshal(data["id"], &id) != nil || json.Unmarshal(data["item_id"], &itemID) != nil || json.Unmarshal(data["revision"], &revision) != nil ||
		!boundedString(id, 1, 128) || itemID != owner || revision < 1 || response.Header().Get("ETag") != strconv.Quote(strconv.FormatInt(revision, 10)) ||
		response.Header().Get("Set-Cookie") != "" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("owned reply mixed identity, owner, independent proof or private response rules")
	}
	return value
}

func ownedPageReply(t *testing.T, response *httptest.ResponseRecorder) struct {
	Data       []map[string]json.RawMessage `json:"data"`
	NextCursor *string                      `json:"next_cursor"`
} {
	t.Helper()
	var value struct {
		Data       []map[string]json.RawMessage `json:"data"`
		NextCursor *string                      `json:"next_cursor"`
	}
	var fields map[string]json.RawMessage
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &value) != nil || value.Data == nil ||
		json.Unmarshal(response.Body.Bytes(), &fields) != nil || len(fields) != 2 || fields["next_cursor"] == nil ||
		response.Header().Get("ETag") != "" || response.Header().Get("Set-Cookie") != "" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("invalid complete owned collection reply: %d %s", response.Code, response.Body.String())
	}
	return value
}

func exerciseOwnedHTTP(t *testing.T, call func(string, string, string, string, string) *httptest.ResponseRecorder, freshLogin func()) {
	t.Helper()
	item := catalogReply(t, call(http.MethodPost, "/api/v1/items", `{"name":"Owned metadata"}`, "owned-http-item", ""))
	base := "/api/v1/items/" + item.ID
	ownerRevision := int64(1)
	for _, kind := range []string{"barcodes", "aliases", "attributes"} {
		collection := base + "/" + kind
		if page := ownedPageReply(t, call(http.MethodGet, collection, "", "", "")); len(page.Data) != 0 || page.NextCursor != nil {
			t.Fatal("new Item had invented owned metadata")
		}
		body, patch, second := "", "", ""
		switch kind {
		case "barcodes":
			body, patch, second = `{"symbology":"EAN_13","raw_value":"4006381333931"}`, `{"symbology":"UPC_E","raw_value":"04252614"}`, `{"symbology":"CODE_128","raw_value":"00123"}`
		case "aliases":
			body, patch, second = `{"alias":"  原別名 😀 é  "}`, `{"alias":"NewAlias"}`, `{"alias":"SecondAlias"}`
		case "attributes":
			body, patch, second = `{"attribute_key":"RepeatedKey","attribute_value":"Original"}`, `{"attribute_value":""}`, `{"attribute_key":"RepeatedKey","attribute_value":"Second"}`
		}
		proof := strconv.Quote(strconv.FormatInt(ownerRevision, 10))
		created := call(http.MethodPost, collection, body, "owned-create-"+kind, proof)
		var id string
		switch kind {
		case "barcodes":
			value := ownedReply[store.Barcode](t, created, item.ID, 6)
			id = value.ID
			if value.NormalizedValue != "4006381333931" {
				t.Fatal("HTTP creation did not use the real barcode validator")
			}
		case "aliases":
			value := ownedReply[store.Alias](t, created, item.ID, 4)
			id = value.ID
			if value.Alias != "  原別名 😀 é  " {
				t.Fatal("HTTP alias lost exact Unicode or whitespace")
			}
		case "attributes":
			id = ownedReply[store.Attribute](t, created, item.ID, 5).ID
		}
		ownerRevision++
		if response := call(http.MethodPost, collection, second, "owned-second-"+kind, strconv.Quote(strconv.FormatInt(ownerRevision, 10))); response.Code != 200 || response.Header().Get("ETag") != `"1"` {
			t.Fatal("second independent row failed", response.Code)
		}
		ownerRevision++
		first := ownedPageReply(t, call(http.MethodGet, collection+"?limit=1", "", "", ""))
		if len(first.Data) != 1 || first.NextCursor == nil {
			t.Fatal("owned list did not return a complete signed page")
		}
		last := ownedPageReply(t, call(http.MethodGet, collection+"?limit=1&cursor="+url.QueryEscape(*first.NextCursor), "", "", ""))
		if len(last.Data) != 1 || last.NextCursor != nil || reflect.DeepEqual(first.Data[0], last.Data[0]) {
			t.Fatal("owned cursor skipped or duplicated an independent identity")
		}
		path := "/api/v1/" + kind + "/" + id
		updated := call(http.MethodPatch, path, patch, "owned-patch-"+kind, `"1"`)
		switch kind {
		case "barcodes":
			value := ownedReply[store.Barcode](t, updated, item.ID, 6)
			if value.RawValue != "04252614" || value.NormalizedValue != "04252614" || value.Symbology != "UPC_E" {
				t.Fatal("atomic pair replacement expanded or mixed barcode meanings")
			}
		case "aliases":
			ownedReply[store.Alias](t, updated, item.ID, 4)
		case "attributes":
			value := ownedReply[store.Attribute](t, updated, item.ID, 5)
			if value.AttributeKey != "RepeatedKey" || value.AttributeValue != "" {
				t.Fatal("partial attribute edit replaced an omitted field or ignored empty text")
			}
		}
		if updated.Header().Get("ETag") != `"2"` {
			t.Fatal("row edit incorrectly used the separately advanced Item revision")
		}
		ownerRevision++
		deleted := call(http.MethodDelete, path, "", "owned-delete-"+kind, `"2"`)
		var ack store.ActionAck
		if deleted.Code != 200 || json.Unmarshal(deleted.Body.Bytes(), &ack) != nil || !ack.OK || ack.Revision != 3 || ack.OperationID == "" || deleted.Header().Get("ETag") != "" {
			t.Fatal("owned deletion did not acknowledge the independent row proof")
		}
		ownerRevision++
		if response := call(http.MethodDelete, path, "", "owned-delete-"+kind, `"2"`); response.Code != 200 || response.Body.String() != deleted.Body.String() {
			t.Fatal("deleted row prevented exact original-key acknowledgement replay")
		}
		if response := call(http.MethodPost, collection, body, "owned-create-"+kind, proof); response.Code != 200 || response.Body.String() != created.Body.String() || response.Header().Get("ETag") != `"1"` {
			t.Fatal("original creation reply was silently rebased onto current source")
		}
	}
	tag := taxonomyReply[store.Tag](t, call(http.MethodPost, "/api/v1/tags", `{"name":"AssignedTag"}`, "owned-assigned-tag", ""))
	proof := strconv.Quote(strconv.FormatInt(ownerRevision, 10))
	tagBody := fmt.Sprintf(`{"tag_ids":[%q]}`, tag.ID)
	original := call(http.MethodPut, base+"/tags", tagBody, "owned-replace-tags", proof)
	assigned := catalogReply(t, original)
	ownerRevision++
	if assigned.Revision != ownerRevision {
		t.Fatal("tag replacement returned a Tag page/revision instead of the owning Item")
	}
	if page := ownedPageReply(t, call(http.MethodGet, base+"/tags", "", "", "")); len(page.Data) != 1 || page.NextCursor != nil {
		t.Fatal("actual assigned membership read failed")
	}
	cleared := catalogReply(t, call(http.MethodPut, base+"/tags", `{"tag_ids":[]}`, "owned-clear-tags", strconv.Quote(strconv.FormatInt(ownerRevision, 10))))
	if cleared.Revision != ownerRevision+1 {
		t.Fatal("explicit empty tag array did not clear the complete set")
	}
	if freshLogin != nil {
		if response := call(http.MethodPost, "/api/v1/auth/logout", "", "owned-http-logout", ""); response.Code != 200 {
			t.Fatal("logout failed")
		}
		freshLogin()
	}
	if replay := call(http.MethodPut, base+"/tags", tagBody, "owned-replace-tags", proof); replay.Code != 200 || replay.Body.String() != original.Body.String() || replay.Header().Get("ETag") != original.Header().Get("ETag") {
		t.Fatal("fresh-session tag acknowledgement was replaced by a newer set")
	}
	if page := ownedPageReply(t, call(http.MethodGet, base+"/tags", "", "", "")); len(page.Data) != 0 {
		t.Fatal("historical tag acknowledgement was installed as current membership")
	}
}

func TestCatalogLinksHTTPAllFourteenOperationsThroughRealServices(t *testing.T) {
	_, handler, cookie, csrf := domainHTTPFixture(t)
	call := func(method, path, body, key, revision string) *httptest.ResponseRecorder {
		return respond(handler, domainRequest(method, path, body, cookie, csrf, key, revision))
	}
	exerciseOwnedHTTP(t, call, nil)
}

func TestCatalogLinksHTTPAllFourteenOperationsThroughVerifiedTLSAndFreshSession(t *testing.T) {
	call, login := domainTLSFixture(t)
	exerciseOwnedHTTP(t, call, login)
}
