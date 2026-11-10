package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMediaAttachJSONPreservesExactPresenceAndIntegralValues(t *testing.T) {
	for _, tc := range []struct {
		body, kind string
	}{
		{`{"item_id":"item","owner_revision":1}`, "item"},
		{`{"item_unit_id":"unit","owner_revision":1.0,"is_cover":false,"sort_order":0,"description":""}`, "unit"},
		{`{"location_id":"\u0000","owner_revision":9e0,"is_cover":true,"sort_order":1e2,"description":"é\n"}`, "location"},
	} {
		r := apiRequest(http.MethodPost, "/api/v1/media/photo/links", tc.body)
		w := httptest.NewRecorder()
		input, ok := readMediaLinkCreate(w, r, "request")
		if !ok {
			t.Fatal("valid attachment input rejected", tc.kind, w.Code, w.Body.String())
		}
		switch tc.kind {
		case "item":
			if input.ItemID == nil || *input.ItemID != "item" || input.ItemUnitID != nil || input.LocationID != nil || input.OwnerRevision != 1 || input.IsCover != nil || input.SortOrder != nil || input.Description != nil {
				t.Fatal("omission or Item proof was lost", input)
			}
		case "unit":
			if input.ItemUnitID == nil || *input.ItemUnitID != "unit" || input.IsCover == nil || *input.IsCover || input.SortOrder == nil || *input.SortOrder != 0 || input.Description == nil || *input.Description != "" {
				t.Fatal("explicit false/zero/empty intent was omitted", input)
			}
		case "location":
			if input.LocationID == nil || *input.LocationID != "\x00" || input.OwnerRevision != 9 || input.IsCover == nil || !*input.IsCover || input.SortOrder == nil || *input.SortOrder != 100 || input.Description == nil || *input.Description != "é\n" {
				t.Fatal("exact retained owner or integral exponent was changed", input)
			}
		}
	}
}

func TestMediaAttachJSONAcceptsMaximumSafeUnicodeAndIntegerBounds(t *testing.T) {
	id, description := strings.Repeat("😀", 128), strings.Repeat("界", 10000)
	body := fmt.Sprintf(`{"item_id":%q,"owner_revision":9007199254740991,"sort_order":9007199254740991,"description":%q}`, id, description)
	r := apiRequest(http.MethodPost, "/api/v1/media/photo/links", body)
	w := httptest.NewRecorder()
	input, ok := readMediaLinkCreate(w, r, "request")
	if !ok || input.ItemID == nil || *input.ItemID != id || input.OwnerRevision != maxJSONInteger || input.SortOrder == nil || *input.SortOrder != maxJSONInteger || input.Description == nil || *input.Description != description {
		t.Fatal("valid maximum Unicode or safe integer boundary was rejected", w.Code)
	}
}

func TestMediaAttachJSONRejectsIncompleteAmbiguousAndUnsafeInput(t *testing.T) {
	for _, tc := range []struct {
		body string
		code int
	}{
		{`{}`, 422}, {`[]`, 422}, {`null`, 422}, {`{"item_id":"item"}`, 422},
		{`{"owner_revision":1}`, 422}, {`{"item_id":null,"owner_revision":1}`, 422},
		{`{"item_id":"","owner_revision":1}`, 422}, {fmt.Sprintf(`{"item_id":%q,"owner_revision":1}`, strings.Repeat("界", 129)), 422},
		{`{"item_id":"item","item_unit_id":"unit","owner_revision":1}`, 422},
		{`{"item_id":"item","location_id":null,"owner_revision":1}`, 422},
		{`{"item_id":"item","owner_revision":null}`, 422}, {`{"item_id":"item","owner_revision":0}`, 422},
		{`{"item_id":"item","owner_revision":true}`, 422}, {`{"item_id":"item","owner_revision":1.1}`, 422},
		{`{"item_id":"item","owner_revision":9007199254740992}`, 422},
		{`{"item_id":"item","owner_revision":1,"is_cover":null}`, 422},
		{`{"item_id":"item","owner_revision":1,"is_cover":0}`, 422},
		{`{"item_id":"item","owner_revision":1,"is_cover":"false"}`, 422},
		{`{"item_id":"item","owner_revision":1,"sort_order":null}`, 422},
		{`{"item_id":"item","owner_revision":1,"sort_order":-1}`, 422},
		{`{"item_id":"item","owner_revision":1,"sort_order":0.1}`, 422},
		{`{"item_id":"item","owner_revision":1,"sort_order":9007199254740992}`, 422},
		{`{"item_id":"item","owner_revision":1,"description":null}`, 422},
		{fmt.Sprintf(`{"item_id":"item","owner_revision":1,"description":%q}`, strings.Repeat("界", 10001)), 422},
		{`{"item_id":"item","owner_revision":1,"unknown":true}`, 422},
		{`{"ITEM_ID":"item","owner_revision":1}`, 422},
		{`{"item_id":"item","item_id":"other","owner_revision":1}`, 400},
		{`{"item_id":"\ud800","owner_revision":1}`, 400},
		{`{"item_id":"item","owner_revision":1} trailing`, 400},
		{string([]byte{'{', '"', 'i', 't', 'e', 'm', '_', 'i', 'd', '"', ':', '"', 0xff, '"', '}'}), 400},
	} {
		r := apiRequest(http.MethodPost, "/api/v1/media/photo/links", tc.body)
		w := httptest.NewRecorder()
		w.Header().Set("X-Request-ID", "request")
		if _, ok := readMediaLinkCreate(w, r, "request"); ok || w.Code != tc.code {
			t.Fatalf("invalid JSON accepted or misclassified: %q: %d %s", tc.body, w.Code, w.Body.String())
		}
	}
}

func TestMediaAttachJSONEnforcesContentAndExactBodyBudget(t *testing.T) {
	body := `{"item_id":"item","owner_revision":1}`
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
			r := apiRequest(http.MethodPost, "/api/v1/media/photo/links", body)
			tc.edit(r)
			w := httptest.NewRecorder()
			if _, ok := readMediaLinkCreate(w, r, "request"); ok || w.Code != http.StatusUnsupportedMediaType {
				t.Fatal("unsupported media admitted", w.Code)
			}
		})
	}
	for _, size := range []int{mediaLinkBodyLimit, mediaLinkBodyLimit + 1} {
		padded := body + strings.Repeat(" ", size-len(body))
		r := apiRequest(http.MethodPost, "/api/v1/media/photo/links", padded)
		w := httptest.NewRecorder()
		_, ok := readMediaLinkCreate(w, r, "request")
		if size == mediaLinkBodyLimit && !ok || size > mediaLinkBodyLimit && (ok || w.Code != http.StatusRequestEntityTooLarge) {
			t.Fatal("attachment JSON byte limit was not exact", size, w.Code)
		}
	}
}

func FuzzMediaAttachJSON(f *testing.F) {
	for _, value := range []string{`{"item_id":"item","owner_revision":1}`, `{"item_unit_id":"unit","owner_revision":1,"is_cover":false}`, `{"location_id":null}`, `{"item_id":"\ud800","owner_revision":1}`, `{"item_id":"item","owner_revision":1.1}`} {
		f.Add(value)
	}
	f.Fuzz(func(t *testing.T, body string) {
		r := apiRequest(http.MethodPost, "/api/v1/media/photo/links", body)
		if body == "" {
			r.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		input, ok := readMediaLinkCreate(w, r, "request")
		if !ok {
			return
		}
		owners := 0
		for _, id := range []*string{input.ItemID, input.ItemUnitID, input.LocationID} {
			if id != nil {
				owners++
				if !boundedString(*id, 1, 128) {
					t.Fatal("accepted an invalid owner ID")
				}
			}
		}
		if owners != 1 || input.OwnerRevision < 1 || input.OwnerRevision > maxJSONInteger || input.SortOrder != nil && (*input.SortOrder < 0 || *input.SortOrder > maxJSONInteger) || input.Description != nil && !boundedString(*input.Description, 0, 10000) {
			t.Fatal("accepted invalid Media attachment input")
		}
	})
}
