package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

	"homebox/internal/store"
)

const maxJSONInteger int64 = 9007199254740991

func jsonString(raw json.RawMessage, minimum, maximum int) (*string, bool) {
	if len(raw) == 0 || raw[0] != '"' {
		return nil, false
	}
	var value string
	if json.Unmarshal(raw, &value) != nil || !boundedString(value, minimum, maximum) {
		return nil, false
	}
	return &value, true
}

func jsonParent(raw json.RawMessage) (store.NullableID, bool) {
	if bytes.Equal(raw, []byte("null")) {
		return store.NullableID{Set: true}, true
	}
	value, ok := jsonString(raw, 1, 128)
	return store.NullableID{Set: true, Value: value}, ok
}

func resourceRevision(w http.ResponseWriter, r *http.Request, requestID string, required bool) (int64, bool, bool) {
	values := r.Header.Values("If-Match")
	if len(values) == 0 {
		if required {
			writeError(w, http.StatusConflict, "PRECONDITION_REQUIRED", "Send the current quoted aggregate revision in If-Match.", requestID)
			return 0, false, false
		}
		return 0, false, true
	}
	invalid := func() (int64, bool, bool) {
		writeError(w, http.StatusBadRequest, "INVALID_REVISION", "If-Match must contain one quoted positive safe-integer revision.", requestID)
		return 0, true, false
	}
	if len(values) != 1 || len(values[0]) < 3 || len(values[0]) > 18 {
		return invalid()
	}
	text := values[0]
	if text[0] != '"' || text[len(text)-1] != '"' || text[1] == '0' {
		return invalid()
	}
	for _, character := range text[1 : len(text)-1] {
		if character < '0' || character > '9' {
			return invalid()
		}
	}
	revision, err := strconv.ParseInt(text[1:len(text)-1], 10, 64)
	if err != nil || revision < 1 || revision > maxJSONInteger {
		return invalid()
	}
	return revision, true, true
}

func metadataListQuery(w http.ResponseWriter, r *http.Request, requestID string) (string, int, bool) {
	invalid := func() (string, int, bool) {
		writeError(w, http.StatusBadRequest, "INVALID_QUERY", "Use one bounded limit and one opaque list cursor only.", requestID)
		return "", 0, false
	}
	if len(r.URL.RawQuery) > 2300 {
		return invalid()
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return invalid()
	}
	limit := 100
	cursor := ""
	for name, entries := range values {
		if len(entries) != 1 {
			return invalid()
		}
		switch name {
		case "limit":
			value, err := strconv.Atoi(entries[0])
			if err != nil || value < 1 || value > 100 {
				return invalid()
			}
			limit = value
		case "cursor":
			if len(entries[0]) == 0 || len(entries[0]) > 2048 {
				return invalid()
			}
			cursor = entries[0]
		default:
			return invalid()
		}
	}
	return cursor, limit, true
}

func reviewedParentProof(w http.ResponseWriter, requestID string, parent *string, supplied bool) bool {
	if parent != nil && !supplied {
		writeError(w, http.StatusConflict, "PRECONDITION_REQUIRED", "Child creation requires the current parent revision in If-Match.", requestID)
		return false
	}
	if parent == nil && supplied {
		writeError(w, http.StatusBadRequest, "INVALID_REVISION", "Root creation does not accept a parent If-Match.", requestID)
		return false
	}
	return true
}
