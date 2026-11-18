package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestSyncRecordValidationBindsEveryPayloadIdentity(t *testing.T) {
	data, err := os.ReadFile("../offlineprojection/testdata/payloads.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures map[string]json.RawMessage
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		kind, id, identityField string
	}{
		{"location", "loc-1", "id"}, {"item", "item-1", "id"}, {"unit", "unit-1", "id"},
		{"stock", projectionPair("item-1", "loc-1"), "location_id"}, {"barcode", "barcode-1", "id"},
		{"media", "media-1", "id"}, {"media_link", "link-1", "id"}, {"category", "category-1", "id"},
		{"tag", "tag-1", "id"}, {"item_tag", projectionPair("item-1", "tag-1"), "tag_id"},
		{"search_doc", projectionPair("item", "item-1"), "entity_id"}, {"purchase", "purchase-1", "id"},
		{"purchase_line", "line-1", "id"}, {"asset", "unit-1", "item_unit_id"},
		{"usage", "usage-1", "id"}, {"cost", "cost-1", "id"}, {"disposal", "unit-1", "item_unit_id"},
		{"alias", "alias-1", "id"}, {"attribute", "attribute-1", "id"}, {"location_event", "location-event-1", "id"},
		{"inventory_event", "inventory-event-1", "id"}, {"ai_review", "review-1", "id"},
	}
	if len(fixtures) != len(tests) {
		t.Fatal("identity fixtures do not cover all projection kinds")
	}
	for _, test := range tests {
		t.Run(test.kind, func(t *testing.T) {
			payload, found := fixtures[test.kind]
			if !found {
				t.Fatal("missing fixture")
			}
			original := bytes.Clone(payload)
			if err := validateSyncRecord(test.kind, test.id, "upsert", 1, 1, payload); err != nil || !bytes.Equal(original, payload) {
				t.Fatal("valid frozen upsert rejected or changed", err)
			}
			if err := validateSyncRecord(test.kind, test.id, "delete", maxSafeInteger, 1, nil); err != nil {
				t.Fatal("valid null tombstone rejected", err)
			}
			var fields map[string]any
			if err := json.Unmarshal(payload, &fields); err != nil {
				t.Fatal(err)
			}
			fields[test.identityField] = "different-identity"
			changed, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateSyncRecord(test.kind, test.id, "upsert", 1, 1, changed); !errors.Is(err, ErrDomainState) {
				t.Fatal("mismatched payload identity was accepted", err)
			}
			for _, input := range []struct {
				operation string
				revision  int64
				version   int64
				payload   []byte
			}{
				{"upsert", 0, 1, payload}, {"upsert", maxSafeInteger + 1, 1, payload},
				{"upsert", 1, 0, payload}, {"upsert", 1, 2, payload},
				{"upsert", 1, 1, nil}, {"upsert", 1, 1, []byte(`null`)},
				{"delete", 1, 1, []byte{}}, {"delete", 1, 1, []byte(`null`)},
				{"delete", 1, 1, payload}, {"other", 1, 1, payload},
			} {
				if err := validateSyncRecord(test.kind, test.id, input.operation, input.revision, input.version, input.payload); !errors.Is(err, ErrDomainState) {
					t.Fatal("invalid record metadata or payload was accepted", err)
				}
			}
		})
	}
}

func TestSyncRecordValidationUsesCanonicalExactIdentityAndGenericErrors(t *testing.T) {
	const item = "i\x00<😀"
	const place = "位置\n"
	payload, err := json.Marshal(map[string]any{"item_id": item, "location_id": place, "quantity_base": 0, "unit_code": "pcs", "base_unit": "pcs", "scale_factor": 1})
	if err != nil {
		t.Fatal(err)
	}
	id := projectionPair(item, place)
	if err := validateSyncRecord("stock", id, "upsert", 1, 1, payload); err != nil {
		t.Fatal("canonical Unicode/control identity was rejected", err)
	}
	for _, badID := range []string{strings.Replace(id, "<", "\\u003c", 1), `[ "i\u0000<😀", "位置\n" ]`, projectionPair(item, "different"), ""} {
		if err := validateSyncRecord("stock", badID, "upsert", 1, 1, payload); !errors.Is(err, ErrDomainState) {
			t.Fatal("noncanonical or mismatched pair accepted", err)
		}
	}
	for _, test := range []struct {
		kind, id, payload string
	}{
		{"stock", projectionPair("item-1", "loc-1"), `{"item_id":"other","location_id":"loc-1","quantity_base":0,"unit_code":"pcs","base_unit":"pcs","scale_factor":1}`},
		{"item_tag", projectionPair("item-1", "tag-1"), `{"item_id":"other","tag_id":"tag-1"}`},
		{"search_doc", projectionPair("item", "item-1"), `{"entity_type":"unit","entity_id":"item-1","approved_text":""}`},
	} {
		if err := validateSyncRecord(test.kind, test.id, "upsert", 1, 1, []byte(test.payload)); !errors.Is(err, ErrDomainState) {
			t.Fatal("first composite identity component was not checked", test.kind, err)
		}
	}
	for _, test := range []struct {
		kind, id string
		payload  []byte
	}{
		{"unknown", "id", []byte(`{"id":"private-value"}`)},
		{"tag", strings.Repeat("a", 129), []byte(`{"id":"private-value","name":"x"}`)},
		{"tag", "private-value", []byte(`{"id":"private-value","id":"other","name":"x"}`)},
		{"search_doc", projectionPair("unit", "item-1"), []byte(`{"entity_type":"item","entity_id":"item-1","approved_text":"private-value"}`)},
	} {
		err := validateSyncRecord(test.kind, test.id, "upsert", 1, 1, test.payload)
		if !errors.Is(err, ErrDomainState) || strings.Contains(err.Error(), "private-value") {
			t.Fatal("invalid record was accepted or disclosed untrusted data", err)
		}
	}
}

func FuzzSyncRecordValidation(f *testing.F) {
	f.Add("tag", "a", "upsert", int64(1), int64(1), []byte(`{"id":"a","name":"x"}`))
	f.Add("tag", "a", "delete", int64(1), int64(1), []byte{})
	f.Add("stock", projectionPair("item", "loc"), "upsert", int64(1), int64(1), []byte(`{"item_id":"item","location_id":"loc","quantity_base":0,"unit_code":"pcs","base_unit":"pcs","scale_factor":1}`))
	f.Add("search_doc", projectionPair("item", "x"), "upsert", int64(1), int64(1), []byte(`{"entity_type":"item","entity_id":"x","approved_text":""}`))
	f.Fuzz(func(t *testing.T, kind, id, operation string, revision, version int64, payload []byte) {
		if len(kind) > 128 || len(id) > 2*128*6+7 || len(operation) > 128 || len(payload) > 128<<10 {
			return
		}
		original := bytes.Clone(payload)
		for _, err := range []error{
			validateSyncRecord(kind, id, operation, revision, version, payload),
			validateSyncRecord("tag", "a", "upsert", 1, 1, payload),
		} {
			if err != nil && !errors.Is(err, ErrDomainState) {
				t.Fatal("record validation disclosed a non-generic error", err)
			}
		}
		if !bytes.Equal(original, payload) {
			t.Fatal("record validation changed frozen bytes")
		}
	})
}
