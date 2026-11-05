package httpapi

import (
	"encoding/json"
	"net/http"

	"homebox/internal/store"
)

const locationBodyLimit = 128 << 10

func locationValidation(w http.ResponseWriter, requestID string) bool {
	writeError(w, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "Location fields must use their documented types, lengths, and integer bounds.", requestID)
	return false
}

func readLocationFields(w http.ResponseWriter, r *http.Request, requestID string) (store.LocationPatch, bool) {
	var input store.LocationPatch
	values, ok := readJSONObject(w, r, requestID, locationBodyLimit, "parent_id", "name", "code", "kind", "sort_order", "status", "description")
	if !ok {
		return input, false
	}
	for _, field := range []struct {
		name        string
		min, max    int
		destination **string
	}{
		{"name", 1, 200, &input.Name}, {"code", 1, 100, &input.Code}, {"kind", 1, 32, &input.Kind},
		{"status", 1, 8, &input.Status}, {"description", 0, 10000, &input.Description},
	} {
		if raw, exists := values[field.name]; exists {
			value, ok := jsonString(raw, field.min, field.max)
			if !ok {
				return input, locationValidation(w, requestID)
			}
			*field.destination = value
		}
	}
	if input.Status != nil && *input.Status != "active" && *input.Status != "archived" {
		return input, locationValidation(w, requestID)
	}
	if raw, exists := values["parent_id"]; exists {
		parent, ok := jsonParent(raw)
		if !ok {
			return input, locationValidation(w, requestID)
		}
		input.ParentID = parent
	}
	if raw, exists := values["sort_order"]; exists {
		value, ok := exactSafeInteger(raw)
		if !ok {
			return input, locationValidation(w, requestID)
		}
		input.SortOrder = &value
	}
	return input, true
}

func readLocationMove(w http.ResponseWriter, r *http.Request, requestID string) (*string, bool) {
	values, ok := readJSONObject(w, r, requestID, locationBodyLimit, "target_parent_id")
	if !ok {
		return nil, false
	}
	raw, exists := values["target_parent_id"]
	if !exists {
		return nil, locationValidation(w, requestID)
	}
	parent, ok := jsonParent(raw)
	if !ok {
		return nil, locationValidation(w, requestID)
	}
	return parent.Value, true
}

func readLocationArchive(w http.ResponseWriter, r *http.Request, requestID string) (bool, string, bool) {
	values, ok := readJSONObject(w, r, requestID, locationBodyLimit, "archive_children", "reason")
	if !ok {
		return false, "", false
	}
	var children bool
	var reason string
	if raw, exists := values["archive_children"]; exists {
		if len(raw) == 0 || raw[0] != 't' && raw[0] != 'f' || json.Unmarshal(raw, &children) != nil {
			return false, "", locationValidation(w, requestID)
		}
	}
	if raw, exists := values["reason"]; exists {
		value, ok := jsonString(raw, 0, 10000)
		if !ok {
			return false, "", locationValidation(w, requestID)
		}
		reason = *value
	}
	return children, reason, true
}
