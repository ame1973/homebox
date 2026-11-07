package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

var assetJSONBodies = []struct {
	kind, body string
	required   []string
}{
	{"profile", `{"item_revision":1,"asset_revision":null,"cost_basis":"manual","currency":"JPY","manual_acquisition_minor":0,"in_service_at":null,"retired_at":null,"estimated_residual_minor":null,"usage_unit":"times"}`,
		[]string{"item_revision", "asset_revision", "cost_basis", "currency", "manual_acquisition_minor", "in_service_at", "retired_at", "estimated_residual_minor", "usage_unit"}},
	{"usage", `{"delta":1,"usage_unit":"times","event_type":"use","occurred_at":"2026-11-07T00:00:00Z"}`, []string{"delta", "usage_unit", "event_type", "occurred_at"}},
	{"cost", `{"amount_minor":1,"currency":"JPY","kind":"repair","direction":"expense","occurred_at":"2026-11-07T00:00:00Z"}`, []string{"amount_minor", "currency", "kind", "direction", "occurred_at"}},
	{"retire", `{"unit_revision":1,"retired_at":"2026-11-07T00:00:00Z"}`, []string{"unit_revision", "retired_at"}},
	{"disposal", `{"unit_revision":1,"kind":"sold","disposed_at":"2026-11-07T00:00:00Z","realized_proceeds_minor":0,"currency":"JPY"}`, []string{"unit_revision", "kind", "disposed_at", "realized_proceeds_minor", "currency"}},
}

func readAssetJSONForTest(kind string, w http.ResponseWriter, r *http.Request) (any, bool) {
	// Mirror the real outer request-ID wrapper for direct parser checks.
	w.Header().Set("X-Request-ID", "request")
	switch kind {
	case "profile":
		return readAssetProfile(w, r, "request")
	case "usage":
		return readAssetUsage(w, r, "request")
	case "cost":
		return readAssetCost(w, r, "request")
	case "retire":
		return readAssetRetirement(w, r, "request")
	default:
		return readAssetDisposal(w, r, "request")
	}
}

func assetJSONMutation(t *testing.T, body, name, raw string) string {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &fields); err != nil {
		t.Fatal(err)
	}
	if raw == "" {
		delete(fields, name)
	} else {
		fields[name] = json.RawMessage(raw)
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestAssetsJSONRequiresEveryActualDecisionIncludingNullableFieldsAndExplicitZero(t *testing.T) {
	for _, test := range assetJSONBodies {
		t.Run(test.kind, func(t *testing.T) {
			if _, ok := readAssetJSONForTest(test.kind, httptest.NewRecorder(), apiRequest(http.MethodPost, "/api/v1/fixture", test.body)); !ok {
				t.Fatal("complete actual decision was rejected")
			}
			for _, field := range test.required {
				w := httptest.NewRecorder()
				value, ok := readAssetJSONForTest(test.kind, w, apiRequest(http.MethodPost, "/api/v1/fixture", assetJSONMutation(t, test.body, field, "")))
				if ok || !reflect.ValueOf(value).IsZero() {
					t.Fatal("omitted required decision became a nullable/default zero", field, value)
				}
				expectError(t, w, 422, "VALIDATION_ERROR")
			}
		})
	}
}

func TestAssetsJSONParsesSafeIndependentIntegersExactlyAndRejectsRoundingOrWrongTypes(t *testing.T) {
	for _, test := range []struct {
		index          int
		field          string
		valid, invalid []string
	}{
		{0, "item_revision", []string{"1.0", "1e0", "9007199254740991"}, []string{"0", "-1", "null", "true", `"1"`, "1.1", "9007199254740992", "9007199254740990.9999999999"}},
		{0, "asset_revision", []string{"null", "1.0", "1e0"}, []string{"0", "-1", "false", `"1"`, "1.1", "9007199254740992"}},
		{0, "manual_acquisition_minor", []string{"0.0e999", "1e2", "9007199254740991"}, []string{"null", "-1", "true", `"0"`, "0.1", "9007199254740992"}},
		{0, "estimated_residual_minor", []string{"null", "-0.0e999", "1e2"}, []string{"-1", "true", `"0"`, "0.1", "9007199254740992"}},
		{1, "delta", []string{"1.0", "1e2", "9007199254740991"}, []string{"0", "-0.0e999", "-1", "null", "true", `"1"`, "1.1", "9007199254740992"}},
		{2, "amount_minor", []string{"1.0", "1e2", "9007199254740991"}, []string{"0", "-1", "null", "true", `"1"`, "1.1", "9007199254740992"}},
		{3, "unit_revision", []string{"1.0", "1e2", "9007199254740991"}, []string{"0", "-1", "null", "true", `"1"`, "1.1", "9007199254740992"}},
		{4, "realized_proceeds_minor", []string{"-0.0e999", "1e2", "9007199254740991"}, []string{"-1", "null", "true", `"0"`, "0.1", "9007199254740992"}},
	} {
		for _, raw := range test.valid {
			body := assetJSONMutation(t, assetJSONBodies[test.index].body, test.field, raw)
			if _, ok := readAssetJSONForTest(assetJSONBodies[test.index].kind, httptest.NewRecorder(), apiRequest(http.MethodPost, "/api/v1/fixture", body)); !ok {
				t.Fatal("exact schema integer/null was coerced or rejected", test.field, raw)
			}
		}
		for _, raw := range test.invalid {
			body := assetJSONMutation(t, assetJSONBodies[test.index].body, test.field, raw)
			w := httptest.NewRecorder()
			value, ok := readAssetJSONForTest(assetJSONBodies[test.index].kind, w, apiRequest(http.MethodPost, "/api/v1/fixture", body))
			if ok || !reflect.ValueOf(value).IsZero() {
				t.Fatal("unsafe/fractional/boolean/string integer became a financial decision", test.field, raw)
			}
			expectError(t, w, 422, "VALIDATION_ERROR")
		}
	}
}

func TestAssetsJSONPreservesDiscriminatedReferencesNullableDatesAndBoundedExactNotes(t *testing.T) {
	for _, test := range []struct {
		kind, body string
		valid      bool
	}{
		{"profile", assetJSONMutation(t, assetJSONBodies[0].body, "cost_basis", `"future"`), false},
		{"profile", assetJSONMutation(t, assetJSONBodies[0].body, "currency", `true`), false},
		{"profile", assetJSONMutation(t, assetJSONBodies[0].body, "usage_unit", `null`), false},
		{"profile", assetJSONMutation(t, assetJSONBodies[0].body, "cost_basis", `"purchase"`), false},
		{"profile", assetJSONMutation(t, assetJSONMutation(t, assetJSONBodies[0].body, "cost_basis", `"purchase"`), "manual_acquisition_minor", "null"), true},
		{"profile", assetJSONMutation(t, assetJSONBodies[0].body, "in_service_at", `""`), false},
		{"profile", assetJSONMutation(t, assetJSONBodies[0].body, "in_service_at", `"2026-11-07T00:00:00Z"`), true},
		{"usage", assetJSONMutation(t, assetJSONBodies[1].body, "correction_of", "null"), true},
		{"usage", assetJSONMutation(t, assetJSONBodies[1].body, "correction_of", `"target"`), false},
		{"usage", `{"delta":-1,"usage_unit":"times","event_type":"correction","occurred_at":"2026-11-07T00:00:00Z","correction_of":"target"}`, true},
		{"usage", `{"delta":-1,"usage_unit":"times","event_type":"correction","occurred_at":"2026-11-07T00:00:00Z","correction_of":null}`, false},
		{"cost", assetJSONMutation(t, assetJSONBodies[2].body, "correction_of", `"target"`), false},
		{"cost", assetJSONMutation(t, assetJSONBodies[2].body, "kind", `"adjustment"`), false},
		{"cost", assetJSONMutation(t, assetJSONMutation(t, assetJSONBodies[2].body, "kind", `"adjustment"`), "correction_of", `"target"`), true},
		{"disposal", assetJSONMutation(t, assetJSONBodies[4].body, "kind", `"donated"`), true},
		{"disposal", assetJSONMutation(t, assetJSONMutation(t, assetJSONBodies[4].body, "kind", `"other"`), "realized_proceeds_minor", "1"), false},
	} {
		w := httptest.NewRecorder()
		if _, ok := readAssetJSONForTest(test.kind, w, apiRequest(http.MethodPost, "/api/v1/fixture", test.body)); ok != test.valid {
			t.Fatal("reference/null/base/explicit-disposal decision lost its discriminant", test.kind, test.valid)
		}
	}
	for _, test := range assetJSONBodies[1:] {
		for _, text := range []string{"", "\x00  Exact é\n ", strings.Repeat("😀", 10000)} {
			raw, _ := json.Marshal(text)
			body := assetJSONMutation(t, test.body, "note", string(raw))
			value, ok := readAssetJSONForTest(test.kind, httptest.NewRecorder(), apiRequest(http.MethodPost, "/api/v1/fixture", body))
			encoded, _ := json.Marshal(value)
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(encoded, &fields)
			if !ok || string(fields["note"]) != string(raw) {
				t.Fatal("valid exact/native-control/max-width note was changed", test.kind)
			}
		}
		for _, raw := range []string{"null", `"` + strings.Repeat("界", 10001) + `"`} {
			if _, ok := readAssetJSONForTest(test.kind, httptest.NewRecorder(), apiRequest(http.MethodPost, "/api/v1/fixture", assetJSONMutation(t, test.body, "note", raw))); ok {
				t.Fatal("null/oversized note was coerced or accepted", test.kind)
			}
		}
	}
}

func TestAssetsJSONAllReadersEnforceActualBodyBudgetsExactFieldsAndUnicode(t *testing.T) {
	for _, test := range assetJSONBodies {
		for _, body := range []string{`null`, `[]`, `{}`, test.body + `{}`, test.body[:len(test.body)-1] + `,"extra":1}`,
			test.body[:len(test.body)-1] + `,"note":"\ud800"}`, test.body[:len(test.body)-1] + ",\"note\":\"" + string([]byte{0xff}) + "\"}",
			test.body[:len(test.body)-1] + `,"currency":"JPY","currency":"USD"}`} {
			w := httptest.NewRecorder()
			value, ok := readAssetJSONForTest(test.kind, w, apiRequest(http.MethodPost, "/api/v1/fixture", body))
			if ok || !reflect.ValueOf(value).IsZero() || w.Code == 200 {
				t.Fatal("invalid exact JSON/Unicode/duplicate boundary returned a financial proposal", test.kind)
			}
		}
		body := test.body + strings.Repeat(" ", assetBodyLimit-len(test.body))
		if _, ok := readAssetJSONForTest(test.kind, httptest.NewRecorder(), apiRequest(http.MethodPost, "/api/v1/fixture", body)); !ok {
			t.Fatal("exact documented 128-KiB body boundary was rejected", test.kind)
		}
		w := httptest.NewRecorder()
		if _, ok := readAssetJSONForTest(test.kind, w, apiRequest(http.MethodPost, "/api/v1/fixture", body+" ")); ok {
			t.Fatal("over-budget financial JSON was accepted", test.kind)
		}
		expectError(t, w, 413, "BODY_TOO_LARGE")
	}
}

func TestAssetsJSONMediaChunkedBudgetsAndReadFailuresStayBounded(t *testing.T) {
	for _, test := range assetJSONBodies {
		for _, mutate := range []func(*http.Request){
			func(r *http.Request) { r.Header.Del("Content-Type") },
			func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") },
			func(r *http.Request) { r.Header.Set("Content-Type", "application/json; charset=latin1") },
			func(r *http.Request) { r.Header.Add("Content-Type", "application/json") },
			func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") },
		} {
			r := apiRequest(http.MethodPost, "/", test.body)
			mutate(r)
			w := httptest.NewRecorder()
			value, ok := readAssetJSONForTest(test.kind, w, r)
			if ok || !reflect.ValueOf(value).IsZero() {
				t.Fatal("encoded/ambiguous financial JSON returned a partial proposal", test.kind)
			}
			expectError(t, w, 415, "UNSUPPORTED_MEDIA_TYPE")
		}
		for _, extra := range []int{0, 1} {
			r := apiRequest(http.MethodPost, "/", test.body+strings.Repeat(" ", assetBodyLimit-len(test.body)+extra))
			r.ContentLength = -1
			w := httptest.NewRecorder()
			_, ok := readAssetJSONForTest(test.kind, w, r)
			if ok != (extra == 0) {
				t.Fatal("chunked financial JSON bypassed the actual raw-byte budget", test.kind, extra)
			}
			if extra != 0 {
				expectError(t, w, 413, "BODY_TOO_LARGE")
			}
		}
		r := apiRequest(http.MethodPost, "/", test.body)
		r.Body = io.NopCloser(brokenReader{})
		w := httptest.NewRecorder()
		value, ok := readAssetJSONForTest(test.kind, w, r)
		if ok || !reflect.ValueOf(value).IsZero() || strings.Contains(w.Body.String(), "private transport") {
			t.Fatal("failed financial body read returned a value or transport diagnostics")
		}
		expectError(t, w, 400, "INVALID_BODY")
	}
}

func FuzzAssetsJSON(f *testing.F) {
	for index, test := range assetJSONBodies {
		f.Add(uint8(index), test.body)
		f.Add(uint8(index), `{}`)
		f.Add(uint8(index), `{"note":"\ud800"}`)
	}
	f.Fuzz(func(t *testing.T, kind uint8, body string) {
		if len(body) > assetBodyLimit+1 {
			t.Skip()
		}
		w := httptest.NewRecorder()
		value, ok := readAssetJSONForTest(assetJSONBodies[int(kind)%len(assetJSONBodies)].kind, w, apiRequest(http.MethodPost, "/api/v1/fixture", body))
		if ok {
			encoded, err := json.Marshal(value)
			if !utf8.ValidString(body) || err != nil || !json.Valid(encoded) || w.Code != 200 {
				t.Fatal("accepted financial JSON did not retain a valid bounded typed proposal")
			}
		} else if w.Code == 200 || !reflect.ValueOf(value).IsZero() {
			t.Fatal("rejected financial JSON released a partial typed proposal")
		}
	})
}
