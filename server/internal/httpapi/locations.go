package httpapi

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"homebox/internal/store"
)

func locationRoute(path string) (route, id, allow string) {
	switch path {
	case "/api/v1/locations":
		return "create", "", http.MethodPost
	case "/api/v1/locations/tree":
		return "tree", "", http.MethodGet
	}
	const prefix = "/api/v1/locations/"
	if !strings.HasPrefix(path, prefix) {
		return "", "", ""
	}
	parts := strings.Split(strings.TrimPrefix(path, prefix), "/")
	if len(parts) == 1 && parts[0] != "" {
		return "resource", parts[0], "GET, PATCH, DELETE"
	}
	if len(parts) == 2 && parts[0] != "" && (parts[1] == "move" || parts[1] == "archive") {
		return parts[1], parts[0], http.MethodPost
	}
	return "", "", ""
}

// serveLocations shares the configured transport/admission boundary, but keeps
// auth acknowledgement replay outside this domain-only active-session gate.
func (a *authAPI) serveLocations(w http.ResponseWriter, r *http.Request, requestID string) bool {
	route, id, allow := locationRoute(r.URL.Path)
	if route == "" {
		return false
	}
	if !a.requireAdmin(w, r, requestID) {
		return true
	}
	if !slices.Contains(strings.Split(allow, ", "), r.Method) {
		w.Header().Set("Allow", allow)
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "The method is not supported for this route.", requestID)
		return true
	}
	if r.URL.RawPath != "" || strings.ContainsAny(id, "\\\x00") || id == "." || id == ".." || id != "" && !boundedString(id, 1, 128) ||
		route != "tree" && (r.URL.RawQuery != "" || r.URL.ForceQuery) {
		writeError(w, http.StatusBadRequest, "INVALID_LOCATION_URL", "Use an exact location path and only documented list parameters.", requestID)
		return true
	}
	token, _ := sessionToken(r) // requireAdmin already verified the header boundary.
	if route == "tree" {
		if !requireNoBody(w, r, requestID) {
			return true
		}
		cursor, limit, ok := locationListQuery(w, r, requestID)
		if !ok {
			return true
		}
		result, err := a.locations.Page(r.Context(), token, cursor, limit)
		if err != nil {
			writeLocationError(w, err, requestID)
			return true
		}
		writeJSON(w, result)
		return true
	}
	if r.Method == http.MethodGet {
		if !requireNoBody(w, r, requestID) {
			return true
		}
		location, err := a.locations.Get(r.Context(), token, id)
		writeLocationResult(w, location, err, requestID)
		return true
	}
	revision, supplied, ok := locationRevision(w, r, requestID, route != "create")
	if !ok {
		return true
	}
	key := r.Header.Get("Idempotency-Key")
	switch {
	case route == "create":
		fields, ok := readLocationFields(w, r, requestID)
		if !ok {
			return true
		}
		if fields.Name == nil || fields.Code == nil {
			locationValidation(w, requestID)
			return true
		}
		if fields.ParentID.Value != nil && !supplied {
			writeError(w, http.StatusConflict, "PRECONDITION_REQUIRED", "Child creation requires the current parent revision in If-Match.", requestID)
			return true
		}
		if fields.ParentID.Value == nil && supplied {
			writeError(w, http.StatusBadRequest, "INVALID_REVISION", "Root creation does not accept a parent If-Match.", requestID)
			return true
		}
		input := store.LocationCreate{ParentID: fields.ParentID.Value, Name: *fields.Name, Code: *fields.Code, Kind: fields.Kind, Status: fields.Status}
		if fields.SortOrder != nil {
			input.SortOrder = *fields.SortOrder
		}
		if fields.Description != nil {
			input.Description = *fields.Description
		}
		location, err := a.locations.Create(r.Context(), token, key, revision, input)
		writeLocationResult(w, location, err, requestID)
	case route == "move":
		parent, ok := readLocationMove(w, r, requestID)
		if !ok {
			return true
		}
		location, err := a.locations.Move(r.Context(), token, key, id, revision, parent)
		writeLocationResult(w, location, err, requestID)
	case route == "archive":
		children, reason, ok := readLocationArchive(w, r, requestID)
		if !ok {
			return true
		}
		location, err := a.locations.Archive(r.Context(), token, key, id, revision, children, reason)
		writeLocationResult(w, location, err, requestID)
	case r.Method == http.MethodPatch:
		input, ok := readLocationFields(w, r, requestID)
		if !ok {
			return true
		}
		location, err := a.locations.Patch(r.Context(), token, key, id, revision, input)
		writeLocationResult(w, location, err, requestID)
	case r.Method == http.MethodDelete:
		if !requireNoBody(w, r, requestID) {
			return true
		}
		ack, err := a.locations.Delete(r.Context(), token, key, id, revision)
		if err != nil {
			writeLocationError(w, err, requestID)
			return true
		}
		writeJSON(w, ack)
	}
	return true
}

func locationRevision(w http.ResponseWriter, r *http.Request, requestID string, required bool) (int64, bool, bool) {
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

func locationListQuery(w http.ResponseWriter, r *http.Request, requestID string) (string, int, bool) {
	invalid := func() (string, int, bool) {
		writeError(w, http.StatusBadRequest, "INVALID_QUERY", "Use one bounded limit and one opaque location cursor only.", requestID)
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

func writeLocationResult(w http.ResponseWriter, location store.Location, err error, requestID string) {
	if err != nil {
		writeLocationError(w, err, requestID)
		return
	}
	w.Header().Set("ETag", strconv.Quote(strconv.FormatInt(location.Revision, 10)))
	writeJSON(w, location)
}

func writeLocationError(w http.ResponseWriter, err error, requestID string) {
	status, code, message := http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "Location data is temporarily unavailable. Retry explicitly later."
	switch {
	case errors.Is(err, store.ErrAuthentication), errors.Is(err, store.ErrPasswordChangeRequired), errors.Is(err, store.ErrIdempotencyConflict):
		writeAuthError(w, err, requestID)
		return
	case errors.Is(err, store.ErrDomainInput):
		locationValidation(w, requestID)
		return
	case errors.Is(err, store.ErrNotFound):
		status, code, message = http.StatusNotFound, "NOT_FOUND", "The location does not exist."
	case errors.Is(err, store.ErrInvalidCursor):
		status, code, message = http.StatusBadRequest, "INVALID_CURSOR", "Refresh the list; its continuation is invalid, expired, or differently scoped."
	case errors.Is(err, store.ErrPreconditionRequired):
		status, code, message = http.StatusConflict, "PRECONDITION_REQUIRED", "Refresh the location and send its current revision."
	case errors.Is(err, store.ErrRevisionConflict):
		status, code, message = http.StatusConflict, "REVISION_CONFLICT", "The location or its subtree changed. Refresh and review before submitting again."
	case errors.Is(err, store.ErrIdempotencyExpired):
		status, code, message = http.StatusConflict, "IDEMPOTENCY_EXPIRED", "The original acknowledgement expired. Review current state before using a new key."
	case errors.Is(err, store.ErrLocationCode):
		status, code, message = http.StatusConflict, "LOCATION_CODE_CONFLICT", "This location code is already reserved."
	case errors.Is(err, store.ErrLocationCycle):
		status, code, message = http.StatusConflict, "LOCATION_CYCLE", "A location cannot contain itself or one of its ancestors."
	case errors.Is(err, store.ErrLocationArchived):
		status, code, message = http.StatusConflict, "LOCATION_ARCHIVED", "The destination or an ancestor is archived. Choose an active destination."
	case errors.Is(err, store.ErrLocationChildren):
		status, code, message = http.StatusConflict, "LOCATION_HAS_CHILDREN", "Review the location's children before archiving or deleting it."
	case errors.Is(err, store.ErrLocationInUse):
		status, code, message = http.StatusConflict, "LOCATION_IN_USE", "Move or resolve retained inventory before archiving; archive locations with historical references instead of deleting them."
	}
	writeError(w, status, code, message, requestID)
}
