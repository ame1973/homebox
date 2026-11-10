package httpapi

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"homebox/internal/store"
)

func mediaReadRoute(path string) (route, id string) {
	const prefix = "/api/v1/media/"
	if !strings.HasPrefix(path, prefix) {
		return "", ""
	}
	parts := strings.Split(strings.TrimPrefix(path, prefix), "/")
	if len(parts) == 1 && parts[0] != "" {
		if parts[0] == "links" {
			return "owner", ""
		}
		return "resource", parts[0]
	}
	if len(parts) == 2 && parts[0] != "" {
		switch parts[1] {
		case "view":
			return "resource", parts[0]
		case "links":
			return "membership", parts[0]
		}
	}
	return "", ""
}

// Metadata and link pages are independent of the still-unimplemented image file
// services. Only these reads are registered; no bytes or writes are exposed.
func (a *authAPI) serveMediaReads(w http.ResponseWriter, r *http.Request, requestID string) bool {
	route, id := mediaReadRoute(r.URL.Path)
	if route == "" {
		return false
	}
	if !a.requireAdmin(w, r, requestID) {
		return true
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "The method is not supported for this route.", requestID)
		return true
	}
	if r.URL.RawPath != "" || strings.ContainsAny(id, "\\\x00") || id == "." || id == ".." || id != "" && !boundedString(id, 1, 128) ||
		route == "resource" && (r.URL.RawQuery != "" || r.URL.ForceQuery) {
		writeError(w, http.StatusBadRequest, "INVALID_MEDIA_URL", "Use an exact Media path and only documented list parameters.", requestID)
		return true
	}
	if !requireNoBody(w, r, requestID) {
		return true
	}
	token, _ := sessionToken(r)
	switch route {
	case "resource":
		image, err := a.media.Get(r.Context(), token, id)
		if err != nil {
			writeMediaError(w, err, requestID)
		} else {
			w.Header().Set("ETag", strconv.Quote(strconv.FormatInt(image.Revision, 10)))
			writeJSON(w, image)
		}
	case "membership":
		cursor, limit, ok := boundedMetadataQuery(w, r, requestID, 50, 16<<10)
		if !ok {
			return true
		}
		page, err := a.media.PageMediaLinks(r.Context(), token, id, cursor, limit)
		if err != nil {
			writeMediaError(w, err, requestID)
		} else {
			writeJSON(w, page)
		}
	case "owner":
		ownerType, ownerID, cursor, limit, ok := mediaOwnerQuery(w, r, requestID)
		if !ok {
			return true
		}
		page, err := a.media.PageOwnerLinks(r.Context(), token, ownerType, ownerID, cursor, limit)
		if err != nil {
			writeMediaError(w, err, requestID)
		} else {
			writeJSON(w, page)
		}
	}
	return true
}

func mediaOwnerQuery(w http.ResponseWriter, r *http.Request, requestID string) (string, string, string, int, bool) {
	invalid := func() (string, string, string, int, bool) {
		writeError(w, http.StatusBadRequest, "INVALID_QUERY", "Use one documented owner kind and ID, plus a bounded limit and cursor.", requestID)
		return "", "", "", 0, false
	}
	if len(r.URL.RawQuery) > 16<<10 {
		return invalid()
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return invalid()
	}
	ownerType, ownerID, cursor, limit := "", "", "", 50
	for name, entries := range values {
		if len(entries) != 1 {
			return invalid()
		}
		value := entries[0]
		switch name {
		case "owner_type":
			if value != "item" && value != "unit" && value != "location" {
				return invalid()
			}
			ownerType = value
		case "owner_id":
			if !boundedString(value, 1, 128) {
				return invalid()
			}
			ownerID = value
		case "cursor":
			if len(value) == 0 || len(value) > 2048 {
				return invalid()
			}
			cursor = value
		case "limit":
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed < 1 || parsed > 100 {
				return invalid()
			}
			limit = parsed
		default:
			return invalid()
		}
	}
	if ownerType == "" || ownerID == "" {
		return invalid()
	}
	return ownerType, ownerID, cursor, limit, true
}

func writeMediaError(w http.ResponseWriter, err error, requestID string) {
	status, code, message := http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "Media data is temporarily unavailable. Review the source and retry explicitly later."
	switch {
	case errors.Is(err, store.ErrAuthentication), errors.Is(err, store.ErrPasswordChangeRequired):
		writeAuthError(w, err, requestID)
		return
	case errors.Is(err, store.ErrDomainInput):
		status, code, message = http.StatusUnprocessableEntity, "VALIDATION_ERROR", "Media input is outside its allowed bounds."
	case errors.Is(err, store.ErrNotFound):
		status, code, message = http.StatusNotFound, "NOT_FOUND", "The Media or selected owner does not exist."
	case errors.Is(err, store.ErrInvalidCursor):
		status, code, message = http.StatusBadRequest, "INVALID_CURSOR", "Refresh the collection; its continuation is invalid, expired or differently scoped."
	}
	writeError(w, status, code, message, requestID)
}
