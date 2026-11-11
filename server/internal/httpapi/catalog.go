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

func catalogRoute(path string) (route, id, allow string) {
	const prefix = "/api/v1/items"
	if path == "/api/v1/items-by-identity" {
		return "identity", "", http.MethodGet
	}
	if path == prefix {
		return "collection", "", "GET, POST"
	}
	if !strings.HasPrefix(path, prefix+"/") {
		return "", "", ""
	}
	parts := strings.Split(strings.TrimPrefix(path, prefix+"/"), "/")
	if len(parts) == 1 && parts[0] != "" {
		return "resource", parts[0], "GET, PATCH, DELETE"
	}
	if len(parts) == 2 && parts[0] != "" && parts[1] == "archive" {
		return "archive", parts[0], http.MethodPost
	}
	return "", "", ""
}

// Keep this active-session gate within domain dispatch, not before the separate
// revoked-cookie authentication acknowledgement paths.
func (a *authAPI) serveCatalog(w http.ResponseWriter, r *http.Request, requestID string) bool {
	route, id, allow := catalogRoute(r.URL.Path)
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
	isList := route == "collection" && r.Method == http.MethodGet
	isIdentity := route == "identity" && r.Method == http.MethodGet
	if r.URL.RawPath != "" || strings.ContainsAny(id, "\\\x00") || id == "." || id == ".." || id != "" && !boundedString(id, 1, 128) ||
		!isList && !isIdentity && (r.URL.RawQuery != "" || r.URL.ForceQuery) {
		writeError(w, http.StatusBadRequest, "INVALID_CATALOG_URL", "Use an exact catalog path and only documented list parameters.", requestID)
		return true
	}
	token, _ := sessionToken(r) // requireAdmin verified the transport/header/session boundary.
	if isList {
		if !requireNoBody(w, r, requestID) {
			return true
		}
		cursor, limit, filters, ok := catalogListQuery(w, r, requestID)
		if !ok {
			return true
		}
		page, err := a.catalog.Page(r.Context(), token, cursor, limit, filters)
		if errors.Is(err, store.ErrDomainInput) {
			catalogQueryError(w, requestID)
		} else if err != nil {
			writeCatalogError(w, err, requestID)
		} else {
			writeJSON(w, page)
		}
		return true
	}
	if r.Method == http.MethodGet {
		if !requireNoBody(w, r, requestID) {
			return true
		}
		if isIdentity {
			var ok bool
			id, ok = exactIdentityQuery(r)
			if !ok {
				writeError(w, http.StatusBadRequest, "INVALID_QUERY", "Use one exact Item identity in the id query parameter.", requestID)
				return true
			}
		}
		item, err := a.catalog.Get(r.Context(), token, id)
		writeCatalogResult(w, item, err, requestID)
		return true
	}
	revision, supplied, ok := resourceRevision(w, r, requestID, route != "collection")
	if !ok {
		return true
	}
	key := r.Header.Get("Idempotency-Key")
	switch {
	case route == "collection":
		if supplied {
			writeError(w, http.StatusBadRequest, "INVALID_REVISION", "Catalog creation does not accept an unrelated If-Match.", requestID)
			return true
		}
		input, ok := readItemCreate(w, r, requestID)
		if !ok {
			return true
		}
		item, err := a.catalog.Create(r.Context(), token, key, input)
		writeCatalogResult(w, item, err, requestID)
	case route == "archive":
		reason, ok := readItemArchive(w, r, requestID)
		if !ok {
			return true
		}
		item, err := a.catalog.Archive(r.Context(), token, key, id, revision, reason)
		writeCatalogResult(w, item, err, requestID)
	case r.Method == http.MethodPatch:
		input, ok := readItemFields(w, r, requestID)
		if !ok {
			return true
		}
		item, err := a.catalog.Patch(r.Context(), token, key, id, revision, input)
		writeCatalogResult(w, item, err, requestID)
	case r.Method == http.MethodDelete:
		if !requireNoBody(w, r, requestID) {
			return true
		}
		ack, err := a.catalog.Delete(r.Context(), token, key, id, revision)
		if err != nil {
			writeCatalogError(w, err, requestID)
		} else {
			writeJSON(w, ack)
		}
	}
	return true
}

func catalogQueryError(w http.ResponseWriter, requestID string) {
	writeError(w, http.StatusBadRequest, "INVALID_QUERY", "Use one value per documented catalog filter, valid civil bounds, and a bounded page size/cursor.", requestID)
}

func catalogListQuery(w http.ResponseWriter, r *http.Request, requestID string) (string, int, store.ItemFilters, bool) {
	var filters store.ItemFilters
	invalid := func() (string, int, store.ItemFilters, bool) {
		catalogQueryError(w, requestID)
		return "", 0, store.ItemFilters{}, false
	}
	if len(r.URL.RawQuery) > 16<<10 {
		return invalid()
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return invalid()
	}
	fields := map[string]struct {
		destination *string
		min, max    int
	}{
		"from": {&filters.From, 10, 10}, "to": {&filters.To, 10, 10}, "currency": {&filters.Currency, 3, 3},
		"category_id": {&filters.CategoryID, 1, 128}, "location_id": {&filters.LocationID, 1, 128}, "scope": {&filters.Scope, 1, 7},
		"status": {&filters.Status, 1, 8}, "merchant": {&filters.Merchant, 1, 300}, "q": {&filters.Query, 0, 500},
	}
	cursor, limit := "", 50
	for name, entries := range values {
		if len(entries) != 1 {
			return invalid()
		}
		value := entries[0]
		switch name {
		case "limit":
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed < 1 || parsed > 100 {
				return invalid()
			}
			limit = parsed
		case "cursor":
			if !boundedString(value, 1, 2048) {
				return invalid()
			}
			cursor = value
		default:
			field, known := fields[name]
			if !known || !boundedString(value, field.min, field.max) {
				return invalid()
			}
			*field.destination = value
		}
	}
	return cursor, limit, filters, true
}

func writeCatalogResult(w http.ResponseWriter, item store.Item, err error, requestID string) {
	if err != nil {
		writeCatalogError(w, err, requestID)
		return
	}
	w.Header().Set("ETag", strconv.Quote(strconv.FormatInt(item.Revision, 10)))
	writeJSON(w, item)
}

func writeCatalogError(w http.ResponseWriter, err error, requestID string) {
	status, code, message := http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "Catalog data is temporarily unavailable. Retry explicitly later."
	switch {
	case errors.Is(err, store.ErrAuthentication), errors.Is(err, store.ErrPasswordChangeRequired), errors.Is(err, store.ErrIdempotencyConflict):
		writeAuthError(w, err, requestID)
		return
	case errors.Is(err, store.ErrDomainInput):
		catalogValidation(w, requestID)
		return
	case errors.Is(err, store.ErrNotFound):
		status, code, message = http.StatusNotFound, "NOT_FOUND", "The item or referenced catalog resource does not exist."
	case errors.Is(err, store.ErrInvalidCursor):
		status, code, message = http.StatusBadRequest, "INVALID_CURSOR", "Refresh the list; its continuation is invalid, expired, or differently scoped."
	case errors.Is(err, store.ErrPreconditionRequired):
		status, code, message = http.StatusConflict, "PRECONDITION_REQUIRED", "Refresh the reviewed resource and send its original revision."
	case errors.Is(err, store.ErrRevisionConflict):
		status, code, message = http.StatusConflict, "REVISION_CONFLICT", "The reviewed resource or owning membership changed. Refresh and review before submitting again."
	case errors.Is(err, store.ErrIdempotencyExpired):
		status, code, message = http.StatusConflict, "IDEMPOTENCY_EXPIRED", "The original acknowledgement expired. Review current state before using a new key."
	case errors.Is(err, store.ErrBarcodeDuplicate):
		status, code, message = http.StatusConflict, "BARCODE_CONFLICT", "This product barcode is already linked to the item under the same symbology."
	case errors.Is(err, store.ErrAliasDuplicate):
		status, code, message = http.StatusConflict, "ALIAS_CONFLICT", "This exact alias is already linked to the item."
	case errors.Is(err, store.ErrItemHistory):
		status, code, message = http.StatusConflict, "ITEM_HAS_HISTORY", "Retained units, balances, purchases or movements prevent deletion or measurement changes; preserve their meaning and archive metadata instead."
	}
	writeError(w, status, code, message, requestID)
}
