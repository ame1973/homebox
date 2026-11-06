package httpapi

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func readHoldingJSON(w http.ResponseWriter, r *http.Request, mode uint8) bool {
	switch mode % 8 {
	case 0:
		_, ok := readUnitCreate(w, r, "request")
		return ok
	case 1:
		_, ok := readUnitPatch(w, r, "request")
		return ok
	case 2:
		_, ok := readUnitMove(w, r, "request")
		return ok
	case 3:
		_, ok := readUnitStatus(w, r, "request")
		return ok
	default:
		_, ok := readStockInput(w, r, "request", []string{"receive", "consume", "transfer", "adjust"}[mode%8-4])
		return ok
	}
}

func TestHoldingsJSONPreservesRequiredNullableOmittedAndExactIntegralFields(t *testing.T) {
	created, ok := readUnitCreate(httptest.NewRecorder(), apiRequest(http.MethodPost, "/", `{"location_id":null}`), "request")
	if !ok || created.LocationID != nil || created.SerialNumber != nil || created.Status != nil || created.Notes != "" {
		t.Fatal("JSON reader installed defaults or lost explicit unknown location")
	}
	patch, ok := readUnitPatch(httptest.NewRecorder(), apiRequest(http.MethodPatch, "/", `{"serial_number":null}`), "request")
	if !ok || !patch.SerialNumber.Set || patch.SerialNumber.Value != nil || patch.Notes != nil {
		t.Fatal("serial clear lost omission/null presence")
	}
	patch, ok = readUnitPatch(httptest.NewRecorder(), apiRequest(http.MethodPatch, "/", `{"serial_number":"","notes":"\u0000Exact é\n"}`), "request")
	if !ok || patch.SerialNumber.Value == nil || *patch.SerialNumber.Value != "" || patch.Notes == nil || *patch.Notes != "\x00Exact é\n" {
		t.Fatal("empty/control/Unicode Unit text was repaired")
	}
	for _, number := range []string{"1", "1.0", "1e3", "1.5e3", "9007199254740991", "9007199254740991.0"} {
		value, ok := readStockInput(httptest.NewRecorder(), apiRequest(http.MethodPost, "/", `{"item_id":"item","target_location_id":"target","quantity_base":`+number+`}`), "request", "receive")
		if !ok || value.quantity < 1 || value.quantity > maxJSONInteger {
			t.Fatal("exact safe integral JSON representation was rounded/rejected", number)
		}
	}
	for _, test := range []struct {
		mode uint8
		body string
	}{
		{0, fmt.Sprintf(`{"location_id":%q,"serial_number":%q,"notes":%q,"status":"lost"}`, strings.Repeat("界", 128), strings.Repeat("😀", 10000), strings.Repeat("😀", 10000))},
		{1, fmt.Sprintf(`{"notes":%q}`, strings.Repeat("😀", 10000))},
		{2, fmt.Sprintf(`{"target_location_id":%q}`, strings.Repeat("界", 128))},
		{3, `{"status":"disposed","disposal_kind":"discarded","note":""}`},
		{3, `{"status":"retired"}`},
		{4, `{"item_id":"item","target_location_id":"target","quantity_base":1,"note":"\u0000Exact"}`},
		{5, `{"item_id":"item","source_location_id":"source","quantity_base":1}`},
		{6, `{"item_id":"item","source_location_id":"same","target_location_id":"same","quantity_base":1}`},
		{7, fmt.Sprintf(`{"item_id":"item","target_location_id":"target","quantity_base":-0.0e999,"reason":%q}`, strings.Repeat("😀", 10000))},
	} {
		if !readHoldingJSON(httptest.NewRecorder(), apiRequest(http.MethodPost, "/", test.body), test.mode) {
			t.Fatal("valid typed holding shape was rejected", test.mode)
		}
	}
	// Source sufficiency, same-place transfer, active paths and asset-backed
	// lifecycle are authoritative runtime rules, not certified by this JSON test.
}

func TestHoldingsJSONRejectsUnknownDuplicateNullWidthLifecycleAndUnsafeNumberInputs(t *testing.T) {
	for _, test := range []struct {
		mode uint8
		body string
		code int
	}{
		{0, `{}`, 422}, {0, `{"serial_number":null}`, 422}, {0, `{"location_id":""}`, 422}, {0, `{"location_id":null,"notes":null}`, 422},
		{0, `{"location_id":null,"asset_code":"caller"}`, 422}, {0, `{"location_id":null,"item_id":"other"}`, 422},
		{0, `{"location_id":null,"status":"retired"}`, 422}, {0, `{"location_id":null,"status":"disposed"}`, 422}, {0, `{"Location_id":null}`, 422},
		{1, `{}`, 422}, {1, `{"notes":null}`, 422}, {1, `{"serial_number":123}`, 422}, {1, `{"location_id":"target"}`, 422}, {1, `{"status":"stored"}`, 422},
		{2, `{}`, 422}, {2, `{"target_location_id":null}`, 422}, {2, `{"target_location_id":"target","note":"uncontracted"}`, 422},
		{3, `{}`, 422}, {3, `{"status":"unknown"}`, 422}, {3, `{"status":"stored","note":null}`, 422},
		{3, `{"status":"disposed"}`, 422}, {3, `{"status":"disposed","disposal_kind":null}`, 422}, {3, `{"status":"disposed","disposal_kind":"other"}`, 422},
		{3, `{"status":"stored","disposal_kind":"discarded"}`, 422}, {3, `{"status":"disposed","disposal_kind":"sold","currency":"TWD"}`, 422},
		{4, `{"item_id":"item","target_location_id":"target"}`, 422}, {4, `{"item_id":null,"target_location_id":"target","quantity_base":1}`, 422},
		{4, `{"item_id":"item","target_location_id":null,"quantity_base":1}`, 422}, {4, `{"item_id":"item","target_location_id":"target","quantity_base":0}`, 422},
		{4, `{"item_id":"item","target_location_id":"target","quantity_base":"1"}`, 422}, {4, `{"item_id":"item","target_location_id":"target","quantity_base":1,"scale_factor":1000}`, 422},
		{5, `{"item_id":"item","quantity_base":1}`, 422}, {5, `{"item_id":"item","source_location_id":"source","target_location_id":"target","quantity_base":1}`, 422},
		{6, `{"item_id":"item","source_location_id":"source","quantity_base":1}`, 422},
		{7, `{"item_id":"item","target_location_id":"target","quantity_base":0}`, 422}, {7, `{"item_id":"item","target_location_id":"target","quantity_base":0,"reason":""}`, 422},
		{1, `{"notes":"A","notes":"B"}`, 400}, {0, `{"location_id":null,"location_id":null}`, 400}, {3, `{"status":"lost","note":"\ud800"}`, 400},
		{4, `{"item_id":"\udc00","target_location_id":"target","quantity_base":1}`, 400}, {1, "{\"notes\":\"\xff\"}", 400},
	} {
		w := httptest.NewRecorder()
		w.Header().Set("X-Request-ID", "request")
		if readHoldingJSON(w, apiRequest(http.MethodPost, "/", test.body), test.mode) || w.Code != test.code {
			t.Fatal("invalid holding JSON reached a domain command", test.mode, w.Code, test.body)
		}
	}
	for mode := uint8(0); mode < 8; mode++ {
		for _, body := range []string{`null`, `[]`, `true`, `{"unknown":1}`, `{"unknown":1,"unknown":2}`, `{} {}`, `{"note":"\ud800"}`} {
			if readHoldingJSON(httptest.NewRecorder(), apiRequest(http.MethodPost, "/", body), mode) {
				t.Fatal("invalid root/unknown/Unicode bypassed a holding parser", mode)
			}
		}
	}
	for _, number := range []string{"null", "true", "-1", "9007199254740992", "9007199254740990.9", "1e-1", "1e9999"} {
		if readHoldingJSON(httptest.NewRecorder(), apiRequest(http.MethodPost, "/", `{"item_id":"item","target_location_id":"target","quantity_base":`+number+`}`), 4) {
			t.Fatal("unsafe/fractional quantity was rounded into stock", number)
		}
	}
	for _, test := range []struct {
		mode uint8
		body string
	}{
		{0, fmt.Sprintf(`{"location_id":%q}`, strings.Repeat("界", 129))}, {0, fmt.Sprintf(`{"location_id":null,"serial_number":%q}`, strings.Repeat("😀", 10001))},
		{1, fmt.Sprintf(`{"notes":%q}`, "\x00"+strings.Repeat("界", 10000))}, {3, fmt.Sprintf(`{"status":"stored","note":%q}`, strings.Repeat("界", 10001))},
		{4, fmt.Sprintf(`{"item_id":%q,"target_location_id":"target","quantity_base":1}`, strings.Repeat("界", 129))},
		{7, fmt.Sprintf(`{"item_id":"item","target_location_id":"target","quantity_base":0,"reason":%q}`, "\x00"+strings.Repeat("界", 10000))},
	} {
		if readHoldingJSON(httptest.NewRecorder(), apiRequest(http.MethodPost, "/", test.body), test.mode) {
			t.Fatal("Unicode/native-NUL width bound was truncated", test.mode)
		}
	}
}

func TestHoldingsJSONMediaChunkedLimitsAndReadErrorsRemainExactAndSafe(t *testing.T) {
	bodies := []string{`{"location_id":null}`, `{"notes":""}`, `{"target_location_id":"target"}`, `{"status":"stored"}`,
		`{"item_id":"item","target_location_id":"target","quantity_base":1}`, `{"item_id":"item","source_location_id":"source","quantity_base":1}`,
		`{"item_id":"item","source_location_id":"source","target_location_id":"target","quantity_base":1}`, `{"item_id":"item","target_location_id":"target","quantity_base":0,"reason":"Counted"}`}
	for mode, body := range bodies {
		for _, mutate := range []func(*http.Request){func(r *http.Request) { r.Header.Del("Content-Type") }, func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, func(r *http.Request) { r.Header.Set("Content-Type", "application/json; charset=latin1") }, func(r *http.Request) { r.Header.Add("Content-Type", "application/json") }, func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") }} {
			r := apiRequest(http.MethodPost, "/", body)
			mutate(r)
			w := httptest.NewRecorder()
			w.Header().Set("X-Request-ID", "request")
			if readHoldingJSON(w, r, uint8(mode)) {
				t.Fatal("encoded/ambiguous holding body was accepted")
			}
			expectError(t, w, 415, "UNSUPPORTED_MEDIA_TYPE")
		}
		limit := holdingBodyLimit
		if mode == 2 {
			limit = unitMoveBodyLimit
		}
		for _, extra := range []int{0, 1} {
			r := apiRequest(http.MethodPost, "/", body+strings.Repeat(" ", limit-len(body)+extra))
			r.ContentLength = -1
			w := httptest.NewRecorder()
			ok := readHoldingJSON(w, r, uint8(mode))
			if extra == 0 && !ok || extra == 1 && (ok || w.Code != 413) {
				t.Fatal("holding parser changed its exact raw-byte budget", mode, w.Code)
			}
		}
		r := apiRequest(http.MethodPost, "/", body)
		r.Body = io.NopCloser(brokenReader{})
		w := httptest.NewRecorder()
		if readHoldingJSON(w, r, uint8(mode)) || strings.Contains(w.Body.String(), "private transport") {
			t.Fatal("failed holding transport produced success or leaked diagnostics")
		}
	}
}

func FuzzHoldingsJSON(f *testing.F) {
	for _, body := range []string{`{}`, `null`, `{"location_id":null}`, `{"serial_number":"\ud800"}`, `{"target_location_id":"target"}`, `{"status":"disposed","disposal_kind":"sold"}`, `{"item_id":"item","target_location_id":"target","quantity_base":1.5e3}`, `{"item_id":"item","target_location_id":"target","quantity_base":9007199254740990.9}`, `{"item_id":"item","target_location_id":"target","quantity_base":0,"reason":"Counted"}`} {
		for mode := uint8(0); mode < 8; mode++ {
			f.Add(body, mode)
		}
	}
	f.Fuzz(func(t *testing.T, body string, mode uint8) {
		if len(body) > holdingBodyLimit+1 {
			t.Skip()
		}
		w := httptest.NewRecorder()
		w.Header().Set("X-Request-ID", "request")
		_ = readHoldingJSON(w, apiRequest(http.MethodPost, "/", body), mode)
		if w.Code < 200 || w.Code >= 500 {
			t.Fatal("holding JSON boundary produced an internal failure")
		}
	})
}
