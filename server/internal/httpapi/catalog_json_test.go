package httpapi

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCatalogJSONExactNullablePresenceAndIntegerForms(t *testing.T) {
	r := apiRequest(http.MethodPatch, "/api/v1/items/id", `{"name":"  物品 😀 é  ","brand":null,"model":"","category_id":null,"description":"","notes":"Note","tracking_mode":"quantity","unit_code":"kg","base_unit":"g","scale_factor":1e3,"status":"archived"}`)
	response := httptest.NewRecorder()
	response.Header().Set("X-Request-ID", "request")
	fields, ok := readItemFields(response, r, "request")
	if !ok || fields.Name == nil || *fields.Name != "  物品 😀 é  " || !fields.Brand.Set || fields.Brand.Value != nil || !fields.Model.Set || fields.Model.Value == nil || *fields.Model.Value != "" ||
		!fields.CategoryID.Set || fields.CategoryID.Value != nil || fields.Description == nil || *fields.Description != "" || fields.ScaleFactor == nil || *fields.ScaleFactor != 1000 {
		t.Fatal("item JSON lost exact text, explicit null, empty text or an exact exponent")
	}
	minimal, ok := readItemCreate(httptest.NewRecorder(), apiRequest(http.MethodPost, "/api/v1/items", `{"name":"Minimal"}`), "request")
	if !ok || minimal.Name != "Minimal" || minimal.UnitCode != nil || minimal.BaseUnit != nil || minimal.ScaleFactor != nil || minimal.TrackingMode != nil || minimal.Status != nil || minimal.Brand != nil || minimal.Model != nil {
		t.Fatal("HTTP creation incorrectly installed or required optional server defaults")
	}
	for _, number := range []string{"1", "1.0", "10e-1", "9007199254740991", "9007199254740991.0"} {
		fields, ok := readItemFields(httptest.NewRecorder(), apiRequest(http.MethodPatch, "/api/v1/items/id", `{"scale_factor":`+number+`}`), "request")
		if !ok || fields.ScaleFactor == nil || *fields.ScaleFactor < 1 || *fields.ScaleFactor > maxJSONInteger {
			t.Fatal("a valid exact positive JSON integer was rejected", number)
		}
	}
	for _, body := range []string{`{}`, `{"reason":""}`, `{"reason":"  Retain history 😀  "}`} {
		if _, ok := readItemArchive(httptest.NewRecorder(), apiRequest(http.MethodPost, "/api/v1/items/id/archive", body), "request"); !ok {
			t.Fatal("valid empty/optional archive reason was rejected", body)
		}
	}
}

func TestCatalogJSONRejectsUnknownNamesTypesNullsDuplicatesUnicodeAndUnsafeIntegers(t *testing.T) {
	for _, test := range []struct {
		body string
		code int
	}{
		{`{}`, 422}, {`[]`, 422}, {`null`, 422}, {`{"Name":"Case"}`, 422}, {`{"name":"A","name":"B"}`, 400},
		{`{"brand":"A","brand":"B"}`, 400}, {`{"name":"X","quantity_base":1}`, 422}, {`{"name":"X","raw_ocr":"Private"}`, 422},
		{`{"name":null}`, 422}, {`{"name":1}`, 422}, {`{"brand":1}`, 422}, {`{"model":false}`, 422}, {`{"category_id":""}`, 422},
		{`{"brand":{"set":true,"value":null}}`, 422}, {`{"category_id":{"set":true,"value":null}}`, 422},
		{`{"description":null}`, 422}, {`{"notes":null}`, 422}, {`{"tracking_mode":null}`, 422}, {`{"tracking_mode":"mixed"}`, 422},
		{`{"unit_code":null}`, 422}, {`{"base_unit":null}`, 422}, {`{"status":null}`, 422}, {`{"status":"disposed"}`, 422},
		{`{"scale_factor":null}`, 422}, {`{"scale_factor":"1"}`, 422}, {`{"scale_factor":0}`, 422}, {`{"scale_factor":-1}`, 422},
		{`{"scale_factor":0.5}`, 422}, {`{"scale_factor":9007199254740990.5}`, 422}, {`{"scale_factor":9007199254740992}`, 422},
		{`{"scale_factor":1e9999999}`, 422}, {`{"name":"\ud800"}`, 400}, {`{"brand":"\udc00"}`, 400},
		{"{\"name\":\"\xff\"}", 400}, {`{"name":"X"} {}`, 400}, {`{"name":"X"} trailing`, 400},
	} {
		t.Run(test.body, func(t *testing.T) {
			response := httptest.NewRecorder()
			response.Header().Set("X-Request-ID", "request")
			if _, ok := readItemFields(response, apiRequest(http.MethodPatch, "/api/v1/items/id", test.body), "request"); ok || response.Code != test.code {
				t.Fatal("invalid item JSON reached its command", response.Code, response.Body.String())
			}
		})
	}
	for _, test := range []struct {
		field   string
		maximum int
		minimum int
	}{
		{"name", 300, 1}, {"brand", 300, 0}, {"model", 300, 0}, {"category_id", 128, 1},
		{"description", 10000, 0}, {"notes", 10000, 0}, {"unit_code", 32, 1}, {"base_unit", 32, 1},
	} {
		text := strings.Repeat("😀", test.maximum)
		if test.field == "base_unit" {
			text = strings.Repeat("g", test.maximum)
		}
		good := fmt.Sprintf(`{"%s":%q}`, test.field, text)
		if _, ok := readItemFields(httptest.NewRecorder(), apiRequest(http.MethodPatch, "/api/v1/items/id", good), "request"); !ok {
			t.Fatal("valid field code-point bound was narrowed", test.field)
		}
		bad := fmt.Sprintf(`{"%s":%q}`, test.field, strings.Repeat("x", test.maximum+1))
		if _, ok := readItemFields(httptest.NewRecorder(), apiRequest(http.MethodPatch, "/api/v1/items/id", bad), "request"); ok {
			t.Fatal("field maximum was not enforced", test.field)
		}
		if test.minimum == 1 {
			empty := fmt.Sprintf(`{"%s":""}`, test.field)
			if _, ok := readItemFields(httptest.NewRecorder(), apiRequest(http.MethodPatch, "/api/v1/items/id", empty), "request"); ok {
				t.Fatal("required nonempty field accepted an empty value", test.field)
			}
		}
	}
	for _, body := range []string{`{"brand":null}`, `{"status":"active"}`} {
		if _, ok := readItemCreate(httptest.NewRecorder(), apiRequest(http.MethodPost, "/api/v1/items", body), "request"); ok {
			t.Fatal("item creation accepted a patch without its required name")
		}
	}
	for _, body := range []string{`{"reason":null}`, `{"reason":1}`, `{"archive_children":true}`, `{"reason":"A","reason":"B"}`} {
		if _, ok := readItemArchive(httptest.NewRecorder(), apiRequest(http.MethodPost, "/api/v1/items/id/archive", body), "request"); ok {
			t.Fatal("invalid archive body was accepted")
		}
	}
}

func TestCatalogJSONRetainsMediaLimitsChunkedBoundariesAndSafeReadErrors(t *testing.T) {
	for _, media := range []string{"", "text/plain", "application/json; charset=latin1", "application/json; unexpected=1"} {
		r := apiRequest(http.MethodPost, "/api/v1/items", `{"name":"Item"}`)
		r.Header.Set("Content-Type", media)
		response := httptest.NewRecorder()
		response.Header().Set("X-Request-ID", "request")
		if _, ok := readItemCreate(response, r, "request"); ok {
			t.Fatal("unsupported item media type was accepted")
		}
		expectError(t, response, 415, "UNSUPPORTED_MEDIA_TYPE")
	}
	for _, mutate := range []func(*http.Request){
		func(r *http.Request) { r.Header.Add("Content-Type", "application/json") },
		func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") },
	} {
		r := apiRequest(http.MethodPost, "/api/v1/items", `{"name":"Item"}`)
		mutate(r)
		response := httptest.NewRecorder()
		response.Header().Set("X-Request-ID", "request")
		if _, ok := readItemCreate(response, r, "request"); ok {
			t.Fatal("ambiguous/encoded item body was accepted")
		}
		expectError(t, response, 415, "UNSUPPORTED_MEDIA_TYPE")
	}
	for _, extra := range []int{0, 1} {
		body := `{"name":"Item"}`
		body += strings.Repeat(" ", catalogBodyLimit-len(body)+extra)
		r := apiRequest(http.MethodPost, "/api/v1/items", body)
		r.ContentLength = -1
		response := httptest.NewRecorder()
		response.Header().Set("X-Request-ID", "request")
		_, ok := readItemCreate(response, r, "request")
		if extra == 0 && !ok || extra == 1 && ok {
			t.Fatal("exact chunked body byte limit was not retained")
		}
		if extra == 1 {
			expectError(t, response, 413, "BODY_TOO_LARGE")
		}
	}
	r := apiRequest(http.MethodPost, "/api/v1/items", `{"name":"Unread"}`)
	r.Body = io.NopCloser(brokenReader{})
	response := httptest.NewRecorder()
	response.Header().Set("X-Request-ID", "request")
	if _, ok := readItemCreate(response, r, "request"); ok {
		t.Fatal("failed item body read produced a command")
	}
	expectError(t, response, 400, "INVALID_BODY")
}

func FuzzCatalogJSON(f *testing.F) {
	for _, body := range []string{`{"name":"Item"}`, `{"brand":null}`, `{"scale_factor":1e3}`, `{"reason":""}`, `{"name":"\ud800"}`, `{"name":"X","name":"Y"}`} {
		f.Add(body, byte(0))
	}
	f.Fuzz(func(t *testing.T, body string, reader byte) {
		if len(body) > catalogBodyLimit+1 {
			return
		}
		r := apiRequest(http.MethodPost, "/api/v1/items", body)
		response := httptest.NewRecorder()
		response.Header().Set("X-Request-ID", "request")
		switch reader % 3 {
		case 0:
			value, ok := readItemFields(response, r, "request")
			if ok && value.ScaleFactor != nil && (*value.ScaleFactor < 1 || *value.ScaleFactor > maxJSONInteger) {
				t.Fatal("accepted scale escaped its exact integer bound")
			}
		case 1:
			value, ok := readItemCreate(response, r, "request")
			if ok && !boundedString(value.Name, 1, 300) {
				t.Fatal("accepted creation omitted a valid name")
			}
		case 2:
			value, ok := readItemArchive(response, r, "request")
			if ok && !boundedString(value, 0, 10000) {
				t.Fatal("accepted archive escaped its reason bound")
			}
		}
	})
}
