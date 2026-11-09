package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"homebox/internal/store"
)

const purchaseCorrectionReplacement = `{"item_id":"item","item_revision":2,"item_unit_id":null,"quantity_base":1500,"paid_minor":0}`
const disposalCorrectionReplacement = `{"kind":"sold","disposed_at":"2026-11-07T12:00:00.123456789+08:00","realized_proceeds_minor":0,"currency":"JPY"}`

var correctionJSONBodies = []struct {
	kind, body, replacement string
	required, nested        []string
}{
	{"purchase", `{"line_id":"line","line_revision":3,"item_revision":4,"action":"replace","replacement":` + purchaseCorrectionReplacement + `,"reason":"Exact reason"}`, purchaseCorrectionReplacement,
		[]string{"line_id", "line_revision", "item_revision", "action", "replacement", "reason"}, []string{"item_id", "item_revision", "item_unit_id", "quantity_base", "paid_minor"}},
	{"disposal", `{"asset_revision":7,"action":"replace","replacement":` + disposalCorrectionReplacement + `,"reason":"Exact reason"}`, disposalCorrectionReplacement,
		[]string{"asset_revision", "action", "replacement", "reason"}, []string{"kind", "disposed_at", "realized_proceeds_minor", "currency"}},
}

func readFinancialCorrectionJSONForTest(kind string, w http.ResponseWriter, r *http.Request) (any, bool) {
	w.Header().Set("X-Request-ID", "request")
	if kind == "purchase" {
		return readPurchaseLineCorrection(w, r, "request")
	}
	return readDisposalCorrection(w, r, "request")
}

func TestFinancialCorrectionsJSONRequiresEveryOriginalProofAndExplicitNullableReplacement(t *testing.T) {
	for _, test := range correctionJSONBodies {
		t.Run(test.kind, func(t *testing.T) {
			if _, ok := readFinancialCorrectionJSONForTest(test.kind, httptest.NewRecorder(), apiRequest(http.MethodPost, "/", test.body)); !ok {
				t.Fatal("complete actual correction was rejected")
			}
			for _, field := range test.required {
				w := httptest.NewRecorder()
				value, ok := readFinancialCorrectionJSONForTest(test.kind, w, apiRequest(http.MethodPost, "/", assetJSONMutation(t, test.body, field, "")))
				if ok || !reflect.ValueOf(value).IsZero() {
					t.Fatal("missing original proof/nullable decision became a default", field)
				}
				expectError(t, w, 422, "VALIDATION_ERROR")
			}
			for _, field := range test.nested {
				nested := assetJSONMutation(t, test.replacement, field, "")
				body := assetJSONMutation(t, test.body, "replacement", nested)
				w := httptest.NewRecorder()
				value, ok := readFinancialCorrectionJSONForTest(test.kind, w, apiRequest(http.MethodPost, "/", body))
				if ok || !reflect.ValueOf(value).IsZero() {
					t.Fatal("missing nested Item/Unit/amount/date decision became a default", field)
				}
				expectError(t, w, 422, "VALIDATION_ERROR")
			}
			void := assetJSONMutation(t, assetJSONMutation(t, test.body, "action", `"void"`), "replacement", "null")
			if _, ok := readFinancialCorrectionJSONForTest(test.kind, httptest.NewRecorder(), apiRequest(http.MethodPost, "/", void)); !ok {
				t.Fatal("explicit financial void was confused with a zero-valued replacement")
			}
			for _, body := range []string{
				assetJSONMutation(t, test.body, "replacement", "null"),
				assetJSONMutation(t, test.body, "action", `"void"`),
				assetJSONMutation(t, test.body, "action", `"delete"`),
				assetJSONMutation(t, test.body, "replacement", "[]"),
				assetJSONMutation(t, void, "replacement", "0"),
			} {
				w := httptest.NewRecorder()
				value, ok := readFinancialCorrectionJSONForTest(test.kind, w, apiRequest(http.MethodPost, "/", body))
				if ok || !reflect.ValueOf(value).IsZero() {
					t.Fatal("wrong discriminant/nullable decision returned a financial command", body)
				}
				expectError(t, w, 422, "VALIDATION_ERROR")
			}
		})
	}
}

func TestFinancialCorrectionsJSONPreservesWholeReplacementAndExactSafeIntegers(t *testing.T) {
	purchase := `{"line_id":"line","line_revision":3.0,"item_revision":4e0,"action":"replace","replacement":{"item_id":"item","item_revision":2.0,"item_unit_id":null,"quantity_base":1.5e3,"paid_minor":-0.0e999,"note":"\u0000Exact é\n"},"reason":"\u0000Reviewed é\n"}`
	value, ok := readPurchaseLineCorrection(httptest.NewRecorder(), apiRequest(http.MethodPost, "/", purchase), "request")
	if !ok || value.LineRevision != 3 || value.ItemRevision != 4 || value.Reason != "\x00Reviewed é\n" || value.Replacement == nil || value.Replacement.ItemRevision != 2 || value.Replacement.ItemUnitID != nil || value.Replacement.QuantityBase != 1500 || value.Replacement.PaidMinor != 0 || value.Replacement.Note != "\x00Exact é\n" {
		t.Fatal("nested original/replacement proofs, null, exact integer or Unicode changed", value)
	}
	disposal := `{"asset_revision":7.0,"action":"replace","replacement":{"kind":"donated","disposed_at":"2026-11-07T12:00:00.123456789+08:00","realized_proceeds_minor":-0.0e999,"currency":"JPY","note":"\u0000Exact é\n"},"reason":"\u0000Reviewed é\n"}`
	financial, ok := readDisposalCorrection(httptest.NewRecorder(), apiRequest(http.MethodPost, "/", disposal), "request")
	if !ok || financial.AssetRevision != 7 || financial.Replacement == nil || financial.Replacement.RealizedProceedsMinor != 0 || financial.Replacement.DisposedAt != "2026-11-07T12:00:00.123456789+08:00" || financial.Replacement.Note != "\x00Exact é\n" || financial.Reason != "\x00Reviewed é\n" {
		t.Fatal("disposal finance lost explicit zero, source proof, Unicode or nanoseconds", financial)
	}
	for _, test := range []struct {
		kind, outer, nested string
		invalid             []string
	}{
		{"purchase", "line_revision", "", []string{"0", "-1", "null", "true", `"1"`, "1.1", "9007199254740992", "9007199254740990.9999999999"}},
		{"purchase", "item_revision", "", []string{"0", "-1", "null", "true", `"1"`, "1.1", "9007199254740992"}},
		{"purchase", "replacement", "item_revision", []string{"0", "-1", "null", "true", `"1"`, "1.1", "9007199254740992"}},
		{"purchase", "replacement", "quantity_base", []string{"0", "-1", "null", "true", `"1"`, "1.1", "9007199254740992"}},
		{"purchase", "replacement", "paid_minor", []string{"-1", "null", "true", `"0"`, "0.1", "9007199254740992"}},
		{"disposal", "asset_revision", "", []string{"0", "-1", "null", "true", `"1"`, "1.1", "9007199254740992"}},
		{"disposal", "replacement", "realized_proceeds_minor", []string{"-1", "null", "true", `"0"`, "0.1", "9007199254740992"}},
	} {
		base := correctionJSONBodies[0]
		if test.kind == "disposal" {
			base = correctionJSONBodies[1]
		}
		for _, raw := range test.invalid {
			body := assetJSONMutation(t, base.body, test.outer, raw)
			if test.nested != "" {
				body = assetJSONMutation(t, base.body, test.outer, assetJSONMutation(t, base.replacement, test.nested, raw))
			}
			w := httptest.NewRecorder()
			result, ok := readFinancialCorrectionJSONForTest(test.kind, w, apiRequest(http.MethodPost, "/", body))
			if ok || !reflect.ValueOf(result).IsZero() {
				t.Fatal("unsafe/fractional/string/boolean value became a financial proof or amount", test.outer, test.nested, raw)
			}
			expectError(t, w, 422, "VALIDATION_ERROR")
		}
	}
	for _, kind := range []string{"donated", "recycled", "discarded", "other"} {
		input := assetJSONMutation(t, disposalCorrectionReplacement, "kind", purchaseJSONQuoted(t, kind))
		body := assetJSONMutation(t, correctionJSONBodies[1].body, "replacement", input)
		if _, ok := readFinancialCorrectionJSONForTest("disposal", httptest.NewRecorder(), apiRequest(http.MethodPost, "/", body)); !ok {
			t.Fatal("explicit non-sale zero was rejected", kind)
		}
		input = assetJSONMutation(t, input, "realized_proceeds_minor", "1")
		if _, ok := readFinancialCorrectionJSONForTest("disposal", httptest.NewRecorder(), apiRequest(http.MethodPost, "/", assetJSONMutation(t, body, "replacement", input))); ok {
			t.Fatal("non-sale financial replacement accepted proceeds", kind)
		}
	}
	input := assetJSONMutation(t, purchaseCorrectionReplacement, "item_unit_id", `"unit"`)
	if _, ok := readFinancialCorrectionJSONForTest("purchase", httptest.NewRecorder(), apiRequest(http.MethodPost, "/", assetJSONMutation(t, correctionJSONBodies[0].body, "replacement", input))); ok {
		t.Fatal("named Unit accepted bulk quantity")
	}
	input = assetJSONMutation(t, input, "quantity_base", "1")
	if result, ok := readFinancialCorrectionJSONForTest("purchase", httptest.NewRecorder(), apiRequest(http.MethodPost, "/", assetJSONMutation(t, correctionJSONBodies[0].body, "replacement", input))); !ok || result.(store.PurchaseLineCorrectionCreate).Replacement.ItemUnitID == nil {
		t.Fatal("explicit one-Unit decision was not retained")
	}
}

func TestFinancialCorrectionsJSONRejectsNestedDuplicatesUnknownFieldsAndMalformedUnicode(t *testing.T) {
	for _, test := range correctionJSONBodies {
		for _, pair := range []struct {
			body   string
			status int
		}{
			{test.body + `{}`, 400}, {`null`, 422}, {`[]`, 422},
			{test.body[:len(test.body)-1] + `,"action":"void"}`, 400},
			{test.body[:len(test.body)-1] + `,"unexpected":1}`, 422},
			{assetJSONMutation(t, test.body, "replacement", test.replacement[:len(test.replacement)-1]+`,"unexpected":1}`), 422},
			{assetJSONMutation(t, test.body, "replacement", test.replacement[:len(test.replacement)-1]+`,"note":"A","note":"B"}`), 400},
			{assetJSONMutation(t, test.body, "replacement", test.replacement[:len(test.replacement)-1]+`,"note":"\ud800"}`), 400},
			{assetJSONMutation(t, test.body, "reason", `"\udc00"`), 400},
			{test.body[:len(test.body)-1] + ",\"reason\":\"" + string([]byte{0xff}) + "\"}", 400},
		} {
			w := httptest.NewRecorder()
			value, ok := readFinancialCorrectionJSONForTest(test.kind, w, apiRequest(http.MethodPost, "/", pair.body))
			if ok || !reflect.ValueOf(value).IsZero() || w.Code != pair.status {
				t.Fatal("invalid outer/nested JSON entered the correction command", test.kind, pair.status, w.Code, pair.body)
			}
		}
		for _, text := range []string{"", "\x00  Exact é\n ", strings.Repeat("😀", 10000)} {
			raw, _ := json.Marshal(text)
			body := assetJSONMutation(t, test.body, "replacement", assetJSONMutation(t, test.replacement, "note", string(raw)))
			value, ok := readFinancialCorrectionJSONForTest(test.kind, httptest.NewRecorder(), apiRequest(http.MethodPost, "/", body))
			encoded, _ := json.Marshal(value)
			var fields struct {
				Replacement struct {
					Note string `json:"note"`
				} `json:"replacement"`
			}
			_ = json.Unmarshal(encoded, &fields)
			if !ok || fields.Replacement.Note != text {
				t.Fatal("valid exact nested note was truncated, normalized or defaulted", test.kind)
			}
		}
		for _, field := range []string{"reason", "note"} {
			raw := purchaseJSONQuoted(t, "\x00"+strings.Repeat("界", 10000))
			body := assetJSONMutation(t, test.body, field, raw)
			if field == "note" {
				body = assetJSONMutation(t, test.body, "replacement", assetJSONMutation(t, test.replacement, field, raw))
			}
			if _, ok := readFinancialCorrectionJSONForTest(test.kind, httptest.NewRecorder(), apiRequest(http.MethodPost, "/", body)); ok {
				t.Fatal("nested/outer text bound stopped at native NUL", test.kind, field)
			}
		}
	}
}

func TestFinancialCorrectionsJSONMediaChunkedBudgetsAndReadFailuresRemainBounded(t *testing.T) {
	for _, test := range correctionJSONBodies {
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
			value, ok := readFinancialCorrectionJSONForTest(test.kind, w, r)
			if ok || !reflect.ValueOf(value).IsZero() {
				t.Fatal("encoded/ambiguous correction body was accepted")
			}
			expectError(t, w, 415, "UNSUPPORTED_MEDIA_TYPE")
		}
		for _, extra := range []int{0, 1} {
			r := apiRequest(http.MethodPost, "/", test.body+strings.Repeat(" ", assetBodyLimit-len(test.body)+extra))
			r.ContentLength = -1
			w := httptest.NewRecorder()
			value, ok := readFinancialCorrectionJSONForTest(test.kind, w, r)
			if extra == 0 && !ok || extra == 1 && (ok || !reflect.ValueOf(value).IsZero() || w.Code != 413) {
				t.Fatal("chunked financial correction changed the 128-KiB raw-byte budget", test.kind, extra, w.Code)
			}
		}
		r := apiRequest(http.MethodPost, "/", test.body)
		r.Body = io.NopCloser(brokenReader{})
		w := httptest.NewRecorder()
		value, ok := readFinancialCorrectionJSONForTest(test.kind, w, r)
		if ok || !reflect.ValueOf(value).IsZero() || strings.Contains(w.Body.String(), "private transport") {
			t.Fatal("failed financial body read exposed partial command or transport details")
		}
		expectError(t, w, 400, "INVALID_BODY")
	}
}

func FuzzFinancialCorrectionsJSON(f *testing.F) {
	for _, body := range []string{`{}`, `null`, correctionJSONBodies[0].body, correctionJSONBodies[1].body,
		`{"asset_revision":1,"action":"void","replacement":null,"reason":"Reviewed"}`,
		`{"asset_revision":1,"action":"replace","replacement":{"kind":"sold","kind":"other"},"reason":"Reviewed"}`,
		fmt.Sprintf(`{"line_id":"line","line_revision":1,"item_revision":1,"action":"replace","replacement":%s,"reason":"\ud800"}`, purchaseCorrectionReplacement)} {
		f.Add(body, false)
		f.Add(body, true)
	}
	f.Fuzz(func(t *testing.T, body string, disposal bool) {
		if len(body) > assetBodyLimit+1 {
			t.Skip()
		}
		kind := "purchase"
		if disposal {
			kind = "disposal"
		}
		w := httptest.NewRecorder()
		_, _ = readFinancialCorrectionJSONForTest(kind, w, apiRequest(http.MethodPost, "/", body))
		if w.Code < 200 || w.Code >= 500 {
			t.Fatal("financial correction parser produced an internal failure")
		}
	})
}
