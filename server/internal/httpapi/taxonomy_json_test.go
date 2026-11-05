package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTaxonomyJSONPreservesExactNamesAndNullableParentPresence(t *testing.T) {
	for _, input := range []struct {
		body string
		set  bool
		id   string
	}{
		{`{"name":"  分類 😀 é  "}`, false, ""},
		{`{"name":"  分類 😀 é  ","parent_id":null}`, true, ""},
		{`{"name":"  分類 😀 é  ","parent_id":"root"}`, true, "root"},
	} {
		fields, ok := readCategoryFields(httptest.NewRecorder(), apiRequest(http.MethodPatch, "/", input.body), "request")
		if !ok || fields.Name == nil || *fields.Name != "  分類 😀 é  " || fields.ParentID.Set != input.set ||
			input.id != "" && (fields.ParentID.Value == nil || *fields.ParentID.Value != input.id) || input.id == "" && fields.ParentID.Value != nil {
			t.Fatal("category input lost exact text or nullable parent presence")
		}
	}
	for _, body := range []string{`{"parent_id":null}`, `{"parent_id":"root"}`} {
		fields, ok := readCategoryFields(httptest.NewRecorder(), apiRequest(http.MethodPatch, "/", body), "request")
		if !ok || fields.Name != nil || !fields.ParentID.Set {
			t.Fatal("parent-only category move was not a partial patch")
		}
	}
	for _, name := range []string{"  標籤 😀 é  ", " ", strings.Repeat("😀", 200)} {
		body := `{"name":"` + name + `"}`
		got, ok := readTagName(httptest.NewRecorder(), apiRequest(http.MethodPost, "/", body), "request")
		if !ok || got != name {
			t.Fatal("exact tag name was trimmed, normalized, or truncated")
		}
		fields, ok := readCategoryFields(httptest.NewRecorder(), apiRequest(http.MethodPost, "/", body), "request")
		if !ok || fields.Name == nil || *fields.Name != name {
			t.Fatal("category Unicode bounds differed from tag bounds")
		}
	}
}

func TestTaxonomyJSONRejectsAmbiguousWrongTypeAndOutOfBoundsFields(t *testing.T) {
	for _, input := range []struct {
		body string
		code int
	}{
		{`{}`, 422}, {`[]`, 422}, {`null`, 422}, {`"name"`, 422}, {`{`, 400}, {`{} {}`, 400},
		{`{"Name":"wrong case"}`, 422}, {`{"unknown":true}`, 422}, {`{"name":null}`, 422},
		{`{"name":1}`, 422}, {`{"name":false}`, 422}, {`{"name":[]}`, 422}, {`{"name":{}}`, 422},
		{`{"name":""}`, 422}, {`{"name":"first","name":"second"}`, 400},
		{`{"name":"\ud800"}`, 400}, {`{"name":"\udc00"}`, 400}, {"{\"name\":\"\xff\"}", 400},
		{`{"name":"` + strings.Repeat("😀", 201) + `"}`, 422},
	} {
		for _, category := range []bool{false, true} {
			response := httptest.NewRecorder()
			r := apiRequest(http.MethodPost, "/", input.body)
			var ok bool
			if category {
				_, ok = readCategoryFields(response, r, "request")
			} else {
				_, ok = readTagName(response, r, "request")
			}
			if ok || response.Code != input.code {
				t.Fatalf("invalid taxonomy JSON was accepted or misclassified: %q: %d %s", input.body, response.Code, response.Body.String())
			}
		}
	}
	for _, body := range []string{
		`{"parent_id":""}`, `{"parent_id":false}`, `{"parent_id":1}`, `{"parent_id":[]}`,
		`{"parent_id":{"set":true,"value":null}}`, `{"parent_id":"` + strings.Repeat("😀", 129) + `"}`,
		`{"parent_id":null,"parent_id":"parent"}`, `{"parent_id":"\ud800"}`,
	} {
		if _, ok := readCategoryFields(httptest.NewRecorder(), apiRequest(http.MethodPatch, "/", body), "request"); ok {
			t.Fatal("invalid or ambiguous category parent was admitted", body)
		}
	}
	if _, ok := readTagName(httptest.NewRecorder(), apiRequest(http.MethodPost, "/", `{"name":"Tag","parent_id":null}`), "request"); ok {
		t.Fatal("tag accepted category-only input")
	}
}

func TestTaxonomyJSONAdmissionHasItsOwnBoundedBudgetAndSafeReadFailures(t *testing.T) {
	for _, category := range []bool{false, true} {
		read := func(response http.ResponseWriter, r *http.Request) bool {
			if category {
				_, ok := readCategoryFields(response, r, "request")
				return ok
			}
			_, ok := readTagName(response, r, "request")
			return ok
		}
		for _, contentType := range []string{"", "text/plain", "application/json; charset=latin1", "application/json; extra=value", "application/json, application/json"} {
			r := apiRequest(http.MethodPost, "/", `{"name":"Valid"}`)
			r.Header.Set("Content-Type", contentType)
			response := httptest.NewRecorder()
			if read(response, r) || response.Code != 415 {
				t.Fatal("unsupported taxonomy media type was accepted", contentType)
			}
		}
		for _, header := range []string{"Content-Type", "Content-Encoding"} {
			r := apiRequest(http.MethodPost, "/", `{"name":"Valid"}`)
			r.Header.Add(header, "application/json")
			response := httptest.NewRecorder()
			if read(response, r) || response.Code != 415 {
				t.Fatal("duplicate content type or content encoding was accepted")
			}
		}
		for _, size := range []int{taxonomyBodyLimit, taxonomyBodyLimit + 1} {
			body := `{"name":"Valid"}`
			body += strings.Repeat(" ", size-len(body))
			r := apiRequest(http.MethodPost, "/", body)
			r.ContentLength = -1
			response := httptest.NewRecorder()
			if ok := read(response, r); ok != (size == taxonomyBodyLimit) || !ok && response.Code != 413 {
				t.Fatal("taxonomy JSON body limit changed or trusted a missing content length")
			}
		}
		response := httptest.NewRecorder()
		r := apiRequest(http.MethodPost, "/", `{"name":"Valid"}`)
		r.Body = io.NopCloser(brokenReader{})
		if read(response, r) || response.Code != 400 || strings.Contains(response.Body.String(), "private transport error") {
			t.Fatal("failed taxonomy body read escaped its safe boundary")
		}
	}
}

func FuzzTaxonomyJSON(f *testing.F) {
	for _, body := range []string{`{}`, `{"name":"分類"}`, `{"parent_id":null}`, `{"name":"\ud800"}`, `{"name":"first","name":"second"}`, `{"parent_id":{"set":true}}`} {
		f.Add([]byte(body))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		if len(body) > taxonomyBodyLimit+1 {
			return
		}
		fields, ok := readCategoryFields(httptest.NewRecorder(), apiRequest(http.MethodPatch, "/", string(body)), "fuzz-request")
		if ok && (fields.Name == nil && !fields.ParentID.Set || fields.Name != nil && !boundedString(*fields.Name, 1, 200) ||
			fields.ParentID.Value != nil && (!fields.ParentID.Set || !boundedString(*fields.ParentID.Value, 1, 128))) {
			t.Fatal("invalid category shape escaped the strict reader")
		}
		if name, ok := readTagName(httptest.NewRecorder(), apiRequest(http.MethodPost, "/", string(body)), "fuzz-request"); ok && !boundedString(name, 1, 200) {
			t.Fatal("invalid tag text escaped the strict reader")
		}
	})
}
