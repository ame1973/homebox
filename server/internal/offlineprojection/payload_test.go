package offlineprojection

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
)

func payloadFixtures(t testing.TB) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile("testdata/payloads.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures map[string]json.RawMessage
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures) != 22 {
		t.Fatal("the fixture must cover all 22 projection kinds", len(fixtures))
	}
	return fixtures
}

func TestEveryProjectionKindValidatesAgainstTheEmbeddedContract(t *testing.T) {
	for kind, body := range payloadFixtures(t) {
		t.Run(kind, func(t *testing.T) {
			original := bytes.Clone(body)
			if err := ValidatePayload(kind, body); err != nil {
				t.Fatal("valid payload was rejected", err)
			}
			if !bytes.Equal(body, original) {
				t.Fatal("validation changed the frozen bytes")
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(body, &fields); err != nil {
				t.Fatal(err)
			}
			for name := range fields {
				removed := make(map[string]json.RawMessage, len(fields)-1)
				for key, value := range fields {
					if key != name {
						removed[key] = value
					}
				}
				bad, err := json.Marshal(removed)
				if err != nil {
					t.Fatal(err)
				}
				if err := ValidatePayload(kind, bad); !errors.Is(err, ErrInvalidPayload) {
					t.Fatalf("missing required field %q was accepted: %v", name, err)
				}
			}
			fields["raw_ocr"] = json.RawMessage(`"private-not-approved"`)
			bad, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidatePayload(kind, bad); !errors.Is(err, ErrInvalidPayload) || strings.Contains(err.Error(), "private-not-approved") {
				t.Fatal("unknown private data was accepted or disclosed", err)
			}
		})
	}
}

func TestPayloadValidationPreservesExactNumbersAndUnicode(t *testing.T) {
	fixtures := payloadFixtures(t)
	stock := string(fixtures["stock"])
	for _, number := range []string{"0", "1.0", "10e-1", "0e999999999999999999999999", "9007199254740991"} {
		body := []byte(" \n" + strings.Replace(stock, `"quantity_base":0`, `"quantity_base":`+number, 1) + "\t")
		original := bytes.Clone(body)
		if err := ValidatePayload("stock", body); err != nil || !bytes.Equal(body, original) {
			t.Fatal("valid exact number changed or failed", number, err)
		}
	}
	for _, number := range []string{"-1", "1.00000000000000001", "9007199254740992", "1e99999999999999", "0." + strings.Repeat("0", 127)} {
		body := strings.Replace(stock, `"quantity_base":0`, `"quantity_base":`+number, 1)
		if err := ValidatePayload("stock", []byte(body)); !errors.Is(err, ErrInvalidPayload) {
			t.Fatal("unsafe or fractional quantity was accepted", number, err)
		}
	}
	for _, body := range [][]byte{
		[]byte(`{"id":"\ud800","name":"x"}`), []byte(`{"id":"\udc00","name":"x"}`),
		[]byte(`{"id":"a","id":"b","name":"x"}`), []byte(`{"id":"a","\u0069d":"b","name":"x"}`), []byte("{\"id\":\"\xff\",\"name\":\"x\"}"),
		[]byte(`{"id":"a","name":{"nested":"x"}}`), []byte(`null`), []byte(`[]`), []byte(`{} {}`),
	} {
		if err := ValidatePayload("tag", body); !errors.Is(err, ErrInvalidPayload) {
			t.Fatal("invalid or lossy JSON was accepted", err)
		}
	}
	for _, body := range []string{`{"id":"😀","name":"x"}`, `{"id":"\u0000","name":"x"}`, `{"id":"a","name":"\\ud800"}`} {
		if err := ValidatePayload("tag", []byte(body)); err != nil {
			t.Fatal("valid Unicode representation was rejected", err)
		}
	}
	if err := ValidatePayload("not-a-kind", fixtures["tag"]); !errors.Is(err, ErrInvalidPayload) {
		t.Fatal("unknown projection kind was accepted", err)
	}
}

func TestPayloadValidationEnforcesConditionalPrivacyAndFormats(t *testing.T) {
	fixtures := payloadFixtures(t)
	review := string(fixtures["ai_review"])
	for _, state := range []string{"needs_review", "rejected"} {
		body := strings.Replace(review, `"needs_review"`, `"`+state+`"`, 1)
		body = strings.Replace(body, `"effective_value":null`, `"effective_value":"private-raw"`, 1)
		if err := ValidatePayload("ai_review", []byte(body)); !errors.Is(err, ErrInvalidPayload) || strings.Contains(err.Error(), "private-raw") {
			t.Fatal("unreviewed value entered an offline review projection", state, err)
		}
	}
	for _, state := range []string{"accepted", "corrected"} {
		body := strings.Replace(review, `"needs_review"`, `"`+state+`"`, 1)
		body = strings.Replace(body, `"effective_value":null`, `"effective_value":"approved"`, 1)
		if err := ValidatePayload("ai_review", []byte(body)); err != nil {
			t.Fatal("approved review was rejected", state, err)
		}
	}
	cost := string(fixtures["cost"])
	for _, body := range []string{
		strings.Replace(cost, `"2026-11-18T00:00:00Z"`, `"not-a-date"`, 1),
		strings.Replace(cost, `"TWD"`, `"twD"`, 1),
		strings.Replace(cost, `"expense"`, `"unknown"`, 1),
	} {
		if err := ValidatePayload("cost", []byte(body)); !errors.Is(err, ErrInvalidPayload) {
			t.Fatal("invalid schema field was accepted", err)
		}
	}
}

func TestUnsupportedOrAmbiguousPayloadSchemaChangesFailClosed(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"unknown root keyword", func(doc map[string]any) { doc["debug"] = true }},
		{"unsupported root dialect", func(doc map[string]any) { doc["$schema"] = "unknown" }},
		{"additional properties enabled", func(doc map[string]any) {
			doc["$defs"].(map[string]any)["tag"].(map[string]any)["additionalProperties"] = true
		}},
		{"required field removed", func(doc map[string]any) {
			doc["$defs"].(map[string]any)["tag"].(map[string]any)["required"] = []string{"id"}
		}},
		{"case-folded field keyword", func(doc map[string]any) {
			id := doc["$defs"].(map[string]any)["tag"].(map[string]any)["properties"].(map[string]any)["id"].(map[string]any)
			id["MINLENGTH"] = id["minLength"]
		}},
		{"null field constraint", func(doc map[string]any) {
			doc["$defs"].(map[string]any)["tag"].(map[string]any)["properties"].(map[string]any)["id"].(map[string]any)["minLength"] = nil
		}},
		{"unknown nested keyword", func(doc map[string]any) {
			doc["$defs"].(map[string]any)["tag"].(map[string]any)["properties"].(map[string]any)["id"].(map[string]any)["secret"] = true
		}},
		{"unsupported regex", func(doc map[string]any) {
			doc["$defs"].(map[string]any)["tag"].(map[string]any)["properties"].(map[string]any)["id"].(map[string]any)["pattern"] = "(?=x)x"
		}},
		{"unsupported null enum member", func(doc map[string]any) {
			doc["$defs"].(map[string]any)["ai_review"].(map[string]any)["properties"].(map[string]any)["state"].(map[string]any)["enum"] = []any{"needs_review", nil}
		}},
		{"unsupported numeric enum member", func(doc map[string]any) {
			doc["$defs"].(map[string]any)["ai_review"].(map[string]any)["properties"].(map[string]any)["state"].(map[string]any)["enum"] = []any{"needs_review", 1}
		}},
		{"case-folded conditional keyword", func(doc map[string]any) {
			condition := doc["$defs"].(map[string]any)["ai_review"].(map[string]any)["allOf"].([]any)[0].(map[string]any)
			condition["IF"] = condition["if"]
		}},
		{"unsupported nested object", func(doc map[string]any) {
			doc["$defs"].(map[string]any)["tag"].(map[string]any)["properties"].(map[string]any)["id"].(map[string]any)["type"] = "object"
		}},
		{"unsupported format", func(doc map[string]any) {
			doc["$defs"].(map[string]any)["cost"].(map[string]any)["properties"].(map[string]any)["occurred_at"].(map[string]any)["format"] = "date"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var doc map[string]any
			if err := json.Unmarshal(contract, &doc); err != nil {
				t.Fatal(err)
			}
			test.mutate(doc)
			body, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			rules, err := compileContractBytes(body)
			if !errors.Is(err, ErrSchemaUnavailable) || rules != nil {
				t.Fatal("unsupported schema was partially accepted", err)
			}
		})
	}
	duplicate := append([]byte(`{"$schema":"duplicate",`), contract[1:]...)
	for _, body := range [][]byte{nil, []byte(`{`), duplicate, bytes.Repeat([]byte(" "), 4<<20+1)} {
		rules, err := compileContractBytes(body)
		if !errors.Is(err, ErrSchemaUnavailable) || rules != nil {
			t.Fatal("invalid schema was accepted", err)
		}
	}
}

func TestDateTimeFormatChecksRFC3339WithoutChangingStoredText(t *testing.T) {
	for _, value := range []string{
		"2026-11-18T00:00:00Z", "2026-11-18t00:00:00z", "2026-11-18T00:00:00.123456789123Z",
		"0001-01-01T00:00:00+23:59", "9999-12-31T23:59:59-23:59",
	} {
		if !validDateTime(value) {
			t.Fatalf("valid RFC3339 format rejected: %q", value)
		}
	}
	for _, value := range []string{
		"", "2026-11-18T00:00:00Z\n", "0000-01-01T00:00:00Z", "2026-11-18T00:00:60Z",
		"2026-11-18T00:00:00+24:00", "2026-11-18T00:00:00+23:60", "2026-11-18T00:00:00,1Z",
	} {
		if validDateTime(value) {
			t.Fatalf("invalid RFC3339 format accepted: %q", value)
		}
	}
}

func TestProjectionValidatorIsSafeForConcurrentReaders(t *testing.T) {
	fixtures := payloadFixtures(t)
	var workers sync.WaitGroup
	failures := make(chan error, 32)
	for range 32 {
		workers.Go(func() {
			for kind, body := range fixtures {
				if err := ValidatePayload(kind, body); err != nil {
					failures <- err
					return
				}
			}
		})
	}
	workers.Wait()
	close(failures)
	for err := range failures {
		t.Fatal("concurrent validation failed", err)
	}
}

// The Python contract suite supplies independently classified synthetic cases.
func TestProjectionDifferentialFixture(t *testing.T) {
	path := os.Getenv("HOMEBOX_PROJECTION_CASES")
	if path == "" {
		return
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Kind    string          `json:"kind"`
		Payload json.RawMessage `json:"payload"`
		Valid   bool            `json:"valid"`
	}
	if err := json.Unmarshal(body, &cases); err != nil || len(cases) < 1000 {
		t.Fatal("invalid differential fixture", len(cases), err)
	}
	for index, test := range cases {
		err := ValidatePayload(test.Kind, test.Payload)
		if test.Valid && err != nil || !test.Valid && !errors.Is(err, ErrInvalidPayload) {
			t.Fatalf("case %d (%s): expected valid=%v, got %v", index, test.Kind, test.Valid, err)
		}
	}
}

func FuzzProjectionPayloadValidation(f *testing.F) {
	for kind, body := range payloadFixtures(f) {
		f.Add(kind, string(body))
	}
	for _, seed := range []string{`{"id":"a","name":"x"}`, `{"id":"\ud800","name":"x"}`, `{"id":"a","id":"b","name":"x"}`, `null`, "\xff"} {
		f.Add("tag", seed)
	}
	f.Fuzz(func(t *testing.T, kind, body string) {
		if len(kind) > 128 || len(body) > 128<<10 {
			return
		}
		raw := []byte(body)
		original := bytes.Clone(raw)
		for _, target := range []string{kind, "tag"} {
			err := ValidatePayload(target, raw)
			if err != nil && !errors.Is(err, ErrInvalidPayload) {
				t.Fatal("invalid data returned a non-generic error", err)
			}
		}
		if !bytes.Equal(raw, original) {
			t.Fatal("validator changed frozen bytes")
		}
	})
}
