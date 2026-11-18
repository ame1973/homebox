//go:build linux

package httpapi

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"homebox/internal/store"
)

func TestCatalogHTTPSearchRejectsUnreviewedCurrentTextWithoutPartialResults(t *testing.T) {
	db, auth, _, cookie, csrf, path := recoveryHTTPFixture(t, false)
	handler := recoveryHTTPHandler(t, auth, nil)
	catalog, err := store.NewCatalog(auth)
	if err != nil {
		t.Fatal(err)
	}
	first, err := catalog.Create(t.Context(), cookie.Value, "http-search-source-first", store.ItemCreate{Name: "Visible first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := catalog.Create(t.Context(), cookie.Value, "http-search-source-second", store.ItemCreate{Name: "Visible second"})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID > second.ID {
		first, second = second, first
	}
	// Keep the extra SQLite descriptor open until after Store.Close: closing an
	// unrelated descriptor earlier can drop SQLite's process-scoped locks.
	raw, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path, RawQuery: "_pragma=busy_timeout%285000%29"}).String())
	if err != nil {
		t.Fatal(err)
	}
	raw.SetMaxOpenConns(1)
	raw.SetMaxIdleConns(1)
	conn, err := raw.Conn(t.Context())
	if err != nil {
		_ = raw.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
		if err := conn.Close(); err != nil {
			t.Error(err)
		}
		if err := raw.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := conn.ExecContext(t.Context(), `INSERT INTO ai_review_entries(id,item_id,field_key,state,raw_value,created_at,updated_at)
 VALUES('pending-http',?,'keyword','needs_review','PendingPrivateOCR','2026','2026')`, second.ID); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]string{"entity_type": "item", "entity_id": second.ID, "approved_text": second.Name + "\nPendingPrivateOCR"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), `UPDATE current_projections SET payload_json=? WHERE entity_type='search_doc' AND entity_id=json_array('item',?)`, string(payload), second.ID); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"Visible", "PendingPrivateOCR"} {
		endpoint := "/api/v1/items?limit=1&q=" + url.QueryEscape(query)
		response := respond(handler, domainRequest(http.MethodGet, endpoint, "", cookie, csrf, "", ""))
		expectError(t, response, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE")
		if strings.Contains(response.Body.String(), "PendingPrivateOCR") || strings.Contains(response.Body.String(), first.ID) || response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("ETag") != "" {
			t.Fatal("failed search leaked source text or a partial result")
		}
	}
	response := respond(handler, domainRequest(http.MethodGet, "/api/v1/items?limit=2", "", cookie, csrf, "", ""))
	if page := catalogPageReply(t, response); len(page.Data) != 2 {
		t.Fatal("unfiltered catalog read was unnecessarily blocked")
	}
	expectError(t, respond(handler, domainRequest(http.MethodGet, "/api/v1/items?q=Visible", "", nil, "", "", "")), http.StatusUnauthorized, "AUTHENTICATION_REQUIRED")
}
