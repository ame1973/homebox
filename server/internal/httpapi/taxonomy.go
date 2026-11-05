package httpapi

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"homebox/internal/store"
)

func taxonomyRoute(path string) (kind, id, allow string) {
	for _, collection := range []string{"categories", "tags"} {
		prefix := "/api/v1/" + collection
		if path == prefix {
			return collection, "", "GET, POST"
		}
		if strings.HasPrefix(path, prefix+"/") {
			id := strings.TrimPrefix(path, prefix+"/")
			if id != "" && !strings.Contains(id, "/") {
				return collection, id, "PATCH, DELETE"
			}
		}
	}
	return "", "", ""
}

// Keep this active-session gate within domain dispatch, not in front of the
// separate revoked-cookie acknowledgement replay paths for authentication.
func (a *authAPI) serveTaxonomy(w http.ResponseWriter, r *http.Request, requestID string) bool {
	kind, id, allow := taxonomyRoute(r.URL.Path)
	if kind == "" {
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
	isList := id == "" && r.Method == http.MethodGet
	if r.URL.RawPath != "" || strings.ContainsAny(id, "\\\x00") || id == "." || id == ".." || id != "" && !boundedString(id, 1, 128) ||
		!isList && (r.URL.RawQuery != "" || r.URL.ForceQuery) {
		writeError(w, http.StatusBadRequest, "INVALID_TAXONOMY_URL", "Use an exact taxonomy path and only documented list parameters.", requestID)
		return true
	}
	token, _ := sessionToken(r) // requireAdmin already verified the header boundary.
	if isList {
		if !requireNoBody(w, r, requestID) {
			return true
		}
		cursor, limit, ok := metadataListQuery(w, r, requestID)
		if !ok {
			return true
		}
		var result any
		var err error
		if kind == "categories" {
			result, err = a.taxonomy.PageCategories(r.Context(), token, cursor, limit)
		} else {
			result, err = a.taxonomy.PageTags(r.Context(), token, cursor, limit)
		}
		if err != nil {
			writeTaxonomyError(w, err, requestID)
		} else {
			writeJSON(w, result)
		}
		return true
	}
	revision, supplied, ok := resourceRevision(w, r, requestID, id != "")
	if !ok {
		return true
	}
	key := r.Header.Get("Idempotency-Key")
	if r.Method == http.MethodDelete {
		if !requireNoBody(w, r, requestID) {
			return true
		}
		var ack store.ActionAck
		var err error
		if kind == "categories" {
			ack, err = a.taxonomy.DeleteCategory(r.Context(), token, key, id, revision)
		} else {
			ack, err = a.taxonomy.DeleteTag(r.Context(), token, key, id, revision)
		}
		if err != nil {
			writeTaxonomyError(w, err, requestID)
		} else {
			writeJSON(w, ack)
		}
		return true
	}
	if kind == "categories" {
		input, ok := readCategoryFields(w, r, requestID)
		if !ok {
			return true
		}
		var category store.Category
		var err error
		if id == "" {
			if input.Name == nil {
				taxonomyValidation(w, requestID)
				return true
			}
			if !reviewedParentProof(w, requestID, input.ParentID.Value, supplied) {
				return true
			}
			category, err = a.taxonomy.CreateCategory(r.Context(), token, key, revision, store.CategoryCreate{Name: *input.Name, ParentID: input.ParentID.Value})
		} else {
			category, err = a.taxonomy.PatchCategory(r.Context(), token, key, id, revision, input)
		}
		writeTaxonomyResult(w, category, category.Revision, err, requestID)
		return true
	}
	if id == "" && supplied {
		writeError(w, http.StatusBadRequest, "INVALID_REVISION", "Tag creation does not accept an unrelated If-Match.", requestID)
		return true
	}
	name, ok := readTagName(w, r, requestID)
	if !ok {
		return true
	}
	var tag store.Tag
	var err error
	if id == "" {
		tag, err = a.taxonomy.CreateTag(r.Context(), token, key, name)
	} else {
		tag, err = a.taxonomy.PatchTag(r.Context(), token, key, id, revision, name)
	}
	writeTaxonomyResult(w, tag, tag.Revision, err, requestID)
	return true
}

func writeTaxonomyResult(w http.ResponseWriter, value any, revision int64, err error, requestID string) {
	if err != nil {
		writeTaxonomyError(w, err, requestID)
		return
	}
	w.Header().Set("ETag", strconv.Quote(strconv.FormatInt(revision, 10)))
	writeJSON(w, value)
}

func writeTaxonomyError(w http.ResponseWriter, err error, requestID string) {
	status, code, message := http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "Taxonomy data is temporarily unavailable. Retry explicitly later."
	switch {
	case errors.Is(err, store.ErrAuthentication), errors.Is(err, store.ErrPasswordChangeRequired), errors.Is(err, store.ErrIdempotencyConflict):
		writeAuthError(w, err, requestID)
		return
	case errors.Is(err, store.ErrDomainInput):
		taxonomyValidation(w, requestID)
		return
	case errors.Is(err, store.ErrNotFound):
		status, code, message = http.StatusNotFound, "NOT_FOUND", "The category, parent, or tag does not exist."
	case errors.Is(err, store.ErrInvalidCursor):
		status, code, message = http.StatusBadRequest, "INVALID_CURSOR", "Refresh the list; its continuation is invalid, expired, or differently scoped."
	case errors.Is(err, store.ErrPreconditionRequired):
		status, code, message = http.StatusConflict, "PRECONDITION_REQUIRED", "Refresh the reviewed taxonomy aggregate and send its current revision."
	case errors.Is(err, store.ErrRevisionConflict):
		status, code, message = http.StatusConflict, "REVISION_CONFLICT", "The taxonomy aggregate or membership changed. Refresh and review before submitting again."
	case errors.Is(err, store.ErrIdempotencyExpired):
		status, code, message = http.StatusConflict, "IDEMPOTENCY_EXPIRED", "The original acknowledgement expired. Review current state before using a new key."
	case errors.Is(err, store.ErrCategoryCycle):
		status, code, message = http.StatusConflict, "CATEGORY_CYCLE", "A category cannot contain itself or a descendant."
	case errors.Is(err, store.ErrCategoryChildren):
		status, code, message = http.StatusConflict, "CATEGORY_HAS_CHILDREN", "Move or delete child categories explicitly before deleting this category."
	case errors.Is(err, store.ErrTagName):
		status, code, message = http.StatusConflict, "TAG_NAME_CONFLICT", "This exact tag name is already in use."
	}
	writeError(w, status, code, message, requestID)
}
