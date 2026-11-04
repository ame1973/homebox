package httpapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAuthenticationJSONIsBoundedExactAndDoesNotRepairCredentials(t *testing.T) {
	cases := []struct {
		name, body string
		status     int
	}{
		{"empty", "", 400},
		{"truncated", `{"username":"x"`, 400},
		{"two documents", `{"username":"x","password":"y"} {}`, 400},
		{"trailing garbage", `{"username":"x","password":"y"} true`, 400},
		{"duplicate", `{"username":"first","username":"second","password":"x"}`, 400},
		{"escaped duplicate", "{\"username\":\"first\",\"\\u0075sername\":\"second\",\"password\":\"x\"}", 400},
		{"invalid utf8", "{\"username\":\"\xff\",\"password\":\"x\"}", 400},
		{"lone high surrogate", `{"username":"\ud800","password":"x"}`, 400},
		{"lone low surrogate", `{"username":"x","password":"\udfff"}`, 400},
		{"high surrogate then scalar", `{"username":"x","password":"\ud800A"}`, 400},
		{"high surrogate then escape", `{"username":"x","password":"\ud800\\udc00"}`, 400},
		{"null object", `null`, 422},
		{"array", `[]`, 422},
		{"string object", `"x"`, 422},
		{"number object", `1`, 422},
		{"boolean object", `true`, 422},
		{"missing", `{"username":"x"}`, 422},
		{"unknown", `{"username":"x","password":"y","debug":true}`, 422},
		{"case substitution", `{"Username":"x","password":"y"}`, 422},
		{"null string", `{"username":"x","password":null}`, 422},
		{"number string", `{"username":"x","password":123}`, 422},
		{"array string", `{"username":"x","password":[]}`, 422},
		{"object string", `{"username":"x","password":{"a":1,"a":2}}`, 422},
		{"oversize", `{"username":"x","password":"` + strings.Repeat("x", authBodyLimit) + `"}`, 413},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := apiRequest(http.MethodPost, "/api/v1/auth/login", tc.body)
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			values, ok := readAuthJSON(w, r, "json-test-request", "username", "password")
			if ok || values != nil || w.Code != tc.status || w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), tc.body) && tc.body != "" {
				t.Fatalf("invalid input accepted or echoed: %d %s", w.Code, w.Body.String())
			}
		})
	}
	for _, body := range []string{
		`{"username":"管理員","password":"  原樣密碼 😀  "}`,
		"{\"username\":\"管理員\",\"password\":\"  原樣密碼 \\ud83d\\ude00  \"}",
	} {
		w := httptest.NewRecorder()
		values, ok := readAuthJSON(w, apiRequest(http.MethodPost, "/api/v1/auth/login", body), "json-test-request", "username", "password")
		if !ok || values["username"] != "管理員" || values["password"] != "  原樣密碼 😀  " {
			t.Fatal("valid Unicode/surrogate-pair input was changed", w.Body.String())
		}
	}
	body := `{"username":"x","password":"literal\\ud800\/end"}`
	values, ok := readAuthJSON(httptest.NewRecorder(), apiRequest(http.MethodPost, "/api/v1/auth/login", body), "json-test-request", "username", "password")
	if !ok || values["password"] != `literal\ud800/end` {
		t.Fatal("escaped backslash or slash was incorrectly treated as a surrogate")
	}
}

func TestAuthenticationContentTypeEncodingAndByteLimit(t *testing.T) {
	body := `{"username":"x","password":"y"}`
	for _, mediaType := range []string{"", "text/plain", "application/json; charset=iso-8859-1", "application/json; boundary=x", "application/json; charset=utf-8; other=x", "application/json,"} {
		r := apiRequest(http.MethodPost, "/api/v1/auth/login", body)
		r.Header.Set("Content-Type", mediaType)
		w := httptest.NewRecorder()
		if _, ok := readAuthJSON(w, r, "json-test-request", "username", "password"); ok || w.Code != http.StatusUnsupportedMediaType {
			t.Fatal("unsupported JSON representation was accepted")
		}
	}
	for _, header := range []string{"Content-Type", "Content-Encoding"} {
		r := apiRequest(http.MethodPost, "/api/v1/auth/login", body)
		if header == "Content-Type" {
			r.Header.Add(header, "application/json")
		} else {
			r.Header.Set(header, "gzip")
		}
		w := httptest.NewRecorder()
		if _, ok := readAuthJSON(w, r, "json-test-request", "username", "password"); ok || w.Code != http.StatusUnsupportedMediaType {
			t.Fatal("duplicate type or encoded body was accepted")
		}
	}
	for _, mediaType := range []string{"application/json", "application/json; charset=UTF-8", "Application/JSON; Charset=utf-8"} {
		r := apiRequest(http.MethodPost, "/api/v1/auth/login", body+strings.Repeat(" ", authBodyLimit-len(body)))
		r.Header.Set("Content-Type", mediaType)
		if _, ok := readAuthJSON(httptest.NewRecorder(), r, "json-test-request", "username", "password"); !ok {
			t.Fatal("a valid body at the exact byte limit was refused")
		}
	}
	r := httptest.NewRequest(http.MethodPost, "https://homebox.test/api/v1/auth/login", brokenReader{})
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	if _, ok := readAuthJSON(w, r, "json-test-request", "username", "password"); ok || w.Code != http.StatusBadRequest {
		t.Fatal("body read failure was ignored")
	}
}

type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, errors.New("private transport error") }

func FuzzAuthenticationJSON(f *testing.F) {
	for _, seed := range []string{`{}`, `null`, `{"username":"x","password":"y"}`, `{"username":"x","password":"\ud800"}`, `{"username":"x","password":"😀"}`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, body string) {
		r := apiRequest(http.MethodPost, "/api/v1/auth/login", body)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		_, _ = readAuthJSON(w, r, "json-fuzz-request", "username", "password")
	})
}
