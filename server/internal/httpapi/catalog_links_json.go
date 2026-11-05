package httpapi

import (
	"encoding/json"
	"net/http"

	"homebox/internal/store"
)

const catalogLinkBodyLimit = 16 << 10

func readBarcodeWrite(w http.ResponseWriter, r *http.Request, requestID string) (store.BarcodeWrite, bool) {
	values, ok := readJSONObject(w, r, requestID, catalogLinkBodyLimit, "symbology", "raw_value")
	if !ok {
		return store.BarcodeWrite{}, false
	}
	symbology, validSymbology := jsonString(values["symbology"], 1, 8)
	raw, validRaw := jsonString(values["raw_value"], 1, 512)
	if !validSymbology || !validRaw {
		return store.BarcodeWrite{}, catalogValidation(w, requestID)
	}
	// The authoritative service, not JSON width alone, checks real GTIN digits.
	return store.BarcodeWrite{Symbology: *symbology, RawValue: *raw}, true
}

func readAliasWrite(w http.ResponseWriter, r *http.Request, requestID string) (string, bool) {
	values, ok := readJSONObject(w, r, requestID, catalogLinkBodyLimit, "alias")
	if !ok {
		return "", false
	}
	value, ok := jsonString(values["alias"], 1, 300)
	if !ok {
		return "", catalogValidation(w, requestID)
	}
	return *value, true
}

func readAttributeFields(w http.ResponseWriter, r *http.Request, requestID string, create bool) (store.AttributePatch, bool) {
	values, ok := readJSONObject(w, r, requestID, catalogBodyLimit, "attribute_key", "attribute_value")
	if !ok {
		return store.AttributePatch{}, false
	}
	if len(values) == 0 || create && len(values) != 2 {
		return store.AttributePatch{}, catalogValidation(w, requestID)
	}
	var input store.AttributePatch
	for _, field := range []struct {
		name        string
		min, max    int
		destination **string
	}{{"attribute_key", 1, 100, &input.AttributeKey}, {"attribute_value", 0, 10000, &input.AttributeValue}} {
		if raw, present := values[field.name]; present {
			value, ok := jsonString(raw, field.min, field.max)
			if !ok {
				return store.AttributePatch{}, catalogValidation(w, requestID)
			}
			*field.destination = value
		}
	}
	return input, true
}

func readItemTags(w http.ResponseWriter, r *http.Request, requestID string) ([]string, bool) {
	values, ok := readJSONObject(w, r, requestID, catalogBodyLimit, "tag_ids")
	if !ok {
		return nil, false
	}
	raw := values["tag_ids"]
	var entries []json.RawMessage
	if len(raw) == 0 || raw[0] != '[' || json.Unmarshal(raw, &entries) != nil || len(entries) > 100 {
		return nil, catalogValidation(w, requestID)
	}
	ids := make([]string, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		id, ok := jsonString(entry, 1, 128)
		if !ok || seen[*id] {
			return nil, catalogValidation(w, requestID)
		}
		seen[*id] = true
		ids = append(ids, *id)
	}
	return ids, true
}
