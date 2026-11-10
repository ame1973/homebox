package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"homebox/internal/store"
)

func TestMediaLinkUnlinkHTTPUsesIndependentOriginalRowProof(t *testing.T) {
	f := newMediaHTTPFixture(t)
	request := func(key, proof string) *http.Request {
		return domainRequest(http.MethodDelete, "/api/v1/media/links/sibling", "", f.cookie, f.csrf, key, proof)
	}
	first := respond(f.handler, request("media-http-unlink-original", `"5"`))
	var ack store.ActionAck
	var fields map[string]json.RawMessage
	if first.Code != 200 || json.Unmarshal(first.Body.Bytes(), &ack) != nil || json.Unmarshal(first.Body.Bytes(), &fields) != nil || len(fields) != 3 ||
		!ack.OK || ack.OperationID == "" || ack.Revision != 6 || first.Header().Get("ETag") != "" || first.Header().Get("Cache-Control") != "no-store" ||
		first.Header().Get("X-Content-Type-Options") != "nosniff" || first.Header().Get("Set-Cookie") != "" {
		t.Fatalf("actual unlink was not exposed under its independent proof: %d %s", first.Code, first.Body.String())
	}
	if strings.Contains(first.Body.String(), "private-storage-key") || strings.Contains(first.Body.String(), "NeverPublishUnreviewedMediaOCR") {
		t.Fatal("unlink acknowledgement disclosed private source")
	}
	mediaHTTPResource(t, mediaHTTPGet(f, "/api/v1/media/photo"), "photo", 8)
	mediaHTTPResource(t, mediaHTTPGet(f, "/api/v1/media/links/view"), "links", 3)
	page := mediaHTTPPage(t, mediaHTTPGet(f, "/api/v1/media/links?owner_type=item&owner_id="+url.QueryEscape(f.sibling.ID)))
	if len(page.Data) != 1 || page.Data[0].ID != "reserved" || page.Data[0].MediaID != "links" || page.NextCursor != nil {
		t.Fatal("unlink removed unrelated Media or left the selected link")
	}
	replay := respond(f.handler, request("media-http-unlink-original", `"5"`))
	if replay.Code != 200 || replay.Header().Get("ETag") != "" || !bytes.Equal(first.Body.Bytes(), replay.Body.Bytes()) {
		t.Fatal("original-key retry did not preserve the independent acknowledgement")
	}
	expectError(t, respond(f.handler, request("media-http-unlink-original", `"6"`)), 409, "IDEMPOTENCY_CONFLICT")
	expectError(t, respond(f.handler, request("media-http-unlink-new", `"5"`)), 404, "NOT_FOUND")
	mediaHTTPResource(t, mediaHTTPGet(f, "/api/v1/media/photo"), "photo", 8)
}
