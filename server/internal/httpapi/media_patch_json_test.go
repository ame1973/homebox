package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMediaLinkPatchJSONPreservesPartialAndExplicitValues(t *testing.T) {
	for _, tc := range []struct {
		body, kind string
	}{
		{`{"is_cover":false}`, "cover"},
		{`{"sort_order":1e2}`, "sort"},
		{`{"description":"é\n\u0000"}`, "description"},
		{`{"is_cover":true,"sort_order":0,"description":""}`, "all"},
	} {
		r := apiRequest(http.MethodPatch, "/api/v1/media/links/link", tc.body)
		w := httptest.NewRecorder()
		input, ok := readMediaLinkPatch(w, r, "request")
		if !ok {
			t.Fatal("valid partial link input rejected", tc.kind, w.Code, w.Body.String())
		}
		switch tc.kind {
		case "cover":
			if input.IsCover == nil || *input.IsCover || input.SortOrder != nil || input.Description != nil {
				t.Fatal("explicit false or omitted fields changed", input)
			}
		case "sort":
			if input.IsCover != nil || input.SortOrder == nil || *input.SortOrder != 100 || input.Description != nil {
				t.Fatal("integral exponent or omitted fields changed", input)
			}
		case "description":
			if input.IsCover != nil || input.SortOrder != nil || input.Description == nil || *input.Description != "é\n\x00" {
				t.Fatal("exact description or omitted fields changed", input)
			}
		case "all":
			if input.IsCover == nil || !*input.IsCover || input.SortOrder == nil || *input.SortOrder != 0 || input.Description == nil || *input.Description != "" {
				t.Fatal("explicit true/zero/empty intent changed", input)
			}
		}
	}
	description := strings.Repeat("界", 10000)
	body := fmt.Sprintf(`{"sort_order":9007199254740991,"description":%q}`, description)
	w := httptest.NewRecorder()
	input, ok := readMediaLinkPatch(w, apiRequest(http.MethodPatch, "/api/v1/media/links/link", body), "request")
	if !ok || input.SortOrder == nil || *input.SortOrder != maxJSONInteger || input.Description == nil || *input.Description != description {
		t.Fatal("valid maximum Unicode or safe integer was rejected", w.Code)
	}
}

func TestMediaLinkPatchJSONRejectsAmbiguousAndUnsafeInput(t *testing.T) {
	for _, tc := range []struct {
		body string
		code int
	}{
		{`{}`, 422}, {`[]`, 422}, {`null`, 422},
		{`{"is_cover":null}`, 422}, {`{"is_cover":0}`, 422}, {`{"is_cover":"false"}`, 422},
		{`{"sort_order":null}`, 422}, {`{"sort_order":-1}`, 422}, {`{"sort_order":0.1}`, 422}, {`{"sort_order":9007199254740992}`, 422},
		{`{"description":null}`, 422}, {`{"description":false}`, 422}, {fmt.Sprintf(`{"description":%q}`, strings.Repeat("界", 10001)), 422},
		{`{"item_id":"other"}`, 422}, {`{"owner_revision":1}`, 422}, {`{"media_id":"other"}`, 422}, {`{"DESCRIPTION":"x"}`, 422},
		{`{"description":"x","description":"y"}`, 400}, {`{"is_cover":true,"is_cover":false}`, 400},
		{fmt.Sprintf(`{"is_cover":true,"%cu0069s_cover":false}`, '\\'), 400},
		{`{"description":"\ud800"}`, 400}, {`{"description":"x"} trailing`, 400},
		{string([]byte{'{', '"', 'd', 'e', 's', 'c', 'r', 'i', 'p', 't', 'i', 'o', 'n', '"', ':', '"', 0xff, '"', '}'}), 400},
	} {
		r := apiRequest(http.MethodPatch, "/api/v1/media/links/link", tc.body)
		w := httptest.NewRecorder()
		w.Header().Set("X-Request-ID", "request")
		if _, ok := readMediaLinkPatch(w, r, "request"); ok || w.Code != tc.code {
			t.Fatalf("invalid link JSON accepted or misclassified: %q: %d %s", tc.body, w.Code, w.Body.String())
		}
	}
}

func TestMediaLinkPatchJSONEnforcesContentAndExactBodyBudget(t *testing.T) {
	body := `{"description":"x"}`
	for _, tc := range []struct {
		name string
		edit func(*http.Request)
	}{
		{"missing content type", func(r *http.Request) { r.Header.Del("Content-Type") }},
		{"duplicate content type", func(r *http.Request) { r.Header.Add("Content-Type", "application/json") }},
		{"wrong content type", func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }},
		{"content encoding", func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := apiRequest(http.MethodPatch, "/api/v1/media/links/link", body)
			tc.edit(r)
			w := httptest.NewRecorder()
			if _, ok := readMediaLinkPatch(w, r, "request"); ok || w.Code != http.StatusUnsupportedMediaType {
				t.Fatal("unsupported content admitted", w.Code)
			}
		})
	}
	for _, size := range []int{mediaLinkBodyLimit, mediaLinkBodyLimit + 1} {
		padded := body + strings.Repeat(" ", size-len(body))
		w := httptest.NewRecorder()
		_, ok := readMediaLinkPatch(w, apiRequest(http.MethodPatch, "/api/v1/media/links/link", padded), "request")
		if size == mediaLinkBodyLimit && !ok || size > mediaLinkBodyLimit && (ok || w.Code != http.StatusRequestEntityTooLarge) {
			t.Fatal("link edit JSON byte limit was not exact", size, w.Code)
		}
	}
}

func FuzzMediaLinkPatchJSON(f *testing.F) {
	for _, value := range []string{`{"description":"x"}`, `{"is_cover":false,"sort_order":0}`, `{"description":null}`, `{"description":"\ud800"}`, `{"sort_order":1.1}`, `{"is_cover":true,"is_cover":false}`} {
		f.Add(value)
	}
	f.Fuzz(func(t *testing.T, body string) {
		r := apiRequest(http.MethodPatch, "/api/v1/media/links/link", body)
		if body == "" {
			r.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		input, ok := readMediaLinkPatch(w, r, "request")
		if ok && (input.IsCover == nil && input.SortOrder == nil && input.Description == nil ||
			input.SortOrder != nil && (*input.SortOrder < 0 || *input.SortOrder > maxJSONInteger) ||
			input.Description != nil && !boundedString(*input.Description, 0, 10000)) {
			t.Fatal("accepted invalid Media link edit input")
		}
	})
}
