package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"homebox/internal/store"
)

func TestTaxonomyHTTPAllEightOperationsThroughVerifiedTLSAndCookieJar(t *testing.T) {
	call, login := domainTLSFixture(t)
	rootBody := `{"name":"  家用品 😀 é  ","parent_id":null}`
	created := call(http.MethodPost, "/api/v1/categories", rootBody, "tls-root-create", "")
	root := taxonomyReply[store.Category](t, created)
	tag := taxonomyReply[store.Tag](t, call(http.MethodPost, "/api/v1/tags", `{"name":"  隨身 😀 é  "}`, "tls-tag-create", ""))
	child := taxonomyReply[store.Category](t, call(http.MethodPost, "/api/v1/categories", fmt.Sprintf(`{"name":"Child","parent_id":%q}`, root.ID), "tls-child-create", `"1"`))
	rootPath, childPath, tagPath := "/api/v1/categories/"+root.ID, "/api/v1/categories/"+child.ID, "/api/v1/tags/"+tag.ID
	if root.Name != "  家用品 😀 é  " || tag.Name != "  隨身 😀 é  " || child.ParentID == nil || *child.ParentID != root.ID {
		t.Fatal("TLS creation lost exact metadata or parent identity")
	}
	child = taxonomyReply[store.Category](t, call(http.MethodPatch, childPath, `{"parent_id":null}`, "tls-child-root-move", `"1"`))
	if child.ParentID != nil || child.Revision != 2 {
		t.Fatal("TLS partial move did not preserve explicit null")
	}
	root = taxonomyReply[store.Category](t, call(http.MethodPatch, rootPath, `{"name":"Updated"}`, "tls-root-rename", `"3"`))
	tag = taxonomyReply[store.Tag](t, call(http.MethodPatch, tagPath, `{"name":"Updated tag"}`, "tls-tag-rename", `"1"`))
	if root.Revision != 4 || tag.Revision != 2 {
		t.Fatal("TLS edits did not retain their authoritative aggregate revisions")
	}
	expectError(t, call(http.MethodPatch, rootPath, `{"name":"Stale"}`, "tls-stale-root", `"3"`), 409, "REVISION_CONFLICT")
	expectError(t, call(http.MethodPost, "/api/v1/tags", `{"name":"Updated tag"}`, "tls-duplicate-tag", ""), 409, "TAG_NAME_CONFLICT")
	for _, path := range []string{"/api/v1/categories", "/api/v1/tags"} {
		response := call(http.MethodGet, path+"?limit=1", "", "", "")
		var page struct {
			Data       []json.RawMessage `json:"data"`
			NextCursor *string           `json:"next_cursor"`
		}
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &page) != nil || len(page.Data) != 1 || response.Header().Get("ETag") != "" || path == "/api/v1/categories" && page.NextCursor == nil {
			t.Fatal("TLS live list did not expose its real metadata and signed continuation")
		}
	}
	deletedTag := call(http.MethodDelete, tagPath, "", "tls-tag-delete", `"2"`)
	for _, response := range []*httptest.ResponseRecorder{
		deletedTag,
		call(http.MethodDelete, rootPath, "", "tls-root-delete", `"4"`),
		call(http.MethodDelete, childPath, "", "tls-child-delete", `"2"`),
	} {
		var ack store.ActionAck
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &ack) != nil || !ack.OK || ack.OperationID == "" || ack.Revision < 1 || response.Header().Get("ETag") != "" {
			t.Fatal("TLS deletion did not produce a frozen acknowledgement")
		}
	}
	if response := call(http.MethodPost, "/api/v1/auth/logout", "", "tls-logout-taxonomy", ""); response.Code != 200 {
		t.Fatal("TLS logout failed")
	}
	expectError(t, call(http.MethodGet, "/api/v1/categories", "", "", ""), 401, "AUTHENTICATION_REQUIRED")
	login()
	// Fresh login reconciles original keys, never turning old replies into current data.
	replayed := call(http.MethodPost, "/api/v1/categories", rootBody, "tls-root-create", "")
	if replayed.Code != 200 || replayed.Body.String() != created.Body.String() || replayed.Header().Get("ETag") != `"1"` {
		t.Fatal("fresh TLS session could not retrieve the unchanged historical category acknowledgement")
	}
	replayed = call(http.MethodDelete, tagPath, "", "tls-tag-delete", `"2"`)
	if replayed.Code != 200 || replayed.Body.String() != deletedTag.Body.String() {
		t.Fatal("fresh TLS session could not reconcile a committed deletion")
	}
	for _, path := range []string{"/api/v1/categories", "/api/v1/tags"} {
		response := call(http.MethodGet, path, "", "", "")
		if response.Code != 200 || !strings.Contains(response.Body.String(), `"data":[]`) || !strings.Contains(response.Body.String(), `"next_cursor":null`) {
			t.Fatal("frozen replay recreated deleted current metadata")
		}
	}
}
