package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLocationJSONPreservesPresenceUnicodeAndExactIntegers(t *testing.T) {
	for _, body := range []string{
		`{"name":"  箱子 📦 é  ","code":"BOX","description":"<notes>\n","sort_order":1e3}`,
		`{"name":"  箱子 📦 é  ","code":"BOX","description":"<notes>\n","sort_order":1000.00}`,
	} {
		response := httptest.NewRecorder()
		input, ok := readLocationFields(response, apiRequest(http.MethodPost, "/", body), "request")
		if !ok || input.Name == nil || *input.Name != "  箱子 📦 é  " || input.Description == nil || *input.Description != "<notes>\n" || input.SortOrder == nil || *input.SortOrder != 1000 || input.ParentID.Set {
			t.Fatal("valid JSON did not preserve its typed values", response.Body.String())
		}
	}
	for _, body := range []string{`{}`, `{"parent_id":null}`, `{"parent_id":"parent"}`} {
		response := httptest.NewRecorder()
		input, ok := readLocationFields(response, apiRequest(http.MethodPatch, "/", body), "request")
		if !ok || input.ParentID.Set != (body != `{}`) || (body == `{"parent_id":"parent"}` && (input.ParentID.Value == nil || *input.ParentID.Value != "parent")) {
			t.Fatal("nullable parent presence was lost")
		}
	}
}

func TestLocationJSONRejectsAmbiguousWrongTypeAndOutOfBoundsValues(t *testing.T) {
	cases := []struct {
		body string
		code int
	}{
		{`{"name":"one","name":"two"}`, 400},
		{`{"Name":"wrong case"}`, 422}, {`{"unknown":true}`, 422}, {`{"name":null}`, 422},
		{`{"description":null}`, 422}, {`{"kind":null}`, 422}, {`{"kind":""}`, 422},
		{`{"name":[]}`, 422}, {`{"code":1}`, 422}, {`{"status":"deleted"}`, 422},
		{`{"parent_id":{"set":true,"value":null}}`, 422}, {`{"parent_id":""}`, 422},
		{`{"sort_order":null}`, 422}, {`{"sort_order":"1"}`, 422}, {`{"sort_order":true}`, 422},
		{`{"sort_order":1.00000000000000000000001}`, 422}, {`{"sort_order":9007199254740992}`, 422},
		{`{"sort_order":-9007199254740992}`, 422}, {`{"sort_order":1e99999999999999999}`, 422},
		{`{"name":"\ud800"}`, 400}, {`{"code":"\udc00"}`, 400}, {"{\"name\":\"\xff\"}", 400},
		{`{} {}`, 400}, {`[]`, 422}, {`null`, 422}, {`"string"`, 422}, {`{`, 400},
		{`{"name":"` + strings.Repeat("📦", 201) + `"}`, 422},
		{`{"code":"` + strings.Repeat("c", 101) + `"}`, 422},
		{`{"kind":"` + strings.Repeat("k", 33) + `"}`, 422},
		{`{"description":"` + strings.Repeat("d", 10001) + `"}`, 422},
	}
	for _, input := range cases {
		response := httptest.NewRecorder()
		if _, ok := readLocationFields(response, apiRequest(http.MethodPatch, "/", input.body), "request"); ok || response.Code != input.code {
			t.Fatalf("invalid JSON accepted or misclassified: %q: %d %s", input.body, response.Code, response.Body.String())
		}
	}
	// Supplementary Unicode characters fit the body budget when all documented
	// text field bounds are simultaneously exercised.
	body := `{"name":"` + strings.Repeat(`📦`, 200) + `","code":"` + strings.Repeat(`📦`, 100) +
		`","kind":"` + strings.Repeat(`📦`, 32) + `","description":"` + strings.Repeat(`📦`, 10000) + `","sort_order":-9007199254740991}`
	if _, ok := readLocationFields(httptest.NewRecorder(), apiRequest(http.MethodPost, "/", body), "request"); !ok {
		t.Fatal("documented Unicode limits did not fit their body budget")
	}
}

func TestLocationJSONAdmissionTypesAndBodyFailures(t *testing.T) {
	for _, header := range []string{"", "text/plain", "application/json; charset=latin1", "application/json; extra=value", "application/json, application/json"} {
		response := httptest.NewRecorder()
		r := apiRequest(http.MethodPost, "/", `{}`)
		r.Header.Set("Content-Type", header)
		if _, ok := readLocationFields(response, r, "request"); ok || response.Code != 415 {
			t.Fatal("incorrect content type was accepted", header)
		}
	}
	for _, header := range []string{"Content-Type", "Content-Encoding"} {
		response := httptest.NewRecorder()
		r := apiRequest(http.MethodPost, "/", `{}`)
		r.Header.Add(header, "application/json")
		if _, ok := readLocationFields(response, r, "request"); ok || response.Code != 415 {
			t.Fatal("duplicate type or content encoding was accepted")
		}
	}
	response := httptest.NewRecorder()
	if _, ok := readLocationFields(response, apiRequest(http.MethodPost, "/", strings.Repeat(" ", locationBodyLimit+1)), "request"); ok || response.Code != 413 {
		t.Fatal("oversized JSON was read without its admission limit")
	}
	response = httptest.NewRecorder()
	r := apiRequest(http.MethodPost, "/", `{}`)
	r.Body = io.NopCloser(brokenReader{})
	if _, ok := readLocationFields(response, r, "request"); ok || response.Code != 400 || strings.Contains(response.Body.String(), "private transport error") {
		t.Fatal("failed body read was accepted or leaked a diagnostic")
	}
}

func TestExactSafeIntegerNeverRoundsAndBoundsExponentWork(t *testing.T) {
	for _, input := range []struct {
		text string
		want int64
	}{
		{"0", 0}, {"-0", 0}, {"0.00e99999999999999999999999", 0}, {"1.0", 1},
		{"10e-1", 1}, {"1E+3", 1000}, {"9007199254740991", maxJSONInteger},
		{"-9007199254740991", -maxJSONInteger}, {"9007199254740991.000", maxJSONInteger},
		{"1e000000000000000000000000001", 10},
	} {
		value, ok := exactSafeInteger(json.RawMessage(input.text))
		if !ok || value != input.want {
			t.Fatal("exact integer was changed or rejected", input.text, value)
		}
	}
	for _, input := range []string{"", "null", "true", `"1"`, "[]", "01", "-", "1e-1", "1.000000000000000001", "9007199254740990.5", "9007199254740992", "1e18", "1e19", "1e257", "1e-257", "1e9999999999999999999999999999", strings.Repeat("9", 129)} {
		if value, ok := exactSafeInteger(json.RawMessage(input)); ok {
			t.Fatal("nonintegral, unsafe, or unbounded number accepted", input, value)
		}
	}
}

func TestLocationMoveAndArchiveJSONAreExactAndNullable(t *testing.T) {
	for _, body := range []string{`{}`, `{"target_parent_id":null}`, `{"target_parent_id":"parent"}`, `{"target_parent_id":1}`, `{"target_parent_id":""}`, `{"target_parent_id":"parent","extra":true}`} {
		response := httptest.NewRecorder()
		_, ok := readLocationMove(response, apiRequest(http.MethodPost, "/", body), "request")
		want := body == `{"target_parent_id":null}` || body == `{"target_parent_id":"parent"}`
		if ok != want {
			t.Fatal("incorrect move shape admitted", body)
		}
	}
	for _, body := range []string{`{}`, `{"archive_children":true,"reason":"清空"}`, `{"archive_children":false}`, `{"archive_children":null}`, `{"archive_children":"false"}`, `{"reason":null}`, `{"reason":[]}`, `{"extra":true}`} {
		response := httptest.NewRecorder()
		_, _, ok := readLocationArchive(response, apiRequest(http.MethodPost, "/", body), "request")
		want := body == `{}` || body == `{"archive_children":true,"reason":"清空"}` || body == `{"archive_children":false}`
		if ok != want {
			t.Fatal("incorrect archive shape admitted", body)
		}
	}
}

func TestLocationReadsAndDeletesRejectBodiesIncludingChunkedEncoding(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		for _, chunked := range []bool{false, true} {
			r := apiRequest(method, "/", `{}`)
			if chunked {
				r.ContentLength = -1
			}
			if requireNoBody(httptest.NewRecorder(), r, "request") {
				t.Fatal("body-free route consumed a document")
			}
		}
		r := apiRequest(method, "/", "")
		r.Header.Set("Content-Encoding", "gzip")
		if requireNoBody(httptest.NewRecorder(), r, "request") {
			t.Fatal("body-free route accepted a content encoding")
		}
		if !requireNoBody(httptest.NewRecorder(), apiRequest(method, "/", ""), "request") {
			t.Fatal("empty body was rejected")
		}
	}
}

func FuzzLocationJSON(f *testing.F) {
	for _, body := range []string{`{}`, `{"name":"Box"}`, `{"parent_id":null}`, `{"sort_order":1.0}`, `{"name":"\ud800"}`, `{"name":{},"name":false}`} {
		f.Add([]byte(body))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		if len(body) > locationBodyLimit+1 {
			return
		}
		r := apiRequest(http.MethodPatch, "/", string(body))
		response := httptest.NewRecorder()
		input, ok := readLocationFields(response, r, "fuzz-request")
		if ok && (input.SortOrder != nil && (*input.SortOrder > maxJSONInteger || *input.SortOrder < -maxJSONInteger)) {
			t.Fatal("unsafe integer escaped the strict reader")
		}
	})
}
