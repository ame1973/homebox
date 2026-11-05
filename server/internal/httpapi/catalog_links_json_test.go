package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func readOwnedJSON(w http.ResponseWriter, r *http.Request, mode uint8) bool {
	switch mode % 5 {
	case 0:
		_, ok := readBarcodeWrite(w, r, "request")
		return ok
	case 1:
		_, ok := readAliasWrite(w, r, "request")
		return ok
	case 2, 3:
		_, ok := readAttributeFields(w, r, "request", mode%5 == 2)
		return ok
	default:
		_, ok := readItemTags(w, r, "request")
		return ok
	}
}

func TestCatalogLinksJSONExactPresenceBoundsAndEmptyValues(t *testing.T) {
	attribute, ok := readAttributeFields(httptest.NewRecorder(), apiRequest(http.MethodPost, "/", `{"attribute_key":"  Key é  ","attribute_value":""}`), "request", true)
	if !ok || attribute.AttributeKey == nil || *attribute.AttributeKey != "  Key é  " || attribute.AttributeValue == nil || *attribute.AttributeValue != "" {
		t.Fatal("explicit empty creation value was lost")
	}
	patch, ok := readAttributeFields(httptest.NewRecorder(), apiRequest(http.MethodPatch, "/", `{"attribute_value":""}`), "request", false)
	if !ok || patch.AttributeKey != nil || patch.AttributeValue == nil || *patch.AttributeValue != "" {
		t.Fatal("partial attribute presence/empty text was not retained")
	}
	ids, ok := readItemTags(httptest.NewRecorder(), apiRequest(http.MethodPut, "/", `{"tag_ids":[]}`), "request")
	if !ok || ids == nil || len(ids) != 0 {
		t.Fatal("explicit empty tag array became null/omitted")
	}
	for _, test := range []struct {
		mode uint8
		body string
	}{
		{0, `{"symbology":"CODE_128","raw_value":"\u0000GS1\u001d00123"}`},
		{0, fmt.Sprintf(`{"symbology":"QR_CODE","raw_value":%q}`, strings.Repeat("😀", 512))},
		{1, fmt.Sprintf(`{"alias":%q}`, strings.Repeat("😀", 300))},
		{2, fmt.Sprintf(`{"attribute_key":%q,"attribute_value":%q}`, strings.Repeat("😀", 100), strings.Repeat("😀", 10000))},
		{3, `{"attribute_key":"Key"}`}, {3, `{"attribute_value":""}`},
		{4, fmt.Sprintf(`{"tag_ids":[%q]}`, strings.Repeat("界", 128))},
	} {
		if !readOwnedJSON(httptest.NewRecorder(), apiRequest(http.MethodPost, "/", test.body), test.mode) {
			t.Fatal("valid exact Unicode/control/presence boundary was narrowed", test.mode)
		}
	}
}

func TestCatalogLinksJSONRejectsUnknownDuplicateNullOwnerAndMalformedValues(t *testing.T) {
	for _, test := range []struct {
		mode uint8
		body string
		code int
	}{
		{0, `{}`, 422}, {0, `{"symbology":"EAN_13"}`, 422}, {0, `{"raw_value":"00123"}`, 422},
		{0, `{"symbology":null,"raw_value":"00123"}`, 422}, {0, `{"symbology":"OTHER","raw_value":1}`, 422},
		{0, `{"symbology":"OTHER","raw_value":"00123","normalized_value":"00123"}`, 422},
		{0, `{"symbology":"OTHER","raw_value":"00123","item_id":"owner"}`, 422},
		{0, fmt.Sprintf(`{"symbology":"OTHER","raw_value":%q}`, strings.Repeat("x", 513)), 422},
		{1, `{}`, 422}, {1, `{"alias":null}`, 422}, {1, `{"alias":""}`, 422}, {1, `{"Alias":"Name"}`, 422},
		{1, `{"alias":"Name","id":"caller"}`, 422}, {1, `{"alias":"Name","raw_ocr":"Private"}`, 422},
		{1, fmt.Sprintf(`{"alias":%q}`, strings.Repeat("😀", 301)), 422},
		{2, `{"attribute_key":"Key"}`, 422}, {2, `{"attribute_value":""}`, 422},
		{2, `{"attribute_key":"Key","attribute_value":null}`, 422}, {2, `{"attribute_key":"","attribute_value":""}`, 422},
		{3, `{}`, 422}, {3, `{"attribute_key":null}`, 422}, {3, `{"attribute_value":false}`, 422},
		{3, `{"attribute_value":"X","revision":1}`, 422},
		{4, `{}`, 422}, {4, `{"tag_ids":null}`, 422}, {4, `{"tag_ids":"tag"}`, 422},
		{4, `{"tag_ids":["tag","tag"]}`, 422}, {4, `{"tag_ids":[1]}`, 422}, {4, `{"tag_ids":[null]}`, 422},
		{4, `{"tag_ids":[[]]}`, 422}, {4, `{"tag_ids":[""]}`, 422},
		{4, `{"tag_ids":[],"tag_ids":[]}`, 400}, {4, `{"tag_ids":["\ud800"]}`, 400},
		{1, `{"alias":"A","alias":"B"}`, 400}, {1, `{"alias":"\udc00"}`, 400},
		{1, "{\"alias\":\"\xff\"}", 400}, {1, `{"alias":"A"} {}`, 400},
	} {
		w := httptest.NewRecorder()
		w.Header().Set("X-Request-ID", "request")
		if readOwnedJSON(w, apiRequest(http.MethodPost, "/", test.body), test.mode) || w.Code != test.code {
			t.Fatal("invalid owned JSON reached its authoritative command", test.mode, w.Code)
		}
	}
	for mode := uint8(0); mode < 5; mode++ {
		for _, body := range []string{`null`, `[]`, `{"unknown":true}`, `{"x":1,"x":2}`, `{"alias":"\ud800"}`, `{}`} {
			if readOwnedJSON(httptest.NewRecorder(), apiRequest(http.MethodPost, "/", body), mode) {
				t.Fatal("invalid root/unknown/empty object bypassed a reader", mode)
			}
		}
	}
	ids := make([]string, 101)
	for n := range ids {
		ids[n] = fmt.Sprintf("tag-%d", n)
	}
	body, _ := json.Marshal(map[string]any{"tag_ids": ids})
	if readOwnedJSON(httptest.NewRecorder(), apiRequest(http.MethodPut, "/", string(body)), 4) {
		t.Fatal("oversized complete tag set was silently truncated")
	}
}

func TestCatalogLinksJSONMediaChunkedBudgetsAndSafeTransportFailures(t *testing.T) {
	bodies := []string{`{"symbology":"OTHER","raw_value":"00123"}`, `{"alias":"Alias"}`, `{"attribute_key":"Key","attribute_value":""}`, `{"attribute_value":""}`, `{"tag_ids":[]}`}
	for mode, body := range bodies {
		for _, mutate := range []func(*http.Request){
			func(r *http.Request) { r.Header.Del("Content-Type") },
			func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") },
			func(r *http.Request) { r.Header.Set("Content-Type", "application/json; charset=latin1") },
			func(r *http.Request) { r.Header.Add("Content-Type", "application/json") },
			func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") },
		} {
			r := apiRequest(http.MethodPost, "/", body)
			mutate(r)
			w := httptest.NewRecorder()
			w.Header().Set("X-Request-ID", "request")
			if readOwnedJSON(w, r, uint8(mode)) {
				t.Fatal("ambiguous/encoded owned body was accepted")
			}
			expectError(t, w, 415, "UNSUPPORTED_MEDIA_TYPE")
		}
		limit := catalogBodyLimit
		if mode < 2 {
			limit = catalogLinkBodyLimit
		}
		for _, extra := range []int{0, 1} {
			r := apiRequest(http.MethodPost, "/", body+strings.Repeat(" ", limit-len(body)+extra))
			r.ContentLength = -1
			w := httptest.NewRecorder()
			w.Header().Set("X-Request-ID", "request")
			ok := readOwnedJSON(w, r, uint8(mode))
			if extra == 0 && !ok || extra == 1 && (ok || w.Code != 413) {
				t.Fatal("owned parser changed its independent raw-byte body budget", mode, extra, w.Code)
			}
		}
		r := apiRequest(http.MethodPost, "/", body)
		r.Body = io.NopCloser(brokenReader{})
		w := httptest.NewRecorder()
		w.Header().Set("X-Request-ID", "request")
		if readOwnedJSON(w, r, uint8(mode)) || strings.Contains(w.Body.String(), "private transport") {
			t.Fatal("failed body transport reached a command or leaked details")
		}
	}
}

func FuzzCatalogLinksJSON(f *testing.F) {
	for _, body := range []string{`{}`, `null`, `{"symbology":"UPC_E","raw_value":"04252614"}`, `{"alias":"\ud800"}`, `{"attribute_key":"Key","attribute_value":""}`, `{"attribute_value":""}`, `{"tag_ids":[]}`, `{"tag_ids":["tag","tag"]}`} {
		for mode := uint8(0); mode < 5; mode++ {
			f.Add(body, mode)
		}
	}
	f.Fuzz(func(t *testing.T, body string, mode uint8) {
		if len(body) > catalogBodyLimit+1 {
			t.Skip()
		}
		w := httptest.NewRecorder()
		w.Header().Set("X-Request-ID", "request")
		_ = readOwnedJSON(w, apiRequest(http.MethodPost, "/", body), mode)
		if w.Code < 200 || w.Code >= 500 {
			t.Fatal("owned JSON boundary produced an internal HTTP failure")
		}
	})
}
