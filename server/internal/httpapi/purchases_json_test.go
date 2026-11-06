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

func purchaseJSONQuoted(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func readPurchaseJSON(w http.ResponseWriter, r *http.Request, mode uint8) bool {
	switch mode % 3 {
	case 0:
		_, ok := readPurchaseCreate(w, r, "request")
		return ok
	case 1:
		_, ok := readPurchasePatch(w, r, "request")
		return ok
	default:
		_, ok := readPurchaseLine(w, r, "request")
		return ok
	}
}
func TestPurchasesJSONPreservesExactPartialNullableAndIndependentIntegerIntent(t *testing.T) {
	created, ok := readPurchaseCreate(httptest.NewRecorder(), apiRequest(http.MethodPost, "/", `{"merchant_name":"  Exact é  ","purchased_at":"2026-11-06T00:00:00Z","currency":"JPY"}`), "request")
	if !ok || created.MerchantName != "  Exact é  " || created.ReceiptNote != "" || created.Currency != "JPY" {
		t.Fatal("header parser repaired text or invented defaults/totals")
	}
	patch, ok := readPurchasePatch(httptest.NewRecorder(), apiRequest(http.MethodPatch, "/", `{"receipt_note":""}`), "request")
	if !ok || patch.MerchantName != nil || patch.PurchasedAt != nil || patch.Currency != nil || patch.ReceiptNote == nil || *patch.ReceiptNote != "" {
		t.Fatal("partial header lost omitted versus supplied empty text")
	}
	patch, ok = readPurchasePatch(httptest.NewRecorder(), apiRequest(http.MethodPatch, "/", `{"merchant_name":"\u0000Exact é\n"}`), "request")
	if !ok || patch.MerchantName == nil || *patch.MerchantName != "\x00Exact é\n" || patch.ReceiptNote != nil {
		t.Fatal("native control/Unicode text was normalized")
	}
	for _, number := range []string{"1", "1.0", "1e3", "1.5e3", "9007199254740991", "9007199254740991.0"} {
		body := `{"item_id":"item","item_revision":` + number + `,"item_unit_id":null,"quantity_base":` + number + `,"paid_minor":` + number + `}`
		value, ok := readPurchaseLine(httptest.NewRecorder(), apiRequest(http.MethodPost, "/", body), "request")
		if !ok || value.ItemRevision < 1 || value.QuantityBase < 1 || value.PaidMinor < 1 || value.ItemUnitID != nil || value.ItemRevision != value.QuantityBase || value.PaidMinor != value.QuantityBase {
			t.Fatal("exact safe integers were rounded/coerced or conflated with price multiplication", number)
		}
	}
	for _, test := range []struct {
		mode uint8
		body string
	}{
		{0, fmt.Sprintf(`{"merchant_name":%q,"purchased_at":"2026-11-06T00:00:00Z","currency":"ZZZ","receipt_note":%s}`, strings.Repeat("😀", 300), purchaseJSONQuoted(t, "\x00"+strings.Repeat("界", 9999)))},
		{1, fmt.Sprintf(`{"receipt_note":%q}`, strings.Repeat("😀", 10000))},
		{2, fmt.Sprintf(`{"item_id":%q,"item_revision":1,"item_unit_id":null,"quantity_base":1500,"paid_minor":-0.0e999,"note":%q}`, strings.Repeat("😀", 128), strings.Repeat("😀", 10000))},
		{2, `{"item_id":"item","item_revision":7,"item_unit_id":"unit","quantity_base":1.0,"paid_minor":0}`},
	} {
		if !readPurchaseJSON(httptest.NewRecorder(), apiRequest(http.MethodPost, "/", test.body), test.mode) {
			t.Fatal("valid exact typed purchase shape rejected", test.mode)
		}
	}
	// Registry, actual instants/UTC years, ownership/tracking and financial basis
	// are tested through authoritative commands/HTTP, not certified by this parser.
}

func TestPurchasesJSONRejectsMissingNullUnknownDuplicateAndUnsafeFinancialInput(t *testing.T) {
	for _, test := range []struct {
		mode   uint8
		body   string
		status int
	}{
		{0, `{}`, 422}, {0, `{"merchant_name":"M","currency":"JPY"}`, 422}, {0, `{"merchant_name":"M","purchased_at":"2026-11-06T00:00:00Z"}`, 422},
		{0, `{"merchant_name":null,"purchased_at":"2026-11-06T00:00:00Z","currency":"JPY"}`, 422},
		{0, `{"merchant_name":"M","purchased_at":"2026-11-06","currency":"JPY"}`, 422},
		{0, `{"merchant_name":"M","purchased_at":"2026-11-06T00:00:00Z","currency":"JPY","paid_minor":100}`, 422},
		{1, `{}`, 422}, {1, `{"receipt_note":null}`, 422}, {1, `{"merchant_name":""}`, 422}, {1, `{"revision":7}`, 422}, {1, `{"Receipt_note":"x"}`, 422},
		{1, `{"receipt_note":"A","receipt_note":"B"}`, 400}, {1, `{"receipt_note":"\ud800"}`, 400}, {1, "{\"receipt_note\":\"\xff\"}", 400},
		{2, `{"item_id":"item","item_revision":0,"item_unit_id":null,"quantity_base":1,"paid_minor":0}`, 422},
		{2, `{"item_id":"item","item_revision":1,"item_unit_id":null,"quantity_base":0,"paid_minor":0}`, 422},
		{2, `{}`, 422}, {2, `{"item_id":"item","item_unit_id":null,"quantity_base":1,"paid_minor":0}`, 422},
		{2, `{"item_id":"item","item_revision":1,"quantity_base":1,"paid_minor":0}`, 422},
		{2, `{"item_id":"item","item_revision":1,"item_unit_id":null,"paid_minor":0}`, 422},
		{2, `{"item_id":"item","item_revision":1,"item_unit_id":null,"quantity_base":1}`, 422},
		{2, `{"item_id":"item","item_revision":1,"item_unit_id":"unit","quantity_base":2,"paid_minor":0}`, 422},
		{2, `{"item_id":"item","item_revision":1,"item_unit_id":"","quantity_base":1,"paid_minor":0}`, 422},
		{2, `{"item_id":"item","item_revision":1,"item_unit_id":null,"quantity_base":1,"paid_minor":0,"note":null}`, 422},
		{2, `{"item_id":"item","item_revision":1,"item_unit_id":null,"quantity_base":1,"paid_minor":0,"currency":"USD"}`, 422},
		{2, `{"item_id":"item","item_revision":1,"item_unit_id":null,"quantity_base":1,"paid_minor":0,"item_id":"other"}`, 400},
		{2, `{"item_id":null,"item_revision":1,"item_unit_id":null,"quantity_base":1,"paid_minor":0}`, 422},
	} {
		w := httptest.NewRecorder()
		w.Header().Set("X-Request-ID", "request")
		if readPurchaseJSON(w, apiRequest(http.MethodPost, "/", test.body), test.mode) || w.Code != test.status {
			t.Fatal("invalid purchase JSON reached a command", test.mode, w.Code, test.body)
		}
	}
	for mode := uint8(0); mode < 3; mode++ {
		for _, body := range []string{`null`, `[]`, `true`, `{"unknown":1}`, `{} {}`, `{"note":"\ud800"}`} {
			if readPurchaseJSON(httptest.NewRecorder(), apiRequest(http.MethodPost, "/", body), mode) {
				t.Fatal("invalid JSON root/Unicode/unknown property bypassed parser", mode)
			}
		}
	}
	for _, field := range []string{"item_revision", "quantity_base", "paid_minor"} {
		for _, number := range []string{"null", "true", "\"1\"", "-1", "9007199254740992", "9007199254740990.9", "1e-1", "1e9999"} {
			values := map[string]string{"item_revision": "1", "quantity_base": "1", "paid_minor": "0"}
			values[field] = number
			body := fmt.Sprintf(`{"item_id":"item","item_revision":%s,"item_unit_id":null,"quantity_base":%s,"paid_minor":%s}`, values["item_revision"], values["quantity_base"], values["paid_minor"])
			if readPurchaseJSON(httptest.NewRecorder(), apiRequest(http.MethodPost, "/", body), 2) {
				t.Fatal("unsafe/fractional/string value was rounded into a financial command", field, number)
			}
		}
	}
	for _, test := range []struct {
		mode uint8
		body string
	}{
		{0, fmt.Sprintf(`{"merchant_name":%s,"purchased_at":"2026-11-06T00:00:00Z","currency":"JPY"}`, purchaseJSONQuoted(t, "\x00"+strings.Repeat("界", 300)))},
		{1, fmt.Sprintf(`{"receipt_note":%s}`, purchaseJSONQuoted(t, "\x00"+strings.Repeat("😀", 10000)))},
		{2, fmt.Sprintf(`{"item_id":%q,"item_revision":1,"item_unit_id":null,"quantity_base":1,"paid_minor":0}`, strings.Repeat("界", 129))},
		{2, fmt.Sprintf(`{"item_id":"item","item_revision":1,"item_unit_id":%q,"quantity_base":1,"paid_minor":0}`, strings.Repeat("界", 129))},
	} {
		if readPurchaseJSON(httptest.NewRecorder(), apiRequest(http.MethodPost, "/", test.body), test.mode) {
			t.Fatal("width was truncated at UTF/native NUL", test.mode)
		}
	}
}

func TestPurchasesJSONMediaChunkedRawBudgetsAndReadFailuresStayBounded(t *testing.T) {
	bodies := []string{`{"merchant_name":"M","purchased_at":"2026-11-06T00:00:00Z","currency":"JPY"}`, `{"receipt_note":""}`, `{"item_id":"item","item_revision":1,"item_unit_id":null,"quantity_base":1,"paid_minor":0}`}
	for mode, body := range bodies {
		for _, mutate := range []func(*http.Request){func(r *http.Request) { r.Header.Del("Content-Type") }, func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, func(r *http.Request) { r.Header.Set("Content-Type", "application/json; charset=latin1") }, func(r *http.Request) { r.Header.Add("Content-Type", "application/json") }, func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") }} {
			r := apiRequest(http.MethodPost, "/", body)
			mutate(r)
			w := httptest.NewRecorder()
			w.Header().Set("X-Request-ID", "request")
			if readPurchaseJSON(w, r, uint8(mode)) {
				t.Fatal("encoded/ambiguous purchase body admitted")
			}
			expectError(t, w, 415, "UNSUPPORTED_MEDIA_TYPE")
		}
		for _, extra := range []int{0, 1} {
			r := apiRequest(http.MethodPost, "/", body+strings.Repeat(" ", purchaseBodyLimit-len(body)+extra))
			r.ContentLength = -1
			w := httptest.NewRecorder()
			ok := readPurchaseJSON(w, r, uint8(mode))
			if extra == 0 && !ok || extra == 1 && (ok || w.Code != 413) {
				t.Fatal("purchase raw-byte budget changed", mode, w.Code)
			}
		}
		r := apiRequest(http.MethodPost, "/", body)
		r.Body = io.NopCloser(brokenReader{})
		w := httptest.NewRecorder()
		if readPurchaseJSON(w, r, uint8(mode)) || strings.Contains(w.Body.String(), "private transport") {
			t.Fatal("failed body read returned a value/private diagnostic")
		}
	}
}

func FuzzPurchasesJSON(f *testing.F) {
	for _, body := range []string{`{}`, `null`, `{"merchant_name":"M","purchased_at":"2026-11-06T00:00:00Z","currency":"JPY"}`, `{"receipt_note":""}`, `{"receipt_note":"\ud800"}`, `{"item_id":"item","item_revision":1,"item_unit_id":null,"quantity_base":1.5e3,"paid_minor":0}`, `{"item_id":"item","item_revision":9007199254740990.9,"item_unit_id":null,"quantity_base":1,"paid_minor":0}`} {
		for mode := uint8(0); mode < 3; mode++ {
			f.Add(body, mode)
		}
	}
	f.Fuzz(func(t *testing.T, body string, mode uint8) {
		if len(body) > purchaseBodyLimit+1 {
			t.Skip()
		}
		w := httptest.NewRecorder()
		w.Header().Set("X-Request-ID", "request")
		_ = readPurchaseJSON(w, apiRequest(http.MethodPost, "/", body), mode)
		if w.Code < 200 || w.Code >= 500 {
			t.Fatal("purchase JSON parser produced an internal failure")
		}
	})
}
