package httpapi

import (
	"net/http"

	"homebox/internal/store"
)

const taxonomyBodyLimit = 16 << 10

func taxonomyValidation(w http.ResponseWriter, requestID string) bool {
	writeError(w, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "Taxonomy fields must use their documented names, types, and Unicode bounds.", requestID)
	return false
}

func readCategoryFields(w http.ResponseWriter, r *http.Request, requestID string) (store.CategoryPatch, bool) {
	var input store.CategoryPatch
	values, ok := readJSONObject(w, r, requestID, taxonomyBodyLimit, "name", "parent_id")
	if !ok {
		return input, false
	}
	if len(values) == 0 {
		return input, taxonomyValidation(w, requestID)
	}
	if raw, exists := values["name"]; exists {
		value, ok := jsonString(raw, 1, 200)
		if !ok {
			return input, taxonomyValidation(w, requestID)
		}
		input.Name = value
	}
	if raw, exists := values["parent_id"]; exists {
		parent, ok := jsonParent(raw)
		if !ok {
			return input, taxonomyValidation(w, requestID)
		}
		input.ParentID = parent
	}
	return input, true
}

func readTagName(w http.ResponseWriter, r *http.Request, requestID string) (string, bool) {
	values, ok := readJSONObject(w, r, requestID, taxonomyBodyLimit, "name")
	if !ok {
		return "", false
	}
	value, ok := jsonString(values["name"], 1, 200)
	if !ok {
		return "", taxonomyValidation(w, requestID)
	}
	return *value, true
}
