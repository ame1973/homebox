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

func mediaReadRoute(path string) (route, id string) {
	switch path {
	case "/api/v1/media":
		return "global", ""
	case "/api/v1/media-by-identity":
		return "identity-resource", ""
	case "/api/v1/media-by-identity/links":
		return "identity-membership", ""
	}
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

// The same two-segment path can name a Media read and a link write. In
// particular, /media/links/links names Media membership for GET/POST and the
// independent link ID "links" for PATCH/DELETE.
func mediaLinkRoute(path string) (string, bool) {
	const prefix = "/api/v1/media/links/"
	if !strings.HasPrefix(path, prefix) {
		return "", false
	}
	id := strings.TrimPrefix(path, prefix)
	return id, id != "" && !strings.Contains(id, "/")
}

// Metadata and link pages do not expose image bytes. Attachment, edits and
// unlinking change reviewed metadata only; uploads and file storage remain separate.
func (a *authAPI) serveMediaReads(w http.ResponseWriter, r *http.Request, requestID string) bool {
	route, id := mediaReadRoute(r.URL.Path)
	// POST /media is the separately contracted upload, which is not implemented.
	if route == "global" && r.Method == http.MethodPost {
		return false
	}
	linkID, linkRoute := mediaLinkRoute(r.URL.Path)
	if route == "" && (!linkRoute || r.Method == http.MethodGet) {
		return false
	}
	if !a.requireAdmin(w, r, requestID) {
		return true
	}
	var methods []string
	if route != "" {
		methods = append(methods, http.MethodGet)
	}
	if route == "membership" || route == "identity-membership" {
		methods = append(methods, http.MethodPost)
	}
	if linkRoute || route == "owner" {
		methods = append(methods, http.MethodPatch, http.MethodDelete)
	}
	if !slices.Contains(methods, r.Method) {
		w.Header().Set("Allow", strings.Join(methods, ", "))
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "The method is not supported for this route.", requestID)
		return true
	}
	queryLink := route == "owner" && (r.Method == http.MethodPatch || r.Method == http.MethodDelete)
	queryMedia := route == "identity-resource" || route == "identity-membership"
	if !queryLink && (r.Method == http.MethodPatch || r.Method == http.MethodDelete) {
		id = linkID
	}
	isList := r.Method == http.MethodGet && route != "resource" && route != "identity-resource"
	if r.URL.RawPath != "" || strings.ContainsAny(id, "\\\x00") || id == "." || id == ".." || id != "" && !boundedString(id, 1, 128) ||
		!isList && !queryLink && !queryMedia && (r.URL.RawQuery != "" || r.URL.ForceQuery) {
		writeError(w, http.StatusBadRequest, "INVALID_MEDIA_URL", "Use an exact Media path and only documented parameters.", requestID)
		return true
	}
	if queryLink || queryMedia && (route == "identity-resource" || r.Method == http.MethodPost) {
		var ok bool
		id, ok = exactIdentityQuery(r)
		if !ok {
			message := "Use one exact Media identity in the id query parameter."
			if queryLink {
				message = "Use one exact MediaLink identity in the id query parameter."
			}
			writeError(w, http.StatusBadRequest, "INVALID_QUERY", message, requestID)
			return true
		}
	}
	token, _ := sessionToken(r)
	if r.Method == http.MethodPost || r.Method == http.MethodPatch || r.Method == http.MethodDelete {
		revision, _, ok := resourceRevision(w, r, requestID, true)
		if !ok {
			return true
		}
		if r.Method == http.MethodDelete {
			if !requireNoBody(w, r, requestID) {
				return true
			}
			ack, err := a.media.Unlink(r.Context(), token, r.Header.Get("Idempotency-Key"), id, revision)
			if err != nil {
				writeMediaError(w, err, requestID)
			} else {
				writeJSON(w, ack)
			}
			return true
		}
		var value store.MediaLink
		var err error
		if r.Method == http.MethodPost {
			input, ok := readMediaLinkCreate(w, r, requestID)
			if !ok {
				return true
			}
			value, err = a.media.Attach(r.Context(), token, r.Header.Get("Idempotency-Key"), id, revision, input)
		} else {
			input, ok := readMediaLinkPatch(w, r, requestID)
			if !ok {
				return true
			}
			value, err = a.media.PatchLink(r.Context(), token, r.Header.Get("Idempotency-Key"), id, revision, input)
		}
		if err != nil {
			writeMediaError(w, err, requestID)
		} else {
			w.Header().Set("ETag", strconv.Quote(strconv.FormatInt(value.Revision, 10)))
			writeJSON(w, value)
		}
		return true
	}
	if !requireNoBody(w, r, requestID) {
		return true
	}
	switch route {
	case "global":
		cursor, limit, ok := boundedMetadataQuery(w, r, requestID, 50, 16<<10)
		if !ok {
			return true
		}
		page, err := a.media.Page(r.Context(), token, cursor, limit)
		if err != nil {
			writeMediaError(w, err, requestID)
		} else {
			writeJSON(w, page)
		}
	case "resource", "identity-resource":
		image, err := a.media.Get(r.Context(), token, id)
		if err != nil {
			writeMediaError(w, err, requestID)
		} else {
			w.Header().Set("ETag", strconv.Quote(strconv.FormatInt(image.Revision, 10)))
			writeJSON(w, image)
		}
	case "membership", "identity-membership":
		var cursor string
		var limit int
		var ok bool
		if route == "identity-membership" {
			id, cursor, limit, ok = mediaIdentityPageQuery(w, r, requestID)
		} else {
			cursor, limit, ok = boundedMetadataQuery(w, r, requestID, 50, 16<<10)
		}
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

func mediaIdentityPageQuery(w http.ResponseWriter, r *http.Request, requestID string) (string, string, int, bool) {
	invalid := func() (string, string, int, bool) {
		writeError(w, http.StatusBadRequest, "INVALID_QUERY", "Use one exact Media identity and a bounded limit and cursor.", requestID)
		return "", "", 0, false
	}
	if len(r.URL.RawQuery) > 16<<10 {
		return invalid()
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(values["id"]) != 1 || !boundedString(values.Get("id"), 1, 128) {
		return invalid()
	}
	id := values.Get("id")
	delete(values, "id")
	cursor, limit, ok := metadataQueryValues(values, 50)
	if !ok {
		return invalid()
	}
	return id, cursor, limit, true
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
	case errors.Is(err, store.ErrAuthentication), errors.Is(err, store.ErrPasswordChangeRequired), errors.Is(err, store.ErrIdempotencyConflict):
		writeAuthError(w, err, requestID)
		return
	case errors.Is(err, store.ErrDomainInput):
		status, code, message = http.StatusUnprocessableEntity, "VALIDATION_ERROR", "Media input is outside its allowed bounds."
	case errors.Is(err, store.ErrNotFound):
		status, code, message = http.StatusNotFound, "NOT_FOUND", "The Media, link or selected owner does not exist."
	case errors.Is(err, store.ErrInvalidCursor):
		status, code, message = http.StatusBadRequest, "INVALID_CURSOR", "Refresh the collection; its continuation is invalid, expired or differently scoped."
	case errors.Is(err, store.ErrPreconditionRequired):
		status, code, message = http.StatusConflict, "PRECONDITION_REQUIRED", "Send the original reviewed Media, owner or link proofs required for this action."
	case errors.Is(err, store.ErrRevisionConflict):
		status, code, message = http.StatusConflict, "REVISION_CONFLICT", "The reviewed Media, link or owner changed. Refresh and review before a new command."
	case errors.Is(err, store.ErrIdempotencyExpired):
		status, code, message = http.StatusConflict, "IDEMPOTENCY_EXPIRED", "The original acknowledgement expired. Review current source before using a new key."
	case errors.Is(err, store.ErrMediaCoverConflict):
		status, code, message = http.StatusConflict, "MEDIA_COVER_CONFLICT", "Another current link already claims this owner's cover."
	}
	writeError(w, status, code, message, requestID)
}
