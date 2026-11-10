package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"homebox/internal/store"
)

func TestMediaAttachHTTPCreatesIndependentLinkWithOriginalProofs(t *testing.T) {
	f := newMediaHTTPFixture(t)
	body := fmt.Sprintf(`{"item_id":%q,"owner_revision":%d,"description":"Exact é\ncaption"}`, f.empty.ID, f.empty.Revision)
	request := domainRequest(http.MethodPost, "/api/v1/media/empty-media/links", body, f.cookie, f.csrf, "media-http-attach-original", `"1"`)
	response := respond(f.handler, request)
	var link store.MediaLink
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &link) != nil || link.ID == "" || link.MediaID != "empty-media" ||
		link.OwnerType != "item" || link.OwnerID != f.empty.ID || link.Revision != 1 || link.IsCover || link.SortOrder != 0 ||
		link.Description != "Exact é\ncaption" || response.Header().Get("ETag") != `"1"` || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("actual attachment was not exposed with an independent link proof: status %d body %s", response.Code, response.Body.String())
	}
}
