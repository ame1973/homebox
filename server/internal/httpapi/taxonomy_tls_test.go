package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"homebox/internal/store"
)

func TestTaxonomyHTTPAllEightOperationsThroughVerifiedTLSAndCookieJar(t *testing.T) {
	auth := newTestAuth(t)
	setupAccount(t, auth, true)
	server := httptest.NewUnstartedServer(nil)
	origin := "https://" + server.Listener.Addr().String()
	handler, err := New(Config{Auth: auth, PublicOrigin: origin, Assets: fstest.MapFS{"index.html": {Data: []byte("<main>App</main>")}}})
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	server.Config.Handler = handler
	server.StartTLS()
	defer server.Close()
	client := server.Client() // Trust only this server's generated test certificate.
	client.Timeout = 5 * time.Second
	client.Jar, err = cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	csrf := ""
	call := func(method, path, body, key, revision string) *httptest.ResponseRecorder {
		t.Helper()
		r, err := http.NewRequestWithContext(t.Context(), method, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Origin", origin)
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		if method != http.MethodGet && path != "/api/v1/auth/login" {
			r.Header.Set("X-CSRF-Token", csrf)
			r.Header.Set("Idempotency-Key", key)
			r.Header.Set("X-HomeBox-Client-Version", supportedClientVersion)
		}
		if revision != "" {
			r.Header.Set("If-Match", revision)
		}
		response, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil || response.TLS == nil || len(response.TLS.VerifiedChains) == 0 || response.Header.Get("Strict-Transport-Security") == "" || response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("X-Request-ID") == "" {
			t.Fatal("socket reply did not retain its verified TLS and private response boundary", err)
		}
		if (strings.HasPrefix(path, "/api/v1/categories") || strings.HasPrefix(path, "/api/v1/tags")) && response.Header.Get("Set-Cookie") != "" {
			t.Fatal("taxonomy socket reply changed its session cookie")
		}
		// Retain the actual socket status/headers/bytes for shared reply assertions.
		captured := httptest.NewRecorder()
		captured.Code = response.StatusCode
		for name, values := range response.Header {
			captured.Header()[name] = values
		}
		_, _ = captured.Body.Write(data)
		return captured
	}
	login := func() {
		t.Helper()
		response := call(http.MethodPost, "/api/v1/auth/login", fmt.Sprintf(`{"username":"管理員","password":%q}`, changedPassword), "", "")
		cookies := response.Result().Cookies()
		if response.Code != 200 || len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
			t.Fatal("verified TLS login did not deliver a secure opaque session")
		}
		response = call(http.MethodGet, "/api/v1/auth/csrf", "", "", "")
		var token struct{ Token string }
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &token) != nil || token.Token == "" {
			t.Fatal("actual session could not obtain its in-memory CSRF proof")
		}
		csrf = token.Token
	}
	login()
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
	// A fresh active session can reconcile deleted/changed resources by the
	// original principal-scoped keys; it does not turn old replies into current data.
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
